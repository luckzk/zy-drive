// SPDX-License-Identifier: MPL-2.0
package cache

import (
	"fmt"
	"time"
)

// SaveSecret stores one encrypted credential blob under id (e.g.
// "mount:<mountID>"). The blob is already encrypted by
// internal/auth; this package never sees plaintext credentials.
func (db *DB) SaveSecret(id string, ciphertext []byte) error {
	if _, err := db.SQL.Exec(`INSERT INTO secrets (id, ciphertext, key_version, updated_at)
		VALUES (?, ?, 1, ?)
		ON CONFLICT(id) DO UPDATE SET ciphertext=excluded.ciphertext,
			key_version=excluded.key_version, updated_at=excluded.updated_at`,
		id, ciphertext, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("cache: save secret %s: %w", id, err)
	}
	return nil
}

// LoadSecret returns the encrypted blob for id, or sql.ErrNoRows
// when nothing was ever stored under it.
func (db *DB) LoadSecret(id string) ([]byte, error) {
	var ct []byte
	err := db.SQL.QueryRow(`SELECT ciphertext FROM secrets WHERE id = ?`, id).Scan(&ct)
	if err != nil {
		return nil, err
	}
	return ct, nil
}

// DeleteSecret removes one stored blob. Missing rows are not an error.
func (db *DB) DeleteSecret(id string) error {
	_, err := db.SQL.Exec(`DELETE FROM secrets WHERE id = ?`, id)
	return err
}
