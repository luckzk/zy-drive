// SPDX-License-Identifier: MPL-2.0
package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/luckzk/zy-drive/internal/netproxy"
)

func TestSettingsRoundTripAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	downloadDir := filepath.Join(dir, "downloads")
	if err := os.Mkdir(downloadDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "app.db")
	a := newTestAppWithDB(t, dbPath)

	// Fresh install: defaults.
	got, err := a.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings (fresh): %v", err)
	}
	if got != DefaultSettings() {
		t.Fatalf("fresh settings should be defaults, got %+v", got)
	}

	// Save a non-default set, then "restart" (fresh App, same DB).
	// The proxy fields ride along: manual mode with a bare
	// host:port is stored normalized (http://...), and saving
	// applies it to the shared transport at once.
	want := Settings{
		Theme: "dark", Language: "en", DownloadDir: downloadDir,
		SyncCompare: "checksum", SyncConcurrency: 8,
		SyncSpeedLimitKBps: 1024, SyncConflict: "keep-both",
		ProxyMode: "manual", ProxyHTTP: "http://127.0.0.1:18080", ProxySOCKS5: "",
	}
	if err := a.SaveSettings(want); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if cur := netproxy.Current(); cur.Mode != netproxy.ModeManual || cur.HTTP != "http://127.0.0.1:18080" {
		t.Fatalf("SaveSettings must apply the proxy at once, got %+v", cur)
	}
	// The proxy config is process-global: restore the default so
	// later tests (mock token endpoints on loopback) are unaffected.
	defer func() { _ = netproxy.Configure(netproxy.DefaultConfig()) }()
	a2 := reopenApp(t, dbPath)
	got, err = a2.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings (after restart): %v", err)
	}
	if got != want {
		t.Fatalf("settings after restart: got %+v, want %+v", got, want)
	}
}

func TestSettingsValidation(t *testing.T) {
	a := newTestApp(t)
	base := DefaultSettings()

	// Unknown enum values are rejected, nothing is saved.
	for _, bad := range []Settings{
		{Theme: "sepia", Language: "zh"},
		{Theme: "light", Language: "fr"},
		{Theme: "light", Language: "zh", SyncCompare: "magic"},
		{Theme: "light", Language: "zh", SyncConflict: "newer-wins"},
	} {
		s := base
		s.Theme, s.Language, s.SyncCompare, s.SyncConflict = bad.Theme, bad.Language, bad.SyncCompare, bad.SyncConflict
		if s.SyncCompare == "" {
			s.SyncCompare = base.SyncCompare
		}
		if s.SyncConflict == "" {
			s.SyncConflict = base.SyncConflict
		}
		if err := a.SaveSettings(s); err == nil {
			t.Fatalf("SaveSettings(%+v) must fail", s)
		}
	}
	// Concurrency out of range.
	s := base
	s.SyncConcurrency = 0
	if err := a.SaveSettings(s); err == nil {
		t.Fatalf("concurrency 0 must fail")
	}
	s.SyncConcurrency = 17
	if err := a.SaveSettings(s); err == nil {
		t.Fatalf("concurrency 17 must fail")
	}
	// Negative speed limit.
	s = base
	s.SyncSpeedLimitKBps = -1
	if err := a.SaveSettings(s); err == nil {
		t.Fatalf("negative speed limit must fail")
	}
	// Download folder must exist and be a directory.
	s = base
	s.DownloadDir = filepath.Join(t.TempDir(), "no-such-dir")
	if err := a.SaveSettings(s); err == nil {
		t.Fatalf("nonexistent download dir must fail")
	}
	f := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.DownloadDir = f
	if err := a.SaveSettings(s); err == nil {
		t.Fatalf("a file (not a dir) as download dir must fail")
	}
	// A real directory passes, and CheckDownloadDir agrees.
	d := t.TempDir()
	s.DownloadDir = d
	if err := a.SaveSettings(s); err != nil {
		t.Fatalf("real download dir must save: %v", err)
	}
	if err := a.CheckDownloadDir(d); err != nil {
		t.Fatalf("CheckDownloadDir(real dir): %v", err)
	}
	if err := a.CheckDownloadDir(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatalf("CheckDownloadDir(nonexistent) must fail")
	}
	// Rejected saves left the stored settings untouched (still the
	// valid download dir from above).
	got, _ := a.GetSettings()
	if got.DownloadDir != d {
		t.Fatalf("failed saves must not change stored settings: %+v", got)
	}
}

func TestOpenAtLoginWithoutHooks(t *testing.T) {
	a := newTestApp(t)
	// No hooks installed (dev shell / tests): honest error, not a
	// fake value.
	if _, err := a.GetOpenAtLogin(); err == nil {
		t.Fatalf("GetOpenAtLogin without hooks must error")
	}
	if err := a.SetOpenAtLogin(true); err == nil {
		t.Fatalf("SetOpenAtLogin without hooks must error")
	}
	// With hooks installed (as cmd/desktop does), values round-trip.
	state := false
	a.SetLoginHooks(
		func() (bool, error) { return state, nil },
		func(on bool) error { state = on; return nil },
	)
	if on, err := a.GetOpenAtLogin(); err != nil || on {
		t.Fatalf("hooked GetOpenAtLogin: on=%v err=%v", on, err)
	}
	if err := a.SetOpenAtLogin(true); err != nil {
		t.Fatalf("hooked SetOpenAtLogin: %v", err)
	}
	if on, _ := a.GetOpenAtLogin(); !on {
		t.Fatalf("hooked state should be on after set")
	}
}

func TestGetVersions(t *testing.T) {
	a := newTestApp(t)
	v := a.GetVersions()
	if v.Backend == "" || v.Protocol == "" || v.Engine == "" {
		t.Fatalf("versions must not be empty: %+v", v)
	}
}
