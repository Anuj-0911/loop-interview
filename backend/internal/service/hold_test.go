package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

var testNow = time.Date(2026, time.November, 18, 10, 0, 0, 0, time.UTC)

func TestCreateHold(t *testing.T) {
	store := newBookingStoreFake()
	ctx := context.WithValue(context.Background(), contextKey{}, "request-1")
	got, err := New(store).CreateHold(ctx, "slot-1", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	want := Hold{ID: "hold-1", SlotID: "slot-1", UserID: "user-1", Status: HoldActive, ExpiresAt: testNow.Add(30 * time.Second)}
	if got != want {
		t.Fatalf("hold = %+v, want %+v", got, want)
	}
	if len(store.holds) != 1 || store.holds[0] != want {
		t.Fatalf("persisted holds = %+v", store.holds)
	}
	if store.ctx != ctx {
		t.Error("request context was not passed to storage")
	}
}

func TestCreateHoldRejectsOccupiedSlot(t *testing.T) {
	for _, tc := range []struct {
		name   string
		booked bool
		holds  []Hold
	}{
		{name: "confirmed booking", booked: true},
		{name: "another user's hold", holds: []Hold{{UserID: "user-2", Status: HoldActive, ExpiresAt: testNow.Add(time.Second)}}},
		{name: "own active hold", holds: []Hold{{UserID: "user-1", Status: HoldActive, ExpiresAt: testNow.Add(time.Second)}}},
		{name: "one nanosecond before expiry", holds: []Hold{{Status: HoldActive, ExpiresAt: testNow.Add(time.Nanosecond)}}},
		{name: "booking with expired hold", booked: true, holds: []Hold{{Status: HoldActive, ExpiresAt: testNow.Add(-time.Second)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newBookingStoreFake()
			store.booked, store.holds = tc.booked, tc.holds
			before := len(store.holds)
			got, err := New(store).CreateHold(context.Background(), "slot-1", "user-1")
			if !errors.Is(err, ErrSlotUnavailable) {
				t.Fatalf("error = %v, want ErrSlotUnavailable", err)
			}
			if got != (Hold{}) || len(store.holds) != before {
				t.Fatal("conflict must not create a hold")
			}
		})
	}
}

func TestCreateHoldIgnoresExpiredAndInactiveHolds(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    HoldStatus
		expiresAt time.Time
	}{
		{"exactly at expiry", HoldActive, testNow},
		{"past expiry without cleanup", HoldActive, testNow.Add(-time.Second)},
		{"released", HoldReleased, testNow.Add(time.Hour)},
		{"expired", HoldExpired, testNow.Add(time.Hour)},
		{"confirmed historical hold", HoldConfirmed, testNow.Add(time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newBookingStoreFake()
			old := Hold{ID: "old-hold", SlotID: "slot-1", UserID: "user-2", Status: tc.status, ExpiresAt: tc.expiresAt}
			store.holds = []Hold{old}
			got, err := New(store).CreateHold(context.Background(), "slot-1", "user-1")
			if err != nil {
				t.Fatal(err)
			}
			if got.UserID != "user-1" || got.Status != HoldActive || !got.ExpiresAt.Equal(testNow.Add(30*time.Second)) {
				t.Fatalf("new hold = %+v", got)
			}
			if len(store.holds) != 2 || store.holds[0] != old {
				t.Fatal("historical hold must be retained")
			}
		})
	}
}

func TestAnotherUserCanHoldAfterExpiryWithoutCleanup(t *testing.T) {
	store := newBookingStoreFake()
	svc := New(store)
	first, err := svc.CreateHold(context.Background(), "slot-1", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateHold(context.Background(), "slot-1", "user-2"); !errors.Is(err, ErrSlotUnavailable) {
		t.Fatalf("second user error = %v", err)
	}
	store.now = first.ExpiresAt
	second, err := svc.CreateHold(context.Background(), "slot-1", "user-2")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || second.UserID != "user-2" || !second.ExpiresAt.Equal(store.now.Add(30*time.Second)) {
		t.Fatalf("replacement hold = %+v", second)
	}
	if store.holds[0].Status != HoldActive {
		t.Fatal("test must demonstrate expiry without cleanup")
	}
	// An earlier expired hold must not hide the newer, valid hold.
	if _, err := svc.CreateHold(context.Background(), "slot-1", "user-1"); !errors.Is(err, ErrSlotUnavailable) {
		t.Fatalf("third attempt error = %v", err)
	}
}

func TestCreateHoldUsesTimeAfterLock(t *testing.T) {
	store := newBookingStoreFake()
	store.holds = []Hold{{Status: HoldActive, ExpiresAt: testNow.Add(time.Second)}}
	store.onLock = func() { store.now = testNow.Add(2 * time.Second) }
	got, err := New(store).CreateHold(context.Background(), "slot-1", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.ExpiresAt.Equal(testNow.Add(32 * time.Second)) {
		t.Fatalf("expiry = %v, want 30 seconds after acquiring lock", got.ExpiresAt)
	}
}

func TestCreateHoldRejectsMissingIDs(t *testing.T) {
	for _, tc := range []struct{ name, slotID, userID string }{
		{name: "missing slot", userID: "user-1"},
		{name: "missing user", slotID: "slot-1"},
		{name: "blank slot", slotID: "  ", userID: "user-1"},
		{name: "blank user", slotID: "slot-1", userID: "\t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newBookingStoreFake()
			got, err := New(store).CreateHold(context.Background(), tc.slotID, tc.userID)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error = %v, want ErrInvalidInput", err)
			}
			if got != (Hold{}) || store.calls != 0 {
				t.Fatal("invalid input must not reach storage")
			}
		})
	}
}

func TestCreateHoldRejectsUnknownUser(t *testing.T) {
	store := newBookingStoreFake()
	got, err := New(store).CreateHold(context.Background(), "slot-1", "unknown-user")
	if !errors.Is(err, ErrUserNotFound) || got != (Hold{}) || len(store.holds) != 0 {
		t.Fatalf("hold = %+v, error = %v", got, err)
	}
}

func TestCreateHoldErrorsDoNotReturnOrPersistHold(t *testing.T) {
	failure := errors.New("database unavailable")
	for _, stage := range []string{"lock", "user", "occupancy", "clock", "insert", "commit"} {
		t.Run(stage, func(t *testing.T) {
			store := newBookingStoreFake()
			store.failStage, store.failure = stage, failure
			got, err := New(store).CreateHold(context.Background(), "slot-1", "user-1")
			if !errors.Is(err, failure) {
				t.Fatalf("error = %v, want wrapped failure", err)
			}
			if got != (Hold{}) || len(store.holds) != 0 {
				t.Fatal("failed transaction must not expose a hold")
			}
		})
	}
}

func TestCreateHoldMissingSlotAndCancelledRequest(t *testing.T) {
	store := newBookingStoreFake()
	if _, err := New(store).CreateHold(context.Background(), "unknown-slot", "user-1"); !errors.Is(err, ErrSlotNotFound) {
		t.Fatalf("error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(store).CreateHold(ctx, "slot-1", "user-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if len(store.holds) != 0 {
		t.Fatal("request must not create holds")
	}
}

type contextKey struct{}

// This fake provides a transaction snapshot and clock, not availability rules.
// Real locking and rollback guarantees require PostgreSQL integration tests.
type holdStoreFake struct {
	*scheduleStoreFake
	now       time.Time
	holds     []Hold
	bookings  []Booking
	booked    bool
	calls     int
	lookups   int
	ctx       context.Context
	onLock    func()
	failStage string
	failure   error
}

func newBookingStoreFake() *holdStoreFake {
	return &holdStoreFake{now: testNow, scheduleStoreFake: newScheduleStoreFake()}
}

func (s *holdStoreFake) WithSlot(ctx context.Context, slotID string, fn func(SlotTransaction) error) error {
	s.calls++
	s.ctx = ctx
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.failStage == "lock" {
		return s.failure
	}
	if slotID != "slot-1" {
		return ErrSlotNotFound
	}
	if s.onLock != nil {
		s.onLock()
	}
	tx := &holdTransactionFake{store: s, holds: append([]Hold(nil), s.holds...), bookings: append([]Booking(nil), s.bookings...)}
	if err := fn(tx); err != nil {
		return err
	}
	if s.failStage == "commit" {
		return s.failure
	}
	s.holds = tx.holds
	s.bookings = tx.bookings
	return nil
}

type holdTransactionFake struct {
	store    *holdStoreFake
	holds    []Hold
	bookings []Booking
}

func (tx *holdTransactionFake) UserExists(userID string) (bool, error) {
	if tx.store.failStage == "user" {
		return false, tx.store.failure
	}
	return userID == "user-1" || userID == "user-2", nil
}
func (tx *holdTransactionFake) Occupancy() (SlotOccupancy, error) {
	if tx.store.failStage == "occupancy" {
		return SlotOccupancy{}, tx.store.failure
	}
	booked := tx.store.booked
	for _, booking := range tx.bookings {
		if booking.Status == BookingConfirmed {
			booked = true
		}
	}
	return SlotOccupancy{HasConfirmedBooking: booked, Holds: append([]Hold(nil), tx.holds...)}, nil
}
func (tx *holdTransactionFake) Now() (time.Time, error) {
	if tx.store.failStage == "clock" {
		return time.Time{}, tx.store.failure
	}
	return tx.store.now, nil
}
func (tx *holdTransactionFake) InsertHold(userID string, expiresAt time.Time) (Hold, error) {
	if tx.store.failStage == "insert" {
		return Hold{}, tx.store.failure
	}
	hold := Hold{ID: fmt.Sprintf("hold-%d", len(tx.holds)+1), SlotID: "slot-1", UserID: userID, ExpiresAt: expiresAt, Status: HoldActive}
	tx.holds = append(tx.holds, hold)
	return hold, nil
}

func (s *holdStoreFake) SlotIDForHold(ctx context.Context, holdID string) (string, error) {
	s.lookups++
	s.ctx = ctx
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s.failStage == "lookup" {
		return "", s.failure
	}
	for _, hold := range s.holds {
		if hold.ID == holdID {
			return hold.SlotID, nil
		}
	}
	return "", ErrHoldNotFound
}

func (tx *holdTransactionFake) Hold(holdID string) (Hold, error) {
	if tx.store.failStage == "readHold" {
		return Hold{}, tx.store.failure
	}
	for _, hold := range tx.holds {
		if hold.ID == holdID {
			return hold, nil
		}
	}
	return Hold{}, ErrHoldNotFound
}

func (tx *holdTransactionFake) BookingForHold(holdID string) (Booking, error) {
	if tx.store.failStage == "findBooking" {
		return Booking{}, tx.store.failure
	}
	for _, booking := range tx.bookings {
		if booking.HoldID == holdID {
			return booking, nil
		}
	}
	return Booking{}, ErrBookingNotFound
}

func (tx *holdTransactionFake) InsertBooking(hold Hold) (Booking, error) {
	if tx.store.failStage == "insertBooking" {
		return Booking{}, tx.store.failure
	}
	booking := Booking{ID: fmt.Sprintf("booking-%d", len(tx.bookings)+1), HoldID: hold.ID, SlotID: hold.SlotID, UserID: hold.UserID, Status: BookingConfirmed}
	tx.bookings = append(tx.bookings, booking)
	return booking, nil
}

func (tx *holdTransactionFake) SetHoldStatus(holdID string, status HoldStatus) error {
	if tx.store.failStage == "updateHold" {
		return tx.store.failure
	}
	for i := range tx.holds {
		if tx.holds[i].ID == holdID {
			tx.holds[i].Status = status
			return nil
		}
	}
	return ErrHoldNotFound
}

func (s *holdStoreFake) SlotIDForBooking(ctx context.Context, bookingID string) (string, error) {
	s.lookups++
	s.ctx = ctx
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s.failStage == "lookupBooking" {
		return "", s.failure
	}
	for _, booking := range s.bookings {
		if booking.ID == bookingID {
			return booking.SlotID, nil
		}
	}
	return "", ErrBookingNotFound
}

func (tx *holdTransactionFake) Booking(bookingID string) (Booking, error) {
	if tx.store.failStage == "readBooking" {
		return Booking{}, tx.store.failure
	}
	for _, booking := range tx.bookings {
		if booking.ID == bookingID {
			return booking, nil
		}
	}
	return Booking{}, ErrBookingNotFound
}

func (tx *holdTransactionFake) SetBookingStatus(bookingID string, status BookingStatus) error {
	if tx.store.failStage == "updateBooking" {
		return tx.store.failure
	}
	for i := range tx.bookings {
		if tx.bookings[i].ID == bookingID {
			tx.bookings[i].Status = status
			return nil
		}
	}
	return ErrBookingNotFound
}
