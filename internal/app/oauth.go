package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/luckzk/zy-drive/internal/auth"
	"github.com/luckzk/zy-drive/internal/domain"
)

// oauth.go — the desktop OAuth Authorization Code flow for Dropbox,
// Google Drive, Drime and OneDrive.
//
// Flow (A, loopback callback first, manual paste as fallback):
//   1. BeginOAuth starts a temporary HTTP listener on 127.0.0.1
//      with a RANDOM port (never a fixed one) that lives only for
//      this one authorization, and builds the authorize URL with
//      redirect_uri=http://localhost:<random-port> (no path).
//      The browser authorizes and jumps back to the loopback
//      address; the listener validates `state`, catches the `code`,
//      answers a small "授权成功，可以关掉这个页面" page and
//      closes itself immediately. The frontend polls
//      PollOAuthCallback and, once the code lands, exchanges it
//      automatically — the user never copies a code by hand.
//   2. If the browser does not jump back (loopback blocked, the
//      listener could not bind, the user closed the tab), the code
//      is still visible in the browser address bar: the wizard
//      keeps the manual paste box and "换取 Token" button, and the
//      poll gives up after the wait with a plain-language message
//      ("浏览器没有跳回，请复制地址栏里的 code 粘贴到下面").
//      The paste fallback covers a failed delivery, not a rejected
//      redirect URI: if the provider itself answers
//      invalid_redirect_uri, its error page names the URI —
//      register exactly what it shows in the app console and
//      retry.
//
// Redirect URI registration (once per app, in the provider's
// console): register the bare loopback URI `http://localhost`
// (no port). Google and Microsoft accept any port on loopback
// for native/desktop clients (RFC 8252 §7.3); Dropbox accepts a
// bare `http://localhost` registration with a runtime port —
// the loopback form in its OAuth guide. The exchange must send
// the SAME redirect_uri the authorize URL carried, so the
// per-flow URI is stored with the state (not a constant).
//
// Why no bundled client secret: there is no shipped shared
// secret. The builtin one-click flow (B, internal/app/
// builtin_oauth.go) uses public clients + PKCE only — no secret
// is ever sent for those. Self-built apps (advanced fold) may
// still fill in a secret; ExchangeCode sends it only when
// non-empty. Pasting a refresh/access token directly stays
// available as a fallback for users who already have one.
//
// PKCE (RFC 7636, S256) is on for ALL four providers now, not
// just Drime/OneDrive: a public client (the builtin ZyDrive app)
// has no secret to send, and PKCE is the proof of possession
// instead. Sending a code_verifier alongside a client secret is
// accepted by all four token endpoints, so self-built apps keep
// working unchanged.
//
// Drime specifics (docs 2026-11-17): desktop apps are public
// clients, NO client secret is ever sent (Drime refuses one);
// the code is valid for 60 seconds and works once; the user must
// create the Drime application with "The whole Drive" access.
// OneDrive (Microsoft identity platform) is the same shape:
// public client ("Mobile and desktop applications"), PKCE S256,
// no secret; scopes Files.ReadWrite.All + offline_access, and
// the "common" tenant accepts personal and work/school accounts.

type oauthProvider struct {
	authEndpoint string
	tokenURL     string
	scopes       []string
	extra        map[string]string // extra authorize-query params
	pkce         bool              // PKCE (RFC 7636, S256); on for every provider now
}

