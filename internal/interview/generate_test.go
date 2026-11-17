package interview

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestInterview_GoldenDeterministicPack(t *testing.T) {
	f := newFixture(t, fixtureOpts{stories: true})
	in := f.input()
	f.stage("onsite-1", TypeBehavioral, true)
	p, err := f.svc.Build(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	p.Candidacy = 1
	golden(t, "pack_deterministic.golden.json", p)
}

func TestInterview_StoryLinking(t *testing.T) {
	f := newFixture(t, fixtureOpts{stories: true})
	in := f.input()
	f.stage("onsite-1", TypeBehavioral, true)
	p, err := f.svc.Build(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	var own, mentor *Question
	for i, q := range p.Questions {
		switch {
		case strings.Contains(q.Prompt, "Own a backend service"):
			own = &p.Questions[i]
		case strings.Contains(q.Prompt, "mentor engineers"):
			mentor = &p.Questions[i]
		}
	}
	if own == nil || len(own.StoryIDs) == 0 || own.StoryIDs[0] != "alerts-ownership" {
		t.Errorf("ownership question = %+v", own)
	}
	if mentor == nil || len(mentor.StoryIDs) == 0 || mentor.StoryIDs[0] != "review-mentoring" {
		t.Errorf("mentoring question = %+v", mentor)
	}
	// The uncovered requirement is a gap and an action that lead to the story
	// bank for that requirement.
	gap, act := step(p.Gaps, `"Experience with PostgreSQL`), step(p.NextActions, "Mine a story")
	if gap == nil || !strings.Contains(gap.Text, "Mine a story for it") || act == nil {
		t.Fatalf("gap lacks the mining suggestion: gaps %v actions %v", p.Gaps, p.NextActions)
	}
	for _, s := range []*Step{gap, act} {
		if s.Kind != StepStory || !strings.HasPrefix(s.Target, "Experience with PostgreSQL") {
			t.Errorf("story step = %+v", s)
		}
	}
	if err := Validate(p); err != nil {
		t.Fatal(err)
	}
}

func TestInterview_QuestionSet_PerStageType(t *testing.T) {
	cases := []struct {
		typ  string
		want []string
	}{
		{TypeBehavioral, []string{"story", "motivation", "trade-off"}},
		{TypeFinal, []string{"story", "motivation", "trade-off"}},
		{TypeSystemDesign, []string{"architecture", "trade-off", "failure mode"}},
		{TypeTechnical, []string{"depth", "debugging", "collaboration"}},
		{TypePhoneScreen, []string{"role fit", "evidence"}},
		{TypeOnsite, []string{"role fit", "evidence"}},
		{TypeCase, []string{"role fit", "evidence"}},
		{TypeOther, []string{"role fit", "evidence"}},
	}
	f := newFixture(t, fixtureOpts{})
	for _, c := range cases {
		t.Run(c.typ, func(t *testing.T) {
			in := f.input()
			in.Stage.Type = c.typ
			p, err := f.svc.Build(f.ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, q := range p.Questions {
				if q.Category != "requirement" {
					got = append(got, q.Category)
				}
			}
			if fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Errorf("base questions = %v, want %v", got, c.want)
			}
			if n := len(p.Questions) - len(got); n != 3 {
				t.Errorf("%d requirement questions, want one per requirement", n)
			}
		})
	}
}

func TestInterview_GapsForMissingSources(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	in := f.input()
	in.Stage.UserConfirmed = false
	in.Posting, in.Resume, in.Cover, in.Facts, in.Requirements = Doc{}, Doc{}, Doc{}, nil, nil
	p, err := f.svc.Build(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	// Each gap names the place that fills it: the posting is evaluated, the
	// résumé made, the brief built for this company, the stage confirmed.
	for _, want := range []Step{{Text: "No job description", Kind: StepEvaluate}, {Text: "No submitted résumé", Kind: StepDocuments},
		{Text: "No company brief exists, so company-specific hooks were omitted", Kind: StepBrief, Target: in.Company},
		{Text: "is not confirmed by you", Kind: StepConfirm, Target: in.Stage.ID}} {
		if got := step(p.Gaps, want.Text); got == nil || got.Kind != want.Kind || got.Target != want.Target {
			t.Errorf("gap %q = %+v, want kind %q target %q", want.Text, got, want.Kind, want.Target)
		}
	}
	for _, want := range []Step{{Text: "Confirm the details", Kind: StepConfirm, Target: in.Stage.ID}, {Text: "Evaluate the posting", Kind: StepEvaluate},
		{Text: "company brief", Kind: StepBrief, Target: in.Company}, {Text: "Practice", Kind: StepPractice, Target: in.Stage.ID}, {Text: "record how it went", Kind: StepDebrief, Target: in.Stage.ID}} {
		if got := step(p.NextActions, want.Text); got == nil || got.Kind != want.Kind || got.Target != want.Target {
			t.Errorf("action %q = %+v, want kind %q target %q", want.Text, got, want.Kind, want.Target)
		}
	}
	if len(p.Hooks) != 0 || len(p.Claims) != 0 || p.RoleBrief != "" || p.CompanyBrief != "" {
		t.Errorf("a pack without sources invented content: %+v", p)
	}
}

func TestInterview_FrozenSources_StatesCarryToSourceRefs(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	in := f.input()
	in.Resume.State = StateCurrent
	p, err := f.svc.Build(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, s := range p.Sources {
		states[s.Ref] = s.State
	}
	if states[in.Resume.Ref] != StateCurrent || states[in.Posting.Ref] != StateFrozen {
		t.Errorf("source states = %v", states)
	}
	if len(p.Claims) == 0 || !strings.Contains(p.Claims[0], "Led the platform migration") {
		t.Errorf("claims = %v", p.Claims)
	}
}

func TestInterview_SourceRefKinds(t *testing.T) {
	f := newFixture(t, fixtureOpts{stories: true})
	in := f.input()
	st := f.stage("onsite-1", TypeBehavioral, true)
	in.Stage = st
	if _, err := f.svc.RecordFeedback(f.ctx, f.cand, FeedbackInput{Stage: st.ID, From: FromEmail, Text: "They asked about ownership."}); err != nil {
		t.Fatal(err)
	}
	in.PostingKind = KindReport
	p, err := f.svc.Build(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, s := range p.Sources {
		kinds[s.Kind] = true
	}
	for _, k := range []string{KindDossier, KindReport, KindCompanyBrief, KindStory, KindFeedback} {
		if !kinds[k] {
			t.Errorf("no %s source in %+v", k, p.Sources)
		}
	}
}

func TestInterview_FeedbackReachesNextPack(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	st := f.stage("onsite-1", TypeBehavioral, true)
	in := f.input()
	in.Stage = st
	first, err := f.svc.Build(f.ctx, in)
	if err != nil || len(first.PriorFeedback) != 0 {
		t.Fatalf("first pack = %+v, %v", first, err)
	}
	if _, err := f.svc.RecordFeedback(f.ctx, f.cand, FeedbackInput{Stage: st.ID, From: FromUser, Text: "Be shorter on the migration story."}); err != nil {
		t.Fatal(err)
	}
	next, err := f.svc.Build(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.PriorFeedback) != 1 || !strings.Contains(next.PriorFeedback[0], "Be shorter") {
		t.Errorf("prior feedback = %v", next.PriorFeedback)
	}
	f.stage("other-1", TypeTechnical, true)
	if _, err := f.svc.RecordFeedback(f.ctx, f.cand, FeedbackInput{Stage: "other-1", Text: "Different stage."}); err != nil {
		t.Fatal(err)
	}
	// A debrief of another round reaches this stage's pack after its own
	// feedback, named by its stage.
	again, _ := f.svc.Build(f.ctx, in)
	if len(again.PriorFeedback) != 2 || again.PriorFeedback[1] != "Stage other-1, user: Different stage." {
		t.Errorf("prior feedback = %v", again.PriorFeedback)
	}
}

func TestInterview_PackBounds(t *testing.T) {
	f := newFixture(t, fixtureOpts{stories: true})
	st := f.stage("onsite-1", TypeBehavioral, true)
	long := strings.Repeat("word ", 400)
	in := f.input()
	in.Stage = st
	in.Posting.Text = long
	in.Role, in.Company = long, long
	in.Requirements = []string{long, long, long, long, long, long, long, "extra"}
	in.Resume.Text = ""
	for i := 0; i < 30; i++ {
		in.Resume.Text += fmt.Sprintf("- line number %d states something long enough to count as a claim %s\n", i, long)
	}
	in.Facts = nil
	for i := 0; i < 20; i++ {
		in.Facts = append(in.Facts, Fact{Text: long, Source: fmt.Sprintf("knowledge/companies/c%d.md", i)})
	}
	for i := 0; i < 15; i++ {
		if _, err := f.svc.RecordFeedback(f.ctx, f.cand, FeedbackInput{Stage: st.ID, From: FromUser, Text: strings.Repeat("feedback ", 200)}); err != nil {
			t.Fatal(err)
		}
	}
	p, err := f.svc.Build(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(p); err != nil {
		t.Fatalf("a built pack breaks its own bounds: %v", err)
	}
	if len(p.Sources) != MaxSources || len(p.Claims) != MaxClaims || len(p.PriorFeedback) != MaxFeedbackItems || len(p.Hooks) > 4 {
		t.Errorf("sources %d claims %d feedback %d hooks %d", len(p.Sources), len(p.Claims), len(p.PriorFeedback), len(p.Hooks))
	}
	if utf8.RuneCountInString(p.RoleBrief) > MaxBriefChars || utf8.RuneCountInString(p.CompanyBrief) > MaxBriefChars {
		t.Error("a brief is over 1,200 characters")
	}
	over := p
	over.Questions = make([]Question, MaxQuestions+1)
	for i := range over.Questions {
		over.Questions[i] = Question{Category: "x", Prompt: "y", StoryIDs: []string{}, Sources: []string{}}
	}
	if err := Validate(over); !errors.Is(err, ErrInvalid) {
		t.Errorf("21 questions validated: %v", err)
	}
	unsourced := p
	unsourced.Hooks = []Hook{{Text: "no source", Sources: nil}}
	if err := Validate(unsourced); !errors.Is(err, ErrInvalid) {
		t.Errorf("a hook without a source validated: %v", err)
	}
}

// A story counts as used by an application while the newest pack of a stage
// links it; an older revision that a newer one replaced no longer counts.
func TestInterview_StoryUses_NewestRevisionDecides(t *testing.T) {
	f := newFixture(t, fixtureOpts{stories: true})
	in := f.input()
	f.stage("onsite-1", TypeBehavioral, true)
	p, err := f.svc.Build(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Save(f.ctx, p); err != nil {
		t.Fatal(err)
	}
	for i := range p.Questions {
		if !slices.Contains(p.Questions[i].StoryIDs, "alerts-ownership") {
			p.Questions[i].StoryIDs = nil
		}
	}
	if _, err := f.svc.Save(f.ctx, p); err != nil {
		t.Fatal(err)
	}
	uses, err := f.svc.StoryUses(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []StoryUse{{Candidacy: f.cand, Company: "Example Corp", Role: "Backend Engineer"}}
	if !slices.Equal(uses["alerts-ownership"], want) || len(uses["review-mentoring"]) != 0 {
		t.Errorf("uses = %+v", uses)
	}
}
