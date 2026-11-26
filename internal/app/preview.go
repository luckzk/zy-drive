// SPDX-License-Identifier: MPL-2.0
package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/luckzk/zy-drive/internal/storage"
)

// preview.go — file preview (v0.9.1, free): the App-side half.
// Two tiers, per the 2026-11-25 decision:
//   - In-app direct preview (images, streaming video/audio, PDF,
//     plain text/code/Markdown): the webview renders a URL from
//     PreviewURL — a provider direct URL where the driver has one
//     (S3 presigned GET, Dropbox temporary link, OneDrive
//     downloadUrl, pCloud getfilelink), else a token URL on the
//     loopback preview proxy (preview_proxy.go), which streams
//     OpenRead. Text/Markdown go through PreviewText instead (the
//     frontend renders them; nothing is downloaded to disk).
//   - System-app open (Office, PSD, anything the webview cannot
//     render): OpenWithSystemApp downloads to the preview cache
//     (preview_cache.go) and opens the copy with the OS default
//     app — macOS prefers Quick Look. See preview_open.go.
//
// In-app preview never touches disk; only the system-app tier
// writes the preview cache.

// PreviewURLResult is the single return value of PreviewURL.
// Kind is "direct" (a provider-issued URL, short-lived by nature)
// or "proxy" (a zydrive-preview:// token URL, 10 minutes).
type PreviewURLResult struct {
	URL  string `json:"url"`
	Kind string `json:"kind"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
}

// PreviewTextResult is the single return value of PreviewText.
// Text is at most previewTextLimit bytes; Truncated says the file
// continues past it. Size is the full remote size.
type PreviewTextResult struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
	Size      int64  `json:"size"`
}

// previewTextLimit caps PreviewText: 512 KiB, enough for any
// readable file; larger files are truncated, never fully read.
const previewTextLimit = 512 * 1024

// previewProxy returns the App's preview proxy, creating it on
// first use. The proxy owns only tokens; it never touches mounts.
func (a *App) previewProxy() *previewProxy {
	if a.pproxy == nil {
		a.pproxy = newPreviewProxy()
	}
	return a.pproxy
}

// PreviewHandler is the http.Handler cmd/desktop registers as the
// "zydrive-preview" mygo Protocol scheme (before the window is
// created). It serves the proxy token URLs PreviewURL mints.
func (a *App) PreviewHandler() http.Handler { return a.previewProxy() }

// PreviewURL resolves a file to a URL the webview can render
// directly. Direct URLs are preferred (no process hop, real Range
// seeks); a DirectURL failure falls back to the proxy instead of
// failing the preview — the proxy streams the same bytes through
// OpenRead, so the fallback only ever loses seek speed.
func (a *App) PreviewURL(ctx context.Context, mountID, ref string) (*PreviewURLResult, error) {
	h, ok := a.Mount(mountID)
	if !ok {
		return nil, fmt.Errorf("app: 未知的网盘 %q", mountID)
	}
	st, err := h.Provider.Stat(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("app: 预览失败：%w", err)
	}
	if st.IsDir {
		return nil, fmt.Errorf("app: 文件夹不能预览，请先打开它")
	}
	mime := previewMime(st.Name)
	if st.Mime != "" {
		mime = st.Mime
	}
	if du, ok := h.Provider.(storage.DirectURLer); ok {
		if u, err := du.DirectURL(ctx, ref); err == nil && u != "" {
			return &PreviewURLResult{URL: u, Kind: "direct", Mime: mime, Size: st.Size}, nil
		}
		// Fall through to the proxy: same bytes, slower seeks.
	}
	tok, err := a.previewProxy().mint(h.Provider, a.transferLimiter, ref, st.Size, mime)
	if err != nil {
		return nil, fmt.Errorf("app: 预览失败：%w", err)
	}
	return &PreviewURLResult{URL: previewSchemeBase + tok, Kind: "proxy", Mime: mime, Size: st.Size}, nil
}

// PreviewText reads the head of a text file for in-app rendering.
// Binary content (a NUL byte in the head) or non-UTF-8 bytes are a
// plain error, not garbage rendered as text — the frontend only
// calls this for the text tier, so either means the file was
// misclassified and should not be shown as text.
func (a *App) PreviewText(ctx context.Context, mountID, ref string) (*PreviewTextResult, error) {
	h, ok := a.Mount(mountID)
	if !ok {
		return nil, fmt.Errorf("app: 未知的网盘 %q", mountID)
	}
	st, err := h.Provider.Stat(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("app: 预览失败：%w", err)
	}
	if st.IsDir {
		return nil, fmt.Errorf("app: 文件夹不能预览，请先打开它")
	}
	rc, err := h.Provider.OpenRead(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("app: 预览失败：%w", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, previewTextLimit+1))
	if err != nil {
		return nil, fmt.Errorf("app: 预览失败：%w", err)
	}
	truncated := len(data) > previewTextLimit
	if truncated {
		data = data[:previewTextLimit]
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, fmt.Errorf("app: 这个文件不是文本文件（含有二进制内容），无法用文本预览")
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("app: 这个文件不是 UTF-8 文本，无法用文本预览")
	}
	return &PreviewTextResult{Text: string(data), Truncated: truncated, Size: st.Size}, nil
}
