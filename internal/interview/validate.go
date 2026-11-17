package interview

import (
	"fmt"
	"unicode/utf8"
)

func tooLong(v string, n int) bool { return utf8.RuneCountInString(v) > n }

// Validate checks a pack against the specified bounds and references: every
// question and hook cites sources the pack lists.
func Validate(p Pack) error {
	if p.Candidacy < 1 || !stageIDRE.MatchString(p.Stage) {
		return invalid("a pack names a candidacy and a stage")
	}
	check := func(name string, n, max int) error {
		if n > max {
			return invalid("a pack holds at most %d %s, not %d", max, name, n)
		}
		return nil
	}
	for _, c := range []struct {
		name   string
		n, max int
	}{{"hooks", len(p.Hooks), MaxHooks}, {"claims", len(p.Claims), MaxClaims}, {"questions", len(p.Questions), MaxQuestions},
		{"feedback items", len(p.PriorFeedback), MaxFeedbackItems}, {"gaps", len(p.Gaps), MaxGaps},
		{"next actions", len(p.NextActions), MaxActions}, {"sources", len(p.Sources), MaxSources}} {
		if err := check(c.name, c.n, c.max); err != nil {
			return err
		}
	}
	if tooLong(p.RoleBrief, MaxBriefChars) || tooLong(p.CompanyBrief, MaxBriefChars) {
		return invalid("a brief holds at most %d characters", MaxBriefChars)
	}
	known := map[string]bool{}
	for _, s := range p.Sources {
		if s.Ref == "" || tooLong(s.Label, MaxLabelChars) || !oneOf([]string{KindDossier, KindReport, KindCompanyBrief, KindStory, KindFeedback}, s.Kind) ||
			!oneOf([]string{StateFrozen, StateCurrent, StateUserConfirmed, StateUnverified}, s.State) {
			return invalid("source %q is malformed", s.Ref)
		}
		known[s.Ref] = true
	}
	for i, h := range p.Hooks {
		if h.Text == "" || tooLong(h.Text, MaxLineChars) || len(h.Sources) == 0 {
			return invalid("hook %d needs text of at most %d characters and a source", i+1, MaxLineChars)
		}
		for _, r := range h.Sources {
			if !known[r] {
				return invalid("hook %d cites %q, which is not a listed source", i+1, r)
			}
		}
	}
	for _, l := range p.Claims {
		if tooLong(l, MaxLineChars) {
			return invalid("a claim holds at most %d characters", MaxLineChars)
		}
	}
	for _, st := range append(append([]Step{}, p.Gaps...), p.NextActions...) {
		if st.Text == "" || tooLong(st.Text, MaxLineChars) || tooLong(st.Target, MaxLineChars) || (st.Kind != "" && !oneOf(stepKinds, st.Kind)) {
			return invalid("a gap or action needs text of at most %d characters and a known kind", MaxLineChars)
		}
	}
	for _, f := range p.PriorFeedback {
		if tooLong(f, MaxFeedbackChars) {
			return invalid("a feedback item holds at most %d characters", MaxFeedbackChars)
		}
	}
	for i, q := range p.Questions {
		switch {
		case q.Category == "" || tooLong(q.Category, MaxCategoryChars):
			return invalid("question %d needs a category of at most %d characters", i+1, MaxCategoryChars)
		case q.Prompt == "" || tooLong(q.Prompt, MaxLineChars):
			return invalid("question %d needs a prompt of at most %d characters", i+1, MaxLineChars)
		case len(q.StoryIDs) > MaxStoriesPerQ:
			return invalid("question %d links at most %d stories", i+1, MaxStoriesPerQ)
		case len(q.Sources) > MaxRefsPerQ:
			return invalid("question %d cites at most %d sources", i+1, MaxRefsPerQ)
		}
		for _, r := range q.Sources {
			if !known[r] {
				return invalid("question %d cites %q, which is not a listed source", i+1, r)
			}
		}
		for _, id := range q.StoryIDs {
			if !known["story:"+id] {
				return invalid("question %d links story %q, which the pack does not list", i+1, id)
			}
		}
	}
	return nil
}

func (p Pack) String() string { return fmt.Sprintf("pack %d/%s r%d", p.Candidacy, p.Stage, p.Revision) }
