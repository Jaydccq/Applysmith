//go:build browser

package apply

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/credentials"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
)

type loginScenario struct {
	g    *e2e
	b    *board
	url  string
	lane *browser.LaneHandle
}

func newLogin(t *testing.T, accept string) *loginScenario {
	t.Helper()
	inst, st := sharedBrowser(t)
	lane := inst.Lane(browser.LaneApply)
	b := st.add(&board{kind: "generic", marker: fmt.Sprintf("j%d%07d", 4, boardSeq.Add(1)), accept: accept})
	g := newE2E(t, lane, t.TempDir(), Hooks{})
	// Cookies live in the shared browser's profile; a sign-in from an earlier
	// test must not carry into this one.
	clearSession := func() {
		if cs, err := inst.Cookies(context.Background()); err == nil {
			for _, c := range cs {
				if c.Name == "sess" {
					_ = inst.DeleteCookie(context.Background(), c)
				}
			}
		}
	}
	clearSession()
	t.Cleanup(clearSession)
	t.Cleanup(func() {
		for _, ti := range lane.Tabs() {
			if ti.ID != shared.anchor {
				_ = lane.CloseTab(context.Background(), ti.ID)
			}
		}
	})
	return &loginScenario{g: g, b: b, url: st.postingURL(b), lane: lane}
}

func (l *loginScenario) dry(t *testing.T) *Run {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	return l.g.runner.RunOne(ctx, Input{URL: l.url, Caller: gates.CallerCLI})
}

