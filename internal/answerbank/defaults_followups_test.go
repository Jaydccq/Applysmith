package answerbank

import (
	"context"
	"testing"

	"applysmith/internal/answerbank/match"
)

// These tests pin the shape of the shipped entries against the control kinds
// the specification's scenarios fill: an entry that rejects the control its
// own scenario uses abstains with input-mismatch whatever the user recorded.

// withEntry returns the default bank with one entry changed.
func withEntry(t *testing.T, id string, edit func(*match.Entry)) match.Bank {
	t.Helper()
	b := defaultBank(t)
	for i := range b.Entries {
		if b.Entries[i].ID == id {
			edit(&b.Entries[i])
			return b
		}
	}
	t.Fatalf("the default bank has no entry %q", id)
	return b
}

func matchFacts(t *testing.T, b match.Bank, f match.Facts, today match.Date, q match.Question) match.Result {
	t.Helper()
	resp, err := match.New(b, match.DefaultStages()).Match(match.Request{Host: "jobs.example.com", Facts: f, Today: today, Questions: []match.Question{q}})
	if err != nil || len(resp.Results) != 1 {
		t.Fatalf("err %v, %d results", err, len(resp.Results))
	}
	return resp.Results[0]
}

func TestDefaultBank_SkillGateUsesProfileSkillMode(t *testing.T) {
	// Answer bank spec, "Compensation, experience and proficiency ladders":
	// "the default bank uses only profile_skill".
	e := mustFind(t, defaultBank(t), "skill-experience-knowledge-gate")
	if e.SkillMode != match.SkillProfileSkill {
		t.Fatalf("skill_mode %q, want %q", e.SkillMode, match.SkillProfileSkill)
	}
	for _, e := range defaultBank(t).Entries {
		if e.SkillMode == match.SkillManualYes {
			t.Errorf("%s ships manual_yes; the default bank uses only profile_skill", e.ID)
		}
	}
}

func TestDefaultBank_SalaryExpectationFillsAFloorBracketSelect(t *testing.T) {
	// Spec scenario "Floor brackets".
	b := withEntry(t, "salary-expectation", func(e *match.Entry) {
		e.Placeholder, e.Minimum, e.Maximum = false, "90000", "120000"
	})
	r := matchFacts(t, b, match.Facts{}, match.Date{}, match.Question{
		Selector: "#q", Kind: match.KindSelect, Text: "What are your salary expectations?",
		Options: opts("$50,000+", "$75,000+", "$100,000+", "$125,000+"),
	})
	if r.EntryID != "salary-expectation" || r.ChosenLabel != "$75,000+" {
		t.Fatalf("got %+v", r)
	}
}

func TestDefaultBank_AgeBracketAnswersABareAgeBox(t *testing.T) {
	// Spec, "Derived answers from stored facts": "A bare age box receives the number."
	for _, kind := range []match.InputType{"", match.InputText, match.InputNumber} {
		r := matchFacts(t, defaultBank(t), match.Facts{DateOfBirth: "2005-06-15"}, match.Date{Year: 2031, Month: 6, Day: 14}, match.Question{
			Selector: "#q", Kind: match.KindText, InputType: kind, Text: "What is your age?",
		})
		if r.EntryID != "current-age-bracket" || r.Answer != "25" {
			t.Errorf("input type %q: got %+v", kind, r)
		}
	}
}

var wotcQuestions = map[string]string{
	"wotc-long-term-unemployed":      "Have you been unemployed for at least 27 consecutive weeks?",
	"wotc-snap-recipient":            "Have you received Supplemental Nutrition Assistance Program benefits in the past six months?",
	"wotc-ssi-recipient":             "Have you received supplemental security income benefits?",
	"wotc-tanf-family":               "Has your family received TANF benefits in the past 18 months?",
	"wotc-vocational-rehab-referral": "Vocational rehabilitation program participant",
}

