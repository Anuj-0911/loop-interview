package service

import (
	"context"
	"fmt"
)

// Scheduling data only; availability rules are implemented in the service.
type scheduleStoreFake struct {
	resource          Resource
	configs           []AvailabilityConfig
	generated         []Slot
	resourceCtx       context.Context
	resourceCalls     int
	records           []SlotRecord
	snapshotCtx       context.Context
	snapshotCalls     int
	scheduleFailStage string
	scheduleFailure   error
}

func newScheduleStoreFake() *scheduleStoreFake {
	return &scheduleStoreFake{resource: testResource()}
}

func (s *scheduleStoreFake) ReadSlots(ctx context.Context, resourceID string) (SlotSnapshot, error) {
	s.snapshotCalls++
	s.snapshotCtx = ctx
	if err := ctx.Err(); err != nil {
		return SlotSnapshot{}, err
	}
	if s.scheduleFailStage == "snapshot" {
		return SlotSnapshot{}, s.scheduleFailure
	}
	if resourceID != s.resource.ID {
		return SlotSnapshot{}, ErrResourceNotFound
	}
	records := append([]SlotRecord(nil), s.records...)
	for _, slot := range s.generated {
		records = append(records, SlotRecord{Slot: slot})
	}
	return SlotSnapshot{Resource: s.resource, Now: testNow, Slots: records}, nil
}

func (s *scheduleStoreFake) WithResource(ctx context.Context, resourceID string, fn func(ResourceTransaction) error) error {
	s.resourceCalls++
	s.resourceCtx = ctx
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.scheduleFailStage == "resourceLock" {
		return s.scheduleFailure
	}
	if resourceID != s.resource.ID {
		return ErrResourceNotFound
	}
	tx := &resourceTransactionFake{store: s, configs: append([]AvailabilityConfig(nil), s.configs...), slots: append([]Slot(nil), s.generated...)}
	if err := fn(tx); err != nil {
		return err
	}
	if s.scheduleFailStage == "resourceCommit" {
		return s.scheduleFailure
	}
	s.configs, s.generated = tx.configs, tx.slots
	return nil
}

type resourceTransactionFake struct {
	store   *scheduleStoreFake
	configs []AvailabilityConfig
	slots   []Slot
}

func (tx *resourceTransactionFake) Resource() Resource { return tx.store.resource }
func (tx *resourceTransactionFake) Configs() ([]AvailabilityConfig, error) {
	if tx.store.scheduleFailStage == "configs" {
		return nil, tx.store.scheduleFailure
	}
	return append([]AvailabilityConfig(nil), tx.configs...), nil
}
func (tx *resourceTransactionFake) InsertConfig(input AvailabilityInput) (AvailabilityConfig, error) {
	if tx.store.scheduleFailStage == "insertConfig" {
		return AvailabilityConfig{}, tx.store.scheduleFailure
	}
	config := AvailabilityConfig{ID: fmt.Sprintf("config-%d", len(tx.configs)+1), ResourceID: tx.store.resource.ID, AvailabilityInput: input}
	tx.configs = append(tx.configs, config)
	return config, nil
}
func (tx *resourceTransactionFake) Slots() ([]Slot, error) {
	if tx.store.scheduleFailStage == "slots" {
		return nil, tx.store.scheduleFailure
	}
	return append([]Slot(nil), tx.slots...), nil
}
func (tx *resourceTransactionFake) InsertSlots(slots []Slot) ([]Slot, error) {
	if tx.store.scheduleFailStage == "insertSlots" {
		return nil, tx.store.scheduleFailure
	}
	result := make([]Slot, 0, len(slots))
	for _, slot := range slots {
		slot.ID = fmt.Sprintf("generated-%d", len(tx.slots)+1)
		tx.slots = append(tx.slots, slot)
		result = append(result, slot)
	}
	return result, nil
}
