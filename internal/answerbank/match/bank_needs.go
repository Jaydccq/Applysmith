package match

import (
	"slices"
	"strings"
)

// Gap is one entry that still needs the user's answer.
type Gap struct {
	ID       string
	Category string
	Kind     Kind
	// Blocking marks a gap that stops setup: a personal entry of the identity,
	// contact, address, work-authorization, employment, education,
	// compensation or self-identification groups, other than a conditional
	// explanation slot, a conditional slot whose gate is still open, a
	// years-of-experience claim, an optional free-text entry or an optional
	// personal entry (optionalIDs).
	Blocking bool
}

// NeedsReport is the one definition of "needs the user's answer".
type NeedsReport struct {
	// Unanswered lists entries with an empty body that are not blank by
	// design, not a satisfied conditional slot, not archived and not marked
	// ask-me.
	Unanswered []Gap
	// NeedsReview lists active entries whose default the user has not
	// confirmed, kept apart from Unanswered.
	NeedsReview []Gap
	// AskMe lists entries holding the ask-me marker: decisions, not blanks.
	AskMe []string
}

// Blocking returns the blocking subset of Unanswered.
func (r NeedsReport) Blocking() []Gap {
	var out []Gap
	for _, g := range r.Unanswered {
		if g.Blocking {
			out = append(out, g)
		}
	}
	return out
}

var blockingCategories = []string{
	CategoryIdentity, CategoryContact, CategoryAddress, CategoryWorkAuthorization, CategoryEmployment,
	CategoryEducation, CategoryCompensation, CategorySelfIdentification,
}

// optionalIDs are personal entries many people have nothing for, so a blank
// one is listed to answer but never stops setup. The owner's decision: the
// second address line is optional; the county stays required. Clearance,
// minors' work permits and years abroad concern few applicants, and the
// listed-states question is answered from the address when a form asks it.
var optionalIDs = []string{
	"address-line2",
	"willing-to-obtain-clearance", "clearance-citizenship-gated", "under-18-work-permit",
	"countries-lived-outside-us", "lived-outside-us-12-consecutive-months", "live-in-listed-states",
}

// NeedsAnswer applies the needs-answer rule to every entry of the bank, in
// bank order. Run it on the stamped bank so that entries the load answers
// (the career level) are not reported.
func NeedsAnswer(b Bank) NeedsReport {
	var r NeedsReport
	for _, e := range b.Entries {
		if e.Archived() {
			continue
		}
		g := Gap{ID: e.ID, Category: e.Category, Kind: e.Kind}
		if e.NeedsReview {
			r.NeedsReview = append(r.NeedsReview, g)
		}
		if BlankByDesign(e.ID) || satisfiedSlot(b, e) {
			continue
		}
		if e.AsksMe() {
			r.AskMe = append(r.AskMe, e.ID)
			continue
		}
		if !needsBody(e) {
			continue
		}
		g.Blocking = blocking(e) && !openGate(b, e)
		r.Unanswered = append(r.Unanswered, g)
	}
	return r
}

// needsBody applies the per-kind emptiness test.
func needsBody(e Entry) bool {
	switch e.Kind {
	case KindFixed:
		return entryBlank(e.Answer)
	case KindPickOption:
		if entryAnyText(e.Preferred) || !entryBlank(e.FallbackText) {
			return false
		}
		// A consent-style pick is chosen at fill time by its answer key; a
		// placeholder is never that.
		return e.Placeholder || e.AnswerKey == ""
	case KindCompensationRange:
		return entryBlank(e.DefaultAnswer)
	case KindMultiSelect:
		return e.Selected == nil
	}
	return false
}

// slotGate is the answer an entry depends on: the entry applies only when the
// gate is answered Yes (or, with whenYes, only when it is answered No).
type slotGate struct {
	gate    string
	whenYes bool
}

