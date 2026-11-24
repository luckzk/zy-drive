package sync

import (
	"fmt"
	"strings"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
)

// Conflict is a path both sides changed since the last sync.
// V0.1 rule, frozen (ADR-6): never silently delete, never silently
// overwrite the loser's content.
//   - The newer version keeps the original name.
//   - The other version is kept as "name (conflict copy from <side>
//     <date>)" — both copies survive, the user decides later.
type Conflict struct {
	Path     string
	SrcMod   time.Time
	DstMod   time.Time
	Decision string // human-readable, shown in the run report
}

// ResolveConflict applies the frozen V0.1 rule and returns the name the
// losing side's copy will be stored under.
func ResolveConflict(path, losingSide string, losingMod time.Time) Conflict {
	losingName := ConflictCopyName(path, losingSide, losingMod)
	return Conflict{
		Path:     path,
		Decision: fmt.Sprintf("both sides changed; newer keeps %q, other kept as %q", path, losingName),
	}
}

// ConflictCopyName builds the preserved name for the losing copy.
func ConflictCopyName(path, side string, mod time.Time) string {
	ext := ""
	base := path
	if i := strings.LastIndex(path, "."); i > strings.LastIndex(path, "/") {
		ext, base = path[i:], path[:i]
	}
	return fmt.Sprintf("%s (conflict copy from %s %s)%s",
		base, side, mod.Format("2006-01-02"), ext)
}

// BuildBidirectionalPlan merges two change lists (source-vs-baseline and
// target-vs-baseline) into one plan. Rules:
//   - Changed on one side only → copy that side's version over.
//   - Changed on both sides → conflict: newer wins the name, the other
//     is preserved as a conflict copy (see ResolveConflict).
//   - Deleted on one side, modified on the other → keep the modified
//     side and flag it loudly in the report; never apply the delete.
//   - Deleted on both sides → nothing to do.
//
// Full baseline-aware detection (comparing ModTime against the stored
// baseline snapshot) is wired through state.go; this merge is the
// decision table that consumes those change lists.
func BuildBidirectionalPlan(jobID string, srcChanges, dstChanges []Change) *Plan {
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
			// Both sides changed: conflict, newer wins the name.
			newer, loser, loserSide := sc, dc, "target"
			if dc.Src != nil && sc.Src != nil && dc.Src.ModTime.After(sc.Src.ModTime) {
				newer, loser, loserSide = dc, sc, "source"
			}
			_ = loser
			p.Items = append(p.Items, PlanItem{
				Path:   sc.Path,
				Action: domain.ActionConflict,
				Size:   newer.Src.Size,
				Note:   ResolveConflict(sc.Path, loserSide, time.Now()).Decision,
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
