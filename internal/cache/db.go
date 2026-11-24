// Package cache owns SQLite storage: node cache, path-ref mapping,
// delta cursors, transfers, sync jobs, baselines, runs and secrets
// references. It only stores and retrieves — business rules live in
// app/sync/transfer. Tables can be rebuilt (nodes, idmap) without losing
// user data; losing transfers or baselines is not acceptable, so those
// writes are transactional.
package cache

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// DB wraps a SQLite connection with the project schema applied.
type DB struct {
	SQL *sql.DB
}

// Open opens (creating if needed) the SQLite file at path and applies
// pending migrations. Migrations only move forward.
func Open(path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("cache: open %s: %w", path, err)
	}
	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("cache: ping %s: %w", path, err)
	}
	db := &DB{SQL: sqlDB}
	if err := db.Migrate(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return db, nil
}

// Close closes the database.
func (db *DB) Close() error { return db.SQL.Close() }
