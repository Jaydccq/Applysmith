//go:build !windows

package tunnel

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// setProcessGroup puts the child in its own group, so a signal sent to this
// program's group (a terminal interrupt) does not reach it before the
// supervisor has stopped it in order.
func setProcessGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// osProcs inspects and signals processes by identifier.
type osProcs struct{}

func (osProcs) Alive(pid int) bool {
	p, err := os.FindProcess(pid)
	return err == nil && p.Signal(syscall.Signal(0)) == nil
}

func (osProcs) Command(pid int) string {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// StartTime reads the start of the process as the process list prints it, in
// the local zone.
func (osProcs) StartTime(pid int) (time.Time, bool) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=").Output()
	if err != nil {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("Mon Jan _2 15:04:05 2006", strings.Join(strings.Fields(string(out)), " "), time.Local)
	return t, err == nil
}

func (osProcs) Terminate(pid int) error { return sendSignal(pid, syscall.SIGTERM) }
func (osProcs) Kill(pid int) error      { return sendSignal(pid, syscall.SIGKILL) }

func sendSignal(pid int, sig syscall.Signal) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(sig)
}
