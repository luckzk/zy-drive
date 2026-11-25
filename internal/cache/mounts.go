package cache

import (
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
	if _, err := db.SQL.Exec(`INSERT INTO mounts
		(id, display_name, provider, account_id, root_ref, root_label, caps_json,
		 status, group_name, config_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			display_name=excluded.display_name, provider=excluded.provider,
			account_id=excluded.account_id, root_ref=excluded.root_ref,
			root_label=excluded.root_label,
			caps_json=excluded.caps_json, status=excluded.status,
			group_name=excluded.group_name, config_json=excluded.config_json,
			updated_at=excluded.updated_at`,
		m.ID, m.DisplayName, string(m.Provider), m.AccountID, m.RootRef, m.RootLabel,
		string(capsJSON), string(m.Status), m.Group, string(configJSON),
		created, updated); err != nil {
		return fmt.Errorf("cache: save mount: %w", err)
	}
	return nil
}

// UpdateMountGroup changes only the group of a saved mount row.
func (db *DB) UpdateMountGroup(id, group string) error {
	_, err := db.SQL.Exec(`UPDATE mounts SET group_name = ?, updated_at = ?
		WHERE id = ?`, group, time.Now().UTC().Format(time.RFC3339), id)
	return err
}

// ListMounts returns all saved mounts in creation order.
func (db *DB) ListMounts() ([]PersistedMount, error) {
	rows, err := db.SQL.Query(`SELECT id, display_name, provider, account_id,
		root_ref, root_label, caps_json, status, group_name, config_json, created_at, updated_at
		FROM mounts ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("cache: list mounts: %w", err)
	}
	defer rows.Close()
	var out []PersistedMount
	for rows.Next() {
		var m domain.Mount
		var provider, capsJSON, status, configJSON string
		var created, updated string
		if err := rows.Scan(&m.ID, &m.DisplayName, &provider, &m.AccountID,
			&m.RootRef, &m.RootLabel, &capsJSON, &status, &m.Group, &configJSON,
			&created, &updated); err != nil {
			return nil, fmt.Errorf("cache: list mounts: %w", err)
		}
		m.Provider = domain.ProviderKind(provider)
		m.Status = domain.MountStatus(status)
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
func (db *DB) DeleteMount(id string) error {
	tx, err := db.SQL.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
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
	return tx.Commit()
}
