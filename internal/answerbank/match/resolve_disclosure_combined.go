package match

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// Combined and narrowed disclosures: a label that wraps a yes/no predicate with
// yes-only details. The details are omitted only after the predicate alone is
// strongly answered No by an entry the user maintains.

var (
	combinedDisclosureRE = lazyregexp.New(`(?i)^\s*((?:do|have|are|did|were|will|can)\s+you\b[^?]+\?)\s*if\s+(?:yes|so)[,.]?\s*(?:please\s+)?(?:provide|list|enter|specify|identify|describe|explain)\b[\s\S]+$`)
	narrowedDisclosureRE = lazyregexp.New(`(?i)^\s*(?:\(?[A-Ha-h]\)|[A-H]\.)?\s*((?:are|were|have|do|did)\s+you\b[^?]*?)\s+(?:who|that)\s+(?:is|are|was|were|has|have|had)\s+(?:either|both)\b[\s\S]*$`)
	// conjoinedDisclosureRE narrows by a second participle (Everlywell: "Have
	// you ever worked for a State agency and been involved, in any capacity,
	// in the process of procurement ...?").
	conjoinedDisclosureRE = lazyregexp.New(`(?i)^\s*((?:have|had)\s+you\b[^?]*?)\s+and\s+(?:also\s+)?been\b[\s\S]*$`)
	// selfGatedDisclosureRE is a box whose request carries its own condition
	// (Avride: "If you require an accommodation ..., please describe the
	// accommodation requested below."); the condition asked as "Do you ...?" is
	// the primary predicate.
	selfGatedDisclosureRE = lazyregexp.New(`(?i)^\s*if\s+you\s+((?:require|need)\b[^,?.]{3,200}?)\s*,\s*(?:please\s+)?(?:provide|list|enter|specify|identify|describe|explain)\b[\s\S]*$`)
	indicateNoRE          = lazyregexp.New(`(?i)\bif\s+no\s*[,.]?\s*(?:please\s+)?indicate\s+no\b`)
	ifYesNoMarkerRE       = lazyregexp.New(`(?i)\bif\s+(?:yes|no|so)\b`)
	plainNoAnswerRE       = lazyregexp.New(`(?i)^no$`)
	leadingNoRE           = lazyregexp.New(`(?i)^no\b`)
)

// primaryResult classifies a primary predicate on its own and, when an entry the
// user maintains reaches it by a signature, a route or a verbatim example (never
// by fuzzy match), matches it as a question of its own. A non-nil abstain is
// the answer to give instead.
func primaryResult(s *Step, primary string, sub Question) (Result, *Abstain, string) {
	probe := s.Q
	probe.Text = primary
	cx := newClassifier(s.Index, probe, newOptionScorer(probe.Text, s.Req.bud))
	cl, curated := cx.curated()
	if !curated {
		cl = cx.general()
	}
	switch {
	case cl.entry == nil:
		return Result{}, &Abstain{Reason: ReasonNoEntry}, "the primary predicate " + quoteTerm(clip(primary, 60)) + " matches no entry"
	case cl.stage == stageFuzzy || cl.stage == stageTopic || cl.stage == stageOptionOnly:
		return Result{}, &Abstain{Reason: ReasonNoEntry}, "the primary predicate is reachable only by fuzzy match"
	case cl.entry.BodyEmpty() && !cl.entry.derived() && !cl.entry.policyResolved():
		return Result{}, &Abstain{Reason: ReasonPlaceholderEmpty}, "the primary predicate's entry " + cl.entry.ID + " is blank"
	}
	sub.Text = primary
	return s.Rematch(sub), nil, ""
}

// conditionalDraft carries a primary's answer into the disclosure box: same
// entry, same confidence and outcome, source conditional.
func conditionalDraft(s *Step, r Result, answer, chosen string, labels []string, line string) Draft {
	ent := entryNamed(s, r.EntryID)
	if ent == nil && (r.EntryID != "" || r.AnswerKey != "") {
		ent = &Entry{ID: r.EntryID, Meta: Meta{AnswerKey: r.AnswerKey}}
	}
	d := Draft{Entry: ent, Answer: answer, ChosenLabel: chosen, ChosenLabels: labels, Source: SourceConditional,
		Start: r.Confidence, Deterministic: true}
	d.Trace = append(append([]string(nil), r.Trace...), line)
	return d
}

// combinedDisclosure fills a text box that holds one yes/no predicate and a
// yes-only detail request with "No" when the predicate is a maintained No.
func combinedDisclosure(s *Step) (Draft, bool) {
	q := s.Q
	if q.Kind != KindText || hasRenderedOptions(q.Options) {
		return Draft{}, false
	}
	m := combinedDisclosureRE.FindStringSubmatch(q.Text)
	if m == nil {
		return Draft{}, false
	}
	r, ab, why := primaryResult(s, m[1], s.Raw)
	if ab != nil {
		return Draft{Abstain: ab, Trace: []string{"combined disclosure: " + why}}, true
	}
	if !plainNoAnswerRE.MatchString(strings.TrimSpace(r.Answer)) || !isFilled(r) {
		if sponsorDetailRE.MatchString(q.Text) {
			// A sponsorship need's details are the recorded description (sponsorshipDetailStage; Hive).
			return Draft{}, false
		}
		return Draft{Abstain: &Abstain{Reason: ReasonNoEntry},
			Trace: append(append([]string(nil), r.Trace...), "combined disclosure: the primary predicate was not answered No -> the yes-only details are left to the user")}, true
	}
	return conditionalDraft(s, r, "No", "", nil, "combined disclosure: a maintained No answers the primary predicate; yes-only details do not apply"), true
}

