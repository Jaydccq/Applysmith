package browser

import (
	"strings"
	"testing"
)

// Fixture-only switches must never change the browser used by a real run.
func TestBrowser_LaunchArgs_ComponentUpdatesDisabledOnlyForFixtures(t *testing.T) {
	for _, tc := range []struct {
		rules    string
		disabled bool
	}{{"", false}, {"MAP *.test 127.0.0.1", true}} {
		args := strings.Join(launchArgs("/profile", 9333, tc.rules), " ")
		if got := strings.Contains(args, "--disable-component-update"); got != tc.disabled {
			t.Errorf("rules=%q component update disabled=%v, want %v", tc.rules, got, tc.disabled)
		}
	}
}
