// SPDX-License-Identifier: MPL-2.0
package mount

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/luckzk/zy-drive/internal/domain"
)

// fs.go — the go-fuse filesystem: root → one dir per enabled
// mount → provider nodes (v0.19.0, P0).
//
// Reads are served from the mount cache (whole-file download on
// first open; P0 has no Range read). Writes land in the cache
// first and are uploaded on close. The kernel caches entries /
// attrs for 1s only (EntryTimeout / AttrTimeout), so a remote
// change shows up quickly after the watcher invalidates a
// directory.
//
// Error mapping: the kernel only sees errnos — ENOSPC for a
// cache that cannot fit the file, EIO for cloud failures, ENOENT
// for a disabled / missing mount. The human sentence goes to
// the log (and, for volume failures, to the status card).

// volumeServer is what a mounted volume gives back.
type volumeServer interface {
	Unmount() error
}

// volumeBackend performs the actual FUSE mount. The default is
// go-fuse; tests inject a stub.
type volumeBackend interface {
	mount(dir string, root fs.InodeEmbedder, opts *fs.Options) (volumeServer, error)
}

type goFuseBackend struct{}

func (goFuseBackend) mount(dir string, root fs.InodeEmbedder, opts *fs.Options) (volumeServer, error) {
	return fs.Mount(dir, root, opts)
}

// mountOptions builds the go-fuse options: 1s entry/attr
// timeouts (the design's "seconds, not long-lived" rule) and
// the volume name. On macOS the volname option names the
// volume in Finder.
func mountOptions() *fs.Options {
	entryTimeout := 1 * time.Second
	attrTimeout := 1 * time.Second
	opts := &fs.Options{
		MountOptions: fuse.MountOptions{
			AllowOther: false,
			FsName:     "ZyDrive",
			Name:       "zydrive",
		},
		EntryTimeout: &entryTimeout,
		AttrTimeout:  &attrTimeout,
	}
	if runtime.GOOS == "darwin" {
		opts.Options = append(opts.Options, "volname=ZyDrive")
	}
	return opts
}

// fillAttr fills an AttrOut from mode/size/mtime.
func fillAttr(out *fuse.AttrOut, mode uint32, size uint64, mtime time.Time) {
	if mtime.IsZero() {
		mtime = time.Now()
	}
	out.Attr = fuse.Attr{
		Mode:      mode,
		Size:      size,
		Mtime:     uint64(mtime.Unix()),
		Mtimensec: uint32(mtime.Nanosecond()),
		Atime:     uint64(mtime.Unix()),
		Atimensec: uint32(mtime.Nanosecond()),
		Ctime:     uint64(mtime.Unix()),
		Ctimensec: uint32(mtime.Nanosecond()),
		Nlink:     1,
		Owner:     fuse.Owner{Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())},
		Blksize:   4096,
	}
}

func dirMode() uint32  { return syscall.S_IFDIR | 0o755 }
func fileMode() uint32 { return syscall.S_IFREG | 0o644 }

// --- root: ~/ZyDrive itself ---

type rootNode struct {
	fs.Inode
	svc *Service
}

func newRootNode(svc *Service) *rootNode { return &rootNode{svc: svc} }

func (n *rootNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	fillAttr(out, dirMode(), 4096, time.Now())
	return 0
}

// Statfs is deliberately cheap: fixed values, no cloud calls
// (macOS asks for them constantly).
func (n *rootNode) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	const blockSize = 4096
	const blocks = 1 << 24 // 64 GiB virtual volume
	out.Blocks = blocks
	out.Bfree = blocks
	out.Bavail = blocks
	out.Files = 1 << 20
	out.Ffree = 1 << 20
	out.Bsize = blockSize
	out.NameLen = 255
	out.Frsize = blockSize
	return 0
}

func (n *rootNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	n.svc.mu.Lock()
	defer n.svc.mu.Unlock()
	for id, enabled := range n.svc.enabled {
		if !enabled {
			continue
		}
		if n.svc.folder[id] != name {
			continue
		}
		child := n.EmbeddedInode().NewPersistentInode(ctx,
			&dirNode{svc: n.svc, mountID: id, ref: n.svc.mountRootRef(id)},
			fs.StableAttr{Mode: dirMode()})
		return child, 0
	}
	return nil, syscall.ENOENT
}

