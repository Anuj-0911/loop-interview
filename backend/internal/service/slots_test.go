package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

func testResource() Resource {
	return Resource{ID: "resource-1", Name: "Meeting room", Type: "ROOM", Timezone: "Asia/Kolkata"}
}
func testSlot(id string, start time.Time) Slot {
	return Slot{ID: id, ResourceID: "resource-1", AvailabilityConfigID: "config-1", StartTime: start, EndTime: start.Add(30 * time.Minute)}
}

func TestListSlotsEffectiveAvailability(t *testing.T) {
	for _, tc := range []struct {
		name      string
		occupancy SlotOccupancy
		want      SlotStatus
	}{
		{name: "empty", want: SlotAvailable},
		{name: "confirmed booking", occupancy: SlotOccupancy{HasConfirmedBooking: true}, want: SlotBooked},
		{name: "active hold", occupancy: SlotOccupancy{Holds: []Hold{{Status: HoldActive, ExpiresAt: testNow.Add(time.Second)}}}, want: SlotHeld},
		{name: "exact expiry", occupancy: SlotOccupancy{Holds: []Hold{{Status: HoldActive, ExpiresAt: testNow}}}, want: SlotAvailable},
		{name: "expired without cleanup", occupancy: SlotOccupancy{Holds: []Hold{{Status: HoldActive, ExpiresAt: testNow.Add(-time.Second)}}}, want: SlotAvailable},
		{name: "released", occupancy: SlotOccupancy{Holds: []Hold{{Status: HoldReleased, ExpiresAt: testNow.Add(time.Hour)}}}, want: SlotAvailable},
		{name: "expired status", occupancy: SlotOccupancy{Holds: []Hold{{Status: HoldExpired, ExpiresAt: testNow.Add(time.Hour)}}}, want: SlotAvailable},
		{name: "confirmed historical hold", occupancy: SlotOccupancy{Holds: []Hold{{Status: HoldConfirmed, ExpiresAt: testNow.Add(time.Hour)}}}, want: SlotAvailable},
		{name: "booking takes precedence", occupancy: SlotOccupancy{HasConfirmedBooking: true, Holds: []Hold{{Status: HoldActive, ExpiresAt: testNow.Add(time.Hour)}}}, want: SlotBooked},
		{name: "old hold does not hide replacement", occupancy: SlotOccupancy{Holds: []Hold{{Status: HoldActive, ExpiresAt: testNow}, {Status: HoldActive, ExpiresAt: testNow.Add(time.Second)}}}, want: SlotHeld},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newBookingStoreFake()
			slot := testSlot("slot-1", testNow)
			store.records = []SlotRecord{{Slot: slot, Occupancy: tc.occupancy}}
			got, err := New(store).ListSlots(context.Background(), "resource-1", "")
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].Slot != slot || got[0].Status != tc.want {
				t.Fatalf("slots=%+v, want status %s", got, tc.want)
			}
		})
	}
}

func TestListSlotsFiltersByResourceLocalDateAndSorts(t *testing.T) {
	store := newBookingStoreFake()
	// India midnight is 18:30 UTC on the previous date.
	midnight := time.Date(2026, 11, 17, 18, 30, 0, 0, time.UTC)
	early := testSlot("early", midnight)
	late := testSlot("late", midnight.Add(23*time.Hour))
	store.records = []SlotRecord{
		{Slot: late}, {Slot: testSlot("next-day", midnight.Add(24*time.Hour))},
		{Slot: early}, {Slot: testSlot("previous-day", midnight.Add(-30*time.Minute))},
	}
	ctx := context.WithValue(context.Background(), contextKey{}, "listing")
	got, err := New(store).ListSlots(ctx, "resource-1", "2026-11-18")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Slot != early || got[1].Slot != late {
		t.Fatalf("slots=%+v", got)
	}
	if store.snapshotCtx != ctx {
		t.Error("listing did not propagate context")
	}
	if store.records[0].Slot != late {
		t.Fatal("listing reordered storage-owned data")
	}
}

func TestListSlotsEmptyAndErrors(t *testing.T) {
	store := newBookingStoreFake()
	got, err := New(store).ListSlots(context.Background(), "resource-1", "")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("slots=%+v, error=%v", got, err)
	}
	if _, err := New(store).ListSlots(context.Background(), "unknown", ""); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("error=%v", err)
	}
	failure := errors.New("snapshot failed")
	store.scheduleFailStage, store.scheduleFailure = "snapshot", failure
	if got, err := New(store).ListSlots(context.Background(), "resource-1", ""); !errors.Is(err, failure) || got != nil {
		t.Fatalf("slots=%+v, error=%v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store.scheduleFailStage = ""
	if _, err := New(store).ListSlots(ctx, "resource-1", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestListSlotsValidatesInput(t *testing.T) {
	for _, tc := range []struct{ resourceID, date string }{
		{"", ""}, {"  ", "2026-11-18"}, {"resource-1", "2026-02-30"}, {"resource-1", "18-11-2026"}, {"resource-1", "2026-1-1"},
	} {
		store := newBookingStoreFake()
		if _, err := New(store).ListSlots(context.Background(), tc.resourceID, tc.date); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("input=%+v, error=%v", tc, err)
		}
		if store.snapshotCalls != 0 {
			t.Fatal("invalid input reached storage")
		}
	}
}
