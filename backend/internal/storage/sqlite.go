package storage

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/001_initial.sql
var initialSchema string

// Open opens a file-backed SQLite database. Foreign keys and busy timeout are
// configured on every pooled connection; BeginTx uses BEGIN IMMEDIATE. Call
// Migrate separately before using the schema. The caller owns Close.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("database path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("database path: %w", err)
	}
	query := url.Values{}
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "busy_timeout(5000)")
	query.Set("_txlock", "immediate")
	uri := url.URL{Scheme: "file", Path: absolute, RawQuery: query.Encode()}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, fmt.Errorf("open SQLite: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect SQLite: %w", err)
	}
	var journal string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&journal); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	if journal != "wal" {
		db.Close()
		return nil, fmt.Errorf("expected WAL mode, got %q", journal)
	}
	return db, nil
}

// Migrate applies the versioned schema atomically. The IMMEDIATE transaction
// serializes concurrent initializers. Failed migrations leave no partial schema.
func Migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY) STRICT`); err != nil {
		return fmt.Errorf("create migration history: %w", err)
	}
	var version int
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(version),0) FROM schema_migrations").Scan(&version); err != nil {
		return fmt.Errorf("read migration version: %w", err)
	}
	if version > 1 {
		return fmt.Errorf("database schema version %d is newer than supported version 1", version)
	}
	if version == 0 {
		if _, err := tx.ExecContext(ctx, initialSchema); err != nil {
			return fmt.Errorf("apply migration 1: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version) VALUES (1)"); err != nil {
			return fmt.Errorf("record migration 1: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}