func (n *rootNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	n.svc.mu.Lock()
	folders := make([]string, 0, len(n.svc.enabled))
	for id, enabled := range n.svc.enabled {
		if enabled {
			folders = append(folders, n.svc.folder[id])
		}
	}
	n.svc.mu.Unlock()
	entries := make([]fuse.DirEntry, 0, len(folders))
	for _, name := range folders {
		if name == "" {
			continue
		}
		entries = append(entries, fuse.DirEntry{Name: name, Mode: fuse.S_IFDIR})
	}
	return fs.NewListDirStream(entries), 0
}

// --- dirNode: one provider directory ---

type dirNode struct {
	fs.Inode
	svc     *Service
	mountID string

	mu  sync.Mutex // guards ref (rename updates it)
	ref string
}

func (n *dirNode) refNow() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.ref
}

func (n *dirNode) setRef(ref string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.ref = ref
}

func (n *dirNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	fillAttr(out, dirMode(), 4096, time.Now())
	return 0
}

func (n *dirNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	entries, err := n.svc.listDir(n.mountID, n.refNow())
	if err != nil {
		log.Printf("mount: lookup %s/%s: %v", n.mountID, name, err)
		return nil, syscall.EIO
	}
	for _, e := range entries {
		if e.Name != name {
			continue
		}
		if e.IsDir {
			return n.EmbeddedInode().NewInode(ctx,
				&dirNode{svc: n.svc, mountID: n.mountID, ref: e.Ref},
				fs.StableAttr{Mode: dirMode()}), 0
		}
		return n.EmbeddedInode().NewInode(ctx,
			&fileNode{svc: n.svc, mountID: n.mountID, ref: e.Ref, size: e.Size, mtime: e.ModTime},
			fs.StableAttr{Mode: fileMode()}), 0
	}
	return nil, syscall.ENOENT
}

func (n *dirNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries, err := n.svc.listDir(n.mountID, n.refNow())
	if err != nil {
		log.Printf("mount: readdir %s: %v", n.mountID, err)
		return nil, syscall.EIO
	}
	out := make([]fuse.DirEntry, 0, len(entries))
	for _, e := range entries {
		mode := uint32(fuse.S_IFREG)
		if e.IsDir {
			mode = uint32(fuse.S_IFDIR)
		}
		out = append(out, fuse.DirEntry{Name: e.Name, Mode: mode})
	}
	return fs.NewListDirStream(out), 0
}

// Mkdir creates a remote folder and returns its node.
func (n *dirNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	prov, err := n.svc.providerFor(n.mountID)
	if err != nil {
		return nil, syscall.EIO
	}
	ref := joinRef(n.refNow(), name)
	cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := prov.Mkdir(cctx, ref); err != nil {
		log.Printf("mount: mkdir %s: %v", ref, err)
		return nil, syscall.EIO
	}
	n.svc.invalidateDir(n.mountID, n.refNow())
	return n.EmbeddedInode().NewInode(ctx,
		&dirNode{svc: n.svc, mountID: n.mountID, ref: ref},
		fs.StableAttr{Mode: dirMode()}), 0
}

// Create makes a new empty file: cache first, upload on close.
// The file shows in the listing at once (pending overlay) even
// though the cloud does not have it yet.
func (n *dirNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	ref := joinRef(n.refNow(), name)
	full, err := n.svc.cache.createEmpty(n.mountID, ref)
	if err != nil {
		log.Printf("mount: create %s: %v", ref, err)
		return nil, nil, 0, syscall.EIO
	}
	node := &fileNode{svc: n.svc, mountID: n.mountID, ref: ref, size: 0, mtime: time.Now()}
	inode := n.EmbeddedInode().NewInode(ctx, node, fs.StableAttr{Mode: fileMode()})
	n.svc.addPending(n.mountID, n.refNow(), domain.Node{
		Ref: ref, Path: ref, Name: name, Size: 0, ModTime: time.Now(),
	})
	f, err := os.OpenFile(full, os.O_RDWR, 0o644)
	if err != nil {
		log.Printf("mount: create %s: open cache: %v", ref, err)
		return nil, nil, 0, syscall.EIO
	}
	n.svc.cache.AddOpen(n.mountID, ref)
	return inode, &fileHandle{node: node, f: f, writable: true}, 0, 0
}

