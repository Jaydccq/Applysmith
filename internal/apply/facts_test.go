package apply

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/answerbank/bind"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
)

func ptr(b bool) *bool { return &b }

func TestApply_Check_OfficeListAnsweredFromPostingLocation(t *testing.T) {
	label := "If you are applying for a role in a specific location, identify the specified location/s for the role"
	own := ladderOwn
	own.Relocation = &match.RelocationPolicy{Default: "open", Places: []string{"Austin, Texas", "Denver, Colorado", "Boston, Massachusetts"}}
	l := newLadderEnv(t, own, nil, nil)
	l.s.jd = "Platform Engineer\nLocation: Austin, Texas\nBuild things."
	ps := l.run(engine.Row{Selector: "#o", Label: label, Required: true, Outcome: "abstained", MatcherReason: "no-entry", Kind: "text"})
	if v, ok := answeredValue(l.senv, "#o"); !ok || v != "Austin, Texas" || len(ps.leftovers) != 0 {
		t.Fatalf("answers %+v leftovers %+v: the posting's own location is the answer, not the offices the relocation policy accepts", l.page.answers, ps.leftovers)
	}
	// With no posting location the row stays for the user.
	n := newLadderEnv(t, own, nil, nil)
	ps = n.run(engine.Row{Selector: "#o", Label: label, Required: true, Outcome: "abstained", MatcherReason: "no-entry", Kind: "text"})
	if len(n.page.answers) != 0 || len(ps.leftovers) != 1 {
		t.Fatalf("answers %+v leftovers %+v", n.page.answers, ps.leftovers)
	}
	// An office option list takes the one option that names the posting's city.
	if got, ok := OfficeListAnswer(label, "Austin, Texas", []string{"Austin, TX office", "Denver office"}); !ok || got != "Austin, TX office" {
		t.Fatalf("option answer %q %v", got, ok)
	}
	if _, ok := OfficeListAnswer(label, "Austin, Texas", []string{"Denver office", "Boston office"}); ok {
		t.Fatal("an option list that does not name the posting's city was answered")
	}
	if _, ok := OfficeListAnswer("Which office do you prefer?", "Austin, Texas", nil); ok {
		t.Fatal("a preference question is not an office list that defers to the posting")
	}
	// A flag on exactly the posting's office does not block Submit.
	ps2 := &pageState{flags: []gates.LabeledFlag{{Label: "Offices", Flag: gates.Flag{Value: "Austin, Texas", Probability: 0.88, HasProbability: true}}}}
	l.s.pages = []*pageState{ps2}
	if v := l.s.finalVerdict(); !v.Allowed() {
		t.Fatalf("verdict %+v: the flag names the posting's own office", v)
	}
	other := &pageState{flags: []gates.LabeledFlag{{Label: "Offices", Flag: gates.Flag{Value: "Denver, Colorado", Probability: 0.88, HasProbability: true}}}}
	l.s.pages = []*pageState{other}
	if v := l.s.finalVerdict(); v.Allowed() {
		t.Fatal("a flag on another office was dropped")
	}
}

// Cursor: "Citizenship Status" answered "Other / Eligible for required
// authorization" opens a required "If Other, please explain" box, which takes
// the profile's visa status; a status the user's own choice did not make Other
// opens nothing, and a blank visa status is reported for the user.
func TestApply_Check_AnOtherCitizenshipStatusIsExplainedByTheVisaStatus(t *testing.T) {
	gate := engine.Row{Selector: "#g", Label: "Citizenship Status", Required: true, Outcome: "filled", Value: "Other / Eligible for required authorization", OptionLabel: "Other / Eligible for required authorization", Kind: "radio-group"}
	box := engine.Row{Selector: "#e", Label: "Citizenship Status If Other, please explain:", Required: true, Outcome: "abstained", MatcherReason: "sensitive-category", Kind: "text"}
	own := ladderOwn
	own.VisaStatus = "F-1 OPT"
	l := newLadderEnv(t, own, nil, nil)
	if ps := l.run(gate, box); len(ps.leftovers) != 0 {
		t.Fatalf("leftovers %+v", ps.leftovers)
	}
	if v, ok := answeredValue(l.senv, "#e"); !ok || v != "F-1 OPT" {
		t.Fatalf("answers %+v: the box takes the visa status", l.page.answers)
	}
	citizen := gate
	citizen.Value, citizen.OptionLabel = "U.S. Citizen", "U.S. Citizen"
	c := newLadderEnv(t, own, nil, nil)
	if ps := c.run(citizen, box); len(c.page.answers) != 0 || len(ps.leftovers) != 1 {
		t.Fatalf("answers %+v leftovers %+v", c.page.answers, ps.leftovers)
	}
	b := newLadderEnv(t, ladderOwn, nil, nil)
	if ps := b.run(gate, box); len(b.page.answers) != 0 || len(ps.leftovers) != 1 || !strings.Contains(ps.leftovers[0].Detail, "visa status is blank") {
		t.Fatalf("answers %+v leftovers %+v", b.page.answers, ps.leftovers)
	}
}

