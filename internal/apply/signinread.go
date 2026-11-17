package apply

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"applysmith/internal/gates"
)

// openEmailForm opens the email form a social-first wall (Workday) keeps
// behind "Sign in with email" and returns the page and wall it shows; a wall
// with a password box, or a page without that button, is returned unchanged.
func (a *acct) openEmailForm(ctx context.Context, st *PageState, w *wall) (*PageState, *wall, *stop) {
	c := findControl(st, signInEmailRE)
	if w.password != nil || c == nil {
		return st, w, nil
	}
	var p *PageState
	// A press before the page wired its buttons does nothing: the button still
	// shown with no form is pressed once more (it only opens a form).
	for try := 0; p == nil && c != nil && try < 2; try++ {
		if stp := a.pressFlow(ctx, c); stp != nil {
			return st, w, stp
		}
		if p = a.waitFor(ctx, func(p *PageState) bool { nw := findWall(p); return nw != nil && nw.password != nil }); p == nil {
			if again, err := Probe(ctx, a.s.tab); err == nil {
				c = findControl(again, signInEmailRE)
			}
		}
	}
	if p == nil {
		return st, w, a.fail(ClassCredential, "sign-in", "no email sign-in form came after %q; sign in in the browser, then resume%s", "Sign in with email", a.syncNote())
	}
	return p, findWall(p), nil
}

// read polls the page after a sign-in press. The sign-in form staying gone on
// a loaded page for 1.5 seconds, its header not offering Sign In, is signed
// in; a reload that brings it back within that time is not. A refusal needs the page to have changed since the
// press, so an old error message is not read as the answer to this one.
func (a *acct) read(ctx context.Context, pre string) (attemptResult, *stop) {
	deadline := time.Now().Add(a.tm.Answer)
	var goneSince time.Time
	changed, sawError := false, false
	for time.Now().Before(deadline) {
		if !sleepCtx(ctx, a.tm.Poll) {
			return attemptRefused, cancelledStop(StageAccount)
		}
		st, err := Probe(ctx, a.s.tab)
		if err != nil {
			continue // a navigation in flight
		}
		text := strings.Join(statementLines(st.Lines), "\n")
		wall := findWall(st)
		if wall == nil || st.Loading {
			changed = true
		}
		switch {
		case gates.DefiniteLockout(st.Text):
			return attemptLockout, nil
		case wall == nil && verifyRE.MatchString(text):
			return attemptVerify, nil
		case wall == nil && !st.Loading && st.Account != accountSignedOut:
			if goneSince.IsZero() {
				goneSince = time.Now()
			}
			if time.Since(goneSince) >= a.tm.Stable {
				return attemptSignedIn, nil
			}
		case wall == nil:
			goneSince = time.Time{}
		default:
			goneSince = time.Time{}
			if verifyRE.MatchString(text) {
				return attemptVerify, nil
			}
			if len(st.Validation) > 0 || badLoginRE.MatchString(text) {
				sawError = true
			}
			// The comparison uses the very probe the verdict rests on, so a page
			// that moved on between two reads cannot pair a new fingerprint with
			// old validation text. Without a reading from before the press, a
			// changed page proves nothing and only the deadline decides.
			if sig := st.Fingerprint(); pre != "" && sig != pre {
				changed = true
				if len(st.Validation) > 0 || badLoginRE.MatchString(text) {
					return attemptRefused, nil
				}
			}
		}
	}
	// Nothing changed in the whole wait and no error is shown: the page did not
	// answer. An error message that is still standing counts as the refusal,
	// the safe reading, because it spends the budget instead of risking more.
	if !changed && !sawError {
		return attemptNoAnswer, nil
	}
	return attemptRefused, nil
}

// noAnswer is the stop for a sign-in press the page never answered, naming
// what the page showed afterwards (a challenge the press raised, a page still
// loading, a disabled Sign In, the form gone) and the site's own requests that
// answered after the press, by method, path and status only.
func (a *acct) noAnswer(ctx context.Context, watch ResponseWatch) *stop {
	var seen []string
	if st, err := Probe(ctx, a.s.tab); err == nil {
		if d := DetectChallenge(st); d != nil {
			seen = append(seen, "a "+d.Kind+" challenge stands on the page")
		}
		if st.Loading {
			seen = append(seen, "the page is still loading")
		}
		switch w := findWall(st); {
		case w == nil:
			seen = append(seen, "the sign-in form is gone")
		case w.signin != nil:
			if loc, err := Locate(ctx, a.s.tab, w.signin.I); err == nil && loc.Disabled {
				seen = append(seen, "its Sign In is disabled")
			}
		}
	}
	if watch != nil {
		var calls []string
		for _, r := range watch.Responses() {
			u, err := url.Parse(r.URL)
			if err != nil || u.Hostname() != a.host || (r.Type != "XHR" && r.Type != "Fetch") {
				continue
			}
			calls = append(calls, fmt.Sprintf("%s %s %d", r.Method, clip(u.Path, 60), r.Status))
		}
		if n := watch.Pending(); n > 0 {
			calls = append(calls, fmt.Sprintf("%d still pending", n))
		}
		if len(calls) == 0 {
			calls = append(calls, "none")
		}
		seen = append(seen, "the site's requests after the press: "+strings.Join(calls, ", "))
	}
	note := ""
	if len(seen) > 0 {
		note = " (" + strings.Join(seen, "; ") + ")"
	}
	return a.fail(ClassUnavailable, "", "the sign-in on %s did not answer within %s%s; no further password was submitted", a.host, a.tm.Answer, note)
}

// settle waits for the page an account step led to: a sign-in form, returned,
// or a loaded page that stays without one for the stable time while its
// header does not offer Sign In (Workday shows the posting with its form gone
// for seconds before it asks for the activation). Neither within the answer
// time is (nil, false).
func (a *acct) settle(ctx context.Context) (*wall, bool, *stop) {
	deadline := time.Now().Add(a.tm.Answer)
	var since time.Time
	for time.Now().Before(deadline) {
		st, err := Probe(ctx, a.s.tab)
		var w *wall
		if err == nil {
			w = findWall(st)
		}
		switch {
		case err != nil || st.Loading:
			since = time.Time{}
		case w != nil:
			return w, false, nil
		case readEmailStep(st) == nil && !awaitsCode(st) && st.Account != accountSignedOut:
			if since.IsZero() {
				since = time.Now()
			}
			if time.Since(since) >= a.tm.Stable {
				return nil, true, nil
			}
		default:
			since = time.Time{}
		}
		if !sleepCtx(ctx, a.tm.Poll) {
			return nil, false, cancelledStop(StageAccount)
		}
	}
	return nil, false, nil
}
