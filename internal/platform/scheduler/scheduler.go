// Package scheduler provides the in-process cron scheduler shared by
// desktop and Agent. Jobs are stored in the database; the scheduler only
// triggers them on time. It never uses the system crontab, so desktop
// and Agent behave identically.
package scheduler

import (
	"context"
	"sync"
	"time"
)

// Job is a scheduled callback.
type Job struct {
	ID       string
	Schedule string // cron expression; empty = manual only
	Run      func(ctx context.Context)
	NextRun  time.Time
}

// Scheduler triggers jobs. V0.1 supports two schedule shapes:
// "daily HH:MM" and "hourly" (plus empty = manual). A full cron parser
// lands in V0.2; the interface stays the same.
type Scheduler struct {
	mu   sync.Mutex
	jobs map[string]*Job
}

// New returns an empty Scheduler.
func New() *Scheduler { return &Scheduler{jobs: map[string]*Job{}} }

// Add registers a job. Re-adding the same ID replaces it.
func (s *Scheduler) Add(j *Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j.NextRun = nextRun(j.Schedule, time.Now())
	s.jobs[j.ID] = j
}

// Remove unregisters a job.
func (s *Scheduler) Remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, id)
}

// Start runs the tick loop until ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.tick(ctx, now)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context, now time.Time) {
	s.mu.Lock()
	due := []*Job{}
	for _, j := range s.jobs {
		if !j.NextRun.IsZero() && !now.Before(j.NextRun) {
			due = append(due, j)
			j.NextRun = nextRun(j.Schedule, now)
		}
	}
	s.mu.Unlock()
	for _, j := range due {
		go j.Run(ctx)
	}
}

// nextRun computes the next trigger for the supported schedule shapes.
func nextRun(schedule string, from time.Time) time.Time {
	switch {
	case schedule == "":
		return time.Time{} // manual only
	case schedule == "hourly":
		return from.Truncate(time.Hour).Add(time.Hour)
	case len(schedule) > 6 && schedule[:6] == "daily ":
		t, err := time.Parse("15:04", schedule[6:])
		if err == nil {
			next := time.Date(from.Year(), from.Month(), from.Day(),
				t.Hour(), t.Minute(), 0, 0, from.Location())
			if !next.After(from) {
				next = next.Add(24 * time.Hour)
			}
			return next
		}
		return time.Time{}
	default:
		return time.Time{} // unknown shape: manual only, never guess
	}
}
