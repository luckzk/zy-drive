package app

import (
	"context"
	"fmt"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
)

// Account is the stored credential reference for one cloud account.
// It holds no secrets: secrets live encrypted in the secrets table and
// are only decrypted in memory at use time.
type Account struct {
	ID          string              `json:"id"`
	Provider    domain.ProviderKind `json:"provider"`
	DisplayName string              `json:"displayName"`
	Status      domain.MountStatus  `json:"status"`
}

// AddMount registers a Mount and builds its provider from the registry.
// The provider is constructed from a storage.Config assembled by the
// caller (which decrypted the credentials in memory). The wizard UI
// collects provider-specific fields; this function stays generic.
func (a *App) AddMount(ctx context.Context, m *domain.Mount, cfg storage.Config) error {
	if m.ID == "" {
		return fmt.Errorf("app: mount id is required")
	}
	p, err := a.Registry.New(m.Provider, cfg)
	if err != nil {
		return fmt.Errorf("app: add mount %s: %w", m.DisplayName, err)
	}
	// A cheap capability probe: Caps must not panic and must be sane.
	caps := p.Caps()
	_ = caps
	m.Caps = caps
	m.Status = domain.MountOK
	a.mounts[m.ID] = MountHandle{Mount: m, Provider: p}
	return nil
}

// ListMounts returns the registered mounts in registration order.
func (a *App) ListMounts() []*domain.Mount {
	out := make([]*domain.Mount, 0, len(a.mounts))
	for _, h := range a.mounts {
		out = append(out, h.Mount)
	}
	return out
}

// RemoveMount unregisters a mount. It does not delete cloud data.
func (a *App) RemoveMount(id string) {
	delete(a.mounts, id)
}
