package apply

import (
	"context"
	"net/mail"
	"strings"
	"time"

	"applysmith/internal/credentials"
	"applysmith/internal/gates"
	"applysmith/internal/gmail"
	"applysmith/internal/gmail/extract"
	"applysmith/internal/lazyregexp"
)

// activationATS maps the run's system to the system with an activation shape.
func activationATS(system, host string) gates.ATS {
	switch {
	case system == ATSWorkday:
		return gates.ATSWorkday
	case extract.SuccessFactorsHost(host):
		return gates.ATSSuccessFactors
	}
	return gates.ATSGeneric
}

func senderDomain(from string) string {
	if a, err := mail.ParseAddress(from); err == nil {
		if i := strings.LastIndex(a.Address, "@"); i >= 0 {
			return strings.ToLower(a.Address[i+1:])
		}
	}
	if i := strings.LastIndex(from, "@"); i >= 0 {
		return strings.ToLower(strings.Trim(from[i+1:], "> "))
	}
	return ""
}

var activationResendRE = lazyregexp.New(`(?i)\b(?:re-?send|request|send)\b.{0,30}\b(?:verification|activation|confirmation)\b`)

// lookupMail asks for the newest activation mail for the host. after, when
// set, is the instant the mail must follow; wait bounds how long a missing
// mail is polled for (zero is one try).
func (a *acct) lookupMail(ctx context.Context, after time.Time, wait time.Duration) (gmail.LinkResult, bool, *stop) {
	q := gmail.LinkQuery{Host: a.host, SinceDays: 30, After: after}
	deadline := time.Now().Add(wait)
	// A retryable failure (Gmail not answering for a moment) is asked again
	// while the wait lasts, and at least once.
	retried := false
	for {
		res, err := a.r.d.Page.Mail.ActivationLink(ctx, q)
		if err == nil {
			return res, true, nil
		}
		late := wait <= 0 || time.Now().Add(a.tm.MailPoll).After(deadline)
		le, _ := err.(*gmail.LookupError)
		switch {
		case isNotFound(err):
			a.linkRefused = le.Reason == gmail.ReasonLinkRefused
			if late {
				return gmail.LinkResult{}, false, nil
			}
		case le != nil && le.Retryable && (!retried || !late):
			retried = true
		default:
			if mailNotConnected(err) {
				a.rec.Mail = MailNotConnected
			}
			return gmail.LinkResult{}, false, a.fail(ClassCredential, "sign-in", "the activation mail lookup failed: %v", err)
		}
		if !sleepCtx(ctx, a.tm.MailPoll) {
			return gmail.LinkResult{}, false, cancelledStop(StageAccount)
		}
	}
}

// resend presses the tenant's own request-verification control: once per run,
// only on a named system, and then only mail strictly newer than the press
// qualifies. why says what stood in the way when nothing was pressed or no
// mail came.
func (a *acct) resend(ctx context.Context, ats gates.ATS) (res gmail.LinkResult, pressed time.Time, found bool, why string, stp *stop) {
	in := ats
	if !credentials.IsNamed(a.system) {
		in = gates.ATSGeneric // a generic host, SuccessFactors included, gets no resend press
	}
	d := gates.ActivationResend(gates.ResendInput{ATS: in, PressesThisRun: a.e.resends})
	if stp = a.audit(d); stp != nil {
		return
	}
	if !d.Allowed() {
		return res, pressed, false, d.Reason, nil
	}
	st, err := Probe(ctx, a.s.tab)
	if err != nil {
		return res, pressed, false, "", a.fail(ClassEnvironment, "", "the page could not be read: %v", err)
	}
	var ctl *Control
	for i := range st.Controls {
		c := &st.Controls[i]
		if c.Chat || c.Chrome || c.Disabled {
			continue
		}
		for _, l := range c.Labels {
			if activationResendRE.MatchString(l) && ctl == nil {
				ctl = c
			}
		}
	}
	if ctl == nil {
		return res, pressed, false, "no request-verification control stands on the page", nil
	}
	pressed = a.r.d.Now()
	if stp = a.pressFlow(ctx, ctl); stp != nil {
		return
	}
	a.e.resends++
	res, found, stp = a.lookupMail(ctx, pressed, a.tm.MailWait)
	if !found && stp == nil {
		why = "no mail newer than the request arrived"
	}
	return
}

// openActivation applies the four locks to the mail's link, opens it in the
// run's browser and reopens the entry page after the settle wait. notNewer is
// the instant the mail has to follow (the sign-up press or the resend press),
// zero for a mail that already existed.
func (a *acct) openActivation(ctx context.Context, ats gates.ATS, pre gates.Precondition, res gmail.LinkResult, notNewer time.Time) *stop {
	facts := gates.MailFacts{Link: res.URL, SenderDomain: senderDomain(res.From), ReceivedAt: res.ReceivedAt.UnixMilli(), Now: a.r.d.Now().UnixMilli()}
	if !notNewer.IsZero() {
		facts.NotNewerThan = notNewer.UnixMilli()
	}
	d := gates.ActivationLink(gates.ActivationInput{Demo: a.r.demo(), ATS: ats, Precondition: pre, TenantHost: a.host, Mail: facts})
	if st := a.audit(d); st != nil {
		return st
	}
	if !d.Allowed() {
		return a.fail(ClassCredential, "sign-in", "%s", d.Reason)
	}
	if st := a.audit(gates.OpenMailLink(gates.LinkActivation)); st != nil {
		return st
	}
	if _, err := a.s.tab.Navigate(ctx, res.URL); err != nil {
		return a.fail(ClassEnvironment, "", "the activation link could not be opened: %v", err)
	}
	if !sleepCtx(ctx, a.tm.Settle) {
		return cancelledStop(StageAccount)
	}
	if _, err := a.s.tab.Navigate(ctx, a.s.entry.URL); err != nil {
		return a.fail(ClassEnvironment, "", "the page could not be reopened after activation: %v", err)
	}
	return nil
}

