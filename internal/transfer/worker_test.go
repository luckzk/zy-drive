// SPDX-License-Identifier: MPL-2.0
package transfer

import (
	"bytes"
	"context"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/luckzk/zy-drive/internal/cache"
	"github.com/luckzk/zy-drive/internal/domain"
)

// testQueueDB opens a throwaway SQLite DB for queue tests.
func testQueueDB(t *testing.T) *cache.DB {
	t.Helper()
	db, err := cache.Open(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSemaphoreLimitChangeAppliesLive(t *testing.T) {
	s := NewSemaphore(1)
	if !s.Acquire(context.Background()) {
		t.Fatal("first acquire should succeed")
	}
	// Second acquire blocks at limit 1; raising the limit unblocks it.
	done := make(chan bool, 1)
	go func() { done <- s.Acquire(context.Background()) }()
	select {
	case <-done:
		t.Fatal("second acquire succeeded while limit is 1")
	case <-time.After(150 * time.Millisecond):
	}
	s.SetLimit(2)
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("second acquire failed after raising the limit")
		}
	case <-time.After(time.Second):
		t.Fatal("second acquire still blocked after SetLimit(2)")
	}
	if got := s.InUse(); got != 2 {
		t.Fatalf("InUse = %d, want 2", got)
	}
	s.Release()
	s.Release()
	if got := s.Limit(); got != 2 {
		t.Fatalf("Limit = %d, want 2", got)
	}
}

func TestLimiterSetRateCapsThroughput(t *testing.T) {
	// 100 KB/s: copying 200 KB must take ~2s, not ~0s.
	l := NewLimiter(100 * 1024)
	data := make([]byte, 200*1024)
	start := time.Now()
	r := NewLimitedReader(bytes.NewReader(data), l)
	buf := make([]byte, 32*1024)
	for {
		_, err := r.Read(buf)
		if err != nil {
			break
		}
	}
	elapsed := time.Since(start)
	if elapsed < 1500*time.Millisecond {
		t.Fatalf("200 KB at 100 KB/s took %v, want >= 1.5s", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("200 KB at 100 KB/s took %v, want < 5s", elapsed)
	}
	// Raising the rate live makes the next copy fast.
	l.SetRate(0) // unlimited
	start = time.Now()
	r = NewLimitedReader(bytes.NewReader(data), l)
	for {
		_, err := r.Read(buf)
		if err != nil {
			break
		}
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("unlimited copy took %v, want < 500ms", elapsed)
	}
}

func TestWorkerHonoursConcurrencySetting(t *testing.T) {
	q := NewQueue(testQueueDB(t))
	var mu sync.Mutex
	current, maxSeen := 0, 0
	exec := func(ctx context.Context, tr *domain.Transfer, report func(done, speedBps int64)) error {
		mu.Lock()
		current++
		if current > maxSeen {
			maxSeen = current
		}
		mu.Unlock()
		time.Sleep(120 * time.Millisecond)
		mu.Lock()
		current--
		mu.Unlock()
		return nil
	}
	w := NewWorker(q, exec, 2, nil)
	for i := 0; i < 6; i++ {
		if err := q.Enqueue(&domain.Transfer{ID: "t" + strconv.Itoa(i), Kind: domain.TransferUpload, Status: domain.TransferQueued}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Start(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		done := true
		for _, tr := range q.List() {
			if tr.Status != domain.TransferCompleted {
				done = false
				break
			}
		}
		if done {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if maxSeen != 2 {
		t.Fatalf("max concurrent = %d, want exactly 2 (setting)", maxSeen)
	}
	// Raising the cap live is honoured for the next batch.
	w.SetConcurrency(4)
	if got := w.Concurrency(); got != 4 {
		t.Fatalf("Concurrency = %d, want 4", got)
	}
}
