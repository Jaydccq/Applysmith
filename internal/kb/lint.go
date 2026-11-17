package kb

import (
	"fmt"
	"sort"
)

// Lint finding codes.
const (
	LintSectionOverBudget = "section_over_budget"
	LintEmptyAllowedFacts = "empty_allowed_facts"
	LintTriggerCollision  = "trigger_collision"
	LintNearDuplicate     = "near_duplicate"
	LintInvalidTrigger    = "invalid_trigger"
)

// sectionBudget is the number of characters a prompt renders per section.
const sectionBudget = 1800

// Finding is one lint warning. Lint never blocks saving.
type Finding struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Section string `json:"section,omitempty"`
	Length  int    `json:"length,omitempty"`
	Other   string `json:"other,omitempty"`
}

// lintPeer is a snippet as the others are compared with it: its triggers and
// event fingerprint, worked out once so a list of n snippets costs n parses.
type lintPeer struct {
	id       string
	triggers []trigger
	fp       fingerprint
}

func lintPeers(snippets []*Snippet) []lintPeer {
	out := make([]lintPeer, 0, len(snippets))
	for _, o := range snippets {
		out = append(out, lintPeer{id: o.ID, triggers: compileTriggers(o.Triggers), fp: fingerprintOf(o.Facts())})
	}
	return out
}

// lintSnippet checks one snippet against the others in the knowledge base.
func lintSnippet(sn *Snippet, others []lintPeer) []Finding {
	var out []Finding
	for _, sec := range sn.Sections {
		if n := runeLen(sec.Text); n > sectionBudget {
			out = append(out, Finding{Code: LintSectionOverBudget, Section: sec.Heading, Length: n,
				Message: fmt.Sprintf("section %q has %d characters; prompts render at most %d", sec.Heading, n, sectionBudget)})
		}
	}
	// Only a story's facts are read on their own: the duplicate-event check
	// compares them. Any other snippet is searched and quoted by its text, so a
	// snippet from the New snippet form needs no fact list.
	facts := sn.Facts()
	if len(facts) == 0 && sn.Category == CatBehavioral {
		out = append(out, Finding{Code: LintEmptyAllowedFacts,
			Message: "No statement of this story is confirmed, so it is not checked against your other stories for telling the same event. To fix it, add the story again with Add a story and delete this copy."})
	}
	mine := compileTriggers(sn.Triggers)
	for _, t := range mine {
		if t.invalid {
			out = append(out, Finding{Code: LintInvalidTrigger, Message: fmt.Sprintf("trigger %q is not a valid pattern and never matches", t.raw)})
		}
	}
	fp := fingerprintOf(facts)
	for _, o := range others {
		if o.id == sn.ID {
			continue
		}
		theirs := o.triggers
		for _, t := range mine {
			if t.isLiteral() && anyTrigger(theirs, t.raw) {
				out = append(out, Finding{Code: LintTriggerCollision, Other: o.id,
					Message: fmt.Sprintf("trigger %q also fires snippet %q", t.raw, o.id)})
				break
			}
		}
		for _, t := range theirs {
			if t.isLiteral() && anyTrigger(mine, t.raw) {
				if !hasFinding(out, LintTriggerCollision, o.id) {
					out = append(out, Finding{Code: LintTriggerCollision, Other: o.id,
						Message: fmt.Sprintf("trigger %q of snippet %q also fires this snippet", t.raw, o.id)})
				}
				break
			}
		}
		if sameEvent(fp, o.fp) {
			out = append(out, Finding{Code: LintNearDuplicate, Other: o.id,
				Message: fmt.Sprintf("describes the same event as snippet %q", o.id)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

func hasFinding(fs []Finding, code, other string) bool {
	for _, f := range fs {
		if f.Code == code && f.Other == other {
			return true
		}
	}
	return false
}
