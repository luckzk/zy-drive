// SPDX-License-Identifier: MPL-2.0
package oauth

import (
	"context"
	"testing"
	"time"
)

// TestRefreshForcesRefresh: a 401 mid-session must mint a new token
// even when the stored token has no recorded expiry (personal
// access tokens and freshly exchanged OAuth tokens carry none).
// Regression: Refresh used to zero the expiry, and Expired treats a
// zero expiry as "never expires", so the refresh silently returned
// the dead token and the retried request 401'd again.
func TestRefreshForcesRefresh(t *testing.T) {
	calls := 0
	refresher := func(ctx context.Context, rt string) (*Token, error) {
		calls++
		if rt != "rt-1" {
			t.Fatalf("refresher got refresh token %q, want rt-1", rt)
		}
		return &Token{AccessToken: "new-token", RefreshToken: "rt-2"}, nil
	}
	ts := NewTokenSource(&Token{AccessToken: "old-token", RefreshToken: "rt-1"}, refresher)
	// No expiry recorded: Token returns the stored token as-is.
	if got, err := ts.Token(t.Context()); err != nil || got != "old-token" {
		t.Fatalf("Token: got %q (%v), want old-token", got, err)
	}
	// A 401 forces a refresh despite the missing expiry.
	if got, err := ts.Refresh(t.Context()); err != nil || got != "new-token" {
		t.Fatalf("Refresh: got %q (%v), want new-token", got, err)
	}
	if calls != 1 {
		t.Fatalf("refresher called %d times, want 1", calls)
	}
	// Subsequent calls use the new token without refreshing again.
	if got, err := ts.Token(t.Context()); err != nil || got != "new-token" {
		t.Fatalf("Token after refresh: got %q (%v), want new-token", got, err)
	}
	if calls != 1 {
		t.Fatalf("refresher called %d times, want 1", calls)
	}
}

// TestTokenRefreshesWhenExpired: an expired token refreshes
// proactively, before any request is made.
func TestTokenRefreshesWhenExpired(t *testing.T) {
	calls := 0
	refresher := func(ctx context.Context, rt string) (*Token, error) {
		calls++
		return &Token{
			AccessToken: "fresh", RefreshToken: "rt-2",
			Expiry: time.Now().Add(time.Hour),
		}, nil
	}
	ts := NewTokenSource(&Token{
		AccessToken: "stale", RefreshToken: "rt-1",
		Expiry: time.Now().Add(-time.Minute),
	}, refresher)
	if got, err := ts.Token(t.Context()); err != nil || got != "fresh" {
		t.Fatalf("Token: got %q (%v), want fresh", got, err)
	}
	if calls != 1 {
		t.Fatalf("refresher called %d times, want 1", calls)
	}
}

// TestRefreshWithoutRefresher: a token with no refresher cannot be
// refreshed; the error must say so instead of returning the dead
// token.
func TestRefreshWithoutRefresher(t *testing.T) {
	ts := NewTokenSource(&Token{AccessToken: "pat"}, nil)
	if _, err := ts.Refresh(t.Context()); err == nil {
		t.Fatalf("Refresh without refresher: want an error")
	}
}