// selfGatedDisclosure fills a required box whose request carries its own
// condition with "N/A" when the condition, asked on its own, is a maintained
// No, as a follow-up under a No gate is filled. Anything else is not its case:
// the box goes on to the readings it had (a sponsorship description among them).
func selfGatedDisclosure(s *Step) (Draft, bool) {
	q := s.Q
	if q.Kind != KindText || hasRenderedOptions(q.Options) || !q.IsRequired() {
		return Draft{}, false
	}
	m := selfGatedDisclosureRE.FindStringSubmatch(q.Text)
	if m == nil {
		return Draft{}, false
	}
	primary := "Do you " + strings.TrimSpace(m[1]) + "?"
	// The condition is a Yes or No question, whatever box asks it.
	sub := s.Raw
	sub.Kind, sub.InputType, sub.Options = KindRadioGroup, InputYesNo, []Option{{Label: "Yes", Value: "Yes"}, {Label: "No", Value: "No"}}
	r, ab, _ := primaryResult(s, primary, sub)
	if ab != nil || !isFilled(r) || !plainNoAnswerRE.MatchString(strings.TrimSpace(resultLabel(r))) {
		return Draft{}, false
	}
	return conditionalDraft(s, r, "N/A", "", nil, "self-gated disclosure: a maintained No answers "+quoteTerm(primary)+" -> \"N/A\""), true
}

// narrowedDisclosure answers a disclosure narrowed by "who is either (i) ... or
// (ii) ..." or by "and been ...": a maintained No to the primary entails No to
// every narrowing of it; a Yes says nothing about the narrowing and is never
// carried over.
func narrowedDisclosure(s *Step) (Draft, bool) {
	m := narrowedDisclosureRE.FindStringSubmatch(s.Q.Text)
	if m == nil {
		m = conjoinedDisclosureRE.FindStringSubmatch(s.Q.Text)
	}
	if m == nil {
		return Draft{}, false
	}
	primary := strings.TrimSpace(m[1]) + "?"
	r, ab, why := primaryResult(s, primary, s.Raw)
	if ab != nil {
		return Draft{Abstain: ab, Trace: []string{"narrowed disclosure: " + why}}, true
	}
	said := strings.TrimSpace(resultLabel(r))
	if !isFilled(r) || !leadingNoRE.MatchString(said) {
		return Draft{Abstain: &Abstain{Reason: ReasonNoEntry},
			Trace: append(append([]string(nil), r.Trace...), "narrowed disclosure: primary "+quoteTerm(primary)+" did not answer No -> left to the user")}, true
	}
	return conditionalDraft(s, r, r.Answer, r.ChosenLabel, r.ChosenLabels, "narrowed disclosure: a maintained No to the primary predicate answers every narrowing of it"), true
}

// indicateNo answers a text box that ends "If NO, please indicate NO" with the
// literal "NO", only when the predicate before it is a maintained No.
func indicateNo(s *Step) (Draft, bool) {
	q := s.Q
	if q.Kind != KindText || hasRenderedOptions(q.Options) || !indicateNoRE.MatchString(q.Text) {
		return Draft{}, false
	}
	primary := q.Text
	if i := strings.Index(primary, "?"); i >= 0 {
		primary = primary[:i+1]
	} else if loc := ifYesNoMarkerRE.FindStringIndex(primary); loc != nil {
		primary = strings.TrimSpace(primary[:loc[0]])
	}
	r, ab, why := primaryResult(s, primary, s.Raw)
	if ab != nil {
		return Draft{Abstain: ab, Trace: []string{"if NO, indicate NO: " + why}}, true
	}
	if !plainNoAnswerRE.MatchString(strings.TrimSpace(r.Answer)) || !isFilled(r) {
		return Draft{Abstain: &Abstain{Reason: ReasonNoEntry},
			Trace: append(append([]string(nil), r.Trace...), "if NO, indicate NO: the predicate was not answered No -> nothing is typed")}, true
	}
	return conditionalDraft(s, r, "NO", "", nil, "if NO, indicate NO: a maintained No answers the predicate -> the literal NO"), true
}

// combinedDisclosures is step 6: the combined and narrowed disclosures, then the
// sponsorship forms that no route may catch (the details box, the new or
// transfer pair and the conjunction), the time-zone range and named-zone questions; a self-gated box is last, so
// a dedicated form keeps its own reading.
var combinedDisclosures = chain(combinedDisclosure, narrowedDisclosure, sponsorshipDetailStage, sponsorshipKindStage, newH1BStage, conditionalSponsorship, timeZoneRangeStage, timeZoneNamedStage, usPersonStage, noClearanceStage, clearanceEligibilityStage, neverPublicStage, selfGatedDisclosure)
