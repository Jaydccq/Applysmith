package answerbank

import (
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"applysmith/internal/answerbank/match"
)

func defaultBank(t *testing.T) match.Bank {
	t.Helper()
	r, err := DefaultBank()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Warnings) != 0 {
		t.Fatalf("the default bank must load with zero warnings: %v", r.Warnings)
	}
	return r.Bank
}

var declineRe = regexp.MustCompile(`(?i)decline|prefer not|do not wish`)

// ownerDefaults are the entries that ship the owner's own default: relocation
// assistance is needed whenever the role means relocating, and a SAT, ACT or
// GRE score was not taken.
// The user changes them in the Answer Bank like any answer.
var ownerDefaults = map[string]bool{"relocation-assistance-needed": true, "sat-score": true, "act-score": true, "gre-score": true}

func TestDefaultBank_EveryFactEntryIsABlankPlaceholder(t *testing.T) {
	for _, e := range defaultBank(t).Entries {
		// The skill gate answers from the user's own information (F-10).
		if e.Category == match.CategorySelfIdentification || e.Kind == match.KindAutoAcknowledge || e.Kind == match.KindEmployerHistory || e.SkillMode == match.SkillProfileSkill {
			continue
		}
		if ownerDefaults[e.ID] {
			if e.Placeholder || e.BodyEmpty() || e.NeedsReview {
				t.Errorf("%s ships the owner's default, answered and not held for review: %+v", e.ID, e.Body)
			}
			continue
		}
		if !e.Placeholder || !e.BodyEmpty() || len(e.ExceptionTerms) > 0 || e.FallbackAnyOption {
			t.Errorf("%s (%s) is not a blank placeholder: %+v", e.ID, e.Category, e.Body)
		}
		if e.Kind == match.KindMultiSelect && e.Selected != nil {
			t.Errorf("%s: a multi-select placeholder must omit selected", e.ID)
		}
	}
}

func TestDefaultBank_SelfIdentificationDeclinesWithNeedsReview(t *testing.T) {
	n := 0
	for _, e := range defaultBank(t).Entries {
		if e.Category != match.CategorySelfIdentification {
			continue
		}
		n++
		if e.Kind != match.KindPickOption || !e.NeedsReview || len(e.Preferred) == 0 || e.FallbackText != "" || e.FallbackAnyOption {
			t.Errorf("%s: %+v", e.ID, e)
		}
		for _, p := range e.Preferred {
			if !declineRe.MatchString(p) {
				t.Errorf("%s prefers %q, which is not a decline option", e.ID, p)
			}
		}
	}
	if n == 0 {
		t.Fatal("no self-identification entries")
	}
}

func TestDefaultBank_AutoAcknowledgeEntries(t *testing.T) {
	want := map[string]bool{"mandatory-acknowledge": true, "privacy-policy-consent": true, "truth-certification": true}
	for _, e := range defaultBank(t).Entries {
		if (e.Kind == match.KindAutoAcknowledge) != want[e.ID] {
			t.Errorf("%s has kind %s", e.ID, e.Kind)
		}
		if e.Kind == match.KindAutoAcknowledge && (e.Placeholder || e.Category != match.CategoryConsent) {
			t.Errorf("%s: %+v", e.ID, e.Meta)
		}
	}
}

// specIDTable is the id table of the answer-bank specification, without the
// ids that exist only in the user's bank (the two consent entries, the
// years-experience family) or are built per request.
var specIDTable = strings.Fields(`
current-age-bracket age-minimum-threshold full-time-start-availability work-authorization-detail
sponsorship-now-or-future work-auth-and-future-sponsorship other-website-url twitter-url
office-attendance-ok willing-to-travel desired-career-level preferred-work-location
applicant-full-name earliest-start-date current-job-title current-company current-immigration-status
sponsorship-type-description country-of-citizenship address-city address-state address-country time-zone
linkedin-url github-url portfolio-url most-recent-school undergraduate-school field-of-study-choice
undergraduate-major highest-level-education phone-country-code job-source referral-source
referral-source-specify eeo-disability eeo-veteran eeo-race eeo-ethnicity eeo-transgender
military-service-status mandatory-acknowledge privacy-policy-consent truth-certification
no-ai-during-assessment-attestation marketing-text-consent data-retention-preference
us-work-authorization-status relocation-or-local address-type work-authorization-expiry
stem-opt-extension-expiry work-auth-us work-auth-based-on-visa-spouse`)

