package service

import (
	"context"
	"time"
)

// BookingStore provides an atomic transaction under an exclusive slot lock.
// WithSlot returns ErrSlotNotFound for a missing slot, rolls back on callback
// error, and returns nil only after a successful commit. Every operation that
// changes slot occupancy must acquire the same lock. The transaction inherits
// the request context. The callback must not be retried by the implementation.
type BookingStore interface {
	WithSlot(ctx context.Context, slotID string, fn func(SlotTransaction) error) error
	// SlotIDForHold resolves an immutable slot reference without locking the hold.
	// Operations must reread the hold inside WithSlot; this lookup alone cannot
	// establish its current status, ownership, or expiry.
	SlotIDForHold(ctx context.Context, holdID string) (string, error)
	// SlotIDForBooking resolves an immutable slot reference. Cancellation must
	// reread the booking under WithSlot before checking ownership and status.
	SlotIDForBooking(ctx context.Context, bookingID string) (string, error)
}

// SlotOccupancy contains the locked slot's confirmed booking flag and holds.
// Storage may omit inactive holds, but must include every ACTIVE hold even if
// its expires_at is in the past: the service decides expiry using database time.
type SlotOccupancy struct {
	HasConfirmedBooking bool
	Holds               []Hold
}

// SlotTransaction is valid only within a WithSlot callback. All queries and
// inserts belong to that transaction and its locked slot. Now must read current
// database wall-clock time after locking, rather than transaction-start time.
type SlotTransaction interface {
	UserExists(userID string) (bool, error)
	Occupancy() (SlotOccupancy, error)
	Now() (time.Time, error)
	// InsertHold generates an ID and inserts an ACTIVE hold for the locked slot.
	InsertHold(userID string, expiresAt time.Time) (Hold, error)
	// Hold rereads a hold belonging to the locked slot, or returns ErrHoldNotFound.
	Hold(holdID string) (Hold, error)
	// BookingForHold returns the original booking regardless of booking status.
	BookingForHold(holdID string) (Booking, error)
	// InsertBooking creates a CONFIRMED booking linked to the hold and locked slot.
	InsertBooking(hold Hold) (Booking, error)
	SetHoldStatus(holdID string, status HoldStatus) error
	// Booking reads by booking ID within the locked slot, or ErrBookingNotFound.
	Booking(bookingID string) (Booking, error)
	// SetBookingStatus updates only the specified booking in the locked slot.
	SetBookingStatus(bookingID string, status BookingStatus) error
}

type Store interface {
	BookingStore
	ScheduleStore
}
