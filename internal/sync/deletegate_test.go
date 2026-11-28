// SPDX-License-Identifier: MPL-2.0
package sync

import (
	"testing"

	"github.com/luckzk/zy-drive/internal/domain"
)

func mirrorPlanWithDeletes(jobID string, n int) *Plan {
	changes := make([]Change, 0, n)
	for i := 0; i < n; i++ {
		changes = append(changes, Change{
			Path: "/old-" + string(rune('a'+i)) + ".txt",
			Kind: ChangeDeleted,
			Dst:  &domain.Node{Path: "/old.txt", Size: 10},
		})
	}
	// One upload, so the plan is not deletes-only.
	changes = append(changes, Change{
		Path: "/new.txt", Kind: ChangeNew,
		Src: &domain.Node{Path: "/new.txt", Size: 3},
	})
	return BuildPlan(jobID, domain.DirectionMirror, changes)
}

// TestGateBoundaries: the §22 comparison is strict — exactly the
// threshold does NOT trip, one over does (both the count rule and
// the percentage rule).
func TestGateBoundaries(t *testing.T) {
	p := DefaultDeletePolicy()
	cases := []struct {
		deletes, target int
		want            bool
	}{
		{0, 100, false},
		{20, 10000, false}, // exactly 20: no trip
		{21, 10000, true},  // 21: trips on count
		{10, 100, false},   // exactly 10% of 100: no trip
		{11, 100, true},    // 11% of 100: trips on percentage
		{5, 40, true},      // 12.5% of 40: trips on percentage
		{4, 40, false},     // exactly 10% of 40: no trip
		{1, 0, false},      // unknown target: percentage rule skipped
		{21, 0, true},      // unknown target: count rule still fires
	}
	for _, c := range cases {
		got, reason := p.GateTripped(c.deletes, c.target)
		if got != c.want {
			t.Fatalf("GateTripped(%d, %d) = %v (%q), want %v", c.deletes, c.target, got, reason, c.want)
		}
	}
}

// TestApplyDeletePolicySwitchOff: with the §20 switch off, deletes
// are stripped from a mirror plan and counted as deferred; the
// upload survives.
func TestApplyDeletePolicySwitchOff(t *testing.T) {
	plan := mirrorPlanWithDeletes("j1", 3)
	plan.ApplyDeletePolicy(DeletePolicy{Enabled: false, GateMaxCount: 20, GateMaxPercent: 10}, 100)
	if plan.HasDeletes || plan.DeleteCount != 0 {
		t.Fatalf("switch off must clear deletes, got HasDeletes=%v DeleteCount=%d", plan.HasDeletes, plan.DeleteCount)
	}
	if !plan.DeletesDeferred || plan.DeferredDeleteCount != 3 {
		t.Fatalf("switch off must defer 3 deletes, got deferred=%v count=%d", plan.DeletesDeferred, plan.DeferredDeleteCount)
	}
	if len(plan.Items) != 1 || plan.Items[0].Action != domain.ActionUpload {
		t.Fatalf("switch off must keep the upload, got %+v", plan.Items)
	}
	if plan.GateTriggered {
		t.Fatalf("switch off must not trip the gate")
	}
}

// TestApplyDeletePolicySwitchOnGate: with the switch on, deletes
// stay and the gate is evaluated from the live thresholds.
func TestApplyDeletePolicySwitchOnGate(t *testing.T) {
	on := DeletePolicy{Enabled: true, GateMaxCount: 20, GateMaxPercent: 10}
	small := mirrorPlanWithDeletes("j1", 3)
	small.ApplyDeletePolicy(on, 100)
	if !small.HasDeletes || small.DeleteCount != 3 || small.GateTriggered {
		t.Fatalf("3 deletes of 100 must stay, no gate: %+v", small)
	}
	big := mirrorPlanWithDeletes("j1", 21)
	big.ApplyDeletePolicy(on, 10000)
	if !big.GateTriggered || big.GateReason == "" {
		t.Fatalf("21 deletes must trip the gate: %+v", big)
	}
	if big.DeleteCount != 21 || len(big.Items) != 22 {
		t.Fatalf("gate trip must keep the items for the dialog, got %+v", big)
	}
}

// TestApplyDeletePolicyCopyUnaffected: a copy plan never deletes,
// so the switch changes nothing for it.
func TestApplyDeletePolicyCopyUnaffected(t *testing.T) {
	changes := []Change{{Path: "/a.txt", Kind: ChangeNew, Src: &domain.Node{Path: "/a.txt", Size: 3}}}
	plan := BuildPlan("j1", domain.DirectionCopy, changes)
	plan.ApplyDeletePolicy(DeletePolicy{Enabled: false}, 0)
	if len(plan.Items) != 1 || plan.DeletesDeferred {
		t.Fatalf("copy plan must be untouched by the switch off, got %+v", plan)
	}
}

// TestWithoutDeletes: the Agent path keeps the non-delete part and
// counts the held-back deletes; the original plan is untouched.
func TestWithoutDeletes(t *testing.T) {
	plan := mirrorPlanWithDeletes("j1", 2)
	plan.ApplyDeletePolicy(DeletePolicy{Enabled: true, GateMaxCount: 20, GateMaxPercent: 10}, 10000)
	stripped := plan.WithoutDeletes()
	if len(stripped.Items) != 1 || stripped.HasDeletes || stripped.DeleteCount != 0 {
		t.Fatalf("stripped plan must hold only the upload: %+v", stripped.Items)
	}
	if !stripped.DeletesDeferred || stripped.DeferredDeleteCount != 2 {
		t.Fatalf("stripped plan must defer 2, got %+v", stripped)
	}
	if len(plan.Items) != 3 || !plan.HasDeletes {
		t.Fatalf("original plan must be untouched: %+v", plan.Items)
	}
}

// TestPolicyNormalize: corrupt thresholds fall back to the defaults.
func TestPolicyNormalize(t *testing.T) {
	p := DeletePolicy{Enabled: true, GateMaxCount: 0, GateMaxPercent: 0}.Normalize()
	if p.GateMaxCount != DefaultDeleteGateMaxCount || p.GateMaxPercent != DefaultDeleteGateMaxPercent {
		t.Fatalf("zero thresholds must normalize to defaults, got %+v", p)
	}
	p = DeletePolicy{Enabled: true, GateMaxCount: -5, GateMaxPercent: 500}.Normalize()
	if p.GateMaxCount != DefaultDeleteGateMaxCount || p.GateMaxPercent != DefaultDeleteGateMaxPercent {
		t.Fatalf("out-of-range thresholds must normalize to defaults, got %+v", p)
	}
}

// TestJobDeletesAllowed: nil per-job override follows the global
// default; an explicit override wins either way.
func TestJobDeletesAllowed(t *testing.T) {
	if (&domain.SyncJob{}).DeletesAllowed(false) {
		t.Fatalf("nil override + global off must be off")
	}
	if !(&domain.SyncJob{}).DeletesAllowed(true) {
		t.Fatalf("nil override + global on must be on")
	}
	off := &domain.SyncJob{DeleteEnabled: domain.BoolPtr(false)}
	if off.DeletesAllowed(true) {
		t.Fatalf("per-job off must beat global on")
	}
	on := &domain.SyncJob{DeleteEnabled: domain.BoolPtr(true)}
	if !on.DeletesAllowed(false) {
		t.Fatalf("per-job on must beat global off")
	}
}
