// SPDX-License-Identifier: MPL-2.0
// Package mega implements storage.Provider for MEGA using the
// github.com/t3rm1n4l/go-mega client library behind a small
// internal interface (megaClient). MEGA is not a standard REST
// API: it is a private encrypted protocol (api.mega.co.nz) where
// the client derives keys from the password, decrypts the master
// key, and encrypts/decrypts every file chunk with AES-CTR
// client-side — hand-rolling that protocol would be both a
// security risk and a maintenance burden, so the driver wraps the
// maintained library the same way the S3 driver wraps
// aws-sdk-go-v2.
//
// The library itself is only referenced from adapter_mega.go.
// This file is untagged and depends only on the megaClient
// interface, so the whole driver logic — List, Upload, Download,
// resolve, search — is compiled and tested against a fake client
// (mega_test.go). The real adapter compiles against
// github.com/t3rm1n4l/go-mega, pinned to the same commit Alist
// and rclone use (c6acd6a, 2026-07-17).
//
// Auth: email + password (optionally a 2FA code), or a saved
// session. After a successful password login the driver exports
// the session (session ID + master key) via storage.SessionExporter;
// the app stores it encrypted at rest (never the password) and
// the driver logs in with LoginWithKeys on restart. The session
// export is what makes MEGA usable without re-entering the
// password on every start.
//
// Uploads are chunked (≤ 1 MiB per chunk, AES-CTR encrypted by
// the library) but the upload session is NOT persisted: a failed
// upload restarts from zero (SupportsResumableUpload is false).
// Downloads stream chunk by chunk through a seekable reader, so
// preview Range requests can seek without re-downloading from
// the start.
//
// There is no server-side search in the library: Search walks
// the tree from the root and matches names client-side. That is
// O(tree) and can be slow on very large accounts; it is honest
// and correct, and the limitation is documented here.
package mega

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
)

// Node type constants, mirroring go-mega's.
const (
	nodeFile   = 0
	nodeFolder = 1
	nodeRoot   = 2
	nodeInbox  = 3
	nodeTrash  = 4
)

// megaNode is one MEGA file-system node.
type megaNode interface {
	Type() int // 0 file, 1 folder, 2 root, 3 inbox, 4 trash
	Name() string
	Size() int64
	Time() time.Time
	Hash() string
}

// megaDownload is one in-progress MEGA download.
type megaDownload interface {
	Chunks() int
	ChunkLocation(id int) (position int64, size int, err error)
	DownloadChunk(id int) ([]byte, error)
	Finish() error
}

// megaUpload is one in-progress MEGA upload.
type megaUpload interface {
	Chunks() int
	ChunkLocation(id int) (position int64, size int, err error)
	UploadChunk(id int, chunk []byte) error
	Finish() (megaNode, error)
}

// megaClient is the minimal surface the driver needs from the
// MEGA client library. The method names and signatures mirror
// github.com/t3rm1n4l/go-mega exactly, so the tagged adapter is
// a thin forwarding layer (see adapter_mega.go).
type megaClient interface {
	Login(email, password string) error
	MultiFactorLogin(email, password, code string) error
	LoginWithKeys(sessionID string, masterKey []byte) error
	// Session returns the exported session (ID + master key) for
	// storage at rest, so restarts can use LoginWithKeys.
	Session() (sessionID string, masterKey []byte)
	Quota() (used, total uint64, err error)
	Root() megaNode
	Children(n megaNode) ([]megaNode, error)
	Lookup(root megaNode, names []string) ([]megaNode, error)
	Download(n megaNode) (megaDownload, error)
	Upload(parent megaNode, name string, size int64) (megaUpload, error)
	CreateDir(parent megaNode, name string) (megaNode, error)
	Rename(n megaNode, name string) error
	Move(n megaNode, parent megaNode) error
	Delete(n megaNode, destroy bool) error
	ShareLink(n megaNode) (string, error)
}

