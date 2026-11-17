package match

import (
	"strconv"
	"strings"

	"applysmith/internal/lazyregexp"
)

// notListedRE is a list's catch-all in its own words (8th Light: "My state is
// not listed here").
var notListedRE = lazyregexp.New(`(?i)\bnot\s+(?:listed|shown|included|on\s+(?:the|this)\s+list)\b`)

// stateListOther answers a list of US states that does not hold the recorded
// state with the list's one catch-all "Other" (n8n: fourteen states and
// "Other state" for an applicant in South Carolina): the state is outside the
// list, and the list says so in its own words, as a school list does for a
// school it lacks; "My state is not listed here" (8th Light) is that
// catch-all too. A list where any option names the state, in any spelling,
// is not this case; nor is one with no catch-all or with more than one. One
// state beside the catch-all is a state list only under a question that asks
// the state of residence (Verana Health: "What US State do you currently
// reside in?" over "CA" and "Other").
func stateListOther(s *Step, e *Entry) (Draft, bool) {
	if e.ID != "address-state" {
		return Draft{}, false
	}
	own := ""
	for _, p := range e.Preferred {
		if own = stateCodeOf(p); own != "" {
			break
		}
	}
	if own == "" {
		return Draft{}, false
	}
	// The name in any case; the code only as written ("OR" and "IN" are also words).
	name, ok := stateByAbbr[strings.ToLower(own)]
	if !ok {
		return Draft{}, false
	}
	alone := func(b, a rune) bool { return !asciiWordRune(b) && !asciiWordRune(a) }
	named := func(label string) bool {
		return phraseAt(strings.ToLower(label), strings.ToLower(name), alone) >= 0 || phraseAt(label, own, alone) >= 0
	}
	states := 0
	var other []Option
	for _, o := range realOptions(s.Q.Options) {
		switch c := stateCodeOf(o.Label); {
		case c == own, c == "" && named(o.Label):
			return Draft{}, false
		case c != "":
			states++
		case otherOptionRE.MatchString(o.Label) || notListedRE.MatchString(o.Label):
			other = append(other, o)
		}
	}
	if len(other) != 1 || states < 1 || states == 1 && !stateAskRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	d := optionDraft(optionBinding{Label: other[0].Label, Value: other[0].Value, Score: 1}, e, SourceDerived, true)
	d.Start = startProfileDerive
	d.Trace = []string{"state list: " + strconv.Itoa(states) + " states, none of them the recorded " + own + " -> the list's catch-all " + quoteTerm(other[0].Label)}
	return d, true
}
