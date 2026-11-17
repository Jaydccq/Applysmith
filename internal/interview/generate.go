package interview

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"applysmith/internal/kb"
)

// Doc is one source text with where it came from and how far to trust it.
type Doc struct {
	Text  string
	Ref   string
	Label string
	State string
}

// Fact is one line of company context and the source it came from.
type Fact struct {
	Text   string
	Source string
}

// Input is everything a pack is built from, gathered by the caller from the
// frozen application dossier or, when nothing was submitted, the current
// versions.
type Input struct {
	Candidacy int64
	Stage     Stage
	Company   string
	Role      string
	// Posting is the job description; PostingKind is KindDossier for the
	// frozen copy and KindReport for the evaluation report.
	Posting     Doc
	PostingKind string
	// ReportNumber is the evaluation report the posting was read from, 0 when
	// the pack reads the submitted copy instead.
	ReportNumber int64
	// Requirements are the posting's ranked requirements.
	Requirements []string
	Resume       Doc
	Cover        Doc
	// Facts are the company context lines; BriefState is their verification.
	Facts      []Fact
	BriefState string
	RunID      string
}

type refBook struct {
	list []SourceRef
	seen map[string]bool
}

func (b *refBook) add(kind, ref, label, state string) {
	if ref == "" || b.seen[ref] {
		return
	}
	if b.seen == nil {
		b.seen = map[string]bool{}
	}
	b.seen[ref] = true
	b.list = append(b.list, SourceRef{Kind: kind, Ref: ref, Label: clipRunes(label, MaxLabelChars), State: state})
}

// Build assembles the next pack for a stage without storing it. The pack
// rests only on the input, the candidacy's feedback and the story bank; a model,
// when routed, rewords requirement questions and proposes hooks that cite a
// company fact, and anything else it returns is dropped.
func (s *Service) Build(ctx context.Context, in Input) (Pack, error) {
	fb, err := s.StageFeedback(ctx, in.Candidacy, in.Stage.ID, MaxFeedbackItems)
	if err != nil {
		return Pack{}, err
	}
	reqs := trimmedTop(in.Requirements, RequirementQuestions)
	book := &refBook{}
	p := Pack{Candidacy: in.Candidacy, Stage: in.Stage.ID, ReportNumber: in.ReportNumber, Generation: GenDeterministic, Hooks: []Hook{}, Claims: []string{}, Questions: []Question{},
		PriorFeedback: []string{}, Gaps: []Step{}, NextActions: []Step{}}

	hasPosting := strings.TrimSpace(in.Posting.Text) != ""
	if hasPosting {
		book.add(in.PostingKind, in.Posting.Ref, in.Posting.Label, in.Posting.State)
		p.RoleBrief = roleBrief(in)
	} else {
		p.Gaps = append(p.Gaps, Step{Text: "No job description is saved for this candidacy, so there is no role brief and no question per requirement.", Kind: StepEvaluate})
	}
	if strings.TrimSpace(in.Resume.Text) != "" {
		book.add(KindDossier, in.Resume.Ref, in.Resume.Label, in.Resume.State)
		p.Claims = claimsFrom(in.Resume.Text)
	} else {
		p.Gaps = append(p.Gaps, Step{Text: "No submitted résumé is saved for this candidacy, so there are no claims to stay consistent with.", Kind: StepDocuments})
	}
	if strings.TrimSpace(in.Cover.Text) != "" {
		book.add(KindDossier, in.Cover.Ref, in.Cover.Label, in.Cover.State)
	}
	facts := usableFacts(in.Facts)
	if len(facts) > 0 {
		for _, f := range facts {
			book.add(KindCompanyBrief, f.Source, "Company brief: "+f.Source, in.BriefState)
		}
		p.CompanyBrief = companyBrief(in.Company, facts)
		p.Hooks = deterministicHooks(facts)
	} else {
		p.Gaps = append(p.Gaps, Step{Text: "No company brief exists, so company-specific hooks were omitted.", Kind: StepBrief, Target: in.Company})
	}
	if !in.Stage.UserConfirmed {
		p.Gaps = append(p.Gaps, Step{Text: fmt.Sprintf("Stage %s is not confirmed by you, so its type, date and interviewers may be wrong.", in.Stage.ID), Kind: StepConfirm, Target: in.Stage.ID})
	}

	questions := baseQuestions(in.Stage.Type)
	var uncovered []string
	for _, r := range reqs {
		q := Question{Category: "requirement", Prompt: clipRunes("Describe your experience with this requirement: "+clipRunes(r, 400), MaxLineChars), StoryIDs: []string{}, Sources: []string{}}
		if hasPosting {
			q.Sources = append(q.Sources, in.Posting.Ref)
		}
		matches := s.matchStories(ctx, r)
		for _, m := range matches {
			q.StoryIDs = append(q.StoryIDs, m.ID)
			q.Sources = append(q.Sources, "story:"+m.ID)
			book.add(KindStory, "story:"+m.ID, firstNonEmpty(m.Title, m.ID), StateUserConfirmed)
		}
		if len(matches) == 0 {
			uncovered = append(uncovered, r)
			p.Gaps = append(p.Gaps, Step{Text: fmt.Sprintf("No story in your story bank covers the requirement %q. Mine a story for it.", clipRunes(r, 120)), Kind: StepStory, Target: clipRunes(r, 120)})
		}
		questions = append(questions, q)
	}
	p.Questions = questions

	s.assist(ctx, in, &p, reqs, facts)

	for _, f := range fb {
		line := f.From + ": " + f.Text
		if f.Stage != in.Stage.ID {
			line = "Stage " + f.Stage + ", " + line
		}
		p.PriorFeedback = append(p.PriorFeedback, clipRunes(line, MaxFeedbackChars))
		state := StateUnverified
		if f.From == FromUser {
			state = StateUserConfirmed
		}
		book.add(KindFeedback, fmt.Sprintf("feedback:%d", f.ID), fmt.Sprintf("Feedback from %s", f.From), state)
	}
	p.NextActions = nextActions(in, hasPosting, len(facts) > 0, uncovered)
	p.Sources = book.list
	finalize(&p)
	return p, nil
}

