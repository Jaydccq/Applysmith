// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"applysmith/internal/lazyregexp"
)

// Stance is what the posting says about work-authorization sponsorship.
type Stance string

// The four stances. Priority when several apply: restricted, refuses, offers.
const (
	StanceRestricted Stance = "restricted"
	StanceRefuses    Stance = "refuses"
	StanceOffers     Stance = "offers"
	StanceSilent     Stance = "silent"
)

// NoteMixed is the note of a silent stance caused by conflicting sentences.
const NoteMixed = "mixed"

const sponsorObject = `(visas?|h-?1b|work (visas?|authori[sz]ation|permits?)|employment (visas?|authori[sz]ation)|immigration|green cards?|tn|e-?3|opt)\b`

var (
	citizenWord  = lazyregexp.New(`(?i)\b(citizens?|citizenship|permanent residents?|green card)`)
	restrictAsk  = lazyregexp.New(`(?i)\b(must|required|requires?|requirement|only|eligible|limited to|restricted to|open to|need to|mandatory)\b`)
	restrictVoid = lazyregexp.New(`(?i)\b(regardless of|without regard|irrespective of)\b|\b(not|no)\b[^.]{0,25}\b(required|require|requirement|necessary|needed)\b`)

	refusals = compileAll(
		`\b(will|does|do|can|could|would|is|are|am)\s+(not|n't)\s+(currently\s+)?(be\s+able\s+to\s+)?(provide\s+|offer\s+|consider\s+|support\s+)?((visa|immigration|employment|work|h-?1b)\s+)*sponsor(ing|ship)?\b`,
		`\b(won't|don't|doesn't|cannot|can't|unable to)\s+(currently\s+)?(provide\s+|offer\s+|consider\s+|support\s+)?((visa|immigration|employment|work|h-?1b)\s+)*sponsor(ing|ship)?\b`,
		`\bno\s+((visa|immigration|employment|work|h-?1b)\s+)+sponsorship\b`,
		`\bwithout\s+(the\s+need\s+for\s+)?((visa|immigration|employment|work)\s+)*sponsorship\b`,
		`\b(not|isn't)\s+(currently\s+)?(offering|providing)\s+((visa|immigration|employment|work)\s+)*sponsorship\b`,
		`\bsponsorship\s+(is\s+)?not\s+(available|offered|provided)\b`,
		`\bnot\s+eligible\s+for\s+((visa|immigration|employment|work)\s+)*sponsorship\b`,
	)
	// offers needs the verb and its object side by side, so "sponsor a
	// meetup" does not count.
	offers = compileAll(
		`\bsponsor(s|ing)?\s+(an?\s+|the\s+|your\s+)?`+sponsorObject,
		`\b(visa|immigration|work|employment|h-?1b)\s+sponsorship\s+(is\s+|are\s+)?(available|offered|provided)\b`,
		`\b(offer|offers|offering|provide|provides|providing)\s+(visa|immigration|employment|work|h-?1b)\s+sponsorship\b`,
		`\b(will|can|may)\s+(provide|offer)\s+sponsorship\b`,
	)
)

// Sponsorship classifies the posting's stance one sentence at a time. A
// refusal sentence that still says the employer sponsors something is voided
// and counts as an offer. Different sentences giving both an offer and a
// refusal make the stance silent with the note "mixed".
func Sponsorship(text string) (Stance, string) {
	var restricted, refuses, offered bool
	for _, s := range Sentences(text) {
		if restrictedSentence(s) {
			restricted = true
		}
		switch {
		case refusalSentence(s):
			refuses = true
		case anyMatch(offers, s):
			offered = true
		}
	}
	switch {
	case restricted:
		return StanceRestricted, ""
	case refuses && offered:
		return StanceSilent, NoteMixed
	case refuses:
		return StanceRefuses, ""
	case offered:
		return StanceOffers, ""
	}
	return StanceSilent, ""
}

func restrictedSentence(s string) bool {
	if restrictVoid.MatchString(s) {
		return false
	}
	return RequiresUSPerson(s) || (citizenWord.MatchString(s) && restrictAsk.MatchString(s))
}

// refusalSentence is true when the sentence refuses sponsorship and does not
// also say the employer sponsors something once the refusal phrase is removed.
func refusalSentence(s string) bool {
	loc := firstMatch(refusals, s)
	if loc == nil {
		return false
	}
	return !anyMatch(offers, s[:loc[0]]+" "+s[loc[1]:])
}

func firstMatch(res []*lazyregexp.Regexp, s string) []int {
	var best []int
	for _, re := range res {
		if m := re.FindStringIndex(s); m != nil && (best == nil || m[0] < best[0]) {
			best = m
		}
	}
	return best
}
