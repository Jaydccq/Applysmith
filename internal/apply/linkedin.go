package apply

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/gates"
	"applysmith/internal/lazyregexp"
)

//go:embed linkedin.js
var linkedinJS string

var (
	linkedInJobRE      = lazyregexp.New(`(?i)^/jobs/view/(?:[^/?#]*-)?(\d+)`)
	linkedInClosedRE   = lazyregexp.New(`(?i)no longer accepting applications`)
	linkedInAppliedRE  = lazyregexp.New(`(?i)You applied (?:on|to)[^\n]*`)
	linkedInEasyRE     = lazyregexp.New(`(?i)easy apply`)
	linkedInLoginRE    = lazyregexp.New(`(?i)linkedin\.com/(?:uas/)?(?:login|checkpoint)`)
	linkedInWriteRE    = lazyregexp.New(`(?i)voyager/.*applications`)
	linkedInSent       = lazyregexp.New(`(?i)Application sent|Your application was sent`)
	linkedInEasyLabels = []string{"easy apply", "apply easily"}
)

// linkedInBase is the scheme and host the Easy Apply URLs are built on: the
// signed-in member site, whatever locale host the link came with.
func linkedInBase(u *url.URL) string {
	h := "www.linkedin.com"
	if p := u.Port(); p != "" {
		h += ":" + p
	}
	return "https://" + h
}

func linkedInJobID(raw string) (base, id string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !hostIs(strings.ToLower(u.Hostname()), "linkedin.com") {
		return "", ""
	}
	m := linkedInJobRE.FindStringSubmatch(u.Path)
	if m == nil {
		return "", ""
	}
	return linkedInBase(u), m[1]
}

// LinkedInFlowURL is the page that renders the Easy Apply modal for a posting:
// the control on the posting page is a plain link to it, and a click on that
// link does nothing in an automated browser. Any other URL gives "".
func LinkedInFlowURL(raw string) string {
	base, id := linkedInJobID(raw)
	if id == "" {
		return ""
	}
	return base + "/jobs/view/" + id + "/apply/?openSDUIApplyFlow=true"
}

// LinkedInPostingURL is the posting's own page, from any URL that names it.
func LinkedInPostingURL(raw string) string {
	base, id := linkedInJobID(raw)
	if id == "" {
		return ""
	}
	return base + "/jobs/view/" + id + "/"
}

func lkExpr(cmd string) string {
	c, _ := json.Marshal(cmd)
	return "(" + strings.TrimSpace(linkedinJS) + ")(" + string(c) + ")"
}

func lkBool(ctx context.Context, t Tab, cmd string) bool {
	var b bool
	return t.Eval(ctx, lkExpr(cmd), &b) == nil && b
}

func lkText(ctx context.Context, t Tab) string {
	var s string
	_ = t.Eval(ctx, lkExpr("mainText"), &s)
	return s
}

// linkedInAffordance is the Apply control the posting offers when it has no
// Easy Apply button.
type linkedInAffordance struct {
	Label     string `json:"label"`
	Href      string `json:"href"`
	Offsite   bool   `json:"offsite"`
	SignedOut bool   `json:"signedOut"`
}

func lkAffordance(ctx context.Context, t Tab) *linkedInAffordance {
	var a *linkedInAffordance
	if err := t.Eval(ctx, lkExpr("affordance"), &a); err != nil {
		return nil
	}
	return a
}

// pollUntil calls ok every poll until it reports true or the wait ends.
func pollUntil(ctx context.Context, wait, poll time.Duration, ok func() bool) bool {
	deadline := time.Now().Add(wait)
	for {
		if ok() {
			return true
		}
		if !time.Now().Before(deadline) || !sleepCtx(ctx, poll) {
			return false
		}
	}
}

