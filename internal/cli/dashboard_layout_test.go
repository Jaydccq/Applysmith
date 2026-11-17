//go:build dashboard && browser

package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"

	"applysmith/internal/browser"
)

// Layout check: every dashboard page, opened fresh at three widths in a
// headless Chrome with a throwaway profile, must not scroll the document
// sideways and must not cut off a control. It runs in `make test-browser`.

// layoutWidths: a phone (the bottom tab bar), the first width with the sidebar,
// and a desktop.
var layoutWidths = []int{360, 768, 1280}

// layoutPaths is every page route and each tab or section a page keeps in its
// address, plus the drawers the address opens.
var layoutPaths = []string{
	"/", "/apply-next", "/tracker", "/tracker?row=1", "/reports",
	"/discovery?tab=run", "/discovery?tab=monitor", "/discovery?tab=inbox", "/discovery?tab=history", "/discovery?tab=schedules", "/discovery?tab=sources", "/discovery?tab=blocked",
	"/leads?tab=leads", "/leads?tab=signals", "/leads?tab=review",
	"/answer-bank?tab=entries", "/answer-bank?tab=employers", "/answer-bank?tab=candidates", "/answer-bank?tab=usage", "/answer-bank?tab=settings",
	"/profile?tab=cv", "/profile?tab=narrative", "/profile?tab=voice", "/profile?tab=resumes", "/profile?tab=locations", "/profile?tab=authorization", "/profile?tab=identity",
	"/wiki?tab=pages", "/wiki?tab=briefs", "/wiki?tab=stories",
	"/ask?tab=chat", "/ask?tab=approvals", "/ask?tab=goals", "/ask?tab=triggers", "/ask?tab=memory", "/agent-control",
	"/runs", "/browser", "/gmail?tab=inbox", "/gmail?tab=briefs", "/gmail?tab=review", "/usage", "/usage?tab=live",
	"/settings#settings-models", "/settings#settings-applications", "/settings#settings-documents", "/settings#settings-risk", "/settings#settings-scan", "/settings#settings-gate", "/settings#settings-notifications", "/settings#settings-remote", "/settings#settings-files",
	"/settings#settings-advanced", "/settings#settings-gmail", "/settings#settings-askagent", "/settings#settings-extension", "/settings#settings-signin", "/settings#settings-captcha",
	"/settings#settings-telegram", "/settings#settings-quiet", "/settings#settings-runtime", "/settings#settings-computeruse",
	"/setup", "/guide", "/guide?doc=tutorial", "/guide?doc=apply", "/not-a-page",
}

// layoutExtras opens something on a page and checks it as well: the command
// palette, the browser panel, the Notices drawer and the sidebar narrowed to
// icons at every width (a phone has no sidebar, so nothing changes there), and
// the "More" sheet of the tab bar on a phone.
var layoutExtras = []struct {
	name   string
	narrow bool
	open   string
}{
	{"command palette", false, `document.querySelector(".palette-button")?.click()`},
	{"More sheet", true, `document.querySelector("nav.tabbar button[aria-expanded]")?.click()`},
	{"browser panel", false, `document.querySelector("button[aria-label='Browser panel']")?.click()`},
	{"narrow sidebar", false, `document.querySelector("button[aria-label='Narrow sidebar']")?.click()`},
	{"narrow sidebar, a link focused", false, `document.querySelector("button[aria-label='Narrow sidebar']")?.click(); setTimeout(() => document.querySelector("nav.sidebar a[href='/usage']")?.focus(), 50)`},
	{"Notices drawer", false, `document.querySelector(".notices-button")?.click()`},
}

