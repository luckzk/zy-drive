// SPDX-License-Identifier: MPL-2.0
package agentapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/luckzk/zy-drive/internal/cache"
	"github.com/luckzk/zy-drive/internal/domain"
)

// fakeExecutor records RunNow calls and job notifications.
type fakeExecutor struct {
	ran     []string
	upserted []domain.SyncJob
	deleted []string
}

func (f *fakeExecutor) RunNow(ctx context.Context, jobID string) (string, error) {
	f.ran = append(f.ran, jobID)
	return "run-123", nil
}

func (f *fakeExecutor) JobUpserted(job domain.SyncJob) { f.upserted = append(f.upserted, job) }
func (f *fakeExecutor) JobDeleted(jobID string)       { f.deleted = append(f.deleted, jobID) }

func newTestServer(t *testing.T) (*Server, *cache.DB, *fakeExecutor) {
	t.Helper()
	db, err := cache.Open(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	srv := NewServer("test-token")
	if err := srv.SetStore(db); err != nil {
		t.Fatal(err)
	}
	exec := &fakeExecutor{}
	srv.SetExecutor(exec)
	return srv, db, exec
}

func doReq(t *testing.T, srv *Server, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(data)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestServerJobsPersistAndValidate(t *testing.T) {
	srv, db, exec := newTestServer(t)
	job := domain.SyncJob{
		ID: "job-1", Name: "照片同步", SrcMount: "mnt-a", DstMount: "mnt-b",
		Direction: domain.DirectionCopy, RunOn: domain.RunOnAgent, AgentID: "ag-1",
		Schedule: "daily 02:00", Enabled: true,
	}
	rec := doReq(t, srv, http.MethodPost, "/api/jobs", "test-token", JobRequest{Job: job})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/jobs = %d, body %s, want 201", rec.Code, rec.Body.String())
	}
	// Persisted: a fresh server over the same DB sees it.
	srv2 := NewServer("test-token")
	if err := srv2.SetStore(db); err != nil {
		t.Fatal(err)
	}
	rec = doReq(t, srv2, http.MethodGet, "/api/jobs", "test-token", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/jobs = %d", rec.Code)
	}
	var jobs []JobResponse
	if err := json.NewDecoder(rec.Body).Decode(&jobs); err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Job.ID != "job-1" || jobs[0].Job.Schedule != "daily 02:00" {
		t.Fatalf("jobs = %+v, want [job-1 daily 02:00]", jobs)
	}
	if len(exec.upserted) != 1 || exec.upserted[0].ID != "job-1" {
		t.Fatalf("executor upserted = %+v, want [job-1]", exec.upserted)
	}
	// A bad schedule is rejected, not stored.
	bad := job
	bad.ID = "job-bad"
	bad.Schedule = "every friday"
	rec = doReq(t, srv, http.MethodPost, "/api/jobs", "test-token", JobRequest{Job: bad})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad schedule POST = %d, want 400", rec.Code)
	}
	// Runs start empty.
	rec = doReq(t, srv, http.MethodGet, "/api/jobs/job-1/runs", "test-token", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /runs = %d", rec.Code)
	}
	var runs []RunResponse
	if err := json.NewDecoder(rec.Body).Decode(&runs); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs = %+v, want []", runs)
	}
	// Immediate run goes to the executor and returns its run id.
	rec = doReq(t, srv, http.MethodPost, "/api/jobs/job-1/run", "test-token", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /run = %d, body %s, want 202", rec.Code, rec.Body.String())
	}
	var runResp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&runResp); err != nil {
		t.Fatal(err)
	}
	if runResp["runId"] != "run-123" {
		t.Fatalf("run response = %+v, want runId run-123", runResp)
	}
	if len(exec.ran) != 1 || exec.ran[0] != "job-1" {
		t.Fatalf("executor ran = %v, want [job-1]", exec.ran)
	}
	// Unknown job: 404.
	rec = doReq(t, srv, http.MethodPost, "/api/jobs/nope/run", "test-token", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST /run unknown job = %d, want 404", rec.Code)
	}
	// No token: 401.
	rec = doReq(t, srv, http.MethodGet, "/api/jobs", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET = %d, want 401", rec.Code)
	}
	// Delete removes the job (and notifies the executor).
	rec = doReq(t, srv, http.MethodDelete, "/api/jobs/job-1", "test-token", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d, want 204", rec.Code)
	}
	if len(exec.deleted) != 1 || exec.deleted[0] != "job-1" {
		t.Fatalf("executor deleted = %v, want [job-1]", exec.deleted)
	}
	jobs2, err := db.ListSyncJobs()
	if err != nil || len(jobs2) != 0 {
		t.Fatalf("jobs in DB after delete = %d, %v, want 0", len(jobs2), err)
	}
}

func TestServerRunWithoutExecutor(t *testing.T) {
	srv, _, _ := newTestServer(t)
	srv.SetExecutor(nil)
	job := domain.SyncJob{ID: "job-1", Name: "x", SrcMount: "a", DstMount: "b",
		Direction: domain.DirectionCopy, RunOn: domain.RunOnAgent}
	rec := doReq(t, srv, http.MethodPost, "/api/jobs", "test-token", JobRequest{Job: job})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d", rec.Code)
	}
	rec = doReq(t, srv, http.MethodPost, "/api/jobs/job-1/run", "test-token", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /run without executor = %d, want 503", rec.Code)
	}
}

func TestServerSSEHello(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/jobs/job-1/events", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	ctx, cancel := context.WithCancel(context.Background())
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.Handler().ServeHTTP(rec, req)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(rec.Body.String(), "event: hello") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	if !strings.Contains(rec.Body.String(), "event: hello") {
		t.Fatalf("SSE body = %q, want a hello event", rec.Body.String())
	}
}
