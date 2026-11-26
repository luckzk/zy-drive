// SPDX-License-Identifier: MPL-2.0
// Package pcloud implements storage.Provider for pCloud using the
// official pCloud API over plain HTTP (no third-party SDK).
//
// pCloud has TWO data centers: US (api.pcloud.com, authorize at
// my.pcloud.com) and EU (eapi.pcloud.com, authorize at
// e.pcloud.com). The wizard lets the user pick 自动 / 美国 / 欧洲
// (see docs/oauth-apps/pcloud.md): a fixed choice pins the host;
// 自动 authorizes on the US endpoint and the driver then probes
// both hosts with userinfo, keeping the one that answers — an EU
// account whose US token exchange fails gets a plain message
// telling the user to pick 欧洲 explicitly. The API host is
// stored per mount (Extra["pcloud_host"]) so the probe runs once.
//
// Auth is a long-lived Bearer access token (the token endpoint
// returns no refresh token and no expiry — a zero Expiry means
// "never expires" in the token source), so there is no refresher:
// an expired or revoked token is fatal and the user must
// re-authorise. The token endpoint needs a client secret at
// exchange time, so like Box there is no built-in one-click app:
// the wizard uses the user's own app (Client ID + Secret).
//   - List: GET /listfolder?folderid=N.
//   - Upload: POST /uploadfile?folderid=N&nopartial=1
//     (multipart/form-data, one "file" field). Small files are
//     buffered in memory; large files stream with an exact
//     Content-Length (the multipart framing is built by hand, so
//     the length is known — no chunked encoding, no second
//     download path).
//   - Download: GET /getfilelink returns a direct URL (the
//     driver implements DirectURL for previews); OpenRead streams
//     that URL. Range requests work on the direct link.
//   - Quota: GET /userinfo (usedquota / quota).
//   - pCloud is ID-based: folderid/fileid are integers, refs are
//     display paths ("a/b/c.jpg", "" is the root, folderid 0),
//     resolved to ids through a path -> id cache, like Box.
//   - Delete moves to trash (pCloud's default).
//   - Search: GET /search (name + content; the driver keeps only
//     name matches).
//   - API errors are {"result": N, "error": "..."} with result 0
//     meaning success; the driver turns any other result into a
//     plain-language error.
package pcloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/netproxy"
	"github.com/luckzk/zy-drive/internal/storage"
	shttp "github.com/luckzk/zy-drive/internal/storage/http"
	"github.com/luckzk/zy-drive/internal/storage/oauth"
)

const (
	hostUS = "api.pcloud.com"
	hostEU = "eapi.pcloud.com"
	// singleUploadLimit is the in-memory upload ceiling (20 MiB):
	// larger files stream with an exact Content-Length instead.
	singleUploadLimit = 20 << 20
)

// Provider is a pCloud-backed storage.Provider. One instance per
// Mount (never a per-type singleton): each account has its own
// token and its own http client, so two pCloud accounts can never
// share state.
type Provider struct {
	host   string // "" = auto (probe on first use); else api.pcloud.com / eapi.pcloud.com
	tokens *oauth.TokenSource
	client *shttp.Client

	mu     sync.Mutex
	idMap  map[string]int64 // display path ("/a/b") -> folderid/fileid
	dirMap map[string]bool  // display path -> is a folder

	hostMu       sync.Mutex
	hostResolved string // the host the probe settled on ("" while unresolved)
	probeHosts   string // test-only override for the probe list (comma-separated base URLs)
}

// New builds a pCloud provider. cfg.Token is the OAuth access token
// (memory only, long-lived). cfg.Extra["pcloud_host"] pins the API
// host ("" or "auto" = probe US then EU on first use).
// cfg.Extra["api_base"] overrides the whole base URL (tests;
// normally empty).
func New(cfg storage.Config) (storage.Provider, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("pcloud: access token is required")
	}
	host := strings.TrimSpace(cfg.Extra["pcloud_host"])
	if host == "auto" {
		host = ""
	}
	if v := cfg.Extra["api_base"]; v != "" {
		host = v
	}
	ts := oauth.NewTokenSource(&oauth.Token{AccessToken: cfg.Token}, nil)
	p := &Provider{
		host:       host,
		tokens:     ts,
		idMap:      map[string]int64{},
		dirMap:     map[string]bool{},
		probeHosts: strings.TrimSpace(cfg.Extra["probe_hosts"]),
	}
	p.client = shttp.New(nil)
	// The Authorization header is attached by the transport on
	// EVERY attempt (see the Drime driver): the token never
	// changes here, but the pattern keeps every driver alike.
	p.client.HTTP = &http.Client{
		Timeout:   120 * time.Second,
		Transport: authTransport{base: netproxy.Transport(), tokens: ts},
	}
	return p, nil
}