// Provider is a MEGA-backed storage.Provider. One instance per
// Mount (never a per-type singleton): each account has its own
// client and its own session.
//
// The driver keeps NO path -> node cache of its own: the library
// maintains the FS tree in memory (its mutations — Upload,
// CreateDir, Rename, Move, Delete — update the tree), so
// PathLookup is already a cheap in-memory walk.
type Provider struct {
	client megaClient
}

// newProvider builds a Provider around an already-logged-in
// client. Tests use it with a fake client; the tagged New
// (adapter_mega.go) logs in first and then calls it.
func newProvider(client megaClient) *Provider {
	return &Provider{client: client}
}

// ExportSession implements storage.SessionExporter: the session
// ID plus the base64 master key, for the app to store encrypted
// at rest. The password is never exported.
func (p *Provider) ExportSession(ctx context.Context) (sessionID, secret string, err error) {
	id, key := p.client.Session()
	if id == "" || len(key) == 0 {
		return "", "", fmt.Errorf("mega: no session to export (not logged in?)")
	}
	return id, base64.StdEncoding.EncodeToString(key), nil
}

// Caps declares MEGA capabilities.
func (p *Provider) Caps() domain.Caps {
	return domain.Caps{
		CanMkdir: true, CanRename: true, CanDelete: true, DeleteToTrash: true,
		SupportsServerSideCopy:  false,
		SupportsVersions:        false,
		SupportsShare:           true,
		SupportsSearch:          true, // client-side tree walk, see the package comment
		SupportsDelta:           false,
		SupportsThumbnail:       false,
		SupportsResumableUpload: false, // chunked, but the session is not persisted
		SupportsEncryption:      true,  // MEGA encrypts everything client-side by design
		SupportsStorageClass:    false,
		SupportsPresignedURL:    false,
		IDBased:                 true,
		MaxFileSize:             0, // no documented limit
		MinPartSize:             0,
		MaxPathLen:              255,
	}
}

// --- path <-> node ---

func splitRef(ref string) (parent, name string) {
	ref = strings.Trim(ref, "/")
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return "", ref
}

// resolve returns the node for a display path. "" and "/" are
// the root. The lookup walks the library's in-memory FS tree
// (PathLookup), so a rename made outside ZyDrive is picked up
// immediately.
func (p *Provider) resolve(ctx context.Context, ref string) (megaNode, error) {
	ref = strings.Trim(ref, "/")
	if ref == "" {
		return p.client.Root(), nil
	}
	parts := strings.Split(ref, "/")
	nodes, err := p.client.Lookup(p.client.Root(), parts)
	if err != nil || len(nodes) != len(parts) {
		return nil, p.wrap("List", fmt.Errorf("mega: no such file or folder: %q", ref))
	}
	return nodes[len(nodes)-1], nil
}

func nodeToDomain(n megaNode, ref string) domain.Node {
	d := domain.Node{
		Ref:   ref,
		Path:  "/" + strings.Trim(ref, "/"),
		Name:  n.Name(),
		IsDir: n.Type() == nodeFolder || n.Type() == nodeRoot,
		Size:  -1,
		Mime:  mimeOf(n.Name()),
		Extra: map[string]string{"megaHash": n.Hash()},
	}
	if !d.IsDir {
		d.Size = n.Size()
	}
	if t := n.Time(); !t.IsZero() {
		d.ModTime = t
	}
	if ref == "" {
		d.Path = "/"
		d.Name = ""
	}
	return d
}

func (p *Provider) wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	return &storage.ProviderError{Op: op, Provider: "mega", Err: err}
}

// --- Provider interface ---

