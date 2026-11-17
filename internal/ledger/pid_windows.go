//go:build windows

package ledger

import "os"

// ProcessRunning reports whether a process with this id exists: opening a
// handle to a process that is gone fails.
func ProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}
