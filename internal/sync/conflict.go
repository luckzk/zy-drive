// SPDX-License-Identifier: MPL-2.0
package sync

import (
	"fmt"
	"strings"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
)

// Conflict is a path both sides changed since the last sync.
// Rule (roadmap §7, replacing the frozen V0.1 "newer wins the
// name" rule): never silently delete, never silently overwrite the
// loser's content, and never let a timestamp decide a winner on its
// own —
//   - When either side is a local folder, the conflict goes into
//     the plan's per-item list for the user to pick per item
//     (keep left / keep right / keep both, default keep both).
//     Local OS mtime is incomparable with cloud server time, so
//     "keep newer" is not even offered.
//   - Only when both sides are cloud drives is "keep newer"
//     offered as a batch shortcut — and only as an explicit choice
//     the user picks in the preview, never a silent automatic.
//   - The losing side's copy is always preserved under
//     "name (conflict copy from <side> <date>).ext"; nothing is
//     quietly deleted.
type Conflict struct {
	Path     string
	SrcMod   time.Time
	DstMod   time.Time
	Decision string // human-readable, shown in the run report
}

// ResolveConflict describes what happens to the losing side's copy
// once a winner has been chosen (by the user, or keep-both by
// default): the loser is preserved under its conflict-copy name.
// It does NOT pick a winner — the V0.1 behaviour of letting
// ModTime pick one is gone.
func ResolveConflict(path, losingSide string, losingMod time.Time) Conflict {
	losingName := ConflictCopyName(path, losingSide, losingMod)
	return Conflict{
		Path:     path,
		Decision: fmt.Sprintf("both sides changed; losing copy kept as %q (nothing deleted, winner chosen in preview)", losingName),
	}
}

// ConflictCopyName builds the preserved name for the losing copy:
// "name (conflict copy from <side> <date>).ext", e.g.
// "notes (conflict copy from left 2026-11-28).md". <side> is
// "left"/"right" (or a mount name); <date> is the losing copy's
// modification date (today when unknown).
func ConflictCopyName(path, side string, mod time.Time) string {
	ext := ""
	base := path
	if i := strings.LastIndex(path, "."); i > strings.LastIndex(path, "/") {
		ext, base = path[i:], path[:i]
	}
	if mod.IsZero() {
		mod = time.Now()
	}
	return fmt.Sprintf("%s (conflict copy from %s %s)%s",
		base, side, mod.Format("2006-01-02"), ext)
}

// BuildBidirectionalPlan merges two change lists (source-vs-baseline and
// target-vs-baseline) into one plan. Rules:
//   - Changed on one side only → copy that side's version over.
//   - Changed on both sides → conflict: NO automatic winner. The
//     item goes into the plan with ActionConflict plus a
//     ConflictDetail (both sides' size/modtime, options, default
//     keep-both). Unattended runs default to keep-both; the
//     runner preserves the losing copy under its conflict-copy
//     name and never overwrites silently.
//   - Deleted on one side, modified on the other → keep the modified
//     side and flag it loudly in the report; never apply the delete.
//   - Deleted on both sides → nothing to do.
//
// Full baseline-aware detection (comparing ModTime against the stored
// baseline snapshot) is wired through state.go; this merge is the
// decision table that consumes those change lists.
//
// BuildBidirectionalPlan keeps the old signature and assumes both
// sides are cloud drives (keep-newer offered). Callers that know a
// local folder is involved must use BuildBidirectionalPlanWithLocal.
func BuildBidirectionalPlan(jobID string, srcChanges, dstChanges []Change) *Plan {
	return BuildBidirectionalPlanWithLocal(jobID, srcChanges, dstChanges, false)
}

// BuildBidirectionalPlanWithLocal is BuildBidirectionalPlan with
// the local-folder flag: when localInvolved is true, "keep newer"
// is not offered for any conflict in the plan.
func BuildBidirectionalPlanWithLocal(jobID string, srcChanges, dstChanges []Change, localInvolved bool) *Plan {
	p := &Plan{JobID: jobID, Direction: domain.DirectionBidirectional}
	dstByPath := map[string]Change{}
	for _, c := range dstChanges {
		dstByPath[c.Path] = c
	}
	seen := map[string]bool{}
	for _, sc := range srcChanges {
		seen[sc.Path] = true
		dc, hasDst := dstByPath[sc.Path]
		if !hasDst {
			p.Items = append(p.Items, changeToItem(sc, "source")...)
			continue
		}
		switch {
		case sc.Kind == ChangeModified && dc.Kind == ChangeModified:
			// Both sides changed: conflict, no automatic
			// winner. Default keep-both: the target side keeps
			// the original name, the source side is preserved
			// as a conflict copy (see the runner).
			detail := newConflictDetail(sc, dc, localInvolved)
			p.Conflicts = append(p.Conflicts, detail)
			p.Items = append(p.Items, PlanItem{
				Path:   sc.Path,
				Action: domain.ActionConflict,
				Size:   detail.LeftSize,
				Note:   "两边都改了：默认保留双方（左侧另存为冲突副本），可在预览里逐项改选",
			})
		case sc.Kind == ChangeDeleted && dc.Kind == ChangeModified:
			// Deleted on source, modified on target: keep the modified
			// side, flag it. Never apply the delete.
			p.Items = append(p.Items, PlanItem{
				Path: sc.Path, Action: domain.ActionUpdate, Size: dc.Src.Size,
				Note: "deleted on source but modified on target — kept, not deleted",
			})
		case sc.Kind == ChangeModified && dc.Kind == ChangeDeleted:
			p.Items = append(p.Items, PlanItem{
				Path: sc.Path, Action: domain.ActionUpdate, Size: sc.Src.Size,
				Note: "deleted on target but modified on source — kept, not deleted",
			})
		default:
			p.Items = append(p.Items, changeToItem(sc, "source")...)
		}
	}
	for _, dc := range dstChanges {
		if seen[dc.Path] {
			continue
		}
		p.Items = append(p.Items, changeToItem(dc, "target")...)
	}
	return p
}

// newConflictDetail builds the per-item conflict entry for the plan
// preview. Left = source side, Right = target side.
func newConflictDetail(sc, dc Change, localInvolved bool) ConflictDetail {
	d := ConflictDetail{
		Path:          sc.Path,
		LeftLabel:     "left",
		RightLabel:    "right",
		LocalInvolved: localInvolved,
		AllowNewer:    !localInvolved,
		DefaultChoice: ConflictKeepBoth,
	}
	d.Options = conflictOptions(localInvolved)
	if sc.Src != nil {
		d.LeftSize = sc.Src.Size
		if !sc.Src.ModTime.IsZero() {
			d.LeftModTime = sc.Src.ModTime.Format(time.RFC3339)
		}
	}
	if dc.Src != nil {
		d.RightSize = dc.Src.Size
		if !dc.Src.ModTime.IsZero() {
			d.RightModTime = dc.Src.ModTime.Format(time.RFC3339)
		}
	}
	return d
}

func changeToItem(c Change, side string) []PlanItem {
	switch c.Kind {
	case ChangeNew, ChangeModified:
		action := domain.ActionUpload
		if c.Kind == ChangeModified {
			action = domain.ActionUpdate
		}
		size := int64(0)
		if c.Src != nil {
			size = c.Src.Size
		}
		return []PlanItem{{Path: c.Path, Action: action, Size: size, Note: side + ": " + c.Reason}}
	default:
		return nil
	}
}
