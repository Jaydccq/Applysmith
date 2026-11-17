// Package web embeds the built dashboard. The bundle in dist is generated
// by `make generate` and is not committed, so this package compiles only
// after generation.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the built dashboard rooted at its index.html.
func Dist() (fs.FS, error) { return fs.Sub(dist, "dist") }
