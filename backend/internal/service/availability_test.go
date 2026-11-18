package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func testAvailability() AvailabilityInput {
	return AvailabilityInput{StartTime: testNow, EndTime: testNow.Add(2 * time.Hour), SlotDurationMinutes: 30}
}

func configureForTest(t *testing.T, svc *Service, input AvailabilityInput) AvailabilityConfig {
	t.Helper()
	config, err := svc.ConfigureAvailability(context.Background(), "resource-1", input)
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func TestConfigureAvailabilityPersistsAndRetries(t *testing.T) {
	store := newBookingStoreFake()
	svc := New(store)
	input := testAvailability()
	ctx := context.WithValue(context.Background(), contextKey{}, "configuration")
	got, err := svc.ConfigureAvailability(ctx, "resource-1", input)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "" || got.ResourceID != "resource-1" || got.AvailabilityInput != input || len(store.configs) != 1 {
		t.Fatalf("config=%+v, stored=%+v", got, store.configs)
	}
	if store.resourceCtx != ctx {
		t.Error("configuration context not propagated")
	}
	// Same instants expressed in another offset are the same configuration.
	offset := time.FixedZone("offset", 3600)
	input.StartTime = input.StartTime.In(offset)
	input.EndTime = input.EndTime.In(offset)
	store.scheduleFailStage, store.scheduleFailure = "insertConfig", errors.New("unexpected insert")
	retry, err := svc.ConfigureAvailability(context.Background(), "resource-1", input)
	if err != nil || retry != got || len(store.configs) != 1 {
		t.Fatalf("retry=%+v, error=%v", retry, err)
	}
}

func TestConfigureAvailabilityRejectsOverlapButAllowsAdjacentWindows(t *testing.T) {
	for _, tc := range []struct {
		name       string
		start, end time.Duration
		minutes    int
		want       error
	}{
		{"partial overlap", time.Hour, 3 * time.Hour, 30, ErrAvailabilityOverlap},
		{"contained", 30 * time.Minute, time.Hour, 30, ErrAvailabilityOverlap},
		{"contains", -time.Hour, 3 * time.Hour, 30, ErrAvailabilityOverlap},
		{"same window different duration", 0, 2 * time.Hour, 15, ErrAvailabilityOverlap},
		{"adjacent before", -time.Hour, 0, 30, nil},
		{"adjacent after", 2 * time.Hour, 3 * time.Hour, 30, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newBookingStoreFake()
			svc := New(store)
			configureForTest(t, svc, testAvailability())
			_, err := svc.ConfigureAvailability(context.Background(), "resource-1", AvailabilityInput{StartTime: testNow.Add(tc.start), EndTime: testNow.Add(tc.end), SlotDurationMinutes: tc.minutes})
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v, want %v", err, tc.want)
			}
			wantCount := 2
			if tc.want != nil {
				wantCount = 1
			}
			if len(store.configs) != wantCount {
				t.Fatalf("configs=%+v", store.configs)
			}
		})
	}
}

func TestConfigureAvailabilityValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*AvailabilityInput)
	}{
		{"zero start", func(i *AvailabilityInput) { i.StartTime = time.Time{} }},
		{"zero end", func(i *AvailabilityInput) { i.EndTime = time.Time{} }},
		{"equal times", func(i *AvailabilityInput) { i.EndTime = i.StartTime }},
		{"reversed times", func(i *AvailabilityInput) { i.EndTime = i.StartTime.Add(-time.Hour) }},
		{"zero duration", func(i *AvailabilityInput) { i.SlotDurationMinutes = 0 }},
		{"negative duration", func(i *AvailabilityInput) { i.SlotDurationMinutes = -1 }},
		{"excessive duration", func(i *AvailabilityInput) { i.SlotDurationMinutes = 1441 }},
		{"duration exceeds window", func(i *AvailabilityInput) { i.SlotDurationMinutes = 121 }},
		{"crosses local date", func(i *AvailabilityInput) { i.EndTime = i.StartTime.Add(12 * time.Hour) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newBookingStoreFake()
			input := testAvailability()
			tc.change(&input)
			if _, err := New(store).ConfigureAvailability(context.Background(), "resource-1", input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error=%v", err)
			}
			if len(store.configs) != 0 {
				t.Fatal("invalid configuration persisted")
			}
		})
	}
	store := newBookingStoreFake()
	if _, err := New(store).ConfigureAvailability(context.Background(), "  ", testAvailability()); !errors.Is(err, ErrInvalidInput) || store.resourceCalls != 0 {
		t.Fatalf("error=%v", err)
	}
}

