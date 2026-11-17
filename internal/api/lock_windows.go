//go:build windows

package api

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// The lock covers one byte far beyond the document so that other processes
// can still read the lock file while it is held.
const lockOffset = 0x7fff0000

func lockOverlapped() *windows.Overlapped { return &windows.Overlapped{Offset: lockOffset} }

// lockFile tries to take the lock without waiting. A shared lock is for a
// probe: probes do not exclude each other, only a holder.
func lockFile(f *os.File, shared bool) (held bool, err error) {
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if !shared {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	err = windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, lockOverlapped())
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		return true, nil
	}
	return false, err
}

func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, lockOverlapped())
}
