package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
)

// Browse returns the cached listing of a directory immediately, then
// refreshes it in the background and pushes the update through the
// event bus. The user perceives "fast" because the cache answers first,
// not because the network is faster.
//
// The returned slice is the cache snapshot; callers must not mutate it.
func (a *App) Browse(ctx context.Context, mountID, ref string) ([]domain.Node, error) {
	p, err := a.ProviderFor(mountID)
	if err != nil {
		return nil, err
	}
	cached, _ := a.DB.ListCached(mountID, ref)
	// Background refresh: fresh listing replaces the cache and an event
	// notifies the UI. Errors are reported as events, not as failures
	// of this call — the user already sees the cached listing.
	go func() {
		nodes, err := p.List(context.Background(), ref)
		if err != nil {
			a.Events().Publish(Event{Type: EventBrowseError, MountID: mountID, Message: err.Error()})
			return
		}
		if err := a.DB.UpsertNodes(mountID, ref, nodes); err != nil {
			a.Events().Publish(Event{Type: EventBrowseError, MountID: mountID, Message: err.Error()})
			return
		}
		a.Events().Publish(Event{Type: EventBrowseRefreshed, MountID: mountID})
	}()
	if cached == nil {
		// Cold cache: wait for the live listing once so the first open
		// is not an empty folder.
		nodes, err := p.List(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("app: browse %s: %w", mountID, err)
		}
		_ = a.DB.UpsertNodes(mountID, ref, nodes)
		return nodes, nil
	}
	return cached, nil
}

// Mkdir creates one folder on a mount (upload target picker). S3
// writes a zero-byte placeholder object; other drivers create a real
// folder. An empty ref is rejected: the mount root always exists.
func (a *App) Mkdir(ctx context.Context, mountID, ref string) error {
	p, err := a.ProviderFor(mountID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(ref) == "" {
		return fmt.Errorf("app: 请先选一个文件夹")
	}
	if err := p.Mkdir(ctx, ref); err != nil {
		return fmt.Errorf("app: 新建文件夹失败：%w", err)
	}
	return nil
}

// Search prefers server-side search when the provider implements
// storage.Searcher; otherwise it searches the local cache index and
// the results are labelled "本地结果" by the UI.
func (a *App) Search(ctx context.Context, mountID, query string) ([]domain.Node, bool, error) {
	p, err := a.ProviderFor(mountID)
	if err != nil {
		return nil, false, err
	}
	if s, ok := p.(storage.Searcher); ok {
		nodes, err := s.Search(ctx, query)
		return nodes, false, err
	}
	nodes, err := a.DB.SearchLocal(mountID, query, 50)
	return nodes, true, err
}

// Stat returns metadata for one node.
func (a *App) Stat(ctx context.Context, mountID, ref string) (domain.Node, error) {
	p, err := a.ProviderFor(mountID)
	if err != nil {
		return domain.Node{}, err
	}
	return p.Stat(ctx, ref)
}
