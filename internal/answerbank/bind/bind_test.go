package bind

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/learn"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/engine"
	"applysmith/internal/kb"
	"applysmith/internal/workspace"
)

func textQ(sel, text string) match.Question {
	return match.Question{Selector: sel, Text: text, Kind: match.KindText}
}

func TestBind_Facts_BuiltFromProfile(t *testing.T) {
	f := newFixture(t, alexProfile)
	in, err := f.binding(nil).build(context.Background(), Request{Host: "jobs.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	got := in.req.Facts
	no := false
	want := match.Facts{
		DateOfBirth: "1990-04-02", VisaStatus: "Citizen", RequiresSponsorshipNow: &no, AuthorizedCountries: []string{"US"},
		Address: match.AddressFact{Line1: "1 Example Street", City: "Durham", County: "Durham County", Region: "NC", PostalCode: "27701", Country: "US"},
		Education: []match.EducationFact{{School: "Example University", Degree: "BS", Field: "Computer Science",
			Start: "2013-09", End: "2017-05", GradeAverage: "3.8"}},
		Employment: []match.EmploymentFact{
			{Employer: "Example Labs", Title: "Staff Engineer", Start: "2021-03", End: "present", Location: "Durham, NC"},
			{Employer: "Sample Works", Title: "Engineer", Start: "2017-06", End: "2021-02"}},
	}
	// A skill listed without years counts as the owner's default.
	six, dflt := 6.0, defaultSkillYears
	want.Skills = []match.SkillFact{{Name: "Go", Years: &six, Level: "expert"}, {Name: "Rust", Years: &dflt}}
	got.Evidence = nil
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("facts\n got %+v\nwant %+v", got, want)
	}
	// The sponsorship answer the profile leaves blank stays unset: never false.
	if got.RequiresSponsorshipFuture != nil {
		t.Fatalf("future sponsorship read as %v", *got.RequiresSponsorshipFuture)
	}
	// The rest of what the user wrote about themselves is searched too (F-10).
	p, rep := workspace.ParseProfile([]byte("version: 1\nlinks:\n  github: https://github.com/example\ntargets:\n  roles:\n    - title: Data Engineer\n  locations: [Toronto]\n" +
		"preferences:\n  work_modes: [remote]\nresumes:\n  - id: data\n    file: resumes/data.pdf\n    default: true\n    tags: [spark]\nhistory:\n  employment:\n    - employer: Example Labs\n      summary: Ran the Kafka pipeline\n"))
	if !rep.OK() {
		t.Fatal(rep)
	}
	if f := FactsFromProfile(p); f.Employment[0].Summary != "Ran the Kafka pipeline" || !reflect.DeepEqual(f.ProfileText, []string{"Toronto", "remote", "https://github.com/example", "Data Engineer", "spark"}) {
		t.Fatalf("summary %q, profile text %q", f.Employment[0].Summary, f.ProfileText)
	}
	// The work modes are a fact of their own as well.
	if f := FactsFromProfile(p); !reflect.DeepEqual(f.WorkModes, []string{"remote"}) {
		t.Fatalf("work modes %q", f.WorkModes)
	}
}

func TestBind_Facts_RelationshipsFromProfile(t *testing.T) {
	p, rep := workspace.ParseProfile([]byte("version: 1\ncompany_relationships:\n  relationships:\n    - family: relative\n      company: Example Corp\n      aliases: [Example]\n      current: true\n      person_name: Sam Example\n      relation: Sibling\n      recorded: 2026-09-01\n  complete:\n    relative: 2026-09-02\n"))
	if !rep.OK() {
		t.Fatal(rep)
	}
	got := FactsFromProfile(p)
	wantRel := []match.RelationshipFact{{Family: match.FamilyRelative, Company: "Example Corp", Aliases: []string{"Example"}, Current: true,
		Person: "Sam Example", Relation: "Sibling", Recorded: "2026-09-01"}}
	if !reflect.DeepEqual(got.Relationships, wantRel) || got.RelationshipsComplete[match.FamilyRelative] != "2026-09-02" || len(got.RelationshipsComplete) != 1 {
		t.Fatalf("relationships %+v complete %+v", got.Relationships, got.RelationshipsComplete)
	}
}

func TestBind_Facts_EvidenceIsResumeLines(t *testing.T) {
	f := newFixture(t, alexProfile)
	f.write(t, workspace.FileCV, "# Alex Example\n\n- Built Go services at Example Labs\nPython scripting\n")
	in, err := f.binding(nil).build(context.Background(), Request{Host: "jobs.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Built Go services at Example Labs", "Python scripting"}
	if !reflect.DeepEqual(in.req.Facts.Evidence, want) {
		t.Fatalf("evidence %q, want %q", in.req.Facts.Evidence, want)
	}
}

// IN-068: a profile that cannot be read leaves the matcher as if the signal
// never existed.
func TestAnswerBank_Context_ProfileFailureMeansAbsent(t *testing.T) {
	ask := Request{RunID: "run-d", Host: "jobs.example.com", Questions: []match.Question{textQ("#dob", "Date of birth")}}
	cases := []struct {
		name    string
		profile string
		remove  bool
	}{
		{name: "undecodable file", profile: "candidate: [unclosed"},
		{name: "missing file", remove: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, c.profile)
			if c.remove {
				_ = removeIfPresent(f.ws.Path(workspace.FileProfile))
			}
			in, err := f.binding(nil).build(context.Background(), ask)
			if err != nil {
				t.Fatalf("a profile failure must not fail the match: %v", err)
			}
			facts := in.req.Facts
			facts.Evidence = nil
			if !reflect.DeepEqual(facts, match.Facts{}) {
				t.Fatalf("facts not absent: %+v", facts)
			}
			if _, err := f.binding(nil).Run(context.Background(), ask); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("readable profile feeds the matcher", func(t *testing.T) {
		f := newFixture(t, alexProfile)
		in, _ := f.binding(nil).build(context.Background(), ask)
		if in.req.Facts.DateOfBirth != "1990-04-02" {
			t.Fatalf("facts %+v", in.req.Facts)
		}
	})
}

// IN-069: the resolved job context's location, else the page's hint; a hint
// that settles a country outranks a stored place that settles none.
func TestAnswerBank_Context_PostingLocationPrecedence(t *testing.T) {
	const page = "https://jobs.example.com/posting/17"
	cases := []struct {
		name string
		jobs fakeJobs
		hint string
		want string
	}{
		{"job context wins over the hint", fakeJobs{page: "Remote, US"}, "Durham, NC", "Remote, US"},
		{"hint when the job context has no location", fakeJobs{page: ""}, "Durham, NC", "Durham, NC"},
		{"hint when the posting is unknown", fakeJobs{}, "Durham, NC", "Durham, NC"},
		{"nothing known", fakeJobs{}, "", ""},
		{"surrounding space is dropped", fakeJobs{page: "  Remote, US "}, "", "Remote, US"},
		{"a hint that settles a country over a stored place that settles none", fakeJobs{page: "NIH-Bethesda"}, "Bethesda, Maryland, United States", "Bethesda, Maryland, United States"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, alexProfile)
			b := f.binding(func(d *Deps) { d.Jobs = c.jobs })
			in, err := b.build(context.Background(), Request{Host: "jobs.example.com", Posting: Posting{URL: page, Location: c.hint}})
			if err != nil {
				t.Fatal(err)
			}
			if got := in.req.Posting.Location; got != c.want {
				t.Fatalf("location %q, want %q", got, c.want)
			}
		})
	}
	t.Run("hint when no jobs source is wired", func(t *testing.T) {
		f := newFixture(t, alexProfile)
		in, _ := f.binding(nil).build(context.Background(), Request{Host: "h", Posting: Posting{URL: page, Location: "Durham, NC"}})
		if in.req.Posting.Location != "Durham, NC" {
			t.Fatalf("location %q", in.req.Posting.Location)
		}
	})
}

func TestBind_Request_AutoAcceptFromSettings(t *testing.T) {
	f := newFixture(t, alexProfile)
	ctx := context.Background()
	in, _ := f.binding(nil).build(ctx, Request{Host: "h"})
	if !in.req.AutoAcceptLegalDeclarations {
		t.Fatal("the setting defaults to on")
	}
	off := false
	if _, err := f.bank.PatchSettings(ctx, answerbankPatch(&off)); err != nil {
		t.Fatal(err)
	}
	in, _ = f.binding(nil).build(ctx, Request{Host: "h"})
	if in.req.AutoAcceptLegalDeclarations {
		t.Fatal("the stored setting is not read on the next call")
	}
}

func TestBind_Run_BankThatCannotLoadFails(t *testing.T) {
	f := newFixture(t, alexProfile)
	f.write(t, workspace.FileAnswerBank, "version: [\n")
	if _, err := f.binding(nil).Run(context.Background(), Request{RunID: "r", Host: "h", Questions: []match.Question{textQ("#a", "Full name")}}); err == nil {
		t.Fatal("an unloadable bank must fail the match so the engine reports it unavailable")
	}
}

func TestBind_Telemetry_RecordedAfterEachMatch(t *testing.T) {
	f := newFixture(t, alexProfile)
	rec := &recorder{}
	b := f.binding(func(d *Deps) { d.Telemetry = rec })
	qs := []match.Question{textQ("#n", "Full name"), textQ("#x", "Describe your favorite reef"), {Selector: "#empty", Kind: match.KindText}}
	out, err := b.Run(context.Background(), Request{Host: "jobs.example.com", RunID: "run-1", Questions: qs})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.events) < 2 {
		t.Fatalf("events %+v", rec.events)
	}
	for _, e := range rec.events {
		if e.Question == "" {
			t.Fatalf("a question with no text produced an event: %+v", e)
		}
		if e.RunID != "run-1" || e.Host != "jobs.example.com" || !e.At.Equal(f.now) {
			t.Fatalf("event context %+v", e)
		}
	}
	want := learn.EventsFor("jobs.example.com", out.Prepared[0], out.Results[0], f.now, "run-1", 0)
	if !reflect.DeepEqual(rec.events[:len(want)], want) {
		t.Fatalf("first events %+v, want %+v", rec.events[:len(want)], want)
	}
}

