# Booking backend

Go service layer for the generic slot booking demo. Implemented use cases:

- Configure date-specific resource availability and generate persisted slots.
- List slots with effective AVAILABLE, HELD, or BOOKED status.
- Create a 30-second hold, confirm it, release it, and cancel a booking.
- Validate ownership and handle confirmation, release, and cancellation retries.

SQLite connections, migrations, all service storage adapters, and demo seeding
are implemented. HTTP controllers, server wiring, and the Vite frontend are also
complete. The cleanup worker is deferred; timestamp checks already enforce expiry.

## Development

Run from `backend/` with the Go version specified in `go.mod` or newer:

```sh
go test ./...
go vet ./...
go test -race ./...
```

Service tests use Go's standard `testing` package and transactional in-memory
fakes; storage tests use real temporary database files. SQLite uses the pure-Go
`modernc.org/sqlite` driver, pinned in `go.mod`. The SQLite CLI is useful for
inspection but is not required by the Go application. If the default build cache
is restricted, set `GOCACHE` to a writable temporary directory for these commands.

## Service API

| Method | Behavior |
| --- | --- |
| `ConfigureAvailability(ctx, resourceID, input)` | Add a non-overlapping availability window; exact retries reuse it. |
| `GenerateSlots(ctx, resourceID, date)` | Persist missing slots for a local date; retain existing IDs and occupancy. |
| `ListSlots(ctx, resourceID, date)` | List persisted slots in time order; an empty date lists all dates. |
| `CreateHold(ctx, slotID, userID)` | Reject occupied slots and create a 30-second hold. |
| `ConfirmHold(ctx, holdID, userID)` | Create a booking and confirm its hold atomically. |
| `ReleaseHold(ctx, holdID, userID)` | Release a hold; confirmed holds require booking cancellation. |
| `CancelBooking(ctx, bookingID, userID)` | Cancel only that booking, without affecting newer reservations. |

The dated HTTP slots endpoint will generate missing slots before listing them.
Listing itself is read-only. User IDs are simple demo identifiers, not an
authentication mechanism.

## Availability rules

- A resource has an IANA timezone. Date queries use its local `YYYY-MM-DD` date.
- Availability uses explicit start/end timestamps and is date-specific, without
  weekly recurrence. A window must stay within its start date, allowing its end
  to equal the following midnight.
- Slot duration is an integer from 1 to 1440 minutes and must fit in the window.
  A trailing fragment shorter than the duration is omitted.
- Slot lengths are elapsed time; generation accounts for 23-hour and 25-hour
  daylight-saving days. Configuration timestamps are normalized to UTC
  milliseconds to match persistence and keep retries stable.
- Intervals are half-open: adjacent windows/slots are allowed; overlap is rejected
  per resource. Configurations are immutable and additive. Editing or removing a
  window is outside this version's scope, preserving existing holds/bookings.
- Generation is idempotent and can complete partially generated windows.

## Transactions and expiry

`Store` combines `BookingStore` and `ScheduleStore`. `store.go`, `slots.go`, and
`availability.go` document the contracts for the next storage implementation.

Booking rules execute in a `WithSlot` transaction with exclusive write access. SQLite uses
short `BEGIN IMMEDIATE` transactions, serializing all writers, rather than
PostgreSQL row locks. No transaction is kept open during the 30-second checkout.
Hold/booking lookup resolves only an immutable slot reference; the record
is reread under the lock before ownership or status checks. Database wall-clock
time is read after acquiring the lock, rather than using transaction-start time.

A hold stops blocking the slot at exactly `expires_at`, independently of cleanup.
Released and expired holds do not block slots. Confirmed bookings take precedence
when deriving availability. The worker will later mark expired ACTIVE holds as
EXPIRED every five seconds.

A confirmation retry returns the original booking, including a later CANCELLED
status. Cancellation leaves the original hold CONFIRMED, so an old confirmation
cannot recreate a booking. Release/cancellation retries do not change newer
reservations. Callback or commit failures are returned without exposing
uncommitted holds/bookings.

Configuration and generation use `WithResource` transactions to serialize changes
for the same resource. Reads use a consistent snapshot of resource slots,
occupancy, and database time. Listed availability is advisory; every booking write
rechecks it under the slot lock.

## Remaining implementation order

1. **Cleanup worker:** periodic expiry updates, retry logging, and shutdown tests.
2. **Backend verification:** complete booking flows and concurrent demo users.
The Vite frontend is implemented in `../frontend`; see its README for startup
and the two-user demo flow.

