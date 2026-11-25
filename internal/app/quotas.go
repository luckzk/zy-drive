package app

import (
	"context"
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

// friendlyQuotaError strips the storage prefix so the UI shows a
// short human reason ("容量暂不可用" is rendered by the frontend;
// the raw message is kept here for debugging, without secrets —
// ProviderError never contains tokens).
func friendlyQuotaError(err error) string {
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "storage: ")
	return msg
}
