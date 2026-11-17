//go:build dashboard

package cli

import (
	"io/fs"
	"testing"

	"applysmith/web"
)

// Scenario "Headless use": serve with the dashboard tag serves the built
// index document at the loopback address.
func TestServeWithDashboardTagServesBundle(t *testing.T) {
	dist, err := web.Dist()
	if err != nil {
		t.Fatal(err)
	}
	want, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	rs := newTestEnv(t).startServe()
	resp, got := rs.dashboardDocument()
	if resp.StatusCode != 200 || string(got) != string(want) {
		t.Fatalf("GET /: %d, %d bytes (want the %d-byte bundle index)", resp.StatusCode, len(got), len(want))
	}
}
