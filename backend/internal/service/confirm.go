package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type BookingStatus string

const (
	BookingConfirmed BookingStatus = "CONFIRMED"
	BookingCancelled BookingStatus = "CANCELLED"
)

type Booking struct {
	ID     string
	SlotID string
	UserID string
	HoldID string
	Status BookingStatus
}

var (
	ErrHoldNotFound    = errors.New("hold not found")
	ErrForbidden       = errors.New("operation forbidden")
	ErrHoldExpired     = errors.New("hold expired")
	ErrHoldNotActive   = errors.New("hold not active")
	ErrBookingNotFound = errors.New("booking not found")
)

func (s *Service) ConfirmHold(ctx context.Context, holdID, userID string) (Booking, error) {
	if strings.TrimSpace(holdID) == "" || strings.TrimSpace(userID) == "" {
		return Booking{}, fmt.Errorf("%w: holdId and userId are required", ErrInvalidInput)
	}

	slotID, err := s.store.SlotIDForHold(ctx, holdID)
	if err != nil {
		return Booking{}, fmt.Errorf("confirm hold: %w", err)
	}
	var booking Booking
	err = s.store.WithSlot(ctx, slotID, func(tx SlotTransaction) error {
		hold, err := tx.Hold(holdID)
		if err != nil {
			return err
		}
		if hold.UserID != userID {
			return ErrForbidden
		}
		switch hold.Status {
		case HoldConfirmed:
			// A retry returns the original result, including a later cancellation.
			booking, err = tx.BookingForHold(holdID)
			return err
		case HoldExpired:
			return ErrHoldExpired
		case HoldActive:
			// Check the timestamp below; cleanup may not have run yet.
		default:
			return ErrHoldNotActive
		}

		occupancy, err := tx.Occupancy()
		if err != nil {
			return err
		}
		now, err := tx.Now()
		if err != nil {
			return err
		}
		if !hold.ExpiresAt.After(now) {
			return ErrHoldExpired
		}
		if occupancy.HasConfirmedBooking {
			return ErrSlotUnavailable
		}
		booking, err = tx.InsertBooking(hold)
		if err != nil {
			return err
		}
		return tx.SetHoldStatus(holdID, HoldConfirmed)
	})
	if err != nil {
		return Booking{}, fmt.Errorf("confirm hold: %w", err)
	}
	return booking, nil
}
