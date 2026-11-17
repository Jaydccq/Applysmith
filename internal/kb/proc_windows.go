//go:build windows

package kb

import (
	"os/exec"
	"syscall"
)

func groupAttr() *syscall.SysProcAttr { return nil }

// signalGroup ends the program itself. Windows has no process-group signal
// here, so a child the program started can outlive a cancelled call; a job
// object would close that gap.
func signalGroup(cmd *exec.Cmd, kill bool) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
