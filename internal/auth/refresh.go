// SPDX-License-Identifier: MPL-2.0
package auth

import (
	"context"
	"sync"
	"time"

	"github.com/luckzk/zy-drive/internal/storage/oauth"
)

// Refresher wraps a TokenSource with single-flight refresh: concurrent
// callers share one in-flight refresh instead of minting several tokens.
type Refresher struct {
	mu     sync.Mutex
	source *oauth.TokenSource
}

// NewRefresher returns a Refresher for the given token source.
func NewRefresher(source *oauth.TokenSource) *Refresher {
	return &Refresher{source: source}
}

// ValidToken returns a usable access token, refreshing once when needed.
// It never logs the token.
func (r *Refresher) ValidToken(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.source.Token(ctx)
}

// NeedsRefresh reports whether the current token is expired or about to
// expire. Used by the UI to show "re-authorise" state.
func (r *Refresher) NeedsRefresh() bool {
	t := r.source.Current()
	return t == nil || t.Expired()
}

// BackoffForAuthFailure returns how long to wait before retrying an
// auth failure. Auth failures are not retried blindly: the user must
// re-authorise.
func BackoffForAuthFailure() time.Duration { return 0 }
