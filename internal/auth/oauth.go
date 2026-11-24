// Package auth owns OAuth2 flows and encrypted credential storage.
// Tokens are never stored in plaintext and never appear in logs.
// The master key comes from the system keychain when available;
// otherwise a password-derived key encrypts the vault (password itself
// is never stored).
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// OAuthConfig describes one provider's Authorization Code flow.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string `json:"-"`
	AuthEndpoint string
	TokenURL     string
	RedirectURI  string
	Scopes       []string
}

// AuthURL builds the browser URL the user opens to authorise.
// state must be a fresh random value per attempt (CSRF protection).
func (c *OAuthConfig) AuthURL(state string) string {
	q := url.Values{}
	q.Set("client_id", c.ClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", c.RedirectURI)
	q.Set("scope", joinScopes(c.Scopes))
	q.Set("state", state)
	q.Set("access_type", "offline") // ask for a refresh token
	return c.AuthEndpoint + "?" + q.Encode()
}

func joinScopes(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += " "
		}
		out += v
	}
	return out
}

// NewState returns a fresh random CSRF state value.
func NewState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ExchangeCode exchanges an authorization code for tokens. The HTTP
// call is a placeholder in the V0.1 skeleton: drivers call this with
// their real token endpoint; no secrets are logged.
func ExchangeCode(ctx context.Context, cfg *OAuthConfig, code string) (accessToken, refreshToken string, err error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", cfg.RedirectURI)
	form.Set("client_id", cfg.ClientID)
	// Client secret is sent in the POST body, never in a URL or log.
	form.Set("client_secret", cfg.ClientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = req
	// TODO(auth): perform the request and parse the token response.
	// Deliberately not implemented in the skeleton: each driver verifies
	// its token endpoint shape (Drive vs Graph vs Dropbox differ).
	return "", "", fmt.Errorf("auth: ExchangeCode not implemented for this provider yet")
}
