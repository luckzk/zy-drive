// SPDX-License-Identifier: MPL-2.0
// Package local implements storage.Provider for a folder on the local
// filesystem. It is the "local://" endpoint of a sync job: one side of
// a sync can be a folder on this machine (e.g. ~/图片) instead of a
// cloud mount. It only ever runs on the desktop itself — a job with a
// local side must have RunOn=local, enforced by app.ValidateJob —
// because a server Agent has no access to this machine's disk.
//
// Refs are paths relative to the configured root (cfg.Extra["root"],
// default "."). The sync layer passes refs as "local://<abs path>";
// that scheme prefix is stripped here. Files are read/written
// directly; there is no staging, no trash and no versioning.
package local

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
)

// Provider is a local-filesystem storage.Provider.
type Provider struct {
	root string
}

// New builds a local provider rooted at cfg.Extra["root"] (default ".").
func New(cfg storage.Config) (storage.Provider, error) {
	root := cfg.Extra["root"]
	if root == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("local: root %q: %w", root, err)
	}
	return &Provider{root: abs}, nil
}

// Caps declares local-filesystem capabilities.
func (p *Provider) Caps() domain.Caps {
	return domain.Caps{
		CanMkdir: true, CanRename: true, CanDelete: true, DeleteToTrash: false,
		SupportsServerSideCopy:  false,
		SupportsVersions:        false,
		SupportsShare:           false,
		SupportsSearch:          false,
		SupportsDelta:           false,
		SupportsThumbnail:       false,
		SupportsResumableUpload: false,
		SupportsEncryption:      false,
		SupportsStorageClass:    false,
		SupportsPresignedURL:    false,
		IDBased:                 false,
		MaxFileSize:             0, // filesystem limit
		MinPartSize:             0,
	}
}

// resolve turns a ref into an absolute path under the root. Refs may
// be relative ("photos/a.jpg"), absolute ("/home/u/photos/a.jpg") or
// carry the "local://" scheme.
//
// The sync engine passes display paths ("/photos/a.jpg") to
// providers, so an absolute ref is only taken literally when it
// lies under the root. An absolute ref outside the root is treated
// as relative to the root instead: for a real mount rooted at
// ~/图片, "/photos/a.jpg" means ~/图片/photos/a.jpg, not the OS
// root. (For the "local" pseudo-mount the root IS "/", so every
// absolute path is under the root and nothing changes there.)
// Paths escaping the root are rejected.
func (p *Provider) resolve(ref string) (string, error) {
	ref = strings.TrimPrefix(ref, "local://")
	ref = strings.TrimPrefix(ref, "local:")
	if filepath.IsAbs(ref) {
		clean := filepath.Clean(ref)
		if clean == p.root || strings.HasPrefix(clean, p.root+string(os.PathSeparator)) {
			return clean, nil
		}
		ref = strings.TrimPrefix(clean, string(os.PathSeparator))
	}
	full := filepath.Join(p.root, filepath.FromSlash(strings.Trim(ref, "/")))
	rel, err := filepath.Rel(p.root, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("local: ref %q escapes root", ref)
	}
	return full, nil
}

func (p *Provider) wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	return &storage.ProviderError{Op: op, Provider: "local", Err: err}
}

func (p *Provider) node(full string) (domain.Node, error) {
	info, err := os.Stat(full)
	if err != nil {
		return domain.Node{}, err
	}
	rel, _ := filepath.Rel(p.root, full)
	ref := filepath.ToSlash(rel)
	if ref == "." {
		ref = ""
	}
	n := domain.Node{
		Ref: ref, Path: "/" + ref, Name: info.Name(),
		IsDir: info.IsDir(), Size: -1, ModTime: info.ModTime(),
	}
	if !info.IsDir() {
		n.Size = info.Size()
		n.Mime = mimeOf(info.Name())
	}
	return n, nil
}