// easyApplyControl finds the posting page's Easy Apply control among the
// probed controls: its visible label or its aria-label names Easy Apply.
func easyApplyControl(st *PageState) *Control {
	for i := range st.Controls {
		c := &st.Controls[i]
		if c.Chat || c.Disabled {
			continue
		}
		for _, l := range c.Labels {
			low := strings.ToLower(l)
			for _, want := range linkedInEasyLabels {
				if low == want {
					return c
				}
			}
			if linkedInEasyRE.MatchString(l) && len(l) < 40 {
				return c
			}
		}
	}
	return nil
}

// openLinkedIn opens a LinkedIn application: the flow URL renders the modal a
// few seconds after the document is parsed, so the page is not waited on for
// its load event and nothing is clicked until the modal is there. The button
// on the posting page is the fallback, and a posting with no button ends as
// what it is: closed, already applied, sign-in needed, or applied for on the
// employer's site, which the run follows.
func (r *Runner) openLinkedIn(ctx context.Context, e *Exec, s *session) *stop {
	tm := r.d.Page.Timing.withDefaults()
	start := r.d.Now()
	fail := func(class Class, board, reason string) *stop {
		r.record(e, StageAccount, StatusFailed, reason, start)
		return &stop{class: class, board: board, reason: reason, at: StageAccount}
	}
	res, err := s.tab.NavigateDOM(ctx, e.Cls.ApplyURL)
	switch {
	case err != nil:
		return fail(ClassEnvironment, "", "the posting could not be opened: "+err.Error())
	case res.Error != "":
		return fail(ClassUnavailable, "", "the site is not answering ("+res.Error+")")
	}
	modal := func() bool { return lkBool(ctx, s.tab, "modal") }
	reminder := func() bool { return lkBool(ctx, s.tab, "safetyReminder") }
	// A not-found page ends the wait for the modal: it never turns into one.
	notFound := func() bool { return lkBool(ctx, s.tab, "notFound") }
	pollUntil(ctx, tm.OpenWait, 250*time.Millisecond, func() bool { return modal() || notFound() || reminder() })
	if reminder() {
		if stp := r.continueApplying(ctx, e, s, tm, start); stp != nil {
			return stp
		}
		pollUntil(ctx, tm.OpenWait, 250*time.Millisecond, modal)
	}
	if !modal() {
		if notFound() {
			// LinkedIn can answer the flow URL with its not-found page while the
			// posting is live; the posting's own page carries the button.
			posting := LinkedInPostingURL(e.Cls.ApplyURL)
			if posting != "" {
				if _, err := s.tab.NavigateDOM(ctx, posting); err != nil {
					return fail(ClassEnvironment, "", "the posting could not be opened: "+err.Error())
				}
			}
			if posting == "" || lkBool(ctx, s.tab, "notFound") {
				e.Run.PostingFacts = append(e.Run.PostingFacts, "closed: LinkedIn answers Page not found")
				return fail(ClassPosting, "", fmt.Sprintf("the posting is gone: LinkedIn answers \"Page not found\" (404) for %s", redactURL(firstNonEmpty(posting, e.Cls.ApplyURL))))
			}
		}
		var btn *PageState
		var ctl *Control
		pollUntil(ctx, tm.OpenWait, 200*time.Millisecond, func() bool {
			st, perr := Probe(ctx, s.tab)
			if perr != nil {
				return false
			}
			if ctl = easyApplyControl(st); ctl != nil {
				btn = st
			}
			return ctl != nil
		})
		if ctl == nil {
			return r.linkedInNoButton(ctx, e, s, tm, fail)
		}
		w := &walker{r: r, e: e, s: s, tm: tm}
		if _, stp := w.pressAs(ctx, btn, *ctl, gates.StageWalk); stp != nil {
			stp.at = StageAccount
			r.record(e, StageAccount, StatusFailed, stp.reason, start)
			return stp
		}
		pollUntil(ctx, tm.OpenWait, 250*time.Millisecond, func() bool { return modal() || reminder() })
		if reminder() {
			if stp := r.continueApplying(ctx, e, s, tm, start); stp != nil {
				return stp
			}
			pollUntil(ctx, tm.OpenWait, 250*time.Millisecond, modal)
		}
		if !modal() {
			return fail(ClassDefect, "", "Easy Apply modal did not open after clicking the button")
		}
	}
	_ = s.page.ObserveSubmissions(ctx)
	return nil
}

