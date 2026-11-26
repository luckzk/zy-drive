// SPDX-License-Identifier: MPL-2.0
package cache

import (
	"database/sql"
	"fmt"
	"time"
)

// migrations are applied in order, once each. Add new ones at the end;
// never edit an applied migration.
var migrations = []string{
	// v1: core tables (V0.1 skeleton).
	`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS accounts (
		id TEXT PRIMARY KEY,
		provider TEXT NOT NULL,
		display_name TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS mounts (
		id TEXT PRIMARY KEY,
		display_name TEXT NOT NULL,
		provider TEXT NOT NULL,
		account_id TEXT NOT NULL,
		root_ref TEXT NOT NULL DEFAULT '',
		caps_json TEXT NOT NULL DEFAULT '{}',
		status TEXT NOT NULL DEFAULT 'ok',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS secrets (
		id TEXT PRIMARY KEY,
		ciphertext BLOB NOT NULL,
		key_version INTEGER NOT NULL DEFAULT 1,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS nodes (
		mount_id TEXT NOT NULL,
		path TEXT NOT NULL,
		ref TEXT NOT NULL,
		is_dir INTEGER NOT NULL DEFAULT 0,
		size INTEGER NOT NULL DEFAULT -1,
		mod_time TEXT NOT NULL DEFAULT '',
		hash TEXT NOT NULL DEFAULT '',
		hash_kind TEXT NOT NULL DEFAULT '',
		mime TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (mount_id, path)
	)`,
	`CREATE TABLE IF NOT EXISTS idmap (
		mount_id TEXT NOT NULL,
		path TEXT NOT NULL,
		ref TEXT NOT NULL,
		PRIMARY KEY (mount_id, path)
	)`,
	`CREATE TABLE IF NOT EXISTS cursors (
		mount_id TEXT PRIMARY KEY,
		cursor TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS transfers (
		id TEXT PRIMARY KEY,
		kind TEXT NOT NULL,
		job_id TEXT NOT NULL DEFAULT '',
		run_id TEXT NOT NULL DEFAULT '',
		src_mount TEXT NOT NULL DEFAULT '',
		src_ref TEXT NOT NULL DEFAULT '',
		dst_mount TEXT NOT NULL DEFAULT '',
		dst_ref TEXT NOT NULL DEFAULT '',
		size INTEGER NOT NULL DEFAULT 0,
		done INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'queued',
		speed_bps INTEGER NOT NULL DEFAULT 0,
		retries INTEGER NOT NULL DEFAULT 0,
		error TEXT NOT NULL DEFAULT '',
		session_id TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS sync_jobs (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		src_mount TEXT NOT NULL,
		src_ref TEXT NOT NULL DEFAULT '',
		dst_mount TEXT NOT NULL,
		dst_ref TEXT NOT NULL DEFAULT '',
		direction TEXT NOT NULL,
		compare TEXT NOT NULL DEFAULT 'size+time',
		exclude_json TEXT NOT NULL DEFAULT '[]',
		schedule TEXT NOT NULL DEFAULT '',
		run_on TEXT NOT NULL DEFAULT 'local',
		enabled INTEGER NOT NULL DEFAULT 1,
		last_run_at TEXT,
		last_run_ok INTEGER NOT NULL DEFAULT 0,
		next_run_at TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS sync_baselines (
		job_id TEXT NOT NULL,
		side TEXT NOT NULL,
		taken_at TEXT NOT NULL,
		snapshot_json TEXT NOT NULL,
		PRIMARY KEY (job_id, side)
	)`,
	`CREATE TABLE IF NOT EXISTS runs (
		id TEXT PRIMARY KEY,
		job_id TEXT NOT NULL,
		status TEXT NOT NULL,
		plan_json TEXT NOT NULL DEFAULT '{}',
		started_at TEXT NOT NULL,
		ended_at TEXT,
		error TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE IF NOT EXISTS run_items (
		run_id TEXT NOT NULL,
		path TEXT NOT NULL,
		action TEXT NOT NULL,
		size INTEGER NOT NULL DEFAULT 0,
		note TEXT NOT NULL DEFAULT '',
		done INTEGER NOT NULL DEFAULT 0,
		error TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (run_id, path)
	)`,
	`CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE IF NOT EXISTS agent_meta (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL DEFAULT ''
	)`,
	// v2 (2026-11-24, V0.1 fill): mount groups, the multi-Agent list
	// and the per-job Agent choice. Appended, never edited in place.
	`ALTER TABLE mounts ADD COLUMN group_name TEXT NOT NULL DEFAULT ''`,
	`CREATE TABLE IF NOT EXISTS agents (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		address TEXT NOT NULL,
		token_ref TEXT NOT NULL DEFAULT '',
		online INTEGER NOT NULL DEFAULT 0,
		version TEXT NOT NULL DEFAULT '',
		protocol_version TEXT NOT NULL DEFAULT '',
		jobs_running INTEGER NOT NULL DEFAULT 0,
		last_heartbeat TEXT,
		is_default INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`ALTER TABLE sync_jobs ADD COLUMN agent_id TEXT NOT NULL DEFAULT ''`,
	// v18 (2026-11-25, Mount onboarding): non-secret driver config per
	// mount (endpoint/region/bucket/client_id/token_url/local root).
	// Secrets stay in the secrets table, encrypted; this column never
	// holds a token, key or client secret.
	`ALTER TABLE mounts ADD COLUMN config_json TEXT NOT NULL DEFAULT '{}'`,
	// v19 (2026-11-25, S3 root fix): display-only root label
	// ("bucket" or "bucket/prefix" for S3, absolute path for local).
	// RootRef itself is fixed to "" for S3 by the LoadMounts
	// self-heal; the label keeps the bucket visible in Settings.
	`ALTER TABLE mounts ADD COLUMN root_label TEXT NOT NULL DEFAULT ''`,
	// v20 (2026-11-25, one-page form): whether the mount passed a
	// connection probe at least once. Default 1: every mount saved
	// before this column existed was probe-verified at save time
	// (the old wizard refused to save an unprobed mount), so
	// treating old rows as verified is honest, not a guess.
	`ALTER TABLE mounts ADD COLUMN verified INTEGER NOT NULL DEFAULT 1`,
	// v21 (2026-11-25, Drime resume): resumable-upload sessions —
	// one row per (mount, ref, size). parts_json is the Session
	// JSON (parts with their ETags), remote_id the driver session
	// handle (Drime uploadId), remote_key the server object key
	// (Drime S3 key). A session is deleted when its upload
	// completes; an interrupted upload keeps its row so the next
	// attempt resumes from the next missing part.
	`CREATE TABLE IF NOT EXISTS upload_sessions (
		mount_id TEXT NOT NULL,
		ref TEXT NOT NULL,
		size INTEGER NOT NULL,
		session_id TEXT NOT NULL DEFAULT '',
		remote_id TEXT NOT NULL DEFAULT '',
		remote_key TEXT NOT NULL DEFAULT '',
		parts_json TEXT NOT NULL DEFAULT '[]',
		updated_at TEXT NOT NULL,
		PRIMARY KEY (mount_id, ref, size)
	)`,
	// v22 (2026-11-25, preview cache): the manifest of the
	// "system app open" preview cache (docs/roadmap-next.md,
	// "预览缓存管理规则"). One row per cached copy: key is
	// sha256(mount_id + ref), filename the file's name inside the
	// mount's cache dir (it keeps the original extension so the OS
	// picks the right app), size + remote_mtime the remote file
	// the copy was made from (reuse only when both still match),
	// last_access the LRU clock. The manifest is reconciled with
	// the disk at startup: a row without a file is dropped, a file
	// without a row (orphan, or a half-written .part) is deleted.
	`CREATE TABLE IF NOT EXISTS preview_cache (
		key TEXT PRIMARY KEY,
		mount_id TEXT NOT NULL,
		ref TEXT NOT NULL,
		filename TEXT NOT NULL,
		size INTEGER NOT NULL DEFAULT -1,
		remote_mtime TEXT NOT NULL DEFAULT '',
		last_access TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	// v23 (2026-11-26, cross-cloud move): whether a copy transfer
	// deletes its source after a successful upload (see
	// domain.Transfer.DeleteSource). Default 0: every transfer
	// queued before this column existed was a plain copy.
	`ALTER TABLE transfers ADD COLUMN delete_source INTEGER NOT NULL DEFAULT 0`,
	// v24 (2026-11-26, sidebar drag-sort): per-group position of a
	// mount, 0-based and dense 0..n-1 inside its group. The
	// backfill (v25) numbers existing mounts by creation order, so
	// the visible order does not change on upgrade.
	`ALTER TABLE mounts ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0`,
	// v25 (2026-11-26, sidebar drag-sort backfill): number each
	// mount by how many mounts in the same group were created
	// before it (created_at, then id as tie-break) — the same
	// order the sidebar showed before sort_order existed.
	`UPDATE mounts SET sort_order = (
		SELECT COUNT(*) FROM mounts m2
		WHERE m2.group_name = mounts.group_name
		  AND (m2.created_at < mounts.created_at
		    OR (m2.created_at = mounts.created_at AND m2.id < mounts.id))
	)`,
}

// Migrate applies pending migrations in a transaction per migration.
func (db *DB) Migrate() error {
	if _, err := db.SQL.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("cache: migrate bootstrap: %w", err)
	}
	for i, m := range migrations {
		version := i + 1
		var exists int
		err := db.SQL.QueryRow(
			`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&exists)
		if err != nil {
			return fmt.Errorf("cache: migrate check v%d: %w", version, err)
		}
		if exists > 0 {
			continue
		}
		tx, err := db.SQL.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(m); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("cache: migrate v%d: %w", version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			version, time.Now().UTC().Format(time.RFC3339)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("cache: migrate record v%d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("cache: migrate commit v%d: %w", version, err)
		}
	}
	return nil
}

// SchemaVersion returns the highest applied migration version.
func (db *DB) SchemaVersion() (int, error) {
	var v sql.NullInt64
	err := db.SQL.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v)
	if err != nil {
		return 0, err
	}
	return int(v.Int64), nil
}
