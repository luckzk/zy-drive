package app

import (
	"fmt"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/storage/providers/local"
)

// Mounts returns the mount management helpers. (The core Add/List/
// Remove operations live in accounts.go; this file holds mount-scoped
// provider access shared by browse/transfers/syncjobs.)

// ProviderFor returns the live provider for a mount, or an error when
// the mount is unknown. All operations go through here so the app layer
// never keeps provider references in more than one place.
//
// The "local" pseudo-mount (domain.LocalMountID) resolves to the
// on-demand local-filesystem provider rooted at "/": a sync side of
// local://<absolute path> is served by this machine's disk. Provider
// instances are per Mount (never per type): two mounts of the same
// kind each built their own provider in AddMount, with their own
// token, cache and cursors.
func (a *App) ProviderFor(mountID string) (storage.Provider, error) {
	if mountID == domain.LocalMountID {
		if a.localProv == nil {
			p, err := local.New(storage.Config{Extra: map[string]string{"root": "/"}})
			if err != nil {
				return nil, fmt.Errorf("app: local provider: %w", err)
			}
			a.localProv = p
		}
		return a.localProv, nil
	}
	h, ok := a.mounts[mountID]
	if !ok {
		return nil, fmt.Errorf("app: unknown mount %q", mountID)
	}
	if h.Provider == nil {
		return nil, fmt.Errorf("app: mount %q 未能加载（凭证或配置有问题），请删除后重新添加", mountID)
	}
	return h.Provider, nil
}

// MountByID returns the mount metadata for a mount ID.
func (a *App) MountByID(mountID string) (*domain.Mount, error) {
	if mountID == domain.LocalMountID {
		return &domain.Mount{
			ID: mountID, DisplayName: "本地文件夹",
			Provider: domain.ProviderLocal, RootRef: "/",
			Status: domain.MountOK,
		}, nil
	}
	h, ok := a.mounts[mountID]
	if !ok {
		return nil, fmt.Errorf("app: unknown mount %q", mountID)
	}
	return h.Mount, nil
}