// Unlink deletes one file (remote + cache). A pending file
// (created locally, never uploaded) is only local: no remote
// call, it just goes away.
func (n *dirNode) Unlink(ctx context.Context, name string) syscall.Errno {
	ref := joinRef(n.refNow(), name)
	if n.svc.isPending(n.mountID, ref) {
		n.svc.removePending(n.mountID, n.refNow(), name)
		_ = n.svc.cache.deleteEntry(n.mountID, ref)
		n.svc.invalidateDir(n.mountID, n.refNow())
		return 0
	}
	prov, err := n.svc.providerFor(n.mountID)
	if err != nil {
		return syscall.EIO
	}
	cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := prov.Delete(cctx, ref); err != nil {
		log.Printf("mount: delete %s: %v", ref, err)
		return syscall.EIO
	}
	_ = n.svc.cache.deleteEntry(n.mountID, ref)
	n.svc.invalidateDir(n.mountID, n.refNow())
	return 0
}

// Rmdir deletes one (empty) remote folder.
func (n *dirNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	ref := joinRef(n.refNow(), name)
	prov, err := n.svc.providerFor(n.mountID)
	if err != nil {
		return syscall.EIO
	}
	cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := prov.Delete(cctx, ref); err != nil {
		log.Printf("mount: rmdir %s: %v", ref, err)
		return syscall.EIO
	}
	_ = n.svc.cache.deleteEntry(n.mountID, ref)
	n.svc.invalidateDir(n.mountID, n.refNow())
	return 0
}

// Rename moves a file or folder. Same-mount only (cross-mount
// is EXDEV: Provider.Rename cannot cross providers). An editor's
// atomic save (write tmp + rename over) is the hot path: the
// cache file follows the rename and keeps its dirty bit, and
// the tmp file never enters the upload queue on its own.
func (n *dirNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	dst, ok := newParent.EmbeddedInode().Operations().(*dirNode)
	if !ok {
		return syscall.EXDEV
	}
	if dst.mountID != n.mountID {
		return syscall.EXDEV
	}
	srcRef := joinRef(n.refNow(), name)
	dstRef := joinRef(dst.refNow(), newName)
	if srcRef == dstRef {
		return 0
	}
	// Is the source a directory? (Pending entries are files.)
	isDir := false
	if entries, err := n.svc.listDir(n.mountID, n.refNow()); err == nil {
		for _, e := range entries {
			if e.Name == name {
				isDir = e.IsDir
				break
			}
		}
	}
	if n.svc.isPending(n.mountID, srcRef) {
		// Never uploaded: only local state moves, no cloud call.
		if err := n.svc.cache.renameEntry(n.mountID, srcRef, dstRef); err != nil {
			log.Printf("mount: rename cache %s: %v", srcRef, err)
			return syscall.EIO
		}
		n.svc.movePending(n.mountID, n.refNow(), name, dst.refNow(), newName, dstRef)
		n.updateChildRef(name, dstRef, isDir)
		return 0
	}
	prov, err := n.svc.providerFor(n.mountID)
	if err != nil {
		return syscall.EIO
	}
	cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := prov.Rename(cctx, srcRef, dstRef); err != nil {
		log.Printf("mount: rename %s -> %s: %v", srcRef, dstRef, err)
		return syscall.EIO
	}
	if isDir {
		if err := n.svc.cache.renamePrefix(n.mountID, srcRef, dstRef); err != nil {
			log.Printf("mount: rename cache prefix %s: %v", srcRef, err)
		}
		n.svc.renamePendingPrefix(n.mountID, srcRef, dstRef)
	} else {
		if err := n.svc.cache.renameEntry(n.mountID, srcRef, dstRef); err != nil {
			log.Printf("mount: rename cache %s: %v", srcRef, err)
		}
		n.svc.uploader.renameQueued(n.mountID, srcRef, dstRef)
	}
	n.svc.invalidateDir(n.mountID, n.refNow())
	n.svc.invalidateDir(n.mountID, dst.refNow())
	n.updateChildRef(name, dstRef, isDir)
	return 0
}

