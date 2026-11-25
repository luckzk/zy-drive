// SPDX-License-Identifier: MPL-2.0
// Package http provides the shared HTTP client for all drivers.
// It owns retry, backoff and rate-limit discipline so no driver writes
// its own: network errors retry with exponential backoff, 429s wait the
// server-declared (or driver-declared) interval, and auth expiry refreshes
// the token once before counting as a retry.
//
// Logging rule: request logs record method, path template, status and
// latency only. Authorization headers, tokens and presigned URLs are
// never logged — use RedactURL before logging any URL.
package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/luckzk/zy-drive/internal/netproxy"
)

// RetryPolicy is declared per driver (limits live in Caps / driver code,
// never as a global constant — Drime, for example, waits 60s and retries
// only once on 429, never loops).
type RetryPolicy struct {
	MaxRetries    int
	BaseBackoff   time.Duration // first wait, doubled each attempt
	MaxBackoff    time.Duration
	RetryOn429    bool
	Backoff429    time.Duration // fixed wait for 429 when no Retry-After
	Max429Retries int
}

// DefaultPolicy is a conservative starting point for drivers that do not
// declare their own limits.
var DefaultPolicy = RetryPolicy{
	MaxRetries:    3,
	BaseBackoff:   500 * time.Millisecond,
	MaxBackoff:    30 * time.Second,
	RetryOn429:    true,
	Backoff429:    60 * time.Second,
	Max429Retries: 1,
}

// Client is a retrying HTTP client with no business state.
type Client struct {
	HTTP   *http.Client
	Policy RetryPolicy
	// OnAuthExpired is called once when a 401/403 suggests an expired
	// token; it should refresh the token. Returning an error aborts the
	// request and does not count as a retry.
	OnAuthExpired func(ctx context.Context) error
}

// New returns a Client with the given policy (DefaultPolicy when nil).
func New(policy *RetryPolicy) *Client {
	p := DefaultPolicy
	if policy != nil {
		p = *policy
	}
	return &Client{
		// Shared proxy-aware transport: every driver request
		// (Dropbox/Drive/Drime/OneDrive) follows the proxy the
		// user configured in Settings -> Network (system / manual
		// / direct), not just ProxyFromEnvironment.
		HTTP:   netproxy.NewClient(120),
		Policy: p,
	}
}

// Do executes req with retry discipline. The request body must be
// replayable (GetBody set, or a bytes.Reader) for retries to work.
func (c *Client) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	var lastErr error
	retries429 := 0
	for attempt := 0; ; attempt++ {
		resp, err := c.HTTP.Do(req.WithContext(ctx))
		if err != nil {
			lastErr = err
			if attempt >= c.Policy.MaxRetries || ctx.Err() != nil {
				return nil, fmt.Errorf("http: %w", lastErr)
			}
			if !sleep(ctx, c.backoff(attempt)) {
				return nil, ctx.Err()
			}
			if rerr := c.replay(req); rerr != nil {
				return nil, rerr
			}
			continue
		}
		switch {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			// Auth expiry: refresh once, then retry once without
			// counting against the retry budget.
			if c.OnAuthExpired != nil && attempt == 0 {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if ferr := c.OnAuthExpired(ctx); ferr != nil {
					return nil, fmt.Errorf("http: token refresh failed: %w", ferr)
				}
				if rerr := c.replay(req); rerr != nil {
					return nil, rerr
				}
				continue
			}
			return resp, nil
		case resp.StatusCode == http.StatusTooManyRequests:
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if !c.Policy.RetryOn429 || retries429 >= c.Policy.Max429Retries {
				return nil, fmt.Errorf("http: rate limited (429), giving up after %d retr%s",
					retries429, plural(retries429))
			}
			retries429++
			wait := c.Policy.Backoff429
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if secs, err := strconv.Atoi(ra); err == nil {
					wait = time.Duration(secs) * time.Second
				}
			}
			if !sleep(ctx, wait) {
				return nil, ctx.Err()
			}
			if rerr := c.replay(req); rerr != nil {
				return nil, rerr
			}
			continue
		case resp.StatusCode >= 500:
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("http: server error %d", resp.StatusCode)
			if attempt >= c.Policy.MaxRetries {
				return nil, lastErr
			}
			if !sleep(ctx, c.backoff(attempt)) {
				return nil, ctx.Err()
			}
			if rerr := c.replay(req); rerr != nil {
				return nil, rerr
			}
			continue
		default:
			return resp, nil
		}
	}
}

func (c *Client) backoff(attempt int) time.Duration {
	d := float64(c.Policy.BaseBackoff) * math.Pow(2, float64(attempt))
	if d > float64(c.Policy.MaxBackoff) {
		d = float64(c.Policy.MaxBackoff)
	}
	return time.Duration(d)
}

// replay resets the request body for the next attempt.
func (c *Client) replay(req *http.Request) error {
	if req.GetBody == nil {
		return errors.New("http: request body is not replayable, cannot retry")
	}
	body, err := req.GetBody()
	if err != nil {
		return err
	}
	req.Body = body
	return nil
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// RedactURL strips query strings (presigned URLs, tokens) for logging.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<unparseable-url>"
	}
	u.RawQuery = ""
	return u.String()
}

// RedactHeaders returns a copy of headers with Authorization removed.
func RedactHeaders(h http.Header) http.Header {
	out := h.Clone()
	out.Del("Authorization")
	return out
}

// IsRateLimited reports whether err came from a 429 give-up.
func IsRateLimited(err error) bool {
	return err != nil && strings.Contains(err.Error(), "rate limited (429)")
}
