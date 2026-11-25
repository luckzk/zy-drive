// Package auth owns OAuth2 flows and encrypted credential storage.
// Tokens are never stored in plaintext and never appear in logs.
// The master key is a random key generated once and kept in a
// 0600 file inside the config directory (see masterkey.go); it is
// never hard-coded and never committed.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/luckzk/zy-drive/internal/netproxy"
)

// OAuthConfig describes one provider's Authorization Code flow.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string `json:"-"`
	AuthEndpoint string
	TokenURL     string
	RedirectURI  string
	Scopes       []string
	// CodeVerifier is the PKCE verifier (RFC 7636). Empty when the
	// provider does not use PKCE. Desktop/public clients (Drime)
	// require it; confidential clients (Dropbox/Google) do not.
	CodeVerifier string `json:"-"`
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

// NewPKCEVerifier returns a fresh PKCE code_verifier (RFC 7636):
// 48 random bytes, base64url without padding (64 characters, in
// the 43-128 range). Its S256 challenge is ChallengeForVerifier.
func NewPKCEVerifier() (string, error) {
	b := make([]byte, 48)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ChallengeForVerifier returns the S256 code_challenge for a
// verifier: base64url(SHA-256(verifier)). Drime accepts S256 only;
// "plain" is refused.
func ChallengeForVerifier(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ExchangeCode exchanges an authorization code for tokens with a
// standard OAuth2 form POST (grant_type=authorization_code). The
// client secret travels in the POST body, never in a URL or a log.
// Providers differ in endpoint shape, but the token exchange itself
// is the same form POST for Dropbox, Google Drive and OneDrive, so
// one implementation serves them all. Drime differs in two ways,
// both handled here: desktop apps are public clients (no secret is
// ever sent) and PKCE is required (cfg.CodeVerifier). The returned
// refresh token may be empty when the provider only issued a
// short-lived access token.
func ExchangeCode(ctx context.Context, cfg *OAuthConfig, code string) (accessToken, refreshToken string, err error) {
	if cfg.TokenURL == "" {
		return "", "", fmt.Errorf("auth: token URL is required")
	}
	if code == "" {
		return "", "", fmt.Errorf("auth: authorization code is required")
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", cfg.RedirectURI)
	form.Set("client_id", cfg.ClientID)
	// Client secret is sent in the POST body, never in a URL or a
	// log — and only when there is one. Drime desktop apps are
	// public clients: sending a secret (even an empty one) is
	// refused, so it is omitted entirely for them.
	if cfg.ClientSecret != "" {
		form.Set("client_secret", cfg.ClientSecret)
	}
	// PKCE (RFC 7636): the verifier proves the code exchange comes
	// from the same client that started the authorization.
	if cfg.CodeVerifier != "" {
		form.Set("code_verifier", cfg.CodeVerifier)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Proxy-aware client (Settings -> Network): the exchange must
	// follow the user's proxy, not just the process env vars.
	// On failure the error carries a [proxy-kind=...] marker so
	// the UI can tell "the proxy itself is down" apart from
	// "the proxy cannot reach the provider" and "direct is
	// blocked" — and the log line records provider, target host,
	// proxy and error kind, never the code or a token.
	resp, err := netproxy.NewClient(60).Do(req)
	if err != nil {
		kind, proxyAddr := netproxy.ClassifyError(cfg.TokenURL, err)
		log.Printf("oauth exchange: %s", netproxy.DescribeError(cfg.TokenURL, err))
		return "", "", fmt.Errorf("auth: token exchange request [proxy-kind=%s proxy=%s]: %w", kind, proxyAddr, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", "", fmt.Errorf("auth: token exchange response: %w", err)
	}
	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return "", "", fmt.Errorf("auth: token exchange: cannot parse response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || body.AccessToken == "" {
		msg := body.Error
		if body.ErrorDesc != "" {
			msg += ": " + body.ErrorDesc
		}
		if msg == "" {
			msg = fmt.Sprintf("token endpoint returned %d", resp.StatusCode)
		}
		return "", "", fmt.Errorf("auth: token exchange failed: %s", msg)
	}
	return body.AccessToken, body.RefreshToken, nil
}
