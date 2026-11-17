//go:build dashboard && browser

package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"
)

// modelsPath is the Models and providers section of Settings.
const modelsPath = "/settings#settings-models"

// closeDialogsJS closes whatever dialog an earlier scenario left open, the way
// Escape does, so the next one starts from the section itself.
const closeDialogsJS = `for (const d of document.querySelectorAll("dialog[open]")) d.dispatchEvent(new Event("cancel", { cancelable: true }));`

// modelsRootJS finds the section, which every scenario below works inside.
const modelsRootJS = `const root = document.querySelector("section[aria-labelledby=settings-models]");`

// overlapJS lists every pair of visible controls and label texts in the open
// dialog, or else the Models section, whose boxes cross: a sticky bar over a
// field, a button over a label. Controls inside a closed disclosure are not
// drawn and are left out; a control and the label that wraps it are not a pair
// (the label is measured by its own text). The sticky footer of a drawer is left
// out: the form scrolls under it by design.
const overlapJS = `(() => {
  const root = document.querySelector("dialog[open]") || document.querySelector("section[aria-labelledby=settings-models]");
  const closed = (e) => { for (let d = e.closest("details"); d; d = d.parentElement && d.parentElement.closest("details")) { if (!d.open && !(e.closest("summary") && e.closest("details") === d)) return true; } return false; };
  const boxes = [];
  const name = (e) => e.tagName.toLowerCase() + ' "' + (e.getAttribute("aria-label") || e.textContent || e.value || e.placeholder || "").trim().replace(/\s+/g, " ").slice(0, 36) + '"';
  const add = (e, r) => { if (r.width > 1 && r.height > 1) boxes.push({ e, r }); };
  for (const e of root.querySelectorAll("button, input:not([type=hidden]), select, textarea, summary, a[href]")) {
    const cs = getComputedStyle(e);
    if (cs.visibility === "hidden" || cs.display === "none" || closed(e) || e.closest(".sr-only, [hidden], .foot")) continue;
    add(e, e.getBoundingClientRect());
  }
  for (const l of root.querySelectorAll("label")) {
    if (closed(l) || l.closest(".sr-only, [hidden]")) continue;
    const range = document.createRange();
    for (const n of l.childNodes) {
      if (n.nodeType !== Node.TEXT_NODE || !n.textContent.trim()) continue;
      range.selectNodeContents(n);
      for (const r of range.getClientRects()) add(l, r);
    }
  }
  const out = [];
  for (let i = 0; i < boxes.length; i++) for (let j = i + 1; j < boxes.length; j++) {
    const a = boxes[i], b = boxes[j];
    if (a.e === b.e || a.e.contains(b.e) || b.e.contains(a.e)) continue;
    const w = Math.min(a.r.right, b.r.right) - Math.max(a.r.left, b.r.left);
    const h = Math.min(a.r.bottom, b.r.bottom) - Math.max(a.r.top, b.r.top);
    if (w > 2 && h > 2) out.push(name(a.e) + " overlaps " + name(b.e));
  }
  return [...new Set(out)];
})()`

// pressJS presses the first button of root whose text or label starts with label.
func pressJS(label string) string {
	return fmt.Sprintf(`[...root.querySelectorAll("button")].find((b) => (b.getAttribute("aria-label") || b.textContent).trim().startsWith(%q))?.click();`, label)
}

// The whole Models section must fit when everything in it is open, not only
// while the Settings page hides its controls: every disclosure of the accounts
// and of the jobs, the add-an-account form, then the job editor and the
// change-the-default dialog, each at the three widths. layoutProblems holds the
// same checks as the dashboard layout test: no sideways scroll, no control cut
// off or lying outside the window.
//
// APPLYSMITH_LAYOUT_SHOTS names a directory to write default_<width>.png (the
// section as first drawn) and after_<width>.png (every disclosure open) into,
// for looking at the page.
func TestModelsSectionLayoutFitsThreeWidths(t *testing.T) {
	te := newTestEnv(t)
	if out := te.run(context.Background(), "demo", "init", "--workspace", te.ws); out.code != 0 {
		t.Fatalf("demo init failed: %d", out.code)
	}
	te.stdin = "synthetic-layout-provider-key\n"
	if out := te.run(context.Background(), "llm", "configure", "--preset", "deepseek", "--key-stdin", "--workspace", te.ws); out.code != 0 {
		t.Fatalf("provider configuration failed: %d", out.code)
	}
	// One job with its own model and a backup, so the page shows both kinds of row.
	if out := te.run(context.Background(), "llm", "set", "deep-eval", "deepseek/deepseek-v4-pro", "--fallback", "deepseek/deepseek-flash", "--workspace", te.ws); out.code != 0 {
		t.Fatalf("route configuration failed: %d", out.code)
	}
	rs := te.startServe()
	ctx := layoutChrome(t, layoutWidths[0])
	if err := chromedp.Do(ctx, chromedp.Navigate(rs.link())); err != nil {
		t.Fatal(err)
	}
	scenarios := []struct{ name, open, shot string }{
		{"as first drawn", "", "default"},
		{"everything open", closeDialogsJS + modelsRootJS + `for (const d of root.querySelectorAll("details")) d.open = true;` + pressJS("Add another account"), "after"},
		{"job editor", closeDialogsJS + modelsRootJS + pressJS("Change quick-screen") + `setTimeout(() => { for (const d of document.querySelectorAll("dialog[open] details")) d.open = true; }, 100)`, "editor"},
		{"change the default", closeDialogsJS + modelsRootJS + pressJS("Change default"), "default-dialog"},
	}
	for i := range scenarios {
		if scenarios[i].open != "" {
			scenarios[i].open = "(() => {" + scenarios[i].open + "})()"
		}
	}
	for _, width := range layoutWidths {
		t.Run(fmt.Sprintf("%dpx", width), func(t *testing.T) {
			if err := setWidth(ctx, width); err != nil {
				t.Fatal(err)
			}
			if err := chromedp.Do(ctx, chromedp.Navigate(rs.origin+"/")); err != nil {
				t.Fatal(err)
			}
			for _, s := range scenarios {
				problems, err := layoutProblems(ctx, rs.origin, modelsPath, false, s.open)
				if err != nil {
					t.Errorf("%s: %v", s.name, err)
					continue
				}
				for _, p := range problems {
					t.Errorf("%s: %s", s.name, p)
				}
				overlaps, err := chromedp.Run(ctx, chromedp.Evaluate[[]string](overlapJS))
				if err != nil {
					t.Errorf("%s: %v", s.name, err)
				}
				for _, p := range overlaps {
					t.Errorf("%s: %s", s.name, p)
				}
				if dir := os.Getenv("APPLYSMITH_LAYOUT_SHOTS"); dir != "" && s.shot != "" {
					shot := chromedp.Screenshot("section[aria-labelledby=settings-models]")
					if s.shot == "editor" || s.shot == "default-dialog" {
						shot = chromedp.CaptureScreenshot()
					}
					png, err := chromedp.Run(ctx, shot)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s_%d.png", s.shot, width)), png, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}
