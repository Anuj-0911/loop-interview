package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

var (
	ErrAvailabilityOverlap = errors.New("availability windows overlap")
	ErrSlotOverlap         = errors.New("slots overlap")
)

type AvailabilityInput struct {
	StartTime           time.Time
	EndTime             time.Time
	SlotDurationMinutes int
}

type AvailabilityConfig struct {
	ID         string
	ResourceID string
	AvailabilityInput
}

// ResourceTransaction is scoped to the resource locked by WithResource.
// Configs and Slots return all its persisted records. Inserts generate IDs and
// return persisted records in input order. All calls inherit the request context.
type ResourceTransaction interface {
	Resource() Resource
	Configs() ([]AvailabilityConfig, error)
	InsertConfig(input AvailabilityInput) (AvailabilityConfig, error)
	Slots() ([]Slot, error)
	InsertSlots(slots []Slot) ([]Slot, error)
}

// ConfigureAvailability adds an immutable window on one resource-local date.
// Exact retries reuse the configuration. Overlapping changes are rejected so
// that existing holds and bookings are never invalidated by reconfiguration.
func (s *Service) ConfigureAvailability(ctx context.Context, resourceID string, input AvailabilityInput) (AvailabilityConfig, error) {
	if err := validateResourceDate(resourceID, "", false); err != nil {
		return AvailabilityConfig{}, err
	}
	if err := validateWindow(input); err != nil {
		return AvailabilityConfig{}, err
	}
	// Canonical precision must match persistence so exact retries compare equal.
	input.StartTime = input.StartTime.UTC().Truncate(time.Millisecond)
	input.EndTime = input.EndTime.UTC().Truncate(time.Millisecond)
	var config AvailabilityConfig
	err := s.store.WithResource(ctx, resourceID, func(tx ResourceTransaction) error {
		if err := validateLocalWindow(input, tx.Resource().Timezone); err != nil {
			return err
		}
		configs, err := tx.Configs()
		if err != nil {
			return err
		}
		for _, existing := range configs {
			if sameWindow(existing.AvailabilityInput, input) {
				config = existing
				return nil
			}
			if overlaps(input.StartTime, input.EndTime, existing.StartTime, existing.EndTime) {
				return ErrAvailabilityOverlap
			}
		}
		config, err = tx.InsertConfig(input)
		return err
	})
	if err != nil {
		return AvailabilityConfig{}, fmt.Errorf("configure availability: %w", err)
	}
	return config, nil
}

// GenerateSlots persists complete slots for a resource-local YYYY-MM-DD date.
// A trailing fragment shorter than the configured duration is omitted. Existing
// slots are reused, preserving IDs and occupancy across repeated generation.
func (s *Service) GenerateSlots(ctx context.Context, resourceID, date string) ([]Slot, error) {
	if err := validateResourceDate(resourceID, date, true); err != nil {
		return nil, err
	}
	result := make([]Slot, 0)
	err := s.store.WithResource(ctx, resourceID, func(tx ResourceTransaction) error {
		resource := tx.Resource()
		location, err := time.LoadLocation(resource.Timezone)
		if err != nil {
			return fmt.Errorf("resource timezone: %w", err)
		}
		configs, err := tx.Configs()
		if err != nil {
			return err
		}
		existing, err := tx.Slots()
		if err != nil {
			return err
		}
		pending := make([]Slot, 0)
		for _, config := range configs {
			if config.StartTime.In(location).Format(time.DateOnly) != date {
				continue
			}
			if err := validateWindow(config.AvailabilityInput); err != nil {
				return err
			}
			if err := validateLocalWindow(config.AvailabilityInput, resource.Timezone); err != nil {
				return err
			}
			duration := time.Duration(config.SlotDurationMinutes) * time.Minute
			for start := config.StartTime.UTC(); !start.Add(duration).After(config.EndTime); start = start.Add(duration) {
				candidate := Slot{ResourceID: resourceID, AvailabilityConfigID: config.ID, StartTime: start, EndTime: start.Add(duration)}
				found := false
				for _, slot := range existing {
					if sameSlotWindow(slot, candidate) && slot.AvailabilityConfigID == config.ID {
						result = append(result, slot)
						found = true
						continue
					}
					if overlaps(candidate.StartTime, candidate.EndTime, slot.StartTime, slot.EndTime) {
						return ErrSlotOverlap
					}
				}
				if found {
					continue
				}
				for _, slot := range pending {
					if overlaps(candidate.StartTime, candidate.EndTime, slot.StartTime, slot.EndTime) {
						return ErrSlotOverlap
					}
				}
				pending = append(pending, candidate)
			}
		}
		if len(pending) > 0 {
			inserted, err := tx.InsertSlots(pending)
			if err != nil {
				return err
			}
			result = append(result, inserted...)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("generate slots: %w", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].StartTime.Before(result[j].StartTime) })
	return result, nil
}

func validateWindow(input AvailabilityInput) error {
	if input.StartTime.IsZero() || input.EndTime.IsZero() || !input.EndTime.After(input.StartTime) {
		return fmt.Errorf("%w: a positive availability window is required", ErrInvalidInput)
	}
	if input.SlotDurationMinutes < 1 || input.SlotDurationMinutes > 1440 {
		return fmt.Errorf("%w: slot duration must be between 1 and 1440 minutes", ErrInvalidInput)
	}
	if input.EndTime.Sub(input.StartTime) < time.Duration(input.SlotDurationMinutes)*time.Minute {
		return fmt.Errorf("%w: slot duration exceeds the availability window", ErrInvalidInput)
	}
	return nil
}

func validateLocalWindow(input AvailabilityInput, timezone string) error {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return fmt.Errorf("resource timezone: %w", err)
	}
	start := input.StartTime.In(location)
	nextDay := time.Date(start.Year(), start.Month(), start.Day()+1, 0, 0, 0, 0, location)
	if input.EndTime.After(nextDay) {
		return fmt.Errorf("%w: window must end within its resource-local date or at the following midnight", ErrInvalidInput)
	}
	return nil
}

func sameWindow(a, b AvailabilityInput) bool {
	return a.StartTime.Equal(b.StartTime) && a.EndTime.Equal(b.EndTime) && a.SlotDurationMinutes == b.SlotDurationMinutes
}

func sameSlotWindow(a, b Slot) bool {
	return a.ResourceID == b.ResourceID && a.StartTime.Equal(b.StartTime) && a.EndTime.Equal(b.EndTime)
}

// Half-open intervals allow adjacent windows without treating them as overlap.
func overlaps(aStart, aEnd, bStart, bEnd time.Time) bool {
	return aStart.Before(bEnd) && bStart.Before(aEnd)
}
