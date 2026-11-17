//go:build !dashboard

package cli

import "io/fs"

// testDashboard is nil without the dashboard tag: serve shows the placeholder.
func testDashboard() func() (fs.FS, error) { return nil }
