// SPDX-License-Identifier: MPL-2.0
// Package scheduler provides the in-process cron scheduler shared by
// desktop and Agent. Jobs are stored in the database; the scheduler only
// triggers them on time. It never uses the system crontab, so desktop
// and Agent behave identically.
package scheduler

import (
	"context"
	"fmt"
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
	mu       sync.Mutex
	jobs     map[string]*Job
	interval time.Duration
}

// New returns an empty Scheduler ticking once a minute.
func New() *Scheduler { return NewWithInterval(time.Minute) }

// NewWithInterval returns a Scheduler ticking every d. Production
// uses a minute; tests and the Agent e2e script use a shorter tick
// so a "daily HH:MM" job set one minute ahead fires promptly.
func NewWithInterval(d time.Duration) *Scheduler {
	if d <= 0 {
		d = time.Minute
	}
	return &Scheduler{jobs: map[string]*Job{}, interval: d}
}

// ValidateSchedule reports whether a schedule string is a supported
// shape ("" manual, "hourly", "daily HH:MM"). Unknown shapes are
// rejected at save time instead of silently never running.
func ValidateSchedule(schedule string) error {
	if schedule == "" || schedule == "hourly" {
		return nil
	}
	if len(schedule) > 6 && schedule[:6] == "daily " {
		if _, err := time.Parse("15:04", schedule[6:]); err == nil {
			return nil
		}
	}
	return fmt.Errorf("scheduler: 不支持的定时格式 %q（只支持留空、hourly、daily HH:MM，例如 daily 02:00）", schedule)
}

// NextRun returns the next trigger after from, or an error when the
// schedule shape is unknown. "" (manual) returns the zero time.
func NextRun(schedule string, from time.Time) (time.Time, error) {
	if err := ValidateSchedule(schedule); err != nil {
		return time.Time{}, err
	}
	return nextRun(schedule, from), nil
}

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
	t := time.NewTicker(s.interval)
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
