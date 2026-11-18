package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "booking.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db
}

func execSQL(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func rejectSQL(t *testing.T, db *sql.DB, query, message string, args ...any) {
	t.Helper()
	_, err := db.Exec(query, args...)
	if err == nil || !strings.Contains(err.Error(), message) {
		t.Fatalf("error=%v, want rejection containing %q", err, message)
	}
}

func seedSchemaFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	execSQL(t, db, `INSERT INTO users(id,name) VALUES ('user-1','Alice'),('user-2','Bob');
 INSERT INTO resources(id,name,type,timezone) VALUES ('resource-1','Room A','ROOM','Asia/Kolkata'),('resource-2','Room B','ROOM','Asia/Kolkata');
 INSERT INTO availability_configs(id,resource_id,start_time_ms,end_time_ms,slot_duration_minutes)
 VALUES ('config-1','resource-1',0,7200000,30),('config-2','resource-2',0,7200000,30);
 INSERT INTO slots(id,resource_id,availability_config_id,start_time_ms,end_time_ms)
 VALUES ('slot-1','resource-1','config-1',0,1800000),('slot-2','resource-1','config-1',1800000,3600000),('slot-3','resource-2','config-2',0,1800000);
 INSERT INTO holds(id,slot_id,user_id,expires_at_ms,status)
 VALUES ('hold-1','slot-1','user-1',30000,'ACTIVE'),('hold-2','slot-1','user-2',60000,'ACTIVE'),('hold-3','slot-2','user-1',30000,'ACTIVE');`)
}

func TestOpenConfiguresEveryConnection(t *testing.T) {
	db := openTestDB(t)
	var journal string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil || journal != "wal" {
		t.Fatalf("journal=%s, error=%v", journal, err)
	}
	var connections []*sql.Conn
	for range 3 {
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, conn)
		defer conn.Close()
		for _, tc := range []struct {
			query string
			want  int
		}{{"PRAGMA foreign_keys", 1}, {"PRAGMA busy_timeout", 5000}} {
			var value int
			if err := conn.QueryRowContext(context.Background(), tc.query).Scan(&value); err != nil || value != tc.want {
				t.Fatalf("%s=%d, error=%v", tc.query, value, err)
			}
		}
	}
}

func TestMigrationIsRepeatableAndDataPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "booking.sqlite")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	execSQL(t, db, "INSERT INTO users(id,name) VALUES ('user-1','Alice')")
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := Migrate(context.Background(), reopened); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := reopened.QueryRow("SELECT name FROM users WHERE id='user-1'").Scan(&name); err != nil || name != "Alice" {
		t.Fatalf("name=%s, error=%v", name, err)
	}
	var version, count int
	if err := reopened.QueryRow("SELECT MAX(version),COUNT(*) FROM schema_migrations").Scan(&version, &count); err != nil || version != 1 || count != 1 {
		t.Fatalf("version=%d, count=%d, error=%v", version, count, err)
	}
}

func TestSchemaRejectsOverlapAndAllowsAdjacentSlotsAndDifferentResources(t *testing.T) {
	db := openTestDB(t)
	seedSchemaFixture(t, db)
	rejectSQL(t, db, `INSERT INTO slots(id,resource_id,availability_config_id,start_time_ms,end_time_ms) VALUES ('overlap','resource-1','config-1',900000,2700000)`, "overlapping slots")
	rejectSQL(t, db, `INSERT INTO slots(id,resource_id,availability_config_id,start_time_ms,end_time_ms) VALUES ('duplicate','resource-1','config-1',0,1800000)`, "overlapping slots")
	execSQL(t, db, `INSERT INTO slots(id,resource_id,availability_config_id,start_time_ms,end_time_ms) VALUES ('adjacent','resource-1','config-1',3600000,5400000)`)
	rejectSQL(t, db, `INSERT INTO availability_configs(id,resource_id,start_time_ms,end_time_ms,slot_duration_minutes) VALUES ('overlap','resource-1',3600000,10800000,30)`, "overlapping availability")
	execSQL(t, db, `INSERT INTO availability_configs(id,resource_id,start_time_ms,end_time_ms,slot_duration_minutes) VALUES ('adjacent','resource-1',7200000,10800000,30)`)
}

func TestSchemaProtectsBookingUniquenessAndOwnership(t *testing.T) {
	db := openTestDB(t)
	seedSchemaFixture(t, db)
	execSQL(t, db, `INSERT INTO bookings(id,hold_id,slot_id,user_id,status) VALUES ('booking-1','hold-1','slot-1','user-1','CONFIRMED')`)
	rejectSQL(t, db, `INSERT INTO bookings(id,hold_id,slot_id,user_id,status) VALUES ('booking-2','hold-2','slot-1','user-2','CONFIRMED')`, "UNIQUE constraint failed: bookings.slot_id")
	execSQL(t, db, `UPDATE bookings SET status='CANCELLED' WHERE id='booking-1'`)
	execSQL(t, db, `INSERT INTO bookings(id,hold_id,slot_id,user_id,status) VALUES ('booking-2','hold-2','slot-1','user-2','CONFIRMED')`)
	rejectSQL(t, db, `INSERT INTO bookings(id,hold_id,slot_id,user_id,status) VALUES ('retry','hold-1','slot-1','user-1','CANCELLED')`, "UNIQUE constraint failed: bookings.hold_id")
	rejectSQL(t, db, `UPDATE bookings SET status='CONFIRMED' WHERE id='booking-1'`, "UNIQUE constraint failed: bookings.slot_id")
	rejectSQL(t, db, `INSERT INTO bookings(id,hold_id,slot_id,user_id,status) VALUES ('wrong-owner','hold-3','slot-2','user-2','CONFIRMED')`, "FOREIGN KEY constraint failed")
	rejectSQL(t, db, `INSERT INTO bookings(id,hold_id,slot_id,user_id,status) VALUES ('wrong-slot','hold-3','slot-3','user-1','CONFIRMED')`, "FOREIGN KEY constraint failed")
}

