// SPDX-License-Identifier: MPL-2.0
package agentapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/luckzk/zy-drive/internal/cache"
	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/platform/scheduler"
	"github.com/luckzk/zy-drive/internal/version"
)

// Server is the Agent-side HTTP API. Jobs and runs are persisted in
// the Agent's SQLite database (V0.9.0 — V0.1 kept them in memory, so
// a restart forgot every job and every run record). The in-memory
// map is only a read cache kept in sync with the DB.
//
// The Agent must sit behind HTTPS (reverse proxy terminates TLS)
// with an API token — the port is never exposed bare. The API token
// and cloud credentials are two different secrets: the token guards
// the API, the credentials arrive only via the user's own encrypted
// config bundle (see bundle.go), never over this API.
type Server struct {
	mu    sync.RWMutex
	db    *cache.DB
	jobs  map[string]domain.SyncJob
	token string // API token, compared in constant time

	executor Executor

	subMu sync.Mutex
	subs  map[string][]chan Event
}

// Executor runs jobs on the Agent. It is implemented by
// internal/agentrun.Runner and installed by cmd/agent; the server
// itself never executes a sync.
type Executor interface {
	// RunNow starts an immediate run (async) and returns its run id.
	RunNow(ctx context.Context, jobID string) (string, error)
	// JobUpserted (re)registers a job with the scheduler.
	JobUpserted(job domain.SyncJob)
	// JobDeleted removes a job from the scheduler.
	JobDeleted(jobID string)
}

// NewServer returns a Server. An empty token disables auth only when
// explicitly allowed by the caller (local dev); production must pass one.
func NewServer(apiToken string) *Server {
	return &Server{jobs: map[string]domain.SyncJob{}, token: apiToken, subs: map[string][]chan Event{}}
}

// SetStore attaches the Agent database and loads persisted jobs
// into the read cache. Call once at startup, before serving.
func (s *Server) SetStore(db *cache.DB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.db = db
	if db == nil {
		return nil
	}
	jobs, err := db.ListSyncJobs()
	if err != nil {
		return err
	}
	for _, j := range jobs {
		s.jobs[j.ID] = *j
	}
	return nil
}

// SetExecutor installs the job runner.
func (s *Server) SetExecutor(e Executor) {
	s.mu.Lock()
	s.executor = e
	s.mu.Unlock()
}

// Publish broadcasts an SSE event to the subscribers of one job.
// Slow subscribers never block the runner: a full buffer drops
// the event (the client re-syncs via /runs, so nothing is lost).
func (s *Server) Publish(jobID string, ev Event) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for _, ch := range s.subs[jobID] {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Handler returns the mux with auth applied to every /api route.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/jobs", s.auth(s.handleJobs))
	mux.HandleFunc("/api/jobs/", s.auth(s.handleJobSub))
	return mux
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtleCompare(got, s.token) != 0 {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
		}
		next(w, r)
	}
}

func subtleCompare(a, b string) int {
	if len(a) != len(b) {
		return 1
	}
	diff := 0
	for i := range a {
		diff |= int(a[i] ^ b[i])
	}
	return diff
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, HealthResponse{
		Online: true, AgentVersion: version.AppVersion,
		ProtocolVersion: ProtocolVersion, EngineVersion: EngineVersion,
		Time: time.Now().UTC(),
	})
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.mu.RLock()
		out := make([]JobResponse, 0, len(s.jobs))
		for _, j := range s.jobs {
			out = append(out, JobResponse{Job: j, LastRunAt: j.LastRunAt, NextRunAt: j.NextRunAt})
		}
		s.mu.RUnlock()
		writeJSON(w, http.StatusOK, out)
	case http.MethodPost:
		var req JobRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		if req.Job.ID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "job.id is required"})
			return
		}
		if err := scheduler.ValidateSchedule(req.Job.Schedule); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		// NOTE: req.EncryptedToken is ciphertext; the Agent stores it as
		// ciphertext and only decrypts in memory at run time. In
		// practice credentials arrive via the encrypted config bundle
		// (bundle.go), not this field — a job pushed without its
		// mounts on the Agent fails at run time with a clear error
		// telling the user to import the bundle.
		if err := s.storeJob(req.Job); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, JobResponse{Job: req.Job})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// storeJob persists a job (when a DB is attached) and notifies the