// updateChildRef moves the ref on the already-created child
// node, so an open handle keeps uploading under the new name.
func (n *dirNode) updateChildRef(name, newRef string, isDir bool) {
	child := n.EmbeddedInode().GetChild(name)
	if child == nil {
		return
	}
	switch ops := child.Operations().(type) {
	case *fileNode:
		if !isDir {
			ops.setRef(newRef)
		}
	case *dirNode:
		if isDir {
			ops.setRef(newRef)
		}
	}
}

// --- fileNode: one provider file ---

type fileNode struct {
	fs.Inode
	svc     *Service
	mountID string

	mu    sync.Mutex // guards ref/size/mtime
	ref   string
	size  int64
	mtime time.Time
}

func (n *fileNode) refNow() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.ref
}

func (n *fileNode) setRef(ref string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.ref = ref
}

func (n *fileNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	n.mu.Lock()
	size, mtime := n.size, n.mtime
	ref := n.ref
	n.mu.Unlock()
	// A dirty copy shows its LOCAL size/mtime (the editor just
	// wrote it); a clean copy shows the remote values.
	if e, err := n.svc.cache.entry(n.mountID, ref); err == nil && e != nil {
		if e.Dirty {
			if fi, serr := os.Stat(n.svc.cache.pathFor(n.mountID, ref)); serr == nil {
				size, mtime = fi.Size(), fi.ModTime()
			}
		} else {
			size, mtime = e.Size, e.RemoteMtime
		}
	}
	fillAttr(out, fileMode(), uint64(maxInt64(size, 0)), mtime)
	return 0
}

// Setattr handles truncate (the only setattr P0 honours):
// truncate the cached copy, mark it dirty, queue the upload.
// Mtime-only sets are ignored — the local mtime is the file's
// own, and the remote mtime is authoritative at upload time.
func (n *fileNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	size, ok := in.GetSize()
	if !ok {
		return n.Getattr(ctx, f, out)
	}
	ref := n.refNow()
	full := n.svc.cache.pathFor(n.mountID, ref)
	if _, err := os.Stat(full); err != nil {
		// Not cached yet: download first, then truncate.
		if errno := n.ensureCached(); errno != 0 {
			return errno
		}
	}
	lf, err := os.OpenFile(full, os.O_RDWR, 0o644)
	if err != nil {
		return syscall.EIO
	}
	if err := lf.Truncate(int64(size)); err != nil {
		_ = lf.Close()
		return syscall.EIO
	}
	_ = lf.Close()
	n.mu.Lock()
	n.size = int64(size)
	n.mtime = time.Now()
	n.mu.Unlock()
	if err := n.svc.cache.markDirty(n.mountID, ref); err != nil {
		log.Printf("mount: setattr dirty %s: %v", ref, err)
		return syscall.EIO
	}
	n.svc.uploader.enqueue(n.mountID, ref)
	return n.Getattr(ctx, f, out)
}

// ensureCached downloads the file into the cache (or returns
// the error the kernel should see).
func (n *fileNode) ensureCached() syscall.Errno {
	ref := n.refNow()
	st, err := n.svc.statNode(n.mountID, ref)
	if err != nil {
		return syscall.ENOENT
	}
	if st.IsDir {
		return syscall.EISDIR
	}
	prov, err := n.svc.providerFor(n.mountID)
	if err != nil {
		return syscall.EIO
	}
	n.svc.cache.noteRemoteHash(n.mountID, ref, st.Hash, st.HashKind)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	_, _, err = n.svc.cache.ensure(ctx, prov, n.svc.limiterValue(), n.mountID, ref, st.Size, st.ModTime)
	if err != nil {
		if errors.Is(err, ErrNoSpace) {
			log.Printf("mount: open %s: %v", ref, err)
			return syscall.ENOSPC
		}
		log.Printf("mount: open %s: %v", ref, err)
		return syscall.EIO
	}
	return 0
}

