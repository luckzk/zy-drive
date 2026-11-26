// SPDX-License-Identifier: MPL-2.0
package app

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/transfer"
)

// preview_proxy.go — the loopback preview server for drivers that
// cannot hand the webview a direct URL (Drime, Box, MEGA, local:
// see storage.DirectURLer). It streams provider OpenRead to the
// webview, with HTTP Range support so <video> can seek.
//
// Why a custom scheme, not http://127.0.0.1: the production
// frontend loads from mygo://localhost/ (a custom scheme), and a
// plain-http subresource from it is mixed content the webview may
// refuse. So the proxy is registered as a mygo Protocol scheme
// ("zydrive-preview", cmd/desktop/mygo.go, before the window is
// created) and PreviewURL mints zydrive-preview://localhost/
// preview/<token> URLs. The same ServeHTTP works over plain HTTP
// too (tests use httptest), but the desktop never listens on a TCP
// port for it.
//
// Tokens: 16 random bytes, hex. A token is minted per PreviewURL
// call, lives 10 minutes, and is reusable inside that window —
// a <video> element makes one request per seek, so a single-use
// token would break playback after the first seek. The token
// record snapshots the provider (and the limiter) at mint time:
// the handler never touches App.mounts, so there is no concurrent
// map read/write with Add/RemoveMount. A token minted for a mount
// that is later removed still streams until it expires —
// acceptable for a 10-minute unguessable token.
//
// Range: only "bytes=<start>-" and "bytes=<start>-<end>" are
// honoured. Providers without ranged reads are served by
// re-opening and discarding to the offset — a slow seek, and the
// reason direct URLs are preferred where the driver has one.
// Discarded bytes do NOT count against the speed limiter: the
// discard is provider-side re-read overhead, not user-visible
// transfer, and throttling it would make every seek wait for the
// whole prefix to trickle through the cap. Bytes actually served
// to the webview do count (the same shared limiter transfers use).

// previewToken is one minted preview URL.
type previewToken struct {
	provider storage.Provider
	limiter  *transfer.Limiter // nil = unlimited
	ref      string
	size     int64
	mime     string
	expires  time.Time
}

// previewProxy is the token table + HTTP handler. One instance per
// App, created lazily by App.previewProxy().
type previewProxy struct {
	mu     sync.Mutex
	tokens map[string]*previewToken
}

func newPreviewProxy() *previewProxy {
	return &previewProxy{tokens: map[string]*previewToken{}}
}

// previewTokenTTL is how long a minted preview URL works.
const previewTokenTTL = 10 * time.Minute

// mint records a token for one file and returns its random hex.
func (p *previewProxy) mint(provider storage.Provider, limiter *transfer.Limiter, ref string, size int64, mime string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b[:])
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, t := range p.tokens {
		if t.expires.Before(now) {
			delete(p.tokens, k)
		}
	}
	p.tokens[tok] = &previewToken{
		provider: provider, limiter: limiter, ref: ref,
		size: size, mime: mime, expires: now.Add(previewTokenTTL),
	}
	return tok, nil
}

func (p *previewProxy) lookup(tok string) (*previewToken, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, ok := p.tokens[tok]
	if !ok {
		return nil, false
	}
	if time.Now().After(t.expires) {
		delete(p.tokens, tok)
		return nil, false
	}
	return t, true
}

// previewSchemeBase is the URL prefix PreviewURL mints for proxy
// previews. The scheme is registered in cmd/desktop/mygo.go.
const previewSchemeBase = "zydrive-preview://localhost/preview/"

