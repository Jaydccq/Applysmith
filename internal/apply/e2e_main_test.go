//go:build browser

package apply

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"applysmith/internal/browser"
)

// One Chrome with a throwaway profile serves every test; the mock site maps
// the real host names of the named systems to loopback.

var shared struct {
	once sync.Once
	inst *browser.Instance
	site *site
	root string
	err  error

	watchOnce  sync.Once
	anchorOnce sync.Once
	anchor     string
}

func TestMain(m *testing.M) {
	if os.Getenv("AJ_APPLY_CHILD") != "" {
		os.Exit(childMain())
	}
	code := m.Run()
	if shared.inst != nil {
		shared.inst.Stop(context.Background())
	}
	if shared.root != "" && chromeAlive(shared.root) {
		fmt.Fprintln(os.Stderr, "a Chrome of the shared test profile is still running after Stop")
		code = 1
	}
	if shared.site != nil {
		shared.site.close()
	}
	if shared.root != "" {
		_ = os.RemoveAll(shared.root)
	}
	os.Exit(code)
}

func sharedBrowser(t testing.TB) (*browser.Instance, *site) {
	t.Helper()
	shared.once.Do(func() {
		chrome, err := browser.DiscoverChrome(context.Background(), os.Getenv)
		if err != nil {
			shared.err = fmt.Errorf("no usable Google Chrome: %w", err)
			return
		}
		root, err := os.MkdirTemp("", "applysmith-apply-e2e-")
		if err != nil {
			shared.err = err
			return
		}
		shared.root = root
		shared.site = newSite()
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		shared.inst, shared.err = browser.Open(ctx, browser.Options{
			Chrome:       chrome,
			ProfileDir:   filepath.Join(root, "applysmith-apply-e2e-profile"),
			StateDir:     filepath.Join(root, "state"),
			DownloadsDir: filepath.Join(root, "downloads"),
			Environ:      os.Environ(),
		}.WithHostRules(mockHostRules).WithHeadless())
	})
	if shared.err != nil {
		t.Skipf("browser unavailable: %v", shared.err)
	}
	shared.watchOnce.Do(func() { watchdog(shared.inst.PID()) })
	shared.anchorOnce.Do(func() {
		// A tab that stays for the whole run keeps the apply window alive, so a
		// test that closes its own tabs never closes the window under the next.
		lane := shared.inst.Lane(browser.LaneApply)
		id, err := lane.OpenTab(context.Background(), "anchor")
		if err == nil {
			_ = lane.Keep(id)
			shared.anchor = id
		}
	})
	return shared.inst, shared.site
}

// watchdog kills the browser's process group once this process is gone, however
// it went: a test binary that is killed or times out never reaches TestMain's
// cleanup, and a headless Chrome must not outlive it.
func watchdog(chromePID int) {
	if chromePID <= 1 {
		return
	}
	script := fmt.Sprintf("while kill -0 %d 2>/dev/null; do sleep 1; done; kill -9 -%d 2>/dev/null; kill -9 %d 2>/dev/null", os.Getpid(), chromePID, chromePID)
	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	_ = cmd.Start()
}

// chromeProcs lists the processes whose command line carries a profile under
// root. The browser layer resolves symlinks before it passes the profile, and
// the system temporary directory is one (/var is /private/var), so both
// spellings are matched.
func chromeProcs(root string) (procs []chromeProc) {
	needles := []string{"user-data-dir=" + root}
	if real, err := filepath.EvalSymlinks(root); err == nil && real != root {
		needles = append(needles, "user-data-dir="+real)
	}
	out, err := exec.Command("ps", "-Ao", "pid=,pgid=,command=").Output()
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		for _, n := range needles {
			if strings.Contains(line, n) {
				pid, _ := strconv.Atoi(f[0])
				pgid, _ := strconv.Atoi(f[1])
				procs = append(procs, chromeProc{pid: pid, pgid: pgid})
				break
			}
		}
	}
	return procs
}

type chromeProc struct{ pid, pgid int }

// stopChrome kills every process of the profile with its whole process group
// and waits, up to within, until none is left: a SIGKILLed browser keeps
// running until the kernel has torn it down, which under load can take
// seconds. It reports whether nothing remains.
func stopChrome(root string, within time.Duration) bool {
	groups := map[int]bool{}
	deadline := time.Now().Add(within)
	for {
		procs := chromeProcs(root)
		for _, p := range procs {
			if p.pgid > 1 {
				groups[p.pgid] = true
				_ = syscall.Kill(-p.pgid, syscall.SIGKILL)
			}
			if p.pid > 1 {
				_ = syscall.Kill(p.pid, syscall.SIGKILL)
			}
		}
		gone := len(procs) == 0
		for g := range groups {
			if syscall.Kill(-g, 0) != syscall.ESRCH {
				gone = false
			}
		}
		if gone {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// chromeAlive waits, up to a bound that covers a loaded machine, for the
// processes of the profile to finish exiting after a clean Stop. If any is
// still there it is killed (a test run never leaves a browser behind) and the
// call reports true so the run fails.
func chromeAlive(root string) bool {
	deadline := time.Now().Add(30 * time.Second)
	for len(chromeProcs(root)) > 0 {
		if time.Now().After(deadline) {
			left, _ := exec.Command("sh", "-c", "ps -Ao pid,ppid,pgid,stat,etime,command | grep -F "+root+" | grep -v grep | cut -c1-200").Output()
			fmt.Fprintf(os.Stderr, "left after Stop:\n%s", left)
			stopChrome(root, 30*time.Second)
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}
