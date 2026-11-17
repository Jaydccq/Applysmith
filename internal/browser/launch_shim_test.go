package browser

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func shimLaunch(t *testing.T, body string, environ []string) (rec string, err error) {
	t.Helper()
	rec = t.TempDir()
	bin := shim(t, strings.ReplaceAll(body, "$REC", rec))
	start := time.Now()
	_, err = Launch(context.Background(), LaunchOptions{
		Chrome:     Chrome{Path: bin, Version: "155.0.0.0", Major: 155},
		ProfileDir: filepath.Join(t.TempDir(), "profile"),
		Environ:    environ,
	})
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("launch took %v, want an immediate failure", d)
	}
	return rec, err
}

func TestBrowser_Launch_EarlyExitReportsStderr(t *testing.T) {
	_, err := shimLaunch(t, `echo "boom: example failure line" >&2; exit 3`, os.Environ())
	if code, _ := CodeOf(err); code != CodeLaunchFailed {
		t.Fatalf("code %q err %v, want launch-failed", code, err)
	}
	if !strings.Contains(err.Error(), "boom: example failure line") {
		t.Fatalf("message lacks the child's output: %v", err)
	}
}

func TestBrowser_Launch_EnvAllowlist(t *testing.T) {
	environ := []string{
		"PATH=/usr/bin:/bin", "HOME=/home/example", "USER=example", "LANG=en_US.UTF-8", "LC_ALL=C", "TMPDIR=/tmp",
		"APPLYSMITH_SECRET_EXAMPLE=hunter2", "SERVICE_API_KEY=k1", "SERVICE_TOKEN=t1", "DB_PASSWORD=p1", "CLIENT_SECRET=s1",
		"UNRELATED=value",
	}
	rec, _ := shimLaunch(t, `env > "$REC/env"; exit 1`, environ)
	b, err := os.ReadFile(filepath.Join(rec, "env"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, bad := range []string{"APPLYSMITH_SECRET_EXAMPLE", "hunter2", "SERVICE_API_KEY", "SERVICE_TOKEN", "DB_PASSWORD", "CLIENT_SECRET", "UNRELATED"} {
		if strings.Contains(got, bad) {
			t.Errorf("browser environment contains %q", bad)
		}
	}
	for _, want := range []string{"HOME=/home/example", "LANG=en_US.UTF-8", "LC_ALL=C"} {
		if !strings.Contains(got, want) {
			t.Errorf("browser environment lacks %q", want)
		}
	}
}

func TestBrowser_Launch_ArgumentContract(t *testing.T) {
	rec, _ := shimLaunch(t, `printf '%s\n' "$@" > "$REC/args"; exit 1`, os.Environ())
	b, err := os.ReadFile(filepath.Join(rec, "args"))
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(b)), "\n")
	if f := forbiddenFlag(args); f != "" {
		t.Fatalf("forbidden flag %s in %v", f, args)
	}
	var port string
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "--remote-debugging-port="); ok {
			port = v
		}
	}
	if port == "" || port == "0" {
		t.Fatalf("no explicit nonzero debugging port in %v", args)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--no-first-run", "--no-default-browser-check", "--disable-background-timer-throttling",
		"--disable-renderer-backgrounding", "--disable-backgrounding-occluded-windows",
		"--password-store=basic", "--use-mock-keychain", "--user-data-dir=",
		"MacWebContentsOcclusion", "WebContentsOcclusion", "CalculateNativeWinOcclusion", "Translate", "MacAppCodeSignClone",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("arguments lack %q", want)
		}
	}
}

func TestBrowser_ForbiddenFlags_AreDetected(t *testing.T) {
	// The managed profile never loads an extension by flag, nor turns on the
	// switch Chrome 137 and newer needs to load one over DevTools.
	for _, bad := range []string{"--enable-automation", "--headless=new", "--remote-debugging-pipe", "--remote-debugging-port=0",
		"--remote-debugging-address=0.0.0.0", "--remote-allow-origins=*", "--load-extension=/x", "--load-extension",
		"--enable-unsafe-extension-debugging", "--proxy-server=x"} {
		if forbiddenFlag([]string{bad}) == "" {
			t.Errorf("%s not detected as forbidden", bad)
		}
	}
}