// ServeHTTP streams one token's file. Only GET/HEAD; the path is
// /preview/<token> (the scheme host is "localhost", ignored).
func (p *previewProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tok := strings.TrimPrefix(r.URL.Path, "/preview/")
	if tok == "" || tok == r.URL.Path || strings.Contains(tok, "/") {
		http.Error(w, "preview: bad URL", http.StatusBadRequest)
		return
	}
	t, ok := p.lookup(tok)
	if !ok {
		http.Error(w, "preview: link expired or unknown — open the preview again", http.StatusGone)
		return
	}
	rc, err := t.provider.OpenRead(r.Context(), t.ref)
	if err != nil {
		http.Error(w, fmt.Sprintf("preview: cannot open file: %v", err), http.StatusBadGateway)
		return
	}
	defer rc.Close()

	start, end, ranged := parseRange(r.Header.Get("Range"), t.size)
	if ranged && (start < 0 || start >= t.size) {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", t.size))
		http.Error(w, "preview: range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	// Discard to the offset (slow seek — see the file header).
	// Not limiter-counted: see the header note.
	if start > 0 {
		if _, err := io.CopyN(io.Discard, rc, start); err != nil {
			http.Error(w, fmt.Sprintf("preview: seek failed: %v", err), http.StatusBadGateway)
			return
		}
	}
	body := io.Reader(rc)
	if t.limiter != nil {
		body = transfer.NewLimitedReader(rc, t.limiter)
	}
	length := t.size - start
	if ranged {
		length = end - start + 1
	}
	w.Header().Set("Content-Type", t.mime)
	w.Header().Set("Accept-Ranges", "bytes")
	if ranged {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, t.size))
		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	}
	if r.Method == http.MethodHead {
		return
	}
	// Always cap the copy at the announced length: for a closed
	// range (bytes=2-5) the stream must stop at byte 5, not run
	// to the end of the file.
	_, _ = io.Copy(w, io.LimitReader(body, length))
}

// parseRange parses "bytes=<start>-" / "bytes=<start>-<end>" for a
// file of size. It returns (start, end, ranged). A suffix range
// ("bytes=-500") or anything malformed returns ranged=false and
// the caller serves the whole file with 200.
func parseRange(h string, size int64) (start, end int64, ranged bool) {
	if !strings.HasPrefix(h, "bytes=") {
		return 0, size - 1, false
	}
	spec := strings.TrimPrefix(h, "bytes=")
	// Only a single range; multi-range is not served.
	if strings.Contains(spec, ",") {
		return 0, size - 1, false
	}
	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 || parts[0] == "" {
		return 0, size - 1, false
	}
	s, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil || s < 0 {
		return 0, size - 1, false
	}
	if strings.TrimSpace(parts[1]) == "" {
		return s, size - 1, true
	}
	e, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil || e < s {
		return 0, size - 1, false
	}
	if e >= size {
		e = size - 1
	}
	return s, e, true
}

// previewMime maps a file name to the Content-Type the preview
// proxy (and PreviewURL) reports. Text/code extensions are
// text/plain — the frontend renders them, it does not execute them.
// Unknown extensions are application/octet-stream: the browser
// offers a download instead of guessing.
func previewMime(name string) string {
	switch strings.ToLower(strings.TrimPrefix(path.Ext(name), ".")) {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "svg":
		return "image/svg+xml"
	case "bmp":
		return "image/bmp"
	case "ico":
		return "image/x-icon"
	case "avif":
		return "image/avif"
	case "mp4":
		return "video/mp4"
	case "webm":
		return "video/webm"
	case "mov":
		return "video/quicktime"
	case "m4v":
		return "video/x-m4v"
	case "mp3":
		return "audio/mpeg"
	case "m4a":
		return "audio/mp4"
	case "wav":
		return "audio/wav"
	case "ogg":
		return "audio/ogg"
	case "flac":
		return "audio/flac"
	case "aac":
		return "audio/aac"
	case "pdf":
		return "application/pdf"
	case "md", "markdown":
		return "text/markdown"
	case "txt", "log", "csv", "json", "xml", "yaml", "yml", "ini", "cfg", "conf", "toml",
		"go", "js", "ts", "jsx", "tsx", "py", "rb", "rs", "java", "c", "h", "cpp", "hpp",
		"cs", "sh", "bash", "sql", "html", "css", "scss":
		return "text/plain"
	default:
		return "application/octet-stream"
	}
}

// ensure previewProxy implements http.Handler.
var _ http.Handler = (*previewProxy)(nil)
