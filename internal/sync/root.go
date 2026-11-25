// SPDX-License-Identifier: MPL-2.0
package sync

import (
	"context"
	"io"
	"strings"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
)

// PrefixedProvider wraps a provider so every ref is resolved under
// a job's sync root. Scan keys are relative to the sync root (see
// Scan), so execution must re-attach the root: a plan item "/a.txt"
// for a job whose source root is "/photos" reads
// "/photos/a.txt" from the provider.
//
// root may be "" or "/" (pass-through), a cloud path ("/photos"),
// or a local pseudo-mount ref ("local:///home/u/photos" — the
// scheme is stripped, the absolute path is the prefix).
func PrefixedProvider(p storage.Provider, rootRef string) storage.Provider {
	root := strings.TrimPrefix(rootRef, "local://")
	root = strings.TrimPrefix(root, "local:")
	root = strings.Trim(root, "/")
	if root == "" {
		return p
	}
	return &prefixedProvider{inner: p, root: root}
}

type prefixedProvider struct {
	inner storage.Provider
	root  string
}

func (p *prefixedProvider) join(ref string) string {
	return "/" + p.root + "/" + strings.Trim(ref, "/")
}

func (p *prefixedProvider) List(ctx context.Context, ref string) ([]domain.Node, error) {
	return p.inner.List(ctx, p.join(ref))
}
func (p *prefixedProvider) Stat(ctx context.Context, ref string) (domain.Node, error) {
	return p.inner.Stat(ctx, p.join(ref))
}
func (p *prefixedProvider) OpenRead(ctx context.Context, ref string) (io.ReadCloser, error) {
	return p.inner.OpenRead(ctx, p.join(ref))
}
func (p *prefixedProvider) Upload(ctx context.Context, ref string, r io.Reader, size int64) (domain.Node, error) {
	return p.inner.Upload(ctx, p.join(ref), r, size)
}
func (p *prefixedProvider) Mkdir(ctx context.Context, ref string) error {
	return p.inner.Mkdir(ctx, p.join(ref))
}
func (p *prefixedProvider) Rename(ctx context.Context, src, dst string) error {
	return p.inner.Rename(ctx, p.join(src), p.join(dst))
}
func (p *prefixedProvider) Delete(ctx context.Context, ref string) error {
	return p.inner.Delete(ctx, p.join(ref))
}
func (p *prefixedProvider) Caps() domain.Caps { return p.inner.Caps() }

var _ storage.Provider = (*prefixedProvider)(nil)
