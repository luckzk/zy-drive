// SPDX-License-Identifier: MPL-2.0
package sync

import (
	"context"
	"testing"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
)

func modChange(path string, size int64, mod time.Time) Change {
	return Change{
		Path: path, Kind: ChangeModified,
		Src: &domain.Node{Path: path, Size: size, ModTime: mod},
		Dst: &domain.Node{Path: path, Size: size, ModTime: mod},
	}
}

// TestConflictCopyNameFormat: the preserved name is
// "name (conflict copy from <side> <date>).ext".
func TestConflictCopyNameFormat(t *testing.T) {
	got := ConflictCopyName("/docs/notes.md", "left", time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC))
	want := "/docs/notes (conflict copy from left 2026-11-28).md"
	if got != want {
		t.Fatalf("ConflictCopyName = %q, want %q", got, want)
	}
	got = ConflictCopyName("/README", "right", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	if got != "/README (conflict copy from right 2026-01-02)" {
		t.Fatalf("extensionless name = %q", got)
	}
}

// TestBidirectionalConflictLocalNoNewer: with a local folder
// involved, the conflict list must NOT offer keep-newer and must
// default to keep-both; nothing is decided by timestamp.
func TestBidirectionalConflictLocalNoNewer(t *testing.T) {
	srcMod := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	dstMod := time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC)
	sc := modChange("/a.txt", 10, srcMod)
	dc := modChange("/a.txt", 12, dstMod)
	plan := BuildBidirectionalPlanWithLocal("j1", []Change{sc}, []Change{dc}, true)
	if len(plan.Items) != 1 || plan.Items[0].Action != domain.ActionConflict {
		t.Fatalf("both-changed must be one conflict item, got %+v", plan.Items)
	}
	if len(plan.Conflicts) != 1 {
		t.Fatalf("plan must carry one conflict detail, got %d", len(plan.Conflicts))
	}
	d := plan.Conflicts[0]
	if !d.LocalInvolved || d.AllowNewer {
		t.Fatalf("local conflict must be LocalInvolved=true AllowNewer=false, got %+v", d)
	}
	for _, opt := range d.Options {
		if opt == ConflictKeepNewer {
			t.Fatalf("local conflict must not offer keep-newer, options=%v", d.Options)
		}
	}
	if d.DefaultChoice != ConflictKeepBoth || d.ChoiceOrDefault() != ConflictKeepBoth {
		t.Fatalf("local conflict default must be keep-both, got %+v", d)
	}
	if d.LeftSize != 10 || d.RightSize != 12 {
		t.Fatalf("conflict detail must carry both sizes, got %+v", d)
	}
	if plan.Items[0].Note == "" {
		t.Fatalf("conflict item needs a note")
	}
}

// TestBidirectionalConflictCloudOffersNewer: cloud-vs-cloud offers
// keep-newer as an explicit option (still default keep-both).
func TestBidirectionalConflictCloudOffersNewer(t *testing.T) {
	sc := modChange("/a.txt", 10, time.Now())
	dc := modChange("/a.txt", 12, time.Now())
	plan := BuildBidirectionalPlan("j1", []Change{sc}, []Change{dc})
	d := plan.Conflicts[0]
	if d.LocalInvolved || !d.AllowNewer {
		t.Fatalf("cloud conflict must allow newer, got %+v", d)
	}
	found := false
	for _, opt := range d.Options {
		if opt == ConflictKeepNewer {
			found = true
		}
	}
	if !found {
		t.Fatalf("cloud conflict must offer keep-newer, options=%v", d.Options)
	}
	if d.ChoiceOrDefault() != ConflictKeepBoth {
		t.Fatalf("cloud conflict default must still be keep-both")
	}
}

// TestExecuteConflictKeepBothDefault: the default (no user pick)
// keeps both — the target original is untouched and the source
// version lands as a conflict copy.
func TestExecuteConflictKeepBothDefault(t *testing.T) {
	srcMod := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	dstMod := time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC)
	src := newMockProvider(map[string][]byte{"/a.txt": []byte("left-newer")}, 0)
	dst := newMockProvider(map[string][]byte{"/a.txt": []byte("right-older")}, 0)
	plan := BuildBidirectionalPlanWithLocal("j1",
		[]Change{modChange("/a.txt", 10, srcMod)},
		[]Change{modChange("/a.txt", 11, dstMod)}, true)
	r := NewRunner(nil, 2, nil)
	run, err := r.Execute(context.Background(), plan, src, dst, "src", "dst")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if run.Status != domain.RunCompleted {
		t.Fatalf("run status = %v, want completed", run.Status)
	}
	// The newer left version must NOT have silently overwritten
	// the target original — the timestamp decided nothing.
	if got := string(dst.files["/a.txt"]); got != "right-older" {
		t.Fatalf("target original = %q, want right-older untouched", got)
	}
	copyName := "/a (conflict copy from left 2026-11-28).txt"
	if got := string(dst.files[copyName]); got != "left-newer" {
		t.Fatalf("conflict copy %q = %q, want left-newer (files: %v)", copyName, got, dst.files)
	}
}

