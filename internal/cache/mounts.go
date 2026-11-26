// SPDX-License-Identifier: MPL-2.0
package cache

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
)

// PersistedMount is one saved mount: its metadata plus the
// non-secret driver config (endpoint, region, bucket, client_id,
// token_url, local root...). Secrets are never in here: they live
// encrypted in the secrets table under "mount:<mountID>" and are
// decrypted only in memory when the provider is rebuilt.
type PersistedMount struct {
	Mount  *domain.Mount
	Config map[string]string
}

// SaveMount upserts one mount row (metadata + non-secret config).
func (db *DB) SaveMount(m *domain.Mount, config map[string]string) error {
	if err := upsertMount(db.SQL, m, config); err != nil {
		return err
	}
	return nil
}

// SaveMountAndSecret atomically swaps a mount's row, its encrypted
// credential blob and clears its cached listings/cursors — the
// re-authorize path (V0.9.0, item 3). One transaction, so a crash
// mid-write can never leave a new config paired with an old
// secret (or the reverse). The cache is cleared because the new
// credentials may belong to a different account: old listings and
// delta cursors from the previous account must not survive.
func (db *DB) SaveMountAndSecret(m *domain.Mount, config map[string]string, secretID string, ciphertext []byte) error {
	tx, err := db.SQL.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := upsertMount(tx, m, config); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO secrets (id, ciphertext, key_version, updated_at)
		VALUES (?, ?, 1, ?)
		ON CONFLICT(id) DO UPDATE SET ciphertext=excluded.ciphertext,
			key_version=excluded.key_version, updated_at=excluded.updated_at`,
		secretID, ciphertext, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("cache: save secret %s: %w", secretID, err)
	}
	for _, q := range []string{
		`DELETE FROM nodes WHERE mount_id = ?`,
		`DELETE FROM idmap WHERE mount_id = ?`,
		`DELETE FROM cursors WHERE mount_id = ?`,
	} {
		if _, err := tx.Exec(q, m.ID); err != nil {
			return fmt.Errorf("cache: clear mount cache: %w", err)
		}
	}
	return tx.Commit()
}

type sqlExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func upsertMount(db sqlExecer, m *domain.Mount, config map[string]string) error {
	if config == nil {
		config = map[string]string{}
	}
	configJSON, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("cache: save mount: %w", err)
	}
	capsJSON, err := json.Marshal(m.Caps)
	if err != nil {
		return fmt.Errorf("cache: save mount: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	created := now
	if !m.CreatedAt.IsZero() {
		created = m.CreatedAt.UTC().Format(time.RFC3339)
	}
	updated := now
	if !m.UpdatedAt.IsZero() {
		updated = m.UpdatedAt.UTC().Format(time.RFC3339)
	}
	if _, err := db.Exec(`INSERT INTO mounts
		(id, display_name, provider, account_id, root_ref, root_label, caps_json,
		 status, group_name, sort_order, config_json, verified, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			display_name=excluded.display_name, provider=excluded.provider,
			account_id=excluded.account_id, root_ref=excluded.root_ref,
			root_label=excluded.root_label,
			caps_json=excluded.caps_json, status=excluded.status,
			group_name=excluded.group_name, config_json=excluded.config_json,
			verified=excluded.verified,
			updated_at=excluded.updated_at`,
		m.ID, m.DisplayName, string(m.Provider), m.AccountID, m.RootRef, m.RootLabel,
		string(capsJSON), string(m.Status), m.Group, m.SortOrder, string(configJSON),
		boolToInt(m.Verified),
		created, updated); err != nil {
		return fmt.Errorf("cache: save mount: %w", err)
	}
	return nil
}

// UpdateMountGroup changes only the group of a saved mount row,
// appending it at the end of the new group (sort_order = max+1
// there). Moving to the same group is a no-op: the position is
// untouched. Callers that reorder (MoveMount) use
// UpdateMountOrders instead — this is the rename/delete-migration
// path, where "append at the end" is the only sane placement.
func (db *DB) UpdateMountGroup(id, group string) error {
	_, err := db.SQL.Exec(`UPDATE mounts SET group_name = ?, sort_order = (
			SELECT COALESCE(MAX(sort_order), -1) + 1 FROM mounts WHERE group_name = ?
		), updated_at = ?
		WHERE id = ? AND group_name != ?`,
		group, group, time.Now().UTC().Format(time.RFC3339), id, group)
	return err
}

// MountOrder is one mount's (group, sort_order) assignment, as
// written by the MoveMount renumbering.
type MountOrder struct {
	ID        string
	Group     string
	SortOrder int
}

// UpdateMountOrders rewrites group + sort_order for the given
// mounts in one transaction (the MoveMount renumbering: the moved
// mount plus every mount whose position shifted in either group).
func (db *DB) UpdateMountOrders(orders []MountOrder) error {
	tx, err := db.SQL.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, o := range orders {
		if _, err := tx.Exec(`UPDATE mounts SET group_name = ?, sort_order = ?, updated_at = ?
			WHERE id = ?`, o.Group, o.SortOrder, now, o.ID); err != nil {
			return fmt.Errorf("cache: update mount order: %w", err)
		}
	}
	return tx.Commit()
}

// ListMounts returns all saved mounts in sidebar order: sort_order
// inside each group (dense 0..n-1), creation order as tie-break.
func (db *DB) ListMounts() ([]PersistedMount, error) {
	rows, err := db.SQL.Query(`SELECT id, display_name, provider, account_id,
		root_ref, root_label, caps_json, status, group_name, sort_order, config_json, verified, created_at, updated_at
		FROM mounts ORDER BY sort_order ASC, created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("cache: list mounts: %w", err)
	}
	defer rows.Close()
	var out []PersistedMount
	for rows.Next() {
		var m domain.Mount
		var provider, capsJSON, status, configJSON string
		var verified int
		var created, updated string
		if err := rows.Scan(&m.ID, &m.DisplayName, &provider, &m.AccountID,
			&m.RootRef, &m.RootLabel, &capsJSON, &status, &m.Group, &m.SortOrder, &configJSON,
			&verified,
			&created, &updated); err != nil {
			return nil, fmt.Errorf("cache: list mounts: %w", err)
		}
		m.Provider = domain.ProviderKind(provider)
		m.Status = domain.MountStatus(status)
		m.Verified = verified != 0
		if err := json.Unmarshal([]byte(capsJSON), &m.Caps); err != nil {
			return nil, fmt.Errorf("cache: list mounts: bad caps_json: %w", err)
		}
		var cfg map[string]string
		if configJSON != "" {
			if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
				return nil, fmt.Errorf("cache: list mounts: bad config_json: %w", err)
			}
		}
		if cfg == nil {
			cfg = map[string]string{}
		}
		m.CreatedAt, _ = time.Parse(time.RFC3339, created)
		m.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
		out = append(out, PersistedMount{Mount: &m, Config: cfg})
	}
	return out, rows.Err()
}

// DeleteMount removes a mount row and its cached listings/cursors.
// It never touches cloud data; the caller also deletes the secret.
// The rest of the group closes ranks (sort_order > the deleted
// one shifts down by 1), so sort_order stays dense 0..n-1.
func (db *DB) DeleteMount(id string) error {
	tx, err := db.SQL.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var group string
	var sortOrder int
	err = tx.QueryRow(`SELECT group_name, sort_order FROM mounts WHERE id = ?`, id).Scan(&group, &sortOrder)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("cache: delete mount: %w", err)
	}
	for _, q := range []string{
		`DELETE FROM mounts WHERE id = ?`,
		`DELETE FROM nodes WHERE mount_id = ?`,
		`DELETE FROM idmap WHERE mount_id = ?`,
		`DELETE FROM cursors WHERE mount_id = ?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return fmt.Errorf("cache: delete mount: %w", err)
		}
	}
	if err == nil {
		if _, err := tx.Exec(`UPDATE mounts SET sort_order = sort_order - 1
			WHERE group_name = ? AND sort_order > ?`, group, sortOrder); err != nil {
			return fmt.Errorf("cache: delete mount: %w", err)
		}
	}
	return tx.Commit()
}
