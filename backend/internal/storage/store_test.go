package storage

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"loop-interview/backend/internal/service"
)

func bookingFixture(t *testing.T) (*Store, *service.Service, []service.Slot) {
	t.Helper()
	db := openTestDB(t)
	execSQL(t, db, `INSERT INTO users(id,name) VALUES ('user-1','Alice'),('user-2','Bob');
 INSERT INTO resources(id,name,type,timezone) VALUES ('resource-1','Room','ROOM','UTC')`)
	store := New(db)
	svc := service.New(store)
	start := time.Now().UTC().Truncate(time.Hour).Add(time.Hour).Add(123456789 * time.Nanosecond)
	_, err := svc.ConfigureAvailability(context.Background(), "resource-1", service.AvailabilityInput{StartTime: start, EndTime: start.Add(30 * time.Minute), SlotDurationMinutes: 15})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConfigureAvailability(context.Background(), "resource-1", service.AvailabilityInput{StartTime: start, EndTime: start.Add(30 * time.Minute), SlotDurationMinutes: 15}); err != nil {
		t.Fatalf("configuration retry: %v", err)
	}
	slots, err := svc.GenerateSlots(context.Background(), "resource-1", start.Format(time.DateOnly))
	if err != nil || len(slots) != 2 {
		t.Fatalf("slots=%+v, error=%v", slots, err)
	}
	return store, svc, slots
}

func TestStoreBookingLifecycle(t *testing.T) {
	_, svc, slots := bookingFixture(t)
	ctx := context.Background()
	hold, err := svc.CreateHold(ctx, slots[0].ID, "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateHold(ctx, slots[0].ID, "user-2"); !errors.Is(err, service.ErrSlotUnavailable) {
		t.Fatalf("competing hold error=%v", err)
	}
	listed, err := svc.ListSlots(ctx, "resource-1", "")
	if err != nil || listed[0].Status != service.SlotHeld {
		t.Fatalf("slots=%+v, error=%v", listed, err)
	}
	if _, err := svc.ConfirmHold(ctx, hold.ID, "user-2"); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("ownership error=%v", err)
	}
	booking, err := svc.ConfirmHold(ctx, hold.ID, "user-1")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := svc.ConfirmHold(ctx, hold.ID, "user-1")
	if err != nil || retry != booking {
		t.Fatalf("retry=%+v, error=%v", retry, err)
	}
	if err := svc.CancelBooking(ctx, booking.ID, "user-1"); err != nil {
		t.Fatal(err)
	}
	replacement, err := svc.CreateHold(ctx, slots[0].ID, "user-2")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CancelBooking(ctx, booking.ID, "user-1"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReleaseHold(ctx, replacement.ID, "user-2"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReleaseHold(ctx, replacement.ID, "user-2"); err != nil {
		t.Fatal(err)
	}
	listed, err = svc.ListSlots(ctx, "resource-1", "")
	if err != nil || listed[0].Status != service.SlotAvailable {
		t.Fatalf("slots=%+v, error=%v", listed, err)
	}
	regenerated, err := svc.GenerateSlots(ctx, "resource-1", slots[0].StartTime.Format(time.DateOnly))
	if err != nil || len(regenerated) != 2 || regenerated[0] != slots[0] {
		t.Fatalf("regeneration=%+v, error=%v", regenerated, err)
	}
}

func TestStoreExpiredHoldAndRollback(t *testing.T) {
	store, svc, slots := bookingFixture(t)
	ctx := context.Background()
	// Insert a timestamp-expired ACTIVE hold without waiting for a timer.
	execSQL(t, store.db, `INSERT INTO holds(id,slot_id,user_id,expires_at_ms,status) VALUES ('expired',?,'user-1',0,'ACTIVE')`, slots[0].ID)
	if _, err := svc.ConfirmHold(ctx, "expired", "user-1"); !errors.Is(err, service.ErrHoldExpired) {
		t.Fatalf("expired confirmation error=%v", err)
	}
	if _, err := svc.CreateHold(ctx, slots[0].ID, "user-2"); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("force rollback")
	err := store.WithSlot(ctx, slots[1].ID, func(tx service.SlotTransaction) error {
		if _, err := tx.InsertHold("user-1", time.Now().Add(time.Second)); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("rollback error=%v", err)
	}
	if _, err := svc.CreateHold(ctx, slots[1].ID, "user-2"); err != nil {
		t.Fatalf("rollback left slot occupied: %v", err)
	}
	if _, err := svc.CreateHold(ctx, "missing", "user-1"); !errors.Is(err, service.ErrSlotNotFound) {
		t.Fatalf("missing slot error=%v", err)
	}
	if _, err := svc.ConfirmHold(ctx, "missing", "user-1"); !errors.Is(err, service.ErrHoldNotFound) {
		t.Fatalf("missing hold error=%v", err)
	}
}

func TestStoreConcurrentHoldsHaveOneWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.sqlite")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	seedSchemaFixture(t, db)
	// Two separate pools ensure correctness comes from SQLite, not a Go mutex.
	second, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	services := []*service.Service{service.New(New(db)), service.New(New(second))}
	start := make(chan struct{})
	results := make(chan error, 12)
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := services[i%2].CreateHold(context.Background(), "slot-1", []string{"user-1", "user-2"}[i%2])
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, service.ErrSlotUnavailable) {
			t.Fatalf("unexpected concurrent error=%v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("winners=%d, want 1", winners)
	}
}
