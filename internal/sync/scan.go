// SPDX-License-Identifier: MPL-2.0
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
// Scan walks a provider tree from rootRef and returns a snapshot
// keyed by display path. Keys are RELATIVE to the scan root, so a
// job syncing /photos -> /backup compares "a.txt" with "a.txt",
// not "/photos/a.txt" with "/backup/a.txt".
//
// Drivers return root-relative paths from List ("/photos/a.txt"
// for a child of /photos), so the scan root is stripped here. The
// old code did the opposite — it PREPENDED the parent path on
// recursion, doubling nested paths ("/sub/sub/b.txt") — because it
// assumed List returned paths relative to the listed folder, which
// no driver does. The execution side re-attaches the root via
// PrefixedProvider in runJob.
func Scan(ctx context.Context, p storage.Provider, rootRef string) (*Snapshot, error) {
	snap := &Snapshot{Nodes: map[string]domain.Node{}}
	root := strings.TrimPrefix(rootRef, "local://")
	root = strings.TrimPrefix(root, "local:")
	root = strings.Trim(root, "/")
	if err := scanInto(ctx, p, rootRef, root, snap); err != nil {
		return nil, err
	}
	return snap, nil
}

func scanInto(ctx context.Context, p storage.Provider, ref, root string, snap *Snapshot) error {
	children, err := p.List(ctx, ref)
	if err != nil {
		return err
	}
	for _, n := range children {
		// Display path relative to the scan root.
		disp := n.Path
		if root != "" {
			rel := strings.Trim(strings.TrimPrefix(strings.Trim(n.Path, "/"), root+"/"), "/")
			disp = "/" + rel
		}
		nn := n
		nn.Path = disp
		snap.Nodes[disp] = nn
		if n.IsDir {
			if err := scanInto(ctx, p, n.Ref, root, snap); err != nil {
				return err
			}
		}
	}
	return nil
}

// Matches reports whether a path passes the exclude patterns.
// Patterns are matched against the base name and the full path.
// Two pattern shapes get directory semantics, because that is what
// users mean when they write them:
//
//   - A trailing slash (".git/", "node_modules/") names a
//     directory: the directory itself and everything under it,
//     at any depth, is excluded. (Before v0.8.3 the trailing
//     slash never matched anything: path.Match needs a full-string
//     match, and neither the base name nor the full path ends in
//     "/".)
//   - A bare name with no glob characters and no slash
//     ("node_modules", ".DS_Store") matches when ANY path segment
//     equals it, so a directory named that way excludes its whole
//     subtree too — not just a file of that name.
//
// Glob patterns ("*.tmp", "._*", "~$*") keep the old behavior:
// path.Match against the base name and the full path.
func Matches(pathStr string, patterns []string) bool {
	base := path.Base(pathStr)
	for _, pat := range patterns {
		if pat == "" {
			continue
		}
		// Directory pattern: trailing "/" names a directory and
		// everything under it, at any depth.
		if strings.HasSuffix(pat, "/") {
			if dirNameMatches(pathStr, strings.TrimSuffix(pat, "/")) {
				return true
			}
			continue
		}
		// Bare name (no glob meta, no "/"): matches when any path
		// segment equals it — a directory of that name excludes
		// its whole subtree.
		if !strings.ContainsAny(pat, "*?[\\") && !strings.Contains(pat, "/") {
			if dirNameMatches(pathStr, pat) {
				return true
			}
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

// dirNameMatches reports whether any segment of pathStr equals name
// (so "node_modules" matches "node_modules", "a/node_modules/b.js"
// and "a/node_modules" alike). The base name alone is not enough:
// a file deep under an excluded directory must match too.
func dirNameMatches(pathStr, name string) bool {
	if name == "" {
		return false
	}
	for _, seg := range strings.Split(pathStr, "/") {
		if seg == name {
			return true
		}
	}
	return false
}
