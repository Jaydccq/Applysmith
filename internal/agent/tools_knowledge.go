package agent

import (
	"cmp"
	"context"
	"fmt"
	"strings"

	"applysmith/internal/kb"
	"applysmith/internal/prompts"
	"applysmith/internal/workspace"
)

// Knowledge is the knowledge-base service the evidence tools read.
type Knowledge interface {
	Search(ctx context.Context, q kb.Query) (kb.Result, error)
	StoryContextIndexed(ctx context.Context) (kb.StoryContext, error)
}

// ProfileReader returns the user's parsed profile.
type ProfileReader func(ctx context.Context) (workspace.Profile, error)

const (
	factChars   = 600
	defaultHits = 5
)

func kbSearch(k Knowledge) Tool {
	return Tool{Name: "kb_search", Label: "Searching the knowledge base", Class: ClassRead, PII: PIILow, Timeout: readTimeout,
		Description: "Search the user's knowledge base (stories, projects, experience, notes) and return the best matching passages.",
		Schema: `{"type":"object","additionalProperties":false,"required":["query"],"properties":{
			"query":{"type":"string","minLength":2,"maxLength":300},
			"k":{"type":"integer","minimum":1,"maximum":10}}}`,
		Run: func(ctx context.Context, c ToolCall) (ToolResult, error) {
			var a struct {
				Query string
				K     int
			}
			if err := decodeArgs(c.Args, &a); err != nil {
				return ToolResult{}, err
			}
			res, err := k.Search(ctx, kb.Query{Query: a.Query, K: cmp.Or(a.K, defaultHits)})
			if err != nil {
				return ToolResult{}, err
			}
			if len(res.Facts) == 0 {
				return ToolResult{Text: "Nothing in the knowledge base matches."}, nil
			}
			var b strings.Builder
			for _, f := range res.Facts {
				fmt.Fprintf(&b, "- %s: %s\n", f.SourceRef, cutRunes(oneLine(f.Text), factChars))
			}
			return ToolResult{Text: b.String()}, nil
		}}
}

func retrieveEvidence(k Knowledge) Tool {
	return Tool{Name: "retrieve_evidence", Label: "Gathering evidence", Class: ClassRead, PII: PIILow, Timeout: readTimeout,
		Description: "Retrieve evidence for a claim or an answer from everything the system holds about the user: knowledge base, profile, curated answers and corrections. Each passage carries its source key and trust tier, so an answer can cite it.",
		Schema: `{"type":"object","additionalProperties":false,"required":["query"],"properties":{
			"query":{"type":"string","minLength":2,"maxLength":300},
			"k":{"type":"integer","minimum":1,"maximum":10}}}`,
		Run: func(ctx context.Context, c ToolCall) (ToolResult, error) {
			var a struct {
				Query string
				K     int
			}
			if err := decodeArgs(c.Args, &a); err != nil {
				return ToolResult{}, err
			}
			res, err := k.Search(ctx, kb.Query{Query: a.Query, K: cmp.Or(a.K, defaultHits)})
			if err != nil {
				return ToolResult{}, err
			}
			var b strings.Builder
			fmt.Fprintf(&b, "Status: %s\n", res.Status)
			for _, f := range res.Facts {
				fmt.Fprintf(&b, "- [%s] (%s, %s) %s\n", f.ID, f.SourceKind+":"+f.SourceRef, f.Trust, cutRunes(oneLine(f.Text), factChars))
			}
			for _, f := range res.Failed {
				fmt.Fprintf(&b, "Source unavailable: %s\n", f.Source)
			}
			return ToolResult{Text: b.String()}, nil
		}}
}

func storyContext(k Knowledge) Tool {
	return Tool{Name: "story_context", Label: "Reading story coverage", Class: ClassRead, PII: PIINone, Timeout: readTimeout,
		Description: "Show which stories the user has banked and which principles and story shapes still lack one.",
		Schema:      `{"type":"object","additionalProperties":false,"properties":{}}`,
		Run: func(ctx context.Context, c ToolCall) (ToolResult, error) {
			sc, err := k.StoryContextIndexed(ctx)
			if err != nil {
				return ToolResult{}, err
			}
			var b strings.Builder
			fmt.Fprintf(&b, "Banked stories: %d\n", len(sc.Banked))
			for _, e := range sc.Banked {
				fmt.Fprintf(&b, "- %s (%s)\n", cmp.Or(e.Title, e.ID), strings.Join(e.Principles, ", "))
			}
			for _, p := range sc.Gaps.Principles {
				if p.Grade != "green" {
					fmt.Fprintf(&b, "Principle %s: %s\n", p.Name, p.Grade)
				}
			}
			for _, s := range sc.Gaps.Shapes {
				if s.Grade != "green" {
					fmt.Fprintf(&b, "Shape %s: %d of %d (%s)\n", s.Name, s.Have, s.Need, s.Grade)
				}
			}
			return ToolResult{Text: b.String()}, nil
		}}
}

