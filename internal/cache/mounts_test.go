// SPDX-License-Identifier: MPL-2.0
package cache

import (
	"path/filepath"
	"testing"

	"github.com/luckzk/zy-drive/internal/domain"
)

func TestMountsRoundTrip(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	defer db.Close()

	m := &domain.Mount{
		ID: "mnt-1", DisplayName: "工作 Drive", Provider: domain.ProviderDrive,
		AccountID: "mount:mnt-1", RootRef: "", RootLabel: "", Group: "工作",
		Status: domain.MountOK, Verified: true,
	}
	cfg := map[string]string{"client_id": "cid-1", "rootId": "root"}
	if err := db.SaveMount(m, cfg); err != nil {
		t.Fatalf("SaveMount: %v", err)
	}
	got, err := db.ListMounts()
	if err != nil {
		t.Fatalf("ListMounts: %v", err)
	}
	if len(got) != 1 || got[0].Mount.DisplayName != "工作 Drive" ||
		got[0].Mount.Group != "工作" || got[0].Config["client_id"] != "cid-1" ||
		!got[0].Mount.Verified {
		t.Fatalf("unexpected mounts after save: %+v", got)
	}
	// Group change persists through the dedicated update.
	if err := db.UpdateMountGroup("mnt-1", "生活"); err != nil {
		t.Fatalf("UpdateMountGroup: %v", err)
	}
	got, _ = db.ListMounts()
	if got[0].Mount.Group != "生活" {
		t.Fatalf("group after update: %q", got[0].Mount.Group)
	}
	// Secrets round-trip through the encrypted-blob table.
	if err := db.SaveSecret("mount:mnt-1", []byte{1, 2, 3}); err != nil {
		t.Fatalf("SaveSecret: %v", err)
	}
	ct, err := db.LoadSecret("mount:mnt-1")
	if err != nil || len(ct) != 3 {
		t.Fatalf("LoadSecret: %v %v", ct, err)
	}
	if err := db.DeleteMount("mnt-1"); err != nil {
		t.Fatalf("DeleteMount: %v", err)
	}
	if err := db.DeleteSecret("mount:mnt-1"); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}
	got, _ = db.ListMounts()
	if len(got) != 0 {
		t.Fatalf("expected no mounts after delete, got %d", len(got))
	}
	if _, err := db.LoadSecret("mount:mnt-1"); err == nil {
		t.Fatalf("secret should be gone after delete")
	}
}
