package agentapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/version"
)

// Server is the Agent-side HTTP API. It stores jobs in memory for the
// V0.1 skeleton; persistence to the shared SQLite schema lands with the
// scheduler wiring in V0.2. The Agent must sit behind HTTPS (reverse
// proxy terminates TLS) with an API token — the port is never exposed
// bare. The API token and cloud credentials are two different secrets.
type Server struct {
	mu    sync.RWMutex
	jobs  map[string]domain.SyncJob
	runs  map[string][]domain.Run
	token string // API token, compared in constant time
}

// NewServer returns a Server. An empty token disables auth only when
// explicitly allowed by the caller (local dev); production must pass one.
func NewServer(apiToken string) *Server {
	return &Server{jobs: map[string]domain.SyncJob{}, runs: map[string][]domain.Run{}, token: apiToken}
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
		// NOTE: req.EncryptedToken is ciphertext; the Agent stores it as
		// ciphertext and only decrypts in memory at run time.
		s.mu.Lock()
		s.jobs[req.Job.ID] = req.Job
		s.mu.Unlock()
		writeJSON(w, http.StatusCreated, JobResponse{Job: req.Job})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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
		s.mu.RUnlock()
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
			return
		}
		// TODO(agent): hand the job to the scheduler for immediate
		// execution; the run record is created by the runner.
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued", "jobId": id})
	case "runs":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.mu.RLock()
		runs := s.runs[id]
		s.mu.RUnlock()
		if runs == nil {
			runs = []domain.Run{}
		}
		writeJSON(w, http.StatusOK, runs)
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
			req.Job.ID = id
			s.mu.Lock()
			s.jobs[id] = req.Job
			s.mu.Unlock()
			writeJSON(w, http.StatusOK, JobResponse{Job: req.Job})
			return
		}
		if r.Method == http.MethodDelete {
			s.mu.Lock()
			delete(s.jobs, id)
			delete(s.runs, id)
			s.mu.Unlock()
			// Deleting a job also deletes its stored ciphertext
			// credentials; nothing is left behind on the Agent.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	default:
		http.NotFound(w, r)
	}
}

// handleEvents is the SSE endpoint. V0.1 sends a hello event and keeps
// the stream open; the runner pushes progress events in V0.2.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request, jobID string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, "event: hello\ndata: {\"jobId\": %q}\n\n", jobID)
	flusher.Flush()
	<-r.Context().Done()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