func TestApply_Check_GatedFollowUpReAskedAfterRung1(t *testing.T) {
	gate := engine.Row{Selector: "#g", Label: "Will you now or in the future require visa sponsorship?", Required: true, Outcome: "filled", Value: "Yes", OptionLabel: "Yes", Kind: "radio-group"}
	box := engine.Row{Selector: "#e", Label: "If yes, please explain", Required: true, Outcome: "abstained", MatcherReason: "placeholder-empty", Kind: "text"}
	own := ladderOwn
	own.SponsorshipDescription = "An employer-sponsored work visa will be needed for a transfer."
	l := newLadderEnv(t, own, nil, nil)
	ps := l.run(gate, box)
	if v, ok := answeredValue(l.senv, "#e"); !ok || v != own.SponsorshipDescription || len(ps.leftovers) != 0 {
		t.Fatalf("answers %+v leftovers %+v: under a sponsorship Yes the box takes the sponsorship-type description", l.page.answers, ps.leftovers)
	}
	// A follow-up may ask its own question under the gate's condition.
	so := box
	so.Label = "If yes, what type of visa will you need?"
	f := newLadderEnv(t, own, nil, nil)
	ps = f.run(gate, so)
	if v, ok := answeredValue(f.senv, "#e"); !ok || v != own.SponsorshipDescription || len(ps.leftovers) != 0 {
		t.Fatalf("answers %+v leftovers %+v", f.page.answers, ps.leftovers)
	}
	// iCapital (live): a box asking a question of its own is no follow-up of the
	// sponsorship Yes above it.
	future := gate
	future.Label = "Do you require any type of visa sponsorship for employment in the future (i.e. student visa transfer to H1B, etc)?"
	bonus := box
	bonus.Label = "What is your desired bonus expectation? Please specify the amount and currency."
	b := newLadderEnv(t, own, nil, nil)
	ps = b.run(future, bonus)
	if _, ok := answeredValue(b.senv, "#e"); ok || len(ps.leftovers) != 1 {
		t.Fatalf("answers %+v leftovers %+v: the bonus box took the sponsorship description", b.page.answers, ps.leftovers)
	}
	// No description: the box is reported for the user.
	n := newLadderEnv(t, ladderOwn, nil, nil)
	ps = n.run(gate, box)
	if len(ps.leftovers) != 1 || !strings.Contains(ps.leftovers[0].Detail, "sponsorship-type-description is blank") || len(n.page.answers) != 0 {
		t.Fatalf("leftovers %+v answers %+v", ps.leftovers, n.page.answers)
	}
	// A No opens nothing.
	no := gate
	no.Value, no.OptionLabel = "No", "No"
	o := newLadderEnv(t, own, nil, nil)
	ps = o.run(no, box)
	if len(o.page.answers) != 0 || len(ps.leftovers) != 1 {
		t.Fatalf("answers %+v leftovers %+v", o.page.answers, ps.leftovers)
	}
	// Any other gate is re-asked of the bank together with the box, in one request.
	m := &fakeMatcher{reply: func(req engine.MatchRequest) engine.MatchResponse {
		return engine.MatchResponse{Results: []engine.Resolution{{Outcome: "filled", Value: "Yes"}, {Outcome: "filled", Value: "Two years at a bank."}}}
	}}
	other := engine.Row{Selector: "#g", Label: "Have you worked in finance?", Required: true, Outcome: "filled", Value: "Yes", OptionLabel: "Yes", Kind: "radio-group"}
	r := newLadderEnv(t, ladderOwn, nil, func(d *Deps) { d.Page.Matcher = m })
	ps = r.run(other, box)
	if len(m.reqs) != 1 || len(m.reqs[0].Questions) != 2 || m.reqs[0].Questions[0].Selector != "#g" || m.reqs[0].Questions[1].Selector != "#e" {
		t.Fatalf("bank requests %+v: the gate and the box travel together", m.reqs)
	}
	if v, ok := answeredValue(r.senv, "#e"); !ok || v != "Two years at a bank." || len(ps.leftovers) != 0 {
		t.Fatalf("answers %+v leftovers %+v", r.page.answers, ps.leftovers)
	}
}