// layoutCheckJS reports each problem it finds, one string each.
//
//   - the document is wider than the window;
//   - a control lies beyond the window with nothing around it to scroll;
//   - a control is cut by an ancestor that hides what overflows it (a
//     scrolling ancestor is fine: the control can be reached);
//   - a button or link shows less than its own label, without an ellipsis;
//   - beside a docked browser panel, a control of the page lies under it.
const layoutCheckJS = `(() => {
  const out = [];
  const doc = document.documentElement;
  const vw = doc.clientWidth;
  const page = document.querySelector(".shell.browser-docked:not(.browser-full) > main");
  const pageEdge = page ? page.getBoundingClientRect().right : vw;
  const name = (e) => {
    const cls = typeof e.className === "string" ? e.className.trim().split(/\s+/).slice(0, 2).join(".") : "";
    const label = (e.getAttribute("aria-label") || e.textContent || e.value || "").trim().replace(/\s+/g, " ").slice(0, 36);
    return e.tagName.toLowerCase() + (cls ? "." + cls : "") + ' "' + label + '"';
  };
  if (doc.scrollWidth > vw) {
    // The widest elements that no scrolling ancestor holds are the ones to look at.
    const past = [...document.body.querySelectorAll("*")]
      .filter((e) => !(e instanceof SVGElement) && e.getBoundingClientRect().right > vw + 1 && e.getBoundingClientRect().width > 0)
      .filter((e) => { for (let p = e.parentElement; p && p !== doc; p = p.parentElement) { const o = getComputedStyle(p).overflowX; if (o === "auto" || o === "scroll" || o === "hidden" || o === "clip") return false; } return true; })
      .slice(0, 3).map(name);
    out.push("document is " + doc.scrollWidth + "px wide in a " + vw + "px window; past the edge: " + past.join(", "));
  }
  const hides = (v) => v === "hidden" || v === "clip";
  const scrolls = (v) => v === "auto" || v === "scroll";
  // along walks up from a control on one axis: a scrolling ancestor makes the
  // control reachable, an ancestor that hides its overflow cuts it when the
  // control lies beyond that ancestor's box.
  const along = (e, r, sideways) => {
    for (let p = e.parentElement; p && p !== doc; p = p.parentElement) {
      const pcs = getComputedStyle(p);
      if (pcs.display === "contents") continue;
      const overflow = sideways ? pcs.overflowX : pcs.overflowY;
      const pr = p.getBoundingClientRect();
      const grows = sideways ? p.scrollWidth > p.clientWidth + 1 : p.scrollHeight > p.clientHeight + 1;
      if (scrolls(overflow) && grows) return { reachable: true };
      const beyond = sideways ? r.left < pr.left - 1 || r.right > pr.right + 1 : r.top < pr.top - 1 || r.bottom > pr.bottom + 1;
      if (hides(overflow) && beyond) return { cut: "cut " + (sideways ? "sideways" : "vertically") + " by " + name(p) };
      if (pcs.position === "fixed") break;
    }
    return {};
  };
  const controls = document.querySelectorAll('a[href], button, input:not([type=hidden]), select, textarea, summary, [role=tab], [role=button], [role=menuitem], [role=option]');
  for (const e of controls) {
    if (e.closest(".sr-only, .skip, [hidden], [inert]")) continue;
    const cs = getComputedStyle(e);
    if (cs.visibility === "hidden" || cs.display === "none") continue;
    const r = e.getBoundingClientRect();
    if (r.width === 0 || r.height === 0) continue;
    const x = along(e, r, true);
    const y = along(e, r, false);
    if (x.cut || y.cut) out.push(name(e) + " " + (x.cut || y.cut));
    else if (!x.reachable && (r.right > vw + 1 || r.left < -1)) out.push(name(e) + " lies outside the window (" + Math.round(r.left) + " to " + Math.round(r.right) + " of " + vw + ")");
    else if (!x.reachable && page && page.contains(e) && !e.closest("dialog") && r.right > pageEdge + 1) out.push(name(e) + " lies under the browser panel (" + Math.round(r.right) + " past " + Math.round(pageEdge) + ")");
    if ((e.tagName === "BUTTON" || e.tagName === "A") && e.scrollWidth > e.clientWidth + 1 && !scrolls(cs.overflowX) && cs.textOverflow !== "ellipsis" && cs.overflowX !== "visible") out.push(name(e) + " shows less than its label (" + e.scrollWidth + " > " + e.clientWidth + ")");
  }
  return [...new Set(out)];
})()`

