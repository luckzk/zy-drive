package sync

import (
	"testing"

	"github.com/luckzk/zy-drive/internal/domain"
)

// defaultExcludeRules is the default exclude list the new-job editor
// prefills (frontend/src/pages/SyncJobs.tsx, DEFAULT_EXCLUDE_RULES).
// It lives here too so the engine test proves the exact shipped
// defaults really exclude what they claim to — not just display.
var defaultExcludeRules = []string{
	".DS_Store",
	"Thumbs.db",
	"desktop.ini",
	"._*",
	"*.tmp",
	"~$*",
	".git/",
	"node_modules/",
	"*.log",
}

func TestMatchesDirectoryPatterns(t *testing.T) {
	cases := []struct {
		path    string
		pattern string
		want    bool
	}{
		// Trailing-slash directory rules: the dir itself and
		// everything under it, at any depth.
		{".git/config", ".git/", true},
		{".git", ".git/", true},
		{"a/.git/objects/x", ".git/", true},
		{"node_modules/pkg/index.js", "node_modules/", true},
		{"node_modules", "node_modules/", true},
		{"src/node_modules/a/b.js", "node_modules/", true},
		{"my.git/config", ".git/", false},
		{"git/config", ".git/", false},
		// Bare names (no glob, no slash): any segment matches,
		// so a directory of that name excludes its subtree.
		{"node_modules/pkg/index.js", "node_modules", true},
		{"a/.DS_Store", ".DS_Store", true},
		{".DS_Store", ".DS_Store", true},
		{"photos/.DS_Store", ".DS_Store", true},
		// Globs keep the old base-name / full-path behavior.
		{"a/b.tmp", "*.tmp", true},
		{"b.tmp", "*.tmp", true},
		{"a/b.txt", "*.tmp", false},
		{"._foo", "._*", true},
		{"dir/._foo", "._*", true},
		{"~$doc.docx", "~$*", true},
		{"a/~$doc.docx", "~$*", true},
		{"a.log", "*.log", true},
		{"a/b.log", "*.log", true},
		{"a.log/b.txt", "*.log", false},
		// Empty patterns never match.
		{"a/b.txt", "", false},
	}
	for _, c := range cases {
		if got := Matches(c.path, []string{c.pattern}); got != c.want {
			t.Errorf("Matches(%q, [%q]) = %v, want %v", c.path, c.pattern, got, c.want)
		}
	}
}

// TestCompareDefaultExcludes proves the shipped default rules
// really take effect in the compare stage: a tree holding
// .DS_Store, a node_modules subtree, a .git subtree, a .tmp file
// and a Thumbs.db produces a plan with none of those items —
// only the real files remain. (The scan itself still descends;
// Compare is where exclusion happens.)
func TestCompareDefaultExcludes(t *testing.T) {
	node := func(p string, isDir bool) domain.Node {
		return domain.Node{Path: p, Ref: p, Name: p, IsDir: isDir}
	}
	src := &Snapshot{Nodes: map[string]domain.Node{
		"photo.jpg":                 node("photo.jpg", false),
		"notes.txt":                 node("notes.txt", false),
		".DS_Store":                 node(".DS_Store", false),
		"sub/.DS_Store":             node("sub/.DS_Store", false),
		"Thumbs.db":                 node("Thumbs.db", false),
		"desktop.ini":               node("desktop.ini", false),
		"draft.tmp":                 node("draft.tmp", false),
		"._hidden":                  node("._hidden", false),
		"~$doc.docx":                node("~$doc.docx", false),
		"app.log":                   node("app.log", false),
		".git":                      node(".git", true),
		".git/config":               node(".git/config", false),
		"node_modules":              node("node_modules", true),
		"node_modules/pkg/index.js": node("node_modules/pkg/index.js", false),
		"node_modules/pkg/lib.js":   node("node_modules/pkg/lib.js", false),
	}}
	dst := &Snapshot{Nodes: map[string]domain.Node{}}
	changes := Compare(src, dst, domain.CompareSizeTime, defaultExcludeRules)
	excluded := map[string]bool{
		".DS_Store": true, "sub/.DS_Store": true, "Thumbs.db": true,
		"desktop.ini": true, "draft.tmp": true, "._hidden": true,
		"~$doc.docx": true, "app.log": true, ".git": true,
		".git/config": true, "node_modules": true,
		"node_modules/pkg/index.js": true, "node_modules/pkg/lib.js": true,
	}
	found := map[string]bool{}
	for _, ch := range changes {
		if excluded[ch.Path] {
			t.Errorf("plan contains excluded item %q (kind %s) — the default rules did not filter it", ch.Path, ch.Kind)
		}
		found[ch.Path] = true
	}
	// The real files survive: photo.jpg and notes.txt must be in
	// the plan as new.
	for _, want := range []string{"photo.jpg", "notes.txt"} {
		if !found[want] {
			t.Errorf("plan is missing %q — the excludes must not eat real files", want)
		}
	}
	// Re-run without excludes to prove the tree itself is seen.
	all := Compare(src, dst, domain.CompareSizeTime, nil)
	if len(all) != len(src.Nodes) {
		t.Fatalf("without excludes, plan has %d items, want %d (the whole tree)", len(all), len(src.Nodes))
	}
}