func TestApply_Check_SponsorshipNowOnlyAnswersNoWhenNeedIsFuture(t *testing.T) {
	yn := []string{"Yes", "No"}
	now := "Do you currently require sponsorship before you could begin working?"
	if v, ok := SponsorshipNowOnlyAnswer(now, ptr(false), ptr(true), yn); !ok || v != "No" {
		t.Fatalf("%q %v", v, ok)
	}
	for name, c := range map[string]struct {
		label       string
		now, future *bool
	}{
		"future is unset":       {now, ptr(false), nil},
		"needed now":            {now, ptr(true), ptr(true)},
		"now is unset":          {now, nil, ptr(true)},
		"never needed":          {now, ptr(false), ptr(false)},
		"asks about the future": {"Will you now or in the future require sponsorship?", ptr(false), ptr(true)},
		"not sponsorship":       {"Are you currently employed?", ptr(false), ptr(true)},
	} {
		if v, ok := SponsorshipNowOnlyAnswer(c.label, c.now, c.future, yn); ok {
			t.Errorf("%s: answered %q", name, v)
		}
	}
	if _, ok := SponsorshipNowOnlyAnswer(now, ptr(false), ptr(true), []string{"Maybe", "Later"}); ok {
		t.Error("a control with no No option was answered")
	}
	// Through the check: the user's two answers are the evidence.
	own := ladderOwn
	own.SponsorshipNow, own.SponsorshipFuture = ptr(false), ptr(true)
	l := newLadderEnv(t, own, nil, nil)
	l.observe("#s", now, match.KindRadioGroup, match.InputText, "Yes", "No")
	ps := l.run(engine.Row{Selector: "#s", Label: now, Required: true, Outcome: "abstained", MatcherReason: "ambiguous", Kind: "radio-group"})
	if v, ok := answeredValue(l.senv, "#s"); !ok || v != "No" || len(ps.leftovers) != 0 {
		t.Fatalf("answers %+v leftovers %+v", l.page.answers, ps.leftovers)
	}
	// Work authorization in general is never answered from here.
	g := newLadderEnv(t, own, nil, nil)
	g.run(engine.Row{Selector: "#w", Label: "Are you legally authorized to work in the United States?", Required: true, Outcome: "abstained", MatcherReason: "no-entry", Kind: "text"})
	if len(g.page.answers) != 0 {
		t.Fatalf("answers %+v", g.page.answers)
	}
}

func TestApply_Check_AvailabilityMonthYearFromStartDate(t *testing.T) {
	label := "Please input month and year you are available to start"
	today := time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)
	for _, c := range []struct{ earliest, want string }{
		{"2027-06-15", "June 2027"}, {"2027-03-20", "March 2027"}, {"2028-01-02", "January 2028"},
		// A passed or blank start date is thirty days from today (owner's rule).
		{"2026-11-01", "April 2027"}, {"", "April 2027"},
	} {
		if got, ok := AvailabilityMonthYearAnswer(label, c.earliest, nil, today); !ok || got != c.want {
			t.Errorf("%s: %q %v, want %q", c.earliest, got, ok, c.want)
		}
	}
	for _, bad := range []string{"soon", "June 2027", match.AskMe} {
		if _, ok := AvailabilityMonthYearAnswer(label, bad, nil, today); ok {
			t.Errorf("start date %q was used", bad)
		}
	}
	if _, ok := AvailabilityMonthYearAnswer("Please input month and year of your last job", "2027-06-15", nil, today); ok {
		t.Error("an employment-date box was answered from the start date")
	}
	// The month is the matcher's first day to start: moved past a degree in
	// progress, and unknown beside an education entry without an end month.
	enrolled := []match.EducationFact{{School: "Example State University", Degree: "Master of Science", End: "2027-12"}, {School: "Example College", End: "2024-05"}}
	if got, ok := AvailabilityMonthYearAnswer(label, "2027-06-15", enrolled, today); !ok || got != "December 2027" {
		t.Errorf("a degree in progress: %q %v, want December 2027", got, ok)
	}
	if got, ok := AvailabilityMonthYearAnswer(label, "2027-06-15", []match.EducationFact{{School: "Example College"}}, today); ok {
		t.Errorf("an education entry without an end month: %q", got)
	}
	own := ladderOwn
	own.EarliestStart = "2027-06-15"
	own.Employment = []match.EmploymentFact{{Employer: "Past Corp", Title: "Engineer", Start: "2020-01", End: "2023-08"}}
	l := newLadderEnv(t, own, nil, nil)
	l.run(engine.Row{Selector: "#a", Label: label, Required: true, Outcome: "abstained", MatcherReason: "no-entry", Kind: "text"})
	if v, ok := answeredValue(l.senv, "#a"); !ok || v != "June 2027" {
		t.Fatalf("answers %+v: a past job's end month never answers availability", l.page.answers)
	}
}