// The engine names each fill with its own id; the application run it belongs
// to is what the events and system writes are recorded under, as only a run
// the server issued is taken.
func TestBind_AnEngineFillIsRecordedUnderItsApplicationRun(t *testing.T) {
	f := newFixture(t, alexProfile)
	rec := &recorder{}
	b := f.binding(func(d *Deps) { d.Telemetry = rec; d.Writes = rec })
	const run = "run_20261113T202239.502_4cb4d5"
	req := engine.MatchRequest{RunID: "0123456789abcdef01234567", RecordRun: run, Host: "jobs.example.com",
		Questions: []engine.WireQuestion{{Selector: "#n", Text: "Full name", Kind: "text", InputType: "text", Options: []string{}}}}
	if _, err := b.Match(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(rec.events) == 0 || len(rec.writes) == 0 {
		t.Fatalf("events %+v writes %+v", rec.events, rec.writes)
	}
	for _, e := range rec.events {
		if e.RunID != run {
			t.Fatalf("event run %q", e.RunID)
		}
	}
	for _, w := range rec.writes {
		if w[0] != run {
			t.Fatalf("write run %q", w[0])
		}
	}
}

func TestBind_SystemWrites_RecordedPerRunAndControl(t *testing.T) {
	f := newFixture(t, alexProfile)
	rec := &recorder{}
	b := f.binding(func(d *Deps) { d.Writes = rec })
	qs := []match.Question{textQ("#n", "Full name"), textQ("#x", "Describe your favorite reef")}
	out, err := b.Run(context.Background(), Request{Host: "jobs.example.com", RunID: "run-3", Questions: qs})
	if err != nil {
		t.Fatal(err)
	}
	if out.Results[0].Outcome == match.OutcomeAbstained {
		t.Fatalf("the name must be answered: %+v", out.Results[0])
	}
	if len(rec.writes) != 1 || rec.writes[0] != [3]string{"run-3", "#n", out.Results[0].Answer} {
		t.Fatalf("writes %+v", rec.writes)
	}
	// A call that names no run is a caller bug and is refused whole.
	rec.writes = nil
	if _, err := b.Run(context.Background(), Request{Host: "jobs.example.com", Questions: qs}); !errors.Is(err, ErrRunRequired) || len(rec.writes) != 0 {
		t.Fatalf("err %v writes %+v", err, rec.writes)
	}
	// With no log wired nothing fails.
	if _, err := f.binding(nil).Run(context.Background(), Request{Host: "jobs.example.com", RunID: "r", Questions: qs}); err != nil {
		t.Fatal(err)
	}
}

func TestBind_SystemWrites_OptionLabelIsWhatTheEngineWrites(t *testing.T) {
	f := newFixture(t, alexSponsored)
	rec := &recorder{}
	b := f.binding(func(d *Deps) { d.Writes = rec })
	q := match.Question{Selector: "#auth", Text: "Will you now require visa sponsorship to work for us?", Kind: match.KindRadioGroup,
		Options: []match.Option{{Value: "yes", Label: "Yes"}, {Value: "no", Label: "No"}}}
	out, err := b.Run(context.Background(), Request{Host: "jobs.example.com", RunID: "r", Questions: []match.Question{q}})
	if err != nil {
		t.Fatal(err)
	}
	r := out.Results[0]
	if r.ChosenLabel == "" || len(rec.writes) != 1 || rec.writes[0][2] != r.ChosenLabel {
		t.Fatalf("result %+v writes %+v", r, rec.writes)
	}
}

// Decision: a lone checkbox with no legend or container title is asked by its
// own label. The label is the only wording it has, and the matcher's
// declaration, marketing and self-identification rules then judge the sentence
// the user reads. Whatever the matcher answers, the engine's writer still
// enforces the consent rules before any tick.
func TestBind_LoneCheckbox_AskedByItsOwnLabel(t *testing.T) {
	lone := func(label string) match.Question {
		return match.Question{Selector: "#box", Kind: match.KindCheckbox, Required: true, Options: []match.Option{{Value: "on", Label: label}}}
	}
	t.Run("the label becomes the question", func(t *testing.T) {
		got := askable(lone("  I agree to the privacy policy "))
		if got.Text != "I agree to the privacy policy" {
			t.Fatalf("text %q", got.Text)
		}
	})
	t.Run("a question with text, or several options, is left alone", func(t *testing.T) {
		q := lone("I agree")
		q.Text = "Terms"
		if askable(q).Text != "Terms" {
			t.Fatal("text replaced")
		}
		q = lone("a")
		q.Text = ""
		q.Options = append(q.Options, match.Option{Label: "b"})
		if askable(q).Text != "" {
			t.Fatal("a group must not borrow an option as its question")
		}
	})
	t.Run("a required declaration follows the setting", func(t *testing.T) {
		ctx := context.Background()
		f := newFixture(t, alexProfile)
		q := lone("I certify that the information I have provided is true and complete")
		q.InputType = match.InputYesNo // the engine's lone box
		unset := false
		if _, err := f.bank.PatchSettings(ctx, answerbankPatch(&unset)); err != nil {
			t.Fatal(err)
		}
		off, err := f.binding(nil).Run(ctx, Request{RunID: "run-e", Host: "jobs.example.com", Questions: []match.Question{q}})
		if err != nil {
			t.Fatal(err)
		}
		if r := off.Results[0]; r.Outcome != match.OutcomeAbstained || r.Detail != match.DetailLegalDeclarationOff {
			t.Fatalf("setting off: %+v", r)
		}
		on := true
		if _, err := f.bank.PatchSettings(ctx, answerbankPatch(&on)); err != nil {
			t.Fatal(err)
		}
		got, err := f.binding(nil).Run(ctx, Request{RunID: "run-e", Host: "jobs.example.com", Questions: []match.Question{q}})
		if err != nil {
			t.Fatal(err)
		}
		if r := got.Results[0]; r.Outcome == match.OutcomeAbstained {
			t.Fatalf("setting on: %+v", r)
		}
	})
	t.Run("an optional marketing opt-in is never ticked", func(t *testing.T) {
		f := newFixture(t, alexProfile)
		on := true
		if _, err := f.bank.PatchSettings(context.Background(), answerbankPatch(&on)); err != nil {
			t.Fatal(err)
		}
		q := lone("Yes, keep me informed about future roles and job alerts")
		q.Required = false
		out, err := f.binding(nil).Run(context.Background(), Request{RunID: "run-e", Host: "jobs.example.com", Questions: []match.Question{q}})
		if err != nil {
			t.Fatal(err)
		}
		if r := out.Results[0]; r.Outcome != match.OutcomeAbstained {
			t.Fatalf("an optional opt-in was answered: %+v", r)
		}
	})
}

func TestBind_Evidence_KnowledgeSnippetsForLadderQuestionsOnly(t *testing.T) {
	f := newFixture(t, alexProfile)
	kn := &fakeEvidence{}
	b := f.binding(func(d *Deps) { d.Evidence = kn })
	ladder := match.Question{Selector: "#go", Text: "Rate your proficiency with Go", Kind: match.KindSelect, Options: []match.Option{
		{Label: "No experience"}, {Label: "Beginner"}, {Label: "Intermediate"}, {Label: "Advanced"}, {Label: "Expert"}}}
	plain := match.Question{Selector: "#c", Text: "Country", Kind: match.KindSelect, Options: []match.Option{{Label: "US"}, {Label: "CA"}, {Label: "MX"}}}
	in, err := b.build(context.Background(), Request{Host: "h", Questions: []match.Question{plain, ladder}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(kn.queries, []string{"Rate your proficiency with Go"}) {
		t.Fatalf("queries %q", kn.queries)
	}
	if !strings.Contains(strings.Join(in.req.Facts.Evidence, "|"), "Led a Go migration") {
		t.Fatalf("evidence %q", in.req.Facts.Evidence)
	}
}

// ComposeSources adds the CV and profile evidence lines with their role; the
// seeded template CV adds none of its own.
func TestBind_ComposeSources_EvidenceLines(t *testing.T) {
	p, rep := workspace.ParseProfile([]byte(alexProfile))
	if !rep.OK() {
		t.Fatal(rep.Issues)
	}
	cv := "# Alex\n\n## Projects\n\n- tidekit: a command line tool that turns tide tables into survey schedules.\n"
	src := ComposeSources(p, cv, nil)
	var found bool
	for _, l := range src.Evidence {
		found = found || l.Entry == "Project: tidekit" && strings.HasPrefix(l.Text, "A command line tool")
	}
	if !found {
		t.Fatalf("%+v", src.Evidence)
	}
	ws := workspace.Open(t.TempDir())
	if _, err := ws.Init(); err != nil {
		t.Fatal(err)
	}
	c, err := ws.Read(workspace.FileCV)
	if err != nil {
		t.Fatal(err)
	}
	tpl := ComposeSources(p, string(c.Data), nil)
	for _, l := range tpl.Evidence {
		if strings.HasPrefix(l.Ref, "cv.md") {
			t.Fatalf("a template line is evidence: %+v", l)
		}
	}
	if len(tpl.CV) != 0 {
		t.Fatalf("template lines are CV facts: %q", tpl.CV)
	}
	// The matcher's evidence takes no template line either.
	f := newFixture(t, alexProfile)
	f.write(t, workspace.FileCV, string(c.Data))
	in, err := f.binding(nil).build(context.Background(), Request{Host: "h", Questions: []match.Question{{Selector: "#c", Text: "Country", Kind: match.KindText}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(in.req.Facts.Evidence) != 0 {
		t.Fatalf("template lines are evidence: %q", in.req.Facts.Evidence)
	}
}

// A Lever form asks two profile facts through its options: a bare "Work
// Authorization" heading whose radios state the United States question, and a
// "Where are you located?" select of US state names. Both are answered from the
// profile through the shipped bank, the state in the form the options use.
func TestBind_Preview_LeverStateSelectAndWorkAuthorizationHeading(t *testing.T) {
	names := strings.Split("Alabama|Alaska|Arizona|California|Delaware|District of Columbia|Florida|Georgia|Iowa|New York|North Carolina|North Dakota|Ohio|South Carolina|Texas|Utah|Virginia|West Virgina|Wyoming", "|")
	abbrs := strings.Split("AL|AK|AZ|CA|DE|DC|FL|GA|IA|NY|NC|ND|OH|SC|TX|UT|VA|WV|WY", "|")
	opts := func(ls ...string) []match.Option {
		out := []match.Option{{Value: "", Label: "Select..."}}
		for _, l := range ls {
			out = append(out, match.Option{Value: l, Label: l})
		}
		return out
	}
	located := "Where are you located? (We are always expanding, but there is a small list of states we do not hire in, including Alabama, Alaska and Utah, and we can only hire permanent US residents at this time.)"
	ask := func(t *testing.T, profile string, q match.Question) match.Result {
		t.Helper()
		f := newFixture(t, profile)
		out, err := f.binding(nil).Preview(context.Background(), Request{Host: "jobs.lever.co", Posting: Posting{Company: "Example Co", Location: "Remote, United States"}, Questions: []match.Question{q}})
		if err != nil || len(out.Results) != 1 {
			t.Fatalf("err %v, %d results", err, len(out.Results))
		}
		return out.Results[0]
	}
	filled := func(r match.Result) bool {
		return r.Outcome == match.OutcomeFilled || r.Outcome == match.OutcomeFilledReview
	}
	spelled := strings.Replace(alexProfile, "region: NC", "region: North Carolina", 1)
	for _, c := range []struct {
		name, profile string
		options       []string
		want          string
	}{
		{"code in the profile, names in the list", alexProfile, names, "North Carolina"},
		{"code in the profile, codes in the list", alexProfile, abbrs, "NC"},
		{"name in the profile, codes in the list", spelled, abbrs, "NC"},
		{"name in the profile, names in the list", spelled, names, "North Carolina"},
	} {
		t.Run("located/"+c.name, func(t *testing.T) {
			r := ask(t, c.profile, match.Question{Selector: "loc", Text: located, Kind: match.KindSelect, Required: true, Options: opts(c.options...)})
			if !filled(r) || r.EntryID != "address-state" || r.Answer != c.want {
				t.Fatalf("got %s %q %q %q %v", r.Outcome, r.EntryID, r.Answer, r.Reason, r.Trace)
			}
		})
	}
	auth := match.Question{Selector: "wa", Text: "Work Authorization", Kind: match.KindRadioGroup, Required: true, Options: []match.Option{
		{Value: "Yes, I am authorized to work in the United States", Label: "Yes, I am authorized to work in the United States"},
		{Value: "No, I am not authorized to work in the United States", Label: "No, I am not authorized to work in the United States"},
	}}
	t.Run("work authorization heading", func(t *testing.T) {
		r := ask(t, alexProfile, auth)
		if !filled(r) || r.EntryID != "work-auth-us" || r.Answer != "Yes, I am authorized to work in the United States" {
			t.Fatalf("got %s %q %q %q %v", r.Outcome, r.EntryID, r.Answer, r.Reason, r.Trace)
		}
	})
	t.Run("work authorization heading with no authorized country recorded", func(t *testing.T) {
		r := ask(t, strings.Replace(alexProfile, "  authorized_countries: [US]\n", "", 1), auth)
		if filled(r) {
			t.Fatalf("answered without a recorded fact: %q", r.Answer)
		}
	})
}

// "Visa Sponsorship" names no country, so the posting's location says which
// one it is about: a bare "Remote" names none and is placed in the one recorded
// country (owner decision), as the same posting with its board's country is.
func TestBind_Preview_SponsorshipFollowsThePostingCountry(t *testing.T) {
	q := match.Question{Selector: "vs", Text: "Visa Sponsorship", Kind: match.KindRadioGroup, Required: true, Options: []match.Option{
		{Value: "Yes, I need visa sponsorship", Label: "Yes, I need visa sponsorship"},
		{Value: "No, I do not need visa sponsorship", Label: "No, I do not need visa sponsorship"},
	}}
	for loc, want := range map[string]string{"Remote": "No, I do not need visa sponsorship", "Remote, United States": "No, I do not need visa sponsorship"} {
		t.Run(loc, func(t *testing.T) {
			f := newFixture(t, alexSponsored)
			out, err := f.binding(nil).Preview(context.Background(), Request{Host: "jobs.lever.co", Posting: Posting{Location: loc}, Questions: []match.Question{q}})
			if err != nil {
				t.Fatal(err)
			}
			r := out.Results[0]
			if r.Answer != want || (want == "" && r.Reason != match.ReasonContextPolicy) {
				t.Fatalf("got %s %q %q", r.Outcome, r.Answer, r.Reason)
			}
		})
	}
}

// A question about the user's own experience is No, from the shipped skill
// gate, only when the user's information was read: an unreadable knowledge
// base, or a résumé that is only the seeded template, answers nothing. With
// semantic retrieval on, a snippet the semantic search ranks near the question
// may mention it in other words, so it answers nothing either. A question of
// another shape is never searched (F-10).
func TestBind_OwnExperience_NoOnlyWhenNothingMentionsIt(t *testing.T) {
	yesNo := []match.Option{{Value: "Yes", Label: "Yes"}, {Value: "No", Label: "No"}}
	sports := match.Question{Selector: "#sports", Text: "Do you have experience working in the sports industry?", Kind: match.KindRadioGroup, Options: yesNo}
	near := []kb.Fact{{Text: "Ticketing\nBuilt ticketing for a league.", SourceKind: kb.KindSnippet, SourceRef: "knowledge/experience/club.md#Ticketing", Stage: kb.StageSemantic}}
	for _, c := range []struct {
		name              string
		template, lexical bool
		evidence          *fakeEvidence
		want              string // the label chosen, "" an abstain
	}{
		{"nothing mentions it", false, false, &fakeEvidence{}, "No"},
		{"the knowledge base cannot be read", false, false, &fakeEvidence{err: errors.New("unreadable")}, ""},
		{"the seeded résumé", true, false, &fakeEvidence{}, ""},
		{"a snippet the semantic search ranks near it", false, false, &fakeEvidence{facts: near}, ""},
		{"semantic retrieval off", false, true, &fakeEvidence{facts: near}, "No"},
	} {
		f := newFixture(t, alexProfile)
		semantic := !c.lexical
		if _, err := f.bank.PatchSettings(context.Background(), answerbank.SettingsPatch{KnowledgeSemanticRetrieval: &semantic}); err != nil {
			t.Fatal(err)
		}
		if c.template {
			if _, err := f.ws.Init(); err != nil {
				t.Fatal(err)
			}
		} else {
			f.write(t, workspace.FileCV, "# Alex\n\n- Built the billing service in Go.\n")
		}
		b := f.binding(func(d *Deps) { d.Evidence = c.evidence })
		out, err := b.Preview(context.Background(), Request{Host: "jobs.example.com", Questions: []match.Question{sports}})
		if err != nil {
			t.Fatal(err)
		}
		if r := out.Results[0]; c.want == "" && (r.Outcome != match.OutcomeAbstained || r.ChosenLabel != "") || c.want != "" && r.ChosenLabel != c.want {
			t.Errorf("%s: %+v", c.name, r)
		}
		got, err := b.OwnSearch(context.Background(), engine.MatchRequest{Host: "jobs.example.com", Questions: []engine.WireQuestion{
			{Selector: "#sports", Text: sports.Text, Kind: "radio-group", Options: []string{"Yes", "No"}},
			{Selector: "#move", Text: "Are you willing to relocate to Durham?", Kind: "radio-group", Options: []string{"Yes", "No"}},
		}})
		if err != nil || len(got) != 2 || got[0].Absent() != (c.want == "No") || len(got[1].Terms) != 0 {
			t.Errorf("%s: own search %+v %v", c.name, got, err)
		}
	}
	// A years question about a named skill reads the knowledge base too, so a
	// skill nothing names has no years with it (Huntress's Ruby on Rails box);
	// a knowledge base that cannot be read leaves the owner's default of one
	// year.
	ruby := match.Question{Selector: "#ruby", Text: "How many years of experience do you have working with Ruby on Rails?", Kind: match.KindText}
	for _, c := range []struct {
		evidence *fakeEvidence
		want     string
	}{{&fakeEvidence{}, "0"}, {&fakeEvidence{err: errors.New("unreadable")}, "1"}} {
		f := newFixture(t, alexProfile)
		f.write(t, workspace.FileCV, "# Alex\n\n- Built the billing service in Go.\n")
		b := f.binding(func(d *Deps) { d.Evidence = c.evidence })
		out, err := b.Preview(context.Background(), Request{Host: "jobs.example.com", Questions: []match.Question{ruby}})
		if err != nil {
			t.Fatal(err)
		}
		if r := out.Results[0]; r.Answer != c.want {
			t.Errorf("years, knowledge error %v: %+v", c.evidence.err, r)
		}
	}
	// Years in a kind of role over a years menu (Akuity: "in a technical
	// marketing role at a B2B SaaS company") read it as well. The field's
	// "technical" and a market-data tool name no technical marketing.
	var menu []match.Option
	for _, l := range []string{"0", "1-2", "2-3", "3-4", "4+"} {
		menu = append(menu, match.Option{Value: l, Label: l})
	}
	role := match.Question{Selector: "#role", Text: "How many years of experience do you have in a technical marketing role at a B2B SaaS company?", Kind: match.KindSelect, Options: menu}
	for _, c := range []struct {
		evidence *fakeEvidence
		want     string
	}{{&fakeEvidence{}, "0"}, {&fakeEvidence{err: errors.New("unreadable")}, "1-2"}} {
		f := newFixture(t, alexProfile)
		f.write(t, workspace.FileCV, "# Alex\n\n- Built the billing service in Go.\n- Exposed typed tools for market data and technical indicators.\n")
		b := f.binding(func(d *Deps) { d.Evidence = c.evidence })
		out, err := b.Preview(context.Background(), Request{Host: "jobs.example.com", Questions: []match.Question{role}})
		if err != nil {
			t.Fatal(err)
		}
		if r := out.Results[0]; r.ChosenLabel != c.want {
			t.Errorf("role years, knowledge error %v: %+v", c.evidence.err, r)
		}
	}
}

// A skill the CV's skills section names counts as the user's own, so a
// checklist ticks it even when the profile's skills list leaves it out; a slash
// pair names both of its skills, and a skill neither names stays unticked.
func TestBind_Run_ASkillTheCVNamesTicksItsChecklistOption(t *testing.T) {
	f := newFixture(t, alexProfile)
	f.write(t, workspace.FileCV, "# Alex Example\n\n## Skills\n\nLanguages: Java, Python, C/C++, SQL\n")
	var opts []match.Option
	for _, l := range []string{"Python", "C++", "Rust", "Java", "C#", "JavaScript"} {
		opts = append(opts, match.Option{Value: l, Label: l})
	}
	q := match.Question{Selector: "#langs", Text: "Which of the following languages do you consider yourself professionally proficient in?",
		Kind: match.KindCheckbox, InputType: match.InputMultiSelect, Options: opts}
	out, err := f.binding(nil).Run(context.Background(), Request{RunID: "run-s", Host: "jobs.example.com", Questions: []match.Question{q}})
	if err != nil {
		t.Fatal(err)
	}
	// Rust is the profile's own skill; the rest come from the CV.
	if r := out.Results[0]; !reflect.DeepEqual(r.ChosenLabels, []string{"Python", "C++", "Rust", "Java"}) {
		t.Fatalf("%+v", r)
	}
	// A skill only the CV names, or one the profile lists without years,
	// counts as one to two years; the profile's own years stand for a skill
	// it records them for.
	years := func(text string) string {
		t.Helper()
		out, err := f.binding(nil).Run(context.Background(), Request{RunID: "run-s", Host: "jobs.example.com", Questions: []match.Question{textQ("#y", text)}})
		if err != nil {
			t.Fatal(err)
		}
		return out.Results[0].Answer
	}
	if got := years("How many years of experience do you have with Python?"); got != "1" {
		t.Errorf("Python years %q", got)
	}
	if got := years("How many months of experience do you have with Python?"); got != "18" {
		t.Errorf("Python months %q", got)
	}
	if got := years("How many years of experience do you have with Rust?"); got != "1" {
		t.Errorf("Rust years %q", got)
	}
	if got := years("How many years of experience do you have with Go?"); got != "6" {
		t.Errorf("Go years %q", got)
	}
}

// The CV sections a tailored document's manifest does not read (licenses and
// certifications, publications) reach the composer as evidence lines under
// their section heading, and every CV education heading grounds a degree.
func TestBind_ComposeSources_CredentialSections(t *testing.T) {
	p, rep := workspace.ParseProfile([]byte(alexProfile))
	if !rep.OK() {
		t.Fatal(rep.Issues)
	}
	cv := "# Alex\n\n## Licenses & Certifications\n\n- CFA Charterholder\n\n## Publications\n\n- Liquidity premia in municipal bonds, Example Journal of Finance\n\n" +
		"## Education\n\n### PhD in Molecular Biology, Example Institute (2012 to 2017)\n"
	src := ComposeSources(p, cv, nil)
	var lines []string
	for _, l := range src.Evidence {
		lines = append(lines, l.Entry+"|"+l.Text)
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{"Licenses & Certifications|CFA Charterholder", "Publications|Liquidity premia in municipal bonds, Example Journal of Finance"} {
		if !strings.Contains(got, want) {
			t.Errorf("evidence lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Molecular Biology") {
		t.Errorf("an education heading is an evidence line:\n%s", got)
	}
	creds := strings.Join(src.Credentials, "\n")
	for _, want := range []string{"CFA Charterholder", "PhD in Molecular Biology, Example Institute", "BS Computer Science"} {
		if !strings.Contains(creds, want) {
			t.Errorf("credentials lack %q:\n%s", want, creds)
		}
	}
}
