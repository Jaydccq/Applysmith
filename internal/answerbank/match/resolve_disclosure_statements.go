package match

import (
	"cmp"
	"strings"

	"applysmith/internal/lazyregexp"
)

// plainUSRE is "u.s" or "u.s.a" as a plain label writes it, its last stop
// gone; the clause readers read "us".
var plainUSRE = lazyregexp.New(`\bu\.s(?:\.a)?\.?(\s|$)`)

// workAuthStatementList reports plain labels that are each one
// work-authorization or sponsorship statement, both kinds among them: a list
// of statements to tick rather than one answer to pick.
func workAuthStatementList(ls []string) bool {
	var auth, sponsor bool
	for _, l := range ls {
		_, kind, ok := clauseOf(plainUSRE.ReplaceAllString(l, "us$1"), "", false)
		if !ok {
			return false
		}
		auth, sponsor = auth || kind == clauseAuth, sponsor || kind == clauseSponsor
	}
	return len(ls) >= 2 && auth && sponsor
}

// workAuthStatements answers a list of work-authorization and sponsorship
// statements to tick (Ideogram: "Work Authorization in the U.S." over "I am
// authorized to work in the U.S." and "I will need sponsorship to work in the
// U.S."): every statement the recorded answers make true is ticked, so an
// applicant authorized now who will need sponsorship ticks both. A statement of
// both ("I am authorized to work and will not need sponsorship") holds when
// both its clauses do. A statement no clause reader reads, or a fact left
// unset, leaves the list to the user.
func workAuthStatements(s *Step) Draft {
	authorized, authKnown := storedWorkAuth(s)
	company := s.Req.Posting.Company
	authHolds := func(negated bool) (bool, *Draft) {
		if !authKnown {
			d := abstainDraft(ReasonPlaceholderEmpty, "", "the recorded work-authorization answer is unset")
			return false, &d
		}
		return authorized != negated, nil
	}
	sponsorHolds := func(text string, negated bool) (bool, *Draft) {
		need, known := sponsorshipNeed(s.Req.Facts, sponsorshipScope(text))
		if !known {
			d := abstainDraft(ReasonPlaceholderEmpty, "", "the sponsorship fact %q reads is unset", text)
			return false, &d
		}
		return need != negated, nil
	}
	var chosen []string
	for _, o := range realOptions(s.Q.Options) {
		var holds bool
		var stop *Draft
		if negated, kind, ok := clauseOf(o.Label, company, false); ok {
			if kind == clauseAuth {
				holds, stop = authHolds(negated)
			} else {
				holds, stop = sponsorHolds(o.Label, negated)
			}
		} else if c, ok := readCompound(o.Label, company); ok {
			a, astop := authHolds(c.authNeg)
			sp, sstop := sponsorHolds(c.sponsorText, c.sponsorNeg)
			holds, stop = a && sp, cmp.Or(astop, sstop)
		} else {
			return abstainDraft(ReasonAmbiguous, "", "work-authorization statements: %q is no statement the readers know", o.Label)
		}
		if stop != nil {
			return *stop
		}
		if holds {
			chosen = append(chosen, o.Label)
		}
	}
	if len(chosen) == 0 {
		return abstainDraft(ReasonAmbiguous, "", "work-authorization statements: none holds under the recorded answers")
	}
	d := entryDraft(strings.Join(chosen, ", "), "work-authorization statements under the recorded answers -> ["+strings.Join(chosen, ", ")+"]")
	d.HasOption, d.OptionScore, d.ChosenLabel, d.ChosenLabels = true, 1, chosen[0], chosen
	return d
}
