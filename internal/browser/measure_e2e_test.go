//go:build browser

package browser

import (
	"os"
	"testing"
	"time"
)

// TestMeasure_LaneChromeHeld holds one apply-lane tab and one scan-lane tab
// open on a fixture page for the duration named by APPLYSMITH_MEASURE_HOLD, so
// that cmd/measure can read the memory of the Chrome process tree while it is
// settled. It asserts nothing about memory and is skipped without the variable.
func TestMeasure_LaneChromeHeld(t *testing.T) {
	hold, err := time.ParseDuration(os.Getenv("APPLYSMITH_MEASURE_HOLD"))
	if err != nil || hold <= 0 {
		t.Skip("set APPLYSMITH_MEASURE_HOLD to a duration to hold the lanes open (cmd/measure)")
	}
	inst, fx := sharedInstance(t)
	for _, lane := range []Lane{LaneApply, LaneScan} {
		tab := openTab(t, inst, lane, "run-measure")
		navigate(t, inst, lane, tab, fx.Loopback("/probe"))
	}
	time.Sleep(hold)
}
