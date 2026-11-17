//go:build browser

package browser

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Both windows report navigator.webdriver false. The test browser is headless,
// so its user agent carries the marker the product's windows never have; the
// marker rule is covered where the report is judged.
func TestE2E_Browser_CheckFingerprintReadsBothWindows(t *testing.T) {
	inst, _ := sharedInstance(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	got := CheckFingerprint(ctx, inst)
	if len(got) != 2 || got[0].Lane != LaneApply || got[1].Lane != LaneScan {
		t.Fatalf("windows = %+v", got)
	}
	for _, w := range got {
		if !w.Attached || w.Err != "" {
			t.Fatalf("%s window: %+v", w.Lane, w)
		}
		if w.Webdriver == nil || *w.Webdriver {
			t.Errorf("%s window: navigator.webdriver = %v, want false", w.Lane, w.Webdriver)
		}
		if !strings.Contains(w.UserAgent, "Chrome/") {
			t.Errorf("%s window: user agent %q", w.Lane, w.UserAgent)
		}
	}
}
