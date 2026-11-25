// SPDX-License-Identifier: MPL-2.0
package agentapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/netproxy"
)

// Client is the desktop-side Agent client. It negotiates the protocol
// version first: Health must report ProtocolVersion "1" before any job
// is pushed, and an incompatible Agent is reported as "Agent needs
// upgrade" with both versions shown — never silently run against it.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient returns a Client for an Agent at baseURL (e.g.
// https://agent.example.com, no trailing slash).
func NewClient(baseURL, apiToken string) *Client {
	return &Client{baseURL: baseURL, token: apiToken, http: netproxy.NewClient(30)}
}

// Health checks the Agent and verifies the protocol version.
func (c *Client) Health(ctx context.Context) (*HealthResponse, error) {
	var h HealthResponse
	if err := c.do(ctx, http.MethodGet, "/api/health", nil, &h); err != nil {
		return nil, err
	}
	if h.ProtocolVersion != ProtocolVersion {
		return nil, fmt.Errorf("agentapi: protocol mismatch: agent=%s, desktop=%s — Agent needs upgrade",
			h.ProtocolVersion, ProtocolVersion)
	}
	return &h, nil
}

// PushJob creates or updates a job on the Agent.
func (c *Client) PushJob(ctx context.Context, job domain.SyncJob, encryptedToken string) (*JobResponse, error) {
	var out JobResponse
	err := c.do(ctx, http.MethodPost, "/api/jobs",
		JobRequest{Job: job, EncryptedToken: encryptedToken}, &out)
	return &out, err
}

// ListJobs returns the Agent's jobs — the execution source of truth
// when desktop and Agent disagree.
func (c *Client) ListJobs(ctx context.Context) ([]JobResponse, error) {
	var out []JobResponse
	err := c.do(ctx, http.MethodGet, "/api/jobs", nil, &out)
	return out, err
}

// RunJob triggers an immediate run of a job on the Agent.
func (c *Client) RunJob(ctx context.Context, jobID string) error {
	return c.do(ctx, http.MethodPost, "/api/jobs/"+jobID+"/run", nil, nil)
}

// ListRuns returns the run history for a job, newest first, each
// run with its items.
func (c *Client) ListRuns(ctx context.Context, jobID string) ([]RunResponse, error) {
	var out []RunResponse
	err := c.do(ctx, http.MethodGet, "/api/jobs/"+jobID+"/runs", nil, &out)
	return out, err
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("agentapi: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("agentapi: %s %s: status %d: %s", method, path, resp.StatusCode, string(data))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
