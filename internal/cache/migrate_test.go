// SPDX-License-Identifier: MPL-2.0
package cache

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestMigrateBackfillsSortOrder builds a v23-style database by hand
// (mounts table without sort_order, schema_migrations 1..23),
// opens it through cache.Open, and checks that v24+v25 number the
// mounts by creation order inside each group — so an upgrade never
// reorders the sidebar.
func TestMigrateBackfillsSortOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v23.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	// Minimal v23 mounts table: every column the backfill and
	// ListMounts touch, minus sort_order.
	if _, err := raw.Exec(`CREATE TABLE mounts (
		id TEXT PRIMARY KEY,
		display_name TEXT NOT NULL,
		provider TEXT NOT NULL,
		account_id TEXT NOT NULL,
		root_ref TEXT NOT NULL DEFAULT '',
		root_label TEXT NOT NULL DEFAULT '',
		caps_json TEXT NOT NULL DEFAULT '{}',
		status TEXT NOT NULL DEFAULT 'ok',
		group_name TEXT NOT NULL DEFAULT '',
		config_json TEXT NOT NULL DEFAULT '{}',
		verified INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create mounts: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE schema_migrations (
		version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	for v := 1; v <= 23; v++ {
		if _, err := raw.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, '2026-11-26T00:00:00Z')`, v); err != nil {
			t.Fatalf("seed migration %d: %v", v, err)
		}
	}
	// Deliberately out of creation order in the INSERTs: the
	// backfill must follow created_at, not row order.
	rows := []struct{ id, name, group, created string }{
		{"m2", "工作-2", "工作", "2026-11-26T10:01:00Z"},
		{"p2", "个人-2", "个人", "2026-11-26T10:01:00Z"},
		{"m1", "工作-1", "工作", "2026-11-26T10:00:00Z"},
		{"m3", "工作-3", "工作", "2026-11-26T10:02:00Z"},
		{"p1", "个人-1", "个人", "2026-11-26T10:00:00Z"},
	}
	for _, r := range rows {
		if _, err := raw.Exec(`INSERT INTO mounts
			(id, display_name, provider, account_id, group_name, created_at, updated_at)
			VALUES (?, ?, 'local', ?, ?, ?, ?)`,
			r.id, r.name, "mount:"+r.id, r.group, r.created, r.created); err != nil {
			t.Fatalf("insert %s: %v", r.id, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	defer db.Close()
	if v, err := db.SchemaVersion(); err != nil || v != 25 {
		t.Fatalf("schema version = %d, err = %v, want 25", v, err)
	}
	got, err := db.ListMounts()
	if err != nil {
		t.Fatalf("ListMounts: %v", err)
	}
	want := map[string]int{"工作-1": 0, "工作-2": 1, "工作-3": 2, "个人-1": 0, "个人-2": 1}
	if len(got) != len(want) {
		t.Fatalf("got %d mounts, want %d", len(got), len(want))
	}
	for _, pm := range got {
		if want[pm.Mount.DisplayName] != pm.Mount.SortOrder {
			t.Fatalf("mount %s sort_order = %d, want %d", pm.Mount.DisplayName, pm.Mount.SortOrder, want[pm.Mount.DisplayName])
		}
	}
}
