// SPDX-License-Identifier: MPL-2.0
package s3

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
type fakeS3 struct {
	mu            sync.Mutex
	objects       map[string][]byte
	uploads       map[string]map[int][]byte
	nextUpload    int
	multipartUsed bool
}

func newFakeS3() *fakeS3 {
	return &fakeS3{objects: map[string][]byte{}, uploads: map[string]map[int][]byte{}}
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
	case r.Method == http.MethodPut:
		// PutObject (single-part)
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.objects[key] = body
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
		f.objects[key] = data
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
	case r.Method == http.MethodGet:
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
