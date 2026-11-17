//go:build windows

package browser

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
)

var errUnsupported = errors.New("process identity checks are not implemented on this platform")

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000200} // CREATE_NEW_PROCESS_GROUP
}

func processAlive(int) bool { return false }

func askToQuit(int) {}

func killGroup(int) {}

func killPID(int) {}

func processStart(context.Context, int) (string, error) { return "", errUnsupported }

func processCommand(context.Context, int) (string, error) { return "", errUnsupported }

func platformSupported() error {
	return newError(CodePlatformUnsupported, "managed Chrome is not supported on this platform yet: the browser process cannot be verified or stopped safely")
}

func processGroup(context.Context, int) (int, error) { return 0, errUnsupported }

type listener struct {
	PID  int
	Addr string
}

func listeners(context.Context, int) ([]listener, error) { return nil, errUnsupported }
