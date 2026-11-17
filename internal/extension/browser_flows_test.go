//go:build browser

package extension

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/serviceworker"
	"github.com/chromedp/chromedp"
)

// These checks cover the install and daily-use flows of docs/architecture/extension.md
// ("Manual checklist") that need a real browser but not the person's own one.

func waitForPanel(t *testing.T, page context.Context) {
	t.Helper()
	until(t, "the panel to be injected", 20*time.Second, func() bool {
		_, err := panelRoot(page)
		return err == nil
	})
}

func panelButtonShown(ctx context.Context, label string) bool {
	root, err := panelRoot(ctx)
	if err != nil {
		return false
	}
	return walk(root, func(n *cdp.Node) bool {
		return n.NodeName == "BUTTON" && len(n.Children) == 1 && n.Children[0].NodeValue == label && !hasAttr(n, "hidden")
	})
}

func (h *harness) workerToken() string {
	h.t.Helper()
	tok, err := eval[string](h.swCtx, `chrome.storage.local.get("token").then(r => r.token ?? "")`)
	if err != nil {
		h.t.Fatal(err)
	}
	return tok
}

func TestBrowser_PairingMessagesVersionRotationAndOffline(t *testing.T) {
	h := newHarness(t, 150*time.Second)
	api := h.api

	// The version the extensions page shows is the manifest's version_name.
	versionName, err := eval[string](h.swCtx, `chrome.runtime.getManifest().version_name`)
	if err != nil {
		t.Fatal(err)
	}
	if want := "0.1.0+" + h.stamp; versionName != want {
		t.Errorf("manifest version_name %q, want %q", versionName, want)
	}

	popup := h.popup()
	if text := h.popupText(popup); !strings.Contains(text, "Not paired") {
		t.Fatalf("a fresh install should say Not paired: %q", text)
	}
	typeCode := func(code string) {
		if _, err := eval[bool](popup, fmt.Sprintf(`(() => { document.getElementById("code").value = %q; return true; })()`, code)); err != nil {
			t.Fatal(err)
		}
	}
	claimsSeen := func() int {
		api.mu.Lock()
		defer api.mu.Unlock()
		return api.claims
	}

	// A wrong code says so; the application throttles the sixth try, and the popup shows the wait.
	for i := 1; i <= maxWrongCodes; i++ {
		typeCode("WRONGCODE" + fmt.Sprint(i))
		h.popupPress(popup, "Pair")
		until(t, fmt.Sprintf("claim %d to reach the application", i), 10*time.Second, func() bool { return claimsSeen() >= i })
		until(t, "the wrong-code message", 10*time.Second, func() bool {
			return strings.Contains(h.popupText(popup), "That code is wrong, expired or already used.")
		})
	}
	typeCode("WRONGCODE6")
	h.popupPress(popup, "Pair")
	until(t, "the lockout message", 10*time.Second, func() bool {
		return strings.Contains(h.popupText(popup), fmt.Sprintf("Too many tries. Try again in %d s.", lockSeconds))
	})

	// The lockout ends and the right code pairs.
	typeCode(api.code)
	until(t, "the connected state after the lockout", 20*time.Second, func() bool {
		if strings.Contains(h.popupText(popup), "Connected") {
			return true
		}
		h.popupPress(popup, "Pair")
		return false
	})
	text := h.popupText(popup)
	if want := "0.1.0 " + h.stamp; strings.Count(text, want) < 2 {
		t.Errorf("the popup should show build %q for the extension and the application: %q", want, text)
	}
	if h.workerToken() != api.currentToken() {
		t.Error("the worker did not keep the token it was given")
	}

	// A page with the panel, to see the connection state change in it.
	page := h.tab(h.site.srv.URL + "/careers/apply")
	waitForPanel(t, page)
	pattern := h.site.srv.URL + "/careers/*"
	h.panelShow(popup, pattern, false)
	until(t, "the Autofill tab", 15*time.Second, func() bool { return selectPanelTab(page, "Autofill") == nil })
	until(t, "Start while connected", 15*time.Second, func() bool { return panelButtonShown(page, "Start") })

	// A rotated token drops the pairing on the next request; the new code pairs again.
	api.rotate()
	if _, err := chromedp.Run(popup, chromedp.Reload()); err != nil {
		t.Fatal(err)
	}
	until(t, "Not paired after a token rotation", 15*time.Second, func() bool { return strings.Contains(h.popupText(popup), "Not paired") })
	if tok := h.workerToken(); tok != "" {
		t.Errorf("the worker still holds a token after a rejection: %d characters", len(tok))
	}
	until(t, "the pairing form", 10*time.Second, func() bool {
		ok, _ := chromedp.Run(popup, chromedp.Evaluate[bool](`!!document.getElementById("code")`))
		return ok
	})
	typeCode(api.code)
	h.popupPress(popup, "Pair")
	until(t, "Connected after re-pairing", 15*time.Second, func() bool { return strings.Contains(h.popupText(popup), "Connected") })
	if h.workerToken() != api.currentToken() {
		t.Error("the worker did not keep the new token")
	}

	// The application moved to another port: Offline, and a code that carries the port connects again by itself.
	h.setStorage(`{address: "http://127.0.0.1:9"}`)
	api.rotate()
	if _, err := chromedp.Run(popup, chromedp.Reload()); err != nil {
		t.Fatal(err)
	}
	until(t, "Offline at the old port", 15*time.Second, func() bool { return strings.Contains(h.popupText(popup), "Offline") })
	typeCode(strings.TrimPrefix(api.srv.URL, "http://127.0.0.1:") + "-" + api.code)
	h.popupPress(popup, "Pair")
	until(t, "Connected at the code's port", 15*time.Second, func() bool { return strings.Contains(h.popupText(popup), "Connected") })
	if addr, _ := eval[string](h.swCtx, `chrome.storage.local.get("address").then(r => r.address)`); addr != api.srv.URL {
		t.Errorf("the worker saved address %q, want %q", addr, api.srv.URL)
	}

	// The application quits: the popup says Offline with the address, and the panel has no Start.
	api.srv.Close()
	if _, err := chromedp.Run(popup, chromedp.Reload()); err != nil {
		t.Fatal(err)
	}
	until(t, "Offline in the popup", 15*time.Second, func() bool {
		text := h.popupText(popup)
		return strings.Contains(text, "Offline") && strings.Contains(text, "Cannot reach the Applysmith application at "+api.srv.URL)
	})
	// Closing and reopening the panel re-reads the connection.
	h.panelShow(popup, pattern, true)
	h.panelShow(popup, pattern, true)
	until(t, "the panel to say the application is not running", 15*time.Second, func() bool {
		return strings.Contains(panelText(page), "The Applysmith application is not running.")
	})
	if panelButtonShown(page, "Start") {
		t.Error("Start is still offered while the application is not running")
	}
	if !panelButtonShown(page, "Check again") {
		t.Error("the offline panel offers no way to check again")
	}
}

