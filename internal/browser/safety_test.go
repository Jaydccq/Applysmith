package browser

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBrowser_DiscoverChrome_VersionProbeGetsAllowlistedEnv(t *testing.T) {
	rec := t.TempDir()
	bin := shim(t, strings.ReplaceAll(`env > "$REC/env"; echo "Google Chrome 155.0.0.1"`, "$REC", rec))
	t.Setenv("APPLYSMITH_SECRET_PROBE", "hunter2")
	t.Setenv("PROBE_SERVICE_TOKEN", "t0")
	t.Setenv("PROBE_UNRELATED", "u")
	if _, err := DiscoverChrome(context.Background(), envOf(chromeBinEnv, bin)); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, filepath.Join(rec, "env"))
	for _, bad := range []string{"APPLYSMITH_SECRET_PROBE", "hunter2", "PROBE_SERVICE_TOKEN", "PROBE_UNRELATED"} {
		if strings.Contains(got, bad) {
			t.Errorf("the version probe saw %q", bad)
		}
	}
}

func TestBrowser_Launch_EmptyAllowlistDoesNotInheritTheParentEnvironment(t *testing.T) {
	t.Setenv("APPLYSMITH_SECRET_INHERIT", "hunter2")
	t.Setenv("INHERIT_API_KEY", "k")
	rec, _ := shimLaunch(t, `env > "$REC/env"; exit 1`, []string{"UNRELATED=value"})
	got := readFile(t, filepath.Join(rec, "env"))
	for _, bad := range []string{"APPLYSMITH_SECRET_INHERIT", "hunter2", "INHERIT_API_KEY", "UNRELATED"} {
		if strings.Contains(got, bad) {
			t.Errorf("the browser environment holds %q", bad)
		}
	}
}

func TestBrowser_Launch_ErrorRedactsTheDevToolsAddress(t *testing.T) {
	_, err := shimLaunch(t, `echo "DevTools listening on ws://127.0.0.1:9333/devtools/browser/0a1b2c3d-secret" >&2; exit 1`, os.Environ())
	if err == nil {
		t.Fatal("launch succeeded")
	}
	for _, bad := range []string{"devtools/browser", "0a1b2c3d-secret", "127.0.0.1:9333"} {
		if strings.Contains(err.Error(), bad) {
			t.Errorf("error text discloses %q: %v", bad, err)
		}
	}
	if !strings.Contains(err.Error(), "DevTools listening on") {
		t.Errorf("the rest of the output was lost: %v", err)
	}
}

func TestBrowser_OwnerCheck_MatchesWholeArguments(t *testing.T) {
	prof := "/p/profile"
	cases := []struct {
		cmd  string
		port int
		want bool
	}{
		{"chrome --user-data-dir=/p/profile --remote-debugging-port=9222 --no-first-run", 9222, true},
		{"chrome --user-data-dir=/p/profile --remote-debugging-port=92220 --no-first-run", 9222, false},
		{"chrome --user-data-dir=/p/profile2 --remote-debugging-port=9222 --x", 9222, false},
		{"chrome --user-data-dir=/q/p/profile --remote-debugging-port=9222 --x", 9222, false},
		{"chrome --user-data-dir=/p/profile --remote-debugging-port=922", 9222, false},
		{"chrome --remote-debugging-port=9222 --user-data-dir=/p/profile", 9222, false},
	}
	for _, c := range cases {
		if got := commandRunsProfile(c.cmd, prof, c.port); got != c.want {
			t.Errorf("commandRunsProfile(%q, port %d) = %v, want %v", c.cmd, c.port, got, c.want)
		}
	}
	spaced := filepath.Join("example-root", "Application Support", "profile")
	if !commandRunsProfile("chrome --user-data-dir="+spaced+" --remote-debugging-port=7 --y", spaced, 7) {
		t.Error("a profile path with spaces is not matched")
	}
}

