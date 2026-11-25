// SPDX-License-Identifier: MPL-2.0
package sync

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/transfer"
)

// mockProvider is an in-memory Provider for runner tests. OpenRead
// sleeps readDelay (simulating a slow cloud) and tracks the peak
// number of concurrent reads, so the tests prove parallelism and
// the limiter instead of assuming them.
type mockProvider struct {
	mu        sync.Mutex
	files     map[string][]byte
	deleted   []string
	order     []string // "copy:<path>" / "delete:<path>" in execution order
	readDelay time.Duration
	curReads  int32
	maxReads  int32
}

func newMockProvider(files map[string][]byte, readDelay time.Duration) *mockProvider {
	return &mockProvider{files: files, readDelay: readDelay}
}

func (m *mockProvider) List(ctx context.Context, ref string) ([]domain.Node, error) {
	return nil, nil
}

func (m *mockProvider) Stat(ctx context.Context, ref string) (domain.Node, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.files[ref]
	if !ok {
		return domain.Node{}, fmt.Errorf("mock: no such file %s", ref)
	}
	return domain.Node{Path: ref, Ref: ref, Name: ref, Size: int64(len(b))}, nil
}

func (m *mockProvider) OpenRead(ctx context.Context, ref string) (io.ReadCloser, error) {
	cur := atomic.AddInt32(&m.curReads, 1)
	for {
		max := atomic.LoadInt32(&m.maxReads)
		if cur <= max || atomic.CompareAndSwapInt32(&m.maxReads, max, cur) {
			break
		}
	}
	defer atomic.AddInt32(&m.curReads, -1)
	if m.readDelay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(m.readDelay):
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.files[ref]
	if !ok {
		return nil, fmt.Errorf("mock: no such file %s", ref)
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *mockProvider) Upload(ctx context.Context, ref string, r io.Reader, size int64) (domain.Node, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return domain.Node{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.files == nil {
		m.files = map[string][]byte{}
	}
	m.files[ref] = b
	m.order = append(m.order, "copy:"+ref)
	return domain.Node{Path: ref, Ref: ref, Name: ref, Size: int64(len(b))}, nil
}

func (m *mockProvider) Mkdir(ctx context.Context, ref string) error { return nil }

func (m *mockProvider) Rename(ctx context.Context, src, dst string) error { return nil }

func (m *mockProvider) Delete(ctx context.Context, ref string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, ref)
	m.deleted = append(m.deleted, ref)
	m.order = append(m.order, "delete:"+ref)
	return nil
}

func (m *mockProvider) Caps() domain.Caps { return domain.Caps{} }

var _ storage.Provider = (*mockProvider)(nil)

func copyPlan(jobID string, paths ...string) *Plan {
	p := &Plan{JobID: jobID, Direction: domain.DirectionCopy}
	for _, path := range paths {
		p.Items = append(p.Items, PlanItem{Path: path, Action: domain.ActionUpload, Size: 3})
	}
	return p
}

// TestRunnerCopiesInParallel: 4 files × 200ms reads at concurrency 4
// must finish in well under the ~800ms a sequential runner needs,
// and the peak concurrent reads must actually reach 4.
func TestRunnerCopiesInParallel(t *testing.T) {
	srcFiles := map[string][]byte{
		"a.txt": []byte("aaa"), "b.txt": []byte("bbb"),
		"c.txt": []byte("ccc"), "d.txt": []byte("ddd"),
	}
	src := newMockProvider(srcFiles, 200*time.Millisecond)
	dst := newMockProvider(map[string][]byte{}, 0)
	r := NewRunner(nil, 4, nil)
	start := time.Now()
	run, err := r.Execute(context.Background(), copyPlan("j1", "a.txt", "b.txt", "c.txt", "d.txt"), src, dst, "src", "dst")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if run.Status != domain.RunCompleted {
		t.Fatalf("run status = %v, want completed", run.Status)
	}
	if elapsed > 650*time.Millisecond {
		t.Fatalf("4 parallel copies took %v, want < 650ms (sequential would be ~800ms)", elapsed)
	}
	if got := atomic.LoadInt32(&src.maxReads); got != 4 {
		t.Fatalf("peak concurrent reads = %d, want 4", got)
	}
	for p, want := range srcFiles {
		if got := dst.files[p]; !bytes.Equal(got, want) {
			t.Fatalf("dst %s = %q, want %q", p, got, want)
		}
	}
}

// TestRunnerConcurrencyOneIsSequential: concurrency 1 behaves like
// the old runner — peak reads stay 1 and the elapsed time is the
// sum of the reads.
func TestRunnerConcurrencyOneIsSequential(t *testing.T) {
	src := newMockProvider(map[string][]byte{"a.txt": []byte("aaa"), "b.txt": []byte("bbb")}, 150*time.Millisecond)
	dst := newMockProvider(map[string][]byte{}, 0)
	r := NewRunner(nil, 1, nil)
	start := time.Now()
	if _, err := r.Execute(context.Background(), copyPlan("j1", "a.txt", "b.txt"), src, dst, "src", "dst"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 280*time.Millisecond {
		t.Fatalf("concurrency 1 took %v, want >= 280ms (sequential)", elapsed)
	}
	if got := atomic.LoadInt32(&src.maxReads); got != 1 {
		t.Fatalf("peak concurrent reads = %d, want 1", got)
	}
}

// TestRunnerLimiterCapsThroughput: 4 files × 50 KB through one
// shared 100 KB/s limiter must take ~2s for 200 KB, not ~0s.
func TestRunnerLimiterCapsThroughput(t *testing.T) {
	payload := []byte(strings.Repeat("x", 50*1024))
	srcFiles := map[string][]byte{
		"a.bin": payload, "b.bin": payload, "c.bin": payload, "d.bin": payload,
	}
	src := newMockProvider(srcFiles, 0)
	dst := newMockProvider(map[string][]byte{}, 0)
	lim := transfer.NewLimiter(100 * 1024)
	r := NewRunner(nil, 4, lim)
	plan := copyPlan("j1", "a.bin", "b.bin", "c.bin", "d.bin")
	for i := range plan.Items {
		plan.Items[i].Size = int64(len(payload))
	}
	start := time.Now()
	if _, err := r.Execute(context.Background(), plan, src, dst, "src", "dst"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 1500*time.Millisecond {
		t.Fatalf("200 KB at 100 KB/s took %v, want >= 1.5s", elapsed)
	}
	if elapsed > 6*time.Second {
		t.Fatalf("200 KB at 100 KB/s took %v, want < 6s", elapsed)
	}
}

// TestRunnerDeletesStillLast: deletes run after every copy even
// when copies run in parallel.
func TestRunnerDeletesStillLast(t *testing.T) {
	src := newMockProvider(map[string][]byte{"a.txt": []byte("aaa")}, 50*time.Millisecond)
	dst := newMockProvider(map[string][]byte{"old.txt": []byte("old")}, 0)
	r := NewRunner(nil, 4, nil)
	plan := &Plan{JobID: "j1", Direction: domain.DirectionMirror, Items: []PlanItem{
		{Path: "a.txt", Action: domain.ActionUpload, Size: 3},
		{Path: "old.txt", Action: domain.ActionDelete},
	}}
	if _, err := r.Execute(context.Background(), plan, src, dst, "src", "dst"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(dst.order) != 2 || dst.order[0] != "copy:a.txt" || dst.order[1] != "delete:old.txt" {
		t.Fatalf("execution order = %v, want [copy:a.txt delete:old.txt]", dst.order)
	}
}

// TestRunnerDeletesDeepestFirst: when a whole subtree is deleted
// (mirror), the files go before their parent directories —
// otherwise a RemoveAll-style driver deletes the directory first
// and the later file delete fails with "not found".
func TestRunnerDeletesDeepestFirst(t *testing.T) {
	src := newMockProvider(map[string][]byte{"keep.txt": []byte("k")}, 0)
	dst := newMockProvider(map[string][]byte{
		"/a/b/c.txt": []byte("c"), "/a/b/d.txt": []byte("d"),
	}, 0)
	r := NewRunner(nil, 4, nil)
	plan := &Plan{JobID: "j1", Direction: domain.DirectionMirror, Items: []PlanItem{
		{Path: "/a", Action: domain.ActionDelete},
		{Path: "keep.txt", Action: domain.ActionUpload, Size: 1},
		{Path: "/a/b", Action: domain.ActionDelete},
		{Path: "/a/b/c.txt", Action: domain.ActionDelete},
		{Path: "/a/b/d.txt", Action: domain.ActionDelete},
	}}
	if _, err := r.Execute(context.Background(), plan, src, dst, "src", "dst"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	deletes := []string{}
	for _, op := range dst.order {
		if strings.HasPrefix(op, "delete:") {
			deletes = append(deletes, strings.TrimPrefix(op, "delete:"))
		}
	}
	if len(deletes) != 4 {
		t.Fatalf("deletes = %v, want 4", deletes)
	}
	// The two files (depth 3) must come before /a/b (depth 2) and /a.
	if deletes[2] != "/a/b" || deletes[3] != "/a" {
		t.Fatalf("delete order = %v, want files first, then /a/b, then /a", deletes)
	}
}
