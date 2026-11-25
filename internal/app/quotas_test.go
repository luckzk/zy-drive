package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/storage/providers/local"
	"github.com/luckzk/zy-drive/internal/storage/providers/s3"
)

// TestMountQuotasIsolatesFailures: one batch call covers a local
// mount (disk space), an S3 mount (no quota API -> unlimited) and
// a broken mount (quota fetch fails). A failure on one mount must
// not block the others, and the call itself must not error.
func TestMountQuotasIsolatesFailures(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()

	dir := t.TempDir()
	localProv, err := local.New(storage.Config{Extra: map[string]string{"root": dir}})
	if err != nil {
		t.Fatalf("local.New: %v", err)
	}
	s3Prov, err := s3.New(storage.Config{Extra: map[string]string{"bucket": "b"}})
	if err != nil {
		t.Fatalf("s3.New: %v", err)
	}
	gone := filepath.Join(t.TempDir(), "gone")
	if err := os.MkdirAll(gone, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	goneProv, err := local.New(storage.Config{Extra: map[string]string{"root": gone}})
	if err != nil {
		t.Fatalf("local.New (gone): %v", err)
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatalf("remove: %v", err)
	}

	now := time.Now()
	mk := func(id string, kind domain.ProviderKind, at time.Time) *domain.Mount {
		return &domain.Mount{ID: id, DisplayName: id, Provider: kind, Status: domain.MountOK, CreatedAt: at, UpdatedAt: at}
	}
	a.mounts["m-local"] = MountHandle{Mount: mk("m-local", domain.ProviderLocal, now), Provider: localProv}
	a.mounts["m-s3"] = MountHandle{Mount: mk("m-s3", domain.ProviderS3, now.Add(time.Second)), Provider: s3Prov}
	a.mounts["m-gone"] = MountHandle{Mount: mk("m-gone", domain.ProviderLocal, now.Add(2*time.Second)), Provider: goneProv}

	quotas := a.MountQuotas(ctx)
	if len(quotas) != 3 {
		t.Fatalf("MountQuotas: got %d entries, want 3", len(quotas))
	}
	byID := map[string]*domain.MountQuota{}
	for _, q := range quotas {
		byID[q.MountID] = q
	}
	lq := byID["m-local"]
	if lq == nil || !lq.IsLocalDisk || lq.Total <= 0 || lq.Used < 0 || lq.Used > lq.Total || lq.Error != "" {
		t.Fatalf("local quota: %+v", lq)
	}
	sq := byID["m-s3"]
	if sq == nil || !sq.Unlimited || sq.Error != "" {
		t.Fatalf("s3 quota must be unlimited without error: %+v", sq)
	}
	gq := byID["m-gone"]
	if gq == nil || gq.Error == "" {
		t.Fatalf("broken mount quota must carry an error: %+v", gq)
	}
}
