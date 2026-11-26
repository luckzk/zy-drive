// SPDX-License-Identifier: MPL-2.0
package app

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/luckzk/zy-drive/internal/domain"
)

// fileops.go — the file operations behind the browse page's
// right-click menu (v0.10.0): delete, rename, move. The driver
// interface already had Rename/Delete since V0.1; this file is the
// first app-layer wiring that exposes them to the UI. Nothing here
// invents data: every result names the refs that were actually
// handled, and failures carry the reason instead of failing the
// whole batch.

// FileOpResult is the per-item outcome of DeleteNodes/MoveNodes:
// refs finished synchronously (Done), refs that failed with a
// human reason (Failed), and copy transfers still running in the
// queue (Transfers — cross-cloud moves only; the worker deletes
// each source file after its own copy succeeds, see
// Transfer.DeleteSource). mygo.Bind allows at most one value plus
// an error, so all three travel together.
type FileOpResult struct {
	Done      []string            `json:"done"`
	Failed    []UploadSkipped     `json:"failed"`
	Transfers []*domain.Transfer  `json:"transfers"`
}

func newFileOpResult() *FileOpResult {
	return &FileOpResult{Done: []string{}, Failed: []UploadSkipped{}, Transfers: []*domain.Transfer{}}
}

// DeleteNodes deletes each ref on a mount, one by one. A ref that
// fails only fails itself: the rest still run, and the failure
// carries its reason. Whether the provider moves the node to trash
// or deletes it permanently is the provider's Caps.DeleteToTrash —
// the UI says which before asking.
func (a *App) DeleteNodes(ctx context.Context, mountID string, refs []string) (*FileOpResult, error) {
	if len(refs) == 0 {
		return nil, fmt.Errorf("app: 请先选要删除的文件或文件夹")
	}
	p, err := a.ProviderFor(mountID)
	if err != nil {
		return nil, err
	}
	if !p.Caps().CanDelete {
		return nil, fmt.Errorf("app: 这个网盘不支持删除")
	}
	res := newFileOpResult()
	for _, ref := range refs {
		if err := p.Delete(ctx, ref); err != nil {
			res.Failed = append(res.Failed, UploadSkipped{Path: ref, Reason: fmt.Sprintf("删除失败：%s", err)})
			continue
		}
		res.Done = append(res.Done, ref)
	}
	return res, nil
}