// layoutReadyJS resolves once the page has drawn: the shell is up, the page is
// revealed and no skeleton or busy region is left for a moment.
const layoutReadyJS = `new Promise((resolve) => {
  const start = performance.now();
  let quiet = 0;
  const tick = () => {
    const settle = document.querySelector(".settle");
    const done = document.querySelector("main") && (!settle || settle.dataset.settled === "true") && !document.querySelector(".skeleton, [aria-busy=true]");
    quiet = done ? quiet + 1 : 0;
    if (quiet >= 4 || performance.now() - start > 8000) resolve(quiet >= 4);
    else setTimeout(tick, 50);
  };
  tick();
})`

// layoutChrome starts the installed Google Chrome headless with a throwaway
// profile and returns a tab context at the given width. Everything is torn
// down when the test ends.
func layoutChrome(t *testing.T, width int) context.Context {
	t.Helper()
	chrome, err := browser.DiscoverChrome(context.Background(), os.Getenv)
	if err != nil {
		t.Skipf("no usable Google Chrome: %v", err)
	}
	profile, err := os.MkdirTemp("", "applysmith-browser-test-layout-*")
	if err != nil {
		t.Fatal(err)
	}
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		chromedp.ExecPath(chrome.Path), chromedp.UserDataDir(profile),
		chromedp.NoFirstRun, chromedp.NoDefaultBrowserCheck,
		chromedp.Flag("headless", "new"), chromedp.Flag("disable-gpu", true), chromedp.Flag("mute-audio", true),
		// No app-bundle copy that a killed Chrome would leave behind (docs/architecture/browser.md).
		chromedp.Flag("disable-features", "MacAppCodeSignClone"),
		// Off the macOS Keychain: storing the session cookie otherwise waits on
		// a Keychain prompt, which nobody answers while the screen is locked.
		chromedp.Flag("password-store", "basic"), chromedp.Flag("use-mock-keychain", true),
		chromedp.WindowSize(width, 900),
	)
	ctx, cancelCtx := chromedp.NewContext(alloc)
	ctx, cancelTimeout := context.WithTimeout(ctx, 15*time.Minute)
	t.Cleanup(func() {
		cancelTimeout()
		cancelCtx()
		cancelAlloc()
		_ = os.RemoveAll(profile)
	})
	if err := chromedp.Do(ctx); err != nil {
		t.Fatalf("start Chrome: %v", err)
	}
	// A headless page has no focus, so a focused control would not look
	// focused; the focus-visible check below needs it to.
	if _, err := chromedp.Call(ctx, emulation.SetFocusEmulationEnabled, emulation.SetFocusEmulationEnabledParams{Enabled: true}); err != nil {
		t.Fatalf("emulate focus: %v", err)
	}
	return ctx
}

// setWidth sets the layout width of the tab; the height does not matter to a
// sideways check.
func setWidth(ctx context.Context, width int) error {
	return chromedp.Do(ctx, chromedp.EmulateViewport(int64(width), 900))
}

// goToJS moves the open dashboard to another page the way a link does, with
// no new document: a page opened afresh in the same Chrome leaves the board
// stream of the last one open, and after six of them the browser holds the next
// page back for most of a minute.
const goToJS = `((path) => {
  history.pushState(null, "", path);
  dispatchEvent(new PopStateEvent("popstate"));
  dispatchEvent(new HashChangeEvent("hashchange"));
  scrollTo(0, 0);
})`

// layoutProblems shows path (a fresh document when fresh is set, else the
// page the dashboard is already running), waits for it to draw and returns
// what the check found, with the readiness verdict first when the page never
// settled. open runs after the page has drawn, to open something to check.
func layoutProblems(ctx context.Context, origin, path string, fresh bool, open string) ([]string, error) {
	if fresh {
		if err := chromedp.Do(ctx, chromedp.Navigate(origin+path)); err != nil {
			return nil, err
		}
	} else {
		if _, err := chromedp.Run(ctx, chromedp.Evaluate[chromedp.Void](fmt.Sprintf("%s(%q)", goToJS, path))); err != nil {
			return nil, err
		}
		// Let the router render the page before asking whether it has drawn.
		time.Sleep(150 * time.Millisecond)
	}
	return layoutDrawnProblems(ctx, open)
}

