package answerbank

import (
	"slices"
	"strings"
	"testing"

	"applysmith/internal/answerbank/match"
)

// The entries the matcher's routes name must ship in the shape the routes rely
// on. Every fact entry ships blank (a placeholder), so an unconfigured install
// abstains instead of asserting a fact for the user.

func mustFind(t *testing.T, b match.Bank, id string) match.Entry {
	t.Helper()
	e, ok := b.Find(id)
	if !ok {
		t.Fatalf("the default bank has no entry %q", id)
	}
	return e
}

func requireBlankFixed(t *testing.T, e match.Entry) {
	t.Helper()
	if e.Kind != match.KindFixed || e.Answer != "" || !e.Placeholder {
		t.Errorf("%s must ship as a blank placeholder: kind %s, answer %q, placeholder %v", e.ID, e.Kind, e.Answer, e.Placeholder)
	}
}

func hasNonBlank(list []string) bool {
	return slices.ContainsFunc(list, func(s string) bool { return strings.TrimSpace(s) != "" })
}

var yesNo = []match.Option{{Value: "yes", Label: "Yes"}, {Value: "no", Label: "No"}}

func TestDefaultBank_UnconfiguredInstallAbstainsOnTheConvictionQuestion(t *testing.T) {
	r := matchFacts(t, defaultBank(t), match.Facts{}, match.Date{}, match.Question{
		Selector: "#q", Kind: match.KindSelect, Required: true, Options: yesNo,
		Text: "Have you been convicted for a criminal offence in the past ten years in any country, for which a pardon has not been granted?",
	})
	if r.Answer != "" || r.Outcome == match.OutcomeFilled || r.EntryID != "criminal-conviction-history" {
		t.Fatalf("an unconfigured install must abstain on the conviction entry: %+v", r)
	}
}

// The office-days entry ships blank, and the profile's maximum is the user's own
// fact: a question above it is answered from the profile, a question within it
// is not (the stored answer is the user's to give), and no recorded maximum
// answers nothing.
func TestDefaultBank_OfficeDaysAboveTheProfileMaximumAnswersNoFromABlankEntry(t *testing.T) {
	two := 2
	stamped := defaultBank(t).Stamp(match.Preferences{MaxDaysInOffice: &two})
	ask := func(b match.Bank, text string) match.Result {
		return matchFacts(t, b, match.Facts{}, match.Date{}, match.Question{Selector: "#q", Kind: match.KindRadioGroup, Required: true, Options: yesNo, Text: text})
	}
	for _, text := range []string{
		"Are you able to work onsite in downtown Indianapolis at least four days per week? *",
		"This role requires onsite presence at our San Jose office 3 days per week. Are you able to meet this requirement?",
	} {
		r := ask(stamped, text)
		if r.Outcome != match.OutcomeFilled || r.EntryID != "office-attendance-ok" || r.ChosenLabel != "No" {
			t.Errorf("%q: %+v", text, r)
		}
		if r := ask(defaultBank(t), text); r.Outcome == match.OutcomeFilled {
			t.Errorf("%q: no maximum is recorded, yet it filled %q", text, r.ChosenLabel)
		}
	}
	within := "Are you able to work onsite at least two days per week?"
	if r := ask(stamped, within); r.Outcome == match.OutcomeFilled {
		t.Errorf("%q is within the maximum and the entry is blank, yet it filled %q", within, r.ChosenLabel)
	}
}

// "Who referred you" asks for a name; the Yes/No "were you referred" is the
// other entry.
func TestDefaultBank_WhoReferredYouIsTheReferrersNameBox(t *testing.T) {
	b := defaultBank(t)
	for i := range b.Entries {
		if b.Entries[i].ID == "referrer-name" {
			b.Entries[i].Placeholder = false
			b.Entries[i].Answer = "Not referred"
		}
	}
	r := matchFacts(t, b, match.Facts{}, match.Date{}, match.Question{Selector: "#q", Kind: match.KindText, Required: true, Text: "Who referred you for this position?"})
	if r.Outcome != match.OutcomeFilled || r.EntryID != "referrer-name" || r.Answer != "Not referred" {
		t.Fatalf("got %+v", r)
	}
	r = matchFacts(t, b, match.Facts{}, match.Date{}, match.Question{Selector: "#q", Kind: match.KindRadioGroup, Required: true, Options: yesNo, Text: "Were you referred by a current employee?"})
	if r.EntryID == "referrer-name" || r.Outcome == match.OutcomeFilled {
		t.Fatalf("a Yes/No referral question was answered by %q: %+v", r.EntryID, r)
	}
}

