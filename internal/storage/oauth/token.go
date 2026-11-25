// SPDX-License-Identifier: MPL-2.0
// Package oauth holds the shared OAuth2 token model and refresh
// discipline. Drivers use it instead of hand-rolling token handling.
// The Authorization Code flow itself (browser + local callback) lives in
// internal/auth; this package is the token data + refresh contract.
package oauth

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Token is an OAuth2 access token. Only the access token lives in
// memory during a session; refresh tokens are stored encrypted by
// internal/auth and never logged.
type Token struct {
	AccessToken  string    `json:"-"`
	RefreshToken string    `json:"-"`
	TokenType    string    `json:"tokenType,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
}

// Expired reports whether the token needs a refresh (with a 60s margin).
func (t *Token) Expired() bool {
	if t == nil || t.AccessToken == "" {
		return true
	}
	if t.Expiry.IsZero() {
		return false
	}
	return time.Now().Add(60 * time.Second).After(t.Expiry)
}

// Refresher exchanges a refresh token for a new Token.
type Refresher func(ctx context.Context, refreshToken string) (*Token, error)

// TokenSource serialises refreshes: concurrent requests share one
// in-flight refresh (single-flight), so a burst of 401s does not mint
// five new tokens.
type TokenSource struct {
	mu        sync.Mutex
	token     *Token
	refresher Refresher
}

// NewTokenSource returns a source holding token; refresher may be nil
// for backends that do not support refresh (then expiry is fatal).
func NewTokenSource(token *Token, refresher Refresher) *TokenSource {
	return &TokenSource{token: token, refresher: refresher}
}

// Token returns a valid access token, refreshing first when expired.
func (s *TokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != nil && !s.token.Expired() {
		return s.token.AccessToken, nil
	}
	if s.refresher == nil {
		return "", errors.New("oauth: token expired and no refresher configured")
	}
	refresh := ""
	if s.token != nil {
		refresh = s.token.RefreshToken
	}
	next, err := s.refresher(ctx, refresh)
	if err != nil {
		return "", err
	}
	s.token = next
	return next.AccessToken, nil
}

// Current returns the token without refreshing (for persistence after a
// refresh performed elsewhere, e.g. by the Agent).
func (s *TokenSource) Current() *Token {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token
}

// Refresh forces a refresh on the next Token call, even when the stored
// token has not expired yet. Used as the http.Client OnAuthExpired hook:
// a 401 means the token is dead regardless of its recorded expiry.
//
// The expiry is set in the past, not to zero: Expired treats a zero
// expiry as "never expires" (personal access tokens carry none), so
// zeroing it here would silently return the dead token again and
// the 401-refresh path would replay the stale token forever.
func (s *TokenSource) Refresh(ctx context.Context) (string, error) {
	s.mu.Lock()
	if s.token != nil {
		s.token.Expiry = time.Now().Add(-time.Hour)
	}
	s.mu.Unlock()
	return s.Token(ctx)
}
