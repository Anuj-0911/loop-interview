package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"loop-interview/backend/internal/service"
)

// Store implements the service contracts with short SQLite write transactions.
type Store struct{ db *sql.DB }

var _ service.Store = (*Store)(nil)

func New(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) write(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil) // Open configures BEGIN IMMEDIATE on every connection.
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return storageError(err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", storageError(err))
	}
	return nil
}

func (s *Store) WithSlot(ctx context.Context, slotID string, fn func(service.SlotTransaction) error) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var id string
		if err := tx.QueryRowContext(ctx, "SELECT id FROM slots WHERE id=?", slotID).Scan(&id); err != nil {
			return missing(err, service.ErrSlotNotFound)
		}
		return fn(&slotTransaction{ctx: ctx, tx: tx, slotID: slotID})
	})
}

func (s *Store) WithResource(ctx context.Context, resourceID string, fn func(service.ResourceTransaction) error) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		resource, err := readResource(tx.QueryRowContext(ctx, "SELECT id,name,type,timezone FROM resources WHERE id=?", resourceID))
		if err != nil {
			return err
		}
		return fn(&resourceTransaction{ctx: ctx, tx: tx, resource: resource})
	})
}

func (s *Store) SlotIDForHold(ctx context.Context, holdID string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT slot_id FROM holds WHERE id=?", holdID).Scan(&id)
	return id, missing(err, service.ErrHoldNotFound)
}

func (s *Store) SlotIDForBooking(ctx context.Context, bookingID string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT slot_id FROM bookings WHERE id=?", bookingID).Scan(&id)
	return id, missing(err, service.ErrBookingNotFound)
}

