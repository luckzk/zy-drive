// Package sync implements the sync engine: scan → compare → plan →
// confirm → execute. It only understands the unified domain.Node model
// and the storage.Provider interface — vendor names never appear here.
// Deletion is never executed without a confirmed plan.
package sync

import (
	"context"
	"path"
	"strings"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
)

// Snapshot is a full listing of one side at one moment, keyed by display
// path. For bidirectional sync the snapshot is also the baseline: the
// next run compares against it to know who changed or deleted what.
type Snapshot struct {
	Nodes map[string]domain.Node `json:"nodes"`
}

// Scan recursively lists a provider tree into a Snapshot. Depth-first;
// directories are recorded as well as files (empty folders matter for
// Mirror). Errors abort the scan — a partial snapshot must never be
// treated as complete.
func Scan(ctx context.Context, p storage.Provider, rootRef string) (*Snapshot, error) {
	snap := &Snapshot{Nodes: map[string]domain.Node{}}
	if err := scanInto(ctx, p, rootRef, "", snap); err != nil {
		return nil, err
	}
	return snap, nil
}

func scanInto(ctx context.Context, p storage.Provider, ref, prefix string, snap *Snapshot) error {
	children, err := p.List(ctx, ref)
	if err != nil {
		return err
	}
	for _, n := range children {
		// Normalise the display path under the scan root.
		disp := n.Path
		if prefix != "" {
			disp = prefix + "/" + strings.TrimPrefix(n.Path, "/")
		}
		nn := n
		nn.Path = disp
		snap.Nodes[disp] = nn
		if n.IsDir {
			if err := scanInto(ctx, p, n.Ref, disp, snap); err != nil {
				return err
			}
		}
	}
	return nil
}

// Matches reports whether a path passes the exclude patterns.
// Patterns are matched against the base name and the full path.
func Matches(pathStr string, patterns []string) bool {
	base := path.Base(pathStr)
	for _, pat := range patterns {
		if pat == "" {
			continue
		}
		if ok, _ := path.Match(pat, base); ok {
			return true
		}
		if ok, _ := path.Match(pat, pathStr); ok {
			return true
		}
	}
	return false
}
