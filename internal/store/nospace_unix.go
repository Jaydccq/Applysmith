//go:build unix

package store

import "syscall"

var errNoSpace error = syscall.ENOSPC
