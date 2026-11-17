package apply

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"time"

	"applysmith/internal/credentials"
	"applysmith/internal/gates"
	"applysmith/internal/gmail"
	"applysmith/internal/lazyregexp"
)

var (
	forgotRE      = lazyregexp.New(`(?i)^forgot\s+(?:your\s+)?password\??$`)
	signInEmailRE = lazyregexp.New(`(?i)^sign\s*in\s+with\s+e-?mail$`)
	policyWordsRE = lazyregexp.New(`(?i)\b(?:password\b.*\b(?:must|policy|requirement|too short|too weak|not allowed|previous|recent|used before)|(?:policy|requirement)\b.*\bpassword)\b`)
	existsRE      = lazyregexp.New(`(?i)\b(?:already\s+(?:exists|registered|in\s+use|have\s+an\s+account)|existing\s+(?:record|account)\s+with\s+that)\b`)
)

// savedPassword is the one password a reset may set: this host's site
// override, else the shared default; never an alternate, a tenant key or a
// generated one.
func savedPassword(cands []credentials.Candidate) (credentials.Candidate, gates.PasswordSource) {
	for _, c := range cands {
		if c.Kind == credentials.KindSite {
			return c, gates.PasswordSiteOverride
		}
	}
	for _, c := range cands {
		if c.Kind == credentials.KindDefault {
			return c, gates.PasswordSharedDefault
		}
	}
	return credentials.Candidate{}, ""
}

// tryReset runs the password reset of a known Workday account whose saved
// password is refused (or whose host is out of password budget or held, because
// a reset submits no guess). The reset gate decides first, from the run's own
// facts (the system, the refusals, the hold), and is audited; a refusal returns
// ran=false and the caller goes on with the stop it would have made. The
// reset-password gate then decides which password is set, and refuses when the
// user saved none. Once started the reset spends the run's one try on the host, and its
// sign-in is the verdict: signed in clears the host's holds and stores the
// password as the tenant key, anything else stops as credential and starts the
// 24-hour hold, except a reset that pressed nothing. Neither the mailed code
// nor the link reaches a detail, a log or the record.
func (a *acct) tryReset(ctx context.Context, accountExists bool) (detail string, stp *stop, ran bool) {
	creds := a.r.d.Page.Credentials
	if creds == nil || a.r.d.Page.Mail == nil && a.r.d.Page.Codes == nil {
		return "", nil, false
	}
	if a.cands == nil {
		cs, err := creds.Candidates(ctx, a.host, a.system, a.tenant)
		if err != nil {
			return "", nil, false
		}
		a.cands = cs
	}
	known := accountExists
	for _, c := range a.cands {
		known = known || c.Kind == credentials.KindTenant
	}
	failedAt, err := creds.ResetFailedAt(ctx, a.host)
	if err != nil {
		return "", nil, false // an unreadable record reads as a recent failure
	}
	var lastFailed int64
	if !failedAt.IsZero() {
		lastFailed = failedAt.UnixMilli()
	}
	hold, err := creds.Held(ctx, a.host)
	if err != nil {
		return "", nil, false // an unreadable hold may be a lockout
	}
	d := gates.ResetStart(gates.ResetStartInput{
		Demo: a.r.demo(), Now: a.r.d.Now().UnixMilli(), AccountKnown: known, HostIsWorkday: a.system == ATSWorkday,
		SavedPasswordRefused: accountExists || hold.Held || a.refused > 0 || a.lockout,
		ResetsStartedThisRun: a.e.resets[a.host], LockoutWordingSeen: a.lockout || hold.Lockout, LastFailedResetAt: lastFailed,
	})
	if stp := a.audit(d); stp != nil {
		return "", stp, true
	}
	if !d.Allowed() {
		return "", nil, false
	}
	cand, src := savedPassword(a.cands)
	pw := gates.ResetPassword(src)
	if stp := a.audit(pw); stp != nil {
		return "", stp, true
	}
	if !pw.Allowed() {
		a.resetUnsaved = true
		return "", nil, false
	}
	if a.e.resets == nil {
		a.e.resets = map[string]int{}
	}
	a.e.resets[a.host]++
	out := a.runReset(ctx, cand)
	a.rec.ResetState = out.Result
	if out.ClearsHolds {
		_ = creds.ClearHold(ctx, a.host)
	}
	if out.StoreTenantKey {
		_ = creds.Keep(ctx, a.host, a.system, a.tenant, cand)
		a.rec.Succeeded = "reset"
		return "signed in after a password reset", nil, true
	}
	if out.Hold24h {
		// The reset was sent: its mark and its hold are written even when the
		// run was cancelled meanwhile, or the next run starts another reset.
		wctx := context.WithoutCancel(ctx)
		_ = creds.RecordResetFailed(wctx, a.host)
		_ = a.r.d.Store.Write(wctx, func(tx *sql.Tx) error {
			return creds.RecordExhausted(wctx, tx, a.host, false, a.e.Run.ID)
		})
		a.rec.Exhausted = true
	}
	return "", a.fail(ClassCredential, "credential-refused", "password reset on %s: %s", a.host, out.Message), true
}

