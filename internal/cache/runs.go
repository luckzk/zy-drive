// SPDX-License-Identifier: MPL-2.0
package cache

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
)

// runs.go persists sync runs: one row per execution (status, plan
// summary, times, error) plus one row per plan item (done/error).
// The Agent writes here on every scheduled/immediate run and the
// desktop reads it back — a run record is the proof a scheduled job
// actually ran, and it survives restarts on both sides.

// SaveRun upserts one run row. The runner calls it when a run
// starts (status running) and again when it ends (completed/failed),
// so a crashed run stays visible as "running" with its start time.
func (db *DB) SaveRun(r *domain.Run) error {
	planJSON := "{}"
	if r.Plan != nil {
		if b, err := json.Marshal(r.Plan); err == nil {
			planJSON = string(b)
		}
	}
	var ended any
	if r.EndedAt != nil {
		ended = r.EndedAt.UTC().Format(time.RFC3339)
	}
	if _, err := db.SQL.Exec(`INSERT INTO runs (id, job_id, status, plan_json, started_at, ended_at, error, deferred_deletes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET job_id=excluded.job_id, status=excluded.status,
			plan_json=excluded.plan_json, started_at=excluded.started_at,
			ended_at=excluded.ended_at, error=excluded.error,
			deferred_deletes=excluded.deferred_deletes`,
		r.ID, r.JobID, string(r.Status), planJSON,
		r.StartedAt.UTC().Format(time.RFC3339), ended, r.Error, r.DeferredDeletes); err != nil {
		return fmt.Errorf("cache: save run: %w", err)
	}
	return nil
}

// ListRuns returns a job's runs, newest first (limit <= 0 means all).
func (db *DB) ListRuns(jobID string, limit int) ([]*domain.Run, error) {
	q := `SELECT id, job_id, status, plan_json, started_at, ended_at, error, deferred_deletes
		FROM runs WHERE job_id = ? ORDER BY started_at DESC`
	args := []any{jobID}
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := db.SQL.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("cache: list runs: %w", err)
	}
	defer rows.Close()
	out := []*domain.Run{}
	for rows.Next() {
		var r domain.Run
		var status, planJSON, started string
		var ended sql.NullString
		if err := rows.Scan(&r.ID, &r.JobID, &status, &planJSON, &started, &ended, &r.Error, &r.DeferredDeletes); err != nil {
			return nil, fmt.Errorf("cache: list runs: %w", err)
		}
		r.Status = domain.RunStatus(status)
		if planJSON != "" && planJSON != "{}" {
			var p domain.PlanSummary
			if err := json.Unmarshal([]byte(planJSON), &p); err == nil {
				r.Plan = &p
			}
		}
		if t, err := time.Parse(time.RFC3339, started); err == nil {
			r.StartedAt = t
		}
		if ended.Valid {
			if t, err := time.Parse(time.RFC3339, ended.String); err == nil {
				r.EndedAt = &t
			}
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// SaveRunItems replaces a run's item rows in one transaction.
func (db *DB) SaveRunItems(runID string, items []domain.RunItem) error {
	tx, err := db.SQL.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM run_items WHERE run_id = ?`, runID); err != nil {
		return fmt.Errorf("cache: save run items: %w", err)
	}
	for _, it := range items {
		done := 0
		if it.Done {
			done = 1
		}
		if _, err := tx.Exec(`INSERT INTO run_items (run_id, path, action, size, note, done, error)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			runID, it.Path, string(it.Action), it.Size, it.Note, done, it.Error); err != nil {
			return fmt.Errorf("cache: save run items: %w", err)
		}
	}
	return tx.Commit()
}

// ListRunItems returns one run's items in path order.
func (db *DB) ListRunItems(runID string) ([]domain.RunItem, error) {
	rows, err := db.SQL.Query(`SELECT run_id, path, action, size, note, done, error
		FROM run_items WHERE run_id = ? ORDER BY path`, runID)
	if err != nil {
		return nil, fmt.Errorf("cache: list run items: %w", err)
	}
	defer rows.Close()
	out := []domain.RunItem{}
	for rows.Next() {
		var it domain.RunItem
		var action string
		var done int
		if err := rows.Scan(&it.RunID, &it.Path, &action, &it.Size, &it.Note, &done, &it.Error); err != nil {
			return nil, fmt.Errorf("cache: list run items: %w", err)
		}
		it.Action = domain.RunItemAction(action)
		it.Done = done != 0
		out = append(out, it)
	}
	return out, rows.Err()
}

// DeleteRunsForJob removes a job's runs and their items (job delete).
func (db *DB) DeleteRunsForJob(jobID string) error {
	tx, err := db.SQL.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM run_items WHERE run_id IN (SELECT id FROM runs WHERE job_id = ?)`, jobID); err != nil {
		return fmt.Errorf("cache: delete runs: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM runs WHERE job_id = ?`, jobID); err != nil {
		return fmt.Errorf("cache: delete runs: %w", err)
	}
	return tx.Commit()
}
