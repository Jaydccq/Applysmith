package answerbank

import (
	"slices"
	"strings"

	"applysmith/internal/answerbank/match"
)

// The self-example invariant gives every shipped entry a recorded user answer
// that fits its kind, builds a control of every input type the entry accepts
// and asks each of the entry's own examples through the real matcher. This file
// holds the builders; selfexample_test.go holds the invariant and its allowlist.

const seStart = "2027-01-15"

var seToday = match.Date{Year: 2026, Month: 10, Day: 11}

// seFacts is the fictional applicant "Alex Example": the profile values the
// derived and profile-grounded entries are answered from.
func seFacts() match.Facts {
	years := 3.0
	return match.Facts{
		Education: []match.EducationFact{
			{School: "Example State University", Degree: "Master of Science", Field: "Computer Science", Start: "2024-09", End: "2026-05", GradeAverage: "3.9"},
			{School: "Example College", Degree: "Bachelor of Science", Field: "Mathematics", Start: "2020-09", End: "2024-05", GradeAverage: "3.7"},
		},
		Employment: []match.EmploymentFact{
			{Employer: "Example Corp", Title: "Software Engineer", Start: "2024-06", End: "present", Location: "Springfield, IL"},
			{Employer: "Acme Test", Title: "Intern", Start: "2023-06", End: "2023-08"},
		},
		Skills:                    []match.SkillFact{{Name: "Python", Years: &years, Level: "advanced"}, {Name: "Go", Level: "beginner"}},
		AuthorizedCountries:       []string{"US"},
		Citizenships:              []string{"IN"},
		DateOfBirth:               "2000-06-15",
		VisaStatus:                "F-1 OPT",
		RequiresSponsorshipNow:    boolPtr(false),
		RequiresSponsorshipFuture: boolPtr(true),
		Address:                   match.AddressFact{Line1: "1 Example Way", City: "Springfield", Region: "IL", PostalCode: "62701", Country: "US"},
	}
}

// seCompanions are the answers the user recorded for the entries a derived
// entry composes its answer from.
var seCompanions = map[string]map[string]string{
	match.IDWorkAuthorizationDetail: {"work-authorization-expiry": seStart, "stem-opt-extension-expiry": "2028-01-15"},
	match.IDFullTimeStartAvailable:  {match.IDEarliestStartDate: seStart},
	// A list of statements to tick reads the recorded work-authorization answer.
	"work-auth-and-future-sponsorship": {"work-auth-us": "Yes"},
}

// seOptionSets are the choices of the selects whose answer is read from the
// facts or whose question has a closed vocabulary of its own. A key is an entry
// id, or an entry id and an example joined by "|"; every other select offers
// Yes and No for a yes/no entry and three generic options otherwise.
var seOptionSets = map[string][]string{
	"willing-to-travel|What percentage of travel are you comfortable with?": {"Up to 10%", "Up to 25%", "Up to 50%"},

	match.IDCurrentAgeBracket:                {"Under 18", "18-24", "25-34", "35 and over"},
	match.IDFullTimeStartAvailable:           {"Immediately", "1 month", "4 months"},
	"years-full-time-work-experience-number": {"0-1 years", "2-4 years", "5+ years"},
	"address-county":                         {"Option A", "Alex Example", "Option C"},
	"preferred-method-of-contact":            {"Option A", "Alex Example", "Option C"},
	"work-country":                           {"Option A", "Alex Example", "Option C"},
	"most-recent-school":                     {"Option A", "Alex Example", "Option C"},
	"skill-checklist":                        {"Java", "Python", "Rust"},
	"work-auth-and-future-sponsorship": {
		"I am authorized to work and will not need sponsorship",
		"I am authorized to work now and will need sponsorship in the future",
		"I am not authorized to work",
	},
}

// seDerived reports an entry answered from the facts, never from a stored body.
func seDerived(e match.Entry) bool {
	return slices.Contains(match.DerivedIDs, e.ID) || e.ID == match.IDCurrentCompany
}

// seYesNoSelect reports a select that asks a yes/no question: the entry also
// answers yes/no controls, or it is an employer-history entry.
func seYesNoSelect(e match.Entry) bool {
	return slices.Contains(e.InputTypes, string(match.InputYesNo)) || e.Kind == match.KindEmployerHistory
}

