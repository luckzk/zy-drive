package app

import (
	"context"
	"fmt"
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
// Why code + manual copy-paste instead of a localhost callback:
//   - There are no bundled OAuth client credentials: each user creates
//     their own app (Dropbox App Console / Google Cloud Console /
//     Drime developer console / Azure portal app registration) and
//     pastes its client id/secret. Shipping a shared client secret in
//     a public repo would leak it.
//   - A localhost callback needs the user's app to register an exact
//     redirect URI and the desktop window to catch the redirect; that
//     path is unverified in mygo here. The copy-paste flow works with
//     any browser: the backend builds the authorize URL, the user
//     opens it, authorises, and pastes the `code` from the redirected
//     URL back (the browser may show "cannot reach localhost" — the
//     code is still in the address bar). The backend exchanges the
//     code for tokens; tokens are encrypted at rest by AddMount.
//   - Pasting a refresh/access token directly stays available as a
//     fallback for users who already have one (e.g. a Dropbox
//     generated access token, or a Drime personal access token).
//
// Redirect URI is always http://localhost: the user adds it once to
// their app (Dropbox: Settings → Redirect URIs; Google: OAuth client
// → Authorised redirect URIs, client type "Desktop app"; Drime:
// developer console → application Settings → redirect URLs; OneDrive:
// Azure portal → app registration → Authentication → "Mobile and
// desktop applications" → http://localhost).
//
// Drime differs from Dropbox/Google in two ways (docs 2026-11-17):
//   - PKCE is REQUIRED for desktop apps (public clients, S256 only):
//     BeginOAuth generates the verifier and stores it with the
//     state; ExchangeOAuthCode sends it back. The code is valid for
//     60 seconds and works once.
//   - Desktop apps are public clients: NO client secret is ever
//     sent (Drime refuses one). The wizard must not ask for it.
//   - The user must create the Drime application with "The whole
//     Drive" access (a file manager/sync tool): an app created with
//     "A folder of its own" only ever sees its own folder under
//     Applications, and the sync API is unavailable to it. That
//     choice cannot change once someone has connected, so the
//     wizard's help text says so up front.
//
// OneDrive (Microsoft identity platform) is like Drime, not like
// Dropbox/Google: desktop apps are public clients ("Mobile and
// desktop applications" platform), so the flow is authorization
// code + PKCE S256 with no client secret — the same generic RFC 7636
// machinery, not Drime-specific code. A confidential (web)
// registration still works if the user fills in a client secret:
// ExchangeCode sends it only when non-empty, and refresh does the
// same. Scopes are Files.ReadWrite.All (the file access itself)
// plus offline_access (without it Microsoft issues no refresh
// token). The "common" tenant accepts personal and work/school
// accounts alike.

const oauthRedirectURI = "http://localhost"

type oauthProvider struct {
	authEndpoint string
	tokenURL     string
	scopes       []string
	extra        map[string]string // extra authorize-query params
	pkce         bool              // PKCE (RFC 7636, S256) required for public clients (Drime, OneDrive)
}

var oauthProviders = map[domain.ProviderKind]oauthProvider{
	domain.ProviderDropbox: {
		authEndpoint: "https://www.dropbox.com/oauth2/authorize",
		tokenURL:     "https://api.dropboxapi.com/oauth2/token",
		scopes:       []string{"files.metadata.read", "files.content.read", "files.content.write", "files.metadata.write"},
		extra:        map[string]string{"token_access_type": "offline"},
	},
	domain.ProviderDrive: {
		authEndpoint: "https://accounts.google.com/o/oauth2/v2/auth",
		tokenURL:     "https://oauth2.googleapis.com/token",
		scopes:       []string{"https://www.googleapis.com/auth/drive"},
		extra:        map[string]string{"access_type": "offline", "prompt": "consent"},
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

// oauthStates holds pending CSRF states: state -> pending exchange
// (expiry + PKCE verifier when the provider needs one). Single-use:
// ExchangeOAuthCode deletes the state it accepts, so a captured code
// URL cannot be replayed after the exchange.
type oauthPending struct {
	expiry   time.Time
	verifier string // PKCE code_verifier, "" when unused
}

var (
	oauthMu     sync.Mutex
	oauthStates = map[string]oauthPending{}
)

// OAuthStart is what BeginOAuth returns: the URL to open + the state
// the exchange must present back.
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

// BeginOAuth builds the authorize URL for a provider. The user opens
// it in their own browser; no token is involved yet.
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
	verifier := ""
	q := url.Values{}
	q.Set("client_id", strings.TrimSpace(clientID))
	q.Set("response_type", "code")
	q.Set("redirect_uri", oauthRedirectURI)
	q.Set("scope", strings.Join(p.scopes, " "))
	q.Set("state", state)
	for k, v := range p.extra {
		q.Set(k, v)
	}
	if p.pkce {
		// Public clients (Drime, OneDrive desktop apps): PKCE
		// (S256) is required and no secret is ever sent. The
		// verifier is stored with the state and presented at the
		// exchange.
		v, err := auth.NewPKCEVerifier()
		if err != nil {
			return nil, err
		}
		verifier = v
		q.Set("code_challenge", auth.ChallengeForVerifier(v))
		q.Set("code_challenge_method", "S256")
	}
	oauthMu.Lock()
	oauthStates[state] = oauthPending{expiry: time.Now().Add(10 * time.Minute), verifier: verifier}
	oauthMu.Unlock()
	return &OAuthStart{AuthURL: p.authEndpoint + "?" + q.Encode(), State: state}, nil
}

// ExchangeOAuthCode swaps a pasted authorization code for tokens.
// tokenURL is empty in production (per-provider default); tests pass
// a mock token endpoint here. The state must be one BeginOAuth
// issued and not yet used.
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
		RedirectURI:  oauthRedirectURI,
		CodeVerifier: pending.verifier,
	}, strings.TrimSpace(code))
	if err != nil {
		return nil, err
	}
	return &OAuthTokens{AccessToken: access, RefreshToken: refresh}, nil
}
