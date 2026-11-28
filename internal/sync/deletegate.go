// SPDX-License-Identifier: MPL-2.0
package sync

import (
	"fmt"

	"github.com/luckzk/zy-drive/internal/domain"
)

// DeletePolicy is the engine-side delete discipline (roadmap §20/§22).
// It is resolved from Settings (global defaults) plus the per-job
// override before a plan is built or executed — never from the plan
// itself, so a stale or hand-built plan cannot smuggle deletes past
// the switch or the gate.
type DeletePolicy struct {
	// Enabled is the effective delete switch: the per-job
	// DeleteEnabled override when set, otherwise the global
	// sync.delete_default setting (default off). When false,
	// delete items are stripped from the plan and counted as
	// deferred — never executed.
	Enabled bool
	// GateMaxCount / GateMaxPercent are the safety-gate
	// thresholds (§22): a delete count above either one trips
	// the gate. Both are settings-backed (sync.delete_gate_max_count
	// / sync.delete_gate_max_percent), defaults 20 / 10.
	GateMaxCount   int
	GateMaxPercent int
}

// DefaultDeleteGateMaxCount / DefaultDeleteGateMaxPercent are the
// §22 thresholds: more than 20 deletes, or more than 10% of the
// target's files, trips the gate. The comparison is strict:
// exactly 20 deletes, or exactly 10%, does NOT trip it.
const (
	DefaultDeleteGateMaxCount   = 20
	DefaultDeleteGateMaxPercent = 10
)

// DefaultDeletePolicy is the fresh-install policy: deletes off,
// gate at 20 / 10%.
func DefaultDeletePolicy() DeletePolicy {
	return DeletePolicy{
		Enabled:        false,
		GateMaxCount:   DefaultDeleteGateMaxCount,
		GateMaxPercent: DefaultDeleteGateMaxPercent,
	}
}

// Normalize repairs out-of-range thresholds back to the defaults
// (a stored 0, negative, or >100% value is a corrupt/legacy value,
// not a user choice). Enabled is left as-is.
func (p DeletePolicy) Normalize() DeletePolicy {
	if p.GateMaxCount < 1 || p.GateMaxCount > 100000 {
		p.GateMaxCount = DefaultDeleteGateMaxCount
	}
	if p.GateMaxPercent < 1 || p.GateMaxPercent > 100 {
		p.GateMaxPercent = DefaultDeleteGateMaxPercent
	}
	return p
}

// GateTripped reports whether deleteCount trips the gate against
// targetFileCount, with the human reason ("" when not tripped).
// targetFileCount <= 0 means "unknown": only the count rule can
// fire, the percentage rule is skipped (never guessed).
func (p DeletePolicy) GateTripped(deleteCount, targetFileCount int) (bool, string) {
	p = p.Normalize()
	if deleteCount <= 0 {
		return false, ""
	}
	if deleteCount > p.GateMaxCount {
		return true, fmt.Sprintf("删除 %d 个，超过上限 %d 个", deleteCount, p.GateMaxCount)
	}
	if targetFileCount > 0 && deleteCount*100 > p.GateMaxPercent*targetFileCount {
		return true, fmt.Sprintf("删除 %d 个，占目标文件 %d 个的 %d%%，超过 %d%%",
			deleteCount, targetFileCount, deleteCount*100/targetFileCount, p.GateMaxPercent)
	}
	return false, ""
}

// ConflictChoice is one per-item conflict resolution, chosen by the
// user in the plan preview (roadmap §7). The engine never picks one
// on its own — an empty choice means the default, keep-both.
type ConflictChoice string

