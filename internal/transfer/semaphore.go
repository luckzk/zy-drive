// SPDX-License-Identifier: MPL-2.0
package transfer

import (
	"context"
	"sync"
	"time"
)

// Semaphore is a counting semaphore whose limit can change while
// transfers are in flight — the Settings page changes concurrency
// live, and the worker must honour the new value for the next
// transfer instead of waiting for a restart. Acquire blocks until
// a slot frees or ctx is cancelled; it never busy-waits tighter
// than 50ms.
type Semaphore struct {
	mu    sync.Mutex
	limit int
	inUse int
}

// NewSemaphore returns a semaphore with the given limit (clamped to
// at least 1).
func NewSemaphore(limit int) *Semaphore {
	if limit < 1 {
		limit = 1
	}
	return &Semaphore{limit: limit}
}

// Acquire takes one slot, or false when ctx ends first.
func (s *Semaphore) Acquire(ctx context.Context) bool {
	for {
		s.mu.Lock()
		if s.inUse < s.limit {
			s.inUse++
			s.mu.Unlock()
			return true
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Release frees one slot.
func (s *Semaphore) Release() {
	s.mu.Lock()
	if s.inUse > 0 {
		s.inUse--
	}
	s.mu.Unlock()
}

// SetLimit changes the cap for future acquires. Transfers already
// running keep running; the next acquire honours the new value.
func (s *Semaphore) SetLimit(n int) {
	if n < 1 {
		n = 1
	}
	s.mu.Lock()
	s.limit = n
	s.mu.Unlock()
}

// Limit returns the current cap.
func (s *Semaphore) Limit() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.limit
}

// InUse returns how many slots are currently held (tests only).
func (s *Semaphore) InUse() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inUse
}