// TestExecuteConflictChoices: keep-left overwrites the target with
// the source version; keep-right leaves the target alone.
func TestExecuteConflictChoices(t *testing.T) {
	for _, tc := range []struct {
		choice ConflictChoice
		want   string
	}{
		{ConflictKeepLeft, "left-content"},
		{ConflictKeepRight, "right-content"},
	} {
		src := newMockProvider(map[string][]byte{"/a.txt": []byte("left-content")}, 0)
		dst := newMockProvider(map[string][]byte{"/a.txt": []byte("right-content")}, 0)
		plan := BuildBidirectionalPlanWithLocal("j1",
			[]Change{modChange("/a.txt", 12, time.Now())},
			[]Change{modChange("/a.txt", 13, time.Now())}, true)
		plan.Conflicts[0].Choice = tc.choice
		r := NewRunner(nil, 2, nil)
		if _, err := r.Execute(context.Background(), plan, src, dst, "src", "dst"); err != nil {
			t.Fatalf("Execute %s: %v", tc.choice, err)
		}
		if got := string(dst.files["/a.txt"]); got != tc.want {
			t.Fatalf("choice %s: target = %q, want %q", tc.choice, got, tc.want)
		}
		if tc.choice == ConflictKeepRight {
			if len(dst.files) != 1 {
				t.Fatalf("keep-right must not create a copy, files=%v", dst.files)
			}
		}
	}
}

// TestExecuteConflictKeepNewerLocalRejected: picking keep-newer on
// a local-involved conflict fails the run before anything runs.
func TestExecuteConflictKeepNewerLocalRejected(t *testing.T) {
	src := newMockProvider(map[string][]byte{"/a.txt": []byte("left")}, 0)
	dst := newMockProvider(map[string][]byte{"/a.txt": []byte("right")}, 0)
	plan := BuildBidirectionalPlanWithLocal("j1",
		[]Change{modChange("/a.txt", 4, time.Now())},
		[]Change{modChange("/a.txt", 5, time.Now())}, true)
	plan.Conflicts[0].Choice = ConflictKeepNewer
	r := NewRunner(nil, 2, nil)
	run, err := r.Execute(context.Background(), plan, src, dst, "src", "dst")
	if err == nil {
		t.Fatalf("keep-newer on a local conflict must fail")
	}
	if run.Status != domain.RunFailed {
		t.Fatalf("run status = %v, want failed", run.Status)
	}
	if got := string(dst.files["/a.txt"]); got != "right" {
		t.Fatalf("failed run must not touch the target, got %q", got)
	}
}

// TestExecuteConflictKeepNewerCloud: cloud-vs-cloud keep-newer
// lets the newer ModTime win the name (explicit choice only).
func TestExecuteConflictKeepNewerCloud(t *testing.T) {
	newer := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	older := time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC)
	// Source newer: source wins the name.
	src := newMockProvider(map[string][]byte{"/a.txt": []byte("left-newer")}, 0)
	dst := newMockProvider(map[string][]byte{"/a.txt": []byte("right-older")}, 0)
	plan := BuildBidirectionalPlan("j1",
		[]Change{modChange("/a.txt", 10, newer)},
		[]Change{modChange("/a.txt", 11, older)})
	plan.Conflicts[0].Choice = ConflictKeepNewer
	r := NewRunner(nil, 2, nil)
	if _, err := r.Execute(context.Background(), plan, src, dst, "src", "dst"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := string(dst.files["/a.txt"]); got != "left-newer" {
		t.Fatalf("source newer must win the name, got %q", got)
	}
	// Target newer: target keeps the name, nothing uploaded.
	src = newMockProvider(map[string][]byte{"/a.txt": []byte("left-older")}, 0)
	dst = newMockProvider(map[string][]byte{"/a.txt": []byte("right-newer")}, 0)
	plan = BuildBidirectionalPlan("j1",
		[]Change{modChange("/a.txt", 10, older)},
		[]Change{modChange("/a.txt", 11, newer)})
	plan.Conflicts[0].Choice = ConflictKeepNewer
	if _, err := r.Execute(context.Background(), plan, src, dst, "src", "dst"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := string(dst.files["/a.txt"]); got != "right-newer" {
		t.Fatalf("target newer must keep the name, got %q", got)
	}
	if len(dst.files) != 1 {
		t.Fatalf("target-newer must not create a copy, files=%v", dst.files)
	}
}
