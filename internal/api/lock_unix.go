//go:build !windows

package api

import (
	"errors"
	"os"
	"syscall"
)

// lockFile tries to take the lock without waiting. A shared lock is for a
// probe: probes do not exclude each other, only a holder.
func lockFile(f *os.File, shared bool) (held bool, err error) {
	how := syscall.LOCK_EX
	if shared {
		how = syscall.LOCK_SH
	}
	for {
		err = syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB)
		switch {
		case err == nil:
			return false, nil
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EWOULDBLOCK):
			return true, nil
		}
		return false, err
	}
}

func unlockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
