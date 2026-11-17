//go:build !dashboard

package cli

import (
	"strings"
	"testing"
)

func TestServeWithoutDashboardTagShowsPlaceholder(t *testing.T) {
	rs := newTestEnv(t).startServe()
	resp, got := rs.dashboardDocument()
	if resp.StatusCode != 200 || !strings.Contains(string(got), "not built into this binary") {
		t.Fatalf("GET /: %d %s", resp.StatusCode, got)
	}
}
