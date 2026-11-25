// SPDX-License-Identifier: MPL-2.0
package version

import (
	"os"
	"strings"
	"testing"
)

// TestVersionMatchesRootFile guards the single source of truth:
// internal/version.AppVersion must equal the VERSION file at the
// repository root. Bump both in the same commit.
func TestVersionMatchesRootFile(t *testing.T) {
	data, err := os.ReadFile("../../VERSION")
	if err != nil {
		t.Fatalf("read VERSION: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != AppVersion {
		t.Fatalf("version drift: VERSION file=%q, AppVersion=%q — bump both", got, AppVersion)
	}
}