var oauthProviders = map[domain.ProviderKind]oauthProvider{
	domain.ProviderDropbox: {
		authEndpoint: "https://www.dropbox.com/oauth2/authorize",
		tokenURL:     "https://api.dropboxapi.com/oauth2/token",
		scopes:       []string{"files.metadata.read", "files.content.read", "files.content.write", "files.metadata.write"},
		extra:        map[string]string{"token_access_type": "offline"},
		pkce:         true,
	},
	domain.ProviderDrive: {
		authEndpoint: "https://accounts.google.com/o/oauth2/v2/auth",
		tokenURL:     "https://oauth2.googleapis.com/token",
		scopes:       []string{"https://www.googleapis.com/auth/drive"},
		extra:        map[string]string{"access_type": "offline", "prompt": "consent"},
		pkce:         true,
	},
	domain.ProviderDrime: {
		authEndpoint: "https://app.drime.cloud/oauth/authorize",
		tokenURL:     "https://app.drime.cloud/oauth/token",
		scopes:       []string{"files.read", "files.write", "folders.read", "folders.write", "sharing.read", "sharing.write"},
		pkce:         true,
	},
	domain.ProviderOneDrive: {
		authEndpoint: "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
		tokenURL:     "https://login.microsoftonline.com/common/oauth2/v2.0/token",
		// Files.ReadWrite.All is the file access; offline_access is
		// what makes Microsoft issue a refresh token at all.
		// Personal and work/school accounts both work ("common").
		scopes: []string{"Files.ReadWrite.All", "offline_access"},
		pkce:   true,
	},
}

// oauthPending is one in-flight authorization: the state the
// exchange must present, the PKCE verifier, the per-flow loopback
// redirect URI, and — once the browser jumps back — the caught
// code. Single-use: ExchangeOAuthCode deletes the state it
// accepts, so a captured code URL cannot be replayed after the
// exchange. The listener is closed as soon as the code lands
// (or when the flow expires / is cancelled); the pending entry
// itself lives until the exchange consumes it or it expires, so
// PollOAuthCallback can keep reading the outcome.
type oauthPending struct {
	expiry      time.Time
	verifier    string // PKCE code_verifier
	redirectURI string // http://localhost:<random-port>, this flow only
	code        string // set by the callback handler
	codeErr     string // provider-side error (e.g. access_denied)
	done        chan struct{}
	listener    net.Listener
	server      *http.Server
	closeOnce   sync.Once
}

func (p *oauthPending) closeListener() {
	p.closeOnce.Do(func() {
		if p.server != nil {
			// Close (not Shutdown): the response has already been
			// written by the time this runs; there is nothing left
			// to drain, and the port must be freed at once.
			_ = p.server.Close()
		} else if p.listener != nil {
			_ = p.listener.Close()
		}
	})
}

var (
	oauthMu     sync.Mutex
	oauthStates = map[string]*oauthPending{}
)

// OAuthStart is what BeginOAuth returns: the URL to open + the state
// the exchange (or the poll) must present back.
type OAuthStart struct {
	AuthURL string `json:"authUrl"`
	State   string `json:"state"`
}

// OAuthTokens is what ExchangeOAuthCode returns: the tokens the wizard
// puts into the AddMount form (memory only until AddMount saves).
type OAuthTokens struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken,omitempty"`
}