Each behavior was introduced with a failing test before implementation. Service
unit tests cover lifecycle rules, expiry boundaries, retries, ownership, overlap,
local dates, daylight saving, and storage failures. Storage tests now exercise
SQLite schema constraints, connection configuration,
persistence, and migration rollback. Focused adapter tests cover the booking lifecycle, expired holds, rollback, and
12 competing hold requests across two independent connection pools.

## SQLite setup checkpoint

Initialize a database from `backend/`:

```sh
go run ./cmd/dbinit -db booking.sqlite -seed
sqlite3 booking.sqlite '.tables'
```

The initializer opens the file and applies embedded, versioned migrations in an
IMMEDIATE transaction. Repeating it preserves data. A failed migration rolls back
its schema changes and version record. Database files and their WAL/SHM files are
ignored by Git.

Connections enable WAL, foreign keys, and a five-second busy timeout. Foreign keys
and the timeout are configured for every pooled connection. The pool allows four
connections. The driver uses IMMEDIATE transactions for `BeginTx`; the read-snapshot adapter uses one SELECT statement, so listing does not reserve
the writer lock.

The initial schema contains resources, users, availability configurations, slots,
holds, and bookings. Timestamps are stored as UTC Unix milliseconds. Triggers
reject overlapping resource windows/slots and updates to immutable records or
identity fields. Foreign keys enforce resource/configuration relationships and
booking/hold ownership. Indexes enforce one confirmed booking per slot and one
booking per hold, while retaining cancelled booking history.

Timestamp-expired ACTIVE holds may coexist with replacements until cleanup. Their
exclusivity is determined by expiry checks in serialized storage transactions,
which are now implemented. No transaction remains open during checkout.

The optional `-seed` flag creates `user-1`, `user-2`, `user-3`, and `room-1`, with
30-minute slots from 09:00 to 17:00 today and tomorrow in Asia/Kolkata. Repeating
the command preserves existing holds/bookings and reuses slots.

Wire the service with `service.New(storage.New(db))` after opening and migrating
the database. The adapter implements every `service.Store` method; the HTTP
server is wired under `cmd/api`.

Driver options and SQLite behavior: [modernc driver documentation](https://pkg.go.dev/modernc.org/sqlite),
[transactions](https://www.sqlite.org/lang_transaction.html),
[foreign keys](https://www.sqlite.org/foreignkeys.html).


## Run the API

From `backend/`:

```sh
go run ./cmd/api -db booking.sqlite -seed -addr 127.0.0.1:8080
```

Startup opens SQLite, applies migrations, and optionally seeds demo data. SIGINT
and SIGTERM trigger graceful shutdown. All responses use camelCase JSON, and
booking mutations accept a JSON object containing `userId`, including DELETE.
This is a demo identity mechanism without authentication.

| Method | Path | Success |
| --- | --- | --- |
| GET | `/health` | 200, liveness status |
| GET | `/slots?resourceId=room-1&date=YYYY-MM-DD` | 200, slots; date is optional |
| GET | `/resources/{id}/slots?date=YYYY-MM-DD` | 200, generates missing slots then lists |
| PUT | `/resources/{id}/availability` | 200, configuration |
| POST | `/slots/{id}/hold` | 201, hold including `expiresAt` and `serverTime` |
| POST | `/holds/{id}/confirm` | 200, booking; retries return the same booking |
| DELETE | `/holds/{id}` | 204 |
| DELETE | `/bookings/{id}` | 204 |

The singular `/resource/{id}/...` paths from the original README are aliases for
the resource endpoints. Availability JSON contains `startTime`, `endTime` (RFC3339
with timezone), and `slotDurationMinutes`. Slot/hold/booking responses expose their
identifier as `id`.

```sh
curl 'http://127.0.0.1:8080/slots?resourceId=room-1'
curl -X POST 'http://127.0.0.1:8080/slots/SLOT_ID/hold' \
  -H 'Content-Type: application/json' -d '{"userId":"user-1"}'
curl -X POST 'http://127.0.0.1:8080/holds/HOLD_ID/confirm' \
  -H 'Content-Type: application/json' -d '{"userId":"user-1"}'
curl -X DELETE 'http://127.0.0.1:8080/bookings/BOOKING_ID' \
  -H 'Content-Type: application/json' -d '{"userId":"user-1"}'
```

Errors use `{"error":"code","message":"description"}`: invalid input/JSON is
400, missing entities 404, wrong owners 403, booking conflicts 409, and expired
holds 410. Unexpected server errors are logged without exposing database details.
Request bodies are limited to 64 KiB and unknown JSON fields are rejected.

One HTTP smoke test exercises the booking flow and essential validation/error
mapping against a real temporary SQLite database. The worker is not yet wired;
hold expiry already takes effect through timestamp checks in the service.
