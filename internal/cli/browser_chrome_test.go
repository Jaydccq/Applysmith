//go:build browser

package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"applysmith/internal/browser"
)

// Every command that starts the managed browser on its own (the sign-in
// check, the setup browser step, the live doctor, a liveness read) stops the
// browser it launched when it ends, so the next command does not fail as
// profile-in-use. run releases it for the whole command line.
//
// It launches the installed Chrome headless, so it runs only under the
// browser tag (make test-browser). The shim records the PID of every Chrome
// it starts; the test checks and stops only those PIDs, never a process
// found by name.
func TestCommands_StopTheBrowserTheyLaunched(t *testing.T) {
	real, err := browser.DiscoverChrome(context.Background(), os.Getenv)
	if err != nil {
		t.Skipf("no usable Google Chrome: %v", err)
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"browser check", []string{"browser", "check"}},
		{"setup apply browser", []string{"setup", "apply", "browser"}},
		{"doctor --live", []string{"doctor", "--live", "--only", "browser"}},
		{"liveness", []string{"liveness", "https://jobs.example.invalid/role"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Headless, and no host resolves, so no command reaches a site. exec
			// keeps the shell's PID, so the recorded PID is Chrome's.
			dir := t.TempDir()
			pids := filepath.Join(dir, "pids")
			shim := filepath.Join(dir, "chrome")
			script := "#!/bin/sh\necho $$ >> '" + pids + "'\nexec '" + real.Path + "' --headless=new --host-resolver-rules='MAP * ~NOTFOUND, EXCLUDE 127.0.0.1' \"$@\"\n"
			if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("APPLYSMITH_CHROME_BIN", shim)
			te := newTestEnv(t)
			if o := te.run(context.Background(), "init", "--workspace", te.ws); o.code != 0 {
				t.Fatalf("init: %d %s", o.code, o.stderr)
			}
			profile := filepath.Join(te.ws, ".applysmith", "browser", "profile")
			running := func() []int {
				raw, _ := os.ReadFile(pids)
				var alive []int
				for _, f := range strings.Fields(string(raw)) {
					pid, err := strconv.Atoi(f)
					if err != nil || pid <= 1 {
						continue
					}
					// A PID the system has reused for another program is not ours.
					cmdline, _ := exec.Command("ps", "-p", f, "-o", "command=").Output()
					if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) || !strings.Contains(string(cmdline), profile) {
						continue
					}
					alive = append(alive, pid)
				}
				return alive
			}
			t.Cleanup(func() {
				for _, pid := range running() {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			})
			te.run(context.Background(), append(tc.args, "--workspace", te.ws)...)
			// The launcher reaps Chrome on its own goroutine; give it a moment.
			left := running()
			for deadline := time.Now().Add(5 * time.Second); len(left) > 0 && time.Now().Before(deadline); left = running() {
				time.Sleep(50 * time.Millisecond)
			}
			if len(left) > 0 {
				t.Fatalf("%v left the browser it launched running: PIDs %v", tc.args, left)
			}
		})
	}
}
