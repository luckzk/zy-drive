package cache

import (
	"path/filepath"
	"testing"
)

func TestSettingsRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "app.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Missing key is not an error — the caller applies its default.
	if v, ok, err := db.GetSetting("theme"); err != nil || ok || v != "" {
		t.Fatalf("missing key: v=%q ok=%v err=%v", v, ok, err)
	}
	if err := db.SetSetting("theme", "dark"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if v, ok, err := db.GetSetting("theme"); err != nil || !ok || v != "dark" {
		t.Fatalf("after set: v=%q ok=%v err=%v", v, ok, err)
	}
	// Overwrite works (upsert).
	if err := db.SetSetting("theme", "light"); err != nil {
		t.Fatalf("SetSetting overwrite: %v", err)
	}
	if v, _, _ := db.GetSetting("theme"); v != "light" {
		t.Fatalf("after overwrite: v=%q", v)
	}
	// "Restart": a fresh DB handle over the same file still has it.
	db2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open (reopen): %v", err)
	}
	defer db2.Close()
	if v, ok, err := db2.GetSetting("theme"); err != nil || !ok || v != "light" {
		t.Fatalf("after reopen: v=%q ok=%v err=%v", v, ok, err)
	}
}