func TestDefaultBank_WOTCPickersFillRadiosAndSelects(t *testing.T) {
	// The screening answers are voluntary: the default prefers the decline
	// option, whether the page renders Yes/No/decline as radios (a yes/no set)
	// or as a longer select.
	b := defaultBank(t)
	controls := map[string]struct {
		kind match.ControlKind
		opts []match.Option
	}{
		"radios with a decline option": {match.KindRadioGroup, opts("Yes", "No", "I prefer not to say")},
		"select":                       {match.KindSelect, opts("Yes", "No", "Not sure", "I prefer not to say")},
	}
	for id, text := range wotcQuestions {
		for name, c := range controls {
			t.Run(id+"/"+name, func(t *testing.T) {
				r := matchFacts(t, b, match.Facts{}, match.Date{}, match.Question{Selector: "#q", Kind: c.kind, Text: text, Options: c.opts})
				if r.EntryID != id || r.Outcome != match.OutcomeFilledReview || r.ChosenLabel != "I prefer not to say" {
					t.Fatalf("got %+v", r)
				}
			})
		}
		// With no decline option on the page the picker is left to the user: a
		// benefit-status picker is never answered "No" on the user's behalf.
		t.Run(id+"/no decline option", func(t *testing.T) {
			r := matchFacts(t, b, match.Facts{}, match.Date{}, match.Question{Selector: "#q", Kind: match.KindRadioGroup, Text: text, Options: opts("Yes", "No")})
			if r.Outcome != match.OutcomeAbstained || r.ChosenLabel != "" {
				t.Fatalf("got %+v", r)
			}
		})
	}
}

func TestDefaultBank_WOTCFelonyAnswersRadiosAndSelects(t *testing.T) {
	b := withEntry(t, "wotc-felony-year-before-hire", func(e *match.Entry) { e.Placeholder, e.Answer = false, "No" })
	for name, c := range map[string][]match.Option{
		"yes/no radios": opts("Yes", "No"),
		"select":        opts("Yes", "No", "Not sure", "I prefer not to say"),
	} {
		t.Run(name, func(t *testing.T) {
			kind := match.KindSelect
			if len(c) == 2 {
				kind = match.KindRadioGroup
			}
			r := matchFacts(t, b, match.Facts{}, match.Date{}, match.Question{
				Selector: "#q", Kind: kind, Options: c, Text: "Were you convicted of a felony in the year before the hire date?",
			})
			if r.EntryID != "wotc-felony-year-before-hire" || r.ChosenLabel != "No" {
				t.Fatalf("got %+v", r)
			}
		})
	}
}

func TestDefaultBank_VisaStatusTextBoxAcceptsTheRecordedStatus(t *testing.T) {
	// Spec, "Control shape acceptance": a multi-line box never receives a bare
	// scalar unless the entry lists the type, and "describe" makes the inferred
	// input type long_text. The user's visa status is a short fact that answers
	// both the one-line label and the describe wording.
	b := withEntry(t, "current-immigration-status", func(e *match.Entry) { e.Placeholder, e.FallbackText = false, "H-1B" })
	for _, text := range []string{"Current visa status", "Please describe your current visa status"} {
		t.Run(text, func(t *testing.T) {
			r := matchFacts(t, b, match.Facts{}, match.Date{}, match.Question{Selector: "#q", Kind: match.KindText, Text: text})
			if r.EntryID != "current-immigration-status" || r.Answer != "H-1B" {
				t.Fatalf("got %+v", r)
			}
		})
	}
}

// A question that asks for a date, month or year is never answered Yes or No,
// whatever the control: the did-you-graduate route leaves it to the
// graduation-date resolver, which formats the date from the education record,
// or to an abstain when no record exists.
func TestDefaultBank_GraduationDateAskIsNeverAYesNo(t *testing.T) {
	b := withEntry(t, "did-you-graduate", func(e *match.Entry) { e.Placeholder, e.Answer = false, "Yes" })
	today := match.Date{Year: 2031, Month: 1, Day: 1}
	record := match.Facts{Education: []match.EducationFact{{School: "Example State University", Degree: "BS", Start: "2020-09", End: "2024-05"}}}
	controls := map[string]match.Question{
		"text":   {Selector: "#q", Kind: match.KindText},
		"date":   {Selector: "#q", Kind: match.KindText, InputType: match.InputDate},
		"select": {Selector: "#q", Kind: match.KindSelect, Options: opts("Select...", "May 2023", "May 2024", "June 2024")},
	}
	wordings := []string{
		"In what month and year did you graduate?",
		"What year did you graduate?",
		"When did you graduate?",
		"On what date did you graduate from college?",
	}
	for _, text := range wordings {
		for name, base := range controls {
			q := base
			q.Text = text
			t.Run(name+"/"+text, func(t *testing.T) {
				for _, f := range []struct {
					name  string
					facts match.Facts
				}{{"no education record", match.Facts{}}, {"education record", record}} {
					r := matchFacts(t, b, f.facts, today, q)
					if r.Answer == "Yes" || r.Answer == "No" || r.ChosenLabel == "Yes" || r.ChosenLabel == "No" || (r.EntryID == "did-you-graduate" && r.Outcome != match.OutcomeAbstained) {
						t.Errorf("%s: a date ask got a yes/no answer: %+v", f.name, r)
					}
					if f.name == "no education record" && r.Outcome != match.OutcomeAbstained {
						t.Errorf("%s: want an abstain, got %+v", f.name, r)
					}
				}
			})
		}
	}
	t.Run("the yes/no question still answers", func(t *testing.T) {
		r := matchFacts(t, b, match.Facts{}, today, match.Question{Selector: "#q", Kind: match.KindRadioGroup, Options: yesNo, Text: "Did you graduate?"})
		if r.EntryID != "did-you-graduate" || r.ChosenLabel != "Yes" {
			t.Fatalf("got %+v", r)
		}
		r = matchFacts(t, b, match.Facts{}, today, match.Question{Selector: "#q", Kind: match.KindRadioGroup, Options: yesNo, Text: "Did you graduate this year?"})
		if r.EntryID != "did-you-graduate" || r.ChosenLabel != "Yes" {
			t.Fatalf("a yes/no question that mentions a year: %+v", r)
		}
	})
}