// retrySignin signs in once more after an activation, on the sign-in form the
// reopened page shows; a page that settles signed in needs none.
func (a *acct) retrySignin(ctx context.Context, c credentials.Candidate) (attemptResult, *stop) {
	w, signedIn, stp := a.settle(ctx)
	switch {
	case stp != nil:
		return attemptRefused, stp
	case w != nil:
		return a.attempt(ctx, w, c)
	case signedIn:
		return attemptSignedIn, nil
	}
	return attemptRefused, nil
}

// verify runs the activation flow when the sign-in said the account needs
// email verification, or the tenant said an activation email was sent after
// the run's own sign-up. It looks up the activation mail (existing mail over
// 30 days, or after a sign-up only mail newer than the press, waiting for it),
// applies the four locks of the activation gate, opens the link in the run's
// browser, waits for the settle time and signs in again; the second sign-in is
// the verdict. When the mail is missing or its link turns out stale the
// tenant's own request-verification control is pressed once per run, on a named
// system, and only mail strictly newer than that press is taken. A wait that
// ends without the expected mail stops the stage with class credential and a
// reason naming the missing mail, never a generic timeout.
func (a *acct) verify(ctx context.Context, w *wall, c credentials.Candidate, pre gates.Precondition, pressedAt time.Time) (string, *stop) {
	ats := activationATS(a.system, a.host)
	if ats == gates.ATSGeneric || a.r.d.Page.Mail == nil {
		d := gates.ActivationLink(gates.ActivationInput{Demo: a.r.demo(), ATS: ats, Precondition: pre, TenantHost: a.host})
		if st := a.audit(d); st != nil {
			return "", st
		}
		return "", a.fail(ClassCredential, "sign-in", "the account on %s needs email verification and the run cannot open an activation link for it", a.host)
	}
	var after time.Time
	var wait time.Duration
	if pre == gates.PreconditionSentPage {
		after, wait = pressedAt, a.tm.MailWait
	}
	res, found, stp := a.lookupMail(ctx, after, wait)
	if stp != nil {
		return "", stp
	}
	why := ""
	if !found && pre == gates.PreconditionBanner {
		res, after, found, why, stp = a.resend(ctx, ats)
		if stp != nil {
			return "", stp
		}
	}
	if !found {
		return "", a.noMail(pre, why)
	}
	if stp := a.openActivation(ctx, ats, pre, res, after); stp != nil {
		return "", stp
	}
	got, stp := a.retrySignin(ctx, c)
	if stp != nil {
		return "", stp
	}
	if got == attemptVerify && pre == gates.PreconditionBanner {
		// The link did not activate the account: it was stale. One resend, then
		// only mail newer than the press.
		res, after, found, why, stp = a.resend(ctx, ats)
		if stp != nil {
			return "", stp
		}
		if !found {
			return "", a.fail(ClassCredential, "sign-in", "the activation link on %s did not activate the account and no newer mail came (%s); the stored password is fine", a.host, why)
		}
		if stp := a.openActivation(ctx, ats, pre, res, after); stp != nil {
			return "", stp
		}
		if got, stp = a.retrySignin(ctx, c); stp != nil {
			return "", stp
		}
	}
	if got != attemptSignedIn {
		return "", a.fail(ClassCredential, "sign-in", "the sign-in after activation was not accepted on %s", a.host)
	}
	return "activated and signed in with " + c.Label, nil
}

// noMail is the stop for a mail that never came. After a sign-up the account
// is new; after a sign-in banner the password proved itself, so the stop says
// the credential is fine.
func (a *acct) noMail(pre gates.Precondition, why string) *stop {
	if a.linkRefused {
		return a.fail(ClassCredential, "sign-in", "the activation email for %s came, but its link is not one the run may open; open it from your inbox, then resume", a.host)
	}
	if why != "" {
		why = " (" + why + ")"
	}
	if pre == gates.PreconditionSentPage {
		return a.fail(ClassCredential, "sign-in", "no activation email for %s arrived within %s after the sign-up%s", a.host, a.tm.MailWait, why)
	}
	return a.fail(ClassCredential, "sign-in", "no activation email for %s arrived%s; the stored password is fine, only the email confirmation is missing", a.host, why)
}

func isNotFound(err error) bool {
	if le, ok := err.(*gmail.LookupError); ok {
		return le.Kind == gmail.KindNotFound
	}
	return false
}
