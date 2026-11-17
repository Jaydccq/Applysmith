package match

import (
	"fmt"

	"applysmith/internal/lazyregexp"
)

// A state notice ("California Applicants:") may offer, beside its
// acknowledgement, an option that says the notice is not the applicant's: "I
// am not a California applicant" (Alnylam). That option states where the
// applicant lives and works, so it is no decline. It is chosen when the
// recorded state and the posting's place both lie outside the named state; the
// notice is acknowledged as any other when either is in it; a recorded state
// or a posting place that is missing leaves the control to the user.

var outOfStateOptionRE = lazyregexp.New(`(?i)^\s*(?:i\s+am|i'm)\s+not\s+an?\s+(` + stateNameAlternation + `)\s+(?:applicant|resident|candidate)s?\b`)

// outOfStateOption is the one option that places the applicant outside a named
// state, with that state's code.
func outOfStateOption(opts []Option) (Option, string, bool) {
	var found []Option
	code := ""
	for _, o := range opts {
		if m := outOfStateOptionRE.FindStringSubmatch(o.Label); m != nil {
			found, code = append(found, o), stateCodeOf(m[1])
		}
	}
	if len(found) != 1 || code == "" {
		return Option{}, "", false
	}
	return found[0], code, true
}

// stateNoticeAck answers an acknowledgement that offers an out-of-state option.
// It reports false when there is no such option or when the notice is the
// applicant's, which the acknowledgement itself then answers.
func stateNoticeAck(s *Step) (Draft, bool) {
	out, code, ok := outOfStateOption(realOptions(s.Q.Options))
	if !ok {
		return Draft{}, false
	}
	home := stateCodeOfEntry(s)
	job, _ := jobPlaceOf(s.Req.Posting)
	jobState := stateCodeOf(job.Region)
	if home == code || jobState == code {
		return Draft{}, false
	}
	if home == "" || jobState == "" && !job.Remote {
		return Draft{Abstain: &Abstain{Reason: ReasonAmbiguous}, Trace: []string{fmt.Sprintf(
			"state notice: whether the %s notice is the applicant's needs the recorded state (%q) and the posting's place (%q): left for the user",
			code, home, s.Req.postingLocation())}}, true
	}
	where := jobState
	if job.Remote {
		where = "remote"
	}
	return Draft{Answer: out.Value, ChosenLabel: out.Label, Source: SourceDerived, Start: startProfileDerive, Trace: []string{fmt.Sprintf(
		"state notice: the record (%s) and the posting (%s) are outside %s -> %q", home, where, code, out.Label)}}, true
}
