// SPDX-License-Identifier: MPL-2.0
package sync

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/luckzk/zy-drive/internal/domain"
)

func deletePlan(jobID string, paths ...string) *Plan {
	p := &Plan{JobID: jobID, Direction: domain.DirectionMirror}
	for _, path := range paths {
		p.Items = append(p.Items, PlanItem{Path: path, Action: domain.ActionDelete, Size: 1})
	}
	p.HasDeletes = len(paths) > 0
	return p
}

// TestExecuteRefusesDeletesWhenSwitchOff: the §20 engine guard —
// a plan carrying deletes while the switch is off is refused
// BEFORE anything executes (no copy, no delete).
func TestExecuteRefusesDeletesWhenSwitchOff(t *testing.T) {
	src := newMockProvider(map[string][]byte{"a.txt": []byte("aaa")}, 0)
	dst := newMockProvider(map[string][]byte{"old.txt": []byte("old")}, 0)
	plan := &Plan{JobID: "j1", Direction: domain.DirectionMirror, Items: []PlanItem{
		{Path: "a.txt", Action: domain.ActionUpload, Size: 3},
		{Path: "old.txt", Action: domain.ActionDelete},
	}, HasDeletes: true}
	r := NewRunner(nil, 2, nil)
	run, err := r.ExecuteWithPolicy(context.Background(), plan,
		DeletePolicy{Enabled: false, GateMaxCount: 20, GateMaxPercent: 10}, src, dst, "src", "dst")
	if err == nil || !errors.Is(err, ErrDeletesDisabled) {
		t.Fatalf("Execute with switch off must fail with ErrDeletesDisabled, got err=%v", err)
	}
	if run.Status != domain.RunFailed {
		t.Fatalf("run status = %v, want failed", run.Status)
	}
	if _, ok := dst.files["a.txt"]; ok {
		t.Fatalf("refused run must not copy anything")
	}
	if _, ok := dst.files["old.txt"]; !ok {
		t.Fatalf("refused run must not delete anything")
	}
}

// TestExecuteRechecksDeletesNotFlag: the run.go re-check — a stale
// plan with delete items but HasDeletes=false is still refused.
// The flag is never trusted.
func TestExecuteRechecksDeletesNotFlag(t *testing.T) {
	src := newMockProvider(map[string][]byte{"a.txt": []byte("aaa")}, 0)
	dst := newMockProvider(map[string][]byte{"old.txt": []byte("old")}, 0)
	plan := deletePlan("j1", "old.txt")
	plan.HasDeletes = false // stale / hand-built plan lies about deletes
	r := NewRunner(nil, 2, nil)
	_, err := r.ExecuteWithPolicy(context.Background(), plan,
		DeletePolicy{Enabled: false, GateMaxCount: 20, GateMaxPercent: 10}, src, dst, "src", "dst")
	if err == nil || !errors.Is(err, ErrDeletesDisabled) {
		t.Fatalf("stale HasDeletes=false must not smuggle deletes past the guard, got err=%v", err)
	}
	if _, ok := dst.files["old.txt"]; !ok {
		t.Fatalf("guarded file must survive")
	}
}

// TestExecuteGateRefusesUnconfirmed: 21 deletes with the switch on
// and no user confirmation is refused; the same plan with
// ConfirmDeletes runs and deletes.
func TestExecuteGateRefusesUnconfirmed(t *testing.T) {
	paths := make([]string, 0, 21)
	for i := 0; i < 21; i++ {
		paths = append(paths, "/old"+string(rune('a'+i))+".txt")
	}
	policy := DeletePolicy{Enabled: true, GateMaxCount: 20, GateMaxPercent: 10}

	src := newMockProvider(map[string][]byte{}, 0)
	dstFiles := map[string][]byte{}
	for _, p := range paths {
		dstFiles[p] = []byte("x")
	}
	dst := newMockProvider(dstFiles, 0)
	plan := deletePlan("j1", paths...)
	plan.TargetFileCount = 10000 // percentage rule cannot fire; count rule does
	r := NewRunner(nil, 2, nil)
	run, err := r.ExecuteWithPolicy(context.Background(), plan, policy, src, dst, "src", "dst")
	if err == nil || !errors.Is(err, ErrDeleteGate) {
		t.Fatalf("unconfirmed gate trip must fail with ErrDeleteGate, got err=%v", err)
	}
	if run.Status != domain.RunFailed {
		t.Fatalf("run status = %v, want failed", run.Status)
	}
	if !strings.Contains(err.Error(), "21") {
		t.Fatalf("gate error must carry the count, got %q", err.Error())
	}
	if len(dst.files) != 21 {
		t.Fatalf("refused gate run must delete nothing, %d files left", len(dst.files))
	}

	// Confirmed: the same plan runs and deletes.
	dst2Files := map[string][]byte{}
	for _, p := range paths {
		dst2Files[p] = []byte("x")
	}
	dst2 := newMockProvider(dst2Files, 0)
	plan2 := deletePlan("j1", paths...)
	plan2.TargetFileCount = 10000
	plan2.ConfirmDeletes = true
	run, err = r.ExecuteWithPolicy(context.Background(), plan2, policy, src, dst2, "src", "dst")
	if err != nil {
		t.Fatalf("confirmed gate plan must run: %v", err)
	}
	if run.Status != domain.RunCompleted {
		t.Fatalf("run status = %v, want completed", run.Status)
	}
	if len(dst2.files) != 0 {
		t.Fatalf("confirmed run must delete all 21, %d left", len(dst2.files))
	}
}

// TestExecuteGateBelowThresholdRuns: deletes under both thresholds
// run without confirmation.
func TestExecuteGateBelowThresholdRuns(t *testing.T) {
	src := newMockProvider(map[string][]byte{}, 0)
	dst := newMockProvider(map[string][]byte{"old.txt": []byte("old")}, 0)
	plan := deletePlan("j1", "old.txt")
	plan.TargetFileCount = 100
	r := NewRunner(nil, 2, nil)
	run, err := r.ExecuteWithPolicy(context.Background(), plan,
		DeletePolicy{Enabled: true, GateMaxCount: 20, GateMaxPercent: 10}, src, dst, "src", "dst")
	if err != nil {
		t.Fatalf("below-threshold deletes must run: %v", err)
	}
	if run.Status != domain.RunCompleted {
		t.Fatalf("run status = %v, want completed", run.Status)
	}
	if _, ok := dst.files["old.txt"]; ok {
		t.Fatalf("below-threshold delete must execute")
	}
}