// authTransport attaches the Bearer token on every attempt.
type authTransport struct {
	base   http.RoundTripper
	tokens *oauth.TokenSource
}

func (t authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := t.tokens.Token(req.Context())
	if err != nil {
		return nil, err
	}
	req2 := req.Clone(req.Context())
	req2.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(req2)
}

// Caps declares pCloud capabilities.
func (p *Provider) Caps() domain.Caps {
	return domain.Caps{
		CanMkdir: true, CanRename: true, CanDelete: true, DeleteToTrash: true,
		SupportsServerSideCopy:  false,
		SupportsVersions:        false,
		SupportsShare:           true,
		SupportsSearch:          true,
		SupportsDelta:           false,
		SupportsThumbnail:       false,
		SupportsResumableUpload: false,
		SupportsEncryption:      false,
		SupportsStorageClass:    false,
		SupportsPresignedURL:    false, // getfilelink is the direct URL instead
		IDBased:                 true,
		MaxFileSize:             0, // no documented limit
		MinPartSize:             0,
		MaxPathLen:              255,
	}
}

// --- wire types ---

// pcloudError is the error shape of every pCloud response:
// {"result": 0, "error": "..."}. result 0 is success.
type pcloudError struct {
	Result int    `json:"result"`
	Error  string `json:"error"`
}

func (e *pcloudError) err(op string) error {
	if e.Result == 0 {
		return nil
	}
	msg := e.Error
	if msg == "" {
		msg = fmt.Sprintf("pcloud error %d", e.Result)
	}
	return fmt.Errorf("pcloud api %d: %s", e.Result, msg)
}

type pcloudMeta struct {
	Name     string       `json:"name"`
	IsFolder bool         `json:"isfolder"`
	FileID   int64        `json:"fileid"`
	FolderID int64        `json:"folderid"`
	Size     int64        `json:"size"`
	Modified string       `json:"modified"`
	Content  []pcloudMeta `json:"contents"`
}

func (m pcloudMeta) id() int64 {
	if m.IsFolder {
		return m.FolderID
	}
	return m.FileID
}

func (m pcloudMeta) node(ref string) domain.Node {
	n := domain.Node{
		Ref:   ref,
		Path:  "/" + strings.Trim(ref, "/"),
		Name:  m.Name,
		IsDir: m.IsFolder,
		Size:  -1,
		Mime:  mimeOf(m.Name),
		Extra: map[string]string{},
	}
	if !m.IsFolder {
		n.Size = m.Size
	}
	if m.id() != 0 {
		n.Extra["pcloudId"] = strconv.FormatInt(m.id(), 10)
	}
	if t := parseModified(m.Modified); !t.IsZero() {
		n.ModTime = t
	}
	if ref == "" {
		n.Path = "/"
		n.Name = ""
	}
	return n
}

