//go:build !windows

package ledger

import (
	"errors"
	"os"
	"syscall"
)

// ProcessRunning reports whether a process with this id exists. A process the
// caller may not signal still exists.
func ProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
