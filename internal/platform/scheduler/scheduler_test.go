// SPDX-License-Identifier: MPL-2.0
package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestValidateSchedule(t *testing.T) {
	for _, ok := range []string{"", "hourly", "daily 02:00", "daily 23:59"} {
		if err := ValidateSchedule(ok); err != nil {
			t.Fatalf("ValidateSchedule(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"daily", "daily 25:00", "weekly", "0 2 * * *", "daily 02:00:00"} {
		if err := ValidateSchedule(bad); err == nil {
			t.Fatalf("ValidateSchedule(%q) = nil, want an error", bad)
		}
	}
}

func TestNextRunDaily(t *testing.T) {
	from := time.Date(2026, 11, 25, 1, 0, 0, 0, time.UTC)
	next, err := NextRun("daily 02:00", from)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 25, 2, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("NextRun daily 02:00 from 01:00 = %v, want %v", next, want)
	}
	// After 02:00 the next trigger is tomorrow.
	next, err = NextRun("daily 02:00", time.Date(2026, 11, 25, 3, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want = time.Date(2026, 11, 26, 2, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("NextRun daily 02:00 from 03:00 = %v, want %v", next, want)
	}
	next, err = NextRun("hourly", time.Date(2026, 11, 25, 1, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 25, 2, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("NextRun hourly from 01:30 = %v, want %v", next, want)
	}
	next, err = NextRun("", from)
	if err != nil || !next.IsZero() {
		t.Fatalf("NextRun manual = %v, %v, want zero time, nil", next, err)
	}
}

func TestTickFiresDueJobOnly(t *testing.T) {
	s := New()
	var fired int32
	due := time.Date(2026, 11, 25, 2, 0, 0, 0, time.UTC)
	s.Add(&Job{ID: "j1", Schedule: "daily 02:00", Run: func(ctx context.Context) { atomic.AddInt32(&fired, 1) }})
	// Add computes NextRun from the real clock; pin it to the
	// trigger under test (in-package test, so direct access is fine).
	s.mu.Lock()
	s.jobs["j1"].NextRun = due
	s.mu.Unlock()
	// Tick before the trigger: nothing fires, NextRun stays 02:00.
	s.tick(context.Background(), due.Add(-time.Minute))
	if got := atomic.LoadInt32(&fired); got != 0 {
		t.Fatalf("fired = %d before the trigger, want 0", got)
	}
	// Tick at the trigger: fires once and reschedules to tomorrow.
	s.tick(context.Background(), due)
	// The run is launched in a goroutine; give it a moment.
	deadline := time.Now().Add(time.Second)
	for atomic.LoadInt32(&fired) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&fired); got != 1 {
		t.Fatalf("fired = %d at the trigger, want 1", got)
	}
	s.mu.Lock()
	next := s.jobs["j1"].NextRun
	s.mu.Unlock()
	want := time.Date(2026, 11, 26, 2, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("NextRun after firing = %v, want %v", next, want)
	}
	// A manual job never fires.
	s.Add(&Job{ID: "j2", Schedule: "", Run: func(ctx context.Context) { atomic.AddInt32(&fired, 10) }})
	s.tick(context.Background(), due.Add(24*time.Hour))
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&fired); got != 2 {
		t.Fatalf("fired = %d after a day, want 2 (manual job must not fire, daily fires once more)", got)
	}
}