// List returns direct children of ref.
func (p *Provider) List(ctx context.Context, ref string) ([]domain.Node, error) {
	full, err := p.resolve(ref)
	if err != nil {
		return nil, p.wrap("List", err)
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return nil, p.wrap("List", err)
	}
	out := make([]domain.Node, 0, len(entries))
	for _, e := range entries {
		n, err := p.node(filepath.Join(full, e.Name()))
		if err != nil {
			return nil, p.wrap("List", err)
		}
		out = append(out, n)
	}
	return out, nil
}

// Stat returns metadata for one node.
func (p *Provider) Stat(ctx context.Context, ref string) (domain.Node, error) {
	full, err := p.resolve(ref)
	if err != nil {
		return domain.Node{}, p.wrap("Stat", err)
	}
	n, err := p.node(full)
	if err != nil {
		return domain.Node{}, p.wrap("Stat", err)
	}
	return n, nil
}

// OpenRead streams a file for download.
func (p *Provider) OpenRead(ctx context.Context, ref string) (io.ReadCloser, error) {
	full, err := p.resolve(ref)
	if err != nil {
		return nil, p.wrap("OpenRead", err)
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, p.wrap("OpenRead", err)
	}
	return f, nil
}

// Upload writes a file, creating parent directories as needed.
func (p *Provider) Upload(ctx context.Context, ref string, r io.Reader, size int64) (domain.Node, error) {
	full, err := p.resolve(ref)
	if err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	f, err := os.Create(full)
	if err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return domain.Node{}, p.wrap("Upload", err)
	}
	if err := f.Close(); err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	return p.Stat(ctx, ref)
}

// Mkdir creates a folder (parents included).
func (p *Provider) Mkdir(ctx context.Context, ref string) error {
	full, err := p.resolve(ref)
	if err != nil {
		return p.wrap("Mkdir", err)
	}
	return p.wrapErr("Mkdir", os.MkdirAll(full, 0o755))
}

// Rename moves/renames within the same root.
func (p *Provider) Rename(ctx context.Context, src, dst string) error {
	s, err := p.resolve(src)
	if err != nil {
		return p.wrap("Rename", err)
	}
	d, err := p.resolve(dst)
	if err != nil {
		return p.wrap("Rename", err)
	}
	if err := os.MkdirAll(filepath.Dir(d), 0o755); err != nil {
		return p.wrap("Rename", err)
	}
	return p.wrapErr("Rename", os.Rename(s, d))
}

// Delete removes one file or folder (recursive for folders).
func (p *Provider) Delete(ctx context.Context, ref string) error {
	full, err := p.resolve(ref)
	if err != nil {
		return p.wrap("Delete", err)
	}
	return p.wrapErr("Delete", os.RemoveAll(full))
}

func (p *Provider) wrapErr(op string, err error) error {
	if err == nil {
		return nil
	}
	return p.wrap(op, err)
}

// Quota implements storage.QuotaSource from the disk the root
// folder sits on (standard-library syscall.Statfs, no new
// dependency). This is local disk space, not a cloud quota: the
// App layer marks it IsLocalDisk so the UI says "本机磁盘".
// used = total blocks minus free blocks, in bytes.
func (p *Provider) Quota(ctx context.Context) (used, total int64, err error) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(p.root, &fs); err != nil {
		return 0, 0, p.wrap("Quota", err)
	}
	total = int64(fs.Blocks) * int64(fs.Bsize)
	free := int64(fs.Bfree) * int64(fs.Bsize)
	if total <= 0 {
		return 0, -1, nil
	}
	if free > total {
		free = total
	}
	return total - free, total, nil
}

func mimeOf(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".mp4":
		return "video/mp4"
	case ".mp3":
		return "audio/mpeg"
	case ".pdf":
		return "application/pdf"
	case ".txt", ".md":
		return "text/plain"
	case ".zip":
		return "application/zip"
	default:
		return "application/octet-stream"
	}
}

var (
	_ storage.Provider    = (*Provider)(nil)
	_ storage.QuotaSource = (*Provider)(nil)
)
