//go:build darwin

package main

import (
	"os/exec"
	"strconv"
	"strings"
)

// Proc is one row of the process table.
type Proc struct {
	PID, PPID int
	RSSKB     int
	Command   string
}

// Kind classifies a process for the memory breakdown.
func (p Proc) Kind() string {
	switch {
	case strings.Contains(p.Command, "com.apple.WebKit."):
		return "webview"
	case strings.Contains(p.Command, "Google Chrome"):
		return "chrome"
	case strings.Contains(p.Command, ".test"):
		return "test-binary"
	}
	return "app"
}

// parseTable reads `ps -Ao pid=,ppid=,rss=,command=` output.
func parseTable(out string) []Proc {
	var ps []Proc
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		rss, e3 := strconv.Atoi(f[2])
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		ps = append(ps, Proc{PID: pid, PPID: ppid, RSSKB: rss, Command: strings.Join(f[3:], " ")})
	}
	return ps
}

func processTable() []Proc {
	out, err := exec.Command("ps", "-Ao", "pid=,ppid=,rss=,command=").Output()
	if err != nil {
		return nil
	}
	return parseTable(string(out))
}

// descendants returns root and every process below it.
func descendants(table []Proc, root int) []Proc {
	kids := map[int][]Proc{}
	byPID := map[int]Proc{}
	for _, p := range table {
		kids[p.PPID] = append(kids[p.PPID], p)
		byPID[p.PID] = p
	}
	var out []Proc
	seen := map[int]bool{}
	var walk func(pid int)
	walk = func(pid int) {
		if seen[pid] {
			return
		}
		seen[pid] = true
		if p, ok := byPID[pid]; ok {
			out = append(out, p)
		}
		for _, k := range kids[pid] {
			walk(k.PID)
		}
	}
	walk(root)
	return out
}

// Memory is resident memory in MB by kind. ps counts a page shared by two
// processes in both, so the sum is an upper bound of what the tree costs.
type Memory struct {
	TotalMB   float64            `json:"totalMB"`
	ByKindMB  map[string]float64 `json:"byKindMB"`
	Processes int                `json:"processes"`
}

func memoryOf(ps []Proc) Memory {
	m := Memory{ByKindMB: map[string]float64{}, Processes: len(ps)}
	for _, p := range ps {
		mb := float64(p.RSSKB) / 1024
		m.TotalMB += mb
		m.ByKindMB[p.Kind()] += mb
	}
	return m
}

// peak keeps the sample with the highest total.
func (m *Memory) peak(o Memory) {
	if o.TotalMB > m.TotalMB {
		*m = o
	}
}

// webkitHelpers are the WebView helper processes of the table. They are
// children of launchd, not of the app, so the tree walk cannot find them.
func webkitHelpers(table []Proc) []Proc {
	var out []Proc
	for _, p := range table {
		if strings.Contains(p.Command, "com.apple.WebKit.") {
			out = append(out, p)
		}
	}
	return out
}
