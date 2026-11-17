//go:build darwin

package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"applysmith/internal/api"
	"applysmith/internal/workspace"
)

// Target is something that serves the dashboard when started: the packaged
// app, or the server command on its own.
type Target struct {
	Name     string
	Artifact string // the bundle or the binary that is copied for a cold start
	Exec     func(copy string) string
	Args     func(workspace string) []string
	WebView  bool // the target opens a window whose WebKit helpers belong to it
}

// StartSample is one launch.
type StartSample struct {
	ServedMS float64 `json:"servedMS"`
	Load1    float64 `json:"load1"`
	Loaded   bool    `json:"loaded"`
	Settled  Memory  `json:"settledMemory"`
}

// StartResult is the cold and warm series of one target.
type StartResult struct {
	Target    string        `json:"target"`
	Cold      []StartSample `json:"cold"`
	Warm      []StartSample `json:"warm"`
	ColdMS    Summary       `json:"coldMS"`
	WarmMS    Summary       `json:"warmMS"`
	ColdRSS   Summary       `json:"coldRSSMB"`
	WarmRSS   Summary       `json:"warmRSSMB"`
	Breakdown Memory        `json:"medianWarmBreakdown"`
	Loaded    int           `json:"loadedSamples"`
}

const apiToken = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// measureStartup takes n cold launches (a fresh copy of the artifact, a new
// workspace) and n warm ones (one workspace and one copy, already launched
// once). Time runs from the process start to the dashboard page coming back
// for the launch code. Memory is read settle after that.
func measureStartup(ctx context.Context, t Target, n int, settle time.Duration, load Load) (StartResult, error) {
	res := StartResult{Target: t.Name}
	scratch, err := os.MkdirTemp("", "applysmith-measure-")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(scratch)

	for i := 0; i < n; i++ {
		s, err := oneLaunch(ctx, t, filepath.Join(scratch, "cold"), true, settle, load)
		if err != nil {
			return res, err
		}
		res.Cold = append(res.Cold, s)
	}
	warm := filepath.Join(scratch, "warm")
	if _, err := oneLaunch(ctx, t, warm, true, 0, Load{Max: 1e9}); err != nil { // initialises the workspace, not measured
		return res, err
	}
	var breakdowns []Memory
	for i := 0; i < n; i++ {
		s, err := oneLaunch(ctx, t, warm, false, settle, load)
		if err != nil {
			return res, err
		}
		res.Warm = append(res.Warm, s)
		breakdowns = append(breakdowns, s.Settled)
	}
	res.ColdMS, res.ColdRSS = summaries(res.Cold)
	res.WarmMS, res.WarmRSS = summaries(res.Warm)
	for _, s := range append(append([]StartSample{}, res.Cold...), res.Warm...) {
		if s.Loaded {
			res.Loaded++
		}
	}
	res.Breakdown = medianSample(breakdowns)
	return res, nil
}

func summaries(ss []StartSample) (ms, rss Summary) {
	var a, b []float64
	for _, s := range ss {
		a = append(a, s.ServedMS)
		b = append(b, s.Settled.TotalMB)
	}
	return summarize(a), summarize(b)
}

