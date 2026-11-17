package classify

import (
	"strings"
	"time"

	"applysmith/internal/gmail/extract"
	"applysmith/internal/lazyregexp"
)

// SelfTriggeredRule is the rule id of a demoted signal.
const SelfTriggeredRule = "self_triggered_signup_mail"

// SignupGrace is how long after a run mail from its sign-up still counts.
const SignupGrace = 2 * time.Hour

// SignupRun is the part of an apply-run record the demotion needs.
type SignupRun struct {
	JobHost, SignupHost string
	Start, End          time.Time
	// AccountCreated is zero for a run that created no account.
	AccountCreated time.Time
}

// SignupWindow is one run's time and sender window.
type SignupWindow struct {
	Hosts          []string
	ATSMailDomains []string
	From, Until    time.Time
}

// lifecycleSubjectRE matches account-lifecycle subjects: verify, confirm or
// activate an account, address or code; account created or activated; welcome
// to a career portal; a one-time, security or sign-in code; a password reset.
var lifecycleSubjectRE = ci(`\b(?:verif(?:y|ication)|confirm|activate|activation)\b[^.]{0,40}\b(?:account|e-?mail|address|profile|code)\b|\b(?:account|profile)\b[^.]{0,30}\b(?:created|activated|confirmed|verified)\b|\bwelcome to\b[^.]{0,60}\b(?:career|careers|candidate|talent|job|jobs)\b[^.]{0,20}\b(?:portal|site|page|community|network)\b|\b(?:one[- ]time|security|verification|sign[- ]?in|login|access)\s+(?:code|passcode|pin)\b|\breset\b[^.]{0,20}\bpassword\b|\bpassword\b[^.]{0,20}\breset\b`)

var (
	workdayHostSuffix = lazyregexp.New(`(?i)(?:^|\.)(?:myworkdayjobs|myworkday)\.com$`)
)

// atsMailDomains are the domains an applicant-tracking system itself sends
// from for a host of its own.
func atsMailDomains(host string) []string {
	h := strings.ToLower(host)
	switch {
	case workdayHostSuffix.MatchString(h):
		return []string{"otp.workday.com", "myworkday.com"}
	case extract.SuccessFactorsHost(h):
		return []string{"successfactors.com", "sapsf.com"}
	}
	return nil
}

// Windows builds one window per run that created an account. A run that
// created none contributes nothing.
func Windows(runs []SignupRun) []SignupWindow {
	var out []SignupWindow
	for _, r := range runs {
		if r.AccountCreated.IsZero() || r.Start.IsZero() {
			continue
		}
		var hosts, mail []string
		seen := map[string]bool{}
		for _, h := range []string{r.JobHost, r.SignupHost} {
			h = strings.ToLower(strings.TrimSpace(h))
			if h == "" || seen[h] {
				continue
			}
			seen[h] = true
			hosts = append(hosts, h)
			mail = append(mail, atsMailDomains(h)...)
		}
		if len(hosts) == 0 {
			continue
		}
		end := r.End
		if r.AccountCreated.After(end) {
			end = r.AccountCreated
		}
		out = append(out, SignupWindow{Hosts: hosts, ATSMailDomains: mail, From: r.Start, Until: end.Add(SignupGrace)})
	}
	return out
}

func sameOrSub(a, parent string) bool { return a == parent || strings.HasSuffix(a, "."+parent) }

// belongs is true when the sender domain equals, is a subdomain of or is a
// parent domain of a host of the run, or is a mail domain of its ATS.
func (w SignupWindow) belongs(sender string) bool {
	for _, d := range w.ATSMailDomains {
		if sameOrSub(sender, d) {
			return true
		}
	}
	for _, h := range w.Hosts {
		if sameOrSub(h, sender) || sameOrSub(sender, h) {
			return true
		}
	}
	return false
}

// selfTriggered reports mail the product's own sign-up caused: incoming, from
// a host of a run that created an account, inside that run's window, with an
// account-lifecycle subject. A real employer reply fails the subject test.
func selfTriggered(windows []SignupWindow, dir, senderDomain, subject string, received time.Time) bool {
	if dir != DirIncoming || len(windows) == 0 || senderDomain == "" || received.IsZero() {
		return false
	}
	if !lifecycleSubjectRE.MatchString(subject) {
		return false
	}
	for _, w := range windows {
		if !received.Before(w.From) && !received.After(w.Until) && w.belongs(senderDomain) {
			return true
		}
	}
	return false
}

// demoteSelfTriggered keeps the signal as evidence but makes it low priority,
// so it never reaches a status, a digest or an alert.
func (c *Classifier) demoteSelfTriggered(sig *Signal, from Sender) {
	if !selfTriggered(c.Windows, sig.Direction, emailDomain(from.Email), sig.Subject, sig.ReceivedAt) {
		return
	}
	sig.Type, sig.Tier = TypeLowPriority, TierLow
	sig.ReviewReasons = nil
	sig.Evidence.Event = EventEvidence{Type: TypeLowPriority, RuleID: SelfTriggeredRule}
}
