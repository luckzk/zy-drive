package app

import (
	"context"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/sync"
)

// api.go documents the Bind surface: the methods the frontend calls
// through mygo.Bind (desktop) — one method per user action, all routed
// through the app layer. The frontend never calls drivers or cloud APIs
// directly. Method names are stable: renaming one breaks the generated
// TypeScript client.
//
// Sync-task UI (2026-11-24 decision): the sync wizard uses an Air
// Explorer-style dual-pane layout — one file manager on the left
// (source), one on the right (target) — because picking source and
// target side by side is easier to understand than two dropdowns.

// ListMountsForUI returns mounts for the sidebar.
func (a *App) ListMountsForUI() []*domain.Mount { return a.ListMounts() }

// BrowseForUI lists a directory (cache-first, background refresh).
func (a *App) BrowseForUI(ctx context.Context, mountID, ref string) ([]domain.Node, error) {
	return a.Browse(ctx, mountID, ref)
}

// SearchForUI searches a mount. localOnly reports "本地结果".
func (a *App) SearchForUI(ctx context.Context, mountID, query string) ([]domain.Node, bool, error) {
	return a.Search(ctx, mountID, query)
}

// PreviewSyncForUI builds a sync plan for confirmation.
func (a *App) PreviewSyncForUI(ctx context.Context, job *domain.SyncJob) (*sync.Plan, error) {
	return a.PreviewJob(ctx, job)
}

// RunSyncForUI executes a confirmed plan.
func (a *App) RunSyncForUI(ctx context.Context, job *domain.SyncJob, plan *sync.Plan) (*domain.Run, error) {
	return a.RunJob(ctx, job, plan)
}

// TransfersForUI lists transfers for the bottom queue panel.
func (a *App) TransfersForUI() []*domain.Transfer { return a.Transfers() }
