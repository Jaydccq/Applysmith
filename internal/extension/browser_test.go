//go:build browser

package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// eval runs an expression in the tab or worker of ctx and decodes its value.
func eval[T any](ctx context.Context, expr string) (T, error) {
	return chromedp.Run(ctx, chromedp.Evaluate[T](expr, chromedp.EvalAwaitPromise))
}

// This check loads the built extension unpacked into Chrome for Testing with a
// throwaway profile (branded Chrome ignores --load-extension), pairs it with a
// stub API in the popup, and fills a synthetic page. It runs only with the
// browser build tag (make test-browser).

const applyPage = `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Apply, Example Corp</title></head><body>
<h1>Apply to Example Corp</h1>
<form id="f" onsubmit="event.preventDefault(); navigator.sendBeacon('/submitted')">
<label for="first_name">First name</label><input id="first_name" name="first_name" required>
<label for="last_name">Last name</label><input id="last_name" name="last_name" required>
<label for="email">Email</label><input id="email" name="email" type="email" required>
<label for="preferred">Preferred name</label><input id="preferred" name="preferred">
<label for="auth">Are you legally authorized to work in the United States?</label>
<select id="auth" name="auth" required><option value="">Select</option><option>Yes</option><option>No</option></select>
<label for="gender">Gender</label><input id="gender" name="gender">
<label for="pw">Password</label><input id="pw" name="password" type="password">
<button type="submit" id="submit">Submit application</button>
</form></body></html>`

const newsPage = `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>News</title></head><body>
<h1>Local news</h1><form role="search"><input type="search" name="q" placeholder="Search"></form>
<form><input name="newsletter" type="email" placeholder="Newsletter"></form></body></html>`

// The registry host's page keeps focus on a field of its own, so an automatic
// open that steals focus is visible.
var focusPage = strings.Replace(applyPage, `<input id="preferred" name="preferred">`, `<input id="preferred" name="preferred" autofocus>`, 1)

// The embedding page has no form control of its own; the application is a
// frame from another origin, whose Next button and Submit button count presses.
const framePage = `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Apply</title></head><body>
<form onsubmit="event.preventDefault()">
<label for="n">Name</label><input id="n" name="n">
<label for="e">Email</label><input id="e" name="e" type="email">
<button type="button" id="next" onclick="navigator.sendBeacon('/next-pressed')">Next &rarr;</button>
<button type="button" id="final" onclick="navigator.sendBeacon('/submitted')">Submit application</button>
</form></body></html>`

