// SPDX-License-Identifier: MPL-2.0
package sync

import (
	"github.com/luckzk/zy-drive/internal/domain"
)

// PlanItem is one line of a plan: what will happen to one file.
type PlanItem struct {
	Path   string               `json:"path"`
	Action domain.RunItemAction `json:"action"`
	Size   int64                `json:"size"`
	Note   string               `json:"note,omitempty"`
}

// Plan is the full, user-confirmable result of scan+compare. Mirror and
// bidirectional plans may contain deletions; they must be confirmed by
// the user before Execute runs. Copy plans never contain deletions.
type Plan struct {
	JobID       string               `json:"jobId"`
	Direction   domain.SyncDirection `json:"direction"`
	Items       []PlanItem           `json:"items"`
	FilteredOut int                  `json:"filteredOut"`
	// HasDeletes is true when any item deletes a file. The UI must show
	// the deletion list explicitly and require confirmation.
	HasDeletes bool `json:"hasDeletes"`
	// TargetFileCount is how many files the target side holds (used
	// by the §22 gate's percentage rule; 0 = unknown, the rule is
	// then skipped). Set by PreviewJob from the target snapshot.
	TargetFileCount int `json:"targetFileCount"`
	// DeleteCount is the number of delete items currently in the
	// plan (recomputed by ApplyDeletePolicy / the runner — never
	// trust a stored value without recounting).
	DeleteCount int `json:"deleteCount"`
	// GateTriggered / GateReason (§22): the safety gate tripped —
	// deletes exceed the count or percentage threshold. The UI
	// must show the three-way dialog (confirm deletes / sync
	// only the non-delete part / cancel); the engine refuses an
	// unconfirmed triggered plan (see Runner.ExecuteWithPolicy).
	GateTriggered bool   `json:"gateTriggered"`
	GateReason    string `json:"gateReason,omitempty"`
	// DeletesDeferred (§20/§22): deletes were held back from this
	// plan — the switch was off, or (Agent runs) the gate tripped
	// and only the non-delete part was kept. DeferredDeleteCount
	// is how many were held back; the files are untouched.
	DeletesDeferred     bool `json:"deletesDeferred,omitempty"`
	DeferredDeleteCount int  `json:"deferredDeleteCount,omitempty"`
	// ConfirmDeletes is set by the frontend when the user picks
	// "确认删除并执行" in the gate dialog. The engine only honours
	// it together with a freshly recomputed gate check.
	ConfirmDeletes bool `json:"confirmDeletes,omitempty"`
	// Conflicts (§7) is the per-item conflict list for the plan
	// preview: both sides' info, the options, and the user's
	// per-item choice ("" = default keep-both). Empty when the
	// plan has no conflicts.
	Conflicts []ConflictDetail `json:"conflicts,omitempty"`
}

// CountDeletes recounts the delete items actually in the plan.
func (p *Plan) CountDeletes() int {
	n := 0
	for _, it := range p.Items {
		if it.Action == domain.ActionDelete {
			n++
		}
	}
	return n
}

// ConflictFor returns the conflict detail for a path, or nil.
func (p *Plan) ConflictFor(path string) *ConflictDetail {
	for i := range p.Conflicts {
		if p.Conflicts[i].Path == path {
			return &p.Conflicts[i]
		}
	}
	return nil
}

