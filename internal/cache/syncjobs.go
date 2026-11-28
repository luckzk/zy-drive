// SPDX-License-Identifier: MPL-2.0
package cache

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
)

// SaveSyncJob upserts one sync job. The sync_jobs table already exists
// (migrations); V0.1 shipped the table but no code ever wrote to it, so
// saved jobs could not survive a restart. This closes that gap: the
// sidebar "已保存的任务" list reads from here.
func (db *DB) SaveSyncJob(j *domain.SyncJob) error {
	excludeJSON, err := json.Marshal(j.Exclude)
	if err != nil {
		return fmt.Errorf("cache: save sync job: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var lastRun, nextRun *string
	if j.LastRunAt != nil {
		s := j.LastRunAt.UTC().Format(time.RFC3339)
		lastRun = &s
	}
	if j.NextRunAt != nil {
		s := j.NextRunAt.UTC().Format(time.RFC3339)
		nextRun = &s
	}
	var deleteEnabled *int
	if j.DeleteEnabled != nil {
		v := 0
		if *j.DeleteEnabled {
			v = 1
		}
		deleteEnabled = &v
	}
	if _, err := db.SQL.Exec(`INSERT INTO sync_jobs
		(id, name, src_mount, src_ref, dst_mount, dst_ref, direction,
		 compare, exclude_json, schedule, run_on, agent_id, enabled,
		 last_run_at, last_run_ok, next_run_at, created_at, updated_at,
		 delete_enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, src_mount=excluded.src_mount,
			src_ref=excluded.src_ref, dst_mount=excluded.dst_mount,
			dst_ref=excluded.dst_ref, direction=excluded.direction,
			compare=excluded.compare, exclude_json=excluded.exclude_json,
			schedule=excluded.schedule, run_on=excluded.run_on,
			agent_id=excluded.agent_id, enabled=excluded.enabled,
			last_run_at=excluded.last_run_at, last_run_ok=excluded.last_run_ok,
			next_run_at=excluded.next_run_at, updated_at=excluded.updated_at,
			delete_enabled=excluded.delete_enabled`,
		j.ID, j.Name, j.SrcMount, j.SrcRef, j.DstMount, j.DstRef,
		string(j.Direction), string(j.Compare), string(excludeJSON),
		j.Schedule, string(j.RunOn), j.AgentID, boolToInt(j.Enabled),
		lastRun, boolToInt(j.LastRunOK), nextRun, now, now, deleteEnabled); err != nil {
		return fmt.Errorf("cache: save sync job: %w", err)
	}
	return nil
}

// ListSyncJobs returns saved jobs in creation order.
func (db *DB) ListSyncJobs() ([]*domain.SyncJob, error) {
	rows, err := db.SQL.Query(`SELECT id, name, src_mount, src_ref,
		dst_mount, dst_ref, direction, compare, exclude_json, schedule,
		run_on, agent_id, enabled, last_run_at, last_run_ok, next_run_at,
		created_at, updated_at, delete_enabled FROM sync_jobs ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("cache: list sync jobs: %w", err)
	}
	defer rows.Close()
	var out []*domain.SyncJob
	for rows.Next() {
		var j domain.SyncJob
		var direction, compare, runOn, excludeJSON string
		var enabled, lastRunOK int
		var deleteEnabled *int
		var lastRun, nextRun, created, updated *string
		if err := rows.Scan(&j.ID, &j.Name, &j.SrcMount, &j.SrcRef,
			&j.DstMount, &j.DstRef, &direction, &compare, &excludeJSON,
			&j.Schedule, &runOn, &j.AgentID, &enabled, &lastRun,
			&lastRunOK, &nextRun, &created, &updated, &deleteEnabled); err != nil {
			return nil, err
		}
		if deleteEnabled != nil {
			j.DeleteEnabled = domain.BoolPtr(*deleteEnabled != 0)
		}
		j.Direction = domain.SyncDirection(direction)
		j.Compare = domain.CompareMode(compare)
		j.RunOn = domain.RunOn(runOn)
		j.Enabled = enabled != 0
		j.LastRunOK = lastRunOK != 0
		if err := json.Unmarshal([]byte(excludeJSON), &j.Exclude); err != nil {
			return nil, fmt.Errorf("cache: list sync jobs: bad exclude_json: %w", err)
		}
		if lastRun != nil {
			t, _ := time.Parse(time.RFC3339, *lastRun)
			j.LastRunAt = &t
		}
		if nextRun != nil {
			t, _ := time.Parse(time.RFC3339, *nextRun)
			j.NextRunAt = &t
		}
		if created != nil {
			j.CreatedAt, _ = time.Parse(time.RFC3339, *created)
		}
		if updated != nil {
			j.UpdatedAt, _ = time.Parse(time.RFC3339, *updated)
		}
		out = append(out, &j)
	}
	return out, rows.Err()
}

// DeleteSyncJob removes one saved job. Runs and baselines keep their
// job_id so history is not rewritten.
func (db *DB) DeleteSyncJob(id string) error {
	_, err := db.SQL.Exec(`DELETE FROM sync_jobs WHERE id = ?`, id)
	return err
}
