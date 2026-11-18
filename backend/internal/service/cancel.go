package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var ErrBookingNotActive = errors.New("booking not active")

// CancelBooking cancels the specified booking without changing its original
// hold or any subsequent booking on the slot. Repeated cancellation succeeds.
func (s *Service) CancelBooking(ctx context.Context, bookingID, userID string) error {
	if strings.TrimSpace(bookingID) == "" || strings.TrimSpace(userID) == "" {
		return fmt.Errorf("%w: bookingId and userId are required", ErrInvalidInput)
	}

	slotID, err := s.store.SlotIDForBooking(ctx, bookingID)
	if err != nil {
		return fmt.Errorf("cancel booking: %w", err)
	}
	err = s.store.WithSlot(ctx, slotID, func(tx SlotTransaction) error {
		booking, err := tx.Booking(bookingID)
		if err != nil {
			return err
		}
		if booking.UserID != userID {
			return ErrForbidden
		}
		switch booking.Status {
		case BookingCancelled:
			return nil
		case BookingConfirmed:
			return tx.SetBookingStatus(bookingID, BookingCancelled)
		default:
			return ErrBookingNotActive
		}
	})
	if err != nil {
		return fmt.Errorf("cancel booking: %w", err)
	}
	return nil
}