func firstNonEmpty(in ...string) string {
	for _, s := range in {
		if s != "" {
			return s
		}
	}
	return ""
}

func (s *Service) matchStories(ctx context.Context, text string) []kb.StoryMatch {
	if s.d.Stories == nil {
		return nil
	}
	m, err := s.d.Stories.MatchStories(ctx, text, 3)
	if err != nil {
		return nil
	}
	return m
}

func trimmedTop(in []string, n int) []string {
	out := []string{}
	for _, r := range in {
		if r = strings.TrimSpace(r); r != "" && len(out) < n {
			out = append(out, r)
		}
	}
	return out
}

// roleBrief is the role line and the first content lines of the description.
func roleBrief(in Input) string {
	var b strings.Builder
	head := strings.TrimSpace(in.Role)
	if c := strings.TrimSpace(in.Company); c != "" {
		head = strings.TrimSpace(head + " at " + c)
	}
	if head != "" {
		b.WriteString(head + ". ")
	}
	for _, line := range unwrap(in.Posting.Text) {
		line = strings.TrimSpace(strings.TrimLeft(line, "-*#> "))
		if line == "" || strings.EqualFold(line, "posting") || strings.EqualFold(line, head) || titleLine(line, in) {
			continue
		}
		// A list item or a title has no closing mark; it is still its own
		// sentence, not the start of the next line's.
		if !strings.ContainsAny(line[len(line)-1:], ".!?:;") {
			line += "."
		}
		b.WriteString(line + " ")
	}
	return clipRunes(b.String(), MaxBriefChars)
}

// unwrap joins a hard-wrapped paragraph: a line that goes on in lower case,
// not as a list item, continues the sentence of the line above it.
func unwrap(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if n := len(out); n > 0 && out[n-1] != "" && line != "" && unicode.IsLower([]rune(line)[0]) {
			out[n-1] += " " + line
			continue
		}
		out = append(out, line)
	}
	return out
}

// titleLine is a heading that names the role and the company ("Mercury -
// Senior Backend Engineer"), which the brief already opens with.
func titleLine(line string, in Input) bool {
	role, company := strings.TrimSpace(in.Role), strings.TrimSpace(in.Company)
	return role != "" && company != "" && len(line) <= len(role)+len(company)+8 &&
		strings.Contains(strings.ToLower(line), strings.ToLower(role)) && strings.Contains(strings.ToLower(line), strings.ToLower(company))
}

