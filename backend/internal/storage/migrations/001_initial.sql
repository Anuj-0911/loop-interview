-- Timestamps are UTC Unix milliseconds. Availability and slot intervals are [start,end).
CREATE TABLE users (
    id TEXT NOT NULL PRIMARY KEY,
    name TEXT NOT NULL
) STRICT;

CREATE TABLE resources (
    id TEXT NOT NULL PRIMARY KEY,
    name TEXT NOT NULL,
    type TEXT NOT NULL,
    timezone TEXT NOT NULL
) STRICT;

CREATE TABLE availability_configs (
    id TEXT NOT NULL PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    resource_id TEXT NOT NULL REFERENCES resources(id),
    start_time_ms INTEGER NOT NULL,
    end_time_ms INTEGER NOT NULL,
    slot_duration_minutes INTEGER NOT NULL CHECK (slot_duration_minutes BETWEEN 1 AND 1440),
    CHECK (end_time_ms > start_time_ms),
    CHECK (end_time_ms - start_time_ms >= slot_duration_minutes * 60000),
    UNIQUE (id, resource_id)
) STRICT;
CREATE INDEX availability_resource ON availability_configs(resource_id, start_time_ms);

CREATE TRIGGER availability_no_overlap BEFORE INSERT ON availability_configs
WHEN EXISTS (
    SELECT 1 FROM availability_configs
    WHERE resource_id = NEW.resource_id
      AND start_time_ms < NEW.end_time_ms AND NEW.start_time_ms < end_time_ms
)
BEGIN
    SELECT RAISE(ABORT, 'overlapping availability');
END;

CREATE TRIGGER availability_immutable BEFORE UPDATE ON availability_configs
BEGIN
    SELECT RAISE(ABORT, 'availability is immutable');
END;

CREATE TABLE slots (
    id TEXT NOT NULL PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    resource_id TEXT NOT NULL REFERENCES resources(id),
    availability_config_id TEXT NOT NULL,
    start_time_ms INTEGER NOT NULL,
    end_time_ms INTEGER NOT NULL,
    CHECK (end_time_ms > start_time_ms),
    FOREIGN KEY (availability_config_id, resource_id) REFERENCES availability_configs(id, resource_id),
    UNIQUE (resource_id, start_time_ms, end_time_ms)
) STRICT;

CREATE TRIGGER slots_within_availability BEFORE INSERT ON slots
WHEN EXISTS (
    SELECT 1 FROM availability_configs WHERE id = NEW.availability_config_id
      AND (NEW.start_time_ms < start_time_ms OR NEW.end_time_ms > end_time_ms)
)
BEGIN
    SELECT RAISE(ABORT, 'slot outside availability');
END;

CREATE TRIGGER slots_no_overlap BEFORE INSERT ON slots
WHEN EXISTS (
    SELECT 1 FROM slots WHERE resource_id = NEW.resource_id
      AND start_time_ms < NEW.end_time_ms AND NEW.start_time_ms < end_time_ms
)
BEGIN
    SELECT RAISE(ABORT, 'overlapping slots');
END;

CREATE TRIGGER slots_immutable BEFORE UPDATE ON slots
BEGIN
    SELECT RAISE(ABORT, 'slots are immutable');
END;

CREATE TABLE holds (
    id TEXT NOT NULL PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    slot_id TEXT NOT NULL REFERENCES slots(id),
    user_id TEXT NOT NULL REFERENCES users(id),
    expires_at_ms INTEGER NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('ACTIVE', 'RELEASED', 'EXPIRED', 'CONFIRMED')),
    UNIQUE (id, slot_id, user_id)
) STRICT;
CREATE INDEX holds_slot ON holds(slot_id);
CREATE INDEX holds_expiry ON holds(expires_at_ms) WHERE status = 'ACTIVE';

-- Timestamp-expired ACTIVE rows may coexist with replacements until cleanup.
-- Valid hold exclusivity is enforced by short IMMEDIATE storage transactions.
CREATE TRIGGER holds_identity_immutable
BEFORE UPDATE OF id, slot_id, user_id, expires_at_ms ON holds
BEGIN
    SELECT RAISE(ABORT, 'hold identity is immutable');
END;

CREATE TABLE bookings (
    id TEXT NOT NULL PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    hold_id TEXT NOT NULL UNIQUE,
    slot_id TEXT NOT NULL REFERENCES slots(id),
    user_id TEXT NOT NULL REFERENCES users(id),
    status TEXT NOT NULL CHECK (status IN ('CONFIRMED', 'CANCELLED')),
    FOREIGN KEY (hold_id, slot_id, user_id) REFERENCES holds(id, slot_id, user_id)
) STRICT;
CREATE UNIQUE INDEX bookings_one_confirmed_per_slot ON bookings(slot_id) WHERE status = 'CONFIRMED';

CREATE TRIGGER bookings_identity_immutable
BEFORE UPDATE OF id, hold_id, slot_id, user_id ON bookings
BEGIN
    SELECT RAISE(ABORT, 'booking identity is immutable');
END;