func TestApply_Ladder_DeterministicAnswersNarrowAndNullOutsideShape(t *testing.T) {
	cvNone := "Alex Example\nBuilt a metrics pipeline in Go for payments.\nSkills: Go, SQL"
	cvList := "Alex Example\n\n## Certifications\n- Cloud Practitioner\n- Security Fundamentals\n\n## Skills\nGo"
	list := "Please list your licenses and certifications"
	if v, ok := LicenseListAnswer(list, cvNone, nil); !ok || v != "None" {
		t.Fatalf("a CV naming none: %q %v", v, ok)
	}
	if v, ok := LicenseListAnswer(list, cvList, nil); !ok || v != "Cloud Practitioner; Security Fundamentals" {
		t.Fatalf("a CV naming some: %q %v", v, ok)
	}
	if _, ok := LicenseListAnswer(list, cvNone, []string{"Yes", "No"}); ok {
		t.Fatal("silence answered a control with options")
	}
	if _, ok := LicenseListAnswer(list, "", nil); ok {
		t.Fatal("an empty CV proved something")
	}
	if _, ok := LicenseListAnswer("Do you hold a driving licence?", cvNone, nil); ok {
		t.Fatal("a question that is not a list was answered")
	}

	built := "Have you built systems in payments?"
	yn := []string{"Yes", "No"}
	if v, cite, ok := BuiltSystemsAnswer(built, cvNone, yn); !ok || v != "Yes" || !strings.Contains(cite, "payments") {
		t.Fatalf("%q %q %v", v, cite, ok)
	}
	if _, _, ok := BuiltSystemsAnswer("Have you built systems in genomics?", cvNone, yn); ok {
		t.Fatal("a No was answered from silence")
	}
	if _, _, ok := BuiltSystemsAnswer(built, cvNone, nil); ok {
		t.Fatal("a control with no options was answered")
	}

	jobs := []match.EmploymentFact{
		{Employer: "First Corp", Title: "Software Engineering Intern", Start: "2019-06", End: "2019-08"},
		{Employer: "Second Corp", Title: "Data Intern", Start: "2020-06", End: "2020-08"},
		{Employer: "Third Corp", Title: "Engineer", Start: "2021-01", End: "present"},
	}
	if v, ok := LastInternshipAnswer("Where was your last internship?", jobs); !ok || v != "Second Corp" {
		t.Fatalf("%q %v", v, ok)
	}
	if _, ok := LastInternshipAnswer("Where was your last internship?", jobs[2:]); ok {
		t.Fatal("a profile with no internship was answered")
	}
	if _, ok := LastInternshipAnswer("Where do you work now?", jobs); ok {
		t.Fatal("another question was answered")
	}

	// The whole path is quiet about everything else.
	l := newLadderEnv(t, Own{CV: cvNone, Employment: jobs}, nil, nil)
	l.run(
		engine.Row{Selector: "#1", Label: list, Required: true, Outcome: "abstained", MatcherReason: "no-entry", Kind: "text"},
		engine.Row{Selector: "#2", Label: "Where was your last internship?", Required: true, Outcome: "abstained", MatcherReason: "no-entry", Kind: "text"},
		engine.Row{Selector: "#3", Label: "Anything else we should know?", Required: true, Outcome: "abstained", MatcherReason: "no-entry", Kind: "text"},
	)
	if v, _ := answeredValue(l.senv, "#1"); v != "None" {
		t.Fatalf("answers %+v", l.page.answers)
	}
	if v, _ := answeredValue(l.senv, "#2"); v != "Second Corp" {
		t.Fatalf("answers %+v", l.page.answers)
	}
	if _, ok := answeredValue(l.senv, "#3"); ok {
		t.Fatal("an unrelated question was answered")
	}
}

func TestApply_Check_PostingLocationReadsFullStateNames(t *testing.T) {
	for jd, want := range map[string]string{
		"Title\nLocation: New York, New York\nMore": "New York, New York",
		"Job Location - West Virginia":              "West Virginia",
		"Office Location: North Carolina; Remote":   "North Carolina; Remote",
		"Location is flexible":                      "",
	} {
		if got := postingLocation(jd); got != want {
			t.Errorf("%q: %q, want %q", jd, got, want)
		}
	}
}