func TestGenerateSlotsPersistsAndRetries(t *testing.T) {
	store := newBookingStoreFake()
	svc := New(store)
	config := configureForTest(t, svc, testAvailability())
	ctx := context.WithValue(context.Background(), contextKey{}, "generation")
	got, err := svc.GenerateSlots(ctx, "resource-1", "2026-11-18")
	if err != nil {
		t.Fatal(err)
	}
	if store.resourceCtx != ctx {
		t.Error("generation context not propagated")
	}
	if len(got) != 4 || len(store.generated) != 4 {
		t.Fatalf("slots=%+v", got)
	}
	for i, slot := range got {
		start := testNow.Add(time.Duration(i) * 30 * time.Minute)
		if slot.ID == "" || slot.ResourceID != "resource-1" || slot.AvailabilityConfigID != config.ID || !slot.StartTime.Equal(start) || !slot.EndTime.Equal(start.Add(30*time.Minute)) {
			t.Fatalf("slot %d=%+v", i, slot)
		}
	}
	store.scheduleFailStage, store.scheduleFailure = "insertSlots", errors.New("unexpected insert")
	retry, err := svc.GenerateSlots(context.Background(), "resource-1", "2026-11-18")
	if err != nil || !reflect.DeepEqual(got, retry) || len(store.generated) != 4 {
		t.Fatalf("retry=%+v, error=%v", retry, err)
	}
}

func TestGenerateSlotsCompletesPartialGenerationAndSkipsRemainder(t *testing.T) {
	store := newBookingStoreFake()
	svc := New(store)
	input := testAvailability()
	input.EndTime = input.StartTime.Add(70 * time.Minute)
	config := configureForTest(t, svc, input)
	existing := testSlot("existing", testNow)
	existing.AvailabilityConfigID = config.ID
	store.generated = []Slot{existing}
	got, err := svc.GenerateSlots(context.Background(), "resource-1", "2026-11-18")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != existing || !got[1].EndTime.Equal(testNow.Add(time.Hour)) {
		t.Fatalf("slots=%+v", got)
	}
}

func TestGenerateSlotsMultipleWindowsAndDates(t *testing.T) {
	store := newBookingStoreFake()
	svc := New(store)
	later := testAvailability()
	later.StartTime = later.StartTime.Add(3 * time.Hour)
	later.EndTime = later.EndTime.Add(3 * time.Hour)
	configureForTest(t, svc, later)
	configureForTest(t, svc, testAvailability())
	nextDay := testAvailability()
	nextDay.StartTime = nextDay.StartTime.AddDate(0, 0, 1)
	nextDay.EndTime = nextDay.EndTime.AddDate(0, 0, 1)
	configureForTest(t, svc, nextDay)
	got, err := svc.GenerateSlots(context.Background(), "resource-1", "2026-11-18")
	if err != nil || len(got) != 8 {
		t.Fatalf("slots=%+v, error=%v", got, err)
	}
	for i := 1; i < len(got); i++ {
		if got[i].StartTime.Before(got[i-1].EndTime) {
			t.Fatal("slots overlap or are unsorted")
		}
	}
	empty, err := svc.GenerateSlots(context.Background(), "resource-1", "2026-11-20")
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("slots=%+v, error=%v", empty, err)
	}
}