func TestBrowser_LauncherAutoOpenOnceAndSwitchOff(t *testing.T) {
	h := newHarness(t, 150*time.Second)
	h.pairDirectly()
	_, port, _ := strings.Cut(strings.TrimPrefix(h.site.srv.URL, "http://"), ":")
	pageURL := fmt.Sprintf("http://%s:%s/careers/focus", registryHost, port)
	pattern := fmt.Sprintf("http://%s:%s/careers/*", registryHost, port)
	popup := h.popup()

	if key, err := eval[string](h.swCtx, `chrome.runtime.getManifest().commands["toggle-panel"].suggested_key.default`); err != nil || key != "Alt+Shift+C" {
		t.Errorf("the panel shortcut is %q (%v)", key, err)
	}
	focused := func(page context.Context) string {
		id, _ := chromedp.Run(page, chromedp.Evaluate[string](`document.activeElement?.id || (document.activeElement?.hasAttribute("data-applysmith") ? "panel" : "")`))
		return id
	}

	// On a supported application system the panel opens by itself, without taking focus.
	page := h.tab(pageURL)
	waitForPanel(t, page)
	until(t, "the panel to open by itself", 20*time.Second, func() bool {
		v, err := panelStatus(page)
		return err == nil && !v.panelHidden && v.launcherHidden
	})
	if id := focused(page); id != "preferred" {
		t.Errorf("the automatic open moved focus to %q", id)
	}

	// Closed by the person, it stays closed after a reload: the automatic open is once per tab and host.
	if err := clickPanel(page, "close button", func(n *cdp.Node) bool {
		return n.NodeName == "BUTTON" && hasAttrValue(n, "aria-label", "Close panel")
	}); err != nil {
		t.Fatal(err)
	}
	until(t, "the launcher after closing", 10*time.Second, func() bool {
		v, err := panelStatus(page)
		return err == nil && v.panelHidden && !v.launcherHidden
	})
	if err := chromedp.Do(page, chromedp.Reload()); err != nil {
		t.Fatal(err)
	}
	waitForPanel(t, page)
	for i := 0; i < 10; i++ {
		if v, err := panelStatus(page); err == nil && !v.panelHidden {
			t.Fatal("the panel opened by itself a second time")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if v, _ := panelStatus(page); v.launcherHidden {
		t.Error("the launcher is missing after the reload")
	}

	// The launcher opens it and takes focus; the shortcut's message toggles it.
	if err := clickPanel(page, "launcher", func(n *cdp.Node) bool { return n.NodeName == "BUTTON" && hasAttrValue(n, "class", "launcher") }); err != nil {
		t.Fatal(err)
	}
	until(t, "the panel to open from the launcher", 10*time.Second, func() bool {
		v, err := panelStatus(page)
		return err == nil && !v.panelHidden
	})
	if id := focused(page); id != "panel" {
		t.Errorf("opening from the launcher left focus on %q", id)
	}
	h.panelShow(popup, pattern, true)
	until(t, "the toggle to close the panel", 10*time.Second, func() bool {
		v, err := panelStatus(page)
		return err == nil && v.panelHidden
	})

	// Turn off on this host: a new page gets no launcher and no automatic open, and the explicit open still works.
	if out := h.send(popup, fmt.Sprintf(`{kind: "site.set", host: %q, off: true}`, registryHost)); !strings.Contains(out, `"ok":true`) {
		t.Fatalf("site.set answered %s", out)
	}
	if err := chromedp.Cancel(page); err != nil {
		t.Fatal(err)
	}
	off := h.tab(pageURL)
	for i := 0; i < 10; i++ {
		n, err := chromedp.Run(off, chromedp.Evaluate[int](`document.querySelectorAll('[data-applysmith]').length`))
		if err != nil || n != 0 {
			t.Fatalf("a switched-off host got a panel (%d, %v)", n, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	h.panelShow(popup, pattern, false)
	until(t, "the explicit open on a switched-off host", 15*time.Second, func() bool {
		v, err := panelStatus(off)
		return err == nil && !v.panelHidden
	})
	h.panelShow(popup, pattern, true)
	until(t, "the panel to close again", 10*time.Second, func() bool {
		v, err := panelStatus(off)
		return err == nil && v.panelHidden
	})
	if v, _ := panelStatus(off); !v.launcherHidden {
		t.Error("the launcher is back on a switched-off host")
	}
}

func TestBrowser_FillSurvivesAStoppedWorkerForPauseResumeAndStop(t *testing.T) {
	h := newHarness(t, 180*time.Second)
	h.pairDirectly()
	api := h.api
	popup := h.popup()
	pattern := h.site.srv.URL + "/careers/*"
	page := h.tab(h.site.srv.URL + "/careers/apply")
	waitForPanel(t, page)
	h.panelShow(popup, pattern, false)
	until(t, "the Autofill tab", 15*time.Second, func() bool { return selectPanelTab(page, "Autofill") == nil })

	// A fill held at the matcher stays in flight; pause it, then stop the worker for real.
	api.hold()
	until(t, "Start", 15*time.Second, func() bool { return clickPanelButton(page, "Start") == nil })
	until(t, "the matcher to be asked", 60*time.Second, func() bool { return api.heldCount() >= 1 })
	until(t, "Pause", 15*time.Second, func() bool { return clickPanelButton(page, "Pause") == nil })
	until(t, "the paused fill", 15*time.Second, func() bool { return strings.Contains(panelText(page), "Paused after") })
	h.stopWorker(page)
	api.release()
	until(t, "the restarted worker", 15*time.Second, h.findWorker)
	until(t, "the paused fill to be read back", 15*time.Second, func() bool { return strings.Contains(panelText(page), "Paused after") })

	// Resume reaches the revived fill and the result appears.
	until(t, "Resume", 15*time.Second, func() bool { return clickPanelButton(page, "Resume") == nil })
	until(t, "the fill result", 90*time.Second, func() bool { return strings.Contains(panelText(page), "Dismiss result") })
	until(t, "the run to end", 15*time.Second, func() bool { return len(api.endings()) >= 1 })
	if ends := api.endings(); !strings.HasSuffix(ends[0], " succeeded") {
		t.Errorf("run endings %v", ends)
	}

	// Stop reaches a fill the restarted worker has to read back.
	api.hold()
	until(t, "Fill again", 15*time.Second, func() bool { return clickPanelButton(page, "Fill again") == nil })
	until(t, "the second matcher request", 60*time.Second, func() bool { return api.heldCount() >= 2 })
	until(t, "Pause again", 15*time.Second, func() bool { return clickPanelButton(page, "Pause") == nil })
	until(t, "the second paused fill", 15*time.Second, func() bool { return strings.Contains(panelText(page), "Paused after") })
	h.stopWorker(page)
	api.release()
	until(t, "the restarted worker again", 15*time.Second, h.findWorker)
	until(t, "the second paused fill to be read back", 15*time.Second, func() bool { return strings.Contains(panelText(page), "Paused after") })
	until(t, "Stop", 15*time.Second, func() bool { return clickPanelButton(page, "Stop") == nil })
	until(t, "the stopped fill", 60*time.Second, func() bool { return panelButtonShown(page, "Fill again") && !panelButtonShown(page, "Stop") })
	until(t, "the second run to be ended as cancelled", 15*time.Second, func() bool {
		ends := api.endings()
		return len(ends) >= 2 && strings.HasSuffix(ends[len(ends)-1], " cancelled")
	})
	if n := h.site.submitted.Load(); n != 0 {
		t.Errorf("the page was submitted %d times", n)
	}
}

func TestBrowser_NextStepPressesTheEmbeddedFormOnce(t *testing.T) {
	h := newHarness(t, 150*time.Second)
	h.pairDirectly()
	popup := h.popup()
	pattern := h.site.srv.URL + "/careers/*"

	// A page whose only button is Submit offers no Next step.
	plain := h.tab(h.site.srv.URL + "/careers/apply")
	waitForPanel(t, plain)
	h.panelShow(popup, pattern, false)
	until(t, "the Autofill tab", 15*time.Second, func() bool { return selectPanelTab(plain, "Autofill") == nil })
	until(t, "the Autofill tab to draw", 15*time.Second, func() bool { return hasPanelButton(plain, "Next step") })
	if panelButtonShown(plain, "Next step") {
		t.Error("Next step is offered on a page with no progress control and no embedded form")
	}

	// A frame from another origin owns Next; the press is fanned out to it, once, and never reaches Submit.
	embed := h.tab(h.site.srv.URL + "/careers/embed")
	waitForPanel(t, embed)
	h.panelShow(popup, h.site.srv.URL+"/careers/embed", false)
	until(t, "the Autofill tab on the embedding page", 15*time.Second, func() bool { return selectPanelTab(embed, "Autofill") == nil })
	until(t, "Next step to show", 15*time.Second, func() bool { return panelButtonShown(embed, "Next step") })
	if err := clickPanelButton(embed, "Next step"); err != nil {
		t.Fatal(err)
	}
	until(t, "the frame's Next to be pressed", 20*time.Second, func() bool { return h.site.nextPressed.Load() >= 1 })
	until(t, "the panel to say what was pressed", 10*time.Second, func() bool { return strings.Contains(panelText(embed), `Pressed "Next`) })
	time.Sleep(1500 * time.Millisecond)
	if n := h.site.nextPressed.Load(); n != 1 {
		t.Errorf("one click pressed Next %d times", n)
	}
	if n := h.site.submitted.Load(); n != 0 {
		t.Errorf("Next step pressed Submit %d times", n)
	}
}

// registryPage is the fixture's page at a host the engine's site table knows, served over plain http as the
// real application systems are: a context where crypto.randomUUID does not exist.
func (h *harness) registryPage(name string) (pattern, pageURL string) {
	_, port, _ := strings.Cut(strings.TrimPrefix(h.site.srv.URL, "http://"), ":")
	return fmt.Sprintf("http://%s:%s/careers/*", registryHost, port), fmt.Sprintf("http://%s:%s/careers/%s", registryHost, port, name)
}

// reloadOpensPanel reloads the page and shows its Autofill tab again, as a person would after the reload.
func (h *harness) reloadOpensPanel(page, popup context.Context, pattern string) {
	h.t.Helper()
	before, err := eval[float64](page, "performance.timeOrigin")
	if err != nil {
		h.t.Fatal(err)
	}
	if err := chromedp.Do(page, chromedp.Reload()); err != nil {
		h.t.Fatal(err)
	}
	// Reload returns before commit: a panel from the old document is not proof
	// that the new document is ready for a user action.
	until(h.t, "the reloaded document to commit", 15*time.Second, func() bool {
		now, err := eval[float64](page, "performance.timeOrigin")
		return err == nil && now != before
	})
	waitForPanel(h.t, page)
	h.panelShow(popup, pattern, false)
	until(h.t, "the Autofill tab after the reload", 15*time.Second, func() bool { return selectPanelTab(page, "Autofill") == nil })
}

// formIsEmpty reports whether no field of the page holds a value.
func formIsEmpty(t *testing.T, page context.Context) bool {
	t.Helper()
	empty, err := eval[bool](page, `[...document.querySelectorAll("input:not([type=password]), select")].every(el => el.value === "")`)
	if err != nil {
		t.Fatal(err)
	}
	return empty
}

func TestBrowser_ReloadDuringAFillEndsItCancelled(t *testing.T) {
	h := newHarness(t, 150*time.Second)
	h.pairDirectly()
	api := h.api
	popup := h.popup()
	pattern, pageURL := h.registryPage("apply")
	page := h.tab(pageURL)
	waitForPanel(t, page)
	h.panelShow(popup, pattern, false)
	until(t, "the Autofill tab", 15*time.Second, func() bool { return selectPanelTab(page, "Autofill") == nil })

	// The matcher is slow, so the fill is still running when the page reloads.
	api.hold()
	until(t, "Start", 15*time.Second, func() bool { return clickPanelButton(page, "Start") == nil })
	until(t, "the matcher to be asked", 60*time.Second, func() bool { return api.heldCount() >= 1 })
	until(t, "the running fill", 15*time.Second, func() bool { return strings.Contains(panelText(page), "Filling the form") })

	h.reloadOpensPanel(page, popup, pattern)
	until(t, "the panel to say the fill ended because the page changed", 20*time.Second, func() bool {
		return strings.Contains(visiblePanelText(page), "The page changed, so the fill ended.")
	})
	if strings.Contains(panelText(page), "Filling the form") {
		t.Error("the panel of the new page still shows a running fill")
	}
	until(t, "the run to be ended as cancelled", 15*time.Second, func() bool { return len(api.endings()) >= 1 })

	// The matcher answers late: nothing may reach the new document.
	api.release()
	until(t, "the late matcher answer to be served", 15*time.Second, func() bool {
		_, _, _, matches := api.snapshot()
		return len(matches) >= 1
	})
	if !formIsEmpty(t, page) {
		t.Error("the fill wrote into the document that replaced the one it was started on")
	}
	if ends := api.endings(); len(ends) != 1 || !strings.HasSuffix(ends[0], " cancelled") {
		t.Errorf("run endings %v, want one cancelled", ends)
	}
	if w, _, _, _ := api.snapshot(); len(w) != 0 {
		t.Errorf("%d system writes were reported for a fill that never wrote", len(w))
	}
	if n := h.site.submitted.Load(); n != 0 {
		t.Errorf("the page was submitted %d times", n)
	}
}

func TestBrowser_ReloadWhileTheFillStartsWritesNothing(t *testing.T) {
	h := newHarness(t, 150*time.Second)
	h.pairDirectly()
	api := h.api
	popup := h.popup()
	pattern, pageURL := h.registryPage("apply")
	page := h.tab(pageURL)
	waitForPanel(t, page)
	h.panelShow(popup, pattern, false)
	until(t, "the Autofill tab", 15*time.Second, func() bool { return selectPanelTab(page, "Autofill") == nil })

	// The applicant read is slow, so the fill is still starting when the page reloads.
	api.holdStart("/api/autofill/applicant")
	until(t, "Start", 15*time.Second, func() bool { return clickPanelButton(page, "Start") == nil })
	until(t, "the applicant to be asked", 30*time.Second, func() bool { return api.startHeldCount("/api/autofill/applicant") >= 1 })
	h.reloadOpensPanel(page, popup, pattern)
	api.releaseStart("/api/autofill/applicant")

	// The start finds the page gone and ends without a run. The window is bounded and polled,
	// since there is no event to wait for when nothing is supposed to happen.
	until(t, "Start on the new page", 20*time.Second, func() bool { return panelButtonShown(page, "Start") })
	for i := 0; i < 20; i++ {
		if n := api.runsIssued(); n != 0 {
			t.Fatalf("a run was issued for a start whose page was gone (%d)", n)
		}
		if strings.Contains(panelText(page), "Filling the form") {
			t.Fatal("the new page shows a fill that was started on the old one")
		}
		if !formIsEmpty(t, page) {
			t.Fatal("the fill wrote into the new document")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if n := len(api.endings()); n != 0 {
		t.Errorf("a run that was never issued was ended %d times", n)
	}
}

// blockWorker keeps the extension's service worker busy for ms milliseconds once it has said so in
// session storage, which the popup can read while the worker cannot answer. Messages and navigation
// events sent in the meantime queue up behind it.
func (h *harness) blockWorker(popup context.Context, ms int) {
	h.t.Helper()
	expr := fmt.Sprintf(`setTimeout(async () => { await chrome.storage.session.set({busy: 1}); const t = Date.now() + %d; while (Date.now() < t) {} await chrome.storage.session.remove("busy"); }, 0)`, ms)
	if _, err := eval[chromedp.Void](h.swCtx, expr); err != nil {
		h.t.Fatal(err)
	}
	until(h.t, "the worker to be busy", 10*time.Second, func() bool {
		busy, err := eval[bool](popup, `chrome.storage.session.get("busy").then(r => r.busy === 1)`)
		return err == nil && busy
	})
}

func TestBrowser_StartHandledAfterTheReloadCommitsIsDropped(t *testing.T) {
	h := newHarness(t, 150*time.Second)
	h.pairDirectly()
	api := h.api
	popup := h.popup()
	pattern, pageURL := h.registryPage("apply")
	page := h.tab(pageURL)
	waitForPanel(t, page)
	defer func() {
		if t.Failed() {
			t.Logf("new page panel: %q; issued runs: %d", panelText(page), api.runsIssued())
		}
	}()
	h.panelShow(popup, pattern, false)
	until(t, "the Autofill tab", 15*time.Second, func() bool { return selectPanelTab(page, "Autofill") == nil })
	world, err := extensionWorld(page, h.extID)
	if err != nil {
		t.Fatal(err)
	}

	// The worker is busy: Start is sent from the old document, the page reloads, and only then
	// does the worker see the message.
	h.blockWorker(popup, 4000)
	no := false
	if _, err := chromedp.Call(page, runtime.Evaluate, runtime.EvaluateParams{
		Expression:   `chrome.runtime.sendMessage({kind: "fill.start", reading: {url: location.href, title: document.title, jdText: "", posting: false, form: true}, doc: globalThis.__applysmithDoc}); 1`,
		ContextID:    world,
		AwaitPromise: &no,
	}); err != nil {
		t.Fatal(err)
	}
	h.reloadOpensPanel(page, popup, pattern)

	until(t, "the worker to be free again", 20*time.Second, func() bool {
		busy, err := eval[bool](popup, `chrome.storage.session.get("busy").then(r => r.busy === 1)`)
		return err == nil && !busy
	})
	until(t, "Start on the new page", 20*time.Second, func() bool { return panelButtonShown(page, "Start") })
	for i := 0; i < 20; i++ {
		if n := api.runsIssued(); n != 0 {
			t.Fatalf("a run was issued for a Start sent from a document that was gone (%d)", n)
		}
		if strings.Contains(panelText(page), "Filling the form") {
			t.Fatal("the new page shows a fill that was started on the old one")
		}
		if !formIsEmpty(t, page) {
			t.Fatal("the fill wrote into the new document")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if ends := api.endings(); len(ends) != 0 {
		t.Errorf("run endings %v, want none", ends)
	}
}

func TestBrowser_ReloadWhileTheRunIsIssuedCancelsItAndWritesNothing(t *testing.T) {
	h := newHarness(t, 150*time.Second)
	h.pairDirectly()
	api := h.api
	popup := h.popup()
	pattern, pageURL := h.registryPage("apply")
	page := h.tab(pageURL)
	waitForPanel(t, page)
	h.panelShow(popup, pattern, false)
	until(t, "the Autofill tab", 15*time.Second, func() bool { return selectPanelTab(page, "Autofill") == nil })

	// The run is issued only after the page reloaded, so the start continues past its first check.
	api.holdStart("/api/autofill/runs")
	until(t, "Start", 15*time.Second, func() bool { return clickPanelButton(page, "Start") == nil })
	until(t, "the run request", 30*time.Second, func() bool { return api.startHeldCount("/api/autofill/runs") >= 1 })
	h.reloadOpensPanel(page, popup, pattern)
	api.releaseStart("/api/autofill/runs")

	until(t, "the run to be ended as cancelled", 20*time.Second, func() bool { return len(api.endings()) >= 1 })
	// The new panel either read its status before the run existed and offers Start, or read it after
	// and says the page changed; it never shows a fill.
	until(t, "the new page's panel to settle", 20*time.Second, func() bool {
		return panelButtonShown(page, "Start") || strings.Contains(visiblePanelText(page), "The page changed, so the fill ended.")
	})
	if strings.Contains(panelText(page), "Filling the form") {
		t.Fatal("the new page shows a fill that was started on the old one")
	}
	for i := 0; i < 15; i++ {
		if !formIsEmpty(t, page) {
			t.Fatal("the fill wrote into the new document")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if ends := api.endings(); len(ends) != 1 || !strings.HasSuffix(ends[0], " cancelled") {
		t.Errorf("run endings %v, want one cancelled", ends)
	}
}

// applicationFrame is an application form as an ATS embeds it: its Submit
// button posts to the frame's own origin and the form goes away once the post
// is answered. The page-world hook reports the post, and the post with no
// submit control left is the confirmation.
const applicationFrame = `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Application</title></head><body>
<form onsubmit="event.preventDefault()">
<label for="fn">First name</label><input id="fn" name="fn">
<label for="ln">Last name</label><input id="ln" name="ln">
<label for="em">Email</label><input id="em" name="em" type="email">
<label for="ph">Phone</label><input id="ph" name="ph" type="tel">
<label for="cv">Resume</label><input id="cv" name="cv" type="file">
<button type="button" id="send" onclick="fetch('/frame/applications/submit', {method: 'POST'}).then(() => document.querySelector('form').remove())">Submit application</button>
</form></body></html>`

// spaHome reads as no application at all (no form, no application word in its
// address) until the person's click swaps the application form in under a new
// address, the way a single-page careers site does.
const spaHome = `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Example Corp</title></head><body>
<h1>Example Corp</h1><div id="app"><p>Welcome.</p></div>
<script>
function showForm(path) {
  history.pushState({}, "", path);
  document.getElementById("app").innerHTML = '<form onsubmit="event.preventDefault()">' +
    '<label for="fn">First name</label><input id="fn"><label for="ln">Last name</label><input id="ln">' +
    '<label for="em">Email</label><input id="em" type="email"><label for="ph">Phone</label><input id="ph" type="tel">' +
    '<label for="cv">Resume</label><input id="cv" type="file">' +
    '<button type="button" id="send">Submit application</button></form>';
}
</script></body></html>`

// submissionsAt is the records posted for a page address ending in suffix.
func submissionsAt(h *harness, suffix string) []map[string]any {
	var out []map[string]any
	for _, s := range h.api.submissions() {
		if strings.HasSuffix(s["url"].(string), suffix) {
			out = append(out, s)
		}
	}
	return out
}

// clickByID presses the element with this id, in the page or in any frame of
// it, with a real (trusted) mouse click at its center.
func clickByID(ctx context.Context, id string) error {
	doc, err := chromedp.Call(ctx, dom.GetDocument, dom.GetDocumentParams{Depth: &depthAll, Pierce: &pierce})
	if err != nil {
		return err
	}
	var node *cdp.Node
	walk(doc.Root, func(n *cdp.Node) bool {
		if n.NodeType == 1 && hasAttrValue(n, "id", id) {
			node = n
			return true
		}
		return false
	})
	if node == nil {
		return fmt.Errorf("no #%s", id)
	}
	q, err := chromedp.Call(ctx, dom.GetContentQuads, dom.GetContentQuadsParams{BackendNodeID: node.BackendNodeID})
	if err != nil {
		return err
	}
	if len(q.Quads) == 0 || len(q.Quads[0]) < 8 {
		return fmt.Errorf("#%s has no box", id)
	}
	b := q.Quads[0]
	x, y := (b[0]+b[2]+b[4]+b[6])/4, (b[1]+b[3]+b[5]+b[7])/4
	return chromedp.Do(ctx, chromedp.MouseClickXY(x, y))
}

func TestE2E_Extension_SubmitObservedInIframeAndAfterSpaNavigation(t *testing.T) {
	h := newHarness(t, 200*time.Second)
	h.pairDirectly()

	// The ATS frame is another origin of the same site, as an embed on a company's careers page is.
	var confirms atomic.Int32
	frameMux := http.NewServeMux()
	frameMux.HandleFunc("/frame/application", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, applicationFrame) })
	frameMux.HandleFunc("/frame/applications/submit", func(w http.ResponseWriter, _ *http.Request) {
		confirms.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	})
	frameSrv := httptest.NewServer(frameMux)
	t.Cleanup(frameSrv.Close)
	topMux := http.NewServeMux()
	topMux.HandleFunc("/careers/embedded", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, embedPage(frameSrv.URL+"/frame/application?token=abc"))
	})
	topMux.HandleFunc("/spa/", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, spaHome) })
	topSrv := httptest.NewServer(topMux)
	t.Cleanup(topSrv.Close)

	// A submit inside the ATS iframe is recorded once, by the frame's own recorder, and confirmed by the frame's hook.
	page := h.tab(topSrv.URL + "/careers/embedded")
	waitForPanel(t, page)
	time.Sleep(1500 * time.Millisecond) // the frame's recorder is armed after the top frame's widgets start
	if err := clickByID(page, "send"); err != nil {
		t.Fatal(err)
	}
	until(t, "the frame's submission and its confirmation", 20*time.Second, func() bool {
		subs := submissionsAt(h, "/frame/application")
		return len(subs) >= 2 && confirms.Load() == 1
	})
	time.Sleep(1500 * time.Millisecond)
	subs := submissionsAt(h, "/frame/application")
	ids := map[any]bool{}
	confirmed := false
	for _, s := range subs {
		ids[s["entryId"]] = true
		confirmed = confirmed || s["confirmed"] == true
		if !strings.HasPrefix(s["url"].(string), frameSrv.URL) || strings.Contains(s["url"].(string), "token") {
			t.Errorf("the frame's record names %v", s["url"])
		}
	}
	if len(ids) != 1 || !confirmed {
		t.Fatalf("one click in the frame gave %d entries (%d records), confirmed %v", len(ids), len(subs), confirmed)
	}

	// A single-page site: the form arrives under a new address after the loader's 30 s watch has ended.
	spa := h.tab(topSrv.URL + "/spa/home")
	time.Sleep(32 * time.Second)
	if n, err := eval[int](spa, `document.querySelectorAll('[data-applysmith]').length`); err != nil || n != 0 {
		t.Fatalf("the home page got the panel (%d, %v)", n, err)
	}
	if _, err := eval[bool](spa, `(showForm("/spa/apply"), true)`); err != nil {
		t.Fatal(err)
	}
	waitForPanel(t, spa)
	// A second address change runs the loader again; nothing is installed twice.
	if _, err := eval[bool](spa, `(history.pushState({}, "", "/spa/apply/review"), true)`); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if n, err := eval[int](spa, `document.querySelectorAll('[data-applysmith]').length`); err != nil || n < 1 {
		t.Fatalf("the panel is missing after the second change (%d, %v)", n, err)
	}
	if err := clickByID(spa, "send"); err != nil {
		t.Fatal(err)
	}
	until(t, "the single-page site's submission", 15*time.Second, func() bool { return len(submissionsAt(h, "/spa/apply/review")) >= 1 })
	time.Sleep(1500 * time.Millisecond)
	ids = map[any]bool{}
	for _, s := range submissionsAt(h, "/spa/apply/review") {
		ids[s["entryId"]] = true
	}
	if len(ids) != 1 {
		t.Fatalf("one click after the address change gave %d entries", len(ids))
	}
}

// Reloading the extension invalidates its old content-script world, unlike
// suspending the service worker. Late matcher replies must not resume that fill.
func TestBrowser_ExtensionReloadDuringFillCancelsPendingWork(t *testing.T) {
	h := newHarnessInstall(t, 150*time.Second, true)
	h.pairDirectly()
	api := h.api
	popup := h.popup()
	pattern, pageURL := h.registryPage("apply")
	page := h.tab(pageURL)
	waitForPanel(t, page)
	h.panelShow(popup, pattern, false)
	until(t, "the Autofill tab", 15*time.Second, func() bool { return selectPanelTab(page, "Autofill") == nil })
	oldWorld, err := extensionWorld(page, h.extID)
	if err != nil {
		t.Fatal(err)
	}
	api.hold()
	until(t, "Start", 15*time.Second, func() bool { return clickPanelButton(page, "Start") == nil })
	until(t, "the matcher request before extension reload", 60*time.Second, func() bool { return api.heldCount() >= 1 })
	readValues := func() string {
		values, err := eval[string](page, `JSON.stringify([...document.querySelectorAll("input,select,textarea")].map(el => [el.name,el.value]))`)
		if err != nil {
			t.Fatal(err)
		}
		return values
	}
	before := readValues()
	status, err := chromedp.Call(page, runtime.Evaluate, runtime.EvaluateParams{Expression: `globalThis.__applysmithEngine?.status()?.state`, ContextID: oldWorld, ReturnByValue: new(true)})
	if err != nil || status.Result == nil || string(status.Result.Value) != `"running"` {
		t.Fatalf("fill must still be running before runtime.reload: %+v err=%v", status, err)
	}
	oldWorker := h.swID
	if _, err := eval[bool](h.swCtx, `(() => { setTimeout(() => chrome.runtime.reload(), 10); return true; })()`); err != nil {
		t.Fatal(err)
	}
	until(t, "the old extension worker to disappear", 15*time.Second, func() bool {
		infos, err := chromedp.Targets(h.browserCtx)
		if err != nil {
			return false
		}
		for _, info := range infos {
			if info.TargetID == oldWorker {
				return false
			}
		}
		return true
	})
	h.cancelSW()
	h.cancelSW = nil
	if _, err := chromedp.Call(page, serviceworker.Enable, cdp.Empty{}); err != nil {
		t.Fatal(err)
	}
	_, wakeErr := chromedp.Call(page, serviceworker.StartWorker, serviceworker.StartWorkerParams{ScopeURL: "chrome-extension://" + h.extID + "/"})
	t.Logf("worker wake: %v", wakeErr)
	until(t, "the reloaded extension worker", 20*time.Second, func() bool { return h.findWorker() && h.swID != oldWorker })
	api.release()
	until(t, "the old engine to settle after extension invalidation", 20*time.Second, func() bool {
		out, err := chromedp.Call(page, runtime.Evaluate, runtime.EvaluateParams{Expression: `["done", "stopped", "error"].includes(globalThis.__applysmithEngine?.status()?.state)`, ContextID: oldWorld, ReturnByValue: new(true)})
		return err == nil && out.Result != nil && string(out.Result.Value) == "true"
	})
	until(t, "the extension-reloaded fill to end cancelled", 20*time.Second, func() bool {
		ends := api.endings()
		return len(ends) == 1 && strings.HasSuffix(ends[0], " cancelled")
	})
	until(t, "the held matcher response to finish", 15*time.Second, func() bool {
		_, _, _, matches := api.snapshot()
		return len(matches) >= 1
	})
	if after := readValues(); after != before {
		t.Errorf("a late matcher reply wrote after extension reload: before=%s after=%s", before, after)
	}
	if ends := api.endings(); len(ends) != 1 || !strings.HasSuffix(ends[0], " cancelled") {
		t.Errorf("run endings %v, want one cancelled", ends)
	}
	if n := h.site.submitted.Load(); n != 0 {
		t.Errorf("the page was submitted %d times", n)
	}
}
