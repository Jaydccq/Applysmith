//go:build browser

package extension

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/extensions"
	"github.com/chromedp/cdproto/serviceworker"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// harness is one Chrome for Testing with a throwaway profile, the built
// extension loaded unpacked, the stub API and the fixture site. Every browser
// test builds its own, so no test depends on another's browser state.
type harness struct {
	t          *testing.T
	browserCtx context.Context
	api        *stubAPI
	site       *fixtureSite
	stamp      string
	extID      string

	swID     target.ID
	swCtx    context.Context
	cancelSW context.CancelFunc
}

// registryHost resolves to the loopback fixture site and is a host the engine's
// site table knows, which is what makes the panel open by itself.
const registryHost = "job-boards.greenhouse.io"

func newHarness(t *testing.T, budget time.Duration) *harness {
	return newHarnessInstall(t, budget, false)
}

// CDP installation supports a real runtime.reload without Chrome discarding the
// command-line-loaded extension. Only the reload test needs the debugging flag.
func newHarnessInstall(t *testing.T, budget time.Duration, reload bool) *harness {
	t.Helper()
	if _, err := Files(); err != nil {
		t.Fatalf("the extension is not built: %v", err)
	}
	chrome := chromium(t)
	stamp, err := Stamp()
	if err != nil {
		t.Fatal(err)
	}
	extDir := filepath.Join(t.TempDir(), "ext")
	if err := Export(extDir); err != nil {
		t.Fatal(err)
	}
	profile, err := os.MkdirTemp("", "applysmith-browser-test-ext-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(profile) })
	h := &harness{t: t, stamp: stamp, api: newStub(t, stamp), site: newFixtureSite(t)}

	opts := []chromedp.ExecAllocatorOption{
		chromedp.ExecPath(chrome), chromedp.UserDataDir(profile),
		chromedp.NoFirstRun, chromedp.NoDefaultBrowserCheck,
		chromedp.Flag("headless", "new"), chromedp.Flag("disable-gpu", true), chromedp.Flag("mute-audio", true),
		// MacAppCodeSignClone: no app-bundle copy that a killed Chrome would leave behind (docs/architecture/browser.md).
		chromedp.Flag("disable-features", "DisableLoadExtensionCommandLineSwitch,MacAppCodeSignClone"),
		chromedp.Flag("host-resolver-rules", "MAP *.greenhouse.io 127.0.0.1"),
	}
	if reload {
		opts = append(opts, chromedp.Flag("enable-unsafe-extension-debugging", true))
	} else {
		opts = append(opts, chromedp.Flag("load-extension", extDir))
	}
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	t.Cleanup(cancelAlloc)
	ctx, cancel := context.WithTimeout(allocCtx, budget)
	t.Cleanup(cancel)
	browserCtx, cancelBrowser := chromedp.NewContext(ctx)
	t.Cleanup(cancelBrowser)
	if err := chromedp.Do(browserCtx); err != nil {
		t.Fatal(err)
	}
	h.browserCtx = browserCtx
	if reload {
		loaded, err := chromedp.CallBrowser(browserCtx, extensions.LoadUnpacked, extensions.LoadUnpackedParams{Path: extDir})
		if err != nil {
			t.Fatal(err)
		}
		h.extID = loaded.ID
	}
	until(t, "the extension's service worker", 20*time.Second, func() bool { return h.findWorker() })
	// The worker's target appears before Chrome binds the extension APIs in it; a storage write
	// sent in between finds chrome.storage undefined.
	until(t, "the worker's extension APIs", 20*time.Second, func() bool {
		ready, err := eval[bool](h.swCtx, `typeof chrome.storage?.local?.set === "function"`)
		return err == nil && ready
	})
	// The browser is torn down before the stub, so a held request never blocks the stub's close.
	t.Cleanup(h.api.releaseAll)
	h.setStorage(fmt.Sprintf(`{address: %q}`, h.api.srv.URL))
	return h
}

// findWorker attaches to the extension's current service worker, if it runs.
func (h *harness) findWorker() bool {
	infos, _ := chromedp.Targets(h.browserCtx)
	for _, i := range infos {
		if i.Type == "service_worker" && strings.HasSuffix(i.URL, "/sw.js") && (h.extID == "" || i.URL == "chrome-extension://"+h.extID+"/sw.js") {
			if h.cancelSW != nil {
				h.cancelSW()
			}
			h.swID, h.extID = i.TargetID, strings.Split(strings.TrimPrefix(i.URL, "chrome-extension://"), "/")[0]
			h.swCtx, h.cancelSW = chromedp.NewContext(h.browserCtx, chromedp.WithTargetID(h.swID))
			return true
		}
	}
	return false
}

func (h *harness) setStorage(object string) {
	h.t.Helper()
	if _, err := eval[string](h.swCtx, fmt.Sprintf(`chrome.storage.local.set(%s).then(() => "set")`, object)); err != nil {
		h.t.Fatal(err)
	}
}

// pairDirectly stores the stub's token, as a finished pairing would.
func (h *harness) pairDirectly() {
	h.setStorage(fmt.Sprintf(`{token: %q}`, h.api.currentToken()))
}

func (h *harness) tab(url string) context.Context {
	h.t.Helper()
	c, cancel := chromedp.NewContext(h.browserCtx)
	h.t.Cleanup(cancel)
	if url != "" {
		if err := chromedp.Do(c, chromedp.Navigate(url)); err != nil {
			h.t.Fatal(err)
		}
	}
	return c
}

func (h *harness) popup() context.Context {
	h.t.Helper()
	c := h.tab("chrome-extension://" + h.extID + "/popup.html")
	until(h.t, "the popup to draw", 10*time.Second, func() bool {
		text := h.popupText(c)
		return text != "" && !strings.HasPrefix(text, "Loading")
	})
	return c
}

func (h *harness) popupText(c context.Context) string {
	text, _ := chromedp.Run(c, chromedp.Evaluate[string](`document.getElementById("app")?.textContent ?? ""`))
	return text
}

// popupPress presses a labelled popup button.
func (h *harness) popupPress(c context.Context, label string) {
	h.t.Helper()
	expr := fmt.Sprintf(`[...document.querySelectorAll('button')].find(b => b.textContent === %q)?.click()`, label)
	if _, err := chromedp.Run(c, chromedp.Evaluate[chromedp.Void](expr)); err != nil {
		h.t.Fatal(err)
	}
}

// send posts a message to the worker from the popup page, the way the popup's own buttons do.
func (h *harness) send(popup context.Context, msg string) string {
	h.t.Helper()
	out, err := eval[string](popup, fmt.Sprintf(`(async () => JSON.stringify(await chrome.runtime.sendMessage(%s)))()`, msg))
	if err != nil {
		h.t.Fatal(err)
	}
	return out
}

// panelShow asks the worker to open (or toggle) the panel on the tab at urlPattern, as the popup does.
func (h *harness) panelShow(popup context.Context, urlPattern string, toggle bool) {
	h.t.Helper()
	expr := fmt.Sprintf(`(async () => {
	  const [t] = await chrome.tabs.query({url: %q});
	  return JSON.stringify(await chrome.runtime.sendMessage({kind: "panel.show", tabId: t.id, toggle: %t}));
	})()`, urlPattern, toggle)
	if _, err := eval[string](popup, expr); err != nil {
		h.t.Fatal(err)
	}
}

// stopWorker observes Chrome stopping this worker. A restarted extension worker
// can reuse its target ID, so target disappearance is not proof of suspension.
func (h *harness) stopWorker(page context.Context) {
	h.t.Helper()
	watch, cancel := context.WithTimeout(page, 10*time.Second)
	defer cancel()
	events := chromedp.Events(watch, serviceworker.WorkerVersionUpdated)
	if _, err := chromedp.Call(page, serviceworker.Enable, cdp.Empty{}); err != nil {
		h.t.Fatal(err)
	}
	if _, err := chromedp.Call(page, serviceworker.StopAllWorkers, cdp.Empty{}); err != nil {
		h.t.Fatal(err)
	}
	for ev, err := range events {
		if err != nil {
			h.t.Fatalf("observe worker suspension: %v", err)
		}
		for _, v := range ev.Versions {
			if v.ScriptURL == "chrome-extension://"+h.extID+"/sw.js" && v.RunningStatus == serviceworker.VersionRunningStatusStopped {
				return
			}
		}
	}
	h.t.Fatal("Chrome did not report the extension worker stopped")
}

func (h *harness) tabID(urlPattern string) int {
	h.t.Helper()
	id, err := eval[int](h.swCtx, fmt.Sprintf(`chrome.tabs.query({url: %q}).then(ts => ts[0].id)`, urlPattern))
	if err != nil {
		h.t.Fatal(err)
	}
	return id
}

// selectPanelTab clicks a tab of the panel by its label.
func selectPanelTab(ctx context.Context, label string) error {
	return clickPanel(ctx, label+" tab", func(n *cdp.Node) bool {
		return n.NodeName == "BUTTON" && hasAttrValue(n, "role", "tab") && len(n.Children) == 1 && n.Children[0].NodeValue == label
	})
}

type panelView struct{ launcherHidden, panelHidden bool }

// panelStatus reads whether the launcher and the panel are showing.
func panelStatus(ctx context.Context) (panelView, error) {
	root, err := panelRoot(ctx)
	if err != nil {
		return panelView{}, err
	}
	var v panelView
	walk(root, func(n *cdp.Node) bool {
		switch {
		case n.NodeName == "BUTTON" && hasAttrValue(n, "class", "launcher"):
			v.launcherHidden = hasAttr(n, "hidden")
		case n.NodeName == "SECTION" && hasAttrValue(n, "class", "panel"):
			v.panelHidden = hasAttr(n, "hidden")
		}
		return false
	})
	return v, nil
}

func hasPanelButton(ctx context.Context, label string) bool {
	root, err := panelRoot(ctx)
	if err != nil {
		return false
	}
	return walk(root, func(n *cdp.Node) bool {
		return n.NodeName == "BUTTON" && len(n.Children) == 1 && n.Children[0].NodeValue == label
	})
}

// selectedPanelTab is the label of the panel's selected tab, or "" when there is none.
func selectedPanelTab(ctx context.Context) string {
	root, err := panelRoot(ctx)
	if err != nil {
		return ""
	}
	label := ""
	walk(root, func(n *cdp.Node) bool {
		if n.NodeName == "BUTTON" && hasAttrValue(n, "role", "tab") && hasAttrValue(n, "aria-selected", "true") && len(n.Children) == 1 {
			label = n.Children[0].NodeValue
			return true
		}
		return false
	})
	return label
}

// visiblePanelText is the text of the tab panel that is showing.
func visiblePanelText(ctx context.Context) string {
	root, err := panelRoot(ctx)
	if err != nil {
		return ""
	}
	var b strings.Builder
	walk(root, func(n *cdp.Node) bool {
		if n.NodeName == "DIV" && hasAttrValue(n, "role", "tabpanel") && !hasAttr(n, "hidden") {
			walk(n, func(m *cdp.Node) bool {
				if m.NodeType == 3 {
					b.WriteString(m.NodeValue + " ")
				}
				return false
			})
			return true
		}
		return false
	})
	return b.String()
}
