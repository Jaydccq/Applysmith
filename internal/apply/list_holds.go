package apply

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/gates"
	"applysmith/internal/lazyregexp"
)

// buildVouches reports whether a stop recorded by build past still speaks for
// the code that is running now: only the same clean build does. A build with
// local modifications, or one that cannot name itself, never vouches.
func buildVouches(past, current string) bool {
	clean := func(b string) bool { return cleanBuild(b) && !strings.EqualFold(b, "unknown") }
	return clean(past) && clean(current) && past == current
}

var (
	challengeRE = lazyregexp.New(`(?i)bot[- ]check|bot-challenge|anti-bot|verification challenge|verification step; clear it`)
	// postingFactRE matches the stops that state what the posting itself said;
	// closedFactRE the ones of them that say it is closed.
	// The walk names its own ends with hyphens (walk ended posting-closed).
	postingFactRE = lazyregexp.New(`(?i)posting[- ]closed|no longer (accepting|available)|\b(404|410)\b|page not found|liveness expired|already[- ](applied|submitted)|applies by email only|email-application`)
	closedFactRE  = lazyregexp.New(`(?i)posting[- ]closed|no longer (accepting|available)|\b(404|410)\b|page not found|liveness expired`)
	// userDataReasonRE matches an unresolved row whose answer only the user
	// holds: a decline for want of data, a task the form sends the user off to
	// do, or a code only the user's inbox holds. A decline about the control
	// ("No matching option"), an abstain or a failed write is the code's.
	userDataReasonRE = lazyregexp.New(`(?i)^(?:declined:\s*(?:unknown(?:\s+answer)?\s*$|(?:insufficient|not enough)\s+(?:info(?:rmation)?|data|facts?)|(?:cannot|unable to) determine|no\s+(?:(?:relevant|factual|applicable|necessary|applicant)\s+)?(?:data|info(?:rmation)?|facts?|evidence|answer)\b|no[ _]video\b)|excluded:off-form-task$|otp:no code arrived in Gmail$|` + writtenUserData + `)`)
	// profileHeldRE matches a label the profile always answers: the applicant's
	// own name, email, phone and home address. A required row like that left
	// declined is a matching gap a code change fixes, never missing user data.
	profileHeldRE   = lazyregexp.New(`^(?:(?:your |legal )?(?:full )?name|(?:legal |preferred )?(?:first|last|given|family) name|(?:legal )?surname|(?:(?:home|street|mailing|current|permanent) )?address(?: line)?(?: ?[12])?|city|county|state(?: province)?|postal code|zip(?: ?code)?|country(?: phone code)?|(?:(?:home|mobile|cell|work|primary|personal|daytime) )?(?:tele)?phone(?: (?:number|no))?|(?:mobile|cell)(?: (?:number|no))?|(?:confirm|re ?enter|retype|verify)(?: your)? (?:e ?mail|email)(?: address)?|(?:e ?mail|email)(?: address| confirmation)?)$`)
	accountExistsRE = lazyregexp.New(`(?i)account already exists on ([a-z0-9][a-z0-9.-]*)`)
	labelNoiseRE    = lazyregexp.New(`[^a-z0-9]+`)
)

// writtenUserData is the same in the reasons the check writes on a leftover
// (leftovers): a question the user's own entry says to ask about; or one the
// grounded rung declined, declined before on the same data, answered with a
// placeholder or had no profile fact for, alone or after the matcher found no
// bank entry, deferred it as prose or found no grounding. A decline after any
// other abstain, a site message or a mention in the user's information is the
// code's.
const writtenUserData = `abstained:` + string(match.ReasonAskMe) + `$|(?:abstained:(?:` + string(match.ReasonNoEntry) + `|` + string(match.ReasonNarrativeDeferred) + `|` + string(match.ReasonNoGrounding) +
	`); )?(?:` + reasonDeclined + `(?:-before)?|` + reasonMissingData + `|` + reasonNeedsProfileFact + `)$`

// offAggregator is the resolve stage's detail when a walk moved off a mirror
// onto the employer's posting.
const offAggregator = "off an aggregator posting"

func postingVerdictIsFact(reason string) bool { return postingFactRE.MatchString(reason) }

func stoppedRun(r RunFact) bool { return r.Kind == OutcomeStopped }

// challengeStop is a stop on a bot check a person clears in the page.
func challengeStop(r RunFact) bool { return stoppedRun(r) && challengeRE.MatchString(r.Reason) }

