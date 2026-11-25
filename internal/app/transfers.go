package app

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
)

// Download fetches a file to a local path. It writes to
// "<path>.part" first and atomically renames on success, so a
// half-downloaded file never impersonates a complete one.
func (a *App) Download(ctx context.Context, mountID, ref, localPath string) error {
	p, err := a.ProviderFor(mountID)
	if err != nil {
		return err
	}
	rc, err := p.OpenRead(ctx, ref)
	if err != nil {
		return fmt.Errorf("app: download %s: %w", ref, err)
	}
	defer rc.Close()
	partPath := localPath + ".part"
	f, err := os.Create(partPath)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, rc)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(partPath)
		return fmt.Errorf("app: download %s: %w", ref, copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(partPath)
		return closeErr
	}
	if err := os.Rename(partPath, localPath); err != nil {
		_ = os.Remove(partPath)
		return err
	}
	return nil
}

// Upload streams a local file to the cloud. The file is streamed, not
// loaded into memory.
func (a *App) Upload(ctx context.Context, mountID, ref, localPath string) (domain.Node, error) {
	p, err := a.ProviderFor(mountID)
	if err != nil {
		return domain.Node{}, err
	}
	f, err := os.Open(localPath)
	if err != nil {
		return domain.Node{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return domain.Node{}, err
	}
	node, err := p.Upload(ctx, ref, f, st.Size())
	if err != nil {
		return domain.Node{}, fmt.Errorf("app: upload %s: %w", ref, err)
	}
	return node, nil
}

// Copy enqueues a cross-cloud copy: source streams into the target
// through io.Pipe, never staged on local disk. The transfer queue owns
// progress, pause, retry and persistence.
func (a *App) Copy(ctx context.Context, srcMount, srcRef, dstMount, dstRef string) (*domain.Transfer, error) {
	if _, err := a.ProviderFor(srcMount); err != nil {
		return nil, err
	}
	if _, err := a.ProviderFor(dstMount); err != nil {
		return nil, err
	}
	t := &domain.Transfer{
		Kind:     domain.TransferCopy,
		SrcMount: srcMount, SrcRef: srcRef,
		DstMount: dstMount, DstRef: dstRef,
	}
	if err := a.Queue.Enqueue(t); err != nil {
		return nil, err
	}
	return t, nil
}

// TransferQueue exposes the queue for the UI (list, pause, cancel).
func (a *App) Transfers() []*domain.Transfer { return a.Queue.List() }

// UploadSkipped is one dropped path that never entered the transfer
// queue (unreadable, too big, ...), with a human reason. The UI shows
// these inline instead of silently dropping files.
type UploadSkipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// UploadResult is the single return value of UploadFiles: mygo.Bind
// allows at most one value plus an error, so the transfers and the
// skipped list travel together. Transfers are queued, not finished:
// the worker executes them asynchronously and Transfers() shows live
// progress (the dock polls it every 5s).
type UploadResult struct {
	Transfers []*domain.Transfer `json:"transfers"`
	Skipped   []UploadSkipped    `json:"skipped"`
}

// UploadFiles enqueues files dropped on the window for upload into
// the given directory ref. Directories are walked recursively and the
// folder itself is recreated on the target, so dropping "照片" keeps
// "照片/a.jpg" instead of flattening it to "a.jpg". Each file is checked against the mount's
// Caps.MaxFileSize (0 = unlimited) before it is queued: oversize
// files land in Skipped with a human reason instead of failing
// halfway through the upload. Unknown mounts fail the whole call;
// unreadable paths only skip themselves.
func (a *App) UploadFiles(ctx context.Context, mountID, ref string, paths []string) (*UploadResult, error) {
	m, err := a.MountByID(mountID)
	if err != nil {
		return nil, err
	}
	if _, err := a.ProviderFor(mountID); err != nil {
		return nil, err
	}
	res := &UploadResult{Transfers: []*domain.Transfer{}, Skipped: []UploadSkipped{}}
	for _, p := range paths {
		a.expandDrop(m, ref, p, res)
	}
	return res, nil
}

// expandDrop resolves one dropped path (file or directory) into queued
// upload transfers.
func (a *App) expandDrop(m *domain.Mount, dirRef, p string, res *UploadResult) {
	info, err := os.Stat(p)
	if err != nil {
		res.Skipped = append(res.Skipped, UploadSkipped{Path: p, Reason: fmt.Sprintf("读不到这个文件：%s", err)})
		return
	}
	if !info.IsDir() {
		a.queueUpload(m, dirRef, p, filepath.Base(p), info.Size(), res)
		return
	}
	skippedBefore := len(res.Skipped)
	files := 0
	_ = filepath.WalkDir(p, func(fp string, d fs.DirEntry, err error) error {
		if err != nil {
			res.Skipped = append(res.Skipped, UploadSkipped{Path: fp, Reason: fmt.Sprintf("读不到这个文件：%s", err)})
			return nil
		}
		if d.IsDir() {
			return nil
		}
		// os.Stat (not the lstat from WalkDir) so a symlink to a file
		// uploads the target's content with the target's size.
		st, err := os.Stat(fp)
		if err != nil {
			res.Skipped = append(res.Skipped, UploadSkipped{Path: fp, Reason: fmt.Sprintf("读不到这个文件：%s", err)})
			return nil
		}
		if st.IsDir() {
			// A symlink to a directory: WalkDir does not follow it.
			res.Skipped = append(res.Skipped, UploadSkipped{Path: fp, Reason: "文件夹链接暂不支持上传"})
			return nil
		}
		rel, err := filepath.Rel(p, fp)
		if err != nil {
			res.Skipped = append(res.Skipped, UploadSkipped{Path: fp, Reason: fmt.Sprintf("算不出相对路径：%s", err)})
			return nil
		}
		rel = filepath.Join(filepath.Base(p), rel)
		files++
		a.queueUpload(m, dirRef, fp, filepath.ToSlash(rel), st.Size(), res)
		return nil
	})
	if files == 0 && len(res.Skipped) == skippedBefore {
		res.Skipped = append(res.Skipped, UploadSkipped{Path: p, Reason: "文件夹是空的，没有可上传的文件"})
	}
}

// queueUpload checks the size cap and enqueues one file.
func (a *App) queueUpload(m *domain.Mount, dirRef, localPath, rel string, size int64, res *UploadResult) {
	if max := m.Caps.MaxFileSize; max > 0 && size > max {
		res.Skipped = append(res.Skipped, UploadSkipped{
			Path:   localPath,
			Reason: fmt.Sprintf("文件太大（%s），这个网盘单个文件最多 %s", formatBytes(size), formatBytes(max)),
		})
		return
	}
	t := &domain.Transfer{
		Kind:     domain.TransferUpload,
		SrcMount: domain.LocalMountID, SrcRef: localPath,
		DstMount: m.ID, DstRef: joinRef(dirRef, rel),
		Size: size,
	}
	if err := a.Queue.Enqueue(t); err != nil {
		res.Skipped = append(res.Skipped, UploadSkipped{Path: localPath, Reason: fmt.Sprintf("加入传输队列失败：%s", err)})
		return
	}
	res.Transfers = append(res.Transfers, t)
}

// joinRef joins a destination directory ref with a slash-separated
// relative path. Refs are slash paths on every driver (Drive resolves
// them to folder IDs internally), so this is slash joining — but
// path.Join would clean "" to ".", so it is hand-rolled.
func joinRef(dir, rel string) string {
	dir = strings.TrimSuffix(strings.TrimSpace(dir), "/")
	rel = strings.TrimPrefix(rel, "/")
	if dir == "" || dir == "/" {
		return rel
	}
	return dir + "/" + rel
}

// formatBytes renders a byte count for humans ("1.2 GB", "10 B").
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// ExecuteTransfer is the transfer.Worker executor: one implementation
// for every queued move, so the desktop entry point and the tests all
// run the same code. Uploads stream the local file through the
// destination driver (never loaded into memory); copies stream source
// into target through io.Pipe (never staged on disk). report gets
// throttled byte progress for the queue / dock.
func (a *App) ExecuteTransfer(ctx context.Context, t *domain.Transfer, report func(done, speedBps int64)) error {
	switch t.Kind {
	case domain.TransferUpload:
		return a.executeUpload(ctx, t, report)
	case domain.TransferCopy:
		return a.executeCopy(ctx, t, report)
	default:
		return fmt.Errorf("app: 不支持的传输类型 %q", t.Kind)
	}
}

func (a *App) executeUpload(ctx context.Context, t *domain.Transfer, report func(done, speedBps int64)) error {
	src, err := a.ProviderFor(domain.LocalMountID)
	if err != nil {
		return err
	}
	dst, err := a.ProviderFor(t.DstMount)
	if err != nil {
		return err
	}
	rc, err := src.OpenRead(ctx, t.SrcRef)
	if err != nil {
		return fmt.Errorf("app: upload %s: %w", t.SrcRef, err)
	}
	defer rc.Close()
	// Best-effort parent creation: a dropped folder's parents do not
	// exist on the target yet. Mkdir is idempotent on every driver
	// (Dropbox/Drive treat "already exists" as success); a real
	// failure surfaces from Upload itself, so errors are ignored here.
	if parent := path.Dir(t.DstRef); parent != "" && parent != "." && parent != "/" {
		_ = dst.Mkdir(ctx, parent)
	}
	pr := newProgressReader(rc, report)
	if _, err := dst.Upload(ctx, t.DstRef, pr, t.Size); err != nil {
		return fmt.Errorf("app: upload %s: %w", t.SrcRef, err)
	}
	report(t.Size, 0)
	return nil
}

func (a *App) executeCopy(ctx context.Context, t *domain.Transfer, report func(done, speedBps int64)) error {
	src, err := a.ProviderFor(t.SrcMount)
	if err != nil {
		return err
	}
	dst, err := a.ProviderFor(t.DstMount)
	if err != nil {
		return err
	}
	rc, err := src.OpenRead(ctx, t.SrcRef)
	if err != nil {
		return err
	}
	defer rc.Close()
	pr, pw := io.Pipe()
	go func() {
		_, err := io.Copy(pw, newProgressReader(rc, report))
		_ = pw.CloseWithError(err)
	}()
	if _, err := dst.Upload(ctx, t.DstRef, pr, t.Size); err != nil {
		return err
	}
	report(t.Size, 0)
	return nil
}

// progressReader counts bytes through to report, throttled to one
// callback per 200ms so the queue (and the dock's 5s poll) is not
// flooded by 32KB reads.
type progressReader struct {
	r      io.Reader
	report func(done, speedBps int64)
	start  time.Time
	last   time.Time
	done   int64
}

func newProgressReader(r io.Reader, report func(done, speedBps int64)) *progressReader {
	now := time.Now()
	return &progressReader{r: r, report: report, start: now, last: now}
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.done += int64(n)
		now := time.Now()
		if now.Sub(p.last) >= 200*time.Millisecond {
			elapsed := now.Sub(p.start).Seconds()
			var speed int64
			if elapsed > 0 {
				speed = int64(float64(p.done) / elapsed)
			}
			p.report(p.done, speed)
			p.last = now
		}
	}
	return n, err
}
