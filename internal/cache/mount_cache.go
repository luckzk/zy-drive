// SPDX-License-Identifier: MPL-2.0

package cache

import (
	"database/sql"
	"errors"
	"time"
)

// MountCacheEntry is one row of the mount_cache manifest: a cached
// copy of a remote file for the FUSE volume (~/ZyDrive, v0.19.0).
// It mirrors PreviewEntry on purpose (same key algorithm, same
// filename rule) but lives in its own table and directory: the
// preview cache is a read-only one-shot copy, this one is a writable
// working set with a dirty bit.
//
// Key is sha256(mountID + "\x00" + ref); Filename keeps the
// original name (sanitized) after the key so the OS picks the right
// app by extension. Size is the LOCAL copy's size (it changes on
// edit); RemoteSize + RemoteMtime describe the remote file the
// copy was made from — the uploader compares against those, never
// against Size (markDirty overwrites Size with the local size, so
// comparing remote.Size to Size would cry "remote changed" for
// every edited file). LocalMtime is the cached copy's own mtime.
// Dirty marks a local change not yet uploaded back — dirty rows are
// never evicted and never cleared by "清理缓存". LastAccess is the
// LRU clock. Pinned is reserved for explicit pinning (P1); P0 only
// writes 0.
type MountCacheEntry struct {
	Key         string
	MountID     string
	Ref         string
	Filename    string
	Size        int64
	RemoteSize  int64
	RemoteMtime time.Time
	LocalMtime  time.Time
	Dirty       bool
	LastAccess  time.Time
	Pinned      bool
}

// SaveMountCacheEntry upserts one manifest row.
func (db *DB) SaveMountCacheEntry(e *MountCacheEntry) error {
	_, err := db.SQL.Exec(`
		INSERT INTO mount_cache (key, mount_id, ref, filename, size, remote_size, remote_mtime, local_mtime, dirty, last_access, pinned, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET
			mount_id = excluded.mount_id,
			ref = excluded.ref,
			filename = excluded.filename,
			size = excluded.size,
			remote_size = excluded.remote_size,
			remote_mtime = excluded.remote_mtime,
			local_mtime = excluded.local_mtime,
			dirty = excluded.dirty,
			last_access = excluded.last_access,
			pinned = excluded.pinned,
			updated_at = excluded.updated_at`,
		e.Key, e.MountID, e.Ref, e.Filename, e.Size, e.RemoteSize,
		e.RemoteMtime.UTC().Format(time.RFC3339Nano),
		e.LocalMtime.UTC().Format(time.RFC3339Nano),
		boolToInt(e.Dirty),
		e.LastAccess.UTC().Format(time.RFC3339Nano),
		boolToInt(e.Pinned),
		time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// LoadMountCacheEntry returns the manifest row for a cache key, or
// (nil, nil) when the file was never cached.
func (db *DB) LoadMountCacheEntry(key string) (*MountCacheEntry, error) {
	var e MountCacheEntry
	var mtime, localMtime, lastAccess string
	var dirty, pinned int
	err := db.SQL.QueryRow(`
		SELECT key, mount_id, ref, filename, size, remote_size, remote_mtime, local_mtime, dirty, last_access, pinned
		FROM mount_cache WHERE key = ?`, key).
		Scan(&e.Key, &e.MountID, &e.Ref, &e.Filename, &e.Size, &e.RemoteSize, &mtime, &localMtime, &dirty, &lastAccess, &pinned)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e.Dirty = dirty != 0
	e.Pinned = pinned != 0
	if mtime != "" {
		if t, err := time.Parse(time.RFC3339Nano, mtime); err == nil {
			e.RemoteMtime = t
		}
	}
	if localMtime != "" {
		if t, err := time.Parse(time.RFC3339Nano, localMtime); err == nil {
			e.LocalMtime = t
		}
	}
	if t, err := time.Parse(time.RFC3339Nano, lastAccess); err == nil {
		e.LastAccess = t
	}
	return &e, nil
}

// ListMountCacheEntries returns every manifest row, oldest-access
// first (the LRU order the eviction pass walks).
func (db *DB) ListMountCacheEntries() ([]*MountCacheEntry, error) {
	rows, err := db.SQL.Query(`
		SELECT key, mount_id, ref, filename, size, remote_size, remote_mtime, local_mtime, dirty, last_access, pinned
		FROM mount_cache ORDER BY last_access ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*MountCacheEntry
	for rows.Next() {
		var e MountCacheEntry
		var mtime, localMtime, lastAccess string
		var dirty, pinned int
		if err := rows.Scan(&e.Key, &e.MountID, &e.Ref, &e.Filename, &e.Size, &e.RemoteSize, &mtime, &localMtime, &dirty, &lastAccess, &pinned); err != nil {
			return nil, err
		}
		e.Dirty = dirty != 0
		e.Pinned = pinned != 0
		if mtime != "" {
			if t, err := time.Parse(time.RFC3339Nano, mtime); err == nil {
				e.RemoteMtime = t
			}
		}
		if localMtime != "" {
			if t, err := time.Parse(time.RFC3339Nano, localMtime); err == nil {
				e.LocalMtime = t
			}
		}
		if t, err := time.Parse(time.RFC3339Nano, lastAccess); err == nil {
			e.LastAccess = t
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// DeleteMountCacheEntry removes one manifest row. The file itself
// is the caller's job (the mount cache owns the directory).
func (db *DB) DeleteMountCacheEntry(key string) error {
	_, err := db.SQL.Exec(`DELETE FROM mount_cache WHERE key = ?`, key)
	return err
}

// DeleteAllMountCacheEntries clears the whole manifest — used by
// "清理缓存" after the files are gone.
func (db *DB) DeleteAllMountCacheEntries() error {
	_, err := db.SQL.Exec(`DELETE FROM mount_cache`)
	return err
}
