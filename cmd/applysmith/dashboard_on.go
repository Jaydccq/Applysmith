//go:build dashboard

package main

import (
	"io/fs"

	"applysmith/web"
)

// dashboardAssets is the built dashboard, embedded by `make build`.
func dashboardAssets() (fs.FS, error) { return web.Dist() }
