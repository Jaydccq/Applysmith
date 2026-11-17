//go:build !windows

package llm

import (
	"os/exec"
	"syscall"
)

// groupAttr starts the program in its own process group.
func groupAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// signalGroup sends SIGTERM, or SIGKILL when kill is set, to the program's
// process group.
func signalGroup(cmd *exec.Cmd, kill bool) error {
	if cmd.Process == nil {
		return nil
	}
	sig := syscall.SIGTERM
	if kill {
		sig = syscall.SIGKILL
	}
	err := syscall.Kill(-cmd.Process.Pid, sig)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}
