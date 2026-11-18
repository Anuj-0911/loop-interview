package service

import (
	"context"
	"fmt"
	"strings"
)

// ReleaseHold relinquishes a user's hold without cancelling a confirmed booking.
// Released and expired holds are already inactive, so retries succeed unchanged.
func (s *Service) ReleaseHold(ctx context.Context, holdID, userID string) error {
	if strings.TrimSpace(holdID) == "" || strings.TrimSpace(userID) == "" {
		return fmt.Errorf("%w: holdId and userId are required", ErrInvalidInput)
	}

	slotID, err := s.store.SlotIDForHold(ctx, holdID)
	if err != nil {
		return fmt.Errorf("release hold: %w", err)
	}
	err = s.store.WithSlot(ctx, slotID, func(tx SlotTransaction) error {
		hold, err := tx.Hold(holdID)
		if err != nil {
			return err
		}
		if hold.UserID != userID {
			return ErrForbidden
		}
		switch hold.Status {
		case HoldReleased, HoldExpired:
			return nil
		case HoldActive:
			// This also safely releases an ACTIVE hold whose timestamp has expired.
			// Only this hold is updated; a newer hold on the slot is unaffected.
			return tx.SetHoldStatus(holdID, HoldReleased)
		default:
			return ErrHoldNotActive
		}
	})
	if err != nil {
		return fmt.Errorf("release hold: %w", err)
	}
	return nil
}