func medianSample(ms []Memory) Memory {
	if len(ms) == 0 {
		return Memory{}
	}
	sorted := append([]Memory(nil), ms...)
	for i := range sorted {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].TotalMB < sorted[i].TotalMB {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	return sorted[len(sorted)/2]
}

// oneLaunch starts the target in dir (fresh: a new copy and a new workspace),
// times it to a served dashboard, reads the memory of its process tree and
// stops it.
func oneLaunch(ctx context.Context, t Target, dir string, fresh bool, settle time.Duration, load Load) (StartSample, error) {
	var s StartSample
	installed := filepath.Join(dir, filepath.Base(t.Artifact))
	ws := filepath.Join(dir, "workspace")
	if fresh {
		if err := os.RemoveAll(dir); err != nil {
			return s, err
		}
		if err := os.MkdirAll(filepath.Join(dir, "home"), 0o700); err != nil {
			return s, err
		}
		if out, err := exec.Command("ditto", t.Artifact, installed).CombinedOutput(); err != nil {
			return s, errors.New("copy " + t.Artifact + ": " + string(out))
		}
		if err := os.MkdirAll(ws, 0o700); err != nil {
			return s, err
		}
	}
	s.Load1, s.Loaded = load.Settle(ctx)

	before := map[int]bool{}
	for _, p := range webkitHelpers(processTable()) {
		before[p.PID] = true
	}
	cmd := exec.Command(t.Exec(installed), t.Args(ws)...)
	cmd.Env = append(os.Environ(), "APPLYSMITH_WORKSPACE="+ws, "APPLYSMITH_PORT=0", "APPLYSMITH_SECRET_API_TOKEN="+apiToken, "HOME="+filepath.Join(dir, "home"))
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	begin := time.Now()
	if err := cmd.Start(); err != nil {
		return s, err
	}
	pid := cmd.Process.Pid
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	stop := func() {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(15 * time.Second):
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			<-exited
		}
	}

	served, err := waitServed(ws, pid, exited, 120*time.Second)
	if err != nil {
		stop()
		return s, err
	}
	s.ServedMS = float64(served.Sub(begin).Microseconds()) / 1000
	time.Sleep(settle)

	table := processTable()
	tree := descendants(table, pid)
	var helpers []Proc
	if t.WebView {
		helpers = newHelpers(table, before, responsible(pid))
	}
	s.Settled = memoryOf(append(tree, helpers...))
	stop()
	if syscall.Kill(pid, 0) == nil {
		return s, errors.New("process still running after stop")
	}
	if t.WebView {
		// Only helpers that left with the app are the app's: another app that
		// started in the same seconds keeps its own.
		time.Sleep(2 * time.Second)
		alive := map[int]bool{}
		for _, p := range processTable() {
			alive[p.PID] = true
		}
		var own []Proc
		for _, h := range helpers {
			if !alive[h.PID] {
				own = append(own, h)
			}
		}
		s.Settled = memoryOf(append(tree, own...))
	}
	return s, nil
}

// newHelpers are the WebKit helpers that appeared since the launch and are
// held accountable to the same process as the app.
func newHelpers(table []Proc, before map[int]bool, resp int) []Proc {
	var out []Proc
	for _, p := range webkitHelpers(table) {
		if !before[p.PID] && (resp < 0 || responsible(p.PID) == resp) {
			out = append(out, p)
		}
	}
	return out
}

// waitServed polls until the process named by pid serves a healthy dashboard.
func waitServed(root string, pid int, exited <-chan struct{}, limit time.Duration) (time.Time, error) {
	state := workspace.Open(root).StateDir()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return time.Time{}, io.ErrUnexpectedEOF
		default:
		}
		if info, err := api.ReadLockInfo(state); err == nil && info.PID == pid {
			if u, err := url.Parse(info.Origin); err == nil {
				if h, ok := api.ProbeServer(u.Host); ok && h.Status == "ok" && dashboardServed(info) {
					return time.Now(), nil
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return time.Time{}, os.ErrDeadlineExceeded
}

// dashboardServed checks that the dashboard is served without taking a session:
// the document address answers the sessionless landing page (401), and the
// dashboard's index document, which needs no session, comes back with its root
// element. The launch code in the lock document is never redeemed, because the
// app's own window needs it to open.
func dashboardServed(info api.LockInfo) bool {
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	landing, err := client.Get(info.Origin + "/")
	if err != nil {
		return false
	}
	_ = landing.Body.Close()
	if landing.StatusCode != http.StatusUnauthorized {
		return false
	}
	resp, err := client.Get(info.Origin + "/index.html")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode == http.StatusOK && strings.Contains(string(body), `id="root"`)
}