// Open downloads the file on first use (whole file, P0) and
// hands out a handle on the local cache file. A file bigger
// than the cache's free space is refused with ENOSPC.
func (n *fileNode) Open(ctx context.Context, flags uint32) (fh fs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	ref := n.refNow()
	st, err := n.svc.statNode(n.mountID, ref)
	if err != nil {
		return nil, 0, syscall.ENOENT
	}
	if st.IsDir {
		return nil, 0, syscall.EISDIR
	}
	writable := flags&uint32(syscall.O_ACCMODE) != uint32(syscall.O_RDONLY)
	// A pending file (created here, never uploaded) already
	// has its local copy: no download, no remote Stat needed.
	if !n.svc.isPending(n.mountID, ref) {
		if errno := n.ensureCached(); errno != 0 {
			return nil, 0, errno
		}
	}
	full := n.svc.cache.pathFor(n.mountID, ref)
	openFlags := os.O_RDONLY
	if writable {
		openFlags = os.O_RDWR
	}
	f, err := os.OpenFile(full, openFlags, 0o644)
	if err != nil {
		log.Printf("mount: open cache %s: %v", ref, err)
		return nil, 0, syscall.EIO
	}
	if writable && flags&uint32(syscall.O_TRUNC) != 0 {
		if err := f.Truncate(0); err != nil {
			_ = f.Close()
			return nil, 0, syscall.EIO
		}
		n.mu.Lock()
		n.size = 0
		n.mtime = time.Now()
		n.mu.Unlock()
		h := &fileHandle{node: n, f: f, writable: true, wrote: true}
		n.svc.cache.AddOpen(n.mountID, ref)
		return h, 0, 0
	}
	n.svc.cache.AddOpen(n.mountID, ref)
	return &fileHandle{node: n, f: f, writable: writable}, 0, 0
}

// --- fileHandle: one open file ---

type fileHandle struct {
	node     *fileNode
	f        *os.File
	writable bool

	mu    sync.Mutex // guards wrote
	wrote bool
}

func (h *fileHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	n, err := h.f.ReadAt(dest, off)
	if err != nil && err != io.EOF {
		return nil, syscall.EIO
	}
	return fuse.ReadResultData(dest[:n]), 0
}

func (h *fileHandle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	if !h.writable {
		return 0, syscall.EBADF
	}
	n, err := h.f.WriteAt(data, off)
	if err != nil {
		return 0, syscall.EIO
	}
	h.mu.Lock()
	h.wrote = true
	h.mu.Unlock()
	h.node.mu.Lock()
	if off+int64(n) > h.node.size {
		h.node.size = off + int64(n)
	}
	h.node.mtime = time.Now()
	h.node.mu.Unlock()
	return uint32(n), 0
}

func (h *fileHandle) Flush(ctx context.Context) syscall.Errno {
	if err := h.f.Sync(); err != nil {
		return syscall.EIO
	}
	return 0
}

func (h *fileHandle) Fsync(ctx context.Context, flags uint32) syscall.Errno {
	if err := h.f.Sync(); err != nil {
		return syscall.EIO
	}
	return 0
}

func (h *fileHandle) Getattr(ctx context.Context, out *fuse.AttrOut) syscall.Errno {
	fi, err := h.f.Stat()
	if err != nil {
		return syscall.EIO
	}
	fillAttr(out, fileMode(), uint64(fi.Size()), fi.ModTime())
	return 0
}

// Release closes the handle: a handle that wrote marks the
// cache dirty and queues the upload; a read-only close does
// nothing. The last close of the last open file can unmount
// an emptied volume (see Service.afterClose).
func (h *fileHandle) Release(ctx context.Context) syscall.Errno {
	ref := h.node.refNow()
	mountID := h.node.mountID
	_ = h.f.Close()
	h.node.svc.cache.RemoveOpen(mountID, ref)
	h.mu.Lock()
	wrote := h.wrote
	h.mu.Unlock()
	if wrote {
		if err := h.node.svc.cache.markDirty(mountID, ref); err != nil {
			log.Printf("mount: release dirty %s: %v", ref, err)
		} else {
			h.node.svc.uploader.enqueue(mountID, ref)
		}
	}
	h.node.svc.afterClose()
	return 0
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
