//go:build browser

package browser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The e2e suite launches the installed Google Chrome with a throwaway profile.
// It runs only with the browser build tag (make test-browser).

var shared struct {
	once sync.Once
	inst *Instance
	fx   *fixtures
	root string
	err  error
}

// headed is the visible-window twin of shared, started only by a test that
// asks for it under make test-browser-headed.
var headed struct {
	once sync.Once
	inst *Instance
	fx   *fixtures
	root string
	err  error
}

func TestMain(m *testing.M) {
	code := m.Run()
	if shared.inst != nil {
		start := time.Now()
		out := shared.inst.Stop(context.Background())
		fmt.Printf("shared instance stop: %s in %v\n", out, time.Since(start))
	}
	if headed.inst != nil {
		headed.inst.Stop(context.Background())
	}
	if headed.fx != nil {
		headed.fx.close()
	}
	if headed.root != "" {
		_ = os.RemoveAll(headed.root)
	}
	if shared.fx != nil {
		shared.fx.close()
	}
	if shared.root != "" {
		_ = os.RemoveAll(shared.root)
	}
	os.Exit(code)
}

func testChrome(t testing.TB) Chrome {
	t.Helper()
	c, err := DiscoverChrome(context.Background(), os.Getenv)
	if err != nil {
		t.Skipf("no usable Google Chrome: %v", err)
	}
	return c
}

// testOptions returns options with every path under dir, named so a leftover
// process is easy to find.
func testOptions(t testing.TB, dir string) Options {
	return Options{
		Chrome:       testChrome(t),
		ProfileDir:   filepath.Join(dir, "applysmith-browser-test-profile"),
		StateDir:     filepath.Join(dir, "state"),
		DownloadsDir: filepath.Join(dir, "downloads"),
		Environ:      os.Environ(),
		hostRules:    "MAP *.test 127.0.0.1, MAP *.googletagmanager.com 127.0.0.1",
	}.WithHeadless()
}

// headedOptions is testOptions with a visible window, for a test whose subject
// is the window itself (focus, minimizing, the identity a headed Chrome
// presents). Those run only under make test-browser-headed, which sets the
// variable; make test-browser opens no window.
func headedOptions(t testing.TB, dir string) Options {
	t.Helper()
	if os.Getenv("APPLYSMITH_TEST_BROWSER_HEADED") != "1" {
		t.Skip("needs a visible window: run make test-browser-headed")
	}
	o := testOptions(t, dir)
	o.headless = false
	return o
}

// sharedInstance returns one Chrome instance reused by the lane tests.
func sharedInstance(t testing.TB) (*Instance, *fixtures) {
	t.Helper()
	// A skip inside the Once would mark it done with nothing opened, and the
	// next test would get a nil instance; a missing Chrome skips here instead.
	testChrome(t)
	shared.once.Do(func() {
		root, err := os.MkdirTemp("", "applysmith-browser-test-")
		if err != nil {
			shared.err = err
			return
		}
		shared.root = root
		shared.fx = newFixtures()
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		shared.inst, shared.err = Open(ctx, testOptions(t, root))
	})
	if shared.err != nil {
		t.Fatalf("shared instance: %v", shared.err)
	}
	return shared.inst, shared.fx
}

// headedInstance returns one visible-window Chrome for the tests whose subject
// is the window: focus, minimizing, and the identity a headed Chrome presents.
func headedInstance(t testing.TB) (*Instance, *fixtures) {
	t.Helper()
	if os.Getenv("APPLYSMITH_TEST_BROWSER_HEADED") != "1" {
		t.Skip("needs a visible window: run make test-browser-headed")
	}
	testChrome(t)
	headed.once.Do(func() {
		root, err := os.MkdirTemp("", "applysmith-browser-test-")
		if err != nil {
			headed.err = err
			return
		}
		headed.root = root
		headed.fx = newFixtures()
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		headed.inst, headed.err = Open(ctx, headedOptions(t, root))
	})
	if headed.err != nil {
		t.Fatalf("headed instance: %v", headed.err)
	}
	return headed.inst, headed.fx
}

func sharedDownloads() string { return filepath.Join(shared.root, "downloads") }

func ctx30(t testing.TB) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func ok(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func eventually(t testing.TB, d time.Duration, what string, cond func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", d, what)
}

func hasEvent(i *Instance, kind, detail string) bool {
	return hasEventSince(i, kind, detail, time.Time{})
}

// hasEventSince is hasEvent over the events recorded after since: the shared
// instance's log also holds what earlier tests left.
func hasEventSince(i *Instance, kind, detail string, since time.Time) bool {
	for _, e := range i.Events() {
		if e.Kind == kind && (detail == "" || e.Detail == detail) && e.Time.After(since) {
			return true
		}
	}
	return false
}

func eval[T any](t testing.TB, h *LaneHandle, tab, expr string) T {
	t.Helper()
	raw, err := h.Evaluate(ctx30(t), tab, expr)
	if err != nil {
		t.Fatalf("evaluate %q: %v", expr, err)
	}
	var v T
	if err := jsonUnmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}