// ReadSlots uses one SELECT, giving a consistent read snapshot without reserving
// the writer lock. LEFT JOIN retains resources with no slots and slots with no holds.
func (s *Store) ReadSlots(ctx context.Context, resourceID string) (service.SlotSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,r.name,r.type,r.timezone,
 CAST(unixepoch('subsec')*1000 AS INTEGER),
 s.id,s.availability_config_id,s.start_time_ms,s.end_time_ms,
 EXISTS(SELECT 1 FROM bookings b WHERE b.slot_id=s.id AND b.status='CONFIRMED'),
 h.id,h.user_id,h.expires_at_ms,h.status
 FROM resources r LEFT JOIN slots s ON s.resource_id=r.id
 LEFT JOIN holds h ON h.slot_id=s.id AND h.status='ACTIVE'
 WHERE r.id=? ORDER BY s.start_time_ms,s.id`, resourceID)
	if err != nil {
		return service.SlotSnapshot{}, err
	}
	defer rows.Close()
	snapshot := service.SlotSnapshot{Slots: make([]service.SlotRecord, 0)}
	indices := make(map[string]int)
	found := false
	for rows.Next() {
		var resource service.Resource
		var now int64
		var slotID, configID, holdID, userID, status sql.NullString
		var start, end, expiry sql.NullInt64
		var booked bool
		if err := rows.Scan(&resource.ID, &resource.Name, &resource.Type, &resource.Timezone, &now, &slotID, &configID, &start, &end, &booked, &holdID, &userID, &expiry, &status); err != nil {
			return service.SlotSnapshot{}, err
		}
		if !found {
			snapshot.Resource = resource
			snapshot.Now = fromMillis(now)
			found = true
		}
		if !slotID.Valid {
			continue
		}
		index, exists := indices[slotID.String]
		if !exists {
			index = len(snapshot.Slots)
			indices[slotID.String] = index
			slot := service.Slot{ID: slotID.String, ResourceID: resourceID, AvailabilityConfigID: configID.String, StartTime: fromMillis(start.Int64), EndTime: fromMillis(end.Int64)}
			snapshot.Slots = append(snapshot.Slots, service.SlotRecord{Slot: slot, Occupancy: service.SlotOccupancy{HasConfirmedBooking: booked}})
		}
		if holdID.Valid {
			snapshot.Slots[index].Occupancy.Holds = append(snapshot.Slots[index].Occupancy.Holds, service.Hold{ID: holdID.String, SlotID: slotID.String, UserID: userID.String, ExpiresAt: fromMillis(expiry.Int64), Status: service.HoldStatus(status.String)})
		}
	}
	if err := rows.Err(); err != nil {
		return service.SlotSnapshot{}, err
	}
	if !found {
		return service.SlotSnapshot{}, service.ErrResourceNotFound
	}
	return snapshot, nil
}

type slotTransaction struct {
	ctx    context.Context
	tx     *sql.Tx
	slotID string
}

func (t *slotTransaction) UserExists(userID string) (bool, error) {
	var exists bool
	err := t.tx.QueryRowContext(t.ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE id=?)", userID).Scan(&exists)
	return exists, err
}

func (t *slotTransaction) Occupancy() (service.SlotOccupancy, error) {
	var occupancy service.SlotOccupancy
	if err := t.tx.QueryRowContext(t.ctx, "SELECT EXISTS(SELECT 1 FROM bookings WHERE slot_id=? AND status='CONFIRMED')", t.slotID).Scan(&occupancy.HasConfirmedBooking); err != nil {
		return occupancy, err
	}
	rows, err := t.tx.QueryContext(t.ctx, "SELECT id,slot_id,user_id,expires_at_ms,status FROM holds WHERE slot_id=? AND status='ACTIVE'", t.slotID)
	if err != nil {
		return occupancy, err
	}
	defer rows.Close()
	for rows.Next() {
		hold, err := readHold(rows)
		if err != nil {
			return service.SlotOccupancy{}, err
		}
		occupancy.Holds = append(occupancy.Holds, hold)
	}
	return occupancy, rows.Err()
}

func (t *slotTransaction) Now() (time.Time, error) {
	var millis int64
	err := t.tx.QueryRowContext(t.ctx, "SELECT CAST(unixepoch('subsec')*1000 AS INTEGER)").Scan(&millis)
	return fromMillis(millis), err
}

func (t *slotTransaction) InsertHold(userID string, expiresAt time.Time) (service.Hold, error) {
	return readHold(t.tx.QueryRowContext(t.ctx, `INSERT INTO holds(slot_id,user_id,expires_at_ms,status) VALUES (?,?,?,'ACTIVE') RETURNING id,slot_id,user_id,expires_at_ms,status`, t.slotID, userID, expiresAt.UnixMilli()))
}

func (t *slotTransaction) Hold(holdID string) (service.Hold, error) {
	return readHold(t.tx.QueryRowContext(t.ctx, "SELECT id,slot_id,user_id,expires_at_ms,status FROM holds WHERE id=? AND slot_id=?", holdID, t.slotID))
}

func (t *slotTransaction) BookingForHold(holdID string) (service.Booking, error) {
	return readBooking(t.tx.QueryRowContext(t.ctx, "SELECT id,slot_id,user_id,hold_id,status FROM bookings WHERE hold_id=? AND slot_id=?", holdID, t.slotID))
}

func (t *slotTransaction) Booking(bookingID string) (service.Booking, error) {
	return readBooking(t.tx.QueryRowContext(t.ctx, "SELECT id,slot_id,user_id,hold_id,status FROM bookings WHERE id=? AND slot_id=?", bookingID, t.slotID))
}

func (t *slotTransaction) InsertBooking(hold service.Hold) (service.Booking, error) {
	// The FK verifies that the hold belongs to this slot and user.
	return readBooking(t.tx.QueryRowContext(t.ctx, `INSERT INTO bookings(hold_id,slot_id,user_id,status) VALUES (?,?,?,'CONFIRMED') RETURNING id,slot_id,user_id,hold_id,status`, hold.ID, t.slotID, hold.UserID))
}

func (t *slotTransaction) SetHoldStatus(holdID string, status service.HoldStatus) error {
	result, err := t.tx.ExecContext(t.ctx, "UPDATE holds SET status=? WHERE id=? AND slot_id=?", status, holdID, t.slotID)
	return changed(result, err, service.ErrHoldNotFound)
}

func (t *slotTransaction) SetBookingStatus(bookingID string, status service.BookingStatus) error {
	result, err := t.tx.ExecContext(t.ctx, "UPDATE bookings SET status=? WHERE id=? AND slot_id=?", status, bookingID, t.slotID)
	return changed(result, err, service.ErrBookingNotFound)
}

type resourceTransaction struct {
	ctx      context.Context
	tx       *sql.Tx
	resource service.Resource
}

func (t *resourceTransaction) Resource() service.Resource { return t.resource }

func (t *resourceTransaction) Configs() ([]service.AvailabilityConfig, error) {
	rows, err := t.tx.QueryContext(t.ctx, "SELECT id,resource_id,start_time_ms,end_time_ms,slot_duration_minutes FROM availability_configs WHERE resource_id=? ORDER BY start_time_ms", t.resource.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]service.AvailabilityConfig, 0)
	for rows.Next() {
		config, err := readConfig(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, config)
	}
	return result, rows.Err()
}

func (t *resourceTransaction) InsertConfig(input service.AvailabilityInput) (service.AvailabilityConfig, error) {
	return readConfig(t.tx.QueryRowContext(t.ctx, `INSERT INTO availability_configs(resource_id,start_time_ms,end_time_ms,slot_duration_minutes) VALUES (?,?,?,?) RETURNING id,resource_id,start_time_ms,end_time_ms,slot_duration_minutes`, t.resource.ID, input.StartTime.UnixMilli(), input.EndTime.UnixMilli(), input.SlotDurationMinutes))
}

func (t *resourceTransaction) Slots() ([]service.Slot, error) {
	rows, err := t.tx.QueryContext(t.ctx, "SELECT id,resource_id,availability_config_id,start_time_ms,end_time_ms FROM slots WHERE resource_id=? ORDER BY start_time_ms", t.resource.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]service.Slot, 0)
	for rows.Next() {
		slot, err := readSlot(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, slot)
	}
	return result, rows.Err()
}

func (t *resourceTransaction) InsertSlots(slots []service.Slot) ([]service.Slot, error) {
	result := make([]service.Slot, 0, len(slots))
	for _, slot := range slots {
		if slot.ResourceID != t.resource.ID {
			return nil, fmt.Errorf("%w: slot belongs to another resource", service.ErrInvalidInput)
		}
		stored, err := readSlot(t.tx.QueryRowContext(t.ctx, `INSERT INTO slots(resource_id,availability_config_id,start_time_ms,end_time_ms) VALUES (?,?,?,?) RETURNING id,resource_id,availability_config_id,start_time_ms,end_time_ms`, t.resource.ID, slot.AvailabilityConfigID, slot.StartTime.UnixMilli(), slot.EndTime.UnixMilli()))
		if err != nil {
			return nil, err
		}
		result = append(result, stored)
	}
	return result, nil
}

type scanner interface{ Scan(...any) error }

func readResource(row scanner) (service.Resource, error) {
	var r service.Resource
	err := row.Scan(&r.ID, &r.Name, &r.Type, &r.Timezone)
	return r, missing(err, service.ErrResourceNotFound)
}
func readHold(row scanner) (service.Hold, error) {
	var h service.Hold
	var expiry int64
	err := row.Scan(&h.ID, &h.SlotID, &h.UserID, &expiry, &h.Status)
	h.ExpiresAt = fromMillis(expiry)
	return h, missing(err, service.ErrHoldNotFound)
}
func readBooking(row scanner) (service.Booking, error) {
	var b service.Booking
	err := row.Scan(&b.ID, &b.SlotID, &b.UserID, &b.HoldID, &b.Status)
	return b, missing(err, service.ErrBookingNotFound)
}
func readConfig(row scanner) (service.AvailabilityConfig, error) {
	var c service.AvailabilityConfig
	var start, end int64
	err := row.Scan(&c.ID, &c.ResourceID, &start, &end, &c.SlotDurationMinutes)
	c.StartTime, c.EndTime = fromMillis(start), fromMillis(end)
	return c, storageError(err)
}
func readSlot(row scanner) (service.Slot, error) {
	var s service.Slot
	var start, end int64
	err := row.Scan(&s.ID, &s.ResourceID, &s.AvailabilityConfigID, &start, &end)
	s.StartTime, s.EndTime = fromMillis(start), fromMillis(end)
	return s, storageError(err)
}
func fromMillis(value int64) time.Time { return time.UnixMilli(value).UTC() }
func missing(err, notFound error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return notFound
	}
	return storageError(err)
}
func changed(result sql.Result, err, notFound error) error {
	if err != nil {
		return storageError(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return notFound
	}
	return nil
}
func storageError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case strings.Contains(err.Error(), "overlapping availability"):
		return fmt.Errorf("%w: %v", service.ErrAvailabilityOverlap, err)
	case strings.Contains(err.Error(), "overlapping slots"):
		return fmt.Errorf("%w: %v", service.ErrSlotOverlap, err)
	case strings.Contains(err.Error(), "UNIQUE constraint failed: bookings.slot_id"):
		return fmt.Errorf("%w: %v", service.ErrSlotUnavailable, err)
	default:
		return err
	}
}