// continueApplying presses "Continue applying" on LinkedIn's "Job search
// safety reminder", which can stand between Easy Apply and its modal. The
// press is the walk's, through its gate.
func (r *Runner) continueApplying(ctx context.Context, e *Exec, s *session, tm WalkTiming, start time.Time) *stop {
	st, err := Probe(ctx, s.tab)
	if err != nil {
		return nil
	}
	for i := range st.Controls {
		c := st.Controls[i]
		if c.Disabled || !slices.ContainsFunc(c.Labels, func(l string) bool { return strings.EqualFold(strings.TrimSpace(l), "continue applying") }) {
			continue
		}
		w := &walker{r: r, e: e, s: s, tm: tm}
		if _, stp := w.pressAs(ctx, st, c, gates.StageWalk); stp != nil {
			stp.at = StageAccount
			r.record(e, StageAccount, StatusFailed, stp.reason, start)
			return stp
		}
		return nil
	}
	return nil
}

// linkedInNoButton explains a posting that has no Easy Apply button. The page
// text is polled and not read once: the receipt of an application made on the
// employer's site appears seconds after the flow URL gives up.
func (r *Runner) linkedInNoButton(ctx context.Context, e *Exec, s *session, tm WalkTiming, fail func(Class, string, string) *stop) *stop {
	var closed bool
	var applied string
	pollUntil(ctx, tm.TerminalWait, 300*time.Millisecond, func() bool {
		text := lkText(ctx, s.tab)
		closed = linkedInClosedRE.MatchString(text)
		applied = strings.TrimSpace(linkedInAppliedRE.FindString(text))
		return closed || applied != ""
	})
	switch {
	case closed:
		e.Run.PostingFacts = append(e.Run.PostingFacts, "closed: the posting is no longer accepting applications")
		return fail(ClassPosting, "", "posting closed: the posting is no longer accepting applications")
	case applied != "":
		return fail(ClassPosting, "", fmt.Sprintf("already applied outside Easy Apply (%q)", applied))
	}
	aff := lkAffordance(ctx, s.tab)
	switch {
	case aff == nil:
		return fail(ClassDefect, "", "Easy Apply button not found on "+redactURL(e.Cls.ApplyURL))
	case aff.SignedOut:
		// The session from the user's everyday Chrome is asked for once before
		// the run stops at the signed-out page.
		host, _ := parseHost(e.Cls.ApplyURL)
		a := s.newAcct(host)
		imported, stp := a.importSession(ctx, "")
		if stp != nil {
			return stp
		}
		if imported {
			return r.openLinkedIn(ctx, e, s)
		}
		return fail(ClassCredential, "sign-in", fmt.Sprintf("LinkedIn is showing a signed-out page%s. "+
			"To open this application, sign in to www.linkedin.com in the app browser and retry this job. Page action: %q.", a.syncNote(), aff.Label))
	case aff.Offsite || !linkedInEasyRE.MatchString(aff.Label):
		where := ""
		if out := linkedInOutbound(resolveHref(e.Cls.ApplyURL, aff.Href)); aff.Href != "" && (aff.Offsite || out != "") {
			where = " (" + aff.Href + ")"
			if dest := firstNonEmpty(out, resolveHref(e.Cls.ApplyURL, aff.Href)); dest != "" {
				cls, st := Classify(dest)
				if st == nil && cls.ATS != ATSLinkedIn && !IsAggregatorURL(dest) {
					return r.handoff(ctx, e, s, dest, "linkedin posting applies on the employer's site at "+redactURL(dest), false)
				}
			}
		}
		return fail(ClassPosting, "", fmt.Sprintf("this posting does not offer Easy Apply; it applies through %q%s", aff.Label, where))
	}
	return fail(ClassDefect, "", "Easy Apply button not found on "+redactURL(e.Cls.ApplyURL))
}

