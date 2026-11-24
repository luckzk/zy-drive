package transfer

import (
	"sync"
	"time"
)

// Limiter is a simple token-bucket rate limiter for one transfer.
// bytesPerSec <= 0 means unlimited.
type Limiter struct {
	bytesPerSec int64

	mu       sync.Mutex
	tokens   int64
	lastFill time.Time
}

// NewLimiter returns a Limiter. Pass 0 for unlimited.
func NewLimiter(bytesPerSec int64) *Limiter {
	return &Limiter{bytesPerSec: bytesPerSec, lastFill: time.Now()}
}

// Wait blocks until n bytes may be sent. It returns immediately when
// unlimited. Callers pass the chunk size they are about to write.
func (l *Limiter) Wait(n int64) {
	if l.bytesPerSec <= 0 {
		return
	}
	for {
		l.mu.Lock()
		now := time.Now()
		elapsed := now.Sub(l.lastFill).Seconds()
		l.tokens += int64(elapsed * float64(l.bytesPerSec))
		if l.tokens > l.bytesPerSec {
			l.tokens = l.bytesPerSec
		}
		l.lastFill = now
		if l.tokens >= n {
			l.tokens -= n
			l.mu.Unlock()
			return
		}
		deficit := n - l.tokens
		wait := time.Duration(float64(deficit) / float64(l.bytesPerSec) * float64(time.Second))
		l.mu.Unlock()
		time.Sleep(wait)
	}
}