// List returns the children of a folder. "" (or "/") is the root.
func (p *Provider) List(ctx context.Context, ref string) ([]domain.Node, error) {
	n, err := p.resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	if n.Type() != nodeFolder && n.Type() != nodeRoot {
		return nil, p.wrap("List", fmt.Errorf("mega: %q is not a folder", ref))
	}
	children, err := p.client.Children(n)
	if err != nil {
		return nil, p.wrap("List", err)
	}
	base := strings.Trim(ref, "/")
	var out []domain.Node
	for _, c := range children {
		// The root's own children exclude the Inbox/Trash nodes
		// the library keeps in the tree: they are MEGA system
		// folders, not user files.
		if c.Type() == nodeInbox || c.Type() == nodeTrash {
			continue
		}
		child := c.Name()
		if base != "" {
			child = base + "/" + child
		}
		out = append(out, nodeToDomain(c, child))
	}
	return out, nil
}

// Stat returns one entry. The root reports a directory with
// unknown size.
func (p *Provider) Stat(ctx context.Context, ref string) (domain.Node, error) {
	ref = strings.Trim(ref, "/")
	if ref == "" {
		return domain.Node{Ref: "", Path: "/", IsDir: true, Size: -1, Extra: map[string]string{}}, nil
	}
	n, err := p.resolve(ctx, ref)
	if err != nil {
		return domain.Node{}, err
	}
	return nodeToDomain(n, ref), nil
}

// OpenRead streams a file, chunk by chunk, through a seekable
// reader (chunk ≤ 1 MiB; seeking re-fetches only the chunk that
// contains the offset). The caller must close it.
func (p *Provider) OpenRead(ctx context.Context, ref string) (io.ReadCloser, error) {
	n, err := p.resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	if n.Type() != nodeFile {
		return nil, p.wrap("OpenRead", fmt.Errorf("mega: %q is not a file", ref))
	}
	dl, err := p.client.Download(n)
	if err != nil {
		return nil, p.wrap("OpenRead", err)
	}
	r, err := newDownloadReader(ctx, dl, n.Size())
	if err != nil {
		_ = dl.Finish()
		return nil, p.wrap("OpenRead", err)
	}
	return r, nil
}

// Upload writes a file in chunks (≤ 1 MiB each, AES-CTR
// encrypted by the library). The reader is consumed in order;
// a seekable source is not required. A failed upload restarts
// from zero — the session is not persisted (see Caps).
func (p *Provider) Upload(ctx context.Context, ref string, r io.Reader, size int64) (domain.Node, error) {
	if size < 0 {
		return domain.Node{}, p.wrap("Upload", fmt.Errorf("mega: upload size is required"))
	}
	parent, name := splitRef(ref)
	parentNode, err := p.resolve(ctx, parent)
	if err != nil {
		return domain.Node{}, err
	}
	if parentNode.Type() != nodeFolder && parentNode.Type() != nodeRoot {
		return domain.Node{}, p.wrap("Upload", fmt.Errorf("mega: %q is not a folder", parent))
	}
	up, err := p.client.Upload(parentNode, name, size)
	if err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	for i := 0; i < up.Chunks(); i++ {
		if err := ctx.Err(); err != nil {
			return domain.Node{}, p.wrap("Upload", err)
		}
		_, chunkSize, err := up.ChunkLocation(i)
		if err != nil {
			return domain.Node{}, p.wrap("Upload", err)
		}
		chunk := make([]byte, chunkSize)
		if _, err := io.ReadFull(r, chunk); err != nil {
			return domain.Node{}, p.wrap("Upload", err)
		}
		if err := up.UploadChunk(i, chunk); err != nil {
			return domain.Node{}, p.wrap("Upload", err)
		}
	}
	node, err := up.Finish()
	if err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	return nodeToDomain(node, ref), nil
}

// Mkdir creates a folder. An existing folder is not an error.
func (p *Provider) Mkdir(ctx context.Context, ref string) error {
	ref = strings.Trim(ref, "/")
	if ref == "" {
		return nil
	}
	if _, err := p.resolve(ctx, ref); err == nil {
		return nil
	}
	parent, name := splitRef(ref)
	parentNode, err := p.resolve(ctx, parent)
	if err != nil {
		return err
	}
	if _, err := p.client.CreateDir(parentNode, name); err != nil {
		return p.wrap("Mkdir", err)
	}
	return nil
}

