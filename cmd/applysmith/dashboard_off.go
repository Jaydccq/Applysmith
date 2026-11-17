//go:build !dashboard

package main

import "io/fs"

// dashboardAssets is nil without the dashboard tag, so a plain `go build` of a
// fresh checkout needs no generated bundle and the server shows a placeholder.
func dashboardAssets() (fs.FS, error) { return nil, nil }