// parseModified parses pCloud's date strings. pCloud returns
// several shapes across endpoints (RFC 3339 and the RFC 1123
// form "Mon, 02 Jan 2006 15:04:05 -0700"); an unparsable value
// leaves ModTime zero rather than a fabricated date.
func parseModified(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339,
		"Mon, 02 Jan 2006 15:04:05 -0700",
		time.RFC1123Z,
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// --- request helpers ---

// apiBase returns the API base URL, resolving the host on first
// use when the mount is on "auto": userinfo is tried on the US
// host first, then the EU host, and the winner is remembered for
// the life of this provider. A pinned host is used as-is.
// cfg.Extra["probe_hosts"] (comma-separated base URLs) overrides
// the probe list — tests only.
func (p *Provider) apiBase(ctx context.Context) (string, error) {
	if p.host != "" {
		if strings.HasPrefix(p.host, "http://") || strings.HasPrefix(p.host, "https://") {
			return strings.TrimSuffix(p.host, "/"), nil
		}
		return "https://" + p.host, nil
	}
	p.hostMu.Lock()
	defer p.hostMu.Unlock()
	if p.hostResolved != "" {
		return p.hostResolved, nil
	}
	// Probe: userinfo on US, then EU. The first host that answers
	// with result 0 wins.
	hosts := []string{"https://" + hostUS, "https://" + hostEU}
	if v := p.probeHosts; v != "" {
		hosts = strings.Split(v, ",")
	}
	for _, base := range hosts {
		if _, err := p.userinfoWithBase(ctx, base); err == nil {
			p.hostResolved = base
			return base, nil
		}
	}
	return "", p.wrap("List", fmt.Errorf("pcloud: 两个数据中心（美国 / 欧洲）都连不上，请检查网络或在添加向导里手动选一个区"))
}

func (p *Provider) userinfoWithBase(ctx context.Context, base string) (int64, error) {
	var out struct {
		pcloudError
		Quota     int64 `json:"quota"`
		UsedQuota int64 `json:"usedquota"`
	}
	if err := p.doJSONWithBase(ctx, "Quota", base+"/userinfo", nil, &out); err != nil {
		return 0, err
	}
	if err := out.err("Quota"); err != nil {
		return 0, p.wrap("Quota", err)
	}
	return out.Quota, nil
}

// doJSON calls a pCloud endpoint on the resolved host and decodes
// the response into out. The caller checks out's result field.
func (p *Provider) doJSON(ctx context.Context, op, endpoint string, arg any, out any) error {
	base, err := p.apiBase(ctx)
	if err != nil {
		return err
	}
	return p.doJSONWithBase(ctx, op, base+endpoint, arg, out)
}

// doJSONWithBase is doJSON against an explicit base URL (used by
// the host probe, which cannot call apiBase yet).
func (p *Provider) doJSONWithBase(ctx context.Context, op, url string, arg any, out any) error {
	var body io.Reader
	if arg != nil {
		buf, err := json.Marshal(arg)
		if err != nil {
			return p.wrap(op, err)
		}
		body = bytes.NewReader(buf)
	}
	req, err := p.newRequest(ctx, http.MethodGet, url, body)
	if err != nil {
		return p.wrap(op, err)
	}
	if arg != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(ctx, req)
	if err != nil {
		return p.wrap(op, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return p.wrap(op, fmt.Errorf("pcloud http %d: %s", resp.StatusCode, strings.TrimSpace(string(data))))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return p.wrap(op, err)
		}
	}
	return nil
}

func (p *Provider) newRequest(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
	var bodyBytes []byte
	var err error
	if body != nil {
		bodyBytes, err = io.ReadAll(body)
		if err != nil {
			return nil, err
		}
	}
	var rdr io.Reader
	if bodyBytes != nil {
		rdr = bytes.NewReader(bodyBytes)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, err
	}
	if bodyBytes != nil {
		b := bodyBytes
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
	} else {
		req.GetBody = func() (io.ReadCloser, error) { return http.NoBody, nil }
	}
	return req, nil
}

func (p *Provider) wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	return &storage.ProviderError{Op: op, Provider: "pcloud", Err: err}
}

// --- path <-> ID ---

func cleanRef(ref string) string { return "/" + strings.Trim(ref, "/") }

func splitRef(ref string) (parent, name string) {
	ref = strings.Trim(ref, "/")
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return "", ref
}