// Rename moves and/or renames a file or folder.
func (p *Provider) Rename(ctx context.Context, oldRef, newRef string) error {
	oldRef = strings.Trim(oldRef, "/")
	newRef = strings.Trim(newRef, "/")
	if oldRef == "" || newRef == "" || oldRef == newRef {
		return p.wrap("Rename", fmt.Errorf("mega: invalid rename %q -> %q", oldRef, newRef))
	}
	n, err := p.resolve(ctx, oldRef)
	if err != nil {
		return err
	}
	newParent, newName := splitRef(newRef)
	if newParent != "" {
		if _, err := p.resolve(ctx, newParent); err != nil {
			return err
		}
	}
	if err := p.client.Rename(n, newName); err != nil {
		return p.wrap("Rename", err)
	}
	oldParent, _ := splitRef(oldRef)
	if newParent != oldParent {
		newParentNode, err := p.resolve(ctx, newParent)
		if err != nil {
			return err
		}
		if err := p.client.Move(n, newParentNode); err != nil {
			return p.wrap("Rename", err)
		}
	}
	return nil
}

// Delete moves a file or folder to trash (destroy=false).
func (p *Provider) Delete(ctx context.Context, ref string) error {
	ref = strings.Trim(ref, "/")
	if ref == "" {
		return p.wrap("Delete", fmt.Errorf("mega: cannot delete the root"))
	}
	n, err := p.resolve(ctx, ref)
	if err != nil {
		return err
	}
	if err := p.client.Delete(n, false); err != nil {
		return p.wrap("Delete", err)
	}
	return nil
}

// Search walks the tree from the root and matches names
// client-side (the library exposes no server-side search).
// Folder matches are skipped: the file browser searches files.
func (p *Provider) Search(ctx context.Context, query string) ([]domain.Node, error) {
	q := strings.ToLower(query)
	var out []domain.Node
	var walk func(ctx context.Context, n megaNode, ref string) error
	walk = func(ctx context.Context, n megaNode, ref string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		children, err := p.client.Children(n)
		if err != nil {
			return p.wrap("Search", err)
		}
		for _, c := range children {
			if c.Type() == nodeInbox || c.Type() == nodeTrash {
				continue
			}
			child := c.Name()
			if ref != "" {
				child = ref + "/" + child
			}
			if c.Type() == nodeFile {
				if strings.Contains(strings.ToLower(c.Name()), q) {
					out = append(out, nodeToDomain(c, child))
				}
			} else if c.Type() == nodeFolder {
				if err := walk(ctx, c, child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(ctx, p.client.Root(), ""); err != nil {
		return nil, err
	}
	return out, nil
}

// Quota returns (used, total) in bytes.
func (p *Provider) Quota(ctx context.Context) (used, total int64, err error) {
	u, t, err := p.client.Quota()
	if err != nil {
		return 0, 0, p.wrap("Quota", err)
	}
	return int64(u), int64(t), nil
}

// ShareLink returns a public link for a file (with the key, so
// the recipient can actually open it).
func (p *Provider) ShareLink(ctx context.Context, ref string) (string, error) {
	n, err := p.resolve(ctx, ref)
	if err != nil {
		return "", err
	}
	if n.Type() != nodeFile {
		return "", p.wrap("Share", fmt.Errorf("mega: cannot share a folder"))
	}
	link, err := p.client.ShareLink(n)
	if err != nil {
		return "", p.wrap("Share", err)
	}
	return link, nil
}

// mimeOf guesses a MIME type from the extension (MEGA metadata
// does not carry one).
func mimeOf(name string) string {
	ext := strings.ToLower(path.Ext(name))
	switch ext {
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
	case ".mov":
		return "video/quicktime"
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
