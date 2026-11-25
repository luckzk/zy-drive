// SPDX-License-Identifier: MPL-2.0
package s3

import (
	"testing"

	"github.com/luckzk/zy-drive/internal/storage"
)

// TestS3HasNoQuota pins the honest absence: S3 has no quota API,
// so the driver must NOT implement storage.QuotaSource — the App
// layer then reports the mount as unlimited ("不限量") instead of
// the driver inventing a number.
func TestS3HasNoQuota(t *testing.T) {
	p, err := New(storage.Config{Extra: map[string]string{"bucket": "test-bucket"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := any(p).(storage.QuotaSource); ok {
		t.Fatal("s3 provider must NOT implement storage.QuotaSource (no quota API exists)")
	}
}