func TestDefaultBank_EmployerHistoryEntriesShipYesNo(t *testing.T) {
	// The ever-worked and currently-employed questions are two entries of the
	// employer-history kind (spec, "Acknowledgement, employer-history and
	// multi-select entries"); the audit-firm route targets a generic id. By the
	// owner's decision they no longer ship blank: they answer Yes/No from the
	// employers list and the profile's work history.
	b := defaultBank(t)
	for _, id := range []string{"employer-history", "currently-employed-by-this-company"} {
		e := mustFind(t, b, id)
		if e.Kind != match.KindEmployerHistory || e.Placeholder || e.AnswerIfMatch != "Yes" || e.AnswerIfNoMatch != "No" || !hasNonBlank(e.Examples) {
			t.Errorf("%s: %+v", id, e)
		}
	}
	requireBlankFixed(t, mustFind(t, b, "audit-firm-employment"))
}

// A named programme is answered only by an entry that names it, never the
// generic graduation entry (route, fuzzy or pin): without one it is an open
// question for the evidence lanes and F-10's search; a family member's
// government employment is the family entry's, never the address state.
func TestDefaultBank_NamedProgramAndFamilyGovernmentRouting(t *testing.T) {
	b := withEntry(t, "did-you-graduate", func(e *match.Entry) { e.Placeholder, e.Answer = false, "Yes" })
	q := func(text string) match.Question {
		return match.Question{Selector: "#q", Kind: match.KindRadioGroup, Options: yesNo, Text: text}
	}
	riseUp := "Are you a graduate of the RiseUp with ServiceNow (formerly NextGen) Program?"
	for _, text := range []string{riseUp, "Did you graduate from the RiseUp program?", "Are you an alumnus of our Early Careers Academy?", "Have you completed the Year Up bootcamp?"} {
		r := matchFacts(t, b, match.Facts{}, match.Date{}, q(text))
		p, _ := match.New(b, match.DefaultStages()).MatchPinned(context.Background(), match.Request{Host: "jobs.example.com"}, q(text), "did-you-graduate")
		if r.Outcome != match.OutcomeAbstained || r.Terminal || r.EntryID != "" || p.Outcome != match.OutcomeAbstained {
			t.Errorf("%q: want an open abstain that names no entry, got %+v, pinned %+v", text, r, p)
		}
	}
	if r := matchFacts(t, b, match.Facts{}, match.Date{}, q("Did you graduate from a degree program?")); r.ChosenLabel != "Yes" {
		t.Errorf("a generic programme: got %+v", r)
	}
	b.Entries = append(b.Entries, match.Entry{ID: "riseup-graduate", Kind: match.KindFixed, Body: match.Body{Answer: "No"},
		Meta: match.Meta{InputTypes: []string{"yes_no"}, Examples: []string{"Are you a graduate of the RiseUp with ServiceNow program?"}}})
	if r := matchFacts(t, b, match.Facts{}, match.Date{}, q(riseUp)); r.EntryID != "riseup-graduate" || r.ChosenLabel != "No" {
		t.Errorf("the user's own programme entry: got %+v", r)
	}
	b = withEntry(t, "government-position-family", func(e *match.Entry) { e.Placeholder, e.Answer = false, "No" })
	for _, text := range []string{
		"D. Within the past 5 years, has a member of your Immediate Family (spouse or child ONLY) ever been an employee of a federal, state, local, municipal, provincial or quasi-governmental entity in the U.S. or any other country?",
		"Have you or a member of your immediate family been employed by the federal government?",
	} {
		if r := matchFacts(t, b, match.Facts{}, match.Date{}, q(text)); r.EntryID != "government-position-family" || r.ChosenLabel != "No" {
			t.Errorf("%q: got %+v", text, r)
		}
	}
}
