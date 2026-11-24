package app

import (
	"context"
	"fmt"
	"io"
	"os"

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
