// SPDX-License-Identifier: MPL-2.0
// Package cache — settings are plain key/value rows in the
// pre-existing `settings` table (created by migration v1). No new
// table is needed: GetSetting returns ("", false) for a missing key
// and the app layer applies its defaults.
package cache

import (
	"database/sql"
	"errors"
	"fmt"
)

// GetSetting reads one setting. ok is false when the key was never
// saved; that is not an error — the caller applies its default.
func (db *DB) GetSetting(key string) (value string, ok bool, err error) {
	err = db.SQL.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("cache: get setting %q: %w", key, err)
	}
	return value, true, nil
}

// SetSetting upserts one setting.
func (db *DB) SetSetting(key, value string) error {
	if _, err := db.SQL.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
		return fmt.Errorf("cache: set setting %q: %w", key, err)
	}
	return nil
}