// resolve returns the folderid/fileid and folder flag for a
// display path, walking from the root with per-folder listings.
// "" and "/" are the root (id 0). Results are cached in
// idMap/dirMap; Rename and Delete invalidate the affected paths.
func (p *Provider) resolve(ctx context.Context, ref string) (int64, bool, error) {
	ref = strings.Trim(ref, "/")
	if ref == "" {
		return 0, true, nil
	}
	p.mu.Lock()
	if id, ok := p.idMap[cleanRef(ref)]; ok {
		isDir := p.dirMap[cleanRef(ref)]
		p.mu.Unlock()
		return id, isDir, nil
	}
	p.mu.Unlock()
	parts := strings.Split(ref, "/")
	parentID := int64(0)
	var parentRef string
	for i, name := range parts {
		items, err := p.listChildren(ctx, parentID)
		if err != nil {
			return 0, false, err
		}
		var found *pcloudMeta
		for _, it := range items {
			if it.Name == name {
				c := it
				found = &c
				break
			}
		}
		if found == nil {
			return 0, false, p.wrap("List", fmt.Errorf("pcloud: no such file or folder: %q", ref))
		}
		curRef := parentRef + "/" + name
		if i == len(parts)-1 {
			p.mu.Lock()
			p.idMap[cleanRef(curRef)] = found.id()
			p.dirMap[cleanRef(curRef)] = found.IsFolder
			p.mu.Unlock()
			return found.id(), found.IsFolder, nil
		}
		if !found.IsFolder {
			return 0, false, p.wrap("List", fmt.Errorf("pcloud: %q is not a folder", curRef))
		}
		parentID = found.id()
		parentRef = curRef
	}
	return 0, false, p.wrap("List", fmt.Errorf("pcloud: no such file or folder: %q", ref))
}

func (p *Provider) invalidate(refs ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ref := range refs {
		ref = cleanRef(ref)
		for k := range p.idMap {
			if k == ref || strings.HasPrefix(k, ref+"/") {
				delete(p.idMap, k)
				delete(p.dirMap, k)
			}
		}
	}
}

func (p *Provider) listChildren(ctx context.Context, parentID int64) ([]pcloudMeta, error) {
	var out struct {
		pcloudError
		Metadata pcloudMeta `json:"metadata"`
	}
	u := fmt.Sprintf("/listfolder?folderid=%d", parentID)
	if err := p.doJSON(ctx, "List", u, nil, &out); err != nil {
		return nil, err
	}
	if err := out.err("List"); err != nil {
		return nil, p.wrap("List", err)
	}
	return out.Metadata.Content, nil
}

// --- Provider interface ---

// List returns the children of a folder. "" (or "/") is the root.
func (p *Provider) List(ctx context.Context, ref string) ([]domain.Node, error) {
	id, isDir, err := p.resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	if !isDir {
		return nil, p.wrap("List", fmt.Errorf("pcloud: %q is not a folder", ref))
	}
	items, err := p.listChildren(ctx, id)
	if err != nil {
		return nil, err
	}
	base := strings.Trim(ref, "/")
	var out []domain.Node
	for _, it := range items {
		child := it.Name
		if base != "" {
			child = base + "/" + child
		}
		out = append(out, it.node(child))
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
	id, _, err := p.resolve(ctx, ref)
	if err != nil {
		return domain.Node{}, err
	}
	var out struct {
		pcloudError
		Metadata pcloudMeta `json:"metadata"`
	}
	u := fmt.Sprintf("/stat?fileid=%d", id)
	if err := p.doJSON(ctx, "Stat", u, nil, &out); err != nil {
		return domain.Node{}, err
	}
	if err := out.err("Stat"); err != nil {
		return domain.Node{}, p.wrap("Stat", err)
	}
	return out.Metadata.node(ref), nil
}

// OpenRead streams a file. pCloud has no direct content endpoint:
// getfilelink returns a direct URL, and OpenRead streams that URL
// with the same proxy-aware client. The returned body must be
// closed by the caller.
func (p *Provider) OpenRead(ctx context.Context, ref string) (io.ReadCloser, error) {
	link, err := p.directURL(ctx, ref)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, p.wrap("OpenRead", err)
	}
	resp, err := p.client.HTTP.Do(req)
	if err != nil {
		return nil, p.wrap("OpenRead", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return nil, p.wrap("OpenRead", fmt.Errorf("pcloud download http %d", resp.StatusCode))
	}
	return resp.Body, nil
}

// directURL resolves a ref to a getfilelink direct URL (the URL
// is temporary; callers must not store it).
func (p *Provider) directURL(ctx context.Context, ref string) (string, error) {
	id, isDir, err := p.resolve(ctx, ref)
	if err != nil {
		return "", err
	}
	if isDir {
		return "", p.wrap("OpenRead", fmt.Errorf("pcloud: %q is a folder", ref))
	}
	var out struct {
		pcloudError
		Hosts []string `json:"hosts"`
		Path  string   `json:"path"`
	}
	u := fmt.Sprintf("/getfilelink?fileid=%d", id)
	if err := p.doJSON(ctx, "OpenRead", u, nil, &out); err != nil {
		return "", err
	}
	if err := out.err("OpenRead"); err != nil {
		return "", p.wrap("OpenRead", err)
	}
	if len(out.Hosts) == 0 || out.Path == "" {
		return "", p.wrap("OpenRead", fmt.Errorf("pcloud: getfilelink returned no link"))
	}
	// Production hosts are bare hostnames ("e2.pcloud.com"); a
	// value that already carries a scheme is used as-is — that is
	// how the mock points the link at its plain-HTTP test server.
	host := out.Hosts[0]
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		return host + out.Path, nil
	}
	return "https://" + host + out.Path, nil
}

