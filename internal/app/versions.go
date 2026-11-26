// SPDX-License-Identifier: MPL-2.0
package app

import (
	"context"
	"fmt"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
)

// versions.go — file history (v0.12.0). The drivers that implement
// storage.Versioner (OneDrive, Drive, Dropbox, S3/R2) list their
// versions as domain.Nodes: each names the same file and carries
// the version id in Extra["versionId"] (the OneDrive pattern).
// The UI only offers this when Caps.SupportsVersions is true, and
// the backend re-checks the interface here — a mount whose driver
// lost the implementation reports "not supported" instead of
// failing halfway through a restore.

// ListVersions returns the version history of one file, oldest
// first. Folders have no versions: the UI never offers this for
// them, and the driver would fail anyway.
func (a *App) ListVersions(ctx context.Context, mountID, ref string) ([]domain.Node, error) {
	p, err := a.ProviderFor(mountID)
	if err != nil {
		return nil, err
	}
	v, ok := p.(storage.Versioner)
	if !ok {
		return nil, fmt.Errorf("app: 这个网盘不支持历史版本")
	}
	vs, err := v.Versions(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("app: 读历史版本失败：%w", err)
	}
	if vs == nil {
		vs = []domain.Node{}
	}
	return vs, nil
}

// RestoreVersion restores one version (by the Extra["versionId"]
// ListVersions returned) as the new head version. The restored
// version stays in the history on every driver, so a restore is
// itself undoable by restoring the previous head.
func (a *App) RestoreVersion(ctx context.Context, mountID, ref, versionID string) error {
	if versionID == "" {
		return fmt.Errorf("app: 没有指定要恢复的版本")
	}
	p, err := a.ProviderFor(mountID)
	if err != nil {
		return err
	}
	v, ok := p.(storage.Versioner)
	if !ok {
		return fmt.Errorf("app: 这个网盘不支持历史版本")
	}
	if err := v.Restore(ctx, ref, versionID); err != nil {
		return fmt.Errorf("app: 恢复历史版本失败：%w", err)
	}
	return nil
}
