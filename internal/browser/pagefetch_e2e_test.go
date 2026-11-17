//go:build browser

package browser

import (
	"slices"
	"testing"
	"time"
)

// A careers page that prints an application code to its console while it
// loads (Metabase): the lines are read from a tab of their own, which is
// closed after, and a file address is never opened.
func TestE2E_Browser_ConsoleLinesOfAPage(t *testing.T) {
	inst, fx := sharedInstance(t)
	h := inst.Lane(LaneApply)
	before := len(h.Tabs())
	lines, err := h.ConsoleLines(ctx30(t), "run-console", fx.Loopback("/console-code"), 300*time.Millisecond)
	ok(t, err)
	if !slices.Contains(lines, "application code 🌳🍀🌱") || !slices.Contains(lines, `{"step":2}`) {
		t.Fatalf("lines %q", lines)
	}
	if after := len(h.Tabs()); after != before {
		t.Fatalf("%d tabs after the read, %d before", after, before)
	}
	if _, err := h.ConsoleLines(ctx30(t), "run-console", "file:///etc/hosts", 0); err == nil {
		t.Fatal("a file address was opened")
	}
}
