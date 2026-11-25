package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/luckzk/zy-drive/internal/domain"
)

// TestBuiltinOAuthClientID: the shipped defaults are the two
// registrations the user completed 2026-11-25 (Dropbox and Drive,
// app "ZyDrive-app" in both consoles); OneDrive is still "" until
// its Azure registration is done, so the wizard must fall back to
// the self-built flow for OneDrive only. A loaded
// oauth-clients.json overrides exactly the providers it names
// (non-empty values), and unknown keys are ignored.
func TestBuiltinOAuthClientID(t *testing.T) {
	a := newTestApp(t)
	if got := a.BuiltinOAuthClientID(domain.ProviderDropbox); got != "ocx73kxhcpga24m" {
		t.Fatalf("BuiltinOAuthClientID(dropbox) = %q, want ocx73kxhcpga24m", got)
	}
	if got := a.BuiltinOAuthClientID(domain.ProviderDrive); got != "504630572830-9nkremocc6fsmbrg0lpk5n185ii4b447.apps.googleusercontent.com" {
		t.Fatalf("BuiltinOAuthClientID(drive) = %q, want the ZyDrive-app Google Client ID", got)
	}
	if got := a.BuiltinOAuthClientID(domain.ProviderOneDrive); got != "" {
		t.Fatalf("BuiltinOAuthClientID(onedrive) = %q before its Azure registration, want empty", got)
	}

	// Save/restore: builtinClients is process-wide; a test that
	// fills it must not leak into other tests' fallback assertions.
	builtinMu.Lock()
	saved := builtinClients
	builtinClients = map[domain.ProviderKind]string{}
	builtinMu.Unlock()
	defer func() {
		builtinMu.Lock()
		builtinClients = saved
		builtinMu.Unlock()
	}()

	dir := t.TempDir()
	// Unknown keys are ignored; a provider left out keeps its
	// builtin default (here: drive is not in the file, and the
	// map was reset, so drive stays "" in this isolated run —
	// the point is the file does not touch providers it omits).
	if err := os.WriteFile(filepath.Join(dir, "oauth-clients.json"),
		[]byte(`{"dropbox": "dbx-app-key", "unknown-provider": "x"}`), 0o600); err != nil {
		t.Fatalf("write oauth-clients.json: %v", err)
	}
	if err := LoadBuiltinOAuthClients(filepath.Join(dir, "oauth-clients.json")); err != nil {
		t.Fatalf("LoadBuiltinOAuthClients: %v", err)
	}
	if got := a.BuiltinOAuthClientID(domain.ProviderDropbox); got != "dbx-app-key" {
		t.Fatalf("BuiltinOAuthClientID(dropbox) = %q, want dbx-app-key (file overrides the builtin default)", got)
	}
	if got := a.BuiltinOAuthClientID(domain.ProviderDrive); got != "" {
		t.Fatalf("BuiltinOAuthClientID(drive) = %q, want empty (not in the file, map was reset)", got)
	}
	// A malformed file is an error, never a silent partial load.
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{not json`), 0o600); err != nil {
		t.Fatalf("write bad.json: %v", err)
	}
	if err := LoadBuiltinOAuthClients(filepath.Join(dir, "bad.json")); err == nil {
		t.Fatalf("malformed oauth-clients.json: want an error")
	}
	// A missing file is simply "not configured".
	if err := LoadBuiltinOAuthClients(filepath.Join(dir, "missing.json")); err != nil {
		t.Fatalf("missing oauth-clients.json: %v", err)
	}

	// Override semantics with the builtin defaults in place: a
	// file naming only OneDrive must leave the Dropbox/Drive
	// defaults untouched.
	builtinMu.Lock()
	builtinClients = map[domain.ProviderKind]string{
		domain.ProviderDropbox:  "ocx73kxhcpga24m",
		domain.ProviderDrive:    "504630572830-9nkremocc6fsmbrg0lpk5n185ii4b447.apps.googleusercontent.com",
		domain.ProviderOneDrive: "",
	}
	builtinMu.Unlock()
	if err := os.WriteFile(filepath.Join(dir, "override.json"),
		[]byte(`{"onedrive": "ms-app-id"}`), 0o600); err != nil {
		t.Fatalf("write override.json: %v", err)
	}
	if err := LoadBuiltinOAuthClients(filepath.Join(dir, "override.json")); err != nil {
		t.Fatalf("LoadBuiltinOAuthClients override: %v", err)
	}
	if got := a.BuiltinOAuthClientID(domain.ProviderOneDrive); got != "ms-app-id" {
		t.Fatalf("BuiltinOAuthClientID(onedrive) = %q, want ms-app-id", got)
	}
	if got := a.BuiltinOAuthClientID(domain.ProviderDropbox); got != "ocx73kxhcpga24m" {
		t.Fatalf("BuiltinOAuthClientID(dropbox) = %q, want the builtin default kept", got)
	}
	if got := a.BuiltinOAuthClientID(domain.ProviderDrive); got != "504630572830-9nkremocc6fsmbrg0lpk5n185ii4b447.apps.googleusercontent.com" {
		t.Fatalf("BuiltinOAuthClientID(drive) = %q, want the builtin default kept", got)
	}
}
