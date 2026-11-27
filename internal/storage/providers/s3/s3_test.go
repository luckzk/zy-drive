// SPDX-License-Identifier: MPL-2.0
package s3

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckzk/zy-drive/internal/storage"
)

// TestS3HasNoQuota pins the honest absence: S3 has no quota API,
// so the driver must NOT implement storage.QuotaSource — the App
// layer then reports the mount as unlimited ("不限量") instead of
// the driver inventing a number.
func TestS3HasNoQuota(t *testing.T) {
	p, err := New(storage.Config{Extra: map[string]string{"bucket": "test-bucket"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := any(p).(storage.QuotaSource); ok {
		t.Fatal("s3 provider must NOT implement storage.QuotaSource (no quota API exists)")
	}
}

// TestS3DirectURL pins the presigned GET: minting is purely local
// signing (no network), the URL carries the bucket/key and a
// signature, and the provider advertises storage.DirectURLer.
func TestS3DirectURL(t *testing.T) {
	p, err := New(storage.Config{
		AccessKey: "test-access",
		SecretKey: "test-secret",
		Extra:     map[string]string{"bucket": "test-bucket"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	du, ok := any(p).(storage.DirectURLer)
	if !ok {
		t.Fatal("s3 provider must implement storage.DirectURLer")
	}
	u, err := du.DirectURL(context.Background(), "hello.txt")
	if err != nil {
		t.Fatalf("DirectURL: %v", err)
	}
	if !strings.Contains(u, "hello.txt") {
		t.Fatalf("DirectURL %q does not contain the object key", u)
	}
	if !strings.Contains(u, "X-Amz-Signature=") {
		t.Fatalf("DirectURL %q is not a signed URL (no X-Amz-Signature)", u)
	}
	if !strings.Contains(u, "test-bucket") {
		t.Fatalf("DirectURL %q does not name the bucket", u)
	}
}

// --- fake S3 server for upload tests ---
//
// The transfer manager talks plain S3 REST: PutObject for small
// files, CreateMultipartUpload / UploadPart / CompleteMultipartUpload
// for large ones. The fake answers just enough for the SDK to
// complete both paths, and records what was stored so the test can
// verify the round-trip.
type fakeVersion struct {
	id           string
	data         []byte
	lastModified time.Time
}

type fakeS3 struct {
	mu            sync.Mutex
	objects       map[string][]byte
	mtimes        map[string]time.Time
	uploads       map[string]map[int][]byte
	nextUpload    int
	multipartUsed bool
	// Versioning: "Enabled" (default) or "Suspended" — the
	// driver returns an empty version list when it is not
	// Enabled, which TestS3VersionsDisabled pins.
	versioning  string
	versions    map[string][]fakeVersion
	nextVersion int
}

func newFakeS3() *fakeS3 {
	return &fakeS3{objects: map[string][]byte{}, mtimes: map[string]time.Time{}, uploads: map[string]map[int][]byte{},
		versioning: "Enabled", versions: map[string][]fakeVersion{}}
}

// putVersionLocked records a new version of key (and makes it the
// latest object). Caller holds f.mu.
func (f *fakeS3) putVersionLocked(key string, data []byte) {
	f.nextVersion++
	f.versions[key] = append(f.versions[key], fakeVersion{
		id: fmt.Sprintf("v%d", f.nextVersion), data: data, lastModified: time.Now().UTC(),
	})
	f.objects[key] = data
	f.mtimes[key] = time.Now().UTC()
}

func xmlEsc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/test-bucket/")
	q := r.URL.Query()
	switch {
	case r.Method == http.MethodPut && q.Has("partNumber"):
		// UploadPart
		part, _ := strconv.Atoi(q.Get("partNumber"))
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		if _, ok := f.uploads[q.Get("uploadId")]; !ok {
			f.uploads[q.Get("uploadId")] = map[int][]byte{}
		}
		f.uploads[q.Get("uploadId")][part] = body
		f.mu.Unlock()
		w.Header().Set("ETag", fmt.Sprintf(`"part-%d"`, part))
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodPut && r.Header.Get("X-Amz-Copy-Source") != "":
		// CopyObject: "bucket/key" -> new version at the PUT key.
		src := strings.TrimPrefix(r.Header.Get("X-Amz-Copy-Source"), "/")
		if i := strings.Index(src, "/"); i >= 0 {
			src = src[i+1:]
		}
		f.mu.Lock()
		data, ok := f.objects[src]
		if ok {
			f.putVersionLocked(key, data)
		}
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><CopyObjectResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><LastModified>%s</LastModified><ETag>"copy"</ETag></CopyObjectResult>`, time.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
	case r.Method == http.MethodPut:
		// PutObject (single-part)
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.putVersionLocked(key, body)
		f.mu.Unlock()
		w.Header().Set("ETag", `"single"`)
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodPost && q.Has("uploads"):
		// CreateMultipartUpload
		f.mu.Lock()
		f.nextUpload++
		id := fmt.Sprintf("up-%d", f.nextUpload)
		f.uploads[id] = map[int][]byte{}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><InitiateMultipartUploadResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Bucket>test-bucket</Bucket><Key>%s</Key><UploadId>%s</UploadId></InitiateMultipartUploadResult>`, key, id)
	case r.Method == http.MethodPost && q.Has("uploadId"):
		// CompleteMultipartUpload: assemble parts in order.
		id := q.Get("uploadId")
		f.mu.Lock()
		parts := f.uploads[id]
		var data []byte
		for i := 1; ; i++ {
			part, ok := parts[i]
			if !ok {
				break
			}
			data = append(data, part...)
		}
		f.putVersionLocked(key, data)
		delete(f.uploads, id)
		f.multipartUsed = true
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><CompleteMultipartUploadResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Location>/test-bucket/%s</Location><Bucket>test-bucket</Bucket><Key>%s</Key><ETag>"complete"</ETag></CompleteMultipartUploadResult>`, key, key)
	case r.Method == http.MethodDelete && q.Has("uploadId"):
		// AbortMultipartUpload
		f.mu.Lock()
		delete(f.uploads, q.Get("uploadId"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete:
		// DeleteObject: S3 succeeds even for a missing key.
		f.mu.Lock()
		delete(f.objects, key)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodHead:
		f.mu.Lock()
		data, ok := f.objects[key]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Header().Set("ETag", `"fake-etag"`)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet && q.Has("versioning"):
		// GetBucketVersioning
		f.mu.Lock()
		status := f.versioning
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>%s</Status></VersioningConfiguration>`, status)
	case r.Method == http.MethodGet && q.Has("versions"):
		// ListObjectVersions, filtered by the requested prefix.
		prefix := q.Get("prefix")
		f.mu.Lock()
		var sb strings.Builder
		sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListVersionsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>test-bucket</Name><IsTruncated>false</IsTruncated>`)
		for k, vs := range f.versions {
			if !strings.HasPrefix(k, prefix) {
				continue
			}
			for i, v := range vs {
				fmt.Fprintf(&sb, `<Version><Key>%s</Key><VersionId>%s</VersionId><IsLatest>%v</IsLatest><LastModified>%s</LastModified><ETag>"etag-%s"</ETag><Size>%d</Size><StorageClass>STANDARD</StorageClass></Version>`,
					k, v.id, i == len(vs)-1, v.lastModified.Format("2006-01-02T15:04:05.000Z"), v.id, len(v.data))
			}
		}
		sb.WriteString(`</ListVersionsResult>`)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(sb.String()))
	case r.Method == http.MethodGet && q.Get("list-type") == "2":
		// ListObjectsV2: prefix + delimiter "/". Keys under the
		// prefix with a "/" in the remainder collapse into
		// CommonPrefixes; the rest are Contents.
		prefix := q.Get("prefix")
		delim := q.Get("delimiter")
		f.mu.Lock()
		keys := make([]string, 0, len(f.objects))
		for k := range f.objects {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		type content struct {
			key  string
			size int
			mod  time.Time
		}
		var contents []content
		prefixes := map[string]bool{}
		for _, k := range keys {
			if !strings.HasPrefix(k, prefix) {
				continue
			}
			rest := strings.TrimPrefix(k, prefix)
			if delim != "" {
				if i := strings.Index(rest, delim); i >= 0 {
					prefixes[prefix+rest[:i+1]] = true
					continue
				}
			}
			contents = append(contents, content{key: k, size: len(f.objects[k]), mod: f.mtimes[k]})
		}
		f.mu.Unlock()
		var sb strings.Builder
		sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>test-bucket</Name>`)
		fmt.Fprintf(&sb, `<Prefix>%s</Prefix><Delimiter>%s</Delimiter><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>`, xmlEsc(prefix), xmlEsc(delim))
		for _, c := range contents {
			fmt.Fprintf(&sb, `<Contents><Key>%s</Key><LastModified>%s</LastModified><ETag>"fake-etag"</ETag><Size>%d</Size><StorageClass>STANDARD</StorageClass></Contents>`,
				xmlEsc(c.key), c.mod.Format("2006-01-02T15:04:05.000Z"), c.size)
		}
		var pfx []string
		for p := range prefixes {
			pfx = append(pfx, p)
		}
		sort.Strings(pfx)
		for _, p := range pfx {
			fmt.Fprintf(&sb, `<CommonPrefixes><Prefix>%s</Prefix></CommonPrefixes>`, xmlEsc(p))
		}
		sb.WriteString(`</ListBucketResult>`)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(sb.String()))
	case r.Method == http.MethodGet:
		// GetObject, optionally of one version (?versionId=).
		if vid := q.Get("versionId"); vid != "" {
			f.mu.Lock()
			var data []byte
			found := false
			for _, v := range f.versions[key] {
				if v.id == vid {
					data, found = v.data, true
				}
			}
			f.mu.Unlock()
			if !found {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}
		f.mu.Lock()
		data, ok := f.objects[key]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newTestS3Provider(t *testing.T, endpoint string) *Provider {
	t.Helper()
	p, err := New(storage.Config{
		AccessKey: "test-access",
		SecretKey: "test-secret",
		Endpoint:  endpoint,
		Extra:     map[string]string{"bucket": "test-bucket", "pathStyle": "true"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p.(*Provider)
}

// TestS3UploadRoundTrip covers both upload paths against the fake
// server: a small file goes through a single PutObject, a file over
// minPartSize through multipart. It pins the transfermanager
// migration (SA1019): if the new uploader cannot complete either
// path against plain S3 REST, this fails.
func TestS3UploadRoundTrip(t *testing.T) {
	fake := newFakeS3()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	p := newTestS3Provider(t, srv.URL)

	small := []byte("hello s3\n")
	node, err := p.Upload(t.Context(), "hello.txt", bytes.NewReader(small), int64(len(small)))
	if err != nil {
		t.Fatalf("Upload small: %v", err)
	}
	if node.Size != int64(len(small)) {
		t.Fatalf("small node.Size: got %d want %d", node.Size, len(small))
	}
	fake.mu.Lock()
	got := fake.objects["hello.txt"]
	fake.mu.Unlock()
	if !bytes.Equal(got, small) {
		t.Fatalf("small object mismatch: got %q", got)
	}

	large := bytes.Repeat([]byte("x"), minPartSize+1)
	node, err = p.Upload(t.Context(), "large.bin", bytes.NewReader(large), int64(len(large)))
	if err != nil {
		t.Fatalf("Upload large: %v", err)
	}
	if node.Size != int64(len(large)) {
		t.Fatalf("large node.Size: got %d want %d", node.Size, len(large))
	}
	fake.mu.Lock()
	got = fake.objects["large.bin"]
	usedMultipart := fake.multipartUsed
	fake.mu.Unlock()
	if !bytes.Equal(got, large) {
		t.Fatalf("large object mismatch: got %d bytes, want %d", len(got), len(large))
	}
	if !usedMultipart {
		t.Fatalf("large upload did not use multipart (threshold = minPartSize)")
	}
}

// TestS3VersionsRestore: two uploads of one key leave two versions
// (ListObjectVersions returns every version, head included);
// restoring the older one makes its bytes the new head, and the
// restore itself adds a third version — so a restore is undoable.
func TestS3VersionsRestore(t *testing.T) {
	fake := newFakeS3()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	p := newTestS3Provider(t, srv.URL)
	ctx := t.Context()
	if _, ok := any(p).(storage.Versioner); !ok {
		t.Fatal("s3 provider must implement storage.Versioner")
	}
	if _, err := p.Upload(ctx, "doc.txt", strings.NewReader("v1"), 2); err != nil {
		t.Fatalf("Upload v1: %v", err)
	}
	if _, err := p.Upload(ctx, "doc.txt", strings.NewReader("v2"), 2); err != nil {
		t.Fatalf("Upload v2: %v", err)
	}
	vs, err := p.Versions(ctx, "doc.txt")
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(vs) != 2 {
		t.Fatalf("Versions: got %d, want 2", len(vs))
	}
	for _, v := range vs {
		if v.Extra["versionId"] == "" {
			t.Fatalf("version node must carry Extra[versionId], got %+v", v)
		}
		if v.Size != 2 || v.Name != "doc.txt" {
			t.Fatalf("version node: %+v, want name doc.txt size 2", v)
		}
	}
	// The oldest version is the v1 bytes: restore the first-listed
	// (oldest) version and read the head back.
	oldID := vs[0].Extra["versionId"]
	if err := p.Restore(ctx, "doc.txt", oldID); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	rc, err := p.OpenRead(ctx, "doc.txt")
	if err != nil {
		t.Fatalf("OpenRead after restore: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != "v1" {
		t.Fatalf("content after restore: %q, want %q", got, "v1")
	}
	vs, err = p.Versions(ctx, "doc.txt")
	if err != nil || len(vs) != 3 {
		t.Fatalf("Versions after restore: %v, %d versions, want 3", err, len(vs))
	}
	if err := p.Restore(ctx, "doc.txt", "v-nope"); err == nil {
		t.Fatal("Restore of an unknown version must fail")
	}
}

// TestS3VersionsDisabled pins the versioning-off contract: a
// bucket without versioning has no history, so Versions returns
// an empty list — not an error — and the UI says "no versions".
func TestS3VersionsDisabled(t *testing.T) {
	fake := newFakeS3()
	fake.versioning = "Suspended"
	srv := httptest.NewServer(fake)
	defer srv.Close()
	p := newTestS3Provider(t, srv.URL)
	if _, err := p.Upload(t.Context(), "doc.txt", strings.NewReader("v1"), 2); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	vs, err := p.Versions(t.Context(), "doc.txt")
	if err != nil {
		t.Fatalf("Versions with versioning off must not error: %v", err)
	}
	if len(vs) != 0 {
		t.Fatalf("Versions with versioning off: got %d, want 0", len(vs))
	}
}

// TestS3ListFileRefReturnsEmpty pins the driver semantics the
// sync engine used to trip over: List appends "/" to a ref that is
// not already suffixed, so a file ref becomes a folder prefix that
// matches nothing — empty list, nil error. The engine must not
// depend on List to discover a file root (sync.Scan Stats the root
// first); this test pins the behaviour so a future change to List
// cannot silently change what the engine sees.
func TestS3ListFileRefReturnsEmpty(t *testing.T) {
	fake := newFakeS3()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	p := newTestS3Provider(t, srv.URL)
	ctx := t.Context()
	if _, err := p.Upload(ctx, "alpine.iso", strings.NewReader("iso-bytes"), 9); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	nodes, err := p.List(ctx, "alpine.iso")
	if err != nil {
		t.Fatalf("List of a file ref must not error: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("List of a file ref: got %d nodes %+v, want 0 (a file ref is treated as a folder prefix)", len(nodes), nodes)
	}
	// The same file is found by listing its parent.
	nodes, err = p.List(ctx, "")
	if err != nil {
		t.Fatalf("List root: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Name != "alpine.iso" {
		t.Fatalf("List root: got %+v, want [alpine.iso]", nodes)
	}
	// And Stat still sees the file — this is what Scan uses now.
	st, err := p.Stat(ctx, "alpine.iso")
	if err != nil {
		t.Fatalf("Stat file: %v", err)
	}
	if st.IsDir || st.Size != 9 {
		t.Fatalf("Stat file: %+v, want a 9-byte file", st)
	}
}

// TestS3CopyRoundTrip: CopyObject duplicates server-side — the
// source stays, the destination has the same bytes — and Rename
// (Copy + Delete) still removes the source afterwards.
func TestS3CopyRoundTrip(t *testing.T) {
	fake := newFakeS3()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	p := newTestS3Provider(t, srv.URL)
	ctx := t.Context()
	cp, ok := any(p).(storage.Copier)
	if !ok {
		t.Fatal("s3 provider must implement storage.Copier")
	}
	if _, err := p.Upload(ctx, "a.txt", strings.NewReader("hello"), 5); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if err := cp.Copy(ctx, "a.txt", "b.txt"); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	for _, ref := range []string{"a.txt", "b.txt"} {
		rc, err := p.OpenRead(ctx, ref)
		if err != nil {
			t.Fatalf("OpenRead %s: %v", ref, err)
		}
		got, _ := io.ReadAll(rc)
		_ = rc.Close()
		if string(got) != "hello" {
			t.Fatalf("OpenRead %s: %q, want %q", ref, got, "hello")
		}
	}
	if err := p.Rename(ctx, "b.txt", "c.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := p.Stat(ctx, "b.txt"); err == nil {
		t.Fatal("Rename must delete the source")
	}
	if _, err := p.Stat(ctx, "c.txt"); err != nil {
		t.Fatalf("Stat renamed file: %v", err)
	}
	if err := cp.Copy(ctx, "missing.txt", "d.txt"); err == nil {
		t.Fatal("Copy of a missing source must fail")
	}
}
