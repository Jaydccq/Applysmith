package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"applysmith/internal/cli"
)

func TestCLIInvokedAs(t *testing.T) {
	for _, c := range []struct {
		argv0 string
		want  bool
	}{
		{"applysmith", true},
		{"/Users/alex/.local/bin/applysmith", true},
		{"/Applications/Applysmith.app/Contents/MacOS/Applysmith", false},
		{"applysmith-helper", false},
	} {
		if got := cli.InvokedAs(c.argv0); got != c.want {
			t.Errorf("InvokedAs(%q) = %v, want %v", c.argv0, got, c.want)
		}
	}
}

// runAsCLI starts the app binary through a symlink called applysmith, in a folder
// of its own, with a temporary home and workspace, and returns what it printed.
// The process is the command line: it prints and exits without a window.
func runAsCLI(t *testing.T, binary string, args ...string) (string, error) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(bin, "applysmith")
	if err := os.Symlink(binary, link); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, link, args...)
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "APPLYSMITH_WORKSPACE="+t.TempDir())
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// packagedBundleRunsAsCLI is the scenario "The app binary is the command
// line": started as applysmith, the binary inside the .app runs the CLI's
// commands, prints its own bundle version and exits. make package runs it;
// the plain suite does not build the app.
func packagedBundleRunsAsCLI(t *testing.T, bundle string) {
	if runtime.GOOS != "darwin" {
		t.Skip("the bundle layout is macOS only")
	}
	binary := filepath.Join(bundle, "Contents", "MacOS", "Applysmith")
	out, err := runAsCLI(t, binary, "version")
	if err != nil || !strings.HasPrefix(out, "applysmith ") {
		t.Fatalf("applysmith version: err %v, output %q", err, out)
	}
	if strings.HasPrefix(out, "applysmith dev ") {
		t.Errorf("the packaged app should report its bundle version, got %q", out)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--help"}, "usage: applysmith <command> [flags]"},
		{[]string{"routes"}, "/api/health"},
	} {
		out, err := runAsCLI(t, binary, c.args...)
		if err != nil || !strings.Contains(out, c.want) {
			t.Errorf("applysmith %v: err %v, output %q, want it to contain %q", c.args, err, out, c.want)
		}
	}
	if out, err := runAsCLI(t, binary, "no-such-command"); err == nil || !strings.Contains(out, "unknown command") {
		t.Errorf("an unknown command must fail like the CLI: err %v, output %q", err, out)
	}
}
