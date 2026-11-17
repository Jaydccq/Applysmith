package bind

import (
	"context"
	"testing"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/engine"
	"applysmith/internal/workspace"
)

func engineRequest(selector, text string) engine.MatchRequest {
	return engine.MatchRequest{RunID: "run", Host: "jobs.example.com",
		Questions: []engine.WireQuestion{{Selector: selector, Text: text, Kind: "text", InputType: "text", Options: []string{}}}}
}

func TestBind_Engine_WireMapping(t *testing.T) {
	f := newFixture(t, alexSponsored)
	b := f.binding(nil)
	req := engine.MatchRequest{RunID: "run", Host: "jobs.example.com", Questions: []engine.WireQuestion{
		{Selector: "#n", Text: "Full name", Kind: "text", InputType: "text", Options: []string{}},
		{Selector: "#s", Text: "Will you now require visa sponsorship to work for us?", Kind: "radio-group", InputType: "yes_no", Options: []string{"Yes", "No"}, Required: true},
		{Selector: "#why", Text: "Why do you want to work here?", Kind: "text", InputType: "long_text", Options: []string{}, MaxLength: 500},
		{Selector: "#mail", Text: "Email address", Kind: "text", InputType: "email", Options: []string{}},
		{Selector: "#phone", Text: "Phone", Kind: "text", InputType: "tel", Options: []string{}},
	}}
	resp, err := b.Match(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != len(req.Questions) {
		t.Fatalf("%d results for %d questions", len(resp.Results), len(req.Questions))
	}
	for i, r := range resp.Results {
		if r.Selector != req.Questions[i].Selector {
			t.Fatalf("result %d answers %q", i, r.Selector)
		}
	}
	name, sponsor, why := resp.Results[0], resp.Results[1], resp.Results[2]
	if name.Outcome != "filled" || name.Value != "Alex Example" || name.Reason != "" || name.ClientMayOverwrite {
		t.Fatalf("name %+v", name)
	}
	if sponsor.Outcome == "abstained" || sponsor.OptionLabel != "No" {
		t.Fatalf("sponsorship %+v", sponsor)
	}
	if mail := resp.Results[3]; mail.Outcome != "filled" || mail.Value != "alex@example.com" {
		t.Fatalf("an email-typed control must read as text, not as an address: %+v", mail)
	}
	if why.Outcome != "abstained" || why.Reason != string(match.ReasonNarrativeDeferred) || why.Reviewed {
		t.Fatalf("narrative %+v", why)
	}
}

func TestBind_Engine_ResumeAndCoverLetterObservations(t *testing.T) {
	f := newFixture(t, alexProfile)
	b := f.binding(nil)
	q := engine.WireQuestion{Selector: "#cl", Text: "Did you submit a cover letter with this application?", Kind: "radio-group", InputType: "yes_no", Options: []string{"Yes", "No"}}
	for _, c := range []struct {
		cover, resume bool
		want          string
	}{{true, false, "Yes"}, {false, false, ""}} {
		resp, err := b.Match(context.Background(), engine.MatchRequest{RunID: "r", Host: "jobs.example.com", CoverLetterAttached: c.cover, ResumeAttached: c.resume, Questions: []engine.WireQuestion{q}})
		if err != nil {
			t.Fatal(err)
		}
		if got := resp.Results[0].OptionLabel; got != c.want {
			t.Fatalf("cover=%v: option %q, want %q", c.cover, got, c.want)
		}
	}
}

func TestBind_Engine_TooManyQuestionsFailsTheCall(t *testing.T) {
	f := newFixture(t, alexProfile)
	req := engine.MatchRequest{RunID: "r", Host: "h"}
	for range match.MaxQuestions + 1 {
		req.Questions = append(req.Questions, engine.WireQuestion{Selector: "#q", Text: "Full name", Kind: "text", InputType: "text"})
	}
	if _, err := f.binding(nil).Match(context.Background(), req); err == nil {
		t.Fatal("expected an error")
	}
}

func TestBind_Engine_InputTypesAndOptionsReadFromTheWire(t *testing.T) {
	cases := []struct {
		wire string
		want match.InputType
	}{
		{"text", match.InputText}, {"long_text", match.InputLongText}, {"yes_no", match.InputYesNo}, {"multi_select", match.InputMultiSelect}, {"single_select", ""},
		{"email", match.InputText}, {"tel", match.InputText}, {"unheard-of", ""}, {"", ""},
	}
	for _, c := range cases {
		t.Run(c.wire, func(t *testing.T) {
			q := questionFromWire(engine.WireQuestion{Selector: "#a", Text: "t", Kind: "select", InputType: c.wire, Options: []string{"A", "B"}, Required: true, MaxLength: 9})
			if q.InputType != c.want || q.Kind != match.KindSelect || !q.Required || q.MaxLength != 9 {
				t.Fatalf("%+v", q)
			}
			if len(q.Options) != 2 || q.Options[1] != (match.Option{Value: "B", Label: "B"}) {
				t.Fatalf("options %+v", q.Options)
			}
		})
	}
}

// The page capture builds the posting text in the engine; the binding passes
// it on unchanged as the posting description.
func TestBind_Engine_PostingTextBecomesTheDescription(t *testing.T) {
	f := newFixture(t, alexProfile)
	var seen Request
	b := f.binding(func(d *Deps) { d.Observe = func(req Request, _ Outcome) { seen = req } })
	req := engineRequest("#n", "Full name")
	req.Posting = engine.PostingContext{Company: "Example Corp", Title: "Engineer", JDText: "You will own the Go services.", Location: "Durham, NC", URL: "https://jobs.example.com/1"}
	if _, err := b.Match(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	want := Posting{Company: "Example Corp", Title: "Engineer", Description: "You will own the Go services.", Location: "Durham, NC", URL: "https://jobs.example.com/1"}
	if seen.Posting != want {
		t.Fatalf("posting %+v, want %+v", seen.Posting, want)
	}
}

// The engine's consent rules let the user's own bank answer apply as written;
// the binding marks it from the result's provenance, never from the wording.
func TestBind_Engine_OwnAnswerMarksOnlyTheUsersOwnEntry(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, alexProfile)
	loaded, err := f.bank.File()
	if err != nil {
		t.Fatal(err)
	}
	b := loaded.Bank
	b.Entries = append(b.Entries, match.Entry{ID: "data-retention-consent", Description: "Data retention consent", Kind: match.KindFixed,
		Body: match.Body{Answer: "Yes"}, Meta: match.Meta{Category: match.CategoryOther,
			Examples: []string{"Do you consent to Example Corp retaining your data for 24 months?"}}})
	if _, err := f.bank.Save(b, loaded.Revision); err != nil {
		t.Fatal(err)
	}
	on := true
	if _, err := f.bank.PatchSettings(ctx, answerbankPatch(&on)); err != nil {
		t.Fatal(err)
	}
	yesNo := []string{"Yes", "No"}
	req := engine.MatchRequest{RunID: "run", Host: "jobs.example.com", Questions: []engine.WireQuestion{
		{Selector: "#keep", Text: "Do you consent to Example Corp retaining your data for 24 months?", Kind: "radio-group", InputType: "yes_no", Options: yesNo},
		{Selector: "#certify", Text: "", Kind: "checkbox", InputType: "", Options: []string{"I certify that the information I have provided is true and complete"}, Required: true},
		{Selector: "#why", Text: "Why do you want to work here?", Kind: "text", InputType: "long_text", Options: []string{}},
	}}
	resp, err := f.binding(nil).Match(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	keep, certify, why := resp.Results[0], resp.Results[1], resp.Results[2]
	if keep.Outcome != "filled" || keep.OptionLabel != "Yes" || !keep.OwnAnswer {
		t.Fatalf("the user's own entry: %+v", keep)
	}
	if certify.Outcome == "abstained" || certify.OwnAnswer {
		t.Fatalf("an acknowledgement under the setting is not the user's own answer: %+v", certify)
	}
	if why.OwnAnswer {
		t.Fatalf("an abstain carries no answer: %+v", why)
	}
}

// A question only a newer default answers is answered at the next match, with
// no update of the bank first (flow round 11, D-1): a file an older release
// seeded holds employer-history as a blank placeholder, and the answer the
// default ships now answers from the profile's employers, never as the user's
// own.
func TestBind_Engine_ANewerDefaultAnswersWithoutAnUpdate(t *testing.T) {
	f := newFixture(t, alexProfile)
	def, err := answerbank.DefaultBank()
	if err != nil {
		t.Fatal(err)
	}
	for i := range def.Bank.Entries {
		if e := &def.Bank.Entries[i]; e.ID == "employer-history" {
			e.AnswerIfMatch, e.AnswerIfNoMatch, e.Placeholder = "", "", true
		}
	}
	data, err := answerbank.Encode(def.Bank)
	if err != nil {
		t.Fatal(err)
	}
	f.write(t, workspace.FileAnswerBank, string(data))
	req := engine.MatchRequest{RunID: "run", Host: "jobs.example.com", Questions: []engine.WireQuestion{
		{Selector: "#former", Text: "Are you a former Northwind Example employee?", Kind: "radio-group", InputType: "yes_no", Options: []string{"Yes", "No"}}}}
	resp, err := f.binding(nil).Match(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if r := resp.Results[0]; r.Outcome != "filled" || r.OptionLabel != "No" || r.OwnAnswer {
		t.Fatalf("%+v", r)
	}
}

// The engine reads a radio group as yes/no only when its two labels are
// exactly Yes and No, so a board that spells the answers as sentences ("Yes, I
// need visa sponsorship") arrives as single_select. The matcher reads those
// labels as yes/no itself, so the wire's narrower reading must not turn the
// same question into an input mismatch against a yes_no bank entry.
func TestBind_Engine_SentenceYesNoRadiosAreYesNoQuestions(t *testing.T) {
	f := newFixture(t, alexSponsored)
	req := engine.MatchRequest{RunID: "run", Host: "jobs.lever.co",
		Posting: engine.PostingContext{Company: "Example", Title: "Engineer", Location: "Remote, United States"},
		Questions: []engine.WireQuestion{
			{Selector: `input[type="radio"][name="cards[x][field3]"]`, Text: "Work Authorization", Kind: "radio-group", InputType: "single_select", Required: true,
				Options: []string{"Yes, I am authorized to work in the United States", "No, I am not authorized to work in the United States"}},
			{Selector: `input[type="radio"][name="cards[x][field4]"]`, Text: "Visa Sponsorship", Kind: "radio-group", InputType: "single_select", Required: true,
				Options: []string{"Yes, I need visa sponsorship", "No, I do not need visa sponsorship"}},
		}}
	resp, err := f.binding(nil).Match(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"Yes, I am authorized to work in the United States", "No, I do not need visa sponsorship"} {
		if r := resp.Results[i]; r.Outcome == "abstained" || r.OptionLabel != want {
			t.Errorf("%s: %+v, want %q", req.Questions[i].Text, r, want)
		}
	}
}