// DirectURL implements storage.DirectURLer: the pCloud direct
// link supports Range requests, so previews can stream it
// without downloading the file first.
func (p *Provider) DirectURL(ctx context.Context, ref string) (string, error) {
	return p.directURL(ctx, ref)
}

// Upload writes a file. Small files are buffered in memory;
// larger files stream with an exact Content-Length (multipart
// framing built by hand, so the length is known — no chunked
// encoding, no spool for local files). nopartial=1 means a failed
// upload leaves no partial file behind.
func (p *Provider) Upload(ctx context.Context, ref string, r io.Reader, size int64) (domain.Node, error) {
	parent, name := splitRef(ref)
	parentID, _, err := p.resolve(ctx, parent)
	if err != nil {
		return domain.Node{}, err
	}
	u, err := p.apiBase(ctx)
	if err != nil {
		return domain.Node{}, err
	}
	uploadURL := fmt.Sprintf("%s/uploadfile?folderid=%d&nopartial=1", u, parentID)

	var node domain.Node
	if size >= 0 && size <= singleUploadLimit {
		// readErr is deliberately separate from err: using err
		// here would shadow the outer err, and the error from
		// uploadMultipart below would be silently dropped.
		data, readErr := io.ReadAll(r)
		if readErr != nil {
			return domain.Node{}, p.wrap("Upload", readErr)
		}
		if int64(len(data)) != size {
			return domain.Node{}, p.wrap("Upload", fmt.Errorf("pcloud: read %d bytes, expected %d", len(data), size))
		}
		node, err = p.uploadMultipart(ctx, uploadURL, name, bytes.NewReader(data), size)
	} else if rs, ok := r.(io.ReadSeeker); ok && size >= 0 {
		// Seekable source (a local file): stream it with an
		// exact Content-Length — no memory, no spool.
		node, err = p.uploadMultipart(ctx, uploadURL, name, rs, size)
	} else {
		// Unknown size, or a non-seekable stream (cross-cloud
		// copy): spool to a temp file so the multipart length
		// is exact, then stream the spool file.
		node, err = p.uploadSpooled(ctx, uploadURL, name, r, size)
	}
	if err != nil {
		return domain.Node{}, err
	}
	p.invalidate(parent)
	// The upload response only carries the file's own metadata;
	// the full ref (with the parent path) is rebuilt here.
	node.Ref = ref
	node.Path = "/" + strings.Trim(ref, "/")
	return node, nil
}

// multipartBody builds a single-part multipart/form-data body
// (field "file") with an exact Content-Length, so the upload can
// stream a large file without chunked encoding and without
// loading it into memory. The boundary and Content-Type are
// fixed by hand (Go's multipart.Writer would not let us know the
// length up front).
func multipartBody(name string, content io.Reader, size int64) (body io.Reader, contentLength int64, contentType string) {
	const boundary = "zy-drive-pcloud-upload"
	header := fmt.Sprintf("--%s\r\nContent-Disposition: form-data; name=\"file\"; filename=\"%s\"\r\nContent-Type: application/octet-stream\r\n\r\n",
		boundary, quoteFilename(name))
	footer := "\r\n--" + boundary + "--\r\n"
	contentType = "multipart/form-data; boundary=" + boundary
	return io.MultiReader(strings.NewReader(header), content, strings.NewReader(footer)),
		int64(len(header)) + size + int64(len(footer)), contentType
}

// quoteFilename escapes a filename for a Content-Disposition
// header (quotes and backslashes only; the API rejects other
// control characters anyway).
func quoteFilename(name string) string {
	return strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(name)
}

