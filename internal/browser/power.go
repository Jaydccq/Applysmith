package browser

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
)

// awake keeps the machine from idle and disk sleep while any lease is held.
// The count is per lease; the hold is released with the last lease. System
// sleep is not blocked, so closing the lid still suspends the machine.
type awake struct {
	// start begins the hold and returns what ends it. Replaced by tests.
	start func() (stop func())

	mu   sync.Mutex
	n    int
	stop func()
}

// hold is the process-wide power hold.
var hold = &awake{start: startIdleInhibitor}

func (a *awake) acquire() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.n++
	if a.n == 1 && a.start != nil {
		a.stop = a.start()
	}
}

func (a *awake) release() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.n == 0 {
		return
	}
	a.n--
	if a.n == 0 && a.stop != nil {
		a.stop()
		a.stop = nil
	}
}

// caffeinateArgs asks for no idle sleep (-i) and no disk sleep (-m) for as
// long as process pid lives, so a server that dies without releasing cannot
// keep the machine awake. System sleep (-s) is deliberately not requested.
func caffeinateArgs(pid int) []string {
	return []string{"-i", "-m", "-w", strconv.Itoa(pid)}
}

// startIdleInhibitor holds the assertion with the platform's tool: caffeinate
// on macOS. Other platforms have no hold here; a missing tool is not an error,
// because the hold is a convenience for long runs.
func startIdleInhibitor() func() {
	if runtime.GOOS != "darwin" {
		return nil
	}
	cmd := exec.Command("caffeinate", caffeinateArgs(os.Getpid())...)
	if err := cmd.Start(); err != nil {
		return nil
	}
	return func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
}
