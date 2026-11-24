package sync

import (
	"github.com/luckzk/zy-drive/internal/domain"
)

// ChangeKind is what compare() decided about one path.
type ChangeKind string

const (
	ChangeNew      ChangeKind = "new"      // only on source
	ChangeModified ChangeKind = "modified" // both sides, source is newer/different
	ChangeDeleted  ChangeKind = "deleted"  // only on target (source no longer has it)
	ChangeSame     ChangeKind = "same"
)

// Change is one path's comparison result.
type Change struct {
	Path   string
	Kind   ChangeKind
	Src    *domain.Node
	Dst    *domain.Node
	Reason string // human-readable, shown in the plan preview
}

// Compare decides, for every path in either snapshot, what happened.
// Hashes are only compared when both sides expose a hash with the SAME
// algorithm; otherwise size+time (or size alone) decides, and the reason
// says so — never silently guess.
func Compare(src, dst *Snapshot, mode domain.CompareMode, exclude []string) []Change {
	var out []Change
	seen := map[string]bool{}
	for p, sn := range src.Nodes {
		seen[p] = true
		if Matches(p, exclude) {
			continue
		}
		dn, ok := dst.Nodes[p]
		if !ok {
			out = append(out, Change{Path: p, Kind: ChangeNew, Src: &sn, Reason: "only on source"})
			continue
		}
		if sn.IsDir && dn.IsDir {
			out = append(out, Change{Path: p, Kind: ChangeSame, Src: &sn, Dst: &dn})
			continue
		}
		if different(sn, dn, mode) {
			out = append(out, Change{Path: p, Kind: ChangeModified, Src: &sn, Dst: &dn, Reason: compareReason(sn, dn, mode)})
		} else {
			out = append(out, Change{Path: p, Kind: ChangeSame, Src: &sn, Dst: &dn})
		}
	}
	for p, dn := range dst.Nodes {
		if seen[p] || Matches(p, exclude) {
			continue
		}
		out = append(out, Change{Path: p, Kind: ChangeDeleted, Dst: &dn, Reason: "only on target"})
	}
	return out
}

func different(a, b domain.Node, mode domain.CompareMode) bool {
	switch mode {
	case domain.CompareChecksum:
		if a.Hash != "" && b.Hash != "" && a.HashKind != "" && a.HashKind == b.HashKind {
			return a.Hash != b.Hash
		}
		// No comparable hash: fall back to size+time, never guess equal.
		fallthrough
	case domain.CompareSizeTime:
		if a.Size != b.Size {
			return true
		}
		return !a.ModTime.Equal(b.ModTime) && a.ModTime.After(b.ModTime) || b.ModTime.After(a.ModTime)
	case domain.CompareSize:
		return a.Size != b.Size
	default:
		return a.Size != b.Size || !a.ModTime.Equal(b.ModTime)
	}
}

func compareReason(a, b domain.Node, mode domain.CompareMode) string {
	if mode == domain.CompareChecksum && a.Hash != "" && b.Hash != "" && a.HashKind == b.HashKind {
		return "checksum differs"
	}
	if a.Size != b.Size {
		return "size differs"
	}
	return "modification time differs"
}
