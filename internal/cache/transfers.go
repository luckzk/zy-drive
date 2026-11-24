package cache

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
)

// SaveTransfer persists a transfer. It implements transfer.Store.
func (db *DB) SaveTransfer(t *domain.Transfer) error {
	_, err := db.SQL.Exec(`INSERT INTO transfers
		(id, kind, job_id, run_id, src_mount, src_ref, dst_mount, dst_ref,
		 size, done, status, speed_bps, retries, error, session_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		kind=excluded.kind, job_id=excluded.job_id, run_id=excluded.run_id,
		src_mount=excluded.src_mount, src_ref=excluded.src_ref,
		dst_mount=excluded.dst_mount, dst_ref=excluded.dst_ref,
		size=excluded.size, done=excluded.done, status=excluded.status,
		speed_bps=excluded.speed_bps, retries=excluded.retries,
		error=excluded.error, session_id=excluded.session_id,
		updated_at=excluded.updated_at`,
		t.ID, string(t.Kind), t.JobID, t.RunID, t.SrcMount, t.SrcRef, t.DstMount, t.DstRef,
		t.Size, t.Done, string(t.Status), t.SpeedBps, t.Retries, t.Error, t.SessionID,
		t.CreatedAt.UTC().Format(time.RFC3339), t.UpdatedAt.UTC().Format(time.RFC3339))
	return err
}

// LoadTransfers returns transfers in the given status.
func (db *DB) LoadTransfers(status domain.TransferStatus) ([]*domain.Transfer, error) {
	rows, err := db.SQL.Query(`SELECT id, kind, job_id, run_id, src_mount, src_ref,
		dst_mount, dst_ref, size, done, status, speed_bps, retries, error, session_id,
		created_at, updated_at FROM transfers WHERE status = ? ORDER BY created_at ASC`,
		string(status))
	if err != nil {
		return nil, fmt.Errorf("cache: load transfers: %w", err)
	}
	defer rows.Close()
	return scanTransfers(rows)
}

// LoadInterrupted returns transfers that were running when the process
// died, so the queue can mark them interrupted on startup.
func (db *DB) LoadInterrupted() ([]*domain.Transfer, error) {
	rows, err := db.SQL.Query(`SELECT id, kind, job_id, run_id, src_mount, src_ref,
		dst_mount, dst_ref, size, done, status, speed_bps, retries, error, session_id,
		created_at, updated_at FROM transfers WHERE status IN ('running','queued') ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("cache: load interrupted: %w", err)
	}
	defer rows.Close()
	return scanTransfers(rows)
}

func scanTransfers(rows *sql.Rows) ([]*domain.Transfer, error) {
	var out []*domain.Transfer
	for rows.Next() {
		var t domain.Transfer
		var kind, status, createdAt, updatedAt string
		if err := rows.Scan(&t.ID, &kind, &t.JobID, &t.RunID, &t.SrcMount, &t.SrcRef,
			&t.DstMount, &t.DstRef, &t.Size, &t.Done, &status, &t.SpeedBps, &t.Retries,
			&t.Error, &t.SessionID, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		t.Kind = domain.TransferKind(kind)
		t.Status = domain.TransferStatus(status)
		t.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		t.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		out = append(out, &t)
	}
	return out, rows.Err()
}
