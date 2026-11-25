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

// TestOAuthPKCEOnForAllProviders: PKCE (S256) is on for every
// provider now, not just Drime/OneDrive — a public client (the
// builtin ZyDrive app) has no secret to send, and PKCE is the
// proof of possession instead. Sending a code_verifier alongside
// a client secret is accepted by all four token endpoints, so
// self-built apps keep working unchanged.
func TestOAuthPKCEOnForAllProviders(t *testing.T) {
	a := newTestApp(t)
	for _, p := range []domain.ProviderKind{domain.ProviderDropbox, domain.ProviderDrive, domain.ProviderDrime, domain.ProviderOneDrive} {
		start, err := a.BeginOAuth(p, "client-id")
		if err != nil {
			t.Fatalf("BeginOAuth(%s): %v", p, err)
		}
		u, _ := url.Parse(start.AuthURL)
		q := u.Query()
		if q.Get("code_challenge") == "" {
			t.Fatalf("%s authorize URL must carry a PKCE code_challenge", p)
		}
		if q.Get("code_challenge_method") != "S256" {
			t.Fatalf("%s code_challenge_method: %q, want S256", p, q.Get("code_challenge_method"))
		}
		// The redirect URI is a per-flow loopback address:
		// http://localhost:<random port>, never a fixed port.
		ru := q.Get("redirect_uri")
		if !strings.HasPrefix(ru, "http://localhost:") {
			t.Fatalf("%s redirect_uri: %q, want http://localhost:<random-port>", p, ru)
		}
		if ru == "http://localhost" {
			t.Fatalf("%s redirect_uri has no port: the listener did not start", p)
		}
		a.CancelOAuth(start.State)
	}
}

// TestOAuthCallbackAutoFlow: the loopback listener catches the
// code, PollOAuthCallback reports it, and the exchange sends the
// SAME redirect_uri the authorize URL carried (the provider checks
// the match) plus the PKCE verifier. This is the whole A flow,
// end to end, against a mock token endpoint.
func TestOAuthCallbackAutoFlow(t *testing.T) {
	a := newTestApp(t)
	start, err := a.BeginOAuth(domain.ProviderDrive, "drive-client-id")
	if err != nil {
		t.Fatalf("BeginOAuth(drive): %v", err)
	}
	u, err := url.Parse(start.AuthURL)
	if err != nil {
		t.Fatalf("parse auth URL: %v", err)
	}
	redirectURI := u.Query().Get("redirect_uri")
	if redirectURI == "" {
		t.Fatalf("no redirect_uri in authorize URL")
	}

	// Before the jump-back, the poll must say "waiting".
	if res, err := a.PollOAuthCallback(start.State); err != nil || res.Status != "waiting" {
		t.Fatalf("poll before callback: %+v, err %v", res, err)
	}

	// The browser jumps back to the loopback address.
	callbackURL := redirectURI + "/?code=auth-code-9&state=" + url.QueryEscape(start.State)
	resp, err := http.Get(callbackURL)
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback status: %d", resp.StatusCode)
	}

	// The poll now reports the caught code.
	res, err := a.PollOAuthCallback(start.State)
	if err != nil {
		t.Fatalf("poll after callback: %v", err)
	}
	if res.Status != "done" || res.Code != "auth-code-9" {
		t.Fatalf("poll after callback: %+v", res)
	}

	// The exchange sends the per-flow redirect_uri + verifier.
	var gotRedirect, gotVerifier, gotSecret string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotRedirect = r.Form.Get("redirect_uri")
		gotVerifier = r.Form.Get("code_verifier")
		gotSecret = r.Form.Get("client_secret")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "drive-access", "refresh_token": "drive-refresh",
			"expires_in": 3600, "token_type": "Bearer",
		})
	}))
	defer srv.Close()

	tokens, err := a.ExchangeOAuthCode(domain.ProviderDrive, "drive-client-id", "drive-secret", res.Code, start.State, srv.URL)
	if err != nil {
		t.Fatalf("ExchangeOAuthCode(drive): %v", err)
	}
	if tokens.AccessToken != "drive-access" || tokens.RefreshToken != "drive-refresh" {
		t.Fatalf("tokens: %+v", tokens)
	}
	if gotRedirect != redirectURI {
		t.Fatalf("exchange redirect_uri: %q, want the per-flow URI %q", gotRedirect, redirectURI)
	}
	if gotVerifier == "" {
		t.Fatalf("exchange sent no code_verifier")
	}
	if got := challengeOf(gotVerifier); got != u.Query().Get("code_challenge") {
		t.Fatalf("code_verifier does not match code_challenge")
	}
	if gotSecret != "drive-secret" {
		t.Fatalf("self-built secret must still be sent when filled in, got %q", gotSecret)
	}
	// Single-use: the state is consumed, the poll now says expired.
	if res, _ := a.PollOAuthCallback(start.State); res.Status != "expired" {
		t.Fatalf("poll after exchange: %+v, want expired", res)
	}
}

// TestOAuthCallbackWrongState: a redirect carrying an unknown
// state stores nothing — a stray hit or a forged redirect cannot
// inject a code into a live flow.
func TestOAuthCallbackWrongState(t *testing.T) {
	a := newTestApp(t)
	start, err := a.BeginOAuth(domain.ProviderDropbox, "dbx-id")
	if err != nil {
		t.Fatalf("BeginOAuth(dropbox): %v", err)
	}
	u, _ := url.Parse(start.AuthURL)
	redirectURI := u.Query().Get("redirect_uri")
	resp, err := http.Get(redirectURI + "/?code=forged-code&state=not-a-real-state")
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("forged callback status: %d, want 400", resp.StatusCode)
	}
	if res, _ := a.PollOAuthCallback(start.State); res.Status != "waiting" {
		t.Fatalf("poll after forged callback: %+v, want waiting", res)
	}
	a.CancelOAuth(start.State)
	if res, _ := a.PollOAuthCallback(start.State); res.Status != "expired" {
		t.Fatalf("poll after cancel: %+v, want expired", res)
	}
}

// TestOAuthCallbackDenied: the provider refusing (access_denied)
// is reported as an error poll result, not a silent hang.
func TestOAuthCallbackDenied(t *testing.T) {
	a := newTestApp(t)
	start, err := a.BeginOAuth(domain.ProviderOneDrive, "od-id")
	if err != nil {
		t.Fatalf("BeginOAuth(onedrive): %v", err)
	}
	u, _ := url.Parse(start.AuthURL)
	resp, err := http.Get(u.Query().Get("redirect_uri") + "/?error=access_denied&error_description=user+denied&state=" + url.QueryEscape(start.State))
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("denied callback status: %d", resp.StatusCode)
	}
	res, err := a.PollOAuthCallback(start.State)
	if err != nil {
		t.Fatalf("poll after denied: %v", err)
	}
	if res.Status != "error" || !strings.Contains(res.Message, "access_denied") {
		t.Fatalf("poll after denied: %+v", res)
	}
	a.CancelOAuth(start.State)
}
