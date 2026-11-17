//go:build darwin

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Environment records what the numbers were measured on and with.
type Environment struct {
	Commit   string `json:"commit"`
	Dirty    bool   `json:"dirty"`
	Machine  string `json:"machine"`
	CPUs     int    `json:"cpus"`
	MemoryGB int    `json:"memoryGB"`
	MacOS    string `json:"macOS"`
	Kernel   string `json:"kernel"`
	Go       string `json:"go"`
	Bun      string `json:"bun"`
	Chrome   string `json:"chrome"`
	Mygo     string `json:"mygo"`
}

func run(dir string, name string, args ...string) string {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "unavailable"
	}
	return strings.TrimSpace(string(out))
}

func readEnvironment(repo, chrome string) Environment {
	mem, _ := strconv.ParseInt(run("", "sysctl", "-n", "hw.memsize"), 10, 64)
	bun := os.Getenv("BUN")
	if bun == "" {
		bun = os.ExpandEnv("$HOME/.bun/bin/bun")
	}
	return Environment{
		Commit:   run(repo, "git", "rev-parse", "--short=9", "HEAD"),
		Dirty:    run(repo, "git", "status", "--porcelain", "--untracked-files=no") != "",
		Machine:  run("", "sysctl", "-n", "machdep.cpu.brand_string"),
		CPUs:     runtime.NumCPU(),
		MemoryGB: int(mem >> 30),
		MacOS:    run("", "sw_vers", "-productVersion") + " (" + run("", "sw_vers", "-buildVersion") + ")",
		Kernel:   run("", "uname", "-r"),
		Go:       strings.TrimPrefix(run(repo, "go", "version"), "go version "),
		Bun:      run(repo, bun, "--version"),
		Chrome:   run("", chrome, "--version"),
		Mygo:     run(repo, "go", "tool", "mygo", "version"),
	}
}

// load1 is the one-minute load average.
func load1() float64 {
	f := strings.Fields(strings.Trim(run("", "sysctl", "-n", "vm.loadavg"), "{} "))
	if len(f) == 0 {
		return -1
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

// Load gates a sample on the machine being quiet. The machine is shared, so a
// sample waits for the one-minute load to fall to Max or below, up to Wait; a
// sample taken above Max anyway is marked loaded and the report says so.
type Load struct {
	Max  float64
	Wait time.Duration
}

// Settle blocks until the load is acceptable or the wait is spent, and returns
// the load at the moment of the sample and whether it is above the limit.
func (l Load) Settle(ctx context.Context) (float64, bool) {
	deadline := time.Now().Add(l.Wait)
	for {
		v := load1()
		if v <= l.Max || time.Now().After(deadline) {
			return v, v > l.Max
		}
		select {
		case <-ctx.Done():
			return v, true
		case <-time.After(5 * time.Second):
		}
	}
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, time.Now().Format("15:04:05 ")+format+"\n", args...)
}
