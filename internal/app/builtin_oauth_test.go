package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/luckzk/zy-drive/internal/domain"
)

// TestBuiltinOAuthClientID: with no registration (the state this
// repo ships in — the three official apps are not registered
// yet), every provider reports "" and the wizard must fall back
// to the self-built flow. A loaded oauth-clients.json enables
// exactly the providers it names, and unknown keys are ignored.
func TestBuiltinOAuthClientID(t *testing.T) {
	a := newTestApp(t)
	for _, p := range []domain.ProviderKind{domain.ProviderDropbox, domain.ProviderDrive, domain.ProviderOneDrive} {
		if got := a.BuiltinOAuthClientID(p); got != "" {
			t.Fatalf("BuiltinOAuthClientID(%s) = %q before any registration, want empty", p, got)
		}
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
	// Unknown keys are ignored; a provider left out stays "".
	if err := os.WriteFile(filepath.Join(dir, "oauth-clients.json"),
		[]byte(`{"dropbox": "dbx-app-key", "unknown-provider": "x"}`), 0o600); err != nil {
		t.Fatalf("write oauth-clients.json: %v", err)
	}
	if err := LoadBuiltinOAuthClients(filepath.Join(dir, "oauth-clients.json")); err != nil {
		t.Fatalf("LoadBuiltinOAuthClients: %v", err)
	}
	if got := a.BuiltinOAuthClientID(domain.ProviderDropbox); got != "dbx-app-key" {
		t.Fatalf("BuiltinOAuthClientID(dropbox) = %q, want dbx-app-key", got)
	}
	if got := a.BuiltinOAuthClientID(domain.ProviderDrive); got != "" {
		t.Fatalf("BuiltinOAuthClientID(drive) = %q, want empty (not in the file)", got)
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
}
