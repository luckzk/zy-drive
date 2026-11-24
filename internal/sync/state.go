package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
)

// Baseline is the snapshot taken after a successful bidirectional sync.
// The next run compares each side against its baseline to know who
// changed or deleted what. Without a baseline, "who deleted" cannot be
// determined — the first bidirectional run must do a Resync to build it,
// and the wizard says so explicitly.
type Baseline struct {
	JobID   string                 `json:"jobId"`
	Side    string                 `json:"side"` // "src" or "dst"
	TakenAt time.Time              `json:"takenAt"`
	Nodes   map[string]domain.Node `json:"nodes"`
}

// BaselineStore persists baselines. The SQLite implementation lives in
// internal/cache; this interface keeps the sync engine testable with an
// in-memory fake.
type BaselineStore interface {
	SaveBaseline(ctx context.Context, b *Baseline) error
	LoadBaseline(ctx context.Context, jobID, side string) (*Baseline, error)
}

// MemoryBaselineStore is the in-memory BaselineStore for tests and for
// the V0.1 skeleton. Production code uses the SQLite store in cache.
type MemoryBaselineStore struct {
	m map[string]*Baseline
}

// NewMemoryBaselineStore returns an empty in-memory store.
func NewMemoryBaselineStore() *MemoryBaselineStore {
	return &MemoryBaselineStore{m: map[string]*Baseline{}}
}

func (s *MemoryBaselineStore) SaveBaseline(_ context.Context, b *Baseline) error {
	s.m[b.JobID+"/"+b.Side] = b
	return nil
}

func (s *MemoryBaselineStore) LoadBaseline(_ context.Context, jobID, side string) (*Baseline, error) {
	b, ok := s.m[jobID+"/"+side]
	if !ok {
		return nil, fmt.Errorf("sync: no baseline for job %s side %s (run a Resync first)", jobID, side)
	}
	return b, nil
}

// SnapshotJSON serialises a Snapshot for storage in SQLite (BLOB/TEXT).
func SnapshotJSON(s *Snapshot) ([]byte, error) { return json.Marshal(s) }

// SnapshotFromJSON parses a stored snapshot.
func SnapshotFromJSON(data []byte) (*Snapshot, error) {
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if s.Nodes == nil {
		s.Nodes = map[string]domain.Node{}
	}
	return &s, nil
}
