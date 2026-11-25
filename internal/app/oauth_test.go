// SPDX-License-Identifier: MPL-2.0
package app

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
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

// TestExchangeRetryAfterFailureKeepsState (Dropbox one-click fix,
// 2026-11-25): the token endpoint failing must NOT burn the flow.
// The first exchange fails (provider 500); the same code + state
// retried must succeed — with the same code_verifier and
// redirect_uri the authorize URL carried. Only a SUCCESSFUL
// exchange consumes the state: a third exchange then reports the
// state as used. Before the fix the state was deleted before the
// token call, so the retry died with the misleading
// "授权链接已失效或已被用过" and the user was stuck.
func TestExchangeRetryAfterFailureKeepsState(t *testing.T) {
	a := newTestApp(t)
	start, err := a.BeginOAuth(domain.ProviderDropbox, "dbx-id")
	if err != nil {
		t.Fatalf("BeginOAuth(dropbox): %v", err)
	}
	u, _ := url.Parse(start.AuthURL)
	redirectURI := u.Query().Get("redirect_uri")
	challenge := u.Query().Get("code_challenge")

	var calls []struct{ verifier, redirect, code string }
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		calls = append(calls, struct{ verifier, redirect, code string }{
			r.Form.Get("code_verifier"), r.Form.Get("redirect_uri"), r.Form.Get("code"),
		})
		n := len(calls)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "temporarily_unavailable", "error_description": "try again later"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "dbx-access", "refresh_token": "dbx-refresh",
			"expires_in": 3600, "token_type": "Bearer",
		})
	}))
	defer srv.Close()

	_, err = a.ExchangeOAuthCode(domain.ProviderDropbox, "dbx-id", "", "code-1", start.State, srv.URL)
	if err == nil {
		t.Fatal("first exchange should fail (mock 500)")
	}
	if strings.Contains(err.Error(), "已失效或已被用过") {
		t.Fatalf("failed exchange must NOT report the state as used: %v", err)
	}
	if !strings.Contains(err.Error(), "再试一次") {
		t.Fatalf("failed exchange must say the code can be retried: %v", err)
	}
	if strings.Contains(err.Error(), "auth: token exchange failed") {
		t.Fatalf("failed exchange must not leak the raw provider error: %v", err)
	}

	tokens, err := a.ExchangeOAuthCode(domain.ProviderDropbox, "dbx-id", "", "code-1", start.State, srv.URL)
	if err != nil {
		t.Fatalf("retry with the same code+state must succeed: %v", err)
	}
	if tokens.AccessToken != "dbx-access" {
		t.Fatalf("tokens: %+v", tokens)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("token endpoint calls: got %d, want 2", len(calls))
	}
	for i, c := range calls {
		if c.code != "code-1" || c.redirect != redirectURI || c.verifier == "" || challengeOf(c.verifier) != challenge {
			t.Fatalf("call %d: code=%q redirect=%q verifier-matches-challenge=%v", i+1, c.code, c.redirect, challengeOf(c.verifier) == challenge)
		}
	}
	// Success consumed the state: a third exchange reports it used.
	if _, err := a.ExchangeOAuthCode(domain.ProviderDropbox, "dbx-id", "", "code-1", start.State, srv.URL); err == nil || !strings.Contains(err.Error(), "已失效或已被用过") {
		t.Fatalf("third exchange: got err %v, want the state-used message", err)
	}
}

// TestExchangeOldCodeAfterRegenerate (Dropbox one-click fix,
// 2026-11-25): regenerating the link voids the old flow. The old
// state's exchange must fail as "used", and a code issued for the
// old flow exchanged against the new flow's verifier/redirect
// must fail with invalid_grant — reported in plain language
// ("重新点'生成授权链接'"), never the raw provider string —
// while the new flow's state stays alive for a retry with the
// right code.
func TestExchangeOldCodeAfterRegenerate(t *testing.T) {
	a := newTestApp(t)
	start1, err := a.BeginOAuth(domain.ProviderDropbox, "dbx-id")
	if err != nil {
		t.Fatalf("BeginOAuth #1: %v", err)
	}
	start2, err := a.BeginOAuth(domain.ProviderDropbox, "dbx-id")
	if err != nil {
		t.Fatalf("BeginOAuth #2: %v", err)
	}
	// The wizard drops the previous link before minting a new one.
	a.CancelOAuth(start1.State)
	if _, err := a.ExchangeOAuthCode(domain.ProviderDropbox, "dbx-id", "", "code-for-flow-1", start1.State, ""); err == nil || !strings.Contains(err.Error(), "已失效或已被用过") {
		t.Fatalf("old state exchange: got err %v, want the state-used message", err)
	}

	// Mock token endpoint: only the code issued for THIS flow
	// ("code-for-flow-2") is accepted; anything else is an
	// invalid_grant (wrong verifier / redirect in real life).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("code") != "code-for-flow-2" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant", "error_description": "code has expired or was already used"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "dbx-access-2", "refresh_token": "dbx-refresh-2",
			"expires_in": 3600, "token_type": "Bearer",
		})
	}))
	defer srv.Close()

	_, err = a.ExchangeOAuthCode(domain.ProviderDropbox, "dbx-id", "", "code-for-flow-1", start2.State, srv.URL)
	if err == nil {
		t.Fatal("old code against the new flow must fail")
	}
	if strings.Contains(err.Error(), "auth: token exchange failed") || strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("invalid_grant must be translated to plain language, got: %v", err)
	}
	if !strings.Contains(err.Error(), "重新点") {
		t.Fatalf("invalid_grant must tell the user to regenerate the link, got: %v", err)
	}
	// The state survived the failed exchange: the right code works.
	tokens, err := a.ExchangeOAuthCode(domain.ProviderDropbox, "dbx-id", "", "code-for-flow-2", start2.State, srv.URL)
	if err != nil {
		t.Fatalf("retry with the flow's own code must succeed: %v", err)
	}
	if tokens.AccessToken != "dbx-access-2" {
		t.Fatalf("tokens: %+v", tokens)
	}
}