// layoutDrawnProblems is the check of the page that is on screen.
func layoutDrawnProblems(ctx context.Context, open string) ([]string, error) {
	var problems []string
	ready, err := chromedp.Run(ctx, chromedp.Evaluate[bool](layoutReadyJS, chromedp.EvalAwaitPromise))
	if err != nil {
		return nil, err
	}
	if !ready {
		problems = append(problems, "the page did not finish drawing in 8 s")
	}
	if open != "" {
		if _, err := chromedp.Run(ctx, chromedp.Evaluate[chromedp.Void](open)); err != nil {
			return nil, err
		}
		time.Sleep(250 * time.Millisecond)
	}
	found, err := chromedp.Run(ctx, chromedp.Evaluate[[]string](layoutCheckJS))
	if err != nil {
		return nil, err
	}
	return append(problems, found...), nil
}

// seedLayoutWorkspace makes a demo workspace and fills the tracker with rows
// whose text is as long as real postings run, so a page that does not shrink
// its table shows it. APPLYSMITH_LAYOUT_WORKSPACE names a throwaway copy of a
// richer workspace to check instead; it is written to like any served one.
func seedLayoutWorkspace(t *testing.T, te *testEnv) {
	t.Helper()
	if dir := os.Getenv("APPLYSMITH_LAYOUT_WORKSPACE"); dir != "" {
		te.ws = dir
		return
	}
	ctx := context.Background()
	if o := te.run(ctx, "demo", "init", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("demo init: %+v", o)
	}
	rows := []struct{ company, role, status string }{
		{"Tidewater Analytics", "Senior Staff Machine Learning Platform Engineer, Applied Research Infrastructure", "evaluated"},
		{"Harborlight Systems International Holdings", "Backend Engineer, Payments Reliability", "applied"},
		{"Northwind Labs", "Principal Software Engineer, Distributed Data Systems and Streaming Infrastructure", "responded"},
		{"Fabrikam", "Platform Engineer", "interview"},
		{"Massive Dynamic", "Senior Software Engineer, Developer Productivity and Build Infrastructure (Bazel, Remote Execution)", "offer"},
		{"Gringotts Ledger Services", "Backend Engineer, Risk", "rejected"},
		{"Contoso", "Site Reliability Engineer", "discarded"},
	}
	for i, r := range rows {
		url := fmt.Sprintf("https://jobs.example.com/layout/%d-%s", i, strings.ToLower(strings.Fields(r.company)[0]))
		args := []string{"tracker", "add", "--workspace", te.ws, "--company", r.company, "--role", r.role, "--url", url, "--location", "Remote, United States", "--status", r.status}
		if o := te.run(ctx, args...); o.code != 0 {
			t.Fatalf("tracker add %s: %+v", r.company, o)
		}
	}
}

// Scenario "Layout at 360, 768 and 1280": no page scrolls sideways or cuts a
// control off at any of the three widths.
func TestDashboardLayoutFitsThreeWidths(t *testing.T) {
	te := newTestEnv(t)
	seedLayoutWorkspace(t, te)
	rs := te.startServe()
	ctx := layoutChrome(t, layoutWidths[0])
	// The printed link opens once and sets the session; every later load of a
	// page uses that session.
	if err := chromedp.Do(ctx, chromedp.Navigate(rs.link())); err != nil {
		t.Fatal(err)
	}
	for _, width := range layoutWidths {
		t.Run(fmt.Sprintf("%dpx", width), func(t *testing.T) {
			if err := setWidth(ctx, width); err != nil {
				t.Fatal(err)
			}
			if err := chromedp.Do(ctx, chromedp.Navigate(rs.origin+"/")); err != nil {
				t.Fatal(err)
			}
			for _, path := range layoutPaths {
				began := time.Now()
				problems, err := layoutProblems(ctx, rs.origin, path, false, "")
				if d := time.Since(began); d > 2*time.Second {
					t.Logf("%s took %v", path, d.Round(time.Millisecond))
				}
				if err != nil {
					t.Errorf("%s: %v", path, err)
					continue
				}
				for _, p := range problems {
					t.Errorf("%s: %s", path, p)
				}
			}
			for _, x := range layoutExtras {
				if x.narrow && width >= 768 {
					continue
				}
				problems, err := layoutProblems(ctx, rs.origin, "/", true, x.open)
				// The panel and the sidebar remember their state; the next one starts without it.
				_, _ = chromedp.Run(ctx, chromedp.Evaluate[chromedp.Void](`localStorage.removeItem("applysmith-browser-panel"); localStorage.removeItem("applysmith-sidebar")`))
				if err != nil {
					t.Errorf("%s: %v", x.name, err)
					continue
				}
				for _, p := range problems {
					t.Errorf("%s: %s", x.name, p)
				}
			}
		})
	}
}

