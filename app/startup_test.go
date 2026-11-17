package main

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"applysmith/internal/api"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

// TestPackagedStartup measures how long a packaged bundle takes from launch to
// a served dashboard, and what the process uses. It launches real bundles, so it
// runs only on request:
//
//	APPLYSMITH_MEASURE_STARTUP=1 APPLYSMITH_APP_BUNDLES=a.app:b.app go test -run PackagedStartup -v ./app/
//
// Each bundle is launched once against one temporary workspace, in the order
// given: the first start is cold (empty workspace, first migration), the rest
// find an initialised one. The port is chosen by the system, the workspace and
// the app's own HOME are temporary, and every process is stopped by its PID.
// With APPLYSMITH_MEASURE_KEYCHAIN=1 the API token goes through the real keychain
// instead of the environment, under a workspace id nothing else uses, and that
// one entry is deleted afterwards. The keychain is the user's login keychain,
// so the app keeps the real HOME: the app's extension folder under
// ~/Library/Application Support/Applysmith is copied aside first and put back
// afterwards, and the app's startup lines are appended to the real
// ~/Library/Logs/Applysmith, which nothing here can redirect.
func TestPackagedStartup(t *testing.T) {
	if os.Getenv("APPLYSMITH_MEASURE_STARTUP") == "" {
		t.Skip("set APPLYSMITH_MEASURE_STARTUP=1 and APPLYSMITH_APP_BUNDLES to launch bundles")
	}
	bundles := strings.Split(os.Getenv("APPLYSMITH_APP_BUNDLES"), ":")
	keychain := os.Getenv("APPLYSMITH_MEASURE_KEYCHAIN") == "1"
	root := t.TempDir()
	ws := workspace.Open(filepath.Join(root, "workspace"))
	if err := os.MkdirAll(ws.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	if keychain {
		keepExtensionFolder(t)
		t.Cleanup(func() {
			if id, ok, _ := ws.ExistingID(); ok {
				k := vault.NewKeychain()
				_ = k.Delete(id + ":" + vault.APIToken)
				if _, err := k.Get(id + ":" + vault.APIToken); err == nil {
					t.Errorf("the measurement's keychain entry for workspace %s is still there", id)
				}
			}
		})
	}
	for i, bundle := range bundles {
		env := append(os.Environ(), "APPLYSMITH_WORKSPACE="+ws.Root, "APPLYSMITH_PORT=0")
		if !keychain {
			env = append(env, "APPLYSMITH_SECRET_API_TOKEN=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "HOME="+filepath.Join(root, "home"))
		}
		cmd := exec.Command(filepath.Join(bundle, "Contents", "MacOS", "Applysmith"))
		cmd.Env = env
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		windowsBefore := securityWindows()
		load := loadAverage()
		begin := time.Now()
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
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
		served, rss, err := waitServed(ws, pid, exited, 60*time.Second)
		if err != nil {
			stop()
			t.Fatalf("%s: %v", bundle, err)
		}
		kind := "warm"
		if i == 0 {
			kind = "cold"
		}
		if keychain {
			// The token must really live in the keychain, or the run proves nothing.
			id, _, _ := ws.ExistingID()
			if _, err := vault.NewKeychain().Get(id + ":" + vault.APIToken); err != nil {
				t.Errorf("start %d: the API token is not in the keychain: %v", i+1, err)
			}
		}
		t.Logf("start %d (%s): dashboard served after %d ms, resident memory %d MB, keychain=%v, authorisation windows %d -> %d, load average %s",
			i+1, kind, served.Sub(begin).Milliseconds(), rss/1024, keychain, windowsBefore, securityWindows(), load)
		stop()
		if syscall.Kill(pid, 0) == nil {
			t.Errorf("process %d is still running", pid)
		}
	}
}

// keepExtensionFolder copies the app's extension folder under the real user
// data directory aside and puts it back when the test ends, so a measured
// build never leaves its extension where the user's Chrome loads it.
func keepExtensionFolder(t *testing.T) {
	t.Helper()
	config, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(config, "Applysmith", "extension")
	// Not t.TempDir: a copy that could not be put back must survive the test.
	tmp, err := os.MkdirTemp("", "applysmith-extension-")
	if err != nil {
		t.Fatal(err)
	}
	saved := filepath.Join(tmp, "extension")
	_, statErr := os.Stat(dir)
	existed := statErr == nil
	if existed {
		if err := os.CopyFS(saved, os.DirFS(dir)); err != nil {
			t.Fatalf("copy the extension folder aside: %v", err)
		}
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove the measured extension folder: %v", err)
			return
		}
		if existed {
			if err := os.CopyFS(dir, os.DirFS(saved)); err != nil {
				t.Errorf("put the extension folder back from %s: %v", saved, err)
				return
			}
		}
		_ = os.RemoveAll(tmp)
	})
}

// waitServed polls until the process named by pid serves a healthy dashboard.
func waitServed(ws *workspace.Workspace, pid int, exited <-chan struct{}, limit time.Duration) (time.Time, int, error) {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return time.Time{}, 0, io.ErrUnexpectedEOF
		default:
		}
		if info, err := api.ReadLockInfo(ws.StateDir()); err == nil && info.PID == pid {
			if u, err := url.Parse(info.Origin); err == nil {
				if h, ok := api.ProbeServer(u.Host); ok && h.Status == "ok" && dashboardServed(info) {
					return time.Now(), residentKB(pid), nil
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return time.Time{}, 0, os.ErrDeadlineExceeded
}

// dashboardServed redeems the lock document's launch code, as the window does,
// and checks that the dashboard page comes back.
func dashboardServed(info api.LockInfo) bool {
	jar, _ := cookiejar.New(nil)
	resp, err := (&http.Client{Jar: jar, Timeout: 5 * time.Second}).Get(info.Origin + "/?launch=" + url.QueryEscape(info.LaunchCode))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode == http.StatusOK && strings.Contains(string(body), `id="root"`)
}

func residentKB(pid int) int {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	n := 0
	for _, c := range strings.TrimSpace(string(out)) {
		n = n*10 + int(c-'0')
	}
	return n
}

// securityWindows counts the on-screen windows of the system's authorisation
// agent, which draws every keychain prompt (and the lock screen, so only a
// change across a launch means anything).
func securityWindows() int {
	script := `ObjC.import('CoreGraphics'); ObjC.deepUnwrap(ObjC.castRefToObject($.CGWindowListCopyWindowInfo($.kCGWindowListOptionOnScreenOnly,0))).filter(function(w){return w.kCGWindowOwnerName=='SecurityAgent'}).length`
	out, err := exec.Command("osascript", "-l", "JavaScript", "-e", script).Output()
	if err != nil {
		return -1
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

func loadAverage() string {
	out, _ := exec.Command("sysctl", "-n", "vm.loadavg").Output()
	return strings.Trim(strings.TrimSpace(string(out)), "{ }")
}
