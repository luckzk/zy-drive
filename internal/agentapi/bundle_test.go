// SPDX-License-Identifier: MPL-2.0
package agentapi

import (
	"strings"
	"testing"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
)

func TestBundleRoundTrip(t *testing.T) {
	b := &Bundle{
		AgentName: "测试服务器",
		Mounts: []BundleMount{{
			Mount:  domain.Mount{ID: "mnt-1", DisplayName: "R2 备份", Provider: domain.ProviderS3},
			Config: map[string]string{"bucket": "bkt"},
			Credentials: BundleCredentials{
				AccessKey: "ak-1", SecretKey: "sk-1",
			},
		}},
		Jobs: []domain.SyncJob{{
			ID: "job-1", Name: "照片同步", SrcMount: "mnt-1", DstMount: "mnt-2",
			Direction: domain.DirectionCopy, RunOn: domain.RunOnAgent, AgentID: "ag-1",
			Schedule: "daily 02:00", Enabled: true,
		}},
	}
	data, err := EncryptBundle(b, "correct-horse-battery")
	if err != nil {
		t.Fatalf("EncryptBundle: %v", err)
	}
	got, err := DecryptBundle(data, "correct-horse-battery")
	if err != nil {
		t.Fatalf("DecryptBundle: %v", err)
	}
	if got.AgentName != "测试服务器" || len(got.Mounts) != 1 || len(got.Jobs) != 1 {
		t.Fatalf("bundle = %+v, want 1 mount + 1 job", got)
	}
	if got.Mounts[0].Credentials.AccessKey != "ak-1" || got.Mounts[0].Credentials.SecretKey != "sk-1" {
		t.Fatalf("credentials = %+v, want ak-1/sk-1", got.Mounts[0].Credentials)
	}
	if got.Jobs[0].Schedule != "daily 02:00" || got.Jobs[0].ID != "job-1" {
		t.Fatalf("job = %+v, want job-1 daily 02:00", got.Jobs[0])
	}
	if got.ExportedAt.IsZero() || time.Since(got.ExportedAt) > time.Minute {
		t.Fatalf("ExportedAt = %v, want ~now", got.ExportedAt)
	}
	// Wrong passphrase: authentication fails, never partial plaintext.
	if _, err := DecryptBundle(data, "wrong-passphrase"); err == nil {
		t.Fatal("wrong passphrase must fail")
	} else if !strings.Contains(err.Error(), "口令") {
		t.Fatalf("wrong-passphrase error = %v, want it to say the passphrase is wrong", err)
	}
	// Tampered ciphertext fails too.
	tampered := append([]byte(nil), data...)
	tampered[len(tampered)-5] ^= 0xFF
	if _, err := DecryptBundle(tampered, "correct-horse-battery"); err == nil {
		t.Fatal("tampered bundle must fail")
	}
	// Not a bundle at all.
	if _, err := DecryptBundle([]byte("hello"), "correct-horse-battery"); err == nil {
		t.Fatal("garbage input must fail")
	}
}

func TestBundleShortPassphraseRefused(t *testing.T) {
	if _, err := EncryptBundle(&Bundle{}, "short"); err == nil {
		t.Fatal("a passphrase under 8 characters must be refused")
	}
	// Two encryptions of the same bundle differ (random salt/nonce).
	b := &Bundle{AgentName: "x"}
	a, err := EncryptBundle(b, "long-enough-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	c, err := EncryptBundle(b, "long-enough-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if string(a) == string(c) {
		t.Fatal("two encryptions must differ (random salt/nonce)")
	}
}
