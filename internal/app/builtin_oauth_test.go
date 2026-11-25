// SPDX-License-Identifier: MPL-2.0
package app

import (
	"testing"

	"github.com/luckzk/zy-drive/internal/domain"
)

// TestBuiltinOAuthClientID: the shipped defaults are the three
// registrations the user completed 2026-11-25 (Dropbox, Drive
// and OneDrive, app "ZyDrive-app" in all three consoles). They
// come from the embedded builtin-oauth-clients.json (with the
// code constants as fallback), and a user's own app never changes
// them — see TestCustomOAuthAppNeverTouchesBuiltin.
func TestBuiltinOAuthClientID(t *testing.T) {
	a := newTestApp(t)
	if got := a.BuiltinOAuthClientID(domain.ProviderDropbox); got != "ocx73kxhcpga24m" {
		t.Fatalf("BuiltinOAuthClientID(dropbox) = %q, want ocx73kxhcpga24m", got)
	}
	if got := a.BuiltinOAuthClientID(domain.ProviderDrive); got != "504630572830-9nkremocc6fsmbrg0lpk5n185ii4b447.apps.googleusercontent.com" {
		t.Fatalf("BuiltinOAuthClientID(drive) = %q, want the ZyDrive-app Google Client ID", got)
	}
	if got := a.BuiltinOAuthClientID(domain.ProviderOneDrive); got != "367e206e-fdfb-4372-8380-7f795ee42533" {
		t.Fatalf("BuiltinOAuthClientID(onedrive) = %q, want the ZyDrive-app Azure Client ID", got)
	}
	// Drime has no builtin OAuth app (PAT-only in the wizard).
	if got := a.BuiltinOAuthClientID(domain.ProviderDrime); got != "" {
		t.Fatalf("BuiltinOAuthClientID(drime) = %q, want empty (no builtin Drime app)", got)
	}
}

// TestLoadBuiltinClientsJSON: the embedded JSON is the source of
// truth; "_comment" keys are ignored, unknown providers are
// ignored, an empty value clears that provider, and a malformed
// file falls back to the code constants instead of failing.
func TestLoadBuiltinClientsJSON(t *testing.T) {
	good := loadBuiltinClientsFrom([]byte(`{"_comment": "note", "dropbox": "dbx-key", "drive": "", "unknown": "x", "onedrive": "ms-id"}`))
	if got := good[domain.ProviderDropbox]; got != "dbx-key" {
		t.Fatalf("dropbox = %q, want dbx-key", got)
	}
	if _, ok := good[domain.ProviderDrive]; ok {
		t.Fatalf("drive should be cleared by an empty value, got %q", good[domain.ProviderDrive])
	}
	if got := good[domain.ProviderOneDrive]; got != "ms-id" {
		t.Fatalf("onedrive = %q, want ms-id", got)
	}
	if len(good) != 2 {
		t.Fatalf("want exactly 2 providers after ignoring _comment/unknown, got %v", good)
	}

	// Malformed JSON: the constants stand in (all three present).
	bad := loadBuiltinClientsFrom([]byte(`{not json`))
	if len(bad) != 3 {
		t.Fatalf("malformed JSON should fall back to 3 constants, got %v", bad)
	}
	if bad[domain.ProviderDropbox] != "ocx73kxhcpga24m" {
		t.Fatalf("malformed fallback dropbox = %q, want the builtin default", bad[domain.ProviderDropbox])
	}
}
