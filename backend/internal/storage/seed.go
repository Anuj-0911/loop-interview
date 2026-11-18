package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"loop-interview/backend/internal/service"
)

// SeedDemo is repeatable. It creates three users, a room, and half-hour slots
// from 09:00 to 17:00 on today and tomorrow in the room's timezone.
func (s *Store) SeedDemo(ctx context.Context) error {
	if err := s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO users(id,name) VALUES ('user-1','Alice'),('user-2','Bob'),('user-3','Charlie') ON CONFLICT(id) DO NOTHING;
  INSERT INTO resources(id,name,type,timezone) VALUES ('room-1','Meeting Room A','ROOM','Asia/Kolkata') ON CONFLICT(id) DO NOTHING;`)
		return err
	}); err != nil {
		return fmt.Errorf("seed demo identities: %w", err)
	}
	resource, err := readResource(s.db.QueryRowContext(ctx, "SELECT id,name,type,timezone FROM resources WHERE id='room-1'"))
	if err != nil {
		return err
	}
	location, err := time.LoadLocation(resource.Timezone)
	if err != nil {
		return err
	}
	today := time.Now().In(location)
	svc := service.New(s)
	for day := range 2 {
		start := time.Date(today.Year(), today.Month(), today.Day()+day, 9, 0, 0, 0, location)
		_, err := svc.ConfigureAvailability(ctx, resource.ID, service.AvailabilityInput{StartTime: start, EndTime: start.Add(8 * time.Hour), SlotDurationMinutes: 30})
		if err != nil {
			return fmt.Errorf("seed availability: %w", err)
		}
		if _, err := svc.GenerateSlots(ctx, resource.ID, start.Format(time.DateOnly)); err != nil {
			return fmt.Errorf("seed slots: %w", err)
		}
	}
	return nil
}