// linkedInOutbound is the destination of LinkedIn's own outbound redirect
// (www.linkedin.com/safety/go/?url=..., /redir/redirect?url=...), "" for any
// other address: the Apply link of a posting that applies off LinkedIn is
// drawn through it.
func linkedInOutbound(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(strings.TrimPrefix(u.Hostname(), "www."), "linkedin.com") {
		return ""
	}
	if p := strings.TrimSuffix(u.Path, "/"); p != "/safety/go" && p != "/redir/redirect" {
		return ""
	}
	dest, err := url.Parse(u.Query().Get("url"))
	if err != nil || (dest.Scheme != "http" && dest.Scheme != "https") || dest.Host == "" {
		return ""
	}
	return dest.String()
}

func resolveHref(base, href string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	h, err := b.Parse(href)
	if err != nil || (h.Scheme != "http" && h.Scheme != "https") {
		return ""
	}
	return h.String()
}

// sessionChallenge reports what stands where LinkedIn's modal should be: a
// redirect to the login or checkpoint page, or a rendered captcha challenge
// (the invisible 0x0 reCAPTCHA anchor every LinkedIn page carries is not one).
func sessionChallenge(st *PageState) (kind string) {
	if linkedInLoginRE.MatchString(st.URL) {
		return "login_redirect"
	}
	for _, c := range st.Captcha {
		if (strings.HasPrefix(c.Kind, "recaptcha") || c.Kind == "hcaptcha") && c.Visible && c.W > 0 && c.H > 0 {
			return "captcha"
		}
	}
	return ""
}

// linkedInAccepted reports whether LinkedIn's application endpoint answered a
// write with 200.
func linkedInAccepted(rs []browser.Response) bool {
	for _, r := range rs {
		if isWrite(r.Method) && linkedInWriteRE.MatchString(r.URL) && r.Status == 200 {
			return true
		}
	}
	return false
}

// linkedInWait gives the press up to the confirmation window to show its
// verdict: a 200 on the applications endpoint, any refused write, or the
// "Application sent" text.
func (r *Runner) linkedInWait(ctx context.Context, s *session, watch ResponseWatch, wait time.Duration) {
	pollUntil(ctx, wait, 250*time.Millisecond, func() bool {
		if watch != nil {
			rs := watch.Responses()
			if linkedInAccepted(rs) {
				return true
			}
			for _, rsp := range rs {
				if isWrite(rsp.Method) && !ignoredWrite(rsp.URL) && rsp.Status >= 400 {
					return true
				}
			}
		}
		st, err := Probe(ctx, s.tab)
		return err == nil && linkedInSent.MatchString(st.Text)
	})
}

// linkedInUnconfirmed says what the page shows after a press nothing
// confirmed: whether the modal still holds its Submit control, which is the
// click not landing, and any alert or invalid control in it, which is LinkedIn
// refusing the click.
func linkedInUnconfirmed(st *PageState, wait time.Duration) string {
	if st == nil {
		return fmt.Sprintf("no confirmation within %s and the page could not be read", wait)
	}
	var parts []string
	if len(Categorize(st, true).Submit) > 0 {
		parts = append(parts, "the Easy Apply modal still holds Submit")
	} else {
		parts = append(parts, "the Easy Apply modal no longer holds Submit")
	}
	if len(st.Validation) > 0 {
		parts = append(parts, "alert or invalid control: "+strings.Join(st.Validation, " / "))
	}
	return fmt.Sprintf("no confirmation within %s; %s", wait, strings.Join(parts, "; "))
}
