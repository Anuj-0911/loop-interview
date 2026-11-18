package service

import (
	"context"
	"errors"
	"testing"
)

func storeWithBooking(t *testing.T) (*holdStoreFake, Booking) {
	t.Helper()
	store := storeWithHold()
	booking, err := New(store).ConfirmHold(context.Background(), "hold-1", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	return store, booking
}

func TestCancelBookingMakesSlotAvailableWithoutReactivatingHold(t *testing.T) {
	store, original := storeWithBooking(t)
	svc := New(store)
	ctx := context.WithValue(context.Background(), contextKey{}, "cancellation")
	if err := svc.CancelBooking(ctx, original.ID, "user-1"); err != nil {
		t.Fatal(err)
	}
	want := original
	want.Status = BookingCancelled
	if store.bookings[0] != want || store.holds[0].Status != HoldConfirmed {
		t.Fatalf("unexpected state: bookings=%+v, holds=%+v", store.bookings, store.holds)
	}
	if store.ctx != ctx {
		t.Error("request context was not propagated")
	}
	retry, err := svc.ConfirmHold(ctx, original.HoldID, "user-1")
	if err != nil || retry != want {
		t.Fatalf("old confirmation retry = %+v, %v", retry, err)
	}
	replacement, err := svc.CreateHold(ctx, original.SlotID, "user-2")
	if err != nil {
		t.Fatalf("cancelled slot unavailable: %v", err)
	}
	if _, err := svc.ConfirmHold(ctx, replacement.ID, "user-2"); err != nil {
		t.Fatalf("replacement confirmation: %v", err)
	}
}

func TestCancelBookingRetryDoesNotAffectReplacement(t *testing.T) {
	store, original := storeWithBooking(t)
	svc := New(store)
	ctx := context.Background()
	if err := svc.CancelBooking(ctx, original.ID, "user-1"); err != nil {
		t.Fatal(err)
	}
	hold, err := svc.CreateHold(ctx, original.SlotID, "user-2")
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := svc.ConfirmHold(ctx, hold.ID, "user-2")
	if err != nil {
		t.Fatal(err)
	}
	// A retry is successful without another write, even with a new booking present.
	store.failStage, store.failure = "updateBooking", errors.New("unexpected write")
	if err := svc.CancelBooking(ctx, original.ID, "user-1"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(store.bookings) != 2 || store.bookings[0].Status != BookingCancelled || store.bookings[1] != replacement {
		t.Fatalf("retry changed bookings: %+v", store.bookings)
	}
	if _, err := svc.CreateHold(ctx, original.SlotID, "user-1"); !errors.Is(err, ErrSlotUnavailable) {
		t.Fatalf("replacement no longer blocks slot: %v", err)
	}
}

func TestCancelBookingRejectsOtherUsersEvenAfterCancellation(t *testing.T) {
	for _, status := range []BookingStatus{BookingConfirmed, BookingCancelled} {
		t.Run(string(status), func(t *testing.T) {
			store, original := storeWithBooking(t)
			store.bookings[0].Status = status
			before := store.bookings[0]
			err := New(store).CancelBooking(context.Background(), original.ID, "user-2")
			if !errors.Is(err, ErrForbidden) {
				t.Fatalf("error = %v, want ErrForbidden", err)
			}
			if store.bookings[0] != before || store.holds[0].Status != HoldConfirmed {
				t.Fatal("unauthorized request changed state")
			}
		})
	}
}

func TestCancelBookingValidatesIDs(t *testing.T) {
	for _, tc := range []struct{ bookingID, userID string }{
		{"", "user-1"}, {"booking-1", ""}, {"  ", "user-1"}, {"booking-1", "\t"},
	} {
		store := newBookingStoreFake()
		err := New(store).CancelBooking(context.Background(), tc.bookingID, tc.userID)
		if !errors.Is(err, ErrInvalidInput) || store.calls != 0 || store.lookups != 0 {
			t.Fatalf("invalid input reached storage: %v", err)
		}
	}
}

func TestCancelBookingMissingOrCancelledRequest(t *testing.T) {
	store, original := storeWithBooking(t)
	svc := New(store)
	if err := svc.CancelBooking(context.Background(), "unknown", "user-1"); !errors.Is(err, ErrBookingNotFound) {
		t.Fatalf("error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := svc.CancelBooking(ctx, original.ID, "user-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if store.bookings[0] != original {
		t.Fatal("failed request changed booking")
	}
}

func TestCancelBookingRereadsAfterLock(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*holdStoreFake)
		want   error
	}{
		{"already cancelled", func(s *holdStoreFake) { s.bookings[0].Status = BookingCancelled }, nil},
		{"booking missing", func(s *holdStoreFake) { s.bookings = nil }, ErrBookingNotFound},
		{"unknown status", func(s *holdStoreFake) { s.bookings[0].Status = "UNKNOWN" }, ErrBookingNotActive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, original := storeWithBooking(t)
			store.onLock = func() { tc.change(store) }
			err := New(store).CancelBooking(context.Background(), original.ID, "user-1")
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if tc.name == "unknown status" && store.bookings[0].Status != "UNKNOWN" {
				t.Fatal("unrecognized status was overwritten")
			}
		})
	}
}

func TestCancelBookingFailuresDoNotChangeState(t *testing.T) {
	failure := errors.New("database unavailable")
	for _, stage := range []string{"lookupBooking", "lock", "readBooking", "updateBooking", "commit"} {
		t.Run(stage, func(t *testing.T) {
			store, original := storeWithBooking(t)
			hold := store.holds[0]
			store.failStage, store.failure = stage, failure
			err := New(store).CancelBooking(context.Background(), original.ID, "user-1")
			if !errors.Is(err, failure) {
				t.Fatalf("error = %v, want wrapped failure", err)
			}
			if len(store.bookings) != 1 || store.bookings[0] != original || store.holds[0] != hold {
				t.Fatal("failed cancellation changed persistent state")
			}
		})
	}
}
