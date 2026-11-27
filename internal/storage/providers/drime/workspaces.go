// SPDX-License-Identifier: MPL-2.0
package drime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/luckzk/zy-drive/internal/netproxy"
	"github.com/luckzk/zy-drive/internal/storage"
	shttp "github.com/luckzk/zy-drive/internal/storage/http"
)

// Workspace is one Drime workspace the token can see: the personal
// workspace (id 0) plus every team workspace the account belongs to.
// Role is the token holder's role in that workspace ("Owner",
// "Workspace Owner", "Member", ...); "" when the server does not
// report one.
type Workspace struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Role string `json:"role,omitempty"`
}

// PersonalWorkspaceID is the personal workspace: always present,
// always id 0, and the server's list never includes it — the caller
// adds it (see ListWorkspaces).
const PersonalWorkspaceID = 0

// PersonalWorkspaceName is the display name of workspace 0.
const PersonalWorkspaceName = "个人空间"

// ListWorkspaces calls GET /me/workspaces with a bare access token
// and returns the workspaces with the personal workspace first.
// It needs no provider instance and no refresh: the add-mount
// wizard calls it before any mount exists, with the personal access
// token the user just pasted (a PAT never expires, so no refresh
// path is needed here). apiBase "" means the production API;
// tests pass their mock server's base.
//
// The workspace list runs before any mount exists, so there is no
// per-mount proxy to use yet: ListWorkspaces follows the current
// global proxy. ListWorkspacesWithProxy is the per-config variant
// for callers that already resolved one (v0.18.0).
//
// The response shape is {"workspaces": [{"id": N, "name": "...",
// "currentUser": {"role_name": "..."}}]} (verified 2026-11-19:
// workspace 8762 is "sese", holder is Workspace Owner there).
// The personal workspace is not in the server's list, so it is
// prepended here; a server that does list id 0 is not duplicated.
func ListWorkspaces(ctx context.Context, apiBase, token string) ([]Workspace, error) {
	return ListWorkspacesWithProxy(ctx, apiBase, token, netproxy.Current())
}

// ListWorkspacesWithProxy is ListWorkspaces on a transport bound
// to one proxy config (see ListWorkspaces).
func ListWorkspacesWithProxy(ctx context.Context, apiBase, token string, proxy netproxy.Config) ([]Workspace, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("drime: 需要先粘贴个人访问令牌，才能拉工作区列表")
	}
	if strings.TrimSpace(apiBase) == "" {
		apiBase = defaultAPIBase
	}
	apiBase = strings.TrimSuffix(strings.TrimSpace(apiBase), "/")
	client := shttp.NewWithProxy(drimePolicy, proxy)
	client.HTTP = &http.Client{
		Timeout:   30 * time.Second,
		Transport: staticBearerTransport{base: netproxy.TransportOrShared(proxy), token: strings.TrimSpace(token)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/me/workspaces", nil)
	if err != nil {
		return nil, fmt.Errorf("drime: 工作区列表：%w", err)
	}
	req.GetBody = func() (io.ReadCloser, error) { return http.NoBody, nil }
	resp, err := client.Do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("drime: 工作区列表：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, readAPIError(resp)
	}
	var body struct {
		Workspaces []struct {
			ID          int    `json:"id"`
			Name        string `json:"name"`
			CurrentUser struct {
				RoleName string `json:"role_name"`
			} `json:"currentUser"`
		} `json:"workspaces"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("drime: 工作区列表：返回内容解析失败：%w", err)
	}
	seen := map[int]bool{}
	out := []Workspace{{ID: PersonalWorkspaceID, Name: PersonalWorkspaceName}}
	seen[PersonalWorkspaceID] = true
	for _, w := range body.Workspaces {
		if w.ID < 0 || seen[w.ID] {
			continue
		}
		seen[w.ID] = true
		name := strings.TrimSpace(w.Name)
		if name == "" {
			name = fmt.Sprintf("工作区 %d", w.ID)
		}
		out = append(out, Workspace{ID: w.ID, Name: name, Role: strings.TrimSpace(w.CurrentUser.RoleName)})
	}
	return out, nil
}

// staticBearerTransport attaches one fixed Bearer token (the
// one-shot workspace list; no refresh, no token source).
type staticBearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t staticBearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req2 := req.Clone(req.Context())
	req2.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(req2)
}

// readAPIError mirrors Provider.readError for the standalone call:
// a short human-readable message from the response body, never a
// raw dump.
func readAPIError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var body struct {
		Message string         `json:"message"`
		Errors  map[string]any `json:"errors"`
	}
	_ = json.Unmarshal(data, &body)
	msg := body.Message
	if msg == "" {
		msg = strings.TrimSpace(string(data))
	}
	if msg == "" {
		msg = resp.Status
	}
	if resp.StatusCode == http.StatusUnauthorized {
		msg = "令牌不对或已失效，请检查粘贴的个人访问令牌（" + msg + "）"
	}
	return &storage.ProviderError{Op: "ListWorkspaces", Provider: "drime", Err: fmt.Errorf("drime api %d: %s", resp.StatusCode, msg)}
}
