// SPDX-License-Identifier: MPL-2.0
package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
)

// Master-key file layout: base64(salt[16] || password[32]), one line,
// mode 0600, inside the config directory (e.g. ~/.config/zy-drive/
// master.key). It is generated once on first run; the file is never
// overwritten, because overwriting it would orphan every credential
// encrypted under the old key (all saved mounts would need to be
// re-added). There is no hard-coded password anywhere: the key is
// random per machine, and the config directory is outside the repo
// (master.key is also gitignore-listed as a belt-and-braces measure).
//
// The salt and the password are both random bytes from this file;
// NewSecretsStore PBKDF2-derives the AES-256-GCM key from them.
const masterKeyFile = "master.key"

// LoadOrCreateMasterKey returns the SecretsStore for dir, generating
// and persisting the master key on first use. A corrupt key file is
// an error, never silently replaced.
func LoadOrCreateMasterKey(dir string) (*SecretsStore, error) {
	path := filepath.Join(dir, masterKeyFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("auth: read master key %s: %w", path, err)
		}
		salt := make([]byte, 16)
		password := make([]byte, 32)
		if _, err := rand.Read(salt); err != nil {
			return nil, fmt.Errorf("auth: generate master key salt: %w", err)
		}
		if _, err := rand.Read(password); err != nil {
			return nil, fmt.Errorf("auth: generate master key: %w", err)
		}
		combined := append(append([]byte{}, salt...), password...)
		encoded := base64.StdEncoding.EncodeToString(combined)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("auth: mkdir %s: %w", dir, err)
		}
		// Write to a temp file and rename so a crash cannot leave a
		// half-written key behind.
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, []byte(encoded), 0o600); err != nil {
			return nil, fmt.Errorf("auth: write master key: %w", err)
		}
		if err := os.Rename(tmp, path); err != nil {
			_ = os.Remove(tmp)
			return nil, fmt.Errorf("auth: store master key: %w", err)
		}
		_ = os.Chmod(path, 0o600)
		return NewSecretsStore(base64.StdEncoding.EncodeToString(password), salt)
	}
	// Existing key: tighten permissions, then decode.
	_ = os.Chmod(path, 0o600)
	combined, err := base64.StdEncoding.DecodeString(string(bytesTrimSpace(raw)))
	if err != nil {
		return nil, fmt.Errorf("auth: master key %s is corrupt (not base64): %w", path, err)
	}
	if len(combined) != 48 {
		return nil, fmt.Errorf("auth: master key %s is corrupt (want 48 bytes, got %d); delete it only if you accept re-adding every saved mount", path, len(combined))
	}
	salt, password := combined[:16], combined[16:]
	return NewSecretsStore(base64.StdEncoding.EncodeToString(password), salt)
}

func bytesTrimSpace(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ' || b[len(b)-1] == '\t') {
		b = b[:len(b)-1]
	}
	for len(b) > 0 && (b[0] == '\n' || b[0] == '\r' || b[0] == ' ' || b[0] == '\t') {
		b = b[1:]
	}
	return b
}
