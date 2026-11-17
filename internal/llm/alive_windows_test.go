//go:build windows

package llm

func processAlive(pid int) bool { return false }