// profileSummary leaves out what the agent must never state: contact details
// and identity. The answer bank holds those. Work authorization is given as
// profile.yml states it, so the agent answers "do I need sponsorship" from the
// profile instead of sending the user to the bank.
func profileSummary(read ProfileReader) Tool {
	return Tool{Name: "profile_summary", Label: "Reading the profile", Class: ClassRead, PII: PIILow, Timeout: readTimeout,
		Description: "Summarise what the user is looking for and what they have done: target roles, locations, compensation range, work modes, work authorization as the profile states it, employment history, education and skills. It holds no contact or identity details; those belong to the answer bank. Pass about with a school or employer the question assumes, and the summary says first when the profile does not list it.",
		Schema:      `{"type":"object","additionalProperties":false,"properties":{"about":{"type":"string","maxLength":120,"description":"A school or employer the user's question takes for granted."}}}`,
		Run: func(ctx context.Context, c ToolCall) (ToolResult, error) {
			var a struct{ About string }
			if err := decodeArgs(c.Args, &a); err != nil {
				return ToolResult{}, err
			}
			p, err := read(ctx)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Text: premiseNote(p, a.About) + summarizeProfile(p)}, nil
		}}
}

func summarizeProfile(p workspace.Profile) string {
	var b strings.Builder
	var roles []string
	for _, r := range p.Targets.Roles {
		roles = append(roles, strings.TrimSpace(r.Seniority+" "+r.Title)+" ("+r.EffectivePriority()+")")
	}
	line := func(label, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%s: %s\n", label, v)
		}
	}
	line("Target roles", strings.Join(roles, "; "))
	line("Target locations", strings.Join(p.Targets.Locations, "; "))
	if c := p.Targets.Compensation; c.Minimum != nil || c.Target != nil {
		line("Compensation", fmt.Sprintf("minimum %s, target %s %s per %s", intText(c.Minimum), intText(c.Target), c.Currency, c.Period))
	}
	line("Work modes", strings.Join(p.Preferences.WorkModes, ", "))
	line("Work authorization (as the profile states it; say nothing beyond it)", workAuthText(p.WorkAuthorization))
	for _, e := range p.History.Employment {
		line("Employment", fmt.Sprintf("%s, %s (%s to %s)", e.Title, e.Employer, e.Start, cmp.Or(e.End, "present")))
	}
	for _, e := range p.History.Education {
		line("Education", strings.TrimSpace(fmt.Sprintf("%s, %s %s", e.Degree, e.School, e.Field)))
	}
	var skills []string
	for _, s := range p.Skills {
		skills = append(skills, s.Name)
	}
	line("Skills", strings.Join(skills, ", "))
	if b.Len() == 0 {
		return "The profile holds no targets, history or skills yet."
	}
	return b.String()
}

// workAuthText lists the work-authorization facts the profile sets; an unset
// fact is left out, never guessed.
func workAuthText(w workspace.WorkAuth) string {
	var parts []string
	if len(w.AuthorizedCountries) > 0 {
		parts = append(parts, "authorized to work in "+strings.Join(w.AuthorizedCountries, ", "))
	}
	yesNo := func(label string, v *bool) {
		if v == nil {
			return
		}
		answer := "no"
		if *v {
			answer = "yes"
		}
		parts = append(parts, label+": "+answer)
	}
	yesNo("needs sponsorship now", w.RequiresSponsorshipNow)
	yesNo("needs sponsorship in the future", w.RequiresSponsorshipFuture)
	if w.VisaStatus != "" {
		parts = append(parts, "visa status "+w.VisaStatus)
	}
	if w.SecurityClearance != "" {
		parts = append(parts, "security clearance "+w.SecurityClearance)
	}
	return strings.Join(parts, "; ")
}

// premiseNote corrects a question that takes a school or an employer for
// granted when the profile lists neither: the answer must not go along with it.
func premiseNote(p workspace.Profile, about string) string {
	named := strings.TrimSpace(about)
	about = strings.ToLower(named)
	if about == "" {
		return ""
	}
	known := func(name string) bool {
		n := strings.ToLower(name)
		return n != "" && (strings.Contains(n, about) || strings.Contains(about, n))
	}
	for _, e := range p.History.Education {
		if known(e.School) {
			return ""
		}
	}
	for _, e := range p.History.Employment {
		if known(e.Employer) {
			return ""
		}
	}
	return fmt.Sprintf("Correction: the profile lists no school or employer called %q, so the question's premise is not supported by it. Say so before answering.\n", named)
}

func intText(n *int) string {
	if n == nil {
		return "not set"
	}
	return fmt.Sprint(*n)
}

func loadPromptMode(set *prompts.Set) Tool {
	names := set.Names()
	enum := `"` + strings.Join(names, `","`) + `"`
	return Tool{Name: "load_prompt_mode", Label: "Loading a prompt mode", Class: ClassRead, PII: PIINone, Timeout: readTimeout,
		Description: "Load the text of one of the product's prompt templates by name, to follow its method. The template is guidance for the work; it is not a source of facts about the user.",
		Schema:      `{"type":"object","additionalProperties":false,"required":["name"],"properties":{"name":{"type":"string","enum":[` + enum + `]}}}`,
		Run: func(ctx context.Context, c ToolCall) (ToolResult, error) {
			var a struct{ Name string }
			if err := decodeArgs(c.Args, &a); err != nil {
				return ToolResult{}, err
			}
			text, _, err := set.Source(a.Name)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{Text: text}, nil
		}}
}
