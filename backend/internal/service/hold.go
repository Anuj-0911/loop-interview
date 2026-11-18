package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const HoldDuration = 30 * time.Second

type HoldStatus string

const (
	HoldActive    HoldStatus = "ACTIVE"
	HoldReleased  HoldStatus = "RELEASED"
	HoldExpired   HoldStatus = "EXPIRED"
	HoldConfirmed HoldStatus = "CONFIRMED"
)

type Hold struct {
	ID        string
	SlotID    string
	UserID    string
	ExpiresAt time.Time
	Status    HoldStatus
}

var (
	ErrInvalidInput    = errors.New("invalid input")
	ErrSlotUnavailable = errors.New("slot unavailable")
	ErrSlotNotFound    = errors.New("slot not found")
	ErrUserNotFound    = errors.New("user not found")
)

type Service struct {
	store Store
}

func New(store Store) *Service {
	return &Service{store: store}
}

func (s *Service) CreateHold(ctx context.Context, slotID, userID string) (Hold, error) {
	if strings.TrimSpace(slotID) == "" || strings.TrimSpace(userID) == "" {
		return Hold{}, fmt.Errorf("%w: slotId and userId are required", ErrInvalidInput)
	}

	var hold Hold
	err := s.store.WithSlot(ctx, slotID, func(tx SlotTransaction) error {
		exists, err := tx.UserExists(userID)
		if err != nil {
			return err
		}
		if !exists {
			return ErrUserNotFound
		}
		occupancy, err := tx.Occupancy()
		if err != nil {
			return err
		}
		now, err := tx.Now()
		if err != nil {
			return err
		}
		if effectiveStatus(occupancy, now) != SlotAvailable {
			return ErrSlotUnavailable
		}
		hold, err = tx.InsertHold(userID, now.Add(HoldDuration))
		return err
	})
	if err != nil {
		return Hold{}, fmt.Errorf("create hold: %w", err)
	}
	return hold, nil
}