func (p *Provider) uploadMultipart(ctx context.Context, uploadURL, name string, content io.Reader, size int64) (domain.Node, error) {
	body, length, contentType := multipartBody(name, content, size)
	var out struct {
		pcloudError
		Metadata []pcloudMeta `json:"metadata"`
	}
	// The body is not replayable (a stream): the client must not
	// retry it on 5xx. The single-request helper retries only
	// when GetBody is set, so build the request by hand.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, body)
	if err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	req.ContentLength = length
	req.Header.Set("Content-Type", contentType)
	resp, err := p.client.HTTP.Do(req)
	if err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return domain.Node{}, p.wrap("Upload", fmt.Errorf("pcloud upload http %d: %s", resp.StatusCode, strings.TrimSpace(string(data))))
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	if err := out.err("Upload"); err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	if len(out.Metadata) == 0 {
		return domain.Node{}, p.wrap("Upload", fmt.Errorf("pcloud: upload returned no file"))
	}
	return out.Metadata[0].node(name), nil
}

// uploadSpooled spools an unknown-size or non-seekable upload to
// a temp file, then streams it with an exact Content-Length.
func (p *Provider) uploadSpooled(ctx context.Context, uploadURL, name string, r io.Reader, size int64) (domain.Node, error) {
	tmp, err := os.CreateTemp("", "zy-drive-pcloud-upload-*")
	if err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		return domain.Node{}, p.wrap("Upload", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		_ = tmp.Close()
		return domain.Node{}, p.wrap("Upload", err)
	}
	st, err := tmp.Stat()
	if err != nil {
		_ = tmp.Close()
		return domain.Node{}, p.wrap("Upload", err)
	}
	if size >= 0 && st.Size() != size {
		_ = tmp.Close()
		return domain.Node{}, p.wrap("Upload", fmt.Errorf("pcloud: read %d bytes, expected %d", st.Size(), size))
	}
	node, err := p.uploadMultipart(ctx, uploadURL, name, tmp, st.Size())
	_ = tmp.Close()
	return node, err
}

// Mkdir creates a folder. An existing folder is not an error.
func (p *Provider) Mkdir(ctx context.Context, ref string) error {
	ref = strings.Trim(ref, "/")
	if ref == "" {
		return nil
	}
	parent, name := splitRef(ref)
	parentID, _, err := p.resolve(ctx, parent)
	if err != nil {
		return err
	}
	var out struct {
		pcloudError
		Metadata pcloudMeta `json:"metadata"`
	}
	u := fmt.Sprintf("/createfolder?folderid=%d&name=%s", parentID, url.QueryEscape(name))
	if err := p.doJSON(ctx, "Mkdir", u, nil, &out); err != nil {
		return err
	}
	if err := out.err("Mkdir"); err != nil {
		// 2004 is pCloud's "already exists" — re-resolve to
		// confirm it really is there, then succeed.
		if out.Result == 2004 {
			if _, isDir, rerr := p.resolve(ctx, ref); rerr == nil && isDir {
				return nil
			}
		}
		return p.wrap("Mkdir", err)
	}
	p.invalidate(parent)
	return nil
}

// Rename moves and/or renames a file or folder.
func (p *Provider) Rename(ctx context.Context, oldRef, newRef string) error {
	oldRef = strings.Trim(oldRef, "/")
	newRef = strings.Trim(newRef, "/")
	if oldRef == "" || newRef == "" || oldRef == newRef {
		return p.wrap("Rename", fmt.Errorf("pcloud: invalid rename %q -> %q", oldRef, newRef))
	}
	id, isDir, err := p.resolve(ctx, oldRef)
	if err != nil {
		return err
	}
	newParent, newName := splitRef(newRef)
	if _, _, err := p.resolve(ctx, newParent); err != nil {
		return err
	}
	var out struct{ pcloudError }
	if isDir {
		u := fmt.Sprintf("/renamefolder?folderid=%d&toname=%s", id, url.QueryEscape(newName))
		if err := p.doJSON(ctx, "Rename", u, nil, &out); err != nil {
			return err
		}
	} else {
		u := fmt.Sprintf("/renamefile?fileid=%d&toname=%s", id, url.QueryEscape(newName))
		if err := p.doJSON(ctx, "Rename", u, nil, &out); err != nil {
			return err
		}
	}
	if err := out.err("Rename"); err != nil {
		return p.wrap("Rename", err)
	}
	oldParent, _ := splitRef(oldRef)
	if newParent != oldParent {
		newParentID, _, err := p.resolve(ctx, newParent)
		if err != nil {
			return err
		}
		var mout struct{ pcloudError }
		var mu string
		if isDir {
			mu = fmt.Sprintf("/movefolder?folderid=%d&tofolderid=%d", id, newParentID)
		} else {
			mu = fmt.Sprintf("/movefile?fileid=%d&tofolderid=%d", id, newParentID)
		}
		if err := p.doJSON(ctx, "Rename", mu, nil, &mout); err != nil {
			return err
		}
		if err := mout.err("Rename"); err != nil {
			return p.wrap("Rename", err)
		}
	}
	p.invalidate(oldParent, newParent)
	return nil
}