func embedPage(frameURL string) string {
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Careers, Example Corp</title></head><body>
<h1>Open roles at Example Corp</h1><iframe id="app" src="` + frameURL + `" width="600" height="400"></iframe></body></html>`
}

func chromium(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("APPLYSMITH_TEST_CHROMIUM"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	matches, _ := filepath.Glob(filepath.Join(home, "Library", "Caches", "ms-playwright", "chromium-*", "chrome-mac*", "Google Chrome for Testing.app", "Contents", "MacOS", "Google Chrome for Testing"))
	if len(matches) == 0 {
		t.Skip("no Chrome for Testing: set APPLYSMITH_TEST_CHROMIUM to one")
	}
	return matches[len(matches)-1]
}

type fixtureSite struct {
	srv         *httptest.Server
	submitted   atomic.Int32
	nextPressed atomic.Int32
}

func newFixtureSite(t *testing.T) *fixtureSite {
	f := &fixtureSite{}
	mux := http.NewServeMux()
	mux.HandleFunc("/careers/apply", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "example-session-value", Path: "/", HttpOnly: true})
		fmt.Fprint(w, applyPage)
	})
	mux.HandleFunc("/news/story", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, newsPage) })
	mux.HandleFunc("/careers/focus", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, focusPage) })
	mux.HandleFunc("/careers/embed", func(w http.ResponseWriter, r *http.Request) {
		_, port, _ := net.SplitHostPort(r.Host)
		fmt.Fprint(w, embedPage("http://localhost:"+port+"/frame/apply"))
	})
	mux.HandleFunc("/frame/apply", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, framePage) })
	mux.HandleFunc("/next-pressed", func(http.ResponseWriter, *http.Request) { f.nextPressed.Add(1) })
	mux.HandleFunc("/submitted", func(http.ResponseWriter, *http.Request) { f.submitted.Add(1) })
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// children lists every node below n, shadow roots and frame documents included.
func children(n *cdp.Node) []*cdp.Node {
	out := append([]*cdp.Node{}, n.Children...)
	out = append(out, n.ShadowRoots...)
	if n.ContentDocument != nil {
		out = append(out, n.ContentDocument)
	}
	return out
}

func walk(n *cdp.Node, visit func(*cdp.Node) bool) bool {
	if visit(n) {
		return true
	}
	for _, c := range children(n) {
		if walk(c, visit) {
			return true
		}
	}
	return false
}

func hasAttr(n *cdp.Node, name string) bool {
	for i := 0; i+1 < len(n.Attributes); i += 2 {
		if n.Attributes[i] == name {
			return true
		}
	}
	return false
}

func hasAttrValue(n *cdp.Node, name, value string) bool {
	for i := 0; i+1 < len(n.Attributes); i += 2 {
		if n.Attributes[i] == name && n.Attributes[i+1] == value {
			return true
		}
	}
	return false
}

var (
	depthAll int64 = -1
	pierce         = true
)

// panelRoot is the closed shadow root of the panel, which DevTools can read.
func panelRoot(ctx context.Context) (*cdp.Node, error) {
	doc, err := chromedp.Call(ctx, dom.GetDocument, dom.GetDocumentParams{Depth: &depthAll, Pierce: &pierce})
	if err != nil {
		return nil, err
	}
	var found *cdp.Node
	walk(doc.Root, func(n *cdp.Node) bool {
		if n.NodeName == "DIV" && hasAttr(n, "data-applysmith") && len(n.ShadowRoots) > 0 {
			found = n.ShadowRoots[0]
			return true
		}
		return false
	})
	if found == nil {
		return nil, fmt.Errorf("no panel in the page")
	}
	return found, nil
}

func panelText(ctx context.Context) string {
	root, err := panelRoot(ctx)
	if err != nil {
		return ""
	}
	var b strings.Builder
	walk(root, func(n *cdp.Node) bool {
		if n.NodeName == "STYLE" {
			return false
		}
		if n.NodeType == 3 {
			b.WriteString(n.NodeValue + " ")
		}
		return false
	})
	return b.String()
}

// clickPanel clicks the first panel node the predicate accepts; tab buttons,
// action buttons and the walk's checkbox all go through it.
func clickPanel(ctx context.Context, what string, match func(*cdp.Node) bool) error {
	return callPanel(ctx, what, match, "this.click()")
}

// focusPanelNode moves keyboard focus to the first panel node the predicate accepts.
func focusPanelNode(ctx context.Context, what string, match func(*cdp.Node) bool) error {
	return callPanel(ctx, what, match, "this.focus()")
}

func callPanel(ctx context.Context, what string, match func(*cdp.Node) bool, body string) error {
	root, err := panelRoot(ctx)
	if err != nil {
		return err
	}
	var node *cdp.Node
	walk(root, func(n *cdp.Node) bool {
		if match(n) {
			node = n
			return true
		}
		return false
	})
	if node == nil {
		return fmt.Errorf("no %s in the panel", what)
	}
	obj, err := chromedp.Call(ctx, dom.ResolveNode, dom.ResolveNodeParams{BackendNodeID: node.BackendNodeID})
	if err != nil {
		return err
	}
	_, err = chromedp.Call(ctx, runtime.CallFunctionOn, runtime.CallFunctionOnParams{FunctionDeclaration: "function(){" + body + "}", ObjectID: obj.Object.ObjectID})
	return err
}

func clickPanelButton(ctx context.Context, label string) error {
	return clickPanel(ctx, fmt.Sprintf("%q button", label), func(n *cdp.Node) bool {
		return n.NodeName == "BUTTON" && len(n.Children) == 1 && n.Children[0].NodeValue == label
	})
}

// extensionWorld returns the id of the extension's isolated world in the page,
// where the panel's own messages to the worker can be sent by hand. Turning the
// runtime domain off and on makes Chrome announce the existing worlds again.
func extensionWorld(ctx context.Context, extID string) (runtime.ExecutionContextID, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ev, err := chromedp.Run(ctx, chromedp.WaitEvent(runtime.ExecutionContextCreated,
		func(e runtime.EventExecutionContextCreated) bool {
			return strings.HasPrefix(e.Context.Origin, "chrome-extension://"+extID)
		},
		func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
			if _, err := cdp.Call(ctx, t, runtime.Disable, cdp.Empty{}); err != nil {
				return chromedp.Void{}, err
			}
			_, err := cdp.Call(ctx, t, runtime.Enable, cdp.Empty{})
			return chromedp.Void{}, err
		}))
	if err != nil {
		return 0, err
	}
	return ev.Context.ID, nil
}

// onFail runs when until gives up, to show what the page said.
var onFail func()

func until(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	if onFail != nil {
		onFail()
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestBrowser_PairAndFillASyntheticPage(t *testing.T) {
	h := newHarness(t, 200*time.Second)
	api, site, stamp, extID := h.api, h.site, h.stamp, h.extID
	swCtx := h.swCtx
	tab := h.tab

	// Pairing happens in the popup, with the one-time code.
	popupCtx := tab("chrome-extension://" + extID + "/popup.html")
	until(t, "the pairing form", 10*time.Second, func() bool {
		ok, _ := chromedp.Run(popupCtx, chromedp.Evaluate[bool](`!!document.getElementById("code")`))
		return ok
	})
	onFail = func() {
		text, _ := chromedp.Run(popupCtx, chromedp.Evaluate[string](`document.getElementById("app").textContent`))
		api.mu.Lock()
		t.Logf("popup says %q; claims=%d rejected=%v reads=%v", text, api.claims, api.rejected, api.reads)
		api.mu.Unlock()
	}
	defer func() { onFail = nil }()
	if _, err := eval[bool](popupCtx, fmt.Sprintf(`(() => { document.getElementById("code").value = %q; return true; })()`, api.code)); err != nil {
		t.Fatal(err)
	}
	h.popupPress(popupCtx, "Pair")
	until(t, "the connected state", 15*time.Second, func() bool { return strings.Contains(h.popupText(popupCtx), "Connected") })
	popupText := h.popupText(popupCtx)
	if !strings.Contains(popupText, stamp) {
		t.Errorf("the popup does not show the build %s: %q", stamp, popupText)
	}
	if strings.Contains(popupText, api.token) {
		t.Error("the popup shows the token")
	}

	// A page that is not an application gets nothing and sends nothing.
	api.mu.Lock()
	before := len(api.reads)
	api.mu.Unlock()
	newsCtx := tab(site.srv.URL + "/news/story")
	if err := chromedp.Do(newsCtx, chromedp.Sleep(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	panels, err := chromedp.Run(newsCtx, chromedp.Evaluate[int](`document.querySelectorAll('[data-applysmith]').length`))
	if err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	after := len(api.reads)
	api.mu.Unlock()
	if panels != 0 || after != before {
		t.Errorf("ordinary page: %d panels, %d requests", panels, after-before)
	}

	// The application page gets the panel.
	pageCtx := tab(site.srv.URL + "/careers/apply")
	if err := chromedp.Do(pageCtx, chromedp.WaitReady("[data-applysmith]")); err != nil {
		t.Fatal(err)
	}

	// Content scripts cannot read the token.
	probe := fmt.Sprintf(`(async () => {
	  const [t] = await chrome.tabs.query({url: %q});
	  const r = await chrome.scripting.executeScript({target: {tabId: t.id}, func: async () => {
	    try { return "readable:" + JSON.stringify(await chrome.storage.local.get("token")); } catch (e) { return "denied"; }
	  }});
	  return r[0].result;
	})()`, site.srv.URL+"/careers/*")
	verdict, err := eval[string](swCtx, probe)
	if err != nil {
		t.Fatal(err)
	}
	if verdict != "denied" || strings.Contains(verdict, api.token) {
		t.Errorf("a content script reads extension storage: %q", verdict)
	}

	// Open the panel as the popup does, then start the fill with its button.
	show := fmt.Sprintf(`(async () => {
	  const [t] = await chrome.tabs.query({url: %q});
	  return JSON.stringify(await chrome.runtime.sendMessage({kind: "panel.show", tabId: t.id}));
	})()`, site.srv.URL+"/careers/*")
	if _, err := eval[string](popupCtx, show); err != nil {
		t.Fatal(err)
	}
	// An application page opens the panel on Autofill; the arrow keys move
	// between the tabs (the last wraps to the first), and a tab with nothing to
	// list says so in words instead of staying blank.
	until(t, "the panel on Autofill", 15*time.Second, func() bool { return selectedPanelTab(pageCtx) == "Autofill" })
	for _, want := range []string{"Analyze", "Answer", "Résumé"} {
		if err := focusPanelNode(pageCtx, "selected tab", func(n *cdp.Node) bool {
			return n.NodeName == "BUTTON" && hasAttrValue(n, "role", "tab") && hasAttrValue(n, "aria-selected", "true")
		}); err != nil {
			t.Fatal(err)
		}
		if err := chromedp.Do(pageCtx, chromedp.KeyEvent(kb.ArrowRight)); err != nil {
			t.Fatal(err)
		}
		until(t, "the "+want+" tab", 5*time.Second, func() bool { return selectedPanelTab(pageCtx) == want })
		until(t, "words in the "+want+" tab", 15*time.Second, func() bool { return len(strings.Fields(visiblePanelText(pageCtx))) >= 4 })
	}
	until(t, "the Autofill tab", 15*time.Second, func() bool { return selectPanelTab(pageCtx, "Autofill") == nil })
	until(t, "the Start button", 15*time.Second, func() bool { return clickPanelButton(pageCtx, "Start") == nil })
	until(t, "the fill result", 60*time.Second, func() bool { return strings.Contains(panelText(pageCtx), "Dismiss result") })

	value := func(sel string) string {
		v, err := chromedp.Run(pageCtx, chromedp.Evaluate[string](fmt.Sprintf(`document.querySelector(%q).value`, sel)))
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	first, last, email, auth := value("#first_name"), value("#last_name"), value("#email"), value("#auth")
	if first != "Alex" || last != "Example" || email != "alex.example@example.com" || auth != "Yes" {
		t.Errorf("fields: %q %q %q %q", first, last, email, auth)
	}
	if n := site.submitted.Load(); n != 0 {
		t.Fatalf("the page was submitted %d times", n)
	}

	writes, _, _, matches := api.snapshot()
	if len(matches) == 0 {
		t.Fatal("the matcher was never asked")
	}
	run, _ := matches[0]["runId"].(string)
	if !strings.HasPrefix(run, "run_") {
		t.Errorf("match run id %q", run)
	}
	got := map[string]string{}
	for _, w := range writes {
		if w["runId"] != run {
			t.Errorf("system write for run %v, want %s", w["runId"], run)
		}
		got[w["selector"].(string)] = w["value"].(string)
	}
	until(t, "the run to end", 10*time.Second, func() bool { return len(api.endings()) > 0 })
	if ends := api.endings(); len(ends) != 1 || ends[0] != run+" succeeded" {
		t.Errorf("run endings %v", ends)
	}
	if got[`[id="first_name"]`] != "Alex" || got[`[id="email"]`] != "alex.example@example.com" {
		t.Errorf("system writes %v", got)
	}

	// What the person types afterwards: trusted edits are reported, secrets never.
	secret := "not-a" + "-real-secret"
	// Real key events from the browser, so the changes are trusted.
	typeInto := func(sel, keys string) {
		focus := fmt.Sprintf(`(() => { const el = document.querySelector(%q); el.focus(); el.setSelectionRange?.(el.value.length, el.value.length); })()`, sel)
		if err := chromedp.Do(pageCtx, chromedp.Evaluate[chromedp.Void](focus), chromedp.KeyEvent(keys)); err != nil {
			t.Fatal(err)
		}
	}
	typeInto("#preferred", "Lex"+kb.Tab)
	typeInto("#first_name", "a"+kb.Tab)
	typeInto("#gender", "Woman"+kb.Tab)
	typeInto("#pw", secret+kb.Tab)
	typed := time.Now()
	until(t, "the manual-fill events", 20*time.Second, func() bool {
		_, events, _, _ := api.snapshot()
		return len(events) >= 2
	})
	// The worker uploads after two quiet seconds, not on every key.
	if d := time.Since(typed); d < 1200*time.Millisecond {
		t.Errorf("the manual edits were uploaded %v after the last key, before the quiet period", d)
	}
	_, events, _, _ := api.snapshot()
	byQuestion := map[string]map[string]any{}
	for _, e := range events {
		byQuestion[fmt.Sprint(e["question"])] = e
	}
	pref := byQuestion["Preferred name"]
	if pref == nil || pref["kind"] != "manual-fill" || pref["after"] != "Lex" || pref["trustedInput"] != true || pref["runId"] != run || pref["selector"] == "" {
		t.Errorf("preferred name event: %v", pref)
	}
	if fn := byQuestion["First name"]; fn == nil || fn["before"] != "Alex" || fn["after"] != "Alexa" {
		t.Errorf("first name event: %v", fn)
	}
	api.mu.Lock()
	for _, body := range api.raw {
		if strings.Contains(body, secret) || strings.Contains(body, "Woman") {
			t.Errorf("a withheld value reached the API: %.120s", body)
		}
	}
	api.mu.Unlock()

	// The user's own click copies the site's sign-in, HttpOnly cookies included.
	pushed, err := eval[string](popupCtx, `(async () => JSON.stringify(await chrome.runtime.sendMessage({kind: "sync.host", host: "127.0.0.1"})))()`)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		OK   bool `json:"ok"`
		Data struct{ Imported int }
	}
	if err := json.Unmarshal([]byte(pushed), &out); err != nil || !out.OK || out.Data.Imported < 1 {
		t.Errorf("push reply %s (%v)", pushed, err)
	}
	_, _, pushes, _ := api.snapshot()
	if len(pushes) != 1 || !strings.Contains(fmt.Sprint(pushes[0]["cookies"]), "example-session-value") {
		t.Errorf("pushes: %v", pushes)
	}
	if strings.Contains(pushed, "example-session-value") {
		t.Error("the cookie value came back to the popup")
	}

	// The fill state survives the worker: stop the worker for real, then ask the
	// restarted one, as the panel does, what this tab's fill is.
	tabID := h.tabID(site.srv.URL + "/careers/*")
	if _, err := eval[string](h.swCtx, `(globalThis.__before = 1, "marked")`); err != nil {
		t.Fatal(err)
	}
	h.stopWorker(pageCtx)
	world, err := extensionWorld(pageCtx, extID)
	if err != nil {
		t.Fatal(err)
	}
	status := func() string {
		yes := true
		res, err := chromedp.Call(pageCtx, runtime.Evaluate, runtime.EvaluateParams{
			Expression:   `chrome.runtime.sendMessage({kind: "fill.status"}).then(r => JSON.stringify(r))`,
			ContextID:    world,
			AwaitPromise: &yes,
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.ExceptionDetails != nil || res.Result == nil {
			t.Fatalf("the status call threw: %+v", res.ExceptionDetails)
		}
		var out string
		if err := json.Unmarshal(res.Result.Value, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	var revived struct {
		OK   bool `json:"ok"`
		Data struct {
			Phase  string `json:"phase"`
			RunID  string `json:"runId"`
			Counts *struct{ Filled int }
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(status()), &revived); err != nil {
		t.Fatal(err)
	}
	if !revived.OK || revived.Data.Phase != "done" || revived.Data.RunID != run || revived.Data.Counts == nil || revived.Data.Counts.Filled < 1 {
		t.Errorf("after a worker restart the fill is %+v, want done as %s", revived, run)
	}
	// A new worker is a new target with empty memory.
	until(t, "the restarted worker", 10*time.Second, h.findWorker)
	swCtx = h.swCtx
	if left, err := eval[string](swCtx, `String(globalThis.__before)`); err != nil || left != "undefined" {
		t.Errorf("the worker was not restarted: %q (%v)", left, err)
	}
	if kept, err := eval[bool](swCtx, fmt.Sprintf(`chrome.storage.session.get("fill:%d").then(r => Object.keys(r).length === 1)`, tabID)); err != nil || !kept {
		t.Errorf("the fill record is not in session storage: %v (%v)", kept, err)
	}

	// Without the option the walk stops before the final Submit and asks for no claim.
	if err := clickPanelButton(pageCtx, "Walk the steps"); err != nil {
		t.Fatal(err)
	}
	until(t, "the walk to stop before Submit", 60*time.Second, func() bool { return strings.Contains(panelText(pageCtx), "Review the form and press it yourself.") })
	if claims := api.pressClaims(); len(claims) != 0 || site.submitted.Load() != 0 {
		t.Errorf("a walk without the option asked for %d claims and submitted %d times", len(claims), site.submitted.Load())
	}

	// The step walker with the submit option on asks for a press claim; the
	// stub refuses, so nothing is pressed and the panel says why.
	if err := clickPanel(pageCtx, "walk option", func(n *cdp.Node) bool { return n.NodeName == "INPUT" && hasAttrValue(n, "id", "walk-submit") }); err != nil {
		t.Fatal(err)
	}
	if err := clickPanelButton(pageCtx, "Walk the steps"); err != nil {
		t.Fatal(err)
	}
	until(t, "the press claim to be refused", 60*time.Second, func() bool { return strings.Contains(panelText(pageCtx), "Submit was not pressed.") })
	if text := panelText(pageCtx); !strings.Contains(text, "This posting was already submitted.") {
		t.Errorf("the refusal reason is not shown: %.300s", text)
	}
	claims := api.pressClaims()
	if len(claims) != 1 || claims[0]["host"] != "127.0.0.1" || !strings.HasSuffix(claims[0]["url"].(string), "/careers/apply") {
		t.Errorf("press claims: %v", claims)
	}
	if n := site.submitted.Load(); n != 0 {
		t.Errorf("Submit was pressed %d times after a refused claim", n)
	}
}
