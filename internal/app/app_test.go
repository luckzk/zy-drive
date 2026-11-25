// SPDX-License-Identifier: MPL-2.0
package app

import (
	"path/filepath"
	"testing"

	"github.com/luckzk/zy-drive/internal/auth"
	"github.com/luckzk/zy-drive/internal/cache"
	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/storage/providers/drive"
	"github.com/luckzk/zy-drive/internal/storage/providers/drime"
	"github.com/luckzk/zy-drive/internal/storage/providers/dropbox"
	"github.com/luckzk/zy-drive/internal/storage/providers/local"
	"github.com/luckzk/zy-drive/internal/storage/providers/onedrive"
	"github.com/luckzk/zy-drive/internal/storage/providers/s3"
	"github.com/luckzk/zy-drive/internal/transfer"
)

// testSecretsStore builds a throwaway master-key store (same
// password+salt every call, so a "restarted" App can be built with an
// equal store and still decrypt what the first App saved).
func testSecretsStore(t *testing.T) *auth.SecretsStore {
	t.Helper()
	s, err := auth.NewSecretsStore("test-master-password", []byte("test-salt-16bytes"))
	if err != nil {
		t.Fatalf("NewSecretsStore: %v", err)
	}
	return s
}

func newTestAppWithDB(t *testing.T, dbPath string) *App {
	t.Helper()
	db, err := cache.Open(dbPath)
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := storage.Default()
	// Registry is process-wide; registration panics on duplicates, so
	// only register once per test binary run.
	register := func(kind domain.ProviderKind, f storage.Factory) {
		defer func() { _ = recover() }()
		reg.Register(kind, f)
	}
	register(domain.ProviderLocal, local.New)
	register(domain.ProviderS3, s3.New)
	register(domain.ProviderDropbox, dropbox.New)
	register(domain.ProviderDrive, drive.New)
	register(domain.ProviderDrime, drime.New)
	register(domain.ProviderOneDrive, onedrive.New)
	a := New(db, reg, transfer.NewQueue(db))
	a.SetSecrets(testSecretsStore(t))
	return a
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	return newTestAppWithDB(t, filepath.Join(t.TempDir(), "app.db"))
}

func TestValidateJobLocalOnlyRunsLocal(t *testing.T) {
	local := &domain.SyncJob{
		ID: "j1", Name: "photos", SrcMount: domain.LocalMountID, SrcRef: "local:///tmp/a",
		DstMount: "m1", DstRef: "/photos", Direction: domain.DirectionCopy,
		RunOn: domain.RunOnLocal,
	}
	if err := ValidateJob(local); err != nil {
		t.Fatalf("local job on local runner should validate: %v", err)
	}
	local.RunOn = domain.RunOnAgent
	local.AgentID = "ag-1"
	if err := ValidateJob(local); err == nil {
		t.Fatalf("local folder on an Agent must be rejected")
	}
	local.RunOn = domain.RunOnLocal
	local.AgentID = ""
	local.SrcRef = "/tmp/a" // missing local:// scheme
	if err := ValidateJob(local); err == nil {
		t.Fatalf("local ref without local:// scheme must be rejected")
	}
}

func TestValidateJobAgentRunOn(t *testing.T) {
	job := &domain.SyncJob{
		ID: "j1", Name: "x", SrcMount: "m1", SrcRef: "/a",
		DstMount: "m2", DstRef: "/b", Direction: domain.DirectionMirror,
		RunOn: domain.RunOnAgent, AgentID: "ag-1",
	}
	if err := ValidateJob(job); err != nil {
		t.Fatalf("agent job should validate: %v", err)
	}
	job.RunOn = domain.RunOnLocal
	if err := ValidateJob(job); err == nil {
		t.Fatalf("agentId with runOn=local must be rejected")
	}
}

func TestAgentsRegistry(t *testing.T) {
	a := newTestApp(t)
	if err := a.AddAgent(&domain.Agent{ID: "ag-1", Name: "家里", Address: "192.168.1.10:8080"}); err != nil {
		t.Fatalf("AddAgent: %v", err)
	}
	if err := a.AddAgent(&domain.Agent{ID: "ag-2", Name: "OCI", Address: "oci.example.com:8080"}); err != nil {
		t.Fatalf("AddAgent: %v", err)
	}
	if got := a.DefaultAgent(); got == nil || got.ID != "ag-1" {
		t.Fatalf("first agent should be default, got %+v", got)
	}
	if st := a.AgentStatus(); st.Total != 2 || st.Online != 0 {
		t.Fatalf("AgentStatus: %+v", st)
	}
	job := &domain.SyncJob{ID: "j1", Name: "x", RunOn: domain.RunOnAgent, AgentID: "ag-2"}
	ag, err := a.AgentForJob(job)
	if err != nil || ag.ID != "ag-2" {
		t.Fatalf("AgentForJob: %v %+v", err, ag)
	}
	if _, err := a.AgentForJob(&domain.SyncJob{RunOn: domain.RunOnAgent, AgentID: "nope"}); err == nil {
		t.Fatalf("unknown agent must error")
	}
	// Persisted: a fresh DB read sees both agents.
	agents, err := a.DB.ListAgents()
	if err != nil || len(agents) != 2 {
		t.Fatalf("ListAgents from cache: %v, %d", err, len(agents))
	}
}

func TestLocalProviderThroughApp(t *testing.T) {
	a := newTestApp(t)
	p, err := a.ProviderFor(domain.LocalMountID)
	if err != nil {
		t.Fatalf("ProviderFor(local): %v", err)
	}
	if p.Caps().IDBased {
		t.Fatalf("local provider is path-based")
	}
	m, err := a.MountByID(domain.LocalMountID)
	if err != nil || m.Provider != domain.ProviderLocal {
		t.Fatalf("MountByID(local): %v %+v", err, m)
	}
}