// A question about the user's own experience that no entry claims and nothing
// the user gave mentions is answered No before any model call; one the
// information mentions goes to rung 2, which answers it from the knowledge
// section it cites and records the section, or leaves it open (F-10).
func TestApply_Ladder_OwnExperienceFromTheKnowledgeBase(t *testing.T) {
	const club = "knowledge/experience/club.md#Ticketing"
	shown := []match.KnowledgeSection{{Ref: club, Text: "Built ticketing for a sports league."}}
	absent := match.OwnSearch{Terms: []string{"open source"}, Complete: true}
	found := match.OwnSearch{Terms: []string{"sports"}, Found: []string{"sports (kb:" + club + ")"}, Complete: true}
	row := func(label, kind string, options ...string) engine.Row {
		r := abstained("#q", label, kind)
		r.Options = options
		return r
	}
	decline := `[{"id":1,"decline":true}]`
	for _, c := range []struct {
		name    string
		row     engine.Row
		search  match.OwnSearch
		claimed bool   // the matcher's abstain names the skill gate
		reply   string // rung 2
		want    string // the value written, "" none
		from    string // its provenance
	}{
		{"nothing mentions it: the option meaning No", row("Do you have a public track record of open-source contributions?", "radio-group", "YES", "NO"), absent, false, decline, "NO", ""},
		{"nothing mentions it: No in a text box", row("Are you a graduate of the RiseUp Program?", "text"), absent, false, decline, "No", ""},
		// The claimed row never takes the absent No; declined, it takes the
		// owner's default for an experience nothing settles: Yes.
		{"an entry claims it", row("Do you have experience in cybersecurity SaaS?", "radio-group", "Yes", "No"), absent, true, decline, "Yes", ""},
		{"its options hold no single No", row("Have you participated in a hackathon?", "radio-group", "Yes", "No", "No, but I would like to"), absent, false, decline, "", ""},
		{"a mention rung 2 answers from the section it cites", row("Do you have experience working in the sports industry?", "text"), found, false, `[{"id":1,"answer":"Yes","cite":["kb:` + club + `"]}]`, "Yes", ProvenanceKnowledge + club},
		{"a mention rung 2 declines", row("Do you have experience working in the sports industry?", "text"), found, false, decline, "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := newLadderEnv(t, ladderOwn, script(c.reply, `[{"id":1,"p":0.9}]`, "[]"), func(d *Deps) {
				d.Page.OwnSearch = func(_ context.Context, req engine.MatchRequest) ([]match.OwnSearch, error) {
					return slices.Repeat([]match.OwnSearch{c.search}, len(req.Questions)), nil
				}
				d.Page.Knowledge = func(context.Context, string) ([]match.KnowledgeSection, error) { return shown, nil }
			})
			if c.claimed {
				l.g.r.d.Page.Matches.Observe(bind.Request{RunID: "er1"}, bind.Outcome{
					Results:  []match.Result{{Selector: "#q", Outcome: match.OutcomeAbstained, Reason: match.ReasonNoEntry, EntryID: "skill-experience-knowledge-gate"}},
					Prepared: []match.Question{{Selector: "#q", Text: c.row.Label, Kind: match.KindRadioGroup, Options: []match.Option{{Value: "Yes", Label: "Yes"}, {Value: "No", Label: "No"}}}},
				})
			}
			ps := l.run(c.row)
			v, ok := answeredValue(l.senv, "#q")
			if c.want == "" && ok || c.want != "" && (v != c.want || c.from != "" && ps.answered["#q"] != c.from) {
				t.Fatalf("answered %q %v, provenance %q", v, ok, ps.answered["#q"])
			}
		})
	}
}

// A row is shown at most 3 of its knowledge sections, each cut to 600
// characters; a row the policies keep from a model is not searched, and a
// choice is searched with its question and its options other than Yes or No
// (F-10).
func TestApply_Ladder_KnowledgeShownForARow(t *testing.T) {
	var queries []string
	long := strings.Repeat("Ran the box office systems. ", 40)
	l := newLadderEnv(t, ladderOwn, nil, func(d *Deps) {
		d.Page.Knowledge = func(_ context.Context, q string) ([]match.KnowledgeSection, error) {
			queries = append(queries, q)
			return []match.KnowledgeSection{{Ref: "a", Text: long}, {Ref: "b", Text: "B."}, {Ref: "c", Text: "C."}, {Ref: "d", Text: "D."}}, nil
		}
	})
	r := &ladderRow{label: "Do you have experience working in the sports industry?", kind: "text"}
	l.s.readKnowledge(context.Background(), r)
	if len(r.knowledge) != maxRowKnowledge || len([]rune(r.knowledge[0].Text)) > maxKnowledgeText {
		t.Fatalf("shown %d sections, the first of %d characters", len(r.knowledge), len([]rune(r.knowledge[0].Text)))
	}
	queries = nil
	l.s.readKnowledge(context.Background(), &ladderRow{label: "Have you ever been terminated for cause?", kind: "radio-group", options: []string{"Yes", "No"}})
	l.s.readKnowledge(context.Background(), &ladderRow{label: "Which of these industries have you worked in?", kind: "select", options: []string{"Sports", "Retail", "Yes"}})
	if len(queries) != 1 || !contains2(queries[0], "Sports", "Retail") || strings.Contains(queries[0], "Yes") {
		t.Fatalf("queries %q", queries)
	}
}