// runReset drives the reset pages and returns how it ended.
func (a *acct) runReset(ctx context.Context, cand credentials.Candidate) gates.ResetOutcome {
	settle := func(signedIn, nothing, policy bool) gates.ResetOutcome {
		return gates.SettleReset(gates.ResetOutcomeInput{SignedIn: signedIn, PressedNothing: nothing, PolicyRefused: policy})
	}
	st, err := Probe(ctx, a.s.tab)
	if err != nil {
		return settle(false, true, false)
	}
	if findControl(st, forgotRE) == nil {
		if c := findControl(st, signInEmailRE); c != nil {
			if a.pressFlow(ctx, c) != nil {
				return settle(false, true, false)
			}
			st = a.waitFor(ctx, func(p *PageState) bool { return findControl(p, forgotRE) != nil })
		}
	}
	forgot := (*Control)(nil)
	if st != nil {
		forgot = findControl(st, forgotRE)
	}
	if forgot == nil {
		return settle(false, true, false)
	}
	if a.pressFlow(ctx, forgot) != nil {
		return settle(false, true, false)
	}
	form := a.waitFor(ctx, func(p *PageState) bool { return emailBox(p) != nil && submitLike(p) != nil })
	if form == nil {
		return settle(false, false, false)
	}
	if a.typeInto(ctx, emailBox(form), cand.Email) != nil {
		return settle(false, false, false)
	}
	pressedAt := a.r.d.Now()
	if a.pressReset(ctx, submitLike(form)) != nil {
		return settle(false, false, false)
	}
	// A code field means the code comes by mail; otherwise a link does.
	next := a.waitFor(ctx, func(p *PageState) bool {
		boxes, _ := codeBoxes(p)
		return len(boxes) > 0 || len(passwordBoxes(p)) > 0
	})
	if next == nil {
		next = &PageState{}
	}
	if boxes, _ := codeBoxes(next); len(boxes) > 0 {
		if !a.resetCode(ctx, boxes, next, pressedAt) {
			return settle(false, false, false)
		}
	} else if !a.resetLink(ctx, pressedAt) {
		return settle(false, false, false)
	}
	pw := a.waitFor(ctx, func(p *PageState) bool { return len(passwordBoxes(p)) > 0 })
	if pw == nil {
		return settle(false, false, false)
	}
	start := time.Now()
	for _, b := range passwordBoxes(pw) {
		if a.typeSecret(ctx, b, cand.Password) != nil {
			return settle(false, false, false)
		}
	}
	// The page is read again after typing: a redrawn submit leaves the handle
	// read before it naming a detached element.
	if cur, err := Probe(ctx, a.s.tab); err == nil {
		pw = cur
	}
	if c := submitLike(pw); c != nil {
		if !a.dwell(ctx, start) || a.pressReset(ctx, c) != nil {
			return settle(false, false, false)
		}
	}
	after := a.waitFor(ctx, func(p *PageState) bool { return len(passwordBoxes(p)) == 0 || len(p.Validation) > 0 })
	if after != nil && len(after.Validation) > 0 && policyWordsRE.MatchString(strings.Join(after.Validation, " ")) {
		return settle(false, false, true)
	}
	// One sign-in, and it is the verdict.
	if _, err := a.s.tab.Navigate(ctx, a.s.entry.URL); err != nil {
		return settle(false, false, false)
	}
	cur, err := Probe(ctx, a.s.tab)
	if err != nil {
		return settle(false, false, false)
	}
	if w := findWall(cur); w != nil {
		res, stp := a.attempt(ctx, w, cand)
		return settle(stp == nil && res == attemptSignedIn, false, false)
	}
	return settle(true, false, false)
}