// RenameNode renames one node in place: newName is a bare file name
// (no slashes), the destination is the same directory with the new
// name. Every driver takes path-style refs and resolves them to
// IDs internally, so the same join works for Drive/OneDrive/Box
// as for path-based clouds. It returns the new ref.
func (a *App) RenameNode(ctx context.Context, mountID, srcRef, newName string) (string, error) {
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return "", fmt.Errorf("app: 新名字不能为空")
	}
	if strings.ContainsAny(newName, `/\`) || newName == "." || newName == ".." {
		return "", fmt.Errorf("app: 新名字不能含 /，也不能是 . 或 ..")
	}
	srcRef = strings.TrimSuffix(strings.TrimSpace(srcRef), "/")
	if srcRef == "" {
		return "", fmt.Errorf("app: 不能重命名根目录")
	}
	p, err := a.ProviderFor(mountID)
	if err != nil {
		return "", err
	}
	if !p.Caps().CanRename {
		return "", fmt.Errorf("app: 这个网盘不支持重命名")
	}
	if path.Base(srcRef) == newName {
		return srcRef, nil
	}
	dst := newName
	if parent := parentDirRef(srcRef); parent != "" {
		dst = parent + "/" + newName
	}
	if err := p.Rename(ctx, srcRef, dst); err != nil {
		return "", fmt.Errorf("app: 重命名失败：%w", err)
	}
	return dst, nil
}

// MoveNodes moves refs to dstRef on dstMount.
//
// Same mount: a straight Rename per ref (server-side, no data
// moves). Moving a node onto itself is skipped, and a folder can
// never be moved into itself or one of its own subfolders.
//
// Cross-mount: the refs expand to files (folders keep their
// structure, like BatchCopy) and each file is enqueued as a copy
// transfer with DeleteSource set — the worker deletes the source
// file only after its own copy succeeded (see executeCopy), so a
// failed copy never deletes its source. The files' parent folders
// are left behind empty: the UI polls the queued transfers and
// deletes a folder once every file under it has completed (see
// FileBrowser's move cleanup poll). The returned Transfers are
// queued, not finished; Done stays empty on this path.
func (a *App) MoveNodes(ctx context.Context, srcMount string, refs []string, dstMount, dstRef string) (*FileOpResult, error) {
	if len(refs) == 0 {
		return nil, fmt.Errorf("app: 请先选要移动的文件或文件夹")
	}
	src, err := a.ProviderFor(srcMount)
	if err != nil {
		return nil, err
	}
	dst, err := a.ProviderFor(dstMount)
	if err != nil {
		return nil, err
	}
	if srcMount == dstMount {
		if !src.Caps().CanRename {
			return nil, fmt.Errorf("app: 这个网盘不支持移动")
		}
		res := newFileOpResult()
		for _, ref := range refs {
			node, err := src.Stat(ctx, ref)
			if err != nil {
				res.Failed = append(res.Failed, UploadSkipped{Path: ref, Reason: fmt.Sprintf("读不到这个文件或文件夹：%s", err)})
				continue
			}
			dst := joinRef(dstRef, node.Name)
			if dst == ref {
				res.Failed = append(res.Failed, UploadSkipped{Path: ref, Reason: "已经在这个文件夹里了"})
				continue
			}
			if node.IsDir && strings.HasPrefix(dst, ref+"/") {
				res.Failed = append(res.Failed, UploadSkipped{Path: ref, Reason: "不能把文件夹移到自己里面"})
				continue
			}
			if err := src.Rename(ctx, ref, dst); err != nil {
				res.Failed = append(res.Failed, UploadSkipped{Path: ref, Reason: fmt.Sprintf("移动失败：%s", err)})
				continue
			}
			res.Done = append(res.Done, ref)
		}
		return res, nil
	}
	// Cross-mount: copy first, then delete only what copied.
	// Empty folders have no files to copy: create the folder at
	// the target and delete the source right away (both
	// synchronous), so moving an empty folder is not a silent
	// no-op. Non-empty folders go through the queue below.
	var copyRefs []string
	res := newFileOpResult()
	for _, ref := range refs {
		node, err := src.Stat(ctx, ref)
		if err != nil {
			res.Failed = append(res.Failed, UploadSkipped{Path: ref, Reason: fmt.Sprintf("读不到这个文件或文件夹：%s", err)})
			continue
		}
		if !node.IsDir {
			copyRefs = append(copyRefs, ref)
			continue
		}
		children, err := src.List(ctx, ref)
		if err != nil {
			res.Failed = append(res.Failed, UploadSkipped{Path: ref, Reason: fmt.Sprintf("列不出文件夹：%s", err)})
			continue
		}
		if len(children) > 0 {
			copyRefs = append(copyRefs, ref)
			continue
		}
		dstDir := joinRef(dstRef, node.Name)
		if err := dst.Mkdir(ctx, dstDir); err != nil {
			res.Failed = append(res.Failed, UploadSkipped{Path: ref, Reason: fmt.Sprintf("在目标创建文件夹失败：%s", err)})
			continue
		}
		if err := src.Delete(ctx, ref); err != nil {
			res.Failed = append(res.Failed, UploadSkipped{Path: ref, Reason: fmt.Sprintf("目标文件夹已建好，但删除源文件夹失败（源文件夹还在）：%s", err)})
			continue
		}
		res.Done = append(res.Done, ref)
	}

	entries, skipped := a.expandBatchRefs(ctx, src, copyRefs)
	res.Failed = append(res.Failed, skipped...)
	used := map[string]bool{}
	for _, e := range entries {
		dst := joinRef(dstRef, e.rel)
		if used[dst] {
			res.Failed = append(res.Failed, UploadSkipped{Path: e.srcRef, Reason: "这次批量里已有同名目标，会互相覆盖，已跳过"})
			continue
		}
		used[dst] = true
		t := &domain.Transfer{
			Kind:         domain.TransferCopy,
			SrcMount:     srcMount,
			SrcRef:       e.srcRef,
			DstMount:     dstMount,
			DstRef:       dst,
			Size:         max(int64(0), e.size),
			DeleteSource: true,
		}
		if err := a.Queue.Enqueue(t); err != nil {
			res.Failed = append(res.Failed, UploadSkipped{Path: e.srcRef, Reason: fmt.Sprintf("加入传输队列失败：%s", err)})
			continue
		}
		res.Transfers = append(res.Transfers, t)
	}
	return res, nil
}

// parentDirRef is path.Dir with "." normalized to "" (the mount
// root): renaming "a.txt" stays at the root instead of moving
// into a literal "." folder.
func parentDirRef(ref string) string {
	d := path.Dir(ref)
	if d == "." {
		return ""
	}
	return d
}