func TestBrowser_OwnerCheck_ListenerMustBeLoopback(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:9222": true, "[::1]:9222": true,
		"*:9222": false, "0.0.0.0:9222": false, "192.168.1.5:9222": false, "garbage": false,
	} {
		if got := loopbackAddr(addr); got != want {
			t.Errorf("loopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestBrowser_Launch_RefusesEverydayProfileRegardlessOfCase(t *testing.T) {
	if !foldCase() {
		t.Skip("file names are case-sensitive on this platform")
	}
	home, everyday := fakeHome(t)
	if err := os.MkdirAll(everyday, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{
		"lower":     filepath.Join(strings.ToLower(everyday), "x"),
		"upper":     filepath.Join(strings.ToUpper(everyday), "x"),
		"lower-eq":  strings.ToLower(everyday),
		"container": strings.ToLower(filepath.Dir(everyday)),
	} {
		if _, err := prepareProfile(dir, home, func(string) string { return "" }); err == nil {
			t.Errorf("%s: %s accepted", name, dir)
		}
	}
}

func TestBrowser_Adopt_RefusesEverydayAndForeignProfiles(t *testing.T) {
	home, everyday := fakeHome(t)
	t.Setenv("HOME", home)
	configured := filepath.Join(t.TempDir(), "profile")
	for name, rec := range map[string]Record{
		"everyday": {PID: os.Getpid(), Profile: everyday, Port: 9222},
		"foreign":  {PID: os.Getpid(), Profile: filepath.Join(t.TempDir(), "other"), Port: 9222},
		"inside":   {PID: os.Getpid(), Profile: filepath.Join(everyday, "Profile 1"), Port: 9222},
	} {
		for _, configuredProfile := range []string{configured, rec.Profile} {
			if name == "foreign" && configuredProfile == rec.Profile {
				continue // a safe profile that matches the configuration goes on to the owner checks
			}
			_, _, err := adopt(context.Background(), rec, 155, configuredProfile)
			if code, _ := CodeOf(err); code != CodeProfileUnsafe {
				t.Errorf("%s (configured %s): code %q err %v, want profile-unsafe", name, configuredProfile, code, err)
			}
		}
	}
}

func TestBrowser_StaleRecord_IgnoredAndRemoved(t *testing.T) {
	state := t.TempDir()
	profile := filepath.Join(t.TempDir(), "profile")
	rec := filepath.Join(state, "instance.json")
	canon, err := canonical(profile)
	if err != nil {
		t.Fatal(err)
	}
	for name, r := range map[string]Record{
		"dead pid":        {PID: deadPID(t), Start: "x", Profile: canon, Port: 9333},
		"foreign profile": {PID: os.Getpid(), Start: "x", Profile: filepath.Join(t.TempDir(), "other"), Port: 9333},
	} {
		if err := WriteRecord(rec, r); err != nil {
			t.Fatal(err)
		}
		// The shim makes the fresh launch fail at once; the record must be gone.
		_, err := startOrAdopt(context.Background(), Options{
			Chrome: Chrome{Path: shim(t, `exit 1`), Major: 155}, ProfileDir: profile, StateDir: state, Environ: os.Environ(),
		}, nil)
		if code, _ := CodeOf(err); code != CodeLaunchFailed {
			t.Fatalf("%s: code %q err %v, want a fresh launch attempt", name, code, err)
		}
		if _, err := os.Stat(rec); err == nil {
			t.Errorf("%s: the stale record was kept", name)
		}
	}
}

func sleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	detach(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

func TestBrowser_Terminate_NeverSignalsAfterTheChildWasReaped(t *testing.T) {
	bystander := sleeper(t)
	done := make(chan struct{})
	close(done)
	p := &Process{PID: bystander.Process.Pid, done: done}
	p.terminate()
	p.stop(context.Background(), nil)
	if !processAlive(bystander.Process.Pid) {
		t.Fatal("a process that reused the pid was killed")
	}
}

func TestBrowser_ForceKill_RefusesARecycledPid(t *testing.T) {
	bystander := sleeper(t)
	p := &Process{PID: bystander.Process.Pid, Start: "Thu Jan  1 00:00:00 1970", done: make(chan struct{})}
	p.forceKill()
	time.Sleep(200 * time.Millisecond)
	if !processAlive(bystander.Process.Pid) {
		t.Fatal("a process with another start time was killed")
	}
}

func TestBrowser_Stop_CancelledContextStillAsksAndDoesNotWaitTenSeconds(t *testing.T) {
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
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	asked := false
	start := time.Now()
	out := p.stop(ctx, func() { asked = true; askToQuit(p.PID) })
	if !asked || out != StopKilled || time.Since(start) > 4*time.Second {
		t.Fatalf("asked=%v outcome=%q after %v", asked, out, time.Since(start))
	}
}
