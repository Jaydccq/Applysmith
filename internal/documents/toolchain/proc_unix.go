//go:build !windows

package toolchain

import (
	"os/exec"
	"syscall"
)

// ownGroup puts the child in a process group of its own, so cancellation can
// stop every descendant of an engine or a verifier.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalGroup(pid int, kill bool) {
	sig := syscall.SIGTERM
	if kill {
		sig = syscall.SIGKILL
	}
	_ = syscall.Kill(-pid, sig)
}