func TestDefaultBank_NamedIDsExist(t *testing.T) {
	seen := map[string]int{}
	for _, e := range defaultBank(t).Entries {
		seen[e.ID]++
	}
	for _, id := range specIDTable {
		if seen[id] != 1 {
			t.Errorf("id %q appears %d times", id, seen[id])
		}
	}
	for _, id := range []string{"terms-and-conditions-consent", "assessment-proctoring-consent", match.IDCoverLetterSubmitted, match.IDResumeSubmitted} {
		if seen[id] != 0 {
			t.Errorf("%q must exist only in the user's bank or per request", id)
		}
	}
	for id, n := range seen {
		if n > 1 || strings.HasPrefix(id, match.YearsExperiencePrefix) {
			t.Errorf("id %q: %d times", id, n)
		}
	}
}

func TestDefaultBank_CompliancePairsWired(t *testing.T) {
	b := defaultBank(t)
	pairs := 0
	for _, e := range b.Entries {
		if !strings.HasSuffix(e.ID, match.ReasonSuffix) {
			continue
		}
		pairs++
		gate, ok := b.Find(strings.TrimSuffix(e.ID, match.ReasonSuffix))
		if !ok || gate.LinkedExplainID != e.ID || !gate.Placeholder || !gate.BodyEmpty() || !e.Placeholder || !e.BodyEmpty() {
			t.Errorf("pair %s is not wired", e.ID)
		}
	}
	if pairs != 8 {
		t.Fatalf("%d compliance pairs, want 8", pairs)
	}
	for _, e := range b.Entries {
		if e.LinkedExplainID != "" {
			if _, ok := b.Find(e.LinkedExplainID); !ok {
				t.Errorf("%s links to the missing %s", e.ID, e.LinkedExplainID)
			}
		}
	}
}

// intentionalSharedKeys lists the answer keys several default entries share on
// purpose: three phrasings of one work-authorization question.
var intentionalSharedKeys = map[string]bool{"work_authorized": true}

func TestDefaultBank_NoUnintendedSharedAnswerKey(t *testing.T) {
	owners := map[string][]string{}
	for _, e := range defaultBank(t).Entries {
		k := e.EffectiveAnswerKey()
		owners[k] = append(owners[k], e.ID)
	}
	for k, ids := range owners {
		if len(ids) > 1 && !intentionalSharedKeys[k] {
			t.Errorf("answer key %q is shared by %v", k, ids)
		}
	}
	if len(owners["work_authorized"]) != 3 {
		t.Errorf("work_authorized owners = %v", owners["work_authorized"])
	}
}

func TestDefaultBank_RoundTripsThroughTheEncoder(t *testing.T) {
	b := defaultBank(t)
	data, err := Encode(b)
	if err != nil {
		t.Fatal(err)
	}
	again := mustParse(t, string(data))
	if len(again.Warnings) != 0 || len(again.Bank.Entries) != len(b.Entries) {
		t.Fatalf("%d entries, warnings %v", len(again.Bank.Entries), again.Warnings)
	}
	for i, want := range b.Entries {
		got := again.Bank.Entries[i]
		got.Runtime, want.Runtime = match.Runtime{}, match.Runtime{}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("entry %s:\n got %+v\nwant %+v", want.ID, got, want)
		}
	}
	second, err := Encode(again.Bank)
	if err != nil || string(second) != string(data) {
		t.Fatal("encoding is not stable")
	}
}

func TestDefaultBank_ThresholdAndPolicyBindings(t *testing.T) {
	b := defaultBank(t)
	office, _ := b.Find(match.IDOfficeAttendanceOK)
	if office.NumericThreshold == nil || office.NumericThreshold.Extractor != match.ExtractorDaysPerWeekOnsite {
		t.Fatal("office-attendance-ok must declare its threshold so the profile maximum can be stamped")
	}
	loc, _ := b.Find(match.IDPreferredWorkLocation)
	if loc.LocationPolicy == nil {
		t.Fatal("preferred-work-location must carry the location-policy binding")
	}
}