// Samsung (Workday, live): the disability box the ladder ticked reads empty at
// the first look and holds the option a moment later; it is kept, not a
// write-not-committed stop.
func TestApply_Ladder_AWriteTheFirstReadMissesGetsOneMoreLook(t *testing.T) {
	l := newLadderEnv(t, ladderOwn, script("[]", "[]", "[]"), nil)
	no := "No, I do not have a disability and have not had one in the past"
	for _, c := range []struct {
		name   string
		reads  []string
		filled bool
	}{
		{"committed a moment later", []string{"", no}, true},
		{"never committed", []string{"", ""}, false},
	} {
		reads := 0
		l.page.rows = func() []engine.InspectRow {
			v := c.reads[min(reads, len(c.reads)-1)]
			reads++
			return []engine.InspectRow{{Selector: "#d", Label: "Please check one of the boxes below:", Kind: "checkbox", Value: v}}
		}
		ps := &pageState{rows: []engine.Row{{Selector: "#d", Label: "Please check one of the boxes below:", Kind: "checkbox", Required: true, Outcome: "skipped"}}}
		l.s.writeAnswers(context.Background(), "er1", ps, []answerWrite{{idx: 0, value: no, option: no, rung: "rung4"}})
		if got := ps.rows[0].Outcome == "filled" && len(ps.notHeld) == 0; got != c.filled {
			t.Errorf("%s: row %+v not held %+v, want filled=%t", c.name, ps.rows[0], ps.notHeld, c.filled)
		}
	}
}

// A later write of a row replaces the record of an earlier one: a value the
// page now holds is no longer one it lacks. A rung 2 answer the computer-use
// rung writes keeps as provenance the entry or the section it cited (F-9,
// F-10).
func TestApply_Ladder_ALaterWriteKeepsWhereTheAnswerCameFrom(t *testing.T) {
	l := newLadderEnv(t, ladderOwn, script("[]", "[]", "[]"), nil)
	held := ""
	l.page.rows = func() []engine.InspectRow {
		return []engine.InspectRow{{Selector: "#q", Label: "City", Kind: "text", Value: held}}
	}
	ps := &pageState{rows: []engine.Row{{Selector: "#q", Label: "City", Kind: "text", Required: true, Outcome: "skipped"}}}
	l.s.writeAnswers(context.Background(), "er1", ps, []answerWrite{{idx: 0, value: "Durham", rung: "rung1"}})
	held = "Durham"
	l.s.writeAnswers(context.Background(), "er1", ps, []answerWrite{{idx: 0, value: "Durham", rung: "fact"}})
	if ps.rows[0].Outcome != "filled" || len(ps.notHeld) != 0 {
		t.Fatalf("row %+v, not held %+v", ps.rows[0], ps.notHeld)
	}
	for _, w := range []answerWrite{{entry: "job-source"}, {knowledge: "knowledge/a.md#Detail"}} {
		ps := &pageState{rows: []engine.Row{{Selector: "#q", Label: "How did you hear about us?", Kind: "custom-listbox", Required: true, Outcome: "abstained"}}}
		w.value, w.option, w.rung = "Referral", "Referral", "rung2"
		ps.unwrite(w, "write-failed:option-not-found")
		ps.cuWrote(l.s.cuWrites("er1", ps)[0])
		if got := ps.answered["#q"]; got != w.provenance() || got == ProvenanceCheck {
			t.Fatalf("provenance %q, want %q", got, w.provenance())
		}
	}
}

// popupTab is an inputLog whose open list shows its option only inside the
// popup it names once open.
type popupTab struct{ inputLog }

func (t *popupTab) Eval(ctx context.Context, expr string, out any) error {
	if strings.Contains(expr, `("cuOption",`) {
		return setJSON(out, map[string]any{"Found": strings.Contains(expr, `"popup":["hear-list"]`), "I": 7, "Count": 1})
	}
	return t.inputLog.Eval(ctx, expr, out)
}

// A list that names its popup only once it is open has its option looked for
// in that popup, never by the ids read before the press.
func TestApply_Ladder_ComputerUseLooksInThePopupTheListNamesOnceOpen(t *testing.T) {
	l := newLadderEnv(t, ladderOwn, script("[]", "[]", "[]"), nil)
	l.page.rows = func() []engine.InspectRow { return []engine.InspectRow{{Selector: "#hear", Value: "Referral"}} }
	l.s.tab = &popupTab{inputLog{shown: cuShown{Popup: []string{"hear-list"}}}}
	r := engine.Row{Selector: "#hear", Label: "How did you hear about us?", Kind: "custom-listbox", Required: true}
	c := &cuRun{s: l.s, holder: holdingHolder{}, docs: []cuDoc{{}}}
	w := cuWrite{value: "Referral", option: "Referral", rung: "rung1", failure: "write-failed:option-not-found"}
	if step := c.write(context.Background(), []engine.Row{r}, r, r.Selector, w, []string{"Referral"}, ""); step != "" {
		t.Fatalf("step %q", step)
	}
}

