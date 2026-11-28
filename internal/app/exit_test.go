// SPDX-License-Identifier: MPL-2.0
package app

import "testing"

// TestConfirmExit covers the exit-dialog decision (v0.19.0): "stay"
// does nothing and never arms the flag; "quit" arms the flag first
// and then calls the quit hook; without a hook (dev shell, tests)
// a quit decision only arms the flag and does not panic.
func TestConfirmExit(t *testing.T) {
	a := newTestApp(t)
	if a.ExitConfirmed() {
		t.Fatal("fresh app should not be exit-confirmed")
	}
	a.ConfirmExit(false)
	if a.ExitConfirmed() {
		t.Fatal("ConfirmExit(false) must not arm the flag")
	}

	quits := 0
	a.SetQuitHook(func() { quits++ })
	a.ConfirmExit(true)
	if !a.ExitConfirmed() {
		t.Fatal("ConfirmExit(true) must arm the flag before quitting")
	}
	if quits != 1 {
		t.Fatalf("quit hook called %d times, want 1", quits)
	}
	// A second true decision re-runs the hook (the flag is already
	// set); mygo.App.Quit itself is idempotent while quitting.
	a.ConfirmExit(true)
	if quits != 2 {
		t.Fatalf("second ConfirmExit(true): quit hook called %d times, want 2", quits)
	}
}

func TestConfirmExitWithoutHook(t *testing.T) {
	a := newTestApp(t)
	a.ConfirmExit(true) // must not panic with no hook installed
	if !a.ExitConfirmed() {
		t.Fatal("ConfirmExit(true) must arm the flag even without a hook")
	}
}