func TestDefaultBank_BasedInUSOrCanadaFillsYesOnceTheUserPrefersIt(t *testing.T) {
	b := defaultBank(t)
	for i := range b.Entries {
		if b.Entries[i].ID == "based-in-us-or-canada" {
			b.Entries[i].Placeholder = false
			b.Entries[i].Preferred = []string{"Yes", "United States", "US"}
		}
	}
	r := matchFacts(t, b, match.Facts{}, match.Date{}, match.Question{
		Selector: "#q", Kind: match.KindRadioGroup, Required: true, Options: yesNo,
		Text: "If hired by Example Corp, will you be based in the U.S. or Canada?",
	})
	if r.Outcome != match.OutcomeFilled || r.EntryID != "based-in-us-or-canada" || r.ChosenLabel != "Yes" {
		t.Fatalf("got %+v", r)
	}
}

// An AI-matching opt-in is a consent the user gives: the default bank ships no
// entry for it, so on a fresh install the question abstains to the user.
func TestDefaultBank_AiMatchingOptInIsTheUsersToGive(t *testing.T) {
	b := defaultBank(t)
	for _, e := range b.Entries {
		if strings.Contains(e.ID, "ai-matching") || strings.Contains(e.ID, "opt-in") {
			t.Errorf("the default bank ships %q", e.ID)
		}
	}
	r := matchFacts(t, b, match.Facts{}, match.Date{}, match.Question{Selector: "#q", Kind: match.KindRadioGroup, Required: true,
		Text:    "AI matching: opt in or opt out",
		Options: []match.Option{{Value: "out", Label: "Opt Out"}, {Value: "in", Label: "Opt In"}}})
	if r.Outcome != match.OutcomeAbstained || r.Answer != "" || r.Source == match.SourceModelOption {
		t.Fatalf("a fresh install must leave the opt-in to the user: %+v", r)
	}
}

// "Preferred Full Name" (AmEx, required) is the full name the applicant goes
// by; "Preferred name" alone is still the first name they go by.
func TestDefaultBank_PreferredFullNameIsItsOwnQuestion(t *testing.T) {
	b := defaultBank(t)
	b.Entries = append([]match.Entry(nil), b.Entries...)
	for i := range b.Entries {
		switch e := &b.Entries[i]; e.ID {
		case "preferred-full-name":
			e.Answer, e.Placeholder = "Alex Example", false
		case "preferred-name":
			e.Answer, e.Placeholder = "Alex", false
		}
	}
	for text, want := range map[string]string{"Preferred Full Name": "Alex Example", "Preferred name": "Alex", "Preferred first name": "Alex"} {
		r := matchFacts(t, b, match.Facts{}, match.Date{}, match.Question{Selector: "#q", Kind: match.KindText, Required: true, Text: text})
		if r.Outcome != match.OutcomeFilled || r.Answer != want {
			t.Errorf("%q: %s %q (%s)", text, r.Outcome, r.Answer, r.Reason)
		}
	}
}

// "How did you hear about …" is asked as a picklist on some forms and as a text
// box on others (Lever's "How did you hear about TeamSnap and this
// opportunity?"); the source entries answer both, so the user's one answer
// fills the box instead of abstaining on the input type.
func TestDefaultBank_SourceEntriesAnswerATextBox(t *testing.T) {
	b := defaultBank(t)
	b.Entries = append([]match.Entry(nil), b.Entries...)
	for i := range b.Entries {
		if e := &b.Entries[i]; e.ID == "job-source" || e.ID == "referral-source" {
			e.Preferred, e.FallbackText, e.Placeholder = []string{"LinkedIn"}, "LinkedIn", false
		}
	}
	// Customer.io writes "your" for "you".
	for _, text := range []string{"How did you hear about TeamSnap and this opportunity?", "How did you hear about us?", "How did you learn about this job?", "How did your hear about this job?"} {
		r := matchFacts(t, b, match.Facts{}, match.Date{}, match.Question{Selector: "#q", Kind: match.KindText, Required: true, Text: text})
		if r.Outcome != match.OutcomeFilled || r.Answer != "LinkedIn" {
			t.Errorf("%q: %s %q (%s)\n%v", text, r.Outcome, r.Answer, r.Reason, r.Trace)
		}
	}
	// Ashby's pronouns box: the shipped blank fallback types nothing.
	if r := matchFacts(t, defaultBank(t), match.Facts{}, match.Date{}, match.Question{Selector: "q", Kind: match.KindText, InputType: match.InputText, Text: "Pronouns"}); r.Reason == match.ReasonInputMismatch {
		t.Errorf("pronouns: %+v", r)
	}
}

