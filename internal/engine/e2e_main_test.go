//go:build browser

package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"applysmith/internal/browser"
)

// The end-to-end suite drives the installed Google Chrome with a throwaway
// profile (make test-browser). One Chrome serves every test.

var shared struct {
	once sync.Once
	inst *browser.Instance
	fx   *fixtures
	root string
	err  error
}

func TestMain(m *testing.M) {
	code := m.Run()
	if shared.inst != nil {
		fmt.Println("shared instance stop:", shared.inst.Stop(context.Background()))
	}
	if shared.fx != nil {
		shared.fx.close()
	}
	if shared.root != "" {
		_ = os.RemoveAll(shared.root)
	}
	os.Exit(code)
}

func sharedInstance(t testing.TB) (*browser.Instance, *fixtures) {
	t.Helper()
	shared.once.Do(func() {
		chrome, err := browser.DiscoverChrome(context.Background(), os.Getenv)
		if err != nil {
			shared.err = fmt.Errorf("no usable Google Chrome: %w", err)
			return
		}
		// The directory name carries the marker that make test-browser sweeps for.
		root, err := os.MkdirTemp("", "applysmith-browser-test-")
		if err != nil {
			shared.err = err
			return
		}
		shared.root = root
		shared.fx = newFixtures()
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		shared.inst, shared.err = browser.Open(ctx, browser.Options{
			Chrome:       chrome,
			ProfileDir:   filepath.Join(root, "applysmith-browser-test-profile"),
			StateDir:     filepath.Join(root, "state"),
			DownloadsDir: filepath.Join(root, "downloads"),
			Environ:      os.Environ(),
		}.WithHostRules("MAP *.test 127.0.0.1, MAP *.ns2cloud.com 127.0.0.1, "+ATSHostRules).WithHeadless())
	})
	if shared.err != nil {
		t.Skipf("browser unavailable: %v", shared.err)
	}
	return shared.inst, shared.fx
}

// tabFixture is one fresh scan-lane tab with its hooks and a Page on it.
type tabFixture struct {
	t     *testing.T
	inst  *browser.Instance
	fx    *fixtures
	lane  *browser.LaneHandle
	tab   string
	hooks *browser.Hooks
	stage string // the staging directory of the pages made by page
}

func openFixture(t *testing.T) *tabFixture {
	t.Helper()
	inst, fx := sharedInstance(t)
	fx.reset()
	lane := inst.Lane(browser.LaneScan)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tab, err := lane.OpenTab(ctx, "engine-e2e")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, ti := range lane.Tabs() {
			if ti.Owner == "engine-e2e" {
				_ = lane.CloseTab(context.Background(), ti.ID)
			}
		}
	})
	hooks, err := lane.Hooks(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	return &tabFixture{t: t, inst: inst, fx: fx, lane: lane, tab: tab, hooks: hooks}
}

func (f *tabFixture) navigate(url string) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := f.lane.Navigate(ctx, f.tab, url); err != nil {
		f.t.Fatal(err)
	}
}

func (f *tabFixture) page(h Host) *Page {
	f.t.Helper()
	if h.StageDir == "" {
		h.StageDir = filepath.Join(f.t.TempDir(), "uploads")
	}
	f.stage = h.StageDir
	p, err := NewPage(f.hooks, h)
	if err != nil {
		f.t.Fatal(err)
	}
	return p
}