// Rung 1 never sends the matcher a row of a kind it does not take: a file
// upload in the batch rejected the whole request, so the text question beside
// Greenhouse's cover-letter "Attach" was left as bank-unavailable.
func TestApply_Ladder_Rung1SendsNoUploadToTheMatcher(t *testing.T) {
	m := &fakeMatcher{reply: func(req engine.MatchRequest) engine.MatchResponse {
		out := engine.MatchResponse{}
		for _, q := range req.Questions {
			if !match.ControlKind(q.Kind).Valid() {
				return engine.MatchResponse{}
			}
			out.Results = append(out.Results, engine.Resolution{Selector: q.Selector, Outcome: "filled", Value: "Alex"})
		}
		return out
	}}
	l := newLadderEnv(t, ladderOwn, script("[]", "[]", "[]"), func(d *Deps) { d.Page.Matcher = m })
	l.run(engine.Row{Selector: "#cover_letter", Label: "Attach", Kind: "file", Required: true, Outcome: "skipped", Reason: "no-lane-claimed"},
		engine.Row{Selector: "#pref", Label: "Preferred first name", Kind: "text", Required: true, Outcome: "skipped", Reason: "no-lane-claimed"})
	if len(m.reqs) != 1 || len(m.reqs[0].Questions) != 1 || m.reqs[0].Questions[0].Selector != "#pref" {
		t.Fatalf("requests %+v", m.reqs)
	}
	if v, ok := answeredValue(l.senv, "#pref"); !ok || v != "Alex" {
		t.Fatalf("answers %+v", l.page.answers)
	}
}

// A write that ran past its deadline answers its row when the page holds its
// value by the time the computer-use rung reads the page, with no trusted
// input (Visa's Workday date box); an empty box, or a write the engine saw
// fail to hold, is left to the rung.
func TestApply_Ladder_AWriteThatLandedLateAnswersItsRow(t *testing.T) {
	for _, c := range []struct {
		name, failure, now string
		answered           bool
	}{
		{"the page holds the date", "write-failed:timeout", "10/31/2026", true},
		{"the box is still empty", "write-failed:timeout", "", false},
		{"a write the page did not keep", "write-failed:write-not-committed", "10/31/2026", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := newLadderEnv(t, ladderOwn, script("[]", "[]", "[]"), nil)
			l.page.rows = func() []engine.InspectRow {
				return []engine.InspectRow{{Selector: "#d", Label: "Date", Kind: "text", Value: c.now}}
			}
			l.s.tab = &inputLog{}
			ps := &pageState{rows: []engine.Row{{Selector: "#d", Label: "Date", Kind: "text", Required: true, Outcome: "skipped"}}}
			ps.unwrite(answerWrite{idx: 0, value: "10/31/2026", rung: "rung1"}, c.failure)
			ps.setWhy("#d", c.failure)
			l.s.computerUse(context.Background(), &engine.PageResult{RunID: "er1"}, ps)
			r := ps.rows[0]
			if got := r.Outcome == "filled" && !open(r) && ps.why["#d"] == ""; got != c.answered || ps.report.Ladder.ComputerUse != 0 || len(l.s.tab.(*inputLog).input) != 0 {
				t.Fatalf("row %+v, why %q, ladder %+v, input %v", r, ps.why["#d"], ps.report.Ladder, l.s.tab.(*inputLog).input)
			}
		})
	}
}

// A free-text row the engine skipped because its composed draft waits for
// review is not asked of the bank again (the matcher would hand back the same
// draft, read as a decline); rung 3 composes it.
func TestApply_Ladder_AHeldDraftIsComposedByRung3(t *testing.T) {
	m := &fakeMatcher{reply: func(req engine.MatchRequest) engine.MatchResponse {
		out := engine.MatchResponse{}
		for range req.Questions {
			out.Results = append(out.Results, engine.Resolution{Outcome: "filled-model", Value: "A draft"})
		}
		return out
	}}
	l := newLadderEnv(t, ladderOwn, script(`[{"id":1,"decline":true}]`, "[]", `[{"id":1,"answer":"The model capabilities work matches what I build."}]`), func(d *Deps) { d.Page.Matcher = m })
	l.run(engine.Row{Selector: "#x", Label: "What excited you about this role?", Required: true, Outcome: "skipped", Reason: "awaiting-review", Kind: "textarea"})
	if len(m.reqs) != 0 {
		t.Fatalf("%d bank requests for a held draft", len(m.reqs))
	}
	if v, ok := answeredValue(l.senv, "#x"); !ok || v != "The model capabilities work matches what I build." {
		t.Fatalf("answers %+v", l.page.answers)
	}
}

