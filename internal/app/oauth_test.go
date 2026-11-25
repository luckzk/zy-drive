package app

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/luckzk/zy-drive/internal/domain"
)

// challengeOf recomputes the PKCE S256 challenge for a verifier.
func challengeOf(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// TestDrimeOAuthPKCE: Drime desktop apps are public clients — PKCE
// (S256) is required and no client secret may be sent. BeginOAuth
// must emit a code_challenge, and the exchange must send the matching
// code_verifier and omit client_secret entirely.
func TestDrimeOAuthPKCE(t *testing.T) {
	a := newTestApp(t)
	start, err := a.BeginOAuth(domain.ProviderDrime, "drime-client-id")
	if err != nil {
		t.Fatalf("BeginOAuth(drime): %v", err)
	}
	u, err := url.Parse(start.AuthURL)
	if err != nil {
		t.Fatalf("parse auth URL: %v", err)
	}
	if !strings.HasPrefix(start.AuthURL, "https://app.drime.cloud/oauth/authorize") {
		t.Fatalf("auth URL: %s", start.AuthURL)
	}
	q := u.Query()
	if q.Get("client_id") != "drime-client-id" {
		t.Fatalf("client_id: %q", q.Get("client_id"))
	}
	if q.Get("code_challenge") == "" {
		t.Fatalf("no code_challenge in authorize URL (PKCE is required for Drime)")
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Fatalf("code_challenge_method: %q, want S256", q.Get("code_challenge_method"))
	}
	if q.Get("state") != start.State {
		t.Fatalf("state mismatch")
	}

	// Mock token endpoint: records the exchange form.
	var gotVerifier, gotSecret, gotCode string
	var gotGrant string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotGrant = r.Form.Get("grant_type")
		gotCode = r.Form.Get("code")
		gotVerifier = r.Form.Get("code_verifier")
		gotSecret = r.Form.Get("client_secret")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "drime-access", "refresh_token": "drime-refresh",
			"expires_in": 3600, "token_type": "Bearer",
		})
	}))
	defer srv.Close()

	tokens, err := a.ExchangeOAuthCode(domain.ProviderDrime, "drime-client-id", "", "auth-code-1", start.State, srv.URL)
	if err != nil {
		t.Fatalf("ExchangeOAuthCode(drime): %v", err)
	}
	if tokens.AccessToken != "drime-access" || tokens.RefreshToken != "drime-refresh" {
		t.Fatalf("tokens: %+v", tokens)
	}
	if gotGrant != "authorization_code" || gotCode != "auth-code-1" {
		t.Fatalf("exchange form: grant=%q code=%q", gotGrant, gotCode)
	}
	if gotVerifier == "" {
		t.Fatalf("exchange sent no code_verifier")
	}
	if gotSecret != "" {
		t.Fatalf("exchange sent client_secret=%q: Drime refuses a secret from public clients", gotSecret)
	}
	// The verifier must be the one behind the challenge: recompute
	// S256 and compare with the authorize URL's challenge.
	if got := challengeOf(gotVerifier); got != q.Get("code_challenge") {
		t.Fatalf("code_verifier does not match code_challenge")
	}
	// Single-use: the same state cannot be exchanged twice.
	if _, err := a.ExchangeOAuthCode(domain.ProviderDrime, "drime-client-id", "", "auth-code-2", start.State, srv.URL); err == nil {
		t.Fatalf("second exchange with the same state: want an error")
	}
}

// TestDrimeOAuthNoSecretInBegin: BeginOAuth for Drime needs only a
// client id; Dropbox/Drive still build their URLs unchanged.
func TestDrimeOAuthBeginOthersUnchanged(t *testing.T) {
	a := newTestApp(t)
	start, err := a.BeginOAuth(domain.ProviderDropbox, "dbx-id")
	if err != nil {
		t.Fatalf("BeginOAuth(dropbox): %v", err)
	}
	u, _ := url.Parse(start.AuthURL)
	if u.Query().Get("code_challenge") != "" {
		t.Fatalf("dropbox authorize URL must not carry a PKCE challenge")
	}
}
