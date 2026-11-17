package match

import (
	"fmt"
	"slices"
	"strings"
)

// Preferences are the profile preferences the load stamps onto the bank. The
// caller reads them from the profile on every load, so profile edits take
// effect on the next request.
type Preferences struct {
	// MaxDaysInOffice is preferences.max_days_in_office (0..7), nil when unset.
	MaxDaysInOffice *int
	// MaxTravelPercent is preferences.max_travel_percent (0..100), nil when unset.
	MaxTravelPercent *int
	// Relocation is the resolved relocation policy, nil while unconfigured.
	Relocation *RelocationPolicy
	// TargetSeniorities is the seniority of every target role.
	TargetSeniorities []string
	// Statements are the answers the structured setup steps already state.
	Statements []Statement
	// PayCurrency is the targets step's pay currency (an ISO code), the
	// denomination of a pay expectation the user wrote as a bare figure.
	PayCurrency string
}

// Statement is a fact the user stated in a structured setup step, written as
// the body of the bank entry that asks the same fact. The load stamps it onto
// that entry while the entry's own body is blank, so the user is never asked
// the same thing twice and the bank follows a later change of the step; an
// answer the user writes in the bank, or the ask-me marker, wins. Kind is the
// kind the entry must have; an entry of another kind is left alone.
type Statement struct {
	ID   string
	Step string // the setup step that holds the fact
	Kind Kind
	Body Body
}

// DegreeLevel is the level keyword of a degree name ("M.S." is "Master"), or
// "" when it names none.
func DegreeLevel(degree string) string { return degreeNameLevel(degree) }

const idSalaryExpectation = "salary-expectation"

// entryLevelOptions are the option labels preferred when every target role is
// an internship or junior role.
var entryLevelOptions = []string{"Entry Level", "Entry-Level", "New Grad", "New Graduate", "Early Career", "Junior"}

// Stamp returns the bank with the preference-derived runtime values applied.
// The receiver is not modified. Stamped bodies are marked so a save never
// writes them.
func (b Bank) Stamp(p Preferences) Bank {
	out := b
	out.Entries = slices.Clone(b.Entries)
	for i := range out.Entries {
		e := &out.Entries[i]
		switch e.ID {
		case IDOfficeAttendanceOK:
			if e.NumericThreshold != nil && p.MaxDaysInOffice != nil {
				t := *e.NumericThreshold
				v := *p.MaxDaysInOffice
				t.MaxValue = &v
				e.NumericThreshold = &t
			}
		case IDWillingToTravel:
			if e.Kind == KindPickOption && p.MaxTravelPercent != nil {
				e.Preferred = travelPreferred(*p.MaxTravelPercent)
				e.StampedBody = true
			}
		case idSalaryExpectation:
			// Setup writes the expectation as a figure alone; without its
			// currency a question naming that currency would abstain.
			if e.Kind == KindCompensationRange && entryBlank(e.Currency) && p.PayCurrency != "" &&
				!(entryBlank(e.DefaultAnswer) && entryBlank(e.Minimum) && entryBlank(e.Maximum)) {
				e.Currency = p.PayCurrency
				e.StampedCurrency = true
			}
		}
		if e.LocationPolicy != nil && p.Relocation != nil {
			lp := *e.LocationPolicy
			lp.Resolved = p.Relocation
			e.LocationPolicy = &lp
		}
	}
	if entryLevelOnly(p.TargetSeniorities) {
		out.stampCareerLevel()
	}
	out.stampStatements(p.Statements)
	return out
}

// stampStatements gives each blank, unarchived entry the body its statement
// holds and marks it as derived from that step.
func (b *Bank) stampStatements(list []Statement) {
	for _, st := range list {
		for i := range b.Entries {
			e := &b.Entries[i]
			if e.ID != st.ID {
				continue
			}
			if e.Kind == st.Kind && e.BodyEmpty() && !e.Archived() {
				e.Body = st.Body.clone()
				e.Placeholder = false
				e.StampedBody = true
				e.DerivedFrom = st.Step
			}
			break
		}
	}
}

// travelPreferred is the travel ceiling as the lists ask it: the percentage,
// the frequency word whose usual extent it covers (Trend Micro's Frequently /
// Occasionally / Rarely / No), then Yes or No.
func travelPreferred(percent int) []string {
	out := []string{fmt.Sprintf("Up to %d%%", percent)}
	switch {
	case percent >= 50:
		out = append(out, "Frequently")
	case percent >= 25:
		out = append(out, "Occasionally")
	case percent >= 10:
		out = append(out, "Rarely")
	}
	polarity := "No"
	if percent > 0 {
		polarity = "Yes"
	}
	return append(out, polarity)
}

func entryLevelOnly(seniorities []string) bool {
	if len(seniorities) == 0 {
		return false
	}
	for _, s := range seniorities {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "intern" && s != "junior" {
			return false
		}
	}
	return true
}

// stampCareerLevel makes desired-career-level prefer the entry-level labels
// when the entry is absent or has an empty body; the user's own answer wins.
func (b *Bank) stampCareerLevel() {
	for i := range b.Entries {
		e := &b.Entries[i]
		if e.ID != IDDesiredCareerLevel {
			continue
		}
		if e.Kind == KindPickOption && e.BodyEmpty() {
			e.Preferred = slices.Clone(entryLevelOptions)
			e.StampedBody = true
		}
		return
	}
	b.Entries = append(b.Entries, Entry{
		ID:          IDDesiredCareerLevel,
		Description: "Career level the applicant is looking for",
		Kind:        KindPickOption,
		Body:        Body{Preferred: slices.Clone(entryLevelOptions), FallbackText: ""},
		Meta:        Meta{Category: CategoryEmployment, InputTypes: []string{InputSingleSelect}},
		Runtime:     Runtime{StampedBody: true},
	})
}
