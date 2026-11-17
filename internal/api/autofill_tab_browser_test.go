//go:build browser

package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/browser"
	"applysmith/internal/workspace"
)

const formHTML = `<!doctype html><html><head><title>Apply</title></head><body>
<h1>Apply for Platform Engineer</h1>
<form id="f" onsubmit="window.__submits=(window.__submits||0)+1;fetch('/submit',{method:'POST'});return false">
<p><label for="first">First name</label><input id="first" name="first_name" type="text" required></p>
<p><label for="last">Last name</label><input id="last" name="last_name" type="text" required></p>
<p><label for="email">Email</label><input id="email" name="email" type="email" required></p>
<p><label for="phone">Phone</label><input id="phone" name="phone" type="tel"></p>
<p><label for="li">LinkedIn profile</label><input id="li" name="linkedin" type="url"></p>
<p><label for="city">City</label><input id="city" name="city" type="text"></p>
<p><label for="why">Why do you want to work at Zebrafish Quarry Holdings?</label><textarea id="why" name="why" required></textarea></p>
<p><label for="ref">Referral code</label><input id="ref" name="referral" type="text"></p>
<button type="button" onclick="location.href='/next'">Next</button>
<button type="submit">Submit application</button>
</form></body></html>`

const articleHTML = `<!doctype html><html><head><title>News</title></head><body>
<h1>Local council approves the new bridge</h1><p>The council met on Tuesday.</p>
<form role="search"><input id="q" type="search" placeholder="Search the site"></form></body></html>`

// TestAutofillTab_RealChrome verifies the three scenarios of "Autofill this
// tab" in a headless Chrome on a throwaway profile, against pages served from
// loopback: Fill once, Not an application and Run in progress.
func TestAutofillTab_RealChrome(t *testing.T) {
	chrome, err := browser.DiscoverChrome(context.Background(), os.Getenv)
	if err != nil {
		t.Skipf("no usable Google Chrome: %v", err)
	}
	var hits atomic.Int32 // requests that would move or submit the form
	mux := http.NewServeMux()
	mux.HandleFunc("/apply", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, formHTML) })
	mux.HandleFunc("/news", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, articleHTML) })
	mux.HandleFunc("/next", func(http.ResponseWriter, *http.Request) { hits.Add(1) })
	mux.HandleFunc("/submit", func(http.ResponseWriter, *http.Request) { hits.Add(1) })
	site := httptest.NewServer(mux)
	t.Cleanup(site.Close)

	dir := t.TempDir()
	profile := filepath.Join(dir, "applysmith-browser-test-profile")
	opts := browser.Options{Chrome: chrome, ProfileDir: profile, StateDir: filepath.Join(dir, "state"), DownloadsDir: filepath.Join(dir, "downloads"),
		Environ: os.Environ()}.WithHeadless()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	inst, err := browser.Open(ctx, opts)
	if err != nil {
		t.Skipf("browser unavailable: %v", err)
	}
	t.Cleanup(func() {
		stop, c := context.WithTimeout(context.Background(), 30*time.Second)
		defer c()
		inst.Stop(stop)
		if out, _ := exec.Command("ps", "-Ao", "command=").Output(); strings.Contains(string(out), "user-data-dir="+profile) {
			t.Errorf("a Chrome of the test profile is still running")
		}
	})
	lane := inst.Lane(browser.LaneApply)

	h := newDomainHarness(t, func(c *Config) {
		c.ApplyWindow = func(context.Context) (apply.Window, error) { return apply.LaneWindow{Lane: lane}, nil }
	})
	profileYAML := "version: 1\ncandidate:\n  full_name: Alex Example\n  email: alex@example.com\n  phone: \"+1 555 0100\"\n  address:\n    city: Durham\n    region: NC\n    country: US\n"
	if err := os.WriteFile(h.ws.Path(workspace.FileProfile), []byte(profileYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	tab, err := lane.OpenTab(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	read := func(expr string) string {
		t.Helper()
		raw, err := lane.Evaluate(ctx, tab, expr)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Trim(string(raw), `"`)
	}
	open := func(path string) {
		t.Helper()
		if _, err := lane.Navigate(ctx, tab, site.URL+path); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("Fill once", func(t *testing.T) {
		open("/apply")
		r := h.post("/api/autofill/active-tab", nil)
		if r.StatusCode != 202 {
			t.Fatalf("%d %s", r.StatusCode, r.body)
		}
		got := waitFill(t, h, decode[wire.RunAccepted](t, r).RunID)
		if got.Status != wire.RunSucceeded {
			t.Fatalf("result = %+v", got)
		}
		labels := func(cs []wire.AutofillControl) string {
			var out []string
			for _, c := range cs {
				out = append(out, c.Label)
			}
			return strings.Join(out, "|")
		}
		if !strings.Contains(labels(got.Filled), "First name") || !strings.Contains(labels(got.Filled), "Email") {
			t.Errorf("the profile fields were not filled: %+v", got)
		}
		if !strings.Contains(labels(got.Missing), "Why do you want to work") {
			t.Errorf("the essay is the one thing it could not answer: %+v", got)
		}
		if read(`document.getElementById('first').value`) != "Alex" || read(`document.getElementById('email').value`) != "alex@example.com" {
			t.Errorf("the page does not hold the answers")
		}
		if read(`location.pathname`) != "/apply" || read(`String(window.__submits||0)`) != "0" || hits.Load() != 0 {
			t.Errorf("the fill moved or submitted the page (hits %d)", hits.Load())
		}
		if n := len(lane.Tabs()); n != 1 {
			t.Errorf("the tab was closed: %d tabs", n)
		}
	})

	t.Run("Not an application", func(t *testing.T) {
		open("/news")
		r := h.post("/api/autofill/active-tab", nil)
		if r.StatusCode != 422 || r.errReason() != wire.ReasonNotAnApp {
			t.Fatalf("%d %s", r.StatusCode, r.body)
		}
		if read(`document.getElementById('q').value`) != "" || h.domainOf().apply.fills.any() {
			t.Errorf("something was written or reserved")
		}
	})

	t.Run("Run in progress", func(t *testing.T) {
		open("/apply")
		// StartApply sets this flag for as long as a run drives the window.
		h.domainOf().apply.busy.Store(true)
		defer h.domainOf().apply.busy.Store(false)
		r := h.post("/api/autofill/active-tab", nil)
		if r.StatusCode != 409 || r.errReason() != wire.ReasonLaneBusy {
			t.Fatalf("%d %s", r.StatusCode, r.body)
		}
		if read(`document.getElementById('first').value`) != "" {
			t.Errorf("the tab was touched")
		}
	})
}
