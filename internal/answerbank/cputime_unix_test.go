//go:build !windows

package answerbank

import (
	"runtime"
	"time"

	"golang.org/x/sys/unix"
)

// cpuNow is the CPU time of the calling goroutine's thread. It locks the
// goroutine to that thread, so two reads in one test measure the matcher's own
// work: other goroutines, the GC's background workers and other tests do not
// count against a budget, and a loaded machine does not either.
func cpuNow() time.Duration {
	runtime.LockOSThread()
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_THREAD_CPUTIME_ID, &ts); err != nil {
		return 0
	}
	return time.Duration(ts.Nano())
}