// "Were you referred by a TeamSnap Employee?" is a statement about this
// application; the user's own answer to the referral entry settles it when a
// route names that entry (an owner decision). Lever spells the options as
// sentences, the "no" one
// "Nope, I found it on my own!", and the follow-up "If yes, who referred you?
// (Type N/A …)" takes the N/A its No asks for. A blank referral entry still
// leaves both to the user.
func TestDefaultBank_ReferralStatementFromTheUsersOwnAnswer(t *testing.T) {
	qs := []match.Question{
		{Selector: "#r", Kind: match.KindRadioGroup, Required: true, Text: "Were you referred by a TeamSnap Employee?",
			Options: []match.Option{{Value: "Yes, I was referred by a TeamSnap Employee!", Label: "Yes, I was referred by a TeamSnap Employee!"},
				{Value: "Nope, I found it on my own!", Label: "Nope, I found it on my own!"}}},
		{Selector: "#n", Kind: match.KindText, Required: true, Text: "If yes, who referred you? (Type N/A if you weren't referred by a TeamSnap Employee)"},
	}
	run := func(b match.Bank) []match.Result {
		resp, err := match.New(b, match.DefaultStages()).Match(match.Request{Host: "jobs.lever.co", Questions: qs})
		if err != nil {
			t.Fatal(err)
		}
		return resp.Results
	}
	blank := run(defaultBank(t))
	for _, r := range blank {
		if r.Outcome != match.OutcomeAbstained {
			t.Errorf("a blank referral entry answers nothing: %+v", r)
		}
	}
	b := defaultBank(t)
	b.Entries = append([]match.Entry(nil), b.Entries...)
	for i := range b.Entries {
		if e := &b.Entries[i]; e.ID == "referred-by-current-employee" {
			e.Answer, e.Placeholder = "No", false
		}
	}
	got := run(b)
	written := func(r match.Result) bool {
		return r.Outcome == match.OutcomeFilled || r.Outcome == match.OutcomeFilledReview
	}
	if r := got[0]; !written(r) || r.Answer != "Nope, I found it on my own!" {
		t.Errorf("referral radio: %s %q (%s)\n%v", r.Outcome, r.Answer, r.Reason, r.Trace)
	}
	if r := got[1]; !written(r) || r.Answer != "N/A" {
		t.Errorf("referrer follow-up: %s %q (%s)\n%v", r.Outcome, r.Answer, r.Reason, r.Trace)
	}
	// AI assistance with the application stays a verbatim-only statement.
	r := matchFacts(t, b, match.Facts{}, match.Date{}, match.Question{Selector: "#a", Kind: match.KindRadioGroup, Required: true,
		Text:    "Did you use AI to help complete this application?",
		Options: []match.Option{{Value: "y", Label: "Yes"}, {Value: "n", Label: "No"}}})
	if r.Outcome != match.OutcomeAbstained {
		t.Errorf("the AI statement is not the referral rule's: %+v", r)
	}
}

// Campbell's (Workday, live): "When are you available to start?" is a
// multi-line box; as an example of the earliest start date it takes the stored
// date, as "When can you start?" does.
func TestDefaultBank_TheStartDateAnswersATextBox(t *testing.T) {
	b := defaultBank(t)
	b.Entries = append([]match.Entry(nil), b.Entries...)
	for i := range b.Entries {
		if e := &b.Entries[i]; e.ID == "earliest-start-date" {
			e.Answer, e.Placeholder = "2027-01-15", false
		}
	}
	r := matchFacts(t, b, match.Facts{}, match.Date{}, match.Question{Selector: "#q", Kind: match.KindText, InputType: match.InputLongText, Required: true, Text: "When are you available to start?"})
	if r.Outcome != match.OutcomeFilled || r.Answer != "2027-01-15" || r.EntryID != "earliest-start-date" {
		t.Fatalf("%s %q %s (%s)", r.Outcome, r.Answer, r.EntryID, r.Reason)
	}
}

// The employer-history entry answers questions about the employer only; wording
// that merely contains its keywords is left to its own entry or to the user.
func TestAnswerBank_EmployerHistory_DoesNotClaimUnrelatedQuestions(t *testing.T) {
	def, err := DefaultBank()
	if err != nil {
		t.Fatal(err)
	}
	history := map[string]bool{}
	for _, e := range def.Bank.Entries {
		if e.Kind == match.KindEmployerHistory {
			history[e.ID] = true
		}
	}
	if len(history) == 0 {
		t.Fatal("the default bank has no employer-history entry")
	}
	m := match.New(def.Bank, match.DefaultStages())
	for _, q := range []string{
		"Are you authorized to work for Example Corp in this country?",
		"Do you require sponsorship to work for Example Corp?",
		"Are you a protected veteran under VEVRAA? Employment decisions at Example Corp are made without regard to status.",
		"If you were to join us for a technical interview, which day suits you?",
		"Describe your experience working at a company like Example Corp.",
		"Why are you interested in working at Example Corp?",
		"I acknowledge and agree that I have read the Example Corp policies on employment.",
		"Do you have working knowledge of the tools Example Corp uses?",
		"Are you willing to work on-site at Example Corp's office?",
		"I certify the information in this application is true and complete.",
	} {
		resp, err := m.Match(match.Request{Host: "jobs.example.com", Posting: match.Posting{Company: "Example Corp"},
			Questions: []match.Question{{Selector: "q", Text: q, Kind: match.KindText}}})
		if err != nil {
			t.Fatal(err)
		}
		if r := resp.Results[0]; history[r.EntryID] && r.Outcome != match.OutcomeAbstained {
			t.Errorf("%q was answered by the employer-history entry %s", q, r.EntryID)
		}
	}
}
