// SPDX-License-Identifier: MPL-2.0
package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExchangeCode(t *testing.T) {
	var gotForm map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotForm = map[string]string{
			"grant_type":    r.Form.Get("grant_type"),
			"code":          r.Form.Get("code"),
			"redirect_uri":  r.Form.Get("redirect_uri"),
			"client_id":     r.Form.Get("client_id"),
			"client_secret": r.Form.Get("client_secret"),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "acc-1", "refresh_token": "ref-1",
			"token_type": "Bearer", "expires_in": 3600,
		})
	}))
	defer srv.Close()
	access, refresh, err := ExchangeCode(context.Background(), &OAuthConfig{
		ClientID: "cid", ClientSecret: "csec", TokenURL: srv.URL,
		RedirectURI: "http://localhost",
	}, "auth-code-1")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if access != "acc-1" || refresh != "ref-1" {
		t.Fatalf("tokens = %q %q", access, refresh)
	}
	if gotForm["grant_type"] != "authorization_code" || gotForm["code"] != "auth-code-1" ||
		gotForm["redirect_uri"] != "http://localhost" || gotForm["client_id"] != "cid" ||
		gotForm["client_secret"] != "csec" {
		t.Fatalf("token request form wrong: %+v", gotForm)
	}
}

func TestExchangeCodeBadCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "invalid_grant", "error_description": "bad code",
		})
	}))
	defer srv.Close()
	_, _, err := ExchangeCode(context.Background(), &OAuthConfig{
		ClientID: "cid", ClientSecret: "csec", TokenURL: srv.URL,
		RedirectURI: "http://localhost",
	}, "nope")
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("expected invalid_grant error, got %v", err)
	}
}
