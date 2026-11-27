// SPDX-License-Identifier: MPL-2.0
package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/luckzk/zy-drive/internal/netproxy"
)

// FormRefresher builds a Refresher that exchanges a refresh token at a
// standard OAuth2 token endpoint (grant_type=refresh_token, form POST).
// It is shared by the Dropbox and Drive drivers so neither hand-rolls
// token refresh. tokenURL/clientID/clientSecret come from the mount's
// stored config (Extra), never from code. The returned Token keeps the
// old refresh token when the endpoint does not rotate it.
func FormRefresher(tokenURL, clientID, clientSecret string) Refresher {
	return FormRefresherWithProxy(tokenURL, clientID, clientSecret, netproxy.Current())
}

// FormRefresherWithProxy is FormRefresher on a transport bound to
// one proxy config (v0.18.0, per-mount proxy): a token refresh must
// travel through the mount's own proxy, not the global one — the
// global proxy may be down or blocked while the mount's proxy
// works, and vice versa. A zero proxy config means "use the
// current global config at call time".
func FormRefresherWithProxy(tokenURL, clientID, clientSecret string, proxy netproxy.Config) Refresher {
	return func(ctx context.Context, refreshToken string) (*Token, error) {
		if refreshToken == "" {
			return nil, fmt.Errorf("oauth: no refresh token stored; re-authorise the account")
		}
		form := url.Values{}
		form.Set("grant_type", "refresh_token")
		form.Set("refresh_token", refreshToken)
		form.Set("client_id", clientID)
		if clientSecret != "" {
			form.Set("client_secret", clientSecret)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL,
			strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		// Proxy-aware client (Settings -> Network), same as the
		// code exchange: a refresh must follow the user's proxy.
		// With WithProxy the mount's own proxy is used; a zero
		// config falls back to the current global one
		// (NewClientFor's rule). The log line records target,
		// proxy and error kind, never a token.
		resp, err := netproxy.NewClientFor(proxy, 60).Do(req)
		if err != nil {
			// Classify against the proxy this request actually
			// used (the mount's own, when set), not the global
			// one: a dead per-mount proxy must read as
			// proxy-down, never as a direct failure.
			effProxy := proxy
			if effProxy == (netproxy.Config{}) {
				effProxy = netproxy.Current()
			}
			kind, proxyAddr := netproxy.ClassifyErrorWith(effProxy, tokenURL, err)
			log.Printf("oauth refresh: %s", netproxy.DescribeErrorWith(effProxy, tokenURL, err))
			return nil, fmt.Errorf("oauth: refresh request [proxy-kind=%s proxy=%s]: %w", kind, proxyAddr, err)
		}
		defer resp.Body.Close()
		var body struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int64  `json:"expires_in"`
			TokenType    string `json:"token_type"`
			Error        string `json:"error"`
			ErrorDesc    string `json:"error_description"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return nil, fmt.Errorf("oauth: refresh response: %w", err)
		}
		if resp.StatusCode != http.StatusOK || body.AccessToken == "" {
			msg := body.Error
			if body.ErrorDesc != "" {
				msg += ": " + body.ErrorDesc
			}
			if msg == "" {
				msg = fmt.Sprintf("token endpoint returned %d", resp.StatusCode)
			}
			return nil, fmt.Errorf("oauth: refresh failed: %s", msg)
		}
		next := &Token{
			AccessToken:  body.AccessToken,
			RefreshToken: body.RefreshToken,
			TokenType:    body.TokenType,
		}
		if next.RefreshToken == "" {
			next.RefreshToken = refreshToken // no rotation: keep the old one
		}
		if body.ExpiresIn > 0 {
			next.Expiry = time.Now().Add(time.Duration(body.ExpiresIn) * time.Second)
		}
		return next, nil
	}
}
