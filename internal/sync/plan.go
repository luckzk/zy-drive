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
}

// Summary returns the user-visible counts for the plan.
func (p *Plan) Summary() domain.PlanSummary {
	s := domain.PlanSummary{FilteredOut: p.FilteredOut}
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
