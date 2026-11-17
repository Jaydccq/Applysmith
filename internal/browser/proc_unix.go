//go:build !windows

package browser

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// detach starts the child in its own session so it survives the server's
// death and its terminal.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// askToQuit asks the browser process to shut down gracefully.
func askToQuit(pid int) {
	_ = syscall.Kill(pid, syscall.SIGTERM)
}

// killPID force-kills one process.
func killPID(pid int) {
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// killGroup force-kills the whole process group led by pid.
func killGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

func ps(ctx context.Context, field string, pid int) (string, error) {
	cmd := exec.CommandContext(ctx, "ps", "-o", field+"=", "-p", strconv.Itoa(pid))
	cmd.Env = []string{"LC_ALL=C", "PATH=/bin:/usr/bin"}
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// processStart returns the start time of pid with one-second resolution. A
// recycled pid shows a different value.
func processStart(ctx context.Context, pid int) (string, error) {
	return ps(ctx, "lstart", pid)
}

// processCommand returns the full command line of pid.
func processCommand(ctx context.Context, pid int) (string, error) {
	return ps(ctx, "command", pid)
}

// platformSupported reports whether this platform can verify and stop the
// browser process.
func platformSupported() error { return nil }

// processGroup returns the process group id of pid.
func processGroup(ctx context.Context, pid int) (int, error) {
	out, err := ps(ctx, "pgid", pid)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(out)
}

// listener is one TCP listening socket: its owning pid and bind address.
type listener struct {
	PID  int
	Addr string // as lsof prints it: 127.0.0.1:9222, [::1]:9222, *:9222
}

// listeners returns the sockets listening on the TCP port, on any address.
func listeners(ctx context.Context, port int) ([]listener, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "lsof", "-nP", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-Fpn")
	cmd.Env = []string{"LC_ALL=C", "PATH=/bin:/usr/bin:/usr/sbin"}
	out, err := cmd.Output()
	var ee *exec.ExitError
	if err != nil && !(errors.As(err, &ee) && len(out) == 0) {
		return nil, err
	}
	var res []listener
	pid := 0
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "p"):
			pid, _ = strconv.Atoi(line[1:])
		case strings.HasPrefix(line, "n") && pid > 0:
			res = append(res, listener{PID: pid, Addr: line[1:]})
		}
	}
	return res, nil
}
