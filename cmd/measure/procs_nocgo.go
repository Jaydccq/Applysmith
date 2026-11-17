//go:build darwin && !cgo

package main

func responsible(pid int) int { return -1 }
