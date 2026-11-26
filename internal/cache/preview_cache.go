// SPDX-License-Identifier: MPL-2.0

package cache

import (
	"database/sql"
	"errors"
	"time"
)

// PreviewEntry is one row of the preview_cache manifest: a cached
// copy of a remote file, downloaded for "open with system app"
// previews (docs/roadmap-next.md, "预览缓存管理规则").
// Key is sha256(mountID + "\x00" + ref); Filename is the file's
// name inside the mount's cache dir (it keeps the original
// extension so the OS picks the right app). Size and RemoteMtime
// describe the remote file the copy was made from: reuse only
// when both still match. LastAccess is the LRU clock, refreshed
// on every reuse.
type PreviewEntry struct {
	Key         string
	MountID     string
	Ref         string
	Filename    string
	Size        int64
	RemoteMtime time.Time
	LastAccess  time.Time
}

// SavePreviewEntry upserts one manifest row.
func (db *DB) SavePreviewEntry(e *PreviewEntry) error {
	_, err := db.SQL.Exec(`
		INSERT INTO preview_cache (key, mount_id, ref, filename, size, remote_mtime, last_access, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET
			mount_id = excluded.mount_id,
			ref = excluded.ref,
			filename = excluded.filename,
			size = excluded.size,
			remote_mtime = excluded.remote_mtime,
			last_access = excluded.last_access,
			updated_at = excluded.updated_at`,
		e.Key, e.MountID, e.Ref, e.Filename, e.Size,
		e.RemoteMtime.UTC().Format(time.RFC3339Nano),
		e.LastAccess.UTC().Format(time.RFC3339Nano),
		time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// LoadPreviewEntry returns the manifest row for a cache key, or
// (nil, nil) when the file was never cached.
func (db *DB) LoadPreviewEntry(key string) (*PreviewEntry, error) {
	var e PreviewEntry
	var mtime, lastAccess string
	err := db.SQL.QueryRow(`
		SELECT key, mount_id, ref, filename, size, remote_mtime, last_access
		FROM preview_cache WHERE key = ?`, key).
		Scan(&e.Key, &e.MountID, &e.Ref, &e.Filename, &e.Size, &mtime, &lastAccess)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if mtime != "" {
		if t, err := time.Parse(time.RFC3339Nano, mtime); err == nil {
			e.RemoteMtime = t
		}
	}
	if t, err := time.Parse(time.RFC3339Nano, lastAccess); err == nil {
		e.LastAccess = t
	}
	return &e, nil
}

// ListPreviewEntries returns every manifest row, oldest-access
// first (the LRU order the eviction pass walks).
func (db *DB) ListPreviewEntries() ([]*PreviewEntry, error) {
	rows, err := db.SQL.Query(`
		SELECT key, mount_id, ref, filename, size, remote_mtime, last_access
		FROM preview_cache ORDER BY last_access ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PreviewEntry
	for rows.Next() {
		var e PreviewEntry
		var mtime, lastAccess string
		if err := rows.Scan(&e.Key, &e.MountID, &e.Ref, &e.Filename, &e.Size, &mtime, &lastAccess); err != nil {
			return nil, err
		}
		if mtime != "" {
			if t, err := time.Parse(time.RFC3339Nano, mtime); err == nil {
				e.RemoteMtime = t
			}
		}
		if t, err := time.Parse(time.RFC3339Nano, lastAccess); err == nil {
			e.LastAccess = t
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// DeletePreviewEntry removes one manifest row. The file itself is
// the caller's job (PreviewCache owns the directory).
func (db *DB) DeletePreviewEntry(key string) error {
	_, err := db.SQL.Exec(`DELETE FROM preview_cache WHERE key = ?`, key)
	return err
}

// DeleteAllPreviewEntries clears the whole manifest — used by
// "清理缓存" after the files are gone.
func (db *DB) DeleteAllPreviewEntries() error {
	_, err := db.SQL.Exec(`DELETE FROM preview_cache`)
	return err
}
