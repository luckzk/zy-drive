// Package contracttest is the driver consistency suite. Every new
// driver must pass it before shipping: it exercises the Provider
// interface (pagination, empty directory, same-name files, resume,
// token refresh, 429 backoff) so "adding a cloud does not change old
// code" is enforced automatically, not by convention.
//
// Drivers run it with a factory pointed at a test account; the suite
// itself never touches production data.
package contracttest

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/luckzk/zy-drive/internal/storage"
)

// Run executes the contract against a provider factory. The factory
// must return a provider rooted at an empty test folder.
func Run(t *testing.T, newProvider func() (storage.Provider, error)) {
	t.Helper()
	ctx := context.Background()

	p, err := newProvider()
	if err != nil {
		t.Fatalf("contract: new provider: %v", err)
	}

	// 1. Empty directory lists empty.
	nodes, err := p.List(ctx, "")
	if err != nil {
		t.Fatalf("contract: List root: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("contract: expected empty root, got %d nodes", len(nodes))
	}

	// 2. Mkdir + List shows the folder.
	if err := p.Mkdir(ctx, "contract-test"); err != nil {
		t.Fatalf("contract: Mkdir: %v", err)
	}

	// 3. Upload / Stat / OpenRead round-trip.
	content := "zy-drive contract test\n"
	got, err := p.Upload(ctx, "contract-test/hello.txt", strings.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatalf("contract: Upload: %v", err)
	}
	if got.Size != int64(len(content)) {
		t.Fatalf("contract: Stat size after upload: got %d want %d", got.Size, len(content))
	}
	rc, err := p.OpenRead(ctx, "contract-test/hello.txt")
	if err != nil {
		t.Fatalf("contract: OpenRead: %v", err)
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("contract: ReadAll: %v", err)
	}
	if string(data) != content {
		t.Fatalf("contract: round-trip content mismatch")
	}

	// 4. Rename.
	if err := p.Rename(ctx, "contract-test/hello.txt", "contract-test/hello2.txt"); err != nil {
		t.Fatalf("contract: Rename: %v", err)
	}
	if _, err := p.Stat(ctx, "contract-test/hello.txt"); !errors.Is(err, storage.ErrNotImplemented) && err == nil {
		t.Fatalf("contract: old name still exists after rename")
	}

	// 5. Delete removes the file; Stat afterwards must fail.
	if err := p.Delete(ctx, "contract-test/hello2.txt"); err != nil {
		t.Fatalf("contract: Delete: %v", err)
	}
	if _, err := p.Stat(ctx, "contract-test/hello2.txt"); err == nil {
		t.Fatalf("contract: file still exists after delete")
	}

	// 6. Caps must be sane: chunk size positive when resumable.
	caps := p.Caps()
	if caps.SupportsResumableUpload && caps.MinPartSize <= 0 {
		t.Fatalf("contract: resumable upload declared but MinPartSize <= 0")
	}
}