const (
	// ConflictKeepBoth (default): both versions survive — the
	// target side keeps the original name, the source side is
	// uploaded as a conflict copy. Never deletes, never overwrites.
	ConflictKeepBoth ConflictChoice = "keep-both"
	// ConflictKeepLeft: the source (left) version wins the name
	// and overwrites the target copy. Explicit user choice only.
	ConflictKeepLeft ConflictChoice = "keep-left"
	// ConflictKeepRight: the target (right) version wins; the
	// source copy is left alone on its own side. Explicit user
	// choice only; the runner does nothing for this item.
	ConflictKeepRight ConflictChoice = "keep-right"
	// ConflictKeepNewer: the newer version (by ModTime) wins the
	// name. Only offered when both sides are cloud drives
	// (AllowNewer); a local folder's OS mtime is incomparable
	// with cloud server time, so the engine rejects this choice
	// whenever LocalInvolved is true.
	ConflictKeepNewer ConflictChoice = "keep-newer"
)

// ConflictDetail is one entry of the plan's per-item conflict list
// (§7): both sides' size + modification time, the available
// options, and the user's choice ("" = default keep-both).
type ConflictDetail struct {
	Path string `json:"path"`
	// Left is the source side, Right the target side.
	LeftSize      int64            `json:"leftSize"`
	LeftModTime   string           `json:"leftModTime,omitempty"` // RFC3339, "" when unknown
	RightSize     int64            `json:"rightSize"`
	RightModTime  string           `json:"rightModTime,omitempty"` // RFC3339, "" when unknown
	LeftLabel     string           `json:"leftLabel"`              // "left" (or the source mount name)
	RightLabel    string           `json:"rightLabel"`             // "right" (or the target mount name)
	LocalInvolved bool             `json:"localInvolved"`          // either side is a local folder
	AllowNewer    bool             `json:"allowNewer"`             // false when LocalInvolved
	Options       []ConflictChoice `json:"options"`                // keep-left / keep-right / keep-both (+ keep-newer)
	DefaultChoice ConflictChoice   `json:"defaultChoice"`          // always keep-both
	Choice        ConflictChoice   `json:"choice,omitempty"`       // user's pick; "" = default
}

// ChoiceOrDefault returns the effective choice for this conflict:
// the user's pick when valid, otherwise the default (keep-both).
// keep-newer on a local-involved conflict is invalid and falls
// back to keep-both here; Execute rejects it outright instead
// (see validateConflictChoices) so a bad plan cannot run.
func (c *ConflictDetail) ChoiceOrDefault() ConflictChoice {
	if c == nil {
		return ConflictKeepBoth
	}
	switch c.Choice {
	case ConflictKeepLeft, ConflictKeepRight, ConflictKeepBoth:
		return c.Choice
	case ConflictKeepNewer:
		if c.AllowNewer {
			return ConflictKeepNewer
		}
		return ConflictKeepBoth
	default:
		return ConflictKeepBoth
	}
}

// conflictOptions returns the option list for a conflict:
// keep-newer only when both sides are cloud drives.
func conflictOptions(localInvolved bool) []ConflictChoice {
	if localInvolved {
		return []ConflictChoice{ConflictKeepLeft, ConflictKeepRight, ConflictKeepBoth}
	}
	return []ConflictChoice{ConflictKeepLeft, ConflictKeepRight, ConflictKeepBoth, ConflictKeepNewer}
}

// validateConflictChoices checks every ActionConflict plan item has
// a legal choice. keep-newer with a local side involved is refused:
// local mtime cannot be compared with cloud server time.
func validateConflictChoices(p *Plan) error {
	for _, it := range p.Items {
		if it.Action != domain.ActionConflict {
			continue
		}
		d := p.ConflictFor(it.Path)
		if d == nil {
			continue
		}
		if d.Choice == ConflictKeepNewer && d.LocalInvolved {
			return fmt.Errorf("sync: 冲突 %s 不能选“保留较新的”：本地文件夹的时间戳与云端服务器时间不可比，请逐项选保留左 / 保留右 / 保留双方", it.Path)
		}
		if d.Choice != "" && d.Choice != ConflictKeepLeft && d.Choice != ConflictKeepRight &&
			d.Choice != ConflictKeepBoth && d.Choice != ConflictKeepNewer {
			return fmt.Errorf("sync: 冲突 %s 的选择 %q 未知", it.Path, d.Choice)
		}
	}
	return nil
}
