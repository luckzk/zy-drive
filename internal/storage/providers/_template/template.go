// Package template is the scaffold for a new cloud driver.
// To add a cloud, copy this directory, rename it, and work through the
// seven steps in the README (docs: architecture §6.4):
//
//  1. Implement the core Provider interface + applicable optional
//     interfaces in this directory only.
//  2. Declare the capability table explicitly (Caps): location mode,
//     delete semantics, chunking, path rules, rate-limit parameters.
//  3. Use the shared layers: HTTP retry/backoff, OAuth refresh,
//     chunked-upload sessions. Never write a second copy inside a driver.
//  4. Write the account settings page: vendor-specific fields only in
//     that cloud's own settings component.
//  5. Add one line to the registry; the mount wizard picks it up.
//  6. Pass internal/storage/contracttest: pagination, empty directory,
//     same-name files, resume, token refresh, 429 backoff.
//  7. Add the delta strategy: cursor when available, otherwise declare
//     the full-refresh interval.
//
// The type below compiles as-is so the template can be registered and
// exercised before any real API call is written.
package template

import (
	"context"
	"io"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
)

// Provider is the new-driver scaffold. All operations return
// ErrNotImplemented until the driver is filled in.
type Provider struct{}

// New builds the scaffold provider.
func New(cfg storage.Config) (storage.Provider, error) { return &Provider{}, nil }

// Caps must be filled in for the real cloud before the driver ships:
// every field left at the zero value hides a feature in the UI.
func (p *Provider) Caps() domain.Caps { return domain.Caps{} }

func (p *Provider) List(ctx context.Context, ref string) ([]domain.Node, error) {
	return nil, storage.ErrNotImplemented
}

func (p *Provider) Stat(ctx context.Context, ref string) (domain.Node, error) {
	return domain.Node{}, storage.ErrNotImplemented
}

func (p *Provider) OpenRead(ctx context.Context, ref string) (io.ReadCloser, error) {
	return nil, storage.ErrNotImplemented
}

func (p *Provider) Upload(ctx context.Context, ref string, r io.Reader, size int64) (domain.Node, error) {
	return domain.Node{}, storage.ErrNotImplemented
}

func (p *Provider) Mkdir(ctx context.Context, ref string) error {
	return storage.ErrNotImplemented
}

func (p *Provider) Rename(ctx context.Context, src, dst string) error {
	return storage.ErrNotImplemented
}

func (p *Provider) Delete(ctx context.Context, ref string) error {
	return storage.ErrNotImplemented
}

var _ storage.Provider = (*Provider)(nil)