func TestBrowser_InstanceRecord_AtomicPrivateRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "instance.json")
	in := Record{PID: 4242, Start: "Sat Oct 11 04:30:00 2026", Profile: "/p", Port: 9333, ChromeVersion: "155.0.0.0", ApplyWindows: []int{1}, ScanWindows: []int{2, 3}}
	if err := WriteRecord(p, in); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("record mode %v, want 0600", st.Mode().Perm())
	}
	if strings.Contains(readFile(t, p), "ws:") || strings.Contains(readFile(t, p), "devtools") {
		t.Fatal("record persists a WebSocket address")
	}
	out, ok := ReadRecord(p)
	if !ok || out.PID != in.PID || out.Port != in.Port || len(out.ScanWindows) != 2 {
		t.Fatalf("round trip: %+v ok=%v", out, ok)
	}
	if err := os.WriteFile(p, []byte("{garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadRecord(p); ok {
		t.Fatal("garbage read as a record")
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBrowser_Launch_TimeoutTerminatesChild(t *testing.T) {
	rec := t.TempDir()
	bin := shim(t, strings.ReplaceAll(`echo $$ > "$REC/pid"; exec sleep 60`, "$REC", rec))
	start := time.Now()
	_, err := Launch(context.Background(), LaunchOptions{
		Chrome: Chrome{Path: bin, Version: "155.0.0.0", Major: 155}, ProfileDir: filepath.Join(t.TempDir(), "p"),
		Environ: os.Environ(), timeout: 1500 * time.Millisecond,
	})
	if code, _ := CodeOf(err); code != CodeLaunchTimeout {
		t.Fatalf("code %q err %v, want launch-timeout", code, err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
	b, _ := os.ReadFile(filepath.Join(rec, "pid"))
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal("shim did not record a pid")
	}
	// The child is waited for before Launch returns, so it must be gone.
	if processAlive(pid) {
		t.Fatalf("child %d still running after a failed launch", pid)
	}
}

func TestBrowser_Stop_EscalatesAndReaps(t *testing.T) {
	old1, old2 := stopGrace, killGrace
	stopGrace, killGrace = 300*time.Millisecond, 3*time.Second
	t.Cleanup(func() { stopGrace, killGrace = old1, old2 })
	rec := t.TempDir()
	bin := shim(t, strings.ReplaceAll(`trap "" TERM; touch "$REC/ready"; while :; do sleep 1; done`, "$REC", rec))
	p, err := spawnForTest(t, bin)
	if err != nil {
		t.Fatal(err)
	}
	for range 100 {
		if _, err := os.Stat(filepath.Join(rec, "ready")); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	asked := false
	out := p.stop(context.Background(), func() { asked = true; askToQuit(p.PID) })
	if !asked || out != StopKilled {
		t.Fatalf("outcome %q asked=%v, want killed after a graceful request", out, asked)
	}
	if processAlive(p.PID) {
		t.Fatal("process survived")
	}
}

func TestBrowser_Stop_GracefulExitReportsClosed(t *testing.T) {
	bin := shim(t, `while :; do sleep 1; done`)
	p, err := spawnForTest(t, bin)
	if err != nil {
		t.Fatal(err)
	}
	if out := p.stop(context.Background(), func() { askToQuit(p.PID) }); out != StopClosed {
		t.Fatalf("outcome %q, want closed", out)
	}
}

func spawnForTest(t *testing.T, bin string) (*Process, error) {
	t.Helper()
	p, err := spawn(LaunchOptions{Chrome: Chrome{Path: bin}, Environ: os.Environ()}, t.TempDir(), 1)
	if err == nil {
		t.Cleanup(func() { killGroup(p.PID) })
	}
	return p, err
}

// Headless exists for the test harness; a launch that does not ask for it never
// carries the flag.
func TestBrowser_Launch_HeadlessOnlyWhenATestAsksForIt(t *testing.T) {
	for _, headless := range []bool{false, true} {
		rec := t.TempDir()
		bin := shim(t, strings.ReplaceAll(`printf '%s\n' "$@" > "$REC/args"; exit 1`, "$REC", rec))
		_, _ = Launch(context.Background(), LaunchOptions{
			Chrome:     Chrome{Path: bin, Version: "155.0.0.0", Major: 155},
			ProfileDir: filepath.Join(t.TempDir(), "profile"),
			Environ:    os.Environ(),
			headless:   headless,
		})
		b, err := os.ReadFile(filepath.Join(rec, "args"))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(string(b), "--headless=new\n"); got != headless {
			t.Errorf("headless=%v: --headless=new present = %v", headless, got)
		}
	}
}