// mirrorChallengeExpired reports whether a challenge stop was the bot wall of
// a mirror's own page, met by a walk that ran on the mirror itself. Current
// code resolves the employer posting first and never loads the mirror, so such
// a wall expires like a judgement. It stays when the walk had already moved to
// the employer (that wall is the employer's) and when the run never walked.
func mirrorChallengeExpired(r RunFact, build string) bool {
	if r.ATS == "" || !IsAggregatorURL(r.URL) || !r.HasResolve || strings.Contains(r.ResolveDetail, offAggregator) {
		return false
	}
	return !buildVouches(r.Build, build)
}

// isJudgementStop is a stop that is the runtime's own reading of the page: an
// answer gap, a defect, or a posting stop that is not what the posting said. A
// challenge never is; the person clears it, not a deploy.
func isJudgementStop(r RunFact) bool {
	if !stoppedRun(r) || challengeStop(r) {
		return false
	}
	switch r.Class {
	case ClassAnswerGap, ClassDefect:
		return true
	case ClassPosting:
		return !postingVerdictIsFact(r.Reason)
	}
	return false
}

// unresolvedParts splits a stored leftover, "Label (detail)", at its last
// parenthesised group.
func unresolvedParts(s string) (label, detail string) {
	s = strings.TrimSpace(s)
	if !strings.HasSuffix(s, ")") {
		return s, ""
	}
	depth := 0
	for i := len(s) - 1; i >= 0; i-- {
		switch s[i] {
		case ')':
			depth++
		case '(':
			depth--
			if depth == 0 {
				return strings.TrimSpace(s[:i]), s[i+1 : len(s)-1]
			}
		}
	}
	return s, ""
}

func profileHeldLabel(label string) bool {
	return profileHeldRE.MatchString(strings.Join(strings.Fields(labelNoiseRE.ReplaceAllString(strings.ToLower(label), " ")), " "))
}

// userDataStop reports whether a stop is a fact about the user's own data and
// nothing else: the check refused a Submit, every required leftover is a
// decline for want of data, an off-form task or a code that never arrived, no
// page shows an identity mismatch, an unkept write, a review flag or a failed
// re-read, and no leftover is a label the profile always answers. A deploy
// does not move such a stop; only a save of the user's data does.
func userDataStop(r RunFact) bool {
	if !stoppedRun(r) || r.Class != ClassAnswerGap || r.Stage != StageCheck {
		return false
	}
	n := 0
	for _, p := range r.Pages {
		if len(p.Mismatches) > 0 || len(p.Uncommitted) > 0 || len(p.Flags) > 0 || rereadFailed(p) {
			return false
		}
		for _, u := range p.Unresolved {
			label, detail := unresolvedParts(u)
			if !userDataReasonRE.MatchString(detail) || profileHeldLabel(label) {
				return false
			}
			n++
		}
	}
	return n > 0
}

func rereadFailed(p PageReport) bool {
	for _, n := range p.Notes {
		if strings.HasPrefix(n, "the page re-read failed") {
			return true
		}
	}
	return false
}

// todayStop is why a row attempted today is not attempted again.
type todayStop struct {
	reason string
	// duplicate: the row was submitted today; nothing ever re-runs it.
	duplicate bool
	// needsUser: a sign-in wall only the user can move.
	needsUser bool
	// waitsForData: the user's own missing data, saved nowhere since.
	waitsForData bool
	owner        string
}

const (
	needsUserNote = " - a sign-in wall only you can clear (a password, a verification e-mail, or a sign-in the site offers only through a third party); name the company to retry it"
	userDataNote  = " - it waits for your answer (a question your saved answers do not hold, or something only you have); save it in your answer bank or profile, or name the company to retry it"
)

// todaySaves are when the user last saved their data (the answer bank, the
// profile or the CV) and when the row last got a ready document or a
// documents override.
type todaySaves struct{ data, documents time.Time }

// todayVerdict reads the last run of the day as a verdict, or nil when it
// holds nothing back: a dry run, a run still going, an environment or
// unavailable stop, a judgement written by code since replaced, a judgement or
// user-data stop the row's documents changed after, and the stops the holds
// own (a fact about the posting, a challenge).
func todayVerdict(r RunFact, build string, saved todaySaves) *todayStop {
	switch {
	case r.Kind == OutcomeSubmitted:
		return &todayStop{reason: "submitted today (run " + r.ID + ")", duplicate: true}
	case !stoppedRun(r), r.Class == ClassEnvironment, r.Class == ClassUnavailable:
		return nil
	}
	// A fact about the posting and a challenge are holds over the whole ledger
	// (holdSet), which the user lifts by naming the company; they are not
	// verdicts of the day that retry-today could lift.
	if challengeStop(r) || (r.Class == ClassPosting && postingVerdictIsFact(r.Reason)) {
		return nil
	}
	newDocuments := saved.documents.After(r.Finished)
	waits := userDataStop(r) && !saved.data.After(r.Finished) && !newDocuments
	if !waits && isJudgementStop(r) && (!buildVouches(r.Build, build) || newDocuments) {
		return nil
	}
	t := &todayStop{
		reason:       fmt.Sprintf("stopped today at %s (%s): %s", r.Stage, r.Class, r.Reason),
		needsUser:    r.Class == ClassCredential,
		waitsForData: waits,
		owner:        ownerOfStop(r),
	}
	return t
}