func TestE2E_Account_AlternateSignsInAndBecomesTheSiteOverrideWithTheDefaultUntouched(t *testing.T) {
	l := newLogin(t, "Alt-One-2!y")
	l.g.seedPassword(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	l.g.seedPassword(t, "signin-alternate-0123456789ab", "signin_alternate", "", "Alt-One-2!y")
	run := l.dry(t)
	defer dump(t, run)
	if run.Outcome.Kind != OutcomeFilled {
		t.Fatalf("outcome %+v; the sign-in endpoint saw %q", run.Outcome, l.b.seen)
	}
	if n := l.b.pwTries.Load(); n != 2 {
		t.Fatalf("the server saw %d passwords, want the default then the alternate", n)
	}
	cands, err := l.g.creds.Candidates(context.Background(), "careers.example.test", "generic", "careers.example.test")
	if err != nil || cands[0].Kind != credentials.KindSite || cands[0].Password != "Alt-One-2!y" {
		t.Fatalf("candidates %+v %v", cands, err)
	}
	if got, _ := l.g.vault.Get("signin-default"); got != "Default-Pw-1!x" {
		t.Fatal("the shared default changed")
	}
	if strings.Contains(fmt.Sprint(run.Stages, run.Signins), "Alt-One-2!y") {
		t.Fatal("a password reached the run record")
	}
}

func TestE2E_Account_SignInUnderAWorkdayProxyIsPressed(t *testing.T) {
	l := newLogin(t, "Default-Pw-1!x")
	l.b.proxy = true
	l.g.seedPassword(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	run := l.dry(t)
	defer dump(t, run)
	if run.Outcome.Kind != OutcomeFilled || l.b.pwTries.Load() != 1 {
		t.Fatalf("outcome %+v, %d passwords submitted: the proxy over the hidden Sign In is pressed", run.Outcome, l.b.pwTries.Load())
	}
}

func TestE2E_Account_SignInButtonRedrawnWhileTypingIsPressed(t *testing.T) {
	l := newLogin(t, "Default-Pw-1!x")
	l.b.redraw = true
	l.g.seedPassword(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	run := l.dry(t)
	defer dump(t, run)
	if run.Outcome.Kind != OutcomeFilled || l.b.pwTries.Load() != 1 {
		t.Fatalf("outcome %+v, %d passwords submitted: the redrawn Sign In is the one pressed", run.Outcome, l.b.pwTries.Load())
	}
}

func TestE2E_Account_SessionFromTheUsersChromeCarriesTheSignInWithoutAPassword(t *testing.T) {
	l := newLogin(t, "never-accepted")
	l.g.seedPassword(t, "signin-default", "signin_default", "", "Default-Pw-1!x")
	inst, _ := sharedBrowser(t)
	l.g.runner.d.Page.Sessions = sessionFunc(func(ctx context.Context, host string) (OwnHostResult, error) {
		err := inst.SetCookie(ctx, browser.Cookie{Name: "sess", Value: "ok", Domain: host, Path: "/", Secure: true})
		return OwnHostResult{Imported: err == nil}, err
	})
	run := l.dry(t)
	defer dump(t, run)
	if run.Outcome.Kind != OutcomeFilled || l.b.pwTries.Load() != 0 {
		t.Fatalf("outcome %+v, %d passwords submitted: a session that takes means no password is submitted", run.Outcome, l.b.pwTries.Load())
	}
	if len(run.Signins) != 1 || run.Signins[0].Session != "imported" {
		t.Fatalf("signins %+v", run.Signins)
	}
}

type sessionFunc func(context.Context, string) (OwnHostResult, error)

func (f sessionFunc) SyncOwnHost(ctx context.Context, host string) (OwnHostResult, error) {
	return f(ctx, host)
}

func TestE2E_Apply_PostingWhoseLoadEventNeverFiresIsOpenedOnceParsed(t *testing.T) {
	// ADP's posting draws its Apply at once and keeps a request open: the run
	// goes on from the parsed document, never waiting for the load event.
	l := newLogin(t, "")
	l.b.kind = "custom"
	l.b.render = func(b *board, r *http.Request) string {
		return `<h1>Apply</h1><img src="/hang/` + b.marker + `" alt=""><form>` + fieldNames + `<button>Next</button></form>`
	}
	start := time.Now()
	run := l.dry(t)
	defer dump(t, run)
	fill := stageOf(run, StageFill)
	if fill.Status == StatusSkipped || fill.Status == "" {
		t.Fatalf("outcome %+v: the posting was not opened", run.Outcome)
	}
	if waited := fill.StartedAt.Sub(start); waited > 15*time.Second {
		t.Fatalf("the fill started %s after the run: the open waited for the load event", waited)
	}
}

func TestE2E_Apply_ApplyRedrawnWhenTheCookiePopupClosesIsFoundAgain(t *testing.T) {
	// Avature (Lenovo): Reject all closes the popup and redraws the posting,
	// so the Apply handle read before the press is gone.
	l := newLogin(t, "")
	l.b.kind = "custom"
	l.b.render = func(b *board, r *http.Request) string {
		if r.URL.Query().Get("form") == "1" {
			return `<h1>Apply</h1><form>` + fieldNames + `<button>Next</button></form>`
		}
		return `<title>AI Engineer</title><main id="m"><h1>AI Engineer</h1><aside><div role="navigation"><a href="?form=1" style="display:block;margin:200px 20px">Apply</a></div></aside></main>
<div class="cookies " id="gate" style="position:fixed;inset:0;z-index:10;background:#fff"><p>We use cookies to personalize content and ads.</p>
<a href="#" role="button" onclick="return false">Accept all</a>
<a href="#" role="button" onclick="document.getElementById('gate').remove(); const m = document.getElementById('m'); m.innerHTML = m.innerHTML; return false">Reject all</a></div>`
	}
	run := l.dry(t)
	defer dump(t, run)
	if fill := stageOf(run, StageFill); strings.Contains(fill.Detail, "control not pressed") || len(run.Pages) == 0 {
		t.Fatalf("outcome %+v fill %q: the walk did not reach the form", run.Outcome, fill.Detail)
	}
}

func TestE2E_Apply_ApplyLinkUnderAChatPanelIsFollowed(t *testing.T) {
	// CVS's Phenom posting: the chat assistant's panel stands over Apply Now;
	// the link's own address is where the press leads.
	l := newLogin(t, "")
	l.b.kind = "custom"
	l.b.render = func(b *board, r *http.Request) string {
		if r.URL.Query().Get("form") == "1" {
			return `<h1>Apply</h1><form>` + fieldNames + `<button>Next</button></form>`
		}
		return `<title>Staff Engineer</title><main><h1>Staff Engineer</h1><a href="?form=1" style="display:block;margin:200px 20px">Apply Now</a></main>
<div style="position:fixed;inset:0;z-index:10;background:rgba(255,255,255,0.02)"><p>Hi There! Are you interested in this job?</p></div>`
	}
	run := l.dry(t)
	defer dump(t, run)
	if len(run.Pages) == 0 {
		t.Fatalf("outcome %+v fill %q: the walk did not reach the form", run.Outcome, stageOf(run, StageFill).Detail)
	}
}

// Owned fixture acceptance of a Google-labelled redirect, not a real provider
// grant: no Google account, token or cookie is created or imported by this test.
func TestE2E_Account_GoogleExistingSessionReturn(t *testing.T) {
	l := newLogin(t, "")
	l.b.kind = "custom"
	l.b.render = func(b *board, r *http.Request) string {
		if r.URL.Query().Get("google") == "done" {
			return `<h1>Apply</h1><form>` + fieldNames + `<button>Next</button></form>`
		}
		return `<h1>Sign in</h1><button onclick="location.search='?google=done'">Sign in with Google</button>`
	}
	l.g.runner.d.Page.Settings = func() Settings { return Settings{AllowGoogleSignIn: true} }
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	tab, err := (LaneWindow{Lane: l.lane}).OpenTab(ctx, "google-fixture", engine.Host{})
	if err != nil {
		t.Fatal(err)
	}
	defer tab.Close(context.Background())
	if _, err := tab.Navigate(ctx, l.url); err != nil {
		t.Fatal(err)
	}
	assertGoogleFixtureWall(t, ctx, tab)
	e := &Exec{r: l.g.runner, Run: &Run{ID: "google-fixture", ATS: ATSGeneric, Tenant: "careers.example.test", Input: Input{URL: l.url}}, Ctx: ctx}
	s := &session{r: l.g.runner, e: e, tab: tab}
	detail, stopped := l.g.runner.account(ctx, e, s)
	if stopped != nil || len(e.Run.Signins) != 1 || e.Run.Signins[0].Succeeded != "google" {
		diagnostic, cancelDiagnostic := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelDiagnostic()
		if page, err := Probe(diagnostic, tab); err == nil {
			t.Logf("Google return page: %+v", page)
		}
		t.Fatalf("detail %q stop %+v signins %+v", detail, stopped, e.Run.Signins)
	}
	if l.b.pwTries.Load() != 0 {
		t.Fatal("Google login submitted a password")
	}
}

// The provider pages are owned TLS fixtures in a throwaway browser profile.
// They exercise the production probe, trusted clicks, navigation and popup
// ownership without granting a real third-party account access.
func TestE2E_Account_GoogleAccountAndConsent(t *testing.T) {
	for _, popup := range []bool{false, true} {
		t.Run(strconv.FormatBool(popup), func(t *testing.T) {
			l := newLogin(t, "")
			l.b.kind = "custom"
			var grants atomic.Int32
			_, site := sharedBrowser(t)
			provider := fmt.Sprintf("https://accounts.google.com:%d/oauth/%s?scope=openid%%20email%%20profile&redirect_uri=%s", site.port(), l.b.marker, url.QueryEscape(l.url))
			l.b.render = func(b *board, r *http.Request) string {
				if strings.HasPrefix(r.Host, "accounts.google.com:") {
					switch r.URL.Query().Get("step") {
					case "done":
						grants.Add(1)
						if popup {
							return `<script>opener.location.href=` + strconv.Quote(l.url+"?google=done") + `;window.close()</script>`
						}
						return `<script>location.href=` + strconv.Quote(l.url+"?google=done") + `</script>`
					case "consent":
						return `<p>alex@example.com</p><h1>Share basic email and profile</h1><button onclick="location.href=location.href.replace('step=consent','step=done')">Allow</button>`
					default:
						return `<h1>Choose an account</h1><div role="link" tabindex="0" data-identifier="alex@example.com" onclick="location.search+='&step=consent'">Alex</div>`
					}
				}
				if r.URL.Query().Get("google") == "done" {
					return `<h1>Apply</h1><form>` + fieldNames + `<button>Next</button></form>`
				}
				action := "location.href=" + strconv.Quote(provider)
				if popup {
					action = "window.open(" + strconv.Quote(provider) + ",'google-login')"
				}
				return `<h1>Sign in</h1><button onclick="` + html.EscapeString(action) + `">Sign in with Google</button>`
			}
			l.g.runner.d.Page.Settings = func() Settings { return Settings{AllowGoogleSignIn: true} }
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			tab, err := (LaneWindow{Lane: l.lane}).OpenTab(ctx, "google-grant-fixture", engine.Host{})
			if err != nil {
				t.Fatal(err)
			}
			defer tab.Close(context.Background())
			if _, err = tab.Navigate(ctx, l.url); err != nil {
				t.Fatal(err)
			}
			assertGoogleFixtureWall(t, ctx, tab)
			e := &Exec{r: l.g.runner, Run: &Run{ID: "google-grant-fixture", ATS: ATSGeneric, Tenant: "careers.example.test", Input: Input{URL: l.url}}, Ctx: ctx}
			detail, stopped := l.g.runner.account(ctx, e, &session{r: l.g.runner, e: e, tab: tab})
			if stopped != nil || len(e.Run.Signins) != 1 || e.Run.Signins[0].Succeeded != "google" || grants.Load() != 1 {
				t.Fatalf("detail %q stop %+v signins %+v grants %d", detail, stopped, e.Run.Signins, grants.Load())
			}
			if l.b.pwTries.Load() != 0 {
				t.Fatal("a password was submitted")
			}
		})
	}
}

// Prove the fixture exercises Google rather than the generic password gate.
func assertGoogleFixtureWall(t *testing.T, ctx context.Context, tab Tab) {
	t.Helper()
	page, err := Probe(ctx, tab)
	if err != nil {
		t.Fatal(err)
	}
	w := findWall(page)
	if w == nil || w.google == nil || w.password != nil {
		t.Fatalf("fixture did not expose a Google-only login wall: %+v", w)
	}
}
