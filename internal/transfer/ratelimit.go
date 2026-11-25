// SPDX-License-Identifier: MPL-2.0
package transfer

import (
	"io"
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

// SetRate changes the cap live (Settings save applies immediately).
// Pass 0 for unlimited. Transfers already in flight pace their
// next reads at the new rate.
func (l *Limiter) SetRate(bytesPerSec int64) {
	l.mu.Lock()
	l.bytesPerSec = bytesPerSec
	l.mu.Unlock()
}

// Rate returns the current cap in bytes per second (0 = unlimited).
func (l *Limiter) Rate() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.bytesPerSec
}

// LimitedReader throttles a stream to the limiter's rate. It returns
// r unchanged when l is nil or unlimited, so callers can wrap
// unconditionally.
func NewLimitedReader(r io.Reader, l *Limiter) io.Reader {
	if l == nil || l.Rate() <= 0 {
		return r
	}
	return &limitedReader{r: r, l: l}
}

type limitedReader struct {
	r io.Reader
	l *Limiter
}

func (lr *limitedReader) Read(b []byte) (int, error) {
	n, err := lr.r.Read(b)
	if n > 0 {
		lr.l.Wait(int64(n))
	}
	return n, err
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