// ownerOfStop says who has the next step of a stopped run.
func ownerOfStop(r RunFact) string {
	switch {
	case challengeStop(r):
		return ownerUser
	case r.Class == ClassCredential, r.Class == ClassAnswerGap, r.Class == ClassVerification:
		return ownerUser
	case r.Class == ClassPosting:
		return ownerPost
	case r.Class == ClassDefect:
		return ownerDefect
	}
	return ownerEnv
}

// holdSet is the ledger-backed state of terminal verdicts: the newest run of
// each posting over the complete ledger, and the exact account hosts a
// credential stop cools for 24 hours.
type holdSet struct {
	latest map[string]RunFact
	cool   map[string]gates.StopHold
}

func newHoldSet(runs []RunFact) *holdSet {
	h := &holdSet{latest: map[string]RunFact{}, cool: map[string]gates.StopHold{}}
	ordered := append([]RunFact(nil), runs...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Started.Before(ordered[j].Started) })
	for _, r := range ordered {
		for _, k := range r.keys() {
			h.latest[k] = r
		}
		for _, s := range r.Signins {
			if s.Succeeded != "" {
				delete(h.cool, strings.ToLower(s.Host))
			}
		}
		switch {
		case r.Kind == OutcomeSubmitted:
			delete(h.cool, hostOf(r.URL))
		case stoppedRun(r) && r.Class == ClassCredential:
			h.coolHosts(r)
		}
	}
	return h
}

// coolHosts cools the account hosts a credential stop is evidence about: the
// hosts of its sign-in records that made a refusal (a mirror's posting host is
// never the account host), the host a failed sign-up names, and, for a run
// that recorded no sign-in at all, the posting host of a system whose account
// lives on the employer's own domain. A held record made no new refusal and
// extends nothing; a shared board with no account-host evidence cools nothing.
func (h *holdSet) coolHosts(r RunFact) {
	hosts := map[string]bool{}
	for _, s := range r.Signins {
		if s.Succeeded == "" && (s.Exhausted || s.Lockout || s.Refusals > 0) {
			hosts[strings.ToLower(s.Host)] = true
		}
	}
	if m := accountExistsRE.FindStringSubmatch(r.Reason); m != nil {
		hosts[strings.ToLower(m[1])] = true
	}
	if len(hosts) == 0 && len(r.Signins) == 0 && (r.ATS == ATSWorkday || r.ATS == ATSGeneric) {
		hosts[hostOf(r.URL)] = true
	}
	for host := range hosts {
		if hold, ok := gates.HoldAfterStop(gates.ClassCredential, host, host != "", r.Finished.UnixMilli()); ok {
			h.cool[host] = hold
		}
	}
}

// holdInfo is a hold a list does not run through; closed when the posting
// said it is closed.
type holdInfo struct {
	reason string
	owner  string
	closed bool
}

// reason is why a row is held by the ledger, or false. A hold on a fact about
// the posting, a challenge or a credential cooldown lifts only when the user
// names the company; a judgement lifts on a new clean build.
func (h *holdSet) reason(rawURL, build string, now time.Time) (holdInfo, bool) {
	if r, ok := h.latest[postingKey(rawURL)]; ok && stoppedRun(r) {
		if challengeStop(r) && !mirrorChallengeExpired(r, build) {
			return holdInfo{"anti-bot challenge - complete verification yourself; name the company to retry after clearing it", ownerUser, false}, true
		}
		if r.Class == ClassPosting && (postingVerdictIsFact(r.Reason) || buildVouches(r.Build, build)) {
			return holdInfo{fmt.Sprintf("terminal posting verdict (%s): %s; name the company after rechecking the posting to retry", r.Started.UTC().Format("2006-01-02"), r.Reason), ownerPost,
				closedFactRE.MatchString(r.Reason)}, true
		}
	}
	host := hostOf(rawURL)
	if host != "" && gates.RowHeld(host, h.holds(), now.UnixMilli()) {
		return holdInfo{fmt.Sprintf("credential cooldown for %s until %s; fix sign-in and name the company to retry", host, time.UnixMilli(h.cool[host].End).UTC().Format(time.RFC3339)), ownerUser, false}, true
	}
	return holdInfo{}, false
}

func (h *holdSet) holds() []gates.StopHold {
	out := make([]gates.StopHold, 0, len(h.cool))
	for _, c := range h.cool {
		out = append(out, c)
	}
	return out
}
