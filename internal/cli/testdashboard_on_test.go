//go:build dashboard

package cli

import (
	"io/fs"

	"applysmith/web"
)

// testDashboard is the bundle the dashboard-tagged tests serve, as the
// cmd/applysmith binary does.
func testDashboard() func() (fs.FS, error) { return web.Dist }