// ADP Workforce Now asks its preliminary questions with shapes the shipped
// entries must take: a check-all-that-apply list of relocation destinations,
// and the truth certification as a required Yes/No radio.
func TestDefaultBank_ADPPreliminaryQuestionShapes(t *testing.T) {
	bank := defaultBank(t).Stamp(match.Preferences{Relocation: &match.RelocationPolicy{Default: "open", Excluded: []string{"Dallas"}}})
	m := match.New(bank, match.DefaultStages())
	resp, err := m.Match(match.Request{AutoAcceptLegalDeclarations: true, Questions: []match.Question{
		{Selector: "places", Text: "Which location(s) would you be willing to relocate to (check all that apply)?", Kind: match.KindCheckbox,
			InputType: match.InputMultiSelect, Options: opts("Atlanta, GA", "Chicago, IL", "Dallas, TX")},
		{Selector: "truth", Text: "I certify that all information I have provided to Example Logistics LLC in support of my application for employment is true and accurate.",
			Kind: match.KindRadioGroup, InputType: match.InputYesNo, Required: true, Options: opts("Yes", "No")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if r := resp.Results[0]; r.Outcome != match.OutcomeFilled || strings.Join(r.ChosenLabels, "|") != "Atlanta, GA|Chicago, IL" {
		t.Errorf("relocation destinations: %+v", r)
	}
	if r := resp.Results[1]; r.Outcome != match.OutcomeFilled || r.ChosenLabel != "Yes" {
		t.Errorf("truth certification: %+v", r)
	}
}

// ADP's application form names its travel field "Willingness to Travel" and
// offers percentage bands; the profile's travel ceiling picks the band.
func TestDefaultBank_ADPWillingnessToTravelBand(t *testing.T) {
	ceiling := 25
	m := match.New(defaultBank(t).Stamp(match.Preferences{MaxTravelPercent: &ceiling}), match.DefaultStages())
	var opts []match.Option
	for _, l := range []string{"None", "Up to 10%", "10% - 25%", "25% - 50%", "50% - 75%", "75% - 100%"} {
		opts = append(opts, match.Option{Label: l, Value: l})
	}
	resp, err := m.Match(match.Request{Questions: []match.Question{{Selector: "travel", Text: "Willingness to Travel", Kind: match.KindCustomListbox,
		InputType: match.InputSingleSelect, Required: true, Options: opts}}})
	if err != nil {
		t.Fatal(err)
	}
	if r := resp.Results[0]; r.Outcome != match.OutcomeFilled || r.ChosenLabel != "10% - 25%" {
		t.Errorf("travel band: %+v", r)
	}
	// Trend Micro (Workday) asks by frequency: a 25% ceiling covers Occasionally.
	opts = nil
	for _, l := range []string{"Frequently", "Occasionally", "Rarely", "No"} {
		opts = append(opts, match.Option{Label: l, Value: l})
	}
	resp, err = m.Match(match.Request{Questions: []match.Question{{Selector: "travel", Text: "Are you willing to travel?", Kind: match.KindCustomListbox,
		InputType: match.InputSingleSelect, Required: true, Options: opts}}})
	if err != nil {
		t.Fatal(err)
	}
	if r := resp.Results[0]; r.Outcome == match.OutcomeAbstained || r.ChosenLabel != "Occasionally" {
		t.Errorf("travel frequency: %+v", r)
	}
}

// ADP's County is a listbox of the state's counties; the profile's county picks its row.
func TestDefaultBank_ADPCountyListboxPicksTheProfileCounty(t *testing.T) {
	b := defaultBank(t)
	for i := range b.Entries {
		if b.Entries[i].ID == "address-county" {
			b.Entries[i].Answer, b.Entries[i].Placeholder = "Charleston", false
		}
	}
	m := match.New(b, match.DefaultStages())
	var opts []match.Option
	for _, l := range []string{"Abbeville", "Charleston", "Greenville"} {
		opts = append(opts, match.Option{Label: l, Value: l})
	}
	resp, err := m.Match(match.Request{Questions: []match.Question{{Selector: "county", Text: "County", Kind: match.KindCustomListbox,
		InputType: match.InputSingleSelect, Required: true, Options: opts}}})
	if err != nil {
		t.Fatal(err)
	}
	if r := resp.Results[0]; r.Outcome != match.OutcomeFilled || r.ChosenLabel != "Charleston" {
		t.Errorf("county: %+v", r)
	}
}

// Oklo (Greenhouse, live) asks "How did you hear about us?" as a multi-select:
// the source the applicant prefers is the one option chosen, as on a menu.
func TestDefaultBank_SourceAskedAsAMultiSelectTakesThePreferredSource(t *testing.T) {
	b := defaultBank(t)
	for i := range b.Entries {
		if b.Entries[i].ID == "job-source" {
			b.Entries[i].Preferred, b.Entries[i].Placeholder = []string{"LinkedIn", "Other"}, false
		}
	}
	m := match.New(b, match.DefaultStages())
	var opts []match.Option
	for _, l := range []string{"LinkedIn", "Wellfound", "Social Media", "Oklo Employee", "Company Website / Careers Page", "Other"} {
		opts = append(opts, match.Option{Label: l, Value: l})
	}
	resp, err := m.Match(match.Request{Questions: []match.Question{
		{Selector: "menu", Text: "How did you hear about us?", Kind: match.KindCustomListbox, InputType: match.InputMultiSelect, Required: true, Options: opts},
		{Selector: "boxes", Text: "How did you hear about us?", Kind: match.KindCheckbox, InputType: match.InputMultiSelect, Required: true, Options: opts},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range resp.Results {
		if r.Outcome != match.OutcomeFilled || r.ChosenLabel != "LinkedIn" || len(r.ChosenLabels) > 1 {
			t.Errorf("%s: %+v", r.Selector, r)
		}
	}
}

// Vola Dynamics (Ashby, live) asks "Which of the following languages do you
// consider yourself professionally proficient in?" as a checkbox list of
// programming languages: the boxes ticked are the profile's own skills, each
// by its whole name (Java never ticks JavaScript, C# never C++), and a list
// none of whose options the profile names is left to the user.
func TestDefaultBank_ASkillChecklistTicksTheProfileSkills(t *testing.T) {
	m := match.New(defaultBank(t), match.DefaultStages())
	var opts []match.Option
	for _, l := range []string{"Python", "C++", "Rust", "Java", "C#", "JavaScript", "Node.js"} {
		opts = append(opts, match.Option{Label: l, Value: l})
	}
	ask := func(text string, opts []match.Option, skills ...string) match.Result {
		t.Helper()
		var f []match.SkillFact
		for _, s := range skills {
			f = append(f, match.SkillFact{Name: s})
		}
		resp, err := m.Match(match.Request{Facts: match.Facts{Skills: f}, Questions: []match.Question{{Selector: "q", Text: text,
			Kind: match.KindCheckbox, InputType: match.InputMultiSelect, Required: true, Options: opts}}})
		if err != nil {
			t.Fatal(err)
		}
		return resp.Results[0]
	}
	const q = "Which of the following languages do you consider yourself professionally proficient in?"
	if r := ask(q, opts, "Python", "Go", "C#", "Java", "NodeJS"); r.Outcome == match.OutcomeAbstained || strings.Join(r.ChosenLabels, "|") != "Python|Java|C#|Node.js" {
		t.Errorf("profile skills: %+v", r)
	}
	if r := ask(q, opts, "Go", "Kotlin"); r.Outcome != match.OutcomeAbstained {
		t.Errorf("no listed skill on the profile: %+v", r)
	}
	// Sphinx Defense names no noun at all: "Select what you have experience
	// working in:" over the languages themselves.
	if r := ask("Select what you have experience working in:", opts, "Rust", "Java"); strings.Join(r.ChosenLabels, "|") != "Rust|Java" {
		t.Errorf("the options name what is asked: %+v", r)
	}
	spoken := []match.Option{{Label: "English", Value: "English"}, {Label: "Spanish", Value: "Spanish"}}
	if r := ask("Which of the following languages do you speak proficiently?", spoken, "Spanish"); r.Outcome != match.OutcomeAbstained {
		t.Errorf("spoken languages: %+v", r)
	}
}

// Workday's My Experience asks for skills through a search prompt, "Type to
// Add Skills", with no list (Target requires it): the profile's skills are
// searched in turn, and a profile with none leaves it to the user. A text box
// labelled Skills is not that prompt.
func TestDefaultBank_AWorkdaySkillsPromptTakesTheProfileSkills(t *testing.T) {
	m := match.New(defaultBank(t), match.DefaultStages())
	ask := func(text string, kind match.ControlKind, input match.InputType, skills ...string) match.Result {
		t.Helper()
		var f []match.SkillFact
		for _, s := range skills {
			f = append(f, match.SkillFact{Name: s})
		}
		resp, err := m.Match(match.Request{Facts: match.Facts{Skills: f}, Questions: []match.Question{{Selector: "q", Text: text, Kind: kind, InputType: input, Required: true}}})
		if err != nil {
			t.Fatal(err)
		}
		return resp.Results[0]
	}
	if r := ask("Type to Add Skills", match.KindCustomListbox, match.InputMultiSelect, "Python", "Go", "React"); r.Outcome == match.OutcomeAbstained || r.Answer != "Python, Go, React" {
		t.Errorf("skills prompt: %+v", r)
	}
	if r := ask("Skills", match.KindCustomListbox, match.InputMultiSelect); r.Outcome != match.OutcomeAbstained {
		t.Errorf("no profile skill: %+v", r)
	}
	if r := ask("Skills", match.KindText, match.InputText, "Python"); r.Outcome != match.OutcomeAbstained {
		t.Errorf("a text box: %+v", r)
	}
}

// HRS asks its required privacy consent as a radio of "Accept" and "Decline",
// and its notice period in a text box.
func TestDefaultBank_AcceptDeclineConsentAndATypedNoticePeriod(t *testing.T) {
	b := defaultBank(t)
	b.Entries = append([]match.Entry(nil), b.Entries...)
	for i := range b.Entries {
		if e := &b.Entries[i]; e.ID == "availability-notice-period" {
			e.Preferred, e.FallbackText, e.Placeholder = []string{"2 weeks"}, "2 weeks", false
		}
	}
	m := match.New(b, match.DefaultStages())
	consent := match.Question{Selector: "#p", Kind: match.KindRadioGroup, Required: true,
		Text:    "I have read and understood the Candidate Privacy Notice. I consent to HRS Group collecting and processing my personal data for the purposes of the recruitment process.",
		Options: []match.Option{{Value: "a", Label: "Accept"}, {Value: "d", Label: "Decline"}}}
	notice := match.Question{Selector: "#n", Kind: match.KindText, Required: true, Text: "Notice Period / Availability"}
	// Coterie's pair of sentences is read as a Yes/No: the same acknowledgement.
	coterie := match.Question{Selector: "#c", Kind: match.KindCustomListbox, Required: true,
		Text:    "Coterie's Privacy Policy and Applicant Privacy Policy explains how we collect, use, disclose, and protect personal information provided during the application and hiring process. By reviewing this policy and submitting your application, you acknowledge and consent to the practices described.",
		Options: []match.Option{{Value: "y", Label: "I acknowledge and consent to Coterie's Privacy Policy and Applicant Privacy Policy."}, {Value: "n", Label: "I do not consent to Coterie's Privacy Policy and Applicant Privacy Policy."}}}
	for _, on := range []bool{true, false} {
		resp, err := m.Match(match.Request{Host: "jobs.example.com", AutoAcceptLegalDeclarations: on, Questions: []match.Question{consent, notice, coterie}})
		if err != nil {
			t.Fatal(err)
		}
		if r := resp.Results[0]; on && (r.Outcome == match.OutcomeAbstained || r.ChosenLabel != "Accept") || !on && r.Outcome != match.OutcomeAbstained {
			t.Errorf("consent, setting %v: %s %q (%s)", on, r.Outcome, r.ChosenLabel, r.Reason)
		}
		if r := resp.Results[1]; r.Answer != "2 weeks" {
			t.Errorf("notice period: %s %q (%s)", r.Outcome, r.Answer, r.Reason)
		}
		if r := resp.Results[2]; on && r.Answer != "y" || !on && r.Outcome != match.OutcomeAbstained {
			t.Errorf("sentence pair, setting %v: %s %q (%s)", on, r.Outcome, r.Answer, r.Reason)
		}
	}
}

// Coder asks "How did you hear about this job?" in a multi-line box: the source
// entry types its text there, never into a box that asks for a story.
func TestDefaultBank_ASourceQuestionInAMultiLineBox(t *testing.T) {
	b := defaultBank(t)
	b.Entries = append([]match.Entry(nil), b.Entries...)
	for i := range b.Entries {
		if e := &b.Entries[i]; e.ID == "referral-source" || e.ID == "job-source" {
			e.FallbackText, e.Placeholder = "LinkedIn", false
		}
	}
	m := match.New(b, match.DefaultStages())
	box := func(text string) match.Question {
		return match.Question{Selector: "#s", Kind: match.KindText, InputType: match.InputLongText, Required: true, Text: text}
	}
	resp, err := m.Match(match.Request{Host: "jobs.example.com", Questions: []match.Question{
		box("How did you hear about this job?"), box("Tell us how you heard about this role and why it caught your eye.")}})
	if err != nil {
		t.Fatal(err)
	}
	if r := resp.Results[0]; r.Answer != "LinkedIn" {
		t.Errorf("short source question: %s %q (%s)", r.Outcome, r.Answer, r.Reason)
	}
	if r := resp.Results[1]; r.Answer == "LinkedIn" {
		t.Errorf("a story was answered with the source: %+v", r)
	}
}

// GDIT's Workday questionnaire renders every text answer as a textarea: the
// typed signature, wherever its sentence opens, and the salary figure are typed
// there, never into a box that asks for a story. Found Energy's "Current
// Location" menu of states takes the state, and Cirrus Logic's lone box under
// an export-control notice is the acknowledgement it states.
func TestDefaultBank_TextareaSignatureSalaryStateMenuAndLoneAcknowledgement(t *testing.T) {
	b := defaultBank(t)
	b.Entries = append([]match.Entry(nil), b.Entries...)
	for i := range b.Entries {
		switch e := &b.Entries[i]; e.ID {
		case "applicant-full-name":
			e.Answer, e.Placeholder = "Sam Example", false
		case "salary-expectation":
			e.DefaultAnswer, e.Placeholder = "120000", false
		case "address-state":
			e.Preferred, e.Placeholder = []string{"South Carolina"}, false
		}
	}
	m := match.New(b, match.DefaultStages())
	box := func(text string) match.Question {
		return match.Question{Selector: "#t", Kind: match.KindText, InputType: match.InputLongText, Required: true, Text: text}
	}
	// The whole list, past the catalog cap a typeahead would search instead.
	var states []match.Option
	for _, s := range []string{"Not a current US resident", "Alabama", "Alaska", "Arizona", "Arkansas", "California", "Colorado", "Connecticut", "Delaware", "Florida", "Georgia",
		"Hawaii", "Idaho", "Illinois", "Indiana", "Iowa", "Kansas", "Kentucky", "Louisiana", "Maine", "Maryland", "New York", "North Carolina", "South Carolina", "Texas", "Utah", "Virginia"} {
		states = append(states, match.Option{Value: s, Label: s})
	}
	resp, err := m.Match(match.Request{Host: "jobs.example.com", AutoAcceptLegalDeclarations: true, Questions: []match.Question{
		box("By typing your name below, I hereby certify that the answers that I have provided are complete and accurate to the best of my recollection and knowledge. Name:"),
		box("AN EMPLOYER WHO VIOLATES THIS LAW IS GUILTY OF A MISDEMEANOR. By entering your name, you confirm that you have read the above statement:"),
		box("If you have specific salary expectations, please list below. If not, enter \"none\"."),
		box("Please explain your salary expectations and why."),
		box("Please explain why you signed. By typing your name below, you confirm the above."),
		{Selector: "#s", Kind: match.KindCustomListbox, Required: true, Text: "Current Location", Options: states},
		{Selector: "#c", Kind: match.KindCheckbox, InputType: match.InputYesNo, Required: true,
			Text:    "Cirrus Logic is a multinational technology company with its corporate headquarters in the United States. U.S. export control laws require that Cirrus Logic obtain an export license prior to providing certain foreign persons access to controlled technologies. Please respond to the questions posed below.",
			Options: []match.Option{{Value: "on", Label: "By checking this box, I confirm that I have read and understood the information regarding U.S. export control laws and regulations as outlined above."}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"Sam Example", "Sam Example", "120000", "", "", "South Carolina", "on"} {
		if r := resp.Results[i]; r.Answer != want {
			t.Errorf("%d: %s %q (%s %s), want %q", i, r.Outcome, r.Answer, r.Reason, r.EntryID, want)
		}
	}
}

// Sphinx Defense: "This position requires eligibility to obtain and maintain a
// U.S. security clearance." over held, eligible and ineligible options is the
// citizenship a clearance requires.
func TestDefaultBank_ClearanceEligibilityIsTheCitizenshipGate(t *testing.T) {
	b := defaultBank(t)
	b.Entries = append([]match.Entry(nil), b.Entries...)
	for i := range b.Entries {
		if e := &b.Entries[i]; e.ID == "clearance-citizenship-gated" {
			e.Preferred, e.FallbackText, e.Placeholder = []string{"No"}, "No", false
		}
	}
	m := match.New(b, match.DefaultStages())
	resp, err := m.Match(match.Request{Host: "jobs.example.com", Questions: []match.Question{{Selector: "#c", Kind: match.KindCustomListbox, Required: true,
		Text: "This position requires eligibility to obtain and maintain a U.S. security clearance.",
		Options: []match.Option{{Value: "a", Label: "Yes, I hold an active U.S. security clearance."}, {Value: "b", Label: "Yes, I am eligible for a U.S. security clearance."},
			{Value: "c", Label: "No, I am ineligible or unwilling to receive a U.S. security clearance."}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if r := resp.Results[0]; r.EntryID != "clearance-citizenship-gated" || r.Answer != "c" {
		t.Errorf("clearance eligibility: %s %s %q (%s)", r.EntryID, r.Outcome, r.Answer, r.Reason)
	}
}

// Q2 asks how much time from an offer the applicant needs to start, in a text box.
func TestDefaultBank_AnOfferFramedLeadTimeIsTyped(t *testing.T) {
	b := defaultBank(t)
	b.Entries = append([]match.Entry(nil), b.Entries...)
	for i := range b.Entries {
		if e := &b.Entries[i]; e.ID == "offer-start-availability" {
			e.Preferred, e.FallbackText, e.Placeholder = []string{"Two weeks after accepting"}, "Two weeks after accepting", false
		}
	}
	m := match.New(b, match.DefaultStages())
	q := match.Question{Selector: "#s", Kind: match.KindText, Required: true, Text: "From the point of an offer, how much time would you need before you’re able to start?"}
	resp, err := m.Match(match.Request{Host: "jobs.example.com", Questions: []match.Question{q}})
	if err != nil {
		t.Fatal(err)
	}
	if r := resp.Results[0]; r.Answer != "Two weeks after accepting" {
		t.Errorf("offer lead time: %s %q (%s) %v", r.Outcome, r.Answer, r.Reason, r.Trace)
	}
	// Q2 renders it as a multi-line box, and a bank may hold the answer only as
	// its one preferred value; of two, none is typed.
	q.InputType = match.InputLongText
	for _, c := range []struct {
		preferred []string
		want      string
	}{{[]string{"Two weeks after accepting"}, "Two weeks after accepting"}, {[]string{"Two weeks", "One month"}, ""}} {
		for i := range b.Entries {
			if e := &b.Entries[i]; e.ID == "offer-start-availability" {
				e.Preferred, e.FallbackText = c.preferred, ""
			}
		}
		resp, err := match.New(b, match.DefaultStages()).Match(match.Request{Host: "jobs.example.com", Questions: []match.Question{q}})
		if err != nil {
			t.Fatal(err)
		}
		if r := resp.Results[0]; r.Answer != c.want {
			t.Errorf("preferred %v in a multi-line box: %s %q (%s) %v", c.preferred, r.Outcome, r.Answer, r.Reason, r.Trace)
		}
	}
}

// The owner's defaults answer the forms that asked them (Baton, SpaceX,
// Freeform, Scopely, Niantic): relocation assistance whenever the role means
// relocating; a test score not taken is N/A in a box and the list's own
// did-not-take choice, never a score range.
func TestDefaultBank_OwnerDefaultsAnswerRelocationAssistanceAndTestScores(t *testing.T) {
	b := defaultBank(t)
	assistance := match.Question{Selector: "#r", Kind: match.KindRadioGroup, Required: true, Text: "If hired, would you require relocation assistance?", Options: yesNo}
	if r := matchFacts(t, b, match.Facts{}, match.Date{}, assistance); r.EntryID != "relocation-assistance-needed" || r.ChosenLabel != "Yes" {
		t.Errorf("relocation assistance with no relocation preference: %+v", r)
	}
	stamped := b.Stamp(match.Preferences{Relocation: &match.RelocationPolicy{Default: "open", Home: match.Home{City: "Charleston", Region: "SC", Country: "US"}}})
	scopely := match.Question{Selector: "#s", Kind: match.KindSelect, InputType: match.InputSingleSelect, Required: true, Text: "Will you require relocation support?", Options: opts(
		"Yes – I would be relocating for this role. (Choose this option if you do not currently reside in the required city or country and would need relocation support.)",
		"No – I am already based in the required location. (Choose this option if you already live in the city or country where the role is based.)",
		"No - I am not willing to relocate.")}
	niantic := match.Question{Selector: "#n", Kind: match.KindSelect, InputType: match.InputSingleSelect, Required: true,
		Text:    "This position is based in the city listed on the job description and requires being in-office 3x/week for individual contributors or 4x/week for people leaders. Please select the statement that best describes your status:",
		Options: opts("Local", "Ready to move (Self-Funded)", "Relocation Assistance Required", "Remote Only")}
	// Hadrian (Ashby, live): the office sentence leads a picker of the same statuses.
	hadrian := match.Question{Selector: "#h", Kind: match.KindRadioGroup, InputType: match.InputSingleSelect, Required: true,
		Text: "This job will be physically based in the office location indicated on this job description. Which of the following describes your current or future situation?",
		Options: opts("I currently live in this location.",
			"I do not currently live in the location mentioned, but would plan to relocate myself for this role.",
			"I do not currently live in the location mentioned, and would need relocation assistance from Hadrian for this role.",
			"I am only available to work remotely.")}
	for _, c := range []struct {
		posting                          string
		yesNo, scopely, niantic, hadrian string
	}{
		{"San Francisco, CA", "Yes", scopely.Options[0].Label, "Relocation Assistance Required", hadrian.Options[2].Label},
		{"Charleston, SC", "No", scopely.Options[1].Label, "Local", hadrian.Options[0].Label},
	} {
		resp, err := match.New(stamped, match.DefaultStages()).Match(match.Request{Host: "jobs.example.com", Posting: match.Posting{Location: c.posting}, Questions: []match.Question{assistance, scopely, niantic, hadrian}})
		if err != nil {
			t.Fatal(err)
		}
		for i, want := range []string{c.yesNo, c.scopely, c.niantic, c.hadrian} {
			if r := resp.Results[i]; r.Outcome != match.OutcomeFilled || r.ChosenLabel != want {
				t.Errorf("posting in %s, question %d: want %q, got %s %q (%s) %v", c.posting, i, want, r.Outcome, r.ChosenLabel, r.Reason, r.Trace)
			}
		}
	}
	// A user who answers No in the Answer Bank moves at their own cost: the
	// self-funded statement, and never Scopely's "would need relocation support".
	for i := range stamped.Entries {
		if e := &stamped.Entries[i]; e.ID == "relocation-assistance-needed" {
			e.Answer = "No"
		}
	}
	niantic.Options = opts("Local: I currently live within a commutable distance.",
		"Ready to move (Self-Funded): I am planning to relocate and do not require financial assistance to be on-site by my start date.",
		"Relocation Assistance Required: I am willing to be onsite by my start date but would require a relocation package/stipend to do so.",
		"Remote Only: I am not open to relocating and am looking for a remote-first role.")
	resp, err := match.New(stamped, match.DefaultStages()).Match(match.Request{Host: "jobs.example.com", Posting: match.Posting{Location: "San Francisco, CA"}, Questions: []match.Question{niantic, scopely, assistance, hadrian}})
	if err != nil {
		t.Fatal(err)
	}
	if r := resp.Results[0]; r.Outcome != match.OutcomeFilled || r.ChosenLabel != niantic.Options[1].Label {
		t.Errorf("self-funded: %s %q (%s) %v", r.Outcome, r.ChosenLabel, r.Reason, r.Trace)
	}
	if r := resp.Results[3]; r.Outcome != match.OutcomeFilled || r.ChosenLabel != hadrian.Options[1].Label {
		t.Errorf("self-funded, Hadrian: %s %q (%s) %v", r.Outcome, r.ChosenLabel, r.Reason, r.Trace)
	}
	if r := resp.Results[1]; r.ChosenLabel != "" {
		t.Errorf("no assistance, Scopely: %s %q (%s) %v", r.Outcome, r.ChosenLabel, r.Reason, r.Trace)
	}
	if r := resp.Results[2]; r.ChosenLabel != "No" {
		t.Errorf("no assistance, Yes/No: %s %q (%s) %v", r.Outcome, r.ChosenLabel, r.Reason, r.Trace)
	}
	for _, q := range []string{"SAT Score", "ACT Score", "GRE Score", "SAT / ACT / GRE Score"} {
		if r := matchFacts(t, b, match.Facts{}, match.Date{}, match.Question{Selector: "#s", Kind: match.KindText, Required: true, Text: q}); r.Answer != "N/A" {
			t.Errorf("%q in a box: %+v", q, r)
		}
	}
	took := []match.Option{{Value: "1", Label: "1500-1600"}, {Value: "2", Label: "1400-1490"}, {Value: "3", Label: "Below 1400"}, {Value: "4", Label: "I did not take the SAT"}}
	if r := matchFacts(t, b, match.Facts{}, match.Date{}, match.Question{Selector: "#s", Kind: match.KindSelect, Required: true, Text: "SAT Score", Options: took}); r.ChosenLabel != "I did not take the SAT" {
		t.Errorf("a list with its did-not-take choice: %+v", r)
	}
	if r := matchFacts(t, b, match.Facts{}, match.Date{}, match.Question{Selector: "#s", Kind: match.KindSelect, Required: true, Text: "SAT Score", Options: took[:3]}); r.ChosenLabel != "" {
		t.Errorf("a list of score ranges alone gets no range: %+v", r)
	}
}

// Stripe asks the countries the applicant anticipates working in as a
// multi-value list: the shipped work-country entry takes a list as well as a
// box, so the user's answer picks its option ("US") there.
func TestAnswerBank_Defaults_WorkCountryTakesAList(t *testing.T) {
	b := defaultBank(t)
	for i := range b.Entries {
		if b.Entries[i].ID == "work-country" {
			b.Entries[i].Answer, b.Entries[i].Placeholder = "United States", false
		}
	}
	m := match.New(b, match.DefaultStages())
	opts := []match.Option{}
	for _, c := range []string{"Australia", "Canada", "India", "UK", "US"} {
		opts = append(opts, match.Option{Label: c, Value: c})
	}
	resp, err := m.Match(match.Request{Host: "job-boards.greenhouse.io", Questions: []match.Question{{Selector: "#c",
		Text: "Please select the country or countries you anticipate working in for the role in which you are applying.",
		Kind: match.KindCustomListbox, InputType: match.InputMultiSelect, Options: opts, Required: true}}})
	if err != nil {
		t.Fatal(err)
	}
	r := resp.Results[0]
	if r.Outcome != match.OutcomeFilled || r.ChosenLabel != "US" && !slices.Equal(r.ChosenLabels, []string{"US"}) {
		t.Fatalf("work-country in a multi-value list: %+v", r)
	}
}
