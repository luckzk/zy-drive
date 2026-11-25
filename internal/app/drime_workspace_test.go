// SPDX-License-Identifier: MPL-2.0
package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/luckzk/zy-drive/internal/domain"
)

// TestDrimeWorkspaceSurvivesRestart: a Drime mount saved with a
// team workspace id must keep asking for THAT workspace's quota
// after a restart — not silently fall back to the personal
// workspace (0) or the account total. The chain under test:
// wizard WorkspaceID -> nonSecretConfig workspace_id ->
// config_json -> LoadMounts rebuild -> Quota workspaceId param.
//
// The mock answers space-usage per workspace (8762 and 0 return
// different numbers) and records the workspaceId it was asked
// for, so a driver that asked for the wrong workspace fails here
// instead of looking right in the UI.
func TestDrimeWorkspaceSurvivesRestart(t *testing.T) {
	var mu sync.Mutex
	var seen []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/user/space-usage" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		ws, _ := strconv.Atoi(r.URL.Query().Get("workspaceId"))
		mu.Lock()
		seen = append(seen, ws)
		mu.Unlock()
		used, avail := int64(100_000_000_000), int64(412_000_000_000) // personal (0)
		if ws == 8762 {
			used, avail = 312345678901, 237165364723
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"used": used, "available": avail})
	}))
	defer srv.Close()

	dbPath := filepath.Join(t.TempDir(), "app.db")
	a := newTestAppWithDB(t, dbPath)
	ctx := context.Background()

	// SkipProbe: the mock has no List handler, and the probe is
	// not what this test exercises — the save + quota path is.
	m, err := a.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "Drime 团队",
		Provider:    domain.ProviderDrime,
		AccessToken: "test-token",
		WorkspaceID: "8762",
		APIBase:     srv.URL + "/api/v1",
		SkipProbe:   true,
	})
	if err != nil {
		t.Fatalf("AddMountWithCredentials: %v", err)
	}
	if m.WorkspaceLabel != "工作区 8762" {
		t.Fatalf("WorkspaceLabel after add: got %q, want %q", m.WorkspaceLabel, "工作区 8762")
	}
	quotas := a.MountQuotas(ctx)
	if len(quotas) != 1 {
		t.Fatalf("MountQuotas: got %d entries, want 1", len(quotas))
	}
	if quotas[0].Used != 312345678901 || quotas[0].Total != 549511043624 {
		t.Fatalf("quota before restart: got used=%d total=%d, want 312345678901 / 549511043624", quotas[0].Used, quotas[0].Total)
	}

	// Fresh App on the same DB file, as after a restart.
	b := newTestAppWithDB(t, dbPath)
	if err := b.LoadMounts(); err != nil {
		t.Fatalf("LoadMounts: %v", err)
	}
	loaded := b.ListMounts()
	if len(loaded) != 1 {
		t.Fatalf("ListMounts after restart: got %d mounts, want 1", len(loaded))
	}
	if loaded[0].WorkspaceLabel != "工作区 8762" {
		t.Fatalf("WorkspaceLabel after restart: got %q, want %q", loaded[0].WorkspaceLabel, "工作区 8762")
	}
	quotas = b.MountQuotas(ctx)
	if len(quotas) != 1 {
		t.Fatalf("MountQuotas after restart: got %d entries, want 1", len(quotas))
	}
	if quotas[0].Used != 312345678901 || quotas[0].Total != 549511043624 {
		t.Fatalf("quota after restart: got used=%d total=%d, want 312345678901 / 549511043624", quotas[0].Used, quotas[0].Total)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0] != 8762 || seen[1] != 8762 {
		t.Fatalf("space-usage workspaceIds: got %v, want [8762 8762]", seen)
	}
}

// TestDrimeWorkspaceLabelPersonal: no workspace id means the
// personal workspace (0) — labelled "个人空间", never blank, so
// the sidebar never shows a quota number with no scope at all.
func TestDrimeWorkspaceLabelPersonal(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	m, err := a.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "Drime 个人",
		Provider:    domain.ProviderDrime,
		AccessToken: "test-token",
		SkipProbe:   true,
	})
	if err != nil {
		t.Fatalf("AddMountWithCredentials: %v", err)
	}
	if m.WorkspaceLabel != "个人空间" {
		t.Fatalf("WorkspaceLabel: got %q, want %q", m.WorkspaceLabel, "个人空间")
	}
}