// executor so the scheduler picks the new schedule up.
func (s *Server) storeJob(job domain.SyncJob) error {
	s.mu.Lock()
	if s.db != nil {
		if err := s.db.SaveSyncJob(&job); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	s.jobs[job.ID] = job
	exec := s.executor
	s.mu.Unlock()
	if exec != nil {
		exec.JobUpserted(job)
	}
	return nil
}

func (s *Server) deleteJob(id string) error {
	s.mu.Lock()
	if s.db != nil {
		if err := s.db.DeleteSyncJob(id); err != nil {
			s.mu.Unlock()
			return err
		}
		if err := s.db.DeleteRunsForJob(id); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	delete(s.jobs, id)
	exec := s.executor
	s.mu.Unlock()
	if exec != nil {
		exec.JobDeleted(id)
	}
	return nil
}

// handleJobSub serves /api/jobs/:id, /run, /runs and /events.
func (s *Server) handleJobSub(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/jobs/")
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}
	switch action {
	case "run":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.mu.RLock()
		_, ok := s.jobs[id]
		exec := s.executor
		s.mu.RUnlock()
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
			return
		}
		if exec == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "agent runner is not configured"})
			return
		}
		runID, err := exec.RunNow(r.Context(), id)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued", "jobId": id, "runId": runID})
	case "runs":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		out, err := s.listRuns(id)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, out)
	case "events":
		// SSE progress stream. If the client disconnects it re-syncs
		// via /runs, so no state is lost.
		s.handleEvents(w, r, id)
	case "":
		if r.Method == http.MethodPut {
			var req JobRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
				return
			}
			if err := scheduler.ValidateSchedule(req.Job.Schedule); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			req.Job.ID = id
			if err := s.storeJob(req.Job); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, JobResponse{Job: req.Job})
			return
		}
		if r.Method == http.MethodDelete {
			if err := s.deleteJob(id); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			// Deleting a job also deletes its runs; stored credential
			// ciphertext (if any was ever pushed) is not kept in the
			// job row — credentials live in the secrets table under
			// the mount, untouched by a job delete.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	default:
		http.NotFound(w, r)
	}
}

// listRuns returns a job's runs newest-first, each with its items.
// Without a DB there is no history to return (the runner persists
// runs to the DB; memory-only mode keeps none).
func (s *Server) listRuns(jobID string) ([]RunResponse, error) {
	out := []RunResponse{}
	s.mu.RLock()
	db := s.db
	s.mu.RUnlock()
	if db == nil {
		return out, nil
	}
	runs, err := db.ListRuns(jobID, 50)
	if err != nil {
		return nil, err
	}
	for _, r := range runs {
		items, err := db.ListRunItems(r.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, RunResponse{Run: *r, Items: items})
	}
	return out, nil
}

// handleEvents is the SSE endpoint: hello first, then the runner's
// progress/done/error events for this job as they arrive.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request, jobID string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	ch := make(chan Event, 16)
	s.subMu.Lock()
	s.subs[jobID] = append(s.subs[jobID], ch)
	s.subMu.Unlock()
	defer func() {
		s.subMu.Lock()
		subs := s.subs[jobID]
		for i, c := range subs {
			if c == ch {
				s.subs[jobID] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		s.subMu.Unlock()
	}()
	fmt.Fprintf(w, "event: hello\ndata: {\"jobId\": %q}\n\n", jobID)
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			data, _ := json.Marshal(ev)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
			flusher.Flush()
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
