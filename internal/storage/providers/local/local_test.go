package local

import (
	"context"
	"strings"
	"testing"

	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/storage/contracttest"
)

func TestLocalContract(t *testing.T) {
	dir := t.TempDir()
	p, err := New(storage.Config{Extra: map[string]string{"root": dir}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	contracttest.Run(t, func() (storage.Provider, error) { return p, nil })
}

func TestLocalSchemePrefix(t *testing.T) {
	dir := t.TempDir()
	p, err := New(storage.Config{Extra: map[string]string{"root": dir}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if _, err := p.Upload(ctx, "local://sub/f.txt", strings.NewReader("x"), 1); err != nil {
		t.Fatalf("Upload local://: %v", err)
	}
	nodes, err := p.List(ctx, "sub")
	if err != nil || len(nodes) != 1 {
		t.Fatalf("List: %v, %d nodes", err, len(nodes))
	}
	if _, err := p.Stat(ctx, "../escape.txt"); err == nil {
		t.Fatalf("path escaping the root must fail")
	}
}

func TestLocalQuotaIsDiskSpace(t *testing.T) {
	dir := t.TempDir()
	p, err := New(storage.Config{Extra: map[string]string{"root": dir}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	qs, ok := any(p).(storage.QuotaSource)
	if !ok {
		t.Fatal("local provider must implement storage.QuotaSource")
	}
	used, total, err := qs.Quota(t.Context())
	if err != nil {
		t.Fatalf("Quota: %v", err)
	}
	if total <= 0 {
		t.Fatalf("Quota: total must be the disk size, got %d", total)
	}
	if used < 0 || used > total {
		t.Fatalf("Quota: used=%d outside [0, total=%d]", used, total)
	}
}