func TestGenerateSlotsRejectsExistingOverlap(t *testing.T) {
	for _, tc := range []struct {
		name     string
		start    time.Time
		configID string
	}{
		{"partial overlap", testNow.Add(15 * time.Minute), "other-config"},
		{"same interval different config", testNow, "other-config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newBookingStoreFake()
			svc := New(store)
			configureForTest(t, svc, testAvailability())
			existing := testSlot("existing", tc.start)
			existing.AvailabilityConfigID = tc.configID
			store.generated = []Slot{existing}
			slots, err := svc.GenerateSlots(context.Background(), "resource-1", "2026-11-18")
			if !errors.Is(err, ErrSlotOverlap) || slots != nil || len(store.generated) != 1 {
				t.Fatalf("slots=%+v, error=%v", slots, err)
			}
		})
	}
}

func TestGenerateSlotsResourceLocalDateAndMidnight(t *testing.T) {
	store := newBookingStoreFake()
	svc := New(store)
	start := time.Date(2026, 11, 17, 18, 30, 0, 0, time.UTC)
	input := AvailabilityInput{StartTime: start, EndTime: start.Add(24 * time.Hour), SlotDurationMinutes: 60}
	configureForTest(t, svc, input)
	got, err := svc.GenerateSlots(context.Background(), "resource-1", "2026-11-18")
	if err != nil || len(got) != 24 || !got[0].StartTime.Equal(start) || !got[23].EndTime.Equal(input.EndTime) {
		t.Fatalf("slots=%+v, error=%v", got, err)
	}
}

func TestSchedulingErrorsAndRollback(t *testing.T) {
	failure := errors.New("database unavailable")
	for _, stage := range []string{"resourceLock", "configs", "insertConfig", "resourceCommit"} {
		t.Run("configure/"+stage, func(t *testing.T) {
			store := newBookingStoreFake()
			store.scheduleFailStage, store.scheduleFailure = stage, failure
			got, err := New(store).ConfigureAvailability(context.Background(), "resource-1", testAvailability())
			if !errors.Is(err, failure) || got != (AvailabilityConfig{}) || len(store.configs) != 0 {
				t.Fatalf("config=%+v, error=%v", got, err)
			}
		})
	}
	for _, stage := range []string{"resourceLock", "configs", "slots", "insertSlots", "resourceCommit"} {
		t.Run("generate/"+stage, func(t *testing.T) {
			store := newBookingStoreFake()
			svc := New(store)
			configureForTest(t, svc, testAvailability())
			store.scheduleFailStage, store.scheduleFailure = stage, failure
			got, err := svc.GenerateSlots(context.Background(), "resource-1", "2026-11-18")
			if !errors.Is(err, failure) || got != nil || len(store.generated) != 0 {
				t.Fatalf("slots=%+v, error=%v", got, err)
			}
		})
	}
	store := newBookingStoreFake()
	svc := New(store)
	if _, err := svc.ConfigureAvailability(context.Background(), "unknown", testAvailability()); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("error=%v", err)
	}
	if _, err := svc.GenerateSlots(context.Background(), "unknown", "2026-11-18"); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.ConfigureAvailability(ctx, "resource-1", testAvailability()); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if _, err := svc.GenerateSlots(ctx, "resource-1", "2026-11-18"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestGenerateSlotsValidatesInput(t *testing.T) {
	for _, tc := range []struct{ resourceID, date string }{{"", "2026-11-18"}, {"resource-1", ""}, {"resource-1", "2026-02-30"}, {"resource-1", "2026-1-1"}} {
		store := newBookingStoreFake()
		if _, err := New(store).GenerateSlots(context.Background(), tc.resourceID, tc.date); !errors.Is(err, ErrInvalidInput) || store.resourceCalls != 0 {
			t.Fatalf("input=%+v, error=%v", tc, err)
		}
	}
}

func TestGenerateSlotsAcrossDaylightSavingDays(t *testing.T) {
	for _, tc := range []struct {
		name, date string
		start, end time.Time
		want       int
	}{
		{"spring forward", "2026-03-08", time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC), time.Date(2026, 3, 9, 4, 0, 0, 0, time.UTC), 23},
		{"fall back", "2026-11-01", time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC), time.Date(2026, 11, 2, 5, 0, 0, 0, time.UTC), 25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newBookingStoreFake()
			store.resource.Timezone = "America/New_York"
			svc := New(store)
			configureForTest(t, svc, AvailabilityInput{StartTime: tc.start, EndTime: tc.end, SlotDurationMinutes: 60})
			got, err := svc.GenerateSlots(context.Background(), "resource-1", tc.date)
			if err != nil || len(got) != tc.want {
				t.Fatalf("got %d slots, want %d, error=%v", len(got), tc.want, err)
			}
			if !got[0].StartTime.Equal(tc.start) || !got[len(got)-1].EndTime.Equal(tc.end) {
				t.Fatal("generation did not cover the local day")
			}
			for i := 1; i < len(got); i++ {
				if !got[i].StartTime.Equal(got[i-1].EndTime) {
					t.Fatal("slot sequence has a gap or overlap")
				}
			}
		})
	}
}