// seOptions are the choices a select or a checkbox group offers for the entry.
func seOptions(e match.Entry, example string) []match.Option {
	labels := []string{"Option A", "Option B", "Option C"}
	if set, ok := seOptionSets[e.ID+"|"+example]; ok {
		labels = set
	} else if set, ok := seOptionSets[e.ID]; ok {
		labels = set
	} else {
		switch {
		case e.Kind == match.KindCompensationRange:
			labels = []string{"$80,000 - $99,999", "$100,000 - $119,999", "$120,000 - $139,999"}
		case e.Kind == match.KindLanguageProficiency:
			labels = []string{"Basic", "Fluent", "Native"}
		case e.Category == match.CategorySelfIdentification && len(e.Preferred) > 0:
			labels = []string{"Option A", e.Preferred[0], "Option C"}
		case seYesNoSelect(e):
			labels = []string{"Yes", "No"}
		}
	}
	out := make([]match.Option, len(labels))
	for i, l := range labels {
		out[i] = match.Option{Value: strings.ToLower(strings.ReplaceAll(l, " ", "-")), Label: l}
	}
	return out
}

// seQuestion builds a control of the input type that asks the example.
func seQuestion(e match.Entry, example string, input match.InputType, required bool) match.Question {
	q := match.Question{Selector: "#q", Text: example, Kind: match.KindText, InputType: input, Required: required}
	switch input {
	case match.InputYesNo:
		q.Kind, q.Options = match.KindRadioGroup, []match.Option{{Value: "yes", Label: "Yes"}, {Value: "no", Label: "No"}}
	case match.InputSingleSelect:
		q.Kind, q.Options = match.KindSelect, seOptions(e, example)
	case match.InputMultiSelect, match.InputCheckbox:
		q.Kind, q.Options = match.KindCheckbox, seOptions(e, example)
		if e.Kind == match.KindAutoAcknowledge {
			q.Options = []match.Option{{Value: "on", Label: "I agree"}}
		}
	}
	return q
}

// seRecord is the entry after the user recorded an answer that fits its kind
// for a control of the input type. A derived entry keeps its shipped blank body;
// a self-identification entry keeps the shipped preference for the option that
// declines, except on a yes/no control, which has no such option.
func seRecord(e match.Entry, example string, input match.InputType) match.Entry {
	if seDerived(e) {
		return e
	}
	e.Placeholder = false
	switch e.Kind {
	case match.KindFixed:
		e.Answer = seFixedAnswer(input)
		if input == match.InputSingleSelect && seYesNoSelect(e) {
			e.Answer = "Yes"
		}
		// A number asked through a menu of ranges is chosen by the range that holds it.
		if input == match.InputSingleSelect && slices.Contains(e.InputTypes, string(match.InputNumber)) {
			e.Answer = seFixedAnswer(match.InputNumber)
		}
	case match.KindPickOption:
		if e.Category != match.CategorySelfIdentification || input == match.InputYesNo {
			e.Preferred = []string{sePicked(e, example, input)}
		}
		e.FallbackText = seFixedAnswer(input)
	case match.KindCompensationRange:
		e.Minimum, e.Maximum, e.DefaultAnswer, e.Currency = "100000", "120000", "110000", "USD"
	case match.KindLanguageProficiency:
		e.Language, e.Speaking, e.Listening, e.Reading, e.Writing = "English", "Fluent", "Fluent", "Native", "Advanced"
	case match.KindEmployerHistory:
		e.AnswerIfMatch, e.AnswerIfNoMatch = "Yes", "No"
	case match.KindMultiSelect:
		e.Selected = []string{"Option B", "Option C"}
		e.AllowedOptions = []string{"Option A", "Option B", "Option C"}
	}
	return e
}

// sePicked is the preferred option the user recorded: Yes for a yes/no control
// (No for a self-identification question), the middle option of a select.
func sePicked(e match.Entry, example string, input match.InputType) string {
	switch {
	case input == match.InputYesNo && e.Category == match.CategorySelfIdentification:
		return "No"
	case input == match.InputYesNo:
		return "Yes"
	case input == match.InputSingleSelect || input == match.InputCheckbox:
		opts := seOptions(e, example)
		return opts[len(opts)/2].Label
	}
	return "Option B"
}

// seFixedAnswer is the value the user typed for a fixed fact of the input type.
func seFixedAnswer(input match.InputType) string {
	switch input {
	case match.InputYesNo:
		return "Yes"
	case match.InputDate:
		return seStart
	case match.InputNumber:
		return "4"
	case match.InputURL:
		return "https://example.com/alex"
	case match.InputLongText:
		return "Alex Example has four years of relevant experience."
	}
	return "Alex Example"
}

// seInputTypes are the input types the entry accepts, one control each.
func seInputTypes(e match.Entry) []match.InputType {
	var out []match.InputType
	for _, t := range e.InputTypes {
		out = append(out, match.InputType(t))
	}
	if len(out) == 0 {
		out = []match.InputType{match.InputText}
	}
	return slices.Compact(out)
}
