// SPDX-License-Identifier: MPL-2.0
package contracttest

import (
	"testing"

	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/storage/providers/drive"
	"github.com/luckzk/zy-drive/internal/storage/providers/dropbox"
	"github.com/luckzk/zy-drive/internal/storage/providers/s3"
)

// TestDriverConstruction verifies the V0.1 driver states without any
// network access: Dropbox and Drive are real drivers that fail fast
// without credentials, and S3 declares sane Caps.
func TestDriverConstruction(t *testing.T) {
	if _, err := dropbox.New(storage.Config{}); err == nil {
		t.Fatalf("dropbox.New without token should fail")
	}
	if _, err := drive.New(storage.Config{}); err == nil {
		t.Fatalf("drive.New without token should fail")
	}
	p, err := dropbox.New(storage.Config{Token: "placeholder"})
	if err != nil {
		t.Fatalf("dropbox.New: %v", err)
	}
	if p.Caps().SupportsDelta != true {
		t.Fatalf("dropbox Caps should declare delta support")
	}
	if p.Caps().IDBased {
		t.Fatalf("dropbox is path-based, Caps.IDBased must be false")
	}
	d, err := drive.New(storage.Config{Token: "placeholder"})
	if err != nil {
		t.Fatalf("drive.New: %v", err)
	}
	if !d.Caps().IDBased {
		t.Fatalf("drive is ID-based, Caps.IDBased must be true")
	}
	if !d.Caps().DeleteToTrash {
		t.Fatalf("drive deletes to trash, Caps.DeleteToTrash must be true")
	}

	// S3 provider construction requires a bucket; without one it must
	// fail fast instead of building a broken client.
	if _, err := s3.New(storage.Config{}); err == nil {
		t.Fatalf("s3.New without bucket should fail")
	}
}
