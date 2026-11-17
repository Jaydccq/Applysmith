//go:build windows

package tunnel

import (
	"os/exec"
	"time"
)

func setProcessGroup(*exec.Cmd) {}

// osProcs cannot inspect processes on Windows: a recorded identifier is never
// treated as a live cloudflared, so nothing unrelated is ever terminated.
type osProcs struct{}

func (osProcs) Alive(int) bool                  { return false }
func (osProcs) Command(int) string              { return "" }
func (osProcs) StartTime(int) (time.Time, bool) { return time.Time{}, false }
func (osProcs) Terminate(int) error             { return nil }
func (osProcs) Kill(int) error                  { return nil }
