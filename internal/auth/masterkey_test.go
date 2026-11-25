package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMasterKeyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s1, err := LoadOrCreateMasterKey(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateMasterKey: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatalf("master.key missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("master.key perms = %o, want 0600", info.Mode().Perm())
	}
	ct, err := s1.Encrypt([]byte("refresh-token-abc"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	// A second load (simulating a restart) must derive the same key.
	s2, err := LoadOrCreateMasterKey(dir)
	if err != nil {
		t.Fatalf("second LoadOrCreateMasterKey: %v", err)
	}
	plain, err := s2.Decrypt(ct)
	if err != nil {
		t.Fatalf("Decrypt with reloaded key: %v", err)
	}
	if string(plain) != "refresh-token-abc" {
		t.Fatalf("decrypted = %q", plain)
	}
	// The key file is stable: no rewrite on reload.
	data1, _ := os.ReadFile(filepath.Join(dir, "master.key"))
	s3, err := LoadOrCreateMasterKey(dir)
	if err != nil {
		t.Fatalf("third load: %v", err)
	}
	_ = s3
	data2, _ := os.ReadFile(filepath.Join(dir, "master.key"))
	if string(data1) != string(data2) {
		t.Fatalf("master.key was rewritten on reload")
	}
}

func TestMasterKeyCorruptIsErrorNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "master.key"), []byte("not-base64!!!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateMasterKey(dir); err == nil {
		t.Fatalf("corrupt master.key must be an error, never silently replaced")
	}
}
