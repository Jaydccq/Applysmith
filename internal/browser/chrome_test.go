package browser

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// shim writes an executable that stands in for Chrome.
func shim(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell shims need a POSIX shell")
	}
	p := filepath.Join(t.TempDir(), "shim")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func envOf(k, v string) func(string) string {
	return func(name string) string {
		if name == k {
			return v
		}
		return ""
	}
}

func TestBrowser_DiscoverChrome_MissingListsPaths(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	_, err := DiscoverChrome(context.Background(), envOf(chromeBinEnv, missing))
	if code, _ := CodeOf(err); code != CodeChromeMissing {
		t.Fatalf("code %q, want chrome-missing (err %v)", code, err)
	}
	if !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "install Google Chrome") {
		t.Fatalf("message lacks searched path or fix: %v", err)
	}
	// An explicit path is exclusive: the standard locations are not searched.
	if strings.Contains(err.Error(), "/Applications") {
		t.Fatalf("standard locations were searched despite an explicit path: %v", err)
	}
}

func TestBrowser_DiscoverChrome_TooOld(t *testing.T) {
	bin := shim(t, `echo "Google Chrome 119.0.6045.105"`)
	_, err := DiscoverChrome(context.Background(), envOf(chromeBinEnv, bin))
	if code, _ := CodeOf(err); code != CodeChromeTooOld {
		t.Fatalf("code %q, want chrome-too-old (err %v)", code, err)
	}
	if !strings.Contains(err.Error(), "119") || !strings.Contains(err.Error(), "120") {
		t.Fatalf("message must name both versions: %v", err)
	}
}

func TestBrowser_DiscoverChrome_RejectsNonGoogleChrome(t *testing.T) {
	for _, line := range []string{"Chromium 130.0.6723.58", "Google Chrome for Testing 130.0.6723.58"} {
		bin := shim(t, `echo "`+line+`"`)
		_, err := DiscoverChrome(context.Background(), envOf(chromeBinEnv, bin))
		if code, _ := CodeOf(err); code != CodeChromeUnsupported {
			t.Errorf("%q: code %q, want chrome-unsupported", line, code)
		}
	}
}

func TestBrowser_DiscoverChrome_AcceptsAndParsesVersion(t *testing.T) {
	bin := shim(t, `echo "Google Chrome 155.0.8059.40 "`)
	c, err := DiscoverChrome(context.Background(), envOf(chromeBinEnv, bin))
	if err != nil {
		t.Fatal(err)
	}
	if c.Major != 155 || c.Version != "155.0.8059.40" || c.Path != bin {
		t.Fatalf("got %+v", c)
	}
}

func TestBrowser_DiscoverChrome_RunsTheVersionOnceUntilTheExecutableChanges(t *testing.T) {
	count := filepath.Join(t.TempDir(), "runs")
	bin := shim(t, `echo x >> "`+count+`"; echo "Google Chrome 155.0.8059.40"`)
	runs := func() int {
		b, _ := os.ReadFile(count)
		return strings.Count(string(b), "x")
	}
	for range 3 {
		if _, err := DiscoverChrome(context.Background(), envOf(chromeBinEnv, bin)); err != nil {
			t.Fatal(err)
		}
	}
	if runs() != 1 {
		t.Fatalf("--version ran %d times for an unchanged executable", runs())
	}
	// An update replaces the executable: it is asked again.
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho x >> \""+count+"\"; echo \"Google Chrome 156.0.1.2\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := DiscoverChrome(context.Background(), envOf(chromeBinEnv, bin))
	if err != nil || c.Major != 156 || runs() != 2 {
		t.Fatalf("after the update: %+v %v, %d runs", c, err, runs())
	}
}

// A probe that could not run (a timeout on a loaded machine, a failed exec)
// says nothing about the executable: it is asked again, where a kept failure
// made Chrome unsupported for the life of the process.
func TestBrowser_DiscoverChrome_AProbeThatCouldNotRunIsNotRemembered(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	bin := shim(t, `if [ -f "`+marker+`" ]; then echo "Google Chrome 155.0.8059.40"; else touch "`+marker+`"; exit 1; fi`)
	if _, err := DiscoverChrome(context.Background(), envOf(chromeBinEnv, bin)); err == nil {
		t.Fatal("a probe that exited with an error was accepted")
	}
	c, err := DiscoverChrome(context.Background(), envOf(chromeBinEnv, bin))
	if err != nil || c.Major != 155 {
		t.Fatalf("the second probe: %+v %v", c, err)
	}
}

// The state the status route reads does not wait for the discovery of Chrome
// that the first start runs. The probed executable is not Chrome, so the start
// fails and nothing is launched.
func TestBrowser_Control_StateIsReadWhileChromeIsBeingDiscovered(t *testing.T) {
	dir := t.TempDir()
	started, release := filepath.Join(dir, "started"), filepath.Join(dir, "release")
	t.Setenv(chromeBinEnv, shim(t, `touch "`+started+`"; while [ ! -f "`+release+`" ]; do sleep 0.02; done; echo "not a browser 1.0"`))
	c := NewControl(Options{ProfileDir: filepath.Join(dir, "profile"), StateDir: filepath.Join(dir, "state")})
	opened := make(chan error, 1)
	go func() { _, err := c.open(context.Background()); opened <- err }()
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(end) {
			t.Fatal("the version probe never ran")
		}
	}
	state := make(chan InstanceState, 1)
	go func() { s, _ := c.State(); state <- s }()
	var got InstanceState
	select {
	case got = <-state:
	case <-time.After(2 * time.Second):
		got = "blocked"
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-opened; err == nil {
		t.Fatal("an executable that is not Chrome was started")
	}
	if got != InstanceStarting {
		t.Fatalf("state during discovery: %s, want %s", got, InstanceStarting)
	}
}
