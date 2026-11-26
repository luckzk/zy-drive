// SPDX-License-Identifier: MPL-2.0

package cache

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/luckzk/zy-drive/internal/storage/upload"
)

// uploadSessionRow is the on-disk shape of an upload_sessions row.
// parts_json holds the Session's Parts array (number, size, ETag,
// done) so a restart can pick up exactly where the upload stopped.
type uploadSessionRow struct {
	SessionID string              `json:"sessionId"`
	RemoteID  string              `json:"remoteId"`
	RemoteKey string              `json:"remoteKey"`
	Parts     []upload.PartState  `json:"parts"`
}

// SaveSession upserts one resumable-upload session. The caller
// (Drime driver) saves after every part completes, so a crash
// between parts loses at most the part in flight.
func (db *DB) SaveSession(mountID string, s *upload.Session) error {
	row, err := json.Marshal(uploadSessionRow{
		SessionID: s.ID,
		RemoteID:  s.RemoteID,
		RemoteKey: s.RemoteKey,
		Parts:     s.Parts,
	})
	if err != nil {
		return err
	}
	_, err = db.SQL.Exec(`
		INSERT INTO upload_sessions (mount_id, ref, size, session_id, remote_id, remote_key, parts_json, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (mount_id, ref, size) DO UPDATE SET
			session_id = excluded.session_id,
			remote_id = excluded.remote_id,
			remote_key = excluded.remote_key,
			parts_json = excluded.parts_json,
			updated_at = excluded.updated_at`,
		mountID, s.Ref, s.TotalSize, s.ID, s.RemoteID, s.RemoteKey, string(row), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// LoadSession returns the stored session for (mountID, ref, size),
// or (nil, nil) when there is none — the normal "nothing to
// resume" case, not an error.
func (db *DB) LoadSession(mountID, ref string, size int64) (*upload.Session, error) {
	var raw string
	var sessionID, remoteID, remoteKey string
	err := db.SQL.QueryRow(`
		SELECT session_id, remote_id, remote_key, parts_json
		FROM upload_sessions WHERE mount_id = ? AND ref = ? AND size = ?`,
		mountID, ref, size).Scan(&sessionID, &remoteID, &remoteKey, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var row uploadSessionRow
	if err := json.Unmarshal([]byte(raw), &row); err != nil {
		return nil, err
	}
	partSize := int64(0)
	if len(row.Parts) > 0 {
		partSize = row.Parts[0].Size
	}
	s, err := upload.NewSession(sessionID, ref, size, partSize)
	if err != nil {
		return nil, err
	}
	s.RemoteID = remoteID
	s.RemoteKey = remoteKey
	s.Parts = row.Parts
	return s, nil
}

// DeleteSession removes a session row — called when its upload
// completes, or when the session is proven dead server-side and a
// fresh one takes over.
func (db *DB) DeleteSession(mountID, ref string, size int64) error {
	_, err := db.SQL.Exec(`
		DELETE FROM upload_sessions WHERE mount_id = ? AND ref = ? AND size = ?`,
		mountID, ref, size)
	return err
}