// TestExchangeWithStaleStateReproducesExpiredMessage (2026-11-25,
// user report on 0.8.4): the browser jump-back lands, the poll
// returns the code, the code IS filled into the box — and the UI
// still shows "授权链接已失效或已被用过". The trigger is the
// frontend's stale React closure: the poll callback was created in
// the render where `oauthState` was still the OLD value ('' on the
// first flow, the previous flow's cancelled state on a retry),
// so the automatic exchange presents the wrong state and the
// backend answers "unknown state" — before any network call to
// the token endpoint. The fix (frontend) passes the state
// explicitly from startWaiting; this test pins the backend half:
// the same code exchanged with '' fails with exactly that
// message, and with the flow's own state it succeeds against a
// mock token endpoint.
func TestExchangeWithStaleStateReproducesExpiredMessage(t *testing.T) {
	a := newTestApp(t)
	start, err := a.BeginOAuth(domain.ProviderDropbox, "dbx-id")
	if err != nil {
		t.Fatalf("BeginOAuth(dropbox): %v", err)
	}
	u, _ := url.Parse(start.AuthURL)
	redirectURI := u.Query().Get("redirect_uri")

	// Browser jump-back (simulated with a plain GET, as in
	// TestOAuthCallbackAutoFlow).
	resp, err := http.Get(redirectURI + "/?code=repro-code-1&state=" + url.QueryEscape(start.State))
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	_ = resp.Body.Close()
	res, err := a.PollOAuthCallback(start.State)
	if err != nil || res.Status != "done" || res.Code != "repro-code-1" {
		t.Fatalf("poll after callback: %+v, err %v", res, err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "dbx-access", "refresh_token": "dbx-refresh",
			"expires_in": 3600, "token_type": "Bearer",
		})
	}))
	defer srv.Close()

	// The stale closure's exchange: state '' on the first flow.
	_, err = a.ExchangeOAuthCode(domain.ProviderDropbox, "dbx-id", "", res.Code, "", srv.URL)
	if err == nil {
		t.Fatal("exchange with an empty (stale) state must fail")
	}
	if !strings.Contains(err.Error(), "已失效或已被用过") {
		t.Fatalf("stale-state exchange must report the expired link, got: %v", err)
	}
	// The failed exchange must NOT have consumed the code: the
	// same code with the flow's own state still succeeds.
	tokens, err := a.ExchangeOAuthCode(domain.ProviderDropbox, "dbx-id", "", res.Code, start.State, srv.URL)
	if err != nil {
		t.Fatalf("exchange with the correct state must succeed after the stale attempt: %v", err)
	}
	if tokens.AccessToken != "dbx-access" {
		t.Fatalf("tokens: %+v", tokens)
	}
}

// TestHumanExchangeErrorProxyKinds (2026-11-25): the three network
// failures must read differently — the user must be able to tell
// "my proxy is not running" from "the proxy cannot reach Dropbox"
// from "direct is blocked", each with its one next action. None
// of them may ask for a re-authorization (the code is still good).
func TestHumanExchangeErrorProxyKinds(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want []string
		not  []string
	}{
		{
			name: "proxy-down names the proxy address",
			err:  fmt.Errorf("auth: token exchange request [proxy-kind=proxy-down proxy=127.0.0.1:7890]: dial tcp 127.0.0.1:7890: connect: connection refused"),
			want: []string{"连不上本机代理（127.0.0.1:7890）", "检查代理软件是否在运行", "重新换取", "不用重新授权"},
		},
		{
			name: "proxy-target blames the proxy route, not the code",
			err:  fmt.Errorf("auth: token exchange request [proxy-kind=proxy-target proxy=127.0.0.1:8001]: context deadline exceeded"),
			want: []string{"代理已连上", "连不上对方的换取接口", "重新换取", "不用重新授权"},
		},
		{
			name: "direct points at Settings Network",
			err:  fmt.Errorf("auth: token exchange request [proxy-kind=direct proxy=]: dial tcp: i/o timeout"),
			want: []string{"直连", "设置 → 网络", "重新换取", "不用重新授权"},
		},
		{
			name: "invalid_grant still asks for a new link",
			err:  fmt.Errorf("auth: token exchange failed: invalid_grant: code has expired"),
			want: []string{"重新点“生成授权链接”"},
			not:  []string{"重新换取"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := humanExchangeError(tc.err).Error()
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Fatalf("want %q in %q", w, got)
				}
			}
			for _, w := range tc.not {
				if strings.Contains(got, w) {
					t.Fatalf("want no %q in %q", w, got)
				}
			}
		})
	}
}