// OAuthCallbackResult is what PollOAuthCallback returns. Status is
// one of "waiting" (browser has not jumped back yet), "done"
// (code caught, ready to exchange), "error" (the provider refused,
// e.g. access_denied — Message carries it in plain language) or
// "expired" (unknown / used / timed-out state).
type OAuthCallbackResult struct {
	Status  string `json:"status"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// BeginOAuth builds the authorize URL for a provider and starts the
// one-shot loopback listener that will catch the code. The user
// opens the URL in their own browser; no token is involved yet.
func (a *App) BeginOAuth(provider domain.ProviderKind, clientID string) (*OAuthStart, error) {
	p, ok := oauthProviders[provider]
	if !ok {
		return nil, fmt.Errorf("app: %s 不支持 OAuth 授权流程", provider)
	}
	if strings.TrimSpace(clientID) == "" {
		return nil, fmt.Errorf("app: 需要先填 OAuth 应用的 Client ID")
	}
	state, err := auth.NewState()
	if err != nil {
		return nil, err
	}
	verifier, err := auth.NewPKCEVerifier()
	if err != nil {
		return nil, err
	}
	// One-shot loopback listener: 127.0.0.1 only (never 0.0.0.0),
	// random port (never a fixed one — the port is minted per flow
	// and the authorize URL carries it). If the bind fails (a
	// sandbox that forbids listening at all), the flow still
	// proceeds: the user can paste the code by hand, and the
	// frontend says so instead of failing the whole authorization.
	pending := &oauthPending{
		expiry:   time.Now().Add(10 * time.Minute),
		verifier: verifier,
		done:     make(chan struct{}),
	}
	if ln, err := net.Listen("tcp", "127.0.0.1:0"); err == nil {
		port := ln.Addr().(*net.TCPAddr).Port
		pending.redirectURI = fmt.Sprintf("http://localhost:%d", port)
		pending.listener = ln
		pending.server = &http.Server{Handler: http.HandlerFunc(oauthCallbackHandler)}
		go func() {
			// Serve returns ErrServerClosed on closeListener; any
			// other error just ends this one flow.
			_ = pending.server.Serve(ln)
		}()
	} else {
		// No listener: fall back to the bare loopback URI so the
		// authorize URL is still valid; the code then only ever
		// arrives by manual paste (the browser cannot jump back
		// to a listener that does not exist).
		pending.redirectURI = "http://localhost"
	}
	oauthMu.Lock()
	oauthStates[state] = pending
	oauthMu.Unlock()
	// Expiry janitor for this one flow: the state is dropped at
	// expiry at the latest, and the listener is freed then too.
	// If the code lands first, the entry stays readable until
	// expiry so Poll/Exchange can still consume it — the handler
	// already closed the listener right after its response.
	go func() {
		timer := time.NewTimer(time.Until(pending.expiry))
		defer timer.Stop()
		select {
		case <-pending.done:
			<-timer.C
		case <-timer.C:
		}
		oauthMu.Lock()
		if cur, ok := oauthStates[state]; ok && cur == pending {
			delete(oauthStates, state)
		}
		oauthMu.Unlock()
		pending.closeListener()
	}()

	q := url.Values{}
	q.Set("client_id", strings.TrimSpace(clientID))
	q.Set("response_type", "code")
	q.Set("redirect_uri", pending.redirectURI)
	q.Set("scope", strings.Join(p.scopes, " "))
	q.Set("state", state)
	for k, v := range p.extra {
		q.Set(k, v)
	}
	q.Set("code_challenge", auth.ChallengeForVerifier(verifier))
	q.Set("code_challenge_method", "S256")
	return &OAuthStart{AuthURL: p.authEndpoint + "?" + q.Encode(), State: state}, nil
}

// oauthCallbackHandler serves the one-shot loopback listener. It
// only accepts a request whose `state` names a pending flow; the
// code (or the provider's error) is stored on that flow and the
// listener is closed right after the response is written, so the
// port lives for exactly one authorization. A request with an
// unknown state gets a plain error page and stores nothing —
// a stray browser hit or a forged redirect cannot inject a code.
func oauthCallbackHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	state := r.URL.Query().Get("state")
	oauthMu.Lock()
	pending, known := oauthStates[state]
	oauthMu.Unlock()
	if !known || time.Now().After(pending.expiry) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>授权失败</title></head><body style="font-family:sans-serif;text-align:center;padding-top:20vh"><h1>这个授权链接已经失效了</h1><p>请回到 ZyDrive，重新点一次“生成授权链接”。</p></body></html>`)
		return
	}
	if errCode := r.URL.Query().Get("error"); errCode != "" {
		desc := r.URL.Query().Get("error_description")
		msg := errCode
		if desc != "" {
			msg = errCode + "：" + desc
		}
		pending.codeErr = msg
		close(pending.done)
		_, _ = fmt.Fprint(w, `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>授权被拒绝</title></head><body style="font-family:sans-serif;text-align:center;padding-top:20vh"><h1>授权没有完成</h1><p>对方说：`+htmlEscape(msg)+`</p><p>可以关掉这个页面，回到 ZyDrive 再试一次。</p></body></html>`)
		go func() { time.Sleep(200 * time.Millisecond); pending.closeListener() }()
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>授权失败</title></head><body style="font-family:sans-serif;text-align:center;padding-top:20vh"><h1>没有拿到授权码</h1><p>请回到 ZyDrive，重新点一次“生成授权链接”。</p></body></html>`)
		return
	}
	pending.code = code
	close(pending.done)
	_, _ = fmt.Fprint(w, `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>授权成功</title></head><body style="font-family:sans-serif;text-align:center;padding-top:20vh"><h1>授权成功</h1><p>已拿到授权码，正在换取 Token…可以关掉这个页面，回到 ZyDrive。</p></body></html>`)
	// Give the browser a beat to read the page before the port
	// goes away; closing mid-flush would truncate the response.
	go func() { time.Sleep(200 * time.Millisecond); pending.closeListener() }()
}

// htmlEscape is the tiny escaper for the one user-controlled string
// (the provider's error_description) in the callback error page.
func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// PollOAuthCallback reports whether the browser has jumped back
// with a code for this state yet. It never blocks: the frontend
// polls it while it waits. "expired" also covers a state the
// exchange already consumed — polling stops once a code is
// exchanged, so that is never a live flow.
func (a *App) PollOAuthCallback(state string) (*OAuthCallbackResult, error) {
	oauthMu.Lock()
	pending, known := oauthStates[state]
	oauthMu.Unlock()
	if !known {
		return &OAuthCallbackResult{Status: "expired", Message: "授权链接已失效或已被用过，请重新点“生成授权链接”"}, nil
	}
	if time.Now().After(pending.expiry) {
		return &OAuthCallbackResult{Status: "expired", Message: "授权链接已过期（10 分钟），请重新生成"}, nil
	}
	select {
	case <-pending.done:
		if pending.codeErr != "" {
			return &OAuthCallbackResult{Status: "error", Message: "对方拒绝了授权：" + pending.codeErr}, nil
		}
		if pending.code != "" {
			return &OAuthCallbackResult{Status: "done", Code: pending.code}, nil
		}
		return &OAuthCallbackResult{Status: "error", Message: "授权没有返回 code，请重新点“生成授权链接”"}, nil
	default:
		return &OAuthCallbackResult{Status: "waiting"}, nil
	}
}

// CancelOAuth drops a pending flow (dialog closed, user gave up)
// and frees its loopback port at once instead of waiting out the
// 10-minute expiry.
func (a *App) CancelOAuth(state string) {
	oauthMu.Lock()
	pending, known := oauthStates[state]
	if known {
		delete(oauthStates, state)
	}
	oauthMu.Unlock()
	if known {
		pending.closeListener()
	}
}

// ExchangeOAuthCode swaps an authorization code for tokens —
// either the code the loopback listener caught (auto flow) or
// one pasted by hand (fallback). tokenURL is empty in production
// (per-provider default); tests pass a mock token endpoint here.
// The state must be one BeginOAuth issued and not yet used. The
// redirect_uri sent here is the one BeginOAuth minted for this
// flow, not a constant: the provider checks it matches the
// authorize URL exactly.
func (a *App) ExchangeOAuthCode(provider domain.ProviderKind, clientID, clientSecret, code, state, tokenURL string) (*OAuthTokens, error) {
	p, ok := oauthProviders[provider]
	if !ok {
		return nil, fmt.Errorf("app: %s 不支持 OAuth 授权流程", provider)
	}
	if strings.TrimSpace(code) == "" {
		return nil, fmt.Errorf("app: 需要粘贴授权码（code）")
	}
	oauthMu.Lock()
	pending, known := oauthStates[state]
	if known {
		delete(oauthStates, state)
	}
	oauthMu.Unlock()
	if !known {
		return nil, fmt.Errorf("app: 授权链接已失效或已被用过，请重新点“生成授权链接”")
	}
	// The exchange ends this flow either way: free the port now
	// (a manual paste never touched it, an auto flow already
	// closed it after the response — Close is idempotent).
	pending.closeListener()
	if time.Now().After(pending.expiry) {
		return nil, fmt.Errorf("app: 授权链接已过期（10 分钟），请重新生成")
	}
	if tokenURL == "" {
		tokenURL = p.tokenURL
	}
	access, refresh, err := auth.ExchangeCode(context.Background(), &auth.OAuthConfig{
		ClientID:     strings.TrimSpace(clientID),
		ClientSecret: clientSecret,
		TokenURL:     tokenURL,
		RedirectURI:  pending.redirectURI,
		CodeVerifier: pending.verifier,
	}, strings.TrimSpace(code))
	if err != nil {
		return nil, err
	}
	return &OAuthTokens{AccessToken: access, RefreshToken: refresh}, nil
}