// resetCode types the code the tenant mailed after the press, from the Workday
// sender only, and presses the page's own button.
func (a *acct) resetCode(ctx context.Context, boxes []*InputInfo, st *PageState, pressedAt time.Time) bool {
	codes := a.r.d.Page.Codes
	if codes == nil {
		return false
	}
	tm := a.r.d.Page.Code
	tm.Poll, tm.GiveUp = a.tm.CodePoll, a.tm.ResetWait
	code, ev, err := a.e.codeLane().Lookup(ctx, codes, tm, a.r.d.Now, CodeAnchor(pressedAt), SenderWorkday, fitsBoxes(len(boxes)))
	if err != nil {
		return false
	}
	d := gates.ResetCodeMail(ev.Sender, ev.At.UnixMilli(), pressedAt.UnixMilli())
	if a.audit(d) != nil || !d.Allowed() {
		return false
	}
	if typeCode(ctx, a.s.tab, boxes, code) != nil {
		return false
	}
	if c := submitLike(st); c != nil {
		return a.pressReset(ctx, c) == nil
	}
	return true
}

// resetLink waits for the reset mail, applies the locks and opens its link in
// the run's browser.
func (a *acct) resetLink(ctx context.Context, pressedAt time.Time) bool {
	mail := a.r.d.Page.Mail
	if mail == nil {
		return false
	}
	deadline := time.Now().Add(a.tm.ResetWait)
	var res gmail.LinkResult
	for {
		r, err := mail.ResetLink(ctx, gmail.LinkQuery{Host: a.host, After: pressedAt})
		if err == nil {
			res = r
			break
		}
		if mailNotConnected(err) {
			a.rec.Mail = MailNotConnected
		}
		if !isNotFound(err) || time.Now().Add(a.tm.MailPoll).After(deadline) || !sleepCtx(ctx, a.tm.MailPoll) {
			return false
		}
	}
	d := gates.ResetLink(gates.ResetLinkInput{HostIsWorkday: true, TenantHost: a.host, Mail: gates.MailFacts{
		Link: res.URL, SenderDomain: senderDomain(res.From), ReceivedAt: res.ReceivedAt.UnixMilli(), NotNewerThan: pressedAt.UnixMilli(), Now: a.r.d.Now().UnixMilli(),
	}})
	if a.audit(d) != nil || !d.Allowed() || a.audit(gates.OpenMailLink(gates.LinkReset)) != nil {
		return false
	}
	_, err := a.s.tab.Navigate(ctx, res.URL)
	return err == nil
}

func (a *acct) pressReset(ctx context.Context, c *Control) *stop {
	return a.pressAs(ctx, c, gates.StageReset)
}

// waitFor probes until the page satisfies ok or the answer time ends; nil when
// it never did.
func (a *acct) waitFor(ctx context.Context, ok func(*PageState) bool) *PageState {
	deadline := time.Now().Add(a.tm.Answer)
	for {
		if st, err := Probe(ctx, a.s.tab); err == nil && ok(st) {
			return st
		}
		if !time.Now().Before(deadline) || !sleepCtx(ctx, a.tm.Poll) {
			return nil
		}
	}
}

func findControl(st *PageState, re *lazyregexp.Regexp) *Control {
	for i := range st.Controls {
		c := &st.Controls[i]
		if c.Chat || c.Chrome || c.Disabled {
			continue
		}
		for _, l := range c.Labels {
			if re.MatchString(strings.TrimSpace(l)) {
				return c
			}
		}
	}
	return nil
}

func emailBox(st *PageState) *InputInfo {
	for i := range st.Inputs {
		in := &st.Inputs[i]
		if !in.Furniture && (in.Type == "email" || strings.Contains(strings.ToLower(in.Label+in.Name), "mail")) {
			return in
		}
	}
	return nil
}

func passwordBoxes(st *PageState) []*InputInfo {
	var out []*InputInfo
	for i := range st.Inputs {
		if in := &st.Inputs[i]; !in.Furniture && in.Type == "password" {
			out = append(out, in)
		}
	}
	return out
}

// submitLike finds the button of a reset form.
func submitLike(st *PageState) *Control {
	for i := range st.Controls {
		c := &st.Controls[i]
		if c.Chat || c.Chrome || c.Disabled {
			continue
		}
		for _, l := range c.Labels {
			if gates.PressControl(gates.ControlInput{Stage: gates.StageReset, Found: true, Labels: []string{l}}).Allowed() {
				return c
			}
		}
	}
	return nil
}

// offersForgot reports a "Forgot password?" control: the page signs in to an
// account that exists, so a new-password token on its one password box only
// turns the browser's saved passwords off (ByteDance's sign-in, with Create
// account a page of its own).
func offersForgot(st *PageState) bool {
	for i := range st.Controls {
		if c := &st.Controls[i]; !c.Chat && slices.ContainsFunc(c.Labels, func(l string) bool { return forgotRE.MatchString(strings.TrimSpace(l)) }) {
			return true
		}
	}
	return false
}