func companyBrief(company string, facts []Fact) string {
	var b strings.Builder
	if c := strings.TrimSpace(company); c != "" {
		b.WriteString(c + ". ")
	}
	for _, f := range facts {
		b.WriteString(strings.TrimSpace(f.Text) + " ")
	}
	return clipRunes(b.String(), MaxBriefChars)
}

func usableFacts(in []Fact) []Fact {
	var out []Fact
	for _, f := range in {
		if strings.TrimSpace(f.Text) != "" && strings.TrimSpace(f.Source) != "" {
			out = append(out, Fact{Text: strings.TrimSpace(f.Text), Source: strings.TrimSpace(f.Source)})
		}
	}
	return out
}

func deterministicHooks(facts []Fact) []Hook {
	out := []Hook{}
	for _, f := range facts {
		if len(out) == 4 {
			break
		}
		out = append(out, Hook{Text: clipRunes(f.Text, MaxLineChars), Sources: []string{f.Source}})
	}
	return out
}

// claimsFrom takes the substantive lines of the submitted résumé, so the user
// can stay consistent with what was sent.
func claimsFrom(resume string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, line := range strings.Split(resume, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-*+ "))
		if len([]rune(line)) <= 20 || strings.HasPrefix(line, "#") || strings.Contains(line, "@") || seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, clipRunes(line, MaxLineChars))
		if len(out) == MaxClaims {
			break
		}
	}
	return out
}

func nextActions(in Input, hasPosting, hasBrief bool, uncovered []string) []Step {
	var out []Step
	for _, r := range uncovered {
		r = clipRunes(r, 120)
		out = append(out, Step{Text: fmt.Sprintf("Mine a story for %q, on the Wiki page or by asking the assistant.", r), Kind: StepStory, Target: r})
	}
	if !in.Stage.UserConfirmed {
		out = append(out, Step{Text: fmt.Sprintf("Confirm the details of stage %s.", in.Stage.ID), Kind: StepConfirm, Target: in.Stage.ID})
	}
	if !hasPosting {
		out = append(out, Step{Text: "Evaluate the posting, or save its description, to get a question per requirement.", Kind: StepEvaluate})
	}
	if !hasBrief {
		out = append(out, Step{Text: "Add a company brief on the Wiki page to get company hooks.", Kind: StepBrief, Target: in.Company})
	}
	return append(out, Step{Text: "Practice the likely questions with mock turns.", Kind: StepPractice, Target: in.Stage.ID},
		Step{Text: "After the interview, record how it went: the next packs of this stage and the later ones use it.", Kind: StepDebrief, Target: in.Stage.ID})
}

// finalize enforces the bounds: it trims lists, then drops references to
// sources that did not fit, and hooks left without one.
func finalize(p *Pack) {
	p.Hooks = capList(p.Hooks, MaxHooks)
	p.Claims = capList(p.Claims, MaxClaims)
	p.Questions = capList(p.Questions, MaxQuestions)
	p.PriorFeedback = capList(p.PriorFeedback, MaxFeedbackItems)
	p.Gaps = capList(p.Gaps, MaxGaps)
	p.NextActions = capList(p.NextActions, MaxActions)
	p.Sources = capList(p.Sources, MaxSources)
	known := map[string]bool{}
	for _, s := range p.Sources {
		known[s.Ref] = true
	}
	keep := func(refs []string, max int) []string {
		out := []string{}
		for _, r := range refs {
			if known[r] && len(out) < max {
				out = append(out, r)
			}
		}
		return out
	}
	hooks := p.Hooks[:0]
	for _, h := range p.Hooks {
		if h.Sources = keep(h.Sources, MaxRefsPerQ); len(h.Sources) > 0 {
			hooks = append(hooks, h)
		}
	}
	p.Hooks = hooks
	for i := range p.Questions {
		q := &p.Questions[i]
		q.Sources = keep(q.Sources, MaxRefsPerQ)
		ids := []string{}
		for _, id := range q.StoryIDs {
			if known["story:"+id] && len(ids) < MaxStoriesPerQ {
				ids = append(ids, id)
			}
		}
		q.StoryIDs = ids
	}
}

func capList[T any](in []T, n int) []T {
	if len(in) > n {
		return in[:n]
	}
	return in
}
