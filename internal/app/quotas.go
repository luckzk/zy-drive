// SPDX-License-Identifier: MPL-2.0
package app

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
)

// quotas.go — batch quota fetch for the sidebar / browse header.
//
// One Bind call returns every mount's quota so the frontend opens
// the app with a single round-trip and then refreshes on a 5-minute
// timer (or a manual refresh), never once per file listing: Drime
// especially must not be polled per List (its 429s have meant a
// ~15h penalty window in the past).
//
// Per-mount isolation: each mount is probed with its own 15s
// timeout, and a failure fills only that mount's Error field —
// the rest still report, and the whole call never fails because
// of one mount. A mount whose provider has no QuotaSource (S3/R2:
// no quota API exists) reports Unlimited, not an error.

const quotaProbeTimeout = 15 * time.Second

// MountQuotas returns one MountQuota per mount, in ListMounts order.
func (a *App) MountQuotas(ctx context.Context) []*domain.MountQuota {
	mounts := a.ListMounts()
	out := make([]*domain.MountQuota, 0, len(mounts))
	for _, m := range mounts {
		out = append(out, a.mountQuota(ctx, m))
	}
	return out
}

func (a *App) mountQuota(ctx context.Context, m *domain.Mount) *domain.MountQuota {
	q := &domain.MountQuota{MountID: m.ID}
	p, err := a.ProviderFor(m.ID)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	qs, ok := p.(storage.QuotaSource)
	if !ok {
		// No quota API for this provider (S3/R2): unlimited, and
		// this is not a failure — the UI shows "不限量".
		q.Unlimited = true
		return q
	}
	probeCtx, cancel := context.WithTimeout(ctx, quotaProbeTimeout)
	defer cancel()
	used, total, err := qs.Quota(probeCtx)
	if err != nil {
		// Log the real reason: the UI only shows "容量暂不可用",
		// so without this line the cause (e.g. a missing scope)
		// is invisible anywhere. ProviderError never contains
		// tokens, so this is safe to log.
		log.Printf("quota: mount=%s provider=%s: %v", m.ID, m.Provider, err)
		q.Error = friendlyQuotaError(err)
		return q
	}
	if m.Provider == domain.ProviderLocal {
		q.IsLocalDisk = true
	}
	if total <= 0 {
		// Provider reports usage but no total (unlimited plan):
		// show used only, never invent a total.
		total = -1
	}
	q.Used, q.Total = used, total
	return q
}

// friendlyQuotaError turns a quota failure into one plain-language
// reason the UI shows next to "容量暂不可用" (sidebar tooltip,
// browse header / Inspector inline, Settings drives row). The raw
// message is only kept in the backend log (see mountQuota) —
// ProviderError never contains tokens.
func friendlyQuotaError(err error) string {
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "storage: ")
	switch {
	case strings.Contains(msg, "missing_scope") && strings.Contains(msg, "dropbox"):
		// get_space_usage needs account_info.read: the token was
		// authorized before ZyDrive requested it (0.8.4 and
		// earlier did not). Files still work; only capacity
		// fails. Enabling the scope in the App Console alone is
		// not enough — an old token never gains a new scope, so
		// the user must re-authorize this mount.
		return "容量接口需要 account_info.read 权限：请先在 Dropbox 应用控制台 → Permissions 里勾选 account_info.read 并提交，然后在设置页 → 网盘 → 重新授权这个网盘"
	case strings.Contains(msg, "401"), strings.Contains(msg, "invalid_access_token"), strings.Contains(msg, "expired_access_token"):
		return "授权已过期或被撤销：请重新授权这个网盘"
	case strings.Contains(msg, "connection"), strings.Contains(msg, "timeout"), strings.Contains(msg, "network"), strings.Contains(msg, "no such host"):
		return "网络问题，容量暂时查不到：请检查网络或代理设置（设置 → 网络）"
	default:
		return msg
	}
}
