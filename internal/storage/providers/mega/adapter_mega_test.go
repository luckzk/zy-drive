// SPDX-License-Identifier: MPL-2.0
package mega

// Adapter-level session-restore tests (v0.9.3): a saved MEGA session
// that the server rejects must surface as storage.ErrSessionExpired
// (so LoadMounts can mark the mount auth_expired), while a transport
// failure must NOT — it is transient and retryable. The fake MEGA
// server answers just the `f` (filesystem) command; anything else
// (the background `sc` event poll) gets an empty answer.
import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/luckzk/zy-drive/internal/storage"
)

// rewriteTransport redirects every request at the MEGA API host to
// the fake server (or fails every request when fail is set).
type rewriteTransport struct {
	target string
	fail   error
}

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.fail != nil {
		return nil, rt.fail
	}
	clone := req.Clone(req.Context())
	tu, err := url.Parse(rt.target)
	if err != nil {
		return nil, err
	}
	u := *clone.URL
	u.Scheme = tu.Scheme
	u.Host = tu.Host
	clone.URL = &u
	return http.DefaultTransport.RoundTrip(clone)
}

// fakeMegaServer answers the `f` command with fResponse and
// everything else with an empty array.
func fakeMegaServer(t *testing.T, fResponse string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), `"a":"f"`) {
			_, _ = w.Write([]byte(fResponse))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
}

func sessionCfg() storage.Config {
	return storage.Config{Extra: map[string]string{
		"mega_session_id": "test-session-id",
		"mega_master_key": base64.StdEncoding.EncodeToString([]byte("0123456789abcdef")),
	}}
}

func TestSessionRestoreRejectedIsSessionExpired(t *testing.T) {
	// MEGA answers the `f` command with an error number, as it
	// does for a dead session.
	srv := fakeMegaServer(t, `[-9]`)
	defer srv.Close()
	client := &http.Client{Transport: &rewriteTransport{target: srv.URL}}
	_, err := newWithClient(sessionCfg(), client)
	if err == nil {
		t.Fatal("expected an error for a rejected session, got nil")
	}
	if !errors.Is(err, storage.ErrSessionExpired) {
		t.Fatalf("error should wrap ErrSessionExpired, got: %v", err)
	}
	if !strings.Contains(err.Error(), "session expired") {
		t.Fatalf("error should say the session expired, got: %v", err)
	}
}

func TestSessionRestoreValidSucceeds(t *testing.T) {
	srv := fakeMegaServer(t, `[{"f":[],"ok":[],"sn":"test-sn"}]`)
	defer srv.Close()
	client := &http.Client{Transport: &rewriteTransport{target: srv.URL}}
	p, err := newWithClient(sessionCfg(), client)
	if err != nil {
		t.Fatalf("valid session should restore: %v", err)
	}
	if p == nil {
		t.Fatal("valid session should return a provider, got nil")
	}
	// Note: no List() here — an empty fake FS has no root node, so
	// List would dereference a nil root. Restoring the provider
	// (the login + filesystem fetch) is what this test proves.
}

func TestSessionRestoreNetworkFailureNotSessionExpired(t *testing.T) {
	client := &http.Client{Transport: &rewriteTransport{fail: fmt.Errorf("mock network down")}}
	_, err := newWithClient(sessionCfg(), client)
	if err == nil {
		t.Fatal("expected an error for a dead network, got nil")
	}
	if errors.Is(err, storage.ErrSessionExpired) {
		t.Fatalf("a network failure must NOT be marked session-expired, got: %v", err)
	}
}