// conditionalSlots are the entries that only apply after another answer: a
// graduate has no expected graduation date, a citizen no authorization expiry.
// A slot whose gate is answered the other way is finished while blank.
var conditionalSlots = map[string]slotGate{
	"expected-graduation-date":             {gate: "currently-enrolled-student"},
	"expected-graduation-month":            {gate: "currently-enrolled-student"},
	"expected-graduation-term":             {gate: "currently-enrolled-student"},
	"expected-graduation-year":             {gate: "currently-enrolled-student"},
	"current-degree-year":                  {gate: "currently-enrolled-student"},
	"current-level-education-pursuing":     {gate: "currently-enrolled-student"},
	"degree-focus-pursuing":                {gate: "currently-enrolled-student"},
	"stem-opt-24-month-extension":          {gate: "currently-in-opt"},
	"stem-opt-extension-expiry":            {gate: "currently-in-opt"},
	"return-to-us-before-start":            {gate: "currently-located-outside-us"},
	"clearance-active-status":              {gate: "security-clearance-history"},
	"clearance-agency":                     {gate: "security-clearance-history"},
	"pr-another-country-after-citizenship": {gate: "citizenship-in-another-country"},
	"sponsorship-type-description":         {gate: "h1b-sponsorship"},
	"work-authorization-expiry":            {gate: "permanent-work-authorization", whenYes: true},
	"competing-offer-deadline":             {gate: "competing-offers"},
	// A citizen or permanent resident may once have held these statuses or
	// have a case open (a naturalization), which the status does not say:
	// setup asks them only of a temporary status, and a form that asks
	// anyone else leaves them to the user.
	"held-h1b-past-6-years":   {gate: "permanent-work-authorization", whenYes: true},
	"ever-held-j1-status":     {gate: "permanent-work-authorization", whenYes: true},
	"active-immigration-case": {gate: "permanent-work-authorization", whenYes: true},
}

// satisfiedSlot reports whether e is a "<gate>-reason" slot whose gate has a
// non-affirmative answer, or a conditional slot whose gate is answered the
// other way.
func satisfiedSlot(b Bank, e Entry) bool {
	g, ok := conditionalSlots[e.ID]
	if !ok {
		gateID, isReason := strings.CutSuffix(e.ID, ReasonSuffix)
		if !isReason || gateID == "" {
			return false
		}
		g = slotGate{gate: gateID}
	}
	gate, found := b.Find(g.gate)
	if !found || gate.Archived() {
		return false
	}
	answer := strings.TrimSpace(gateAnswer(gate))
	return answer != "" && answer != AskMe && affirmative(answer) == g.whenYes
}

// openGate reports whether e is a conditional slot whose gate does not yet say
// whether it applies: the gate is ask-me, so the application asks both, or it
// is a blocking blank, so setup still stops on the gate and asks the slot only
// once the gate is answered the way that makes it apply.
func openGate(b Bank, e Entry) bool {
	g, ok := conditionalSlots[e.ID]
	if !ok {
		return false
	}
	gate, found := b.Find(g.gate)
	if !found || gate.Archived() {
		return false
	}
	return gate.AsksMe() || needsBody(gate) && blocking(gate)
}

func gateAnswer(e Entry) string {
	switch e.Kind {
	case KindPickOption:
		if len(e.Preferred) > 0 {
			return e.Preferred[0]
		}
		return e.FallbackText
	case KindFixed:
		return e.Answer
	case KindCompensationRange:
		return e.DefaultAnswer
	}
	return ""
}

// affirmative reports whether an answer begins with yes, y or true as a word.
func affirmative(answer string) bool {
	lower := strings.ToLower(strings.TrimSpace(answer))
	for _, w := range []string{"yes", "y", "true"} {
		rest, ok := strings.CutPrefix(lower, w)
		if ok && (rest == "" || !isWordByte(rest[0])) {
			return true
		}
	}
	return false
}

func isWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func blocking(e Entry) bool {
	if !slices.Contains(blockingCategories, e.Category) || slices.Contains(optionalIDs, e.ID) {
		return false
	}
	if strings.HasSuffix(e.ID, ReasonSuffix) || strings.HasPrefix(e.ID, YearsExperiencePrefix) {
		return false
	}
	return !slices.Contains(e.InputTypes, InputLongText)
}
