// SPDX-License-Identifier: MPL-2.0
package app

import (
	"testing"

	"github.com/luckzk/zy-drive/internal/domain"
)

// TestSaveMountEnabledPersists verifies the FUSE switch
// round-trips through SQLite: write the switch, re-read the
// mount row, the value is the same (config map key
// "mount_enabled", no new mounts-table column).
func TestSaveMountEnabledPersists(t *testing.T) {
	a := newTestApp(t)
	m := &domain.Mount{
		ID: "m1", DisplayName: "工作 Drive", Provider: domain.ProviderDrive,
		Status:   domain.MountOK,
		Caps:     domain.Caps{SupportsMount: true},
		Verified: true,
	}
	if err := a.DB.SaveMount(m, map[string]string{"client_id": "x"}); err != nil {
		t.Fatalf("SaveMount: %v", err)
	}
	a.setHandle("m1", MountHandle{Mount: m, Provider: nil})
	readEnabled := func() (bool, string) {
		rows, err := a.DB.ListMounts()
		if err != nil {
			t.Fatalf("ListMounts: %v", err)
		}
		for _, pm := range rows {
			if pm.Mount.ID == "m1" {
				return pm.Mount.MountEnabled, pm.Config["mount_enabled"]
			}
		}
		t.Fatalf("mount m1 not found after save")
		return false, ""
	}
	if enabled, raw := readEnabled(); enabled || raw != "" {
		t.Fatalf("fresh mount should read disabled, got enabled=%v raw=%q", enabled, raw)
	}
	if err := a.saveMountEnabled("m1", true); err != nil {
		t.Fatalf("saveMountEnabled(true): %v", err)
	}
	if enabled, raw := readEnabled(); !enabled || raw != "1" {
		t.Fatalf("after enable: enabled=%v raw=%q, want true/\"1\"", enabled, raw)
	}
	if err := a.saveMountEnabled("m1", false); err != nil {
		t.Fatalf("saveMountEnabled(false): %v", err)
	}
	if enabled, raw := readEnabled(); enabled || raw != "0" {
		t.Fatalf("after disable: enabled=%v raw=%q, want false/\"0\"", enabled, raw)
	}
	// A second App on the same DB reads the same switch (the
	// "restart" half of the round-trip).
	if enabled, _ := readEnabled(); enabled {
		t.Fatalf("re-read after disable should be false")
	}
}

// TestSettingsMountCacheLimitGB checks the mount-cache cap
// setting (mount.cache_gb, 2/5/10/20, default 5).
func TestSettingsMountCacheLimitGB(t *testing.T) {
	a := newTestApp(t)
	s, err := a.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if s.MountCacheLimitGB != 5 {
		t.Fatalf("default mount cache limit = %d GB, want 5", s.MountCacheLimitGB)
	}
	s.MountCacheLimitGB = 10
	if err := a.SaveSettings(s); err != nil {
		t.Fatalf("SaveSettings(10): %v", err)
	}
	s2, err := a.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if s2.MountCacheLimitGB != 10 {
		t.Fatalf("saved mount cache limit = %d GB, want 10", s2.MountCacheLimitGB)
	}
	s.MountCacheLimitGB = 7
	if err := a.SaveSettings(s); err == nil {
		t.Fatalf("SaveSettings(7) should reject a non-tier value")
	}
}