func TestSchemaValidatesReferencesWindowsAndStatuses(t *testing.T) {
	db := openTestDB(t)
	seedSchemaFixture(t, db)
	for _, tc := range []struct{ sql, message string }{
		{`INSERT INTO holds(slot_id,user_id,expires_at_ms,status) VALUES ('slot-1','missing',30000,'ACTIVE')`, "FOREIGN KEY constraint failed"},
		{`INSERT INTO holds(slot_id,user_id,expires_at_ms,status) VALUES ('missing','user-1',30000,'ACTIVE')`, "FOREIGN KEY constraint failed"},
		{`INSERT INTO slots(resource_id,availability_config_id,start_time_ms,end_time_ms) VALUES ('resource-2','config-1',3600000,5400000)`, "FOREIGN KEY constraint failed"},
		{`INSERT INTO slots(resource_id,availability_config_id,start_time_ms,end_time_ms) VALUES ('resource-1','config-1',7200000,9000000)`, "slot outside availability"},
		{`INSERT INTO slots(resource_id,availability_config_id,start_time_ms,end_time_ms) VALUES ('resource-1','config-1',5400000,5400000)`, "CHECK constraint failed"},
		{`INSERT INTO availability_configs(resource_id,start_time_ms,end_time_ms,slot_duration_minutes) VALUES ('resource-1',9000000,10800000,0)`, "CHECK constraint failed"},
		{`INSERT INTO availability_configs(resource_id,start_time_ms,end_time_ms,slot_duration_minutes) VALUES ('resource-1',9000000,10800000,60)`, "CHECK constraint failed"},
		{`UPDATE holds SET status='UNKNOWN' WHERE id='hold-1'`, "CHECK constraint failed"},
		{`INSERT INTO bookings(hold_id,slot_id,user_id,status) VALUES ('hold-3','slot-2','user-1','UNKNOWN')`, "CHECK constraint failed"},
	} {
		rejectSQL(t, db, tc.sql, tc.message)
	}
}

func TestSchemaKeepsSlotReferencesImmutable(t *testing.T) {
	db := openTestDB(t)
	seedSchemaFixture(t, db)
	execSQL(t, db, `INSERT INTO bookings(id,hold_id,slot_id,user_id,status) VALUES ('booking-1','hold-1','slot-1','user-1','CONFIRMED')`)
	for _, tc := range []struct{ sql, message string }{
		{`UPDATE slots SET start_time_ms=1 WHERE id='slot-1'`, "slots are immutable"},
		{`UPDATE availability_configs SET slot_duration_minutes=15 WHERE id='config-1'`, "availability is immutable"},
		{`UPDATE holds SET slot_id='slot-2' WHERE id='hold-2'`, "hold identity is immutable"},
		{`UPDATE holds SET expires_at_ms=90000 WHERE id='hold-2'`, "hold identity is immutable"},
		{`UPDATE bookings SET slot_id='slot-2' WHERE id='booking-1'`, "booking identity is immutable"},
	} {
		rejectSQL(t, db, tc.sql, tc.message)
	}
}

func TestOpenRejectsEmptyAndUnwritablePaths(t *testing.T) {
	if db, err := Open(context.Background(), ""); err == nil {
		db.Close()
		t.Fatal("empty path accepted")
	}
	if db, err := Open(context.Background(), t.TempDir()); err == nil {
		db.Close()
		t.Fatal("directory accepted as database file")
	}
}

func TestMigrationRejectsNewerSchema(t *testing.T) {
	db := openTestDB(t)
	execSQL(t, db, "INSERT INTO schema_migrations(version) VALUES (2)")
	if err := Migrate(context.Background(), db); err == nil {
		t.Fatal("newer schema accepted")
	}
}

func TestFailedMigrationRollsBackDDLAndCanBeRetried(t *testing.T) {
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "booking.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Force failure after the migration has already created earlier tables.
	execSQL(t, db, "CREATE TABLE holds(marker TEXT)")
	if err := Migrate(context.Background(), db); err == nil {
		t.Fatal("conflicting table should fail migration")
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('resources','schema_migrations')").Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial migration survived: count=%d, error=%v", count, err)
	}
	execSQL(t, db, "DROP TABLE holds")
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
}

func TestOpenCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if db, err := Open(ctx, filepath.Join(t.TempDir(), "booking.sqlite")); err == nil {
		db.Close()
		t.Fatal("cancelled open succeeded")
	}
}