func TestGenerateSlotsRejectsCorruptStoredConfigurations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*AvailabilityInput)
	}{
		{"zero duration", func(i *AvailabilityInput) { i.SlotDurationMinutes = 0 }},
		{"reversed window", func(i *AvailabilityInput) { i.EndTime = i.StartTime.Add(-time.Hour) }},
		{"cross-date window", func(i *AvailabilityInput) { i.EndTime = i.StartTime.Add(12 * time.Hour) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newBookingStoreFake()
			input := testAvailability()
			tc.change(&input)
			store.configs = []AvailabilityConfig{{ID: "invalid", ResourceID: "resource-1", AvailabilityInput: input}}
			got, err := New(store).GenerateSlots(context.Background(), "resource-1", "2026-11-18")
			if !errors.Is(err, ErrInvalidInput) || got != nil || len(store.generated) != 0 {
				t.Fatalf("slots=%+v, error=%v", got, err)
			}
		})
	}
}

func TestGenerateSlotsRejectsOverlappingStoredConfigurations(t *testing.T) {
	store := newBookingStoreFake()
	input := testAvailability()
	store.configs = []AvailabilityConfig{
		{ID: "config-1", ResourceID: "resource-1", AvailabilityInput: input},
		{ID: "config-2", ResourceID: "resource-1", AvailabilityInput: input},
	}
	got, err := New(store).GenerateSlots(context.Background(), "resource-1", "2026-11-18")
	if !errors.Is(err, ErrSlotOverlap) || got != nil || len(store.generated) != 0 {
		t.Fatalf("slots=%+v, error=%v", got, err)
	}
}

func TestSchedulingRejectsInvalidResourceTimezone(t *testing.T) {
	store := newBookingStoreFake()
	store.resource.Timezone = "Invalid/Timezone"
	svc := New(store)
	if _, err := svc.ConfigureAvailability(context.Background(), "resource-1", testAvailability()); err == nil {
		t.Fatal("configuration accepted invalid resource timezone")
	}
	if _, err := svc.GenerateSlots(context.Background(), "resource-1", "2026-11-18"); err == nil {
		t.Fatal("generation accepted invalid resource timezone")
	}
	if _, err := svc.ListSlots(context.Background(), "resource-1", "2026-11-18"); err == nil {
		t.Fatal("listing accepted invalid resource timezone")
	}
}

func TestGeneratedSlotsAreListed(t *testing.T) {
	store := newBookingStoreFake()
	svc := New(store)
	configureForTest(t, svc, testAvailability())
	generated, err := svc.GenerateSlots(context.Background(), "resource-1", "2026-11-18")
	if err != nil {
		t.Fatal(err)
	}
	listed, err := svc.ListSlots(context.Background(), "resource-1", "2026-11-18")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != len(generated) {
		t.Fatalf("listed %d slots, generated %d", len(listed), len(generated))
	}
	for i, slot := range listed {
		if slot.Slot != generated[i] || slot.Status != SlotAvailable {
			t.Fatalf("listed slot=%+v", slot)
		}
	}
}
