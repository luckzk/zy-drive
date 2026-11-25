// SPDX-License-Identifier: MPL-2.0
package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luckzk/zy-drive/internal/domain"
)

// TestCustomOAuthAppNeverTouchesBuiltin: saving a custom app, and
// switching back to the official app, must leave the builtin
// Client IDs exactly as shipped. This is the core promise of the
// separate custom store (v0.8.3): a user who stops using their
// own app always finds the builtin one-click authorization intact.
func TestCustomOAuthAppNeverTouchesBuiltin(t *testing.T) {
	a := newTestApp(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "custom-oauth-clients.json")
	if err := a.LoadCustomOAuthClients(path); err != nil {
		t.Fatalf("LoadCustomOAuthClients (missing file): %v", err)
	}
	want := map[domain.ProviderKind]string{
		domain.ProviderDropbox:  "ocx73kxhcpga24m",
		domain.ProviderDrive:    "504630572830-9nkremocc6fsmbrg0lpk5n185ii4b447.apps.googleusercontent.com",
		domain.ProviderOneDrive: "367e206e-fdfb-4372-8380-7f795ee42533",
	}
	checkBuiltin := func(where string) {
		t.Helper()
		for p, id := range want {
			if got := a.BuiltinOAuthClientID(p); got != id {
				t.Fatalf("%s: BuiltinOAuthClientID(%s) = %q, want %q (builtin must never change)", where, p, got, id)
			}
		}
	}
	checkBuiltin("before custom save")

	// Save a custom app for Dropbox (with a secret) and Drive.
	if err := a.SaveCustomOAuthApp(domain.ProviderDropbox, "my-dbx-key", "my-dbx-secret"); err != nil {
		t.Fatalf("SaveCustomOAuthApp(dropbox): %v", err)
	}
	if err := a.SaveCustomOAuthApp(domain.ProviderDrive, "my-drive-id.apps.googleusercontent.com", ""); err != nil {
		t.Fatalf("SaveCustomOAuthApp(drive): %v", err)
	}
	checkBuiltin("after custom save")

	got := a.GetCustomOAuthApp(domain.ProviderDropbox)
	if got == nil || got.ClientID != "my-dbx-key" || got.ClientSecret != "my-dbx-secret" || !got.HasSecret {
		t.Fatalf("GetCustomOAuthApp(dropbox) = %+v, want the saved custom app with its secret", got)
	}
	if got := a.GetCustomOAuthApp(domain.ProviderOneDrive); got != nil {
		t.Fatalf("GetCustomOAuthApp(onedrive) = %+v, want nil (nothing saved)", got)
	}
	// The secret must be encrypted on disk, never plaintext.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read custom file: %v", err)
	}
	if strings.Contains(string(raw), "my-dbx-secret") {
		t.Fatalf("custom-oauth-clients.json contains the plaintext secret — it must be encrypted")
	}
	if !strings.Contains(string(raw), "my-dbx-key") {
		t.Fatalf("custom-oauth-clients.json should contain the plaintext Client ID, got %s", raw)
	}

	// Delete the custom app: the builtin is still there, and the
	// custom entry is gone (switching back to official works).
	if err := a.DeleteCustomOAuthApp(domain.ProviderDropbox); err != nil {
		t.Fatalf("DeleteCustomOAuthApp: %v", err)
	}
	if got := a.GetCustomOAuthApp(domain.ProviderDropbox); got != nil {
		t.Fatalf("after delete, GetCustomOAuthApp(dropbox) = %+v, want nil", got)
	}
	checkBuiltin("after custom delete")

	// Reload from disk: the surviving Drive entry comes back.
	b := newTestApp(t)
	if err := b.LoadCustomOAuthClients(path); err != nil {
		t.Fatalf("LoadCustomOAuthClients (reload): %v", err)
	}
	if got := b.GetCustomOAuthApp(domain.ProviderDrive); got == nil || got.ClientID != "my-drive-id.apps.googleusercontent.com" || got.HasSecret {
		t.Fatalf("reloaded GetCustomOAuthApp(drive) = %+v, want the saved entry without a secret", got)
	}
	if got := b.GetCustomOAuthApp(domain.ProviderDropbox); got != nil {
		t.Fatalf("reloaded GetCustomOAuthApp(dropbox) = %+v, want nil (deleted before reload)", got)
	}
}

