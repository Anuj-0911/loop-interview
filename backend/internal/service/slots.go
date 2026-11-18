package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var ErrResourceNotFound = errors.New("resource not found")

type Resource struct {
	ID       string
	Name     string
	Type     string
	Timezone string // IANA timezone used for resource-local date queries.
}

type Slot struct {
	ID                   string
	ResourceID           string
	AvailabilityConfigID string
	StartTime            time.Time
	EndTime              time.Time
}

type SlotStatus string

const (
	SlotAvailable SlotStatus = "AVAILABLE"
	SlotHeld      SlotStatus = "HELD"
	SlotBooked    SlotStatus = "BOOKED"
)

type SlotView struct {
	Slot   Slot
	Status SlotStatus
}

type SlotRecord struct {
	Slot      Slot
	Occupancy SlotOccupancy
}

// SlotSnapshot is a consistent resource-scoped read of persisted slots, their
// occupancy, and current database time. It never exposes other resources' slots.
type SlotSnapshot struct {
	Resource Resource
	Now      time.Time
	Slots    []SlotRecord
}

type ScheduleStore interface {
	// ReadSlots returns ErrResourceNotFound for an unknown resource. Availability
	// remains advisory: writes must recheck it under the slot lock.
	ReadSlots(ctx context.Context, resourceID string) (SlotSnapshot, error)
	// WithResource locks the resource before reading configs or creating slots.
	// Missing resources return ErrResourceNotFound. Callback errors roll back;
	// nil is returned only after commit. Configs and slots are immutable once
	// created. Database constraints must also prevent duplicate/overlapping slots.
	WithResource(ctx context.Context, resourceID string, fn func(ResourceTransaction) error) error
}

// ListSlots returns all persisted slots, optionally filtered by the resource's
// local YYYY-MM-DD start date. Expired holds are inactive without cleanup.
func (s *Service) ListSlots(ctx context.Context, resourceID, date string) ([]SlotView, error) {
	if err := validateResourceDate(resourceID, date, false); err != nil {
		return nil, err
	}
	snapshot, err := s.store.ReadSlots(ctx, resourceID)
	if err != nil {
		return nil, fmt.Errorf("list slots: %w", err)
	}
	location, err := time.LoadLocation(snapshot.Resource.Timezone)
	if err != nil {
		return nil, fmt.Errorf("resource timezone: %w", err)
	}
	result := make([]SlotView, 0, len(snapshot.Slots))
	for _, record := range snapshot.Slots {
		if date != "" && record.Slot.StartTime.In(location).Format(time.DateOnly) != date {
			continue
		}
		result = append(result, SlotView{Slot: record.Slot, Status: effectiveStatus(record.Occupancy, snapshot.Now)})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Slot.StartTime.Equal(result[j].Slot.StartTime) {
			return result[i].Slot.ID < result[j].Slot.ID
		}
		return result[i].Slot.StartTime.Before(result[j].Slot.StartTime)
	})
	return result, nil
}

func effectiveStatus(occupancy SlotOccupancy, now time.Time) SlotStatus {
	if occupancy.HasConfirmedBooking {
		return SlotBooked
	}
	for _, hold := range occupancy.Holds {
		if hold.Status == HoldActive && hold.ExpiresAt.After(now) {
			return SlotHeld
		}
	}
	return SlotAvailable
}

func validateResourceDate(resourceID, date string, required bool) error {
	if strings.TrimSpace(resourceID) == "" {
		return fmt.Errorf("%w: resourceId is required", ErrInvalidInput)
	}
	if date == "" && !required {
		return nil
	}
	parsed, err := time.Parse(time.DateOnly, date)
	if err != nil || parsed.Format(time.DateOnly) != date {
		return fmt.Errorf("%w: date must be YYYY-MM-DD", ErrInvalidInput)
	}
	return nil
}
