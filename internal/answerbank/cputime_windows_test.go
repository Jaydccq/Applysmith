//go:build windows

package answerbank

import "time"

var wallStart = time.Now()

// cpuNow falls back to wall-clock time where thread CPU time is not read.
func cpuNow() time.Duration { return time.Since(wallStart) }