// TestCustomOAuthAppValidation: non-OAuth providers and empty
// saves are rejected / treated as deletes; a malformed custom
// file is an error, never a silent partial load.
func TestCustomOAuthAppValidation(t *testing.T) {
	a := newTestApp(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "custom-oauth-clients.json")
	if err := a.LoadCustomOAuthClients(path); err != nil {
		t.Fatalf("LoadCustomOAuthClients: %v", err)
	}
	if err := a.SaveCustomOAuthApp(domain.ProviderS3, "x", ""); err == nil {
		t.Fatalf("SaveCustomOAuthApp(s3): want an error (not an OAuth provider)")
	}
	if err := a.SaveCustomOAuthApp(domain.ProviderDrime, "x", ""); err == nil {
		t.Fatalf("SaveCustomOAuthApp(drime): want an error (PAT-only, no OAuth app)")
	}
	if err := os.WriteFile(path, []byte(`{not json`), 0o600); err != nil {
		t.Fatalf("write bad custom file: %v", err)
	}
	if err := a.LoadCustomOAuthClients(path); err == nil {
		t.Fatalf("malformed custom-oauth-clients.json: want an error")
	}
}

// TestMigrateLegacyOAuthClients: the old oauth-clients.json
// (builtin override semantics) is moved into the custom store
// and retired — its values are never lost, and never again
// applied as a builtin override.
func TestMigrateLegacyOAuthClients(t *testing.T) {
	a := newTestApp(t)
	dir := t.TempDir()
	legacy := filepath.Join(dir, "oauth-clients.json")
	custom := filepath.Join(dir, "custom-oauth-clients.json")
	if err := a.LoadCustomOAuthClients(custom); err != nil {
		t.Fatalf("LoadCustomOAuthClients: %v", err)
	}
	// The user already has a custom Drive app: the legacy Drive
	// value must NOT overwrite it.
	if err := a.SaveCustomOAuthApp(domain.ProviderDrive, "my-drive-id", ""); err != nil {
		t.Fatalf("SaveCustomOAuthApp(drive): %v", err)
	}
	if err := os.WriteFile(legacy, []byte(`{"dropbox": "legacy-dbx-key", "drive": "legacy-drive-id", "unknown": "x"}`), 0o600); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}
	n, err := a.MigrateLegacyOAuthClients(legacy, custom)
	if err != nil {
		t.Fatalf("MigrateLegacyOAuthClients: %v", err)
	}
	if n != 1 {
		t.Fatalf("migrated = %d, want 1 (only Dropbox; Drive already had a custom app, unknown ignored)", n)
	}
	if got := a.GetCustomOAuthApp(domain.ProviderDropbox); got == nil || got.ClientID != "legacy-dbx-key" {
		t.Fatalf("GetCustomOAuthApp(dropbox) after migration = %+v, want legacy-dbx-key", got)
	}
	if got := a.GetCustomOAuthApp(domain.ProviderDrive); got == nil || got.ClientID != "my-drive-id" {
		t.Fatalf("GetCustomOAuthApp(drive) after migration = %+v, want my-drive-id (legacy must not overwrite)", got)
	}
	// The builtin is untouched by the migration.
	if got := a.BuiltinOAuthClientID(domain.ProviderDropbox); got != "ocx73kxhcpga24m" {
		t.Fatalf("BuiltinOAuthClientID(dropbox) after migration = %q, want the builtin default", got)
	}
	// The legacy file is retired, exactly once.
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy oauth-clients.json should be renamed away, stat err = %v", err)
	}
	if _, err := os.Stat(legacy + ".migrated"); err != nil {
		t.Fatalf("legacy file should exist as .migrated: %v", err)
	}
	// A second migration is a no-op (the file is gone).
	n, err = a.MigrateLegacyOAuthClients(legacy, custom)
	if err != nil || n != 0 {
		t.Fatalf("second migration = (%d, %v), want (0, nil)", n, err)
	}
}