// Metabase (Lever): "What is the application code found in the browser console
// at https://metabase.com/jobs (to help us filter out robots)" is answered with
// the code that page prints to its console, read in a tab of its own (owner
// decision). A page off the employer's site is never opened, and a console that
// prints no single code leaves the question.
func TestApply_Ladder_ConsoleCodeFromTheEmployersPage(t *testing.T) {
	const label = "What is the application code found in the browser console at https://metabase.com/jobs (to help us filter out robots)"
	if name, page, ok := ConsoleCodeAsk(label); !ok || name != "application code" || page != "https://metabase.com/jobs" {
		t.Fatalf("ask = %q %q %v", name, page, ok)
	}
	if _, _, ok := ConsoleCodeAsk("What is your access code?"); ok {
		t.Fatal("a code with no console page was read as a console code")
	}
	for _, c := range []struct {
		lines []string
		want  string
	}{
		{[]string{"Failed to load resource", "application code 🌳🍀🌳🌱"}, "🌳🍀🌳🌱"},
		{[]string{"Application Code: AB12"}, "AB12"},
		{[]string{"application code A", "application code B"}, ""},
		{[]string{"welcome"}, ""},
	} {
		if got, _ := CodeFromConsole(c.lines, "application code"); got != c.want {
			t.Errorf("%q: got %q, want %q", c.lines, got, c.want)
		}
	}
	if !EmployerSite("https://metabase.com/jobs", "", "metabase") || !EmployerSite("https://www.metabase.com/jobs", "Metabase", "") ||
		EmployerSite("https://robots.example/jobs", "Metabase", "metabase") || EmployerSite("file:///metabase", "Metabase", "metabase") {
		t.Fatal("employer site")
	}

	l := newLadderEnv(t, ladderOwn, script("[]", "[]", "[]"), nil)
	l.e.Run.Tenant = "metabase"
	held := map[string]string{}
	l.page.rows = func() []engine.InspectRow {
		return []engine.InspectRow{{Selector: "#c", Label: label, Kind: "text", Value: held["#c"]}, {Selector: "#x", Kind: "text", Value: held["#x"]}}
	}
	l.tab.console = map[string][]string{"https://metabase.com/jobs": {"application code 🌳🍀🌳🌱"}}
	ps := &pageState{rows: []engine.Row{
		abstained("#c", label, "text"),
		abstained("#x", "What is the code printed in the browser console at https://robots.example/jobs?", "text"),
	}}
	held["#c"] = "🌳🍀🌳🌱"
	l.s.consoleCodes(context.Background(), doneResult(ps.rows...), ps)
	if v, ok := answeredValue(l.senv, "#c"); !ok || v != "🌳🍀🌳🌱" || ps.rows[0].Outcome != "filled" {
		t.Fatalf("the code: %q %v, row %+v", v, ok, ps.rows[0])
	}
	if _, ok := answeredValue(l.senv, "#x"); ok || slices.Contains(l.tab.consoleReads, "https://robots.example/jobs") {
		t.Fatalf("a page off the employer's site was read: %q", l.tab.consoleReads)
	}
}

// The owner's default for an experience nothing settles: a question about the
// applicant's own experience that the model declines takes Yes over a plain
// Yes and No (Modern Health: "Have you independently authored technical plans
// ...?"), and a list's least-experience option. A cited answer still stands,
// and a declined question about anything else stays the user's.
func TestApply_Ladder_DeclinedExperienceTakesTheOwnersDefault(t *testing.T) {
	row := func(label string, options []string) engine.Row {
		if options == nil {
			options = []string{"Yes", "No"}
		}
		return engine.Row{Selector: "#q", Label: label, Required: true, Outcome: "skipped", Reason: "no-lane-claimed", Kind: "radio-group", Options: options}
	}
	const plans = "Have you independently authored technical plans for ambiguous initiatives spanning multiple domains or engineering teams, and driven those plans through execution?"
	for _, c := range []struct {
		name, label, reply, support, want string
		options                           []string
	}{
		{name: "declined experience", label: plans, reply: `[{"id":1,"decline":true}]`, want: "Yes"},
		// Tebra: a responsibility held.
		{name: "declined responsibility", label: "Have you been responsible for the end-to-end production operations of data systems, including setting up CI/CD, monitoring, and automated alerting/recovery?", reply: `[{"id":1,"decline":true}]`, want: "Yes"},
		{name: "a cited No", label: plans, reply: `[{"id":1,"answer":"No","cite":["fact:skills"]}]`, want: "No"},
		{name: "declined, not experience", label: "Would you like to join our talent community?", reply: `[{"id":1,"decline":true}]`},
		// Brain Co. (Ashby, owner workspace): an answer the support check
		// refuses settles nothing either, so the list takes its least option.
		{name: "an unsupported answer", label: "How many YOE do you have hiring technical talent (e.g. software engineers, AI/ML, or platform roles)?", options: []string{"0-2", "2-5", "5+"},
			reply: `[{"id":1,"answer":"2-5","cite":["fact:skills"]}]`, support: `[{"id":1,"p":0.2}]`, want: "0-2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			support := c.support
			if support == "" {
				support = `[{"id":1,"p":0.9}]`
			}
			l := newLadderEnv(t, ladderOwn, script(c.reply, support, "[]"), nil)
			l.run(row(c.label, c.options))
			if v, ok := answeredValue(l.senv, "#q"); c.want == "" && ok || c.want != "" && (!ok || v != c.want) {
				t.Fatalf("answered %q (%v), want %q; answers %+v", v, ok, c.want, l.page.answers)
			}
		})
	}
}