// Delete moves a file or folder to trash (pCloud's default).
func (p *Provider) Delete(ctx context.Context, ref string) error {
	ref = strings.Trim(ref, "/")
	if ref == "" {
		return p.wrap("Delete", fmt.Errorf("pcloud: cannot delete the root"))
	}
	id, isDir, err := p.resolve(ctx, ref)
	if err != nil {
		return err
	}
	var out struct{ pcloudError }
	var u string
	if isDir {
		u = fmt.Sprintf("/deletefolder?folderid=%d", id)
	} else {
		u = fmt.Sprintf("/deletefile?fileid=%d", id)
	}
	if err := p.doJSON(ctx, "Delete", u, nil, &out); err != nil {
		return err
	}
	if err := out.err("Delete"); err != nil {
		return p.wrap("Delete", err)
	}
	parent, _ := splitRef(ref)
	p.invalidate(parent)
	return nil
}

// Search finds files by name (pCloud also matches content; the
// driver keeps only name matches, like the Box driver).
func (p *Provider) Search(ctx context.Context, query string) ([]domain.Node, error) {
	var out struct {
		pcloudError
		Entries []struct {
			pcloudMeta
			Path string `json:"path"`
		} `json:"entries"`
	}
	u := "/search?query=" + url.QueryEscape(query)
	if err := p.doJSON(ctx, "Search", u, nil, &out); err != nil {
		return nil, err
	}
	if err := out.err("Search"); err != nil {
		return nil, p.wrap("Search", err)
	}
	var nodes []domain.Node
	q := strings.ToLower(query)
	for _, e := range out.Entries {
		if e.IsFolder {
			continue
		}
		if !strings.Contains(strings.ToLower(e.Name), q) {
			continue
		}
		ref := strings.Trim(e.Path, "/")
		nodes = append(nodes, e.node(ref))
	}
	return nodes, nil
}

// Quota returns (used, total) in bytes from userinfo.
func (p *Provider) Quota(ctx context.Context) (used, total int64, err error) {
	var out struct {
		pcloudError
		Quota     int64 `json:"quota"`
		UsedQuota int64 `json:"usedquota"`
	}
	if err := p.doJSON(ctx, "Quota", "/userinfo", nil, &out); err != nil {
		return 0, 0, err
	}
	if err := out.err("Quota"); err != nil {
		return 0, 0, p.wrap("Quota", err)
	}
	return out.UsedQuota, out.Quota, nil
}

// ShareLink returns a public link for a file.
func (p *Provider) ShareLink(ctx context.Context, ref string) (string, error) {
	id, isDir, err := p.resolve(ctx, ref)
	if err != nil {
		return "", err
	}
	if isDir {
		return "", p.wrap("Share", fmt.Errorf("pcloud: cannot share a folder"))
	}
	var out struct {
		pcloudError
		Link string `json:"link"`
	}
	u := fmt.Sprintf("/getfilepublink?fileid=%d", id)
	if err := p.doJSON(ctx, "Share", u, nil, &out); err != nil {
		return "", err
	}
	if err := out.err("Share"); err != nil {
		return "", p.wrap("Share", err)
	}
	if out.Link == "" {
		return "", p.wrap("Share", fmt.Errorf("pcloud: getfilepublink returned no link"))
	}
	return out.Link, nil
}

// mimeOf guesses a MIME type from the extension (pCloud's
// metadata does not carry one).
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
