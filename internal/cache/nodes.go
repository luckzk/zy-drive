package cache

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
)

// UpsertNodes replaces the cached listing of one directory (mount+path
// prefix). Callers pass the full fresh listing; stale rows under the
// same parent are removed in the same transaction.
func (db *DB) UpsertNodes(mountID, parentPath string, nodes []domain.Node) error {
	tx, err := db.SQL.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	like := parentPath + "/%"
	if _, err := tx.Exec(`DELETE FROM nodes WHERE mount_id = ? AND path LIKE ? AND path NOT LIKE ?`,
		mountID, like, like+"/%"); err != nil {
		return fmt.Errorf("cache: clear nodes: %w", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO nodes
		(mount_id, path, ref, is_dir, size, mod_time, hash, hash_kind, mime)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(mount_id, path) DO UPDATE SET
		ref=excluded.ref, is_dir=excluded.is_dir, size=excluded.size,
		mod_time=excluded.mod_time, hash=excluded.hash,
		hash_kind=excluded.hash_kind, mime=excluded.mime`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, n := range nodes {
		if _, err := stmt.Exec(mountID, n.Path, n.Ref, boolToInt(n.IsDir), n.Size,
			n.ModTime.UTC().Format(time.RFC3339), n.Hash, n.HashKind, n.Mime); err != nil {
			return fmt.Errorf("cache: upsert node %s: %w", n.Path, err)
		}
	}
	return tx.Commit()
}

// ListCached returns the cached children of a directory for instant
// rendering; the app layer refreshes in the background afterwards.
func (db *DB) ListCached(mountID, parentPath string) ([]domain.Node, error) {
	rows, err := db.SQL.Query(`SELECT path, ref, is_dir, size, mod_time, hash, hash_kind, mime
		FROM nodes WHERE mount_id = ? AND path LIKE ? AND path NOT LIKE ?
		ORDER BY is_dir DESC, path ASC`,
		mountID, parentPath+"/%", parentPath+"/%/%")
	if err != nil {
		return nil, fmt.Errorf("cache: list cached: %w", err)
	}
	defer rows.Close()
	var out []domain.Node
	for rows.Next() {
		var n domain.Node
		var isDir int
		var modTime string
		if err := rows.Scan(&n.Path, &n.Ref, &isDir, &n.Size, &modTime, &n.Hash, &n.HashKind, &n.Mime); err != nil {
			return nil, err
		}
		n.IsDir = isDir != 0
		if modTime != "" {
			n.ModTime, _ = time.Parse(time.RFC3339, modTime)
		}
		n.Name = baseName(n.Path)
		out = append(out, n)
	}
	return out, rows.Err()
}

// PutIDMap stores one path->ref mapping for ID-based drivers.
func (db *DB) PutIDMap(mountID, path, ref string) error {
	_, err := db.SQL.Exec(`INSERT INTO idmap (mount_id, path, ref) VALUES (?, ?, ?)
		ON CONFLICT(mount_id, path) DO UPDATE SET ref=excluded.ref`,
		mountID, path, ref)
	return err
}

// RefForPath resolves a display path to a driver ref (ID-based clouds).
func (db *DB) RefForPath(mountID, path string) (string, error) {
	var ref string
	err := db.SQL.QueryRow(`SELECT ref FROM idmap WHERE mount_id = ? AND path = ?`,
		mountID, path).Scan(&ref)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("cache: no ref for path %s", path)
	}
	return ref, err
}

// SaveCursor stores a delta cursor for a mount.
func (db *DB) SaveCursor(mountID, cursor string) error {
	_, err := db.SQL.Exec(`INSERT INTO cursors (mount_id, cursor, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(mount_id) DO UPDATE SET cursor=excluded.cursor, updated_at=excluded.updated_at`,
		mountID, cursor, time.Now().UTC().Format(time.RFC3339))
	return err
}

// LoadCursor returns the stored delta cursor, or "" when none exists.
func (db *DB) LoadCursor(mountID string) (string, error) {
	var cursor string
	err := db.SQL.QueryRow(`SELECT cursor FROM cursors WHERE mount_id = ?`, mountID).Scan(&cursor)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return cursor, err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}
