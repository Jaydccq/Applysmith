//go:build smoke

package main

import (
	_ "embed"
	"github.com/egoist/mygo"
	"os"
)

// A native acceptance run isolates window state and the single-instance lock.
// The override exists only in smoke builds, never in the shipped app.
func init() {
	if dir := os.Getenv("APPLYSMITH_SMOKE_USER_DATA"); dir != "" {
		mygo.App.SetPath(mygo.PathUserData, dir)
	}
}

// A smoke build injects smoke.js into the dashboard page when the app is
// started with --smoke. Release builds do not contain the script.
const smokeBuilt = true

//go:embed smoke.js
var smokeScript string
