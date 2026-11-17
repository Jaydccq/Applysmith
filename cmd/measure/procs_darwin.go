//go:build darwin && cgo

package main

/*
extern int responsibility_get_pid_responsible_for_pid(int);
*/
import "C"

// responsible is the process the system holds accountable for pid; a WebKit
// helper has the same one as the app that asked for it.
func responsible(pid int) int { return int(C.responsibility_get_pid_responsible_for_pid(C.int(pid))) }
