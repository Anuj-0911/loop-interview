package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

func storeWithHold() *holdStoreFake {
	store := newBookingStoreFake()
	store.holds = []Hold{{ID: "hold-1", SlotID: "slot-1", UserID: "user-1", Status: HoldActive, ExpiresAt: testNow.Add(30 * time.Second)}}
	return store
}

func TestConfirmHoldCreatesBookingAndConfirmsHold(t *testing.T) {
	store := storeWithHold()
	ctx := context.WithValue(context.Background(), contextKey{}, "confirmation")
	got, err := New(store).ConfirmHold(ctx, "hold-1", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	want := Booking{ID: "booking-1", HoldID: "hold-1", SlotID: "slot-1", UserID: "user-1", Status: BookingConfirmed}
	if got != want {
		t.Fatalf("booking = %+v, want %+v", got, want)
	}
	if len(store.bookings) != 1 || store.bookings[0] != want || store.holds[0].Status != HoldConfirmed {
		t.Fatalf("confirmation not persisted: holds=%+v, bookings=%+v", store.holds, store.bookings)
	}
	if store.ctx != ctx {
		t.Error("request context was not propagated")
	}
	if _, err := New(store).CreateHold(ctx, "slot-1", "user-2"); !errors.Is(err, ErrSlotUnavailable) {
		t.Fatalf("confirmed slot can be held: %v", err)
	}
}

func TestConfirmHoldRetriesReturnOriginalBooking(t *testing.T) {
	store := storeWithHold()
	svc := New(store)
	first, err := svc.ConfirmHold(context.Background(), "hold-1", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	store.now = testNow.Add(time.Hour)
	second, err := svc.ConfirmHold(context.Background(), "hold-1", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(store.bookings) != 1 {
		t.Fatalf("retry duplicated booking: first=%+v, second=%+v", first, second)
	}
	// Retrying a confirmation after a subsequent cancellation must not rebook it.
	store.bookings[0].Status = BookingCancelled
	got, err := svc.ConfirmHold(context.Background(), "hold-1", "user-1")
	if err != nil || got != store.bookings[0] || len(store.bookings) != 1 {
		t.Fatalf("cancelled booking retry: %+v, %v", got, err)
	}
}

func TestConfirmHoldRejectsOtherUsersEvenOnRetry(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "confirmed"}[confirmed], func(t *testing.T) {
			store := storeWithHold()
			svc := New(store)
			if confirmed {
				if _, err := svc.ConfirmHold(context.Background(), "hold-1", "user-1"); err != nil {
					t.Fatal(err)
				}
			}
			before := store.holds[0]
			count := len(store.bookings)
			got, err := svc.ConfirmHold(context.Background(), "hold-1", "user-2")
			if !errors.Is(err, ErrForbidden) || got != (Booking{}) {
				t.Fatalf("booking=%+v, error=%v", got, err)
			}
			if store.holds[0] != before || len(store.bookings) != count {
				t.Fatal("unauthorized confirmation changed state")
			}
		})
	}
}

func TestConfirmHoldRejectsExpiredOrInactiveHold(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    HoldStatus
		expiresAt time.Time
		want      error
	}{
		{"exact expiry", HoldActive, testNow, ErrHoldExpired},
		{"expired without cleanup", HoldActive, testNow.Add(-time.Second), ErrHoldExpired},
		{"marked expired", HoldExpired, testNow.Add(time.Hour), ErrHoldExpired},
		{"released", HoldReleased, testNow.Add(time.Hour), ErrHoldNotActive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := storeWithHold()
			store.holds[0].Status, store.holds[0].ExpiresAt = tc.status, tc.expiresAt
			before := store.holds[0]
			got, err := New(store).ConfirmHold(context.Background(), "hold-1", "user-1")
			if !errors.Is(err, tc.want) || got != (Booking{}) {
				t.Fatalf("booking=%+v, error=%v, want %v", got, err, tc.want)
			}
			if len(store.bookings) != 0 || store.holds[0] != before {
				t.Fatal("rejected confirmation changed state")
			}
		})
	}
}

func TestConfirmHoldJustBeforeExpiry(t *testing.T) {
	store := storeWithHold()
	store.now = store.holds[0].ExpiresAt.Add(-time.Nanosecond)
	if _, err := New(store).ConfirmHold(context.Background(), "hold-1", "user-1"); err != nil {
		t.Fatal(err)
	}
}