// Scenario "Pages beside the browser panel": at 1280 (the panel narrowed to
// leave the page its room) and at 1440 the panel docks, and every page lays
// out in the room left to it, with nothing under the panel.
func TestDashboardLayoutFitsBesideTheBrowserPanel(t *testing.T) {
	te := newTestEnv(t)
	seedLayoutWorkspace(t, te)
	rs := te.startServe()
	ctx := layoutChrome(t, 1440)
	if err := chromedp.Do(ctx, chromedp.Navigate(rs.link())); err != nil {
		t.Fatal(err)
	}
	for _, width := range []int{1280, 1440} {
		t.Run(fmt.Sprintf("%dpx", width), func(t *testing.T) {
			if err := setWidth(ctx, width); err != nil {
				t.Fatal(err)
			}
			_, _ = chromedp.Run(ctx, chromedp.Evaluate[chromedp.Void](`localStorage.removeItem("applysmith-browser-panel")`))
			problems, err := layoutProblems(ctx, rs.origin, "/", true, `document.querySelector("button[aria-label='Browser panel']")?.click()`)
			if err != nil {
				t.Fatal(err)
			}
			docked, err := chromedp.Run(ctx, chromedp.Evaluate[bool](`!!document.querySelector(".shell.browser-docked")`))
			if err != nil || !docked {
				t.Fatalf("the panel does not dock: %v", err)
			}
			for _, p := range problems {
				t.Errorf("/: %s", p)
			}
			for _, path := range layoutPaths {
				problems, err := layoutProblems(ctx, rs.origin, path, false, "")
				if err != nil {
					t.Errorf("%s: %v", path, err)
					continue
				}
				for _, p := range problems {
					t.Errorf("%s: %s", path, p)
				}
			}
		})
	}
}

// The check reports what it claims to, and nothing for a page that fits: a
// check that never fails would pass every page.
func TestLayoutCheckReportsOverflowAndClippedControls(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/wide":
			fmt.Fprint(w, `<!doctype html><meta name=viewport content="width=device-width"><main><div style="width:900px;height:20px"></div><button style="margin-left:700px">Far away</button></main>`)
		case "/clipped":
			fmt.Fprint(w, `<!doctype html><meta name=viewport content="width=device-width"><main><div style="width:120px;overflow:hidden"><button style="margin-left:100px;white-space:nowrap">Save the whole thing</button></div><button style="width:40px;overflow:hidden;white-space:nowrap">A long label</button></main>`)
		default:
			fmt.Fprint(w, `<!doctype html><meta name=viewport content="width=device-width"><main><div style="width:200px;overflow-x:auto"><button style="margin-left:300px">Scrolls into reach</button></div><button style="max-width:100px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">An ellipsis is a choice</button></main>`)
		}
	}))
	t.Cleanup(site.Close)
	ctx := layoutChrome(t, 360)
	for _, tc := range []struct {
		path string
		want []string
	}{
		{"/fits", nil},
		{"/wide", []string{"document is", "lies outside the window"}},
		{"/clipped", []string{"cut sideways by", "shows less than its label"}},
	} {
		got, err := layoutProblems(ctx, site.URL, tc.path, true, "")
		if err != nil {
			t.Fatal(err)
		}
		// The page has no .skeleton and no settle region, so it counts as drawn.
		if len(tc.want) == 0 && len(got) != 0 {
			t.Errorf("%s: reported %v for a page that fits", tc.path, got)
		}
		for _, w := range tc.want {
			if !strings.Contains(strings.Join(got, "\n"), w) {
				t.Errorf("%s: no problem containing %q in %v", tc.path, w, got)
			}
		}
	}
}
