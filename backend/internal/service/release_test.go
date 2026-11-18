package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestReleaseHoldMakesSlotAvailable(t *testing.T) {
	store := storeWithHold()
	svc := New(store)
	ctx := context.WithValue(context.Background(), contextKey{}, "release")
	if err := svc.ReleaseHold(ctx, "hold-1", "user-1"); err != nil {
		t.Fatal(err)
	}
	if store.holds[0].Status != HoldReleased {
		t.Fatalf("status=%s, want RELEASED", store.holds[0].Status)
	}
	if store.ctx != ctx {
		t.Error("request context was not propagated")
	}
	if _, err := svc.ConfirmHold(ctx, "hold-1", "user-1"); !errors.Is(err, ErrHoldNotActive) {
		t.Fatalf("released hold confirmation error=%v", err)
	}
	if _, err := svc.CreateHold(ctx, "slot-1", "user-2"); err != nil {
		t.Fatalf("slot unavailable after release: %v", err)
	}
	if len(store.bookings) != 0 {
		t.Fatal("release must not create a booking")
	}
}

func TestReleaseHoldRetriesAndExpiredHolds(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    HoldStatus
		expiresAt time.Time
		want      HoldStatus
	}{
		{"already released", HoldReleased, testNow.Add(time.Hour), HoldReleased},
		{"marked expired", HoldExpired, testNow.Add(-time.Second), HoldExpired},
		{"expired without cleanup", HoldActive, testNow.Add(-time.Second), HoldReleased},
		{"exact expiry", HoldActive, testNow, HoldReleased},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := storeWithHold()
			store.holds[0].Status, store.holds[0].ExpiresAt = tc.status, tc.expiresAt
			svc := New(store)
			if err := svc.ReleaseHold(context.Background(), "hold-1", "user-1"); err != nil {
				t.Fatal(err)
			}
			if store.holds[0].Status != tc.want {
				t.Fatalf("status=%s, want %s", store.holds[0].Status, tc.want)
			}
			// A retry must not require another write.
			store.failStage, store.failure = "updateHold", errors.New("unexpected update")
			if err := svc.ReleaseHold(context.Background(), "hold-1", "user-1"); err != nil {
				t.Fatalf("retry error=%v", err)
			}
		})
	}
}

func TestReleaseHoldRejectsOtherUsers(t *testing.T) {
	for _, status := range []HoldStatus{HoldActive, HoldReleased, HoldExpired, HoldConfirmed} {
		t.Run(string(status), func(t *testing.T) {
			store := storeWithHold()
			store.holds[0].Status = status
			before := store.holds[0]
			err := New(store).ReleaseHold(context.Background(), "hold-1", "user-2")
			if !errors.Is(err, ErrForbidden) {
				t.Fatalf("error=%v, want ErrForbidden", err)
			}
			if store.holds[0] != before {
				t.Fatal("unauthorized release changed the hold")
			}
		})
	}
}

func TestReleaseHoldCannotReleaseConfirmedBooking(t *testing.T) {
	store := storeWithHold()
	svc := New(store)
	booking, err := svc.ConfirmHold(context.Background(), "hold-1", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	err = svc.ReleaseHold(context.Background(), "hold-1", "user-1")
	if !errors.Is(err, ErrHoldNotActive) {
		t.Fatalf("error=%v, want ErrHoldNotActive", err)
	}
	if store.holds[0].Status != HoldConfirmed || len(store.bookings) != 1 || store.bookings[0] != booking {
		t.Fatal("release changed a confirmed booking")
	}
	if _, err := svc.CreateHold(context.Background(), "slot-1", "user-2"); !errors.Is(err, ErrSlotUnavailable) {
		t.Fatalf("booked slot became available: %v", err)
	}
}

func TestReleaseOldHoldDoesNotReleaseReplacement(t *testing.T) {
	store := storeWithHold()
	store.now = store.holds[0].ExpiresAt
	svc := New(store)
	replacement, err := svc.CreateHold(context.Background(), "slot-1", "user-2")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ReleaseHold(context.Background(), "hold-1", "user-1"); err != nil {
		t.Fatal(err)
	}
	if store.holds[1] != replacement {
		t.Fatal("release changed the replacement hold")
	}
	if _, err := svc.CreateHold(context.Background(), "slot-1", "user-1"); !errors.Is(err, ErrSlotUnavailable) {
		t.Fatalf("replacement no longer blocks slot: %v", err)
	}
	if _, err := svc.ConfirmHold(context.Background(), replacement.ID, "user-2"); err != nil {
		t.Fatalf("replacement confirmation failed: %v", err)
	}
}

func TestReleaseHoldRechecksStateAfterLock(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*holdStoreFake)
		want   error
	}{
		{"confirmation won", func(s *holdStoreFake) { s.holds[0].Status = HoldConfirmed }, ErrHoldNotActive},
		{"already released", func(s *holdStoreFake) { s.holds[0].Status = HoldReleased }, nil},
		{"cleanup won", func(s *holdStoreFake) { s.holds[0].Status = HoldExpired }, nil},
		{"hold missing", func(s *holdStoreFake) { s.holds = nil }, ErrHoldNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := storeWithHold()
			store.onLock = func() { tc.change(store) }
			err := New(store).ReleaseHold(context.Background(), "hold-1", "user-1")
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v, want %v", err, tc.want)
			}
			if tc.name == "confirmation won" && store.holds[0].Status != HoldConfirmed {
				t.Fatal("release overwrote confirmation")
			}
		})
	}
}

func TestReleaseHoldValidatesInputAndMissingHold(t *testing.T) {
	for _, tc := range []struct{ holdID, userID string }{
		{"", "user-1"}, {"hold-1", ""}, {"  ", "user-1"}, {"hold-1", "\t"},
	} {
		store := storeWithHold()
		err := New(store).ReleaseHold(context.Background(), tc.holdID, tc.userID)
		if !errors.Is(err, ErrInvalidInput) || store.calls != 0 || store.lookups != 0 {
			t.Fatalf("invalid input reached storage: %v", err)
		}
	}
	store := storeWithHold()
	if err := New(store).ReleaseHold(context.Background(), "unknown", "user-1"); !errors.Is(err, ErrHoldNotFound) {
		t.Fatalf("error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := New(store).ReleaseHold(ctx, "hold-1", "user-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if store.holds[0].Status != HoldActive {
		t.Fatal("failed request changed hold status")
	}
}

func TestReleaseHoldFailuresDoNotChangeState(t *testing.T) {
	failure := errors.New("database unavailable")
	for _, stage := range []string{"lookup", "lock", "readHold", "updateHold", "commit"} {
		t.Run(stage, func(t *testing.T) {
			store := storeWithHold()
			before := store.holds[0]
			store.failStage, store.failure = stage, failure
			err := New(store).ReleaseHold(context.Background(), "hold-1", "user-1")
			if !errors.Is(err, failure) {
				t.Fatalf("error=%v, want wrapped failure", err)
			}
			if store.holds[0] != before || len(store.bookings) != 0 {
				t.Fatal("failed release changed persistent state")
			}
		})
	}
}