func TestConfirmHoldRechecksStateAndTimeAfterLock(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*holdStoreFake)
		want   error
	}{
		{"expires while waiting", func(s *holdStoreFake) { s.now = s.holds[0].ExpiresAt }, ErrHoldExpired},
		{"released while waiting", func(s *holdStoreFake) { s.holds[0].Status = HoldReleased }, ErrHoldNotActive},
		{"hold removed", func(s *holdStoreFake) { s.holds = nil }, ErrHoldNotFound},
		{"slot booked", func(s *holdStoreFake) { s.booked = true }, ErrSlotUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := storeWithHold()
			store.onLock = func() { tc.change(store) }
			got, err := New(store).ConfirmHold(context.Background(), "hold-1", "user-1")
			if !errors.Is(err, tc.want) || got != (Booking{}) || len(store.bookings) != 0 {
				t.Fatalf("booking=%+v, error=%v, want %v", got, err, tc.want)
			}
		})
	}
}

func TestConfirmHoldOldExpiredHoldCannotConfirmAfterReplacement(t *testing.T) {
	store := storeWithHold()
	store.now = store.holds[0].ExpiresAt
	svc := New(store)
	replacement, err := svc.CreateHold(context.Background(), "slot-1", "user-2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfirmHold(context.Background(), "hold-1", "user-1"); !errors.Is(err, ErrHoldExpired) {
		t.Fatalf("old hold confirmation error=%v", err)
	}
	if _, err := svc.ConfirmHold(context.Background(), replacement.ID, "user-2"); err != nil {
		t.Fatal(err)
	}
	if len(store.bookings) != 1 || store.bookings[0].UserID != "user-2" {
		t.Fatalf("bookings=%+v", store.bookings)
	}
}

func TestConfirmHoldValidatesInputAndMissingHold(t *testing.T) {
	for _, tc := range []struct{ holdID, userID string }{
		{"", "user-1"}, {"hold-1", ""}, {"  ", "user-1"}, {"hold-1", "\t"},
	} {
		store := storeWithHold()
		got, err := New(store).ConfirmHold(context.Background(), tc.holdID, tc.userID)
		if !errors.Is(err, ErrInvalidInput) || got != (Booking{}) || store.calls != 0 || store.lookups != 0 {
			t.Fatalf("invalid input reached storage: booking=%+v, error=%v", got, err)
		}
	}
	store := storeWithHold()
	if _, err := New(store).ConfirmHold(context.Background(), "unknown", "user-1"); !errors.Is(err, ErrHoldNotFound) {
		t.Fatalf("error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(store).ConfirmHold(ctx, "hold-1", "user-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestConfirmHoldFailuresRollbackAndReturnNoBooking(t *testing.T) {
	failure := errors.New("database unavailable")
	for _, stage := range []string{"lookup", "lock", "readHold", "occupancy", "clock", "insertBooking", "updateHold", "commit", "findBooking"} {
		t.Run(stage, func(t *testing.T) {
			store := storeWithHold()
			if stage == "findBooking" {
				if _, err := New(store).ConfirmHold(context.Background(), "hold-1", "user-1"); err != nil {
					t.Fatal(err)
				}
			}
			before := store.holds[0]
			count := len(store.bookings)
			store.failStage, store.failure = stage, failure
			got, err := New(store).ConfirmHold(context.Background(), "hold-1", "user-1")
			if !errors.Is(err, failure) || got != (Booking{}) {
				t.Fatalf("booking=%+v, error=%v", got, err)
			}
			if store.holds[0] != before || len(store.bookings) != count {
				t.Fatal("failed confirmation changed persistent state")
			}
		})
	}
}

func TestConfirmHoldDoesNotRecreateMissingBooking(t *testing.T) {
	store := storeWithHold()
	store.holds[0].Status = HoldConfirmed
	got, err := New(store).ConfirmHold(context.Background(), "hold-1", "user-1")
	if !errors.Is(err, ErrBookingNotFound) || got != (Booking{}) || len(store.bookings) != 0 {
		t.Fatalf("booking=%+v, error=%v", got, err)
	}
}
