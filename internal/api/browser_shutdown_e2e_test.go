//go:build browser

package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"applysmith/internal/browser"
)

// A headless Chrome opened through the registry, as every lane user does, is
// gone with its whole process group once the server that owns it shuts down.
func TestShutdown_ClosesTheChromeProcessGroup(t *testing.T) {
	chrome, err := browser.DiscoverChrome(context.Background(), os.Getenv)
	if err != nil {
		t.Skipf("no usable Google Chrome: %v", err)
	}
	dir, err := os.MkdirTemp("", "applysmith-shutdown-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	opts := browser.Options{
		Chrome:       chrome,
		ProfileDir:   filepath.Join(dir, "applysmith-browser-test-profile"),
		StateDir:     filepath.Join(dir, "state"),
		DownloadsDir: filepath.Join(dir, "downloads"),
		Environ:      os.Environ(),
	}.WithHeadless()
	h := newDomainHarness(t, func(c *Config) { c.BrowserControl = browser.NewControl(opts) })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	inst, err := browser.Open(ctx, opts)
	if err != nil {
		t.Fatalf("open the browser: %v", err)
	}
	pid := inst.PID()
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	if err := syscall.Kill(-pid, 0); err != nil {
		t.Fatalf("the browser's process group %d is not running: %v", pid, err)
	}

	if err := h.srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		err := syscall.Kill(-pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process group %d still exists after the server shut down: %v", pid, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
