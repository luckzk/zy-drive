// SPDX-License-Identifier: MPL-2.0
package contracttest

import (
	"testing"

	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/storage/providers/box"
	"github.com/luckzk/zy-drive/internal/storage/providers/drime"
	"github.com/luckzk/zy-drive/internal/storage/providers/drive"
	"github.com/luckzk/zy-drive/internal/storage/providers/dropbox"
	"github.com/luckzk/zy-drive/internal/storage/providers/local"
	"github.com/luckzk/zy-drive/internal/storage/providers/onedrive"
	"github.com/luckzk/zy-drive/internal/storage/providers/pcloud"
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

// TestCapsMatchInterfacesAllDrivers constructs every driver that
// can be built without network access (placeholder credentials
// only — New validates presence, it never dials out) and asserts
// Caps.SupportsVersions / SupportsServerSideCopy match the
// Versioner / Copier implementations. MEGA is the exception: its
// New logs in over the network, so it cannot be built here; its
// match is covered by contracttest.Run against the fake client
// in the mega package (TestContract).
func TestCapsMatchInterfacesAllDrivers(t *testing.T) {
	token := storage.Config{Token: "placeholder"}
	drivers := map[string]storage.Provider{}
	var err error
	drivers["box"], err = box.New(token)
	if err != nil {
		t.Fatalf("box.New: %v", err)
	}
	drivers["drime"], err = drime.New(token)
	if err != nil {
		t.Fatalf("drime.New: %v", err)
	}
	drivers["drive"], err = drive.New(token)
	if err != nil {
		t.Fatalf("drive.New: %v", err)
	}
	drivers["dropbox"], err = dropbox.New(token)
	if err != nil {
		t.Fatalf("dropbox.New: %v", err)
	}
	drivers["local"], err = local.New(storage.Config{Extra: map[string]string{"root": t.TempDir()}})
	if err != nil {
		t.Fatalf("local.New: %v", err)
	}
	drivers["onedrive"], err = onedrive.New(token)
	if err != nil {
		t.Fatalf("onedrive.New: %v", err)
	}
	drivers["pcloud"], err = pcloud.New(token)
	if err != nil {
		t.Fatalf("pcloud.New: %v", err)
	}
	drivers["s3"], err = s3.New(storage.Config{
		AccessKey: "test-access", SecretKey: "test-secret",
		Extra: map[string]string{"bucket": "test-bucket"},
	})
	if err != nil {
		t.Fatalf("s3.New: %v", err)
	}
	for name, p := range drivers {
		t.Run(name, func(t *testing.T) { AssertCapsMatchInterfaces(t, p) })
	}
	// Spot-check the positive cases, so a future driver that
	// silently drops its implementation fails loudly here too.
	if !drivers["drive"].Caps().SupportsVersions || !drivers["dropbox"].Caps().SupportsVersions ||
		!drivers["s3"].Caps().SupportsVersions || !drivers["onedrive"].Caps().SupportsVersions {
		t.Fatal("drive/dropbox/s3/onedrive must all advertise SupportsVersions (they implement Versioner)")
	}
	if !drivers["dropbox"].Caps().SupportsServerSideCopy || !drivers["s3"].Caps().SupportsServerSideCopy {
		t.Fatal("dropbox/s3 must advertise SupportsServerSideCopy (they implement Copier)")
	}
	if drivers["drive"].Caps().SupportsServerSideCopy {
		t.Fatal("drive must NOT advertise SupportsServerSideCopy (files.copy has different semantics)")
	}
}