// ApplyDeletePolicy enforces the §20 switch and evaluates the §22
// gate on this plan. Call it once, right after the plan is built,
// with the target file count from the target snapshot:
//   - switch off: delete items are removed, counted as deferred,
//     HasDeletes cleared — deletes never flow, locally or on the
//     Agent. Copy plans are untouched (they never delete).
//   - switch on: deletes stay, DeleteCount is set, and the gate is
//     evaluated (GateTriggered + GateReason).
//
// The gate comparison is strict: exactly the threshold does not trip.
func (p *Plan) ApplyDeletePolicy(policy DeletePolicy, targetFileCount int) {
	policy = policy.Normalize()
	p.TargetFileCount = targetFileCount
	if !policy.Enabled {
		kept := p.Items[:0]
		deferred := 0
		for _, it := range p.Items {
			if it.Action == domain.ActionDelete {
				deferred++
				continue
			}
			kept = append(kept, it)
		}
		p.Items = kept
		p.HasDeletes = false
		p.DeleteCount = 0
		p.GateTriggered = false
		p.GateReason = ""
		p.ConfirmDeletes = false
		if deferred > 0 {
			p.DeletesDeferred = true
			p.DeferredDeleteCount = deferred
		}
		return
	}
	p.DeleteCount = p.CountDeletes()
	p.HasDeletes = p.DeleteCount > 0
	p.GateTriggered, p.GateReason = policy.GateTripped(p.DeleteCount, targetFileCount)
}

// WithoutDeletes returns a copy of the plan with every delete item
// removed and counted as deferred (§22 Agent path: run the
// non-delete part, hold the deletes for confirmation). The
// original plan is untouched. GateTriggered/GateReason are kept so
// the run record still says why the deletes were held back.
func (p *Plan) WithoutDeletes() *Plan {
	if p == nil {
		return nil
	}
	cp := *p
	cp.Items = make([]PlanItem, 0, len(p.Items))
	deferred := p.DeferredDeleteCount
	for _, it := range p.Items {
		if it.Action == domain.ActionDelete {
			deferred++
			continue
		}
		cp.Items = append(cp.Items, it)
	}
	cp.HasDeletes = false
	cp.DeleteCount = 0
	cp.ConfirmDeletes = false
	if deferred > 0 {
		cp.DeletesDeferred = true
		cp.DeferredDeleteCount = deferred
	}
	return &cp
}

// Summary returns the user-visible counts for the plan.
func (p *Plan) Summary() domain.PlanSummary {
	s := domain.PlanSummary{FilteredOut: p.FilteredOut, DeferredDeletes: p.DeferredDeleteCount}
	for _, it := range p.Items {
		switch it.Action {
		case domain.ActionUpload:
			s.ToUpload++
		case domain.ActionUpdate:
			s.ToUpdate++
		case domain.ActionDelete:
			s.ToDelete++
		case domain.ActionConflict:
			s.Conflicts++
		}
	}
	return s
}

// BuildPlan turns comparison changes into an executable plan for the
// given direction. Semantics are frozen:
//   - Copy: new/modified → upload/update; target-only files untouched.
//   - Mirror: target ends up identical to source; target-only → delete.
//   - Bidirectional: handled by BuildBidirectionalPlan (needs baselines).
func BuildPlan(jobID string, dir domain.SyncDirection, changes []Change) *Plan {
	p := &Plan{JobID: jobID, Direction: dir}
	for _, ch := range changes {
		switch dir {
		case domain.DirectionCopy:
			switch ch.Kind {
			case ChangeNew:
				p.Items = append(p.Items, PlanItem{Path: ch.Path, Action: domain.ActionUpload, Size: ch.Src.Size, Note: ch.Reason})
			case ChangeModified:
				p.Items = append(p.Items, PlanItem{Path: ch.Path, Action: domain.ActionUpdate, Size: ch.Src.Size, Note: ch.Reason})
			}
		case domain.DirectionMirror:
			switch ch.Kind {
			case ChangeNew:
				p.Items = append(p.Items, PlanItem{Path: ch.Path, Action: domain.ActionUpload, Size: ch.Src.Size, Note: ch.Reason})
			case ChangeModified:
				p.Items = append(p.Items, PlanItem{Path: ch.Path, Action: domain.ActionUpdate, Size: ch.Src.Size, Note: ch.Reason})
			case ChangeDeleted:
				p.Items = append(p.Items, PlanItem{Path: ch.Path, Action: domain.ActionDelete, Size: ch.Dst.Size, Note: "target only — will be deleted to match source"})
				p.HasDeletes = true
			}
		}
	}
	return p
}
