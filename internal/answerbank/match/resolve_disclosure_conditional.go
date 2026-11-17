package match

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"applysmith/internal/lazyregexp"
)

// Conditional follow-ups: the request pass that decides a follow-up slot from
// the answer before it. A follow-up's answer inherits the confidence and outcome
// of the answer it depends on and has source conditional. Questions arrive in
// page order, so "the question before" is the previous question of the request.

func isFilled(r Result) bool {
	return r.Outcome == OutcomeFilled || r.Outcome == OutcomeFilledReview
}

func resultLabel(r Result) string {
	if r.ChosenLabel != "" {
		return r.ChosenLabel
	}
	return r.Answer
}

// isYesNoGate reports a question that was answered Yes or No: a yes_no input, or
// (with no input type) a two-option control labelled yes and no.
func isYesNoGate(q Question) bool {
	if q.InputType == InputYesNo {
		return true
	}
	if q.InputType != "" {
		return false
	}
	switch q.Kind {
	case KindSelect, KindRadioGroup, KindCustomListbox:
		return len(q.Options) == 2 && isYesNoOptionSet(q.Options)
	}
	return false
}

// inherit builds a conditional result that takes its confidence and outcome from
// the answer it depends on.
func inherit(sel, answer string, from Result, entryID, answerKey string, trace []string, line string) Result {
	return Result{Selector: sel, Outcome: from.Outcome, Answer: answer, EntryID: entryID, AnswerKey: answerKey,
		Source: SourceConditional, Confidence: from.Confidence, Trace: append(append([]string(nil), trace...), line)}
}

// revoked turns a written answer back into an abstain, so no later lane writes it.
func revoked(prev Result, reason Reason, line string) Result {
	return Result{Selector: prev.Selector, Outcome: OutcomeAbstained, Reason: reason, Trace: append(append([]string(nil), prev.Trace...), line)}
}

// conditionalFollowUps is request pass 4: the sibling conditional, the named
// choice follow-up, then for each
// question the specify slot, the sponsorship follow-up and the generic gate rule.
func conditionalFollowUps(pc *passView) {
	if len(pc.Results) >= 2 {
		siblingConditionals(pc)
		namedChoiceFollowUps(pc)
	}
	for i := range pc.Results {
		if pc.Results[i].Terminal || pc.Questions[i].Kind != KindText || pc.Results[i].AnswerKey == namedChoiceKey {
			continue // the page's own words settle a named-choice follow-up
		}
		if specifySlot(pc, i) {
			continue
		}
		if i == 0 {
			continue
		}
		if sponsorshipFollowUp(pc, i) {
			continue
		}
		gateFollowUp(pc, i)
	}
}

// gateOf returns the answered Yes/No gate that immediately precedes question i.
func gateOf(pc *passView, i int) (gate Result, yes, no, ok bool) {
	if i == 0 || !isYesNoGate(pc.Questions[i-1]) || !isFilled(pc.Results[i-1]) {
		return Result{}, false, false, false
	}
	g := pc.Results[i-1]
	label := resultLabel(g)
	yes, no = isYesLabel(label), isNoLabel(label)
	return g, yes, no, yes || no
}

// isFollowUpSlot is a follow-up slot of the question before it: the detail shapes
// of the specification, and the bare "If yes, please explain" and "Which
// ones?", whose subject is the previous answer.
func isFollowUpSlot(text string) bool {
	return isConditionalDetailText(text) || bareExplainFollowE.MatchString(text) || bareWhichFollowRE.MatchString(text)
}

// bareWhichFollowRE is a follow-up that only asks which of what the question
// before named (LiveKit: "Which ones?" under "Do you contribute to open source
// repos?"), or under an "If yes" asks a short which or what of it (Epirus:
// "If yes, What level?" under "Do you have security clearance?").
var bareWhichFollowRE = lazyregexp.New(`(?i)^\s*(?:(?:if\s+(?:yes|so)\s*[,:;-]?\s*)?(?:which|what)\s+ones?|if\s+(?:yes|so)\s*[,:;-]?\s*(?:which|what)(?:\s+[a-z]+){1,2})\s*[?.:*]*\s*$`)

var leadingIfRE = lazyregexp.New(`(?i)^\s*if\b`)

var (
	ownReferralConditionRE = lazyregexp.New(`(?i)\bif\s+(?:you\s+(?:were|are|have\s+been|had\s+been)\s+)?referred\b`)
	referralWordRE         = lazyregexp.New(`(?i)\brefer(?:red|ral|rals|rer|ring)?\b`)
)

// asksOwnQuestion reports a text that opens with a question of its own: "How
// did you hear about Socket? If referred, please let us know by who!"
// (Socket). A conditional clause after it qualifies that question, not the
// answer before it. A lead that is itself conditional ("If so, which ones?")
// or a follow-up shape ("Which ones?") is no question of its own.
func asksOwnQuestion(text string) bool {
	lead, _, ok := strings.Cut(text, "?")
	return ok && strings.TrimSpace(lead) != "" && !leadingIfRE.MatchString(lead) && !isFollowUpSlot(lead+"?")
}

// gateFollowUp decides a follow-up slot from its gate: a No gate gives a required
// slot "N/A" and an optional slot a blank fill; any other gate state abstains and
// revokes a fill. A visa-type follow-up under a Yes gate takes the recorded
// immigration status. A slot the engine named after its gate ("<gate> If yes,
// please explain") is settled by a No gate the same way, a fill from the
// gate's own entry included; under any other gate it keeps its answer, since
// an answer saved to the whole question is the user's. A text that asks its own
// question first is no bare slot.
func gateFollowUp(pc *passView, i int) {
	q, cur := pc.Questions[i], pc.Results[i]
	bare := isFollowUpSlot(q.Text) && !asksOwnQuestion(q.Text)
	if hasRenderedOptions(q.Options) || !bare && !isFollowUpSlot(stemAfter(q.Text, pc.Questions[i-1].Text)) {
		return
	}
	// A slot that states its own referral condition follows only a question
	// about a referral (GHX: "If you were referred to this position by ...,
	// please provide the person's name." under a salary question); under any
	// other its own answer stands.
	if ownReferralConditionRE.MatchString(q.Text) && !referralWordRE.MatchString(pc.Questions[i-1].Text) {
		return
	}
	gate, yes, no, ok := gateOf(pc, i)
	gateText := clip(pc.Questions[i-1].Text, 60)
	switch {
	case ok && no:
		if isFilled(cur) && (bare || cur.EntryID != gate.EntryID) {
			return // a dedicated entry's own answer is left alone
		}
		answer, what := "N/A", `fill "N/A"`
		if !q.IsRequired() {
			answer, what = "", "leave it blank"
		}
		pc.Results[i] = inherit(cur.Selector, answer, gate, "", "", cur.Trace,
			fmt.Sprintf("cross-question: preceding %q answered \"No\" -> %s", gateText, what))
	case !bare:
		return
	case ok && yes && isVisaStatusFollowUp(q.Text):
		status := ownText(entryByID(pc.Entries, idCurrentImmigrationStatus))
		if status == "" {
			pc.Results[i] = revoked(cur, ReasonPlaceholderEmpty, fmt.Sprintf("cross-question: preceding %q answered \"Yes\"; %s is blank -> left to the user", gateText, idCurrentImmigrationStatus))
			return
		}
		pc.Results[i] = inherit(cur.Selector, status, gate, idCurrentImmigrationStatus, "current_immigration_status", cur.Trace,
			fmt.Sprintf("cross-question: preceding %q answered \"Yes\" -> visa-type follow-up takes %s", gateText, idCurrentImmigrationStatus))
	default:
		if isFilled(cur) {
			pc.Results[i] = revoked(cur, ReasonNoEntry, fmt.Sprintf("cross-question: preceding %q is not an answered \"No\" gate -> revoke the fill, let the user answer", gateText))
		}
	}
}

// stemAfter is what text asks after the question before it, when it starts
// with that question: the way the engine names a bare follow-up stem.
func stemAfter(text, before string) string {
	if rest, ok := strings.CutPrefix(text, before); ok && before != "" {
		return strings.TrimLeft(rest, " ,.:;-")
	}
	return ""
}

// sponsorshipFollowUp answers a bare "If yes, please explain" under a sponsorship
// gate answered Yes from the user's sponsorship description; with a No gate the
// generic rule gives the N/A.
func sponsorshipFollowUp(pc *passView, i int) bool {
	q := pc.Questions[i]
	if hasRenderedOptions(q.Options) || !bareExplainFollowE.MatchString(q.Text) {
		return false
	}
	gate, yes, _, ok := gateOf(pc, i)
	if !ok || !yes || gate.EntryID != idSponsorshipNowOrFuture {
		return false
	}
	desc := entryByID(pc.Entries, idSponsorshipTypeDescription)
	text := ownText(desc)
	cur := pc.Results[i]
	if text == "" {
		pc.Results[i] = revoked(cur, ReasonPlaceholderEmpty, "cross-question: sponsorship gate answered \"Yes\"; "+idSponsorshipTypeDescription+" is blank -> left to the user")
		return true
	}
	pc.Results[i] = inherit(cur.Selector, text, gate, desc.ID, desc.EffectiveAnswerKey(), cur.Trace,
		"cross-question: sponsorship gate answered \"Yes\" -> "+idSponsorshipTypeDescription)
	return true
}

func entryByID(entries []Entry, id string) *Entry {
	for i := range entries {
		if entries[i].ID == id && !entries[i].RunContext {
			return &entries[i]
		}
	}
	return nil
}

var (
	otherWordRE   = lazyregexp.New(`(?i)\bother\b`)
	bareYesNoFill = lazyregexp.New(`(?i)^\s*(?:yes|no|true|false)\s*\.?\s*$`)
)

// hasTriggerOption reports that a rendered option is the choice the trigger names.
func hasTriggerOption(q Question, trigger string) bool {
	for _, o := range q.Options {
		l := strings.TrimSpace(o.Label)
		if trigger == "Other" && otherWordRE.MatchString(l) && len(l) < 20 || labelNamesTrigger(l, trigger) {
			return true
		}
	}
	return false
}

// optionMarkerRE is the letter or number a list puts before its option ("(f)
// Other (please explain)").
var optionMarkerRE = lazyregexp.New(`(?i)^\(?[a-z0-9]{1,2}[).]\s+`)

// labelNamesTrigger reports that a rendered or selected label is the trigger.
// "If not" names the No of the Yes/No before it (Flex: "If not, please
// specify your base salary request.").
func labelNamesTrigger(label, trigger string) bool {
	switch {
	case trigger == "Other":
		return startsOtherRE.MatchString(optionMarkerRE.ReplaceAllString(strings.TrimSpace(label), ""))
	case strings.EqualFold(trigger, "not"):
		return isNoLabel(label)
	}
	return containsWord(label, trigger)
}

// selectedLabels are the option labels the parent actually selected, in whatever
// shape the result carries them.
func selectedLabels(r Result) []string {
	switch {
	case len(r.ChosenLabels) > 0:
		return r.ChosenLabels
	case r.ChosenLabel != "":
		return []string{r.ChosenLabel}
	case r.Answer != "":
		return []string{r.Answer}
	}
	return nil
}

// slotNamesSelection reports a slot whose own condition names one of the
// parent's selections beside its trigger ("If you selected Employee Referral,
// ..., or Other, please specify"): the slot applies to it.
func slotNamesSelection(text string, selected []string) bool {
	for _, l := range selected {
		l = strings.ToLower(strings.TrimSpace(l))
		if len(l) < 3 {
			continue
		}
		// \b as RE2 reads it: a word rune on exactly one side of each end.
		first, _ := utf8.DecodeRuneInString(l)
		last, _ := utf8.DecodeLastRuneInString(l)
		edges := func(b, a rune) bool {
			return asciiWordRune(b) != asciiWordRune(first) && asciiWordRune(a) != asciiWordRune(last)
		}
		if phraseAt(strings.ToLower(text), l, edges) >= 0 {
			return true
		}
	}
	return false
}

// specifySlot decides a "please specify" slot from its parent choice group's own
// selection and reports whether it decided the question. The parent is the
// choice group before the slot, or for an "Other" slot with none before it
// that offers "Other", the list right after it that does.
func specifySlot(pc *passView, i int) bool {
	q, cur := pc.Questions[i], pc.Results[i]
	trigger, ok := specifyTrigger(q.Text)
	if !ok || hasRenderedOptions(q.Options) {
		return false
	}
	parent := i - 1
	for parent >= 0 && pc.Questions[parent].Kind == KindText {
		if _, slot := specifyTrigger(pc.Questions[parent].Text); !slot {
			break
		}
		parent--
	}
	abstain := func(line string) bool {
		if isFilled(cur) {
			pc.Results[i] = revoked(cur, ReasonNoEntry, line)
		}
		return true
	}
	// A closed list renders no option yet: the label it was given is its selection.
	closed := parent >= 0 && !hasRenderedOptions(pc.Questions[parent].Options) && isFilled(pc.Results[parent])
	// A page may put an "Other" slot before the list it qualifies (Oklo): with
	// no choice group before it that offers "Other", the list right after it
	// that does is its parent.
	if trigger == "Other" && (parent < 0 || pc.Questions[parent].Kind == KindText || !closed && !hasTriggerOption(pc.Questions[parent], trigger)) {
		if n := i + 1; n < len(pc.Questions) && pc.Questions[n].Kind != KindText && hasTriggerOption(pc.Questions[n], trigger) {
			parent, closed = n, false
		}
	}
	if parent < 0 || pc.Questions[parent].Kind == KindText || !closed && !hasTriggerOption(pc.Questions[parent], trigger) {
		switch {
		case trigger != "Other":
			return abstain("cross-question: no preceding choice group renders " + quoteTerm(trigger) + " -> the specify slot cannot be licensed")
		case isFilled(cur) && bareYesNoFill.MatchString(cur.Answer):
			return abstain("cross-question: a bare yes/no is never an answer to a specify slot -> dropped")
		}
		return false
	}
	parentText := clip(pc.Questions[parent].Text, 60)
	pr := pc.Results[parent]
	selected := selectedLabels(pr)
	if !isFilled(pr) || len(selected) == 0 {
		return abstain(fmt.Sprintf("cross-question: preceding %q is unanswered -> the specify slot cannot be licensed", parentText))
	}
	if !slices.ContainsFunc(selected, func(l string) bool { return labelNamesTrigger(l, trigger) }) {
		answer, what := "", "leave blank"
		switch m := otherwiseTypeRE.FindStringSubmatch(q.Text); {
		case m != nil:
			answer, what = m[1], "type the "+quoteTerm(m[1])+" the slot asks for then"
		case slotNamesSelection(q.Text, selected):
			return abstain(fmt.Sprintf("cross-question: the slot also names %q, which %q selected -> left to the user", selected[0], parentText))
		case q.IsRequired():
			// Tanium: a required slot that does not apply, as under a No gate.
			answer, what = "N/A", `fill "N/A", the slot being required`
		}
		pc.Results[i] = inherit(cur.Selector, answer, pr, "", "", cur.Trace,
			fmt.Sprintf("cross-question: preceding %q did not select %q -> %s", parentText, trigger, what))
		return true
	}
	if trigger != "Other" {
		return abstain(fmt.Sprintf("cross-question: preceding %q selected %q; no entry answers a named specify slot -> left to the user", parentText, trigger))
	}
	id := otherDetailEntry(pc, pr)
	if id == "" {
		return abstain(fmt.Sprintf("cross-question: preceding %q selected \"Other\"; no entry records the detail of %s -> left to the user", parentText, quoteTerm(pr.EntryID)))
	}
	src := entryByID(pc.Entries, id)
	text := ownText(src)
	if text == "" {
		pc.Results[i] = revoked(cur, ReasonPlaceholderEmpty, fmt.Sprintf("cross-question: preceding %q selected \"Other\"; %s is blank -> left to the user", parentText, id))
		return true
	}
	pc.Results[i] = inherit(cur.Selector, text, pr, src.ID, src.EffectiveAnswerKey(), cur.Trace,
		fmt.Sprintf("cross-question: preceding %q selected \"Other\" -> answer from %s", parentText, id))
	return true
}

// otherDetailEntry is the entry whose text answers an "Other" slot under the
// parent's answer: the source detail under a source question, the recorded
// immigration status under a work-authorization one (a citizenship list's
// "Other, please explain"). A parent of any other family records no detail,
// and the referral detail is never an answer to it.
func otherDetailEntry(pc *passView, parent Result) string {
	e := entryByID(pc.Entries, parent.EntryID)
	switch {
	case parent.EntryID == "referral-source" || e != nil && e.Category == CategorySource:
		return "referral-source-specify"
	case e != nil && e.Category == CategoryWorkAuthorization:
		return idCurrentImmigrationStatus
	}
	return ""
}

// siblingClause is one clause of a sibling conditional: the answer key it reads
// and the label that makes it trigger.
type siblingClause struct {
	key     string
	trigger func(string) bool
}

var workAuthConditionalClauses = []siblingClause{
	{"work_authorized", isNoLabel},
	{"work_auth_based_on_visa_spouse", isYesLabel},
	{"sponsorship_now_or_future", isYesLabel},
}

// siblingConditionals resolves "Did you answer No to Q1 and/or Yes to Q2 or 3?"
// from the sibling answers by answer key. Yes is safe the instant any present
// clause triggers; No only when every clause has an answer and none triggers;
// with no clause answered the question is left alone.
func siblingConditionals(pc *passView) {
	for i, q := range pc.Questions {
		if !isWorkAuthConditionalQuestion(q.Text) || pc.Results[i].Terminal {
			continue
		}
		labels := make([]string, len(workAuthConditionalClauses))
		present, triggered := 0, false
		var parts []string
		low := 1.0
		for ci, c := range workAuthConditionalClauses {
			for _, r := range pc.Results {
				if r.AnswerKey == c.key && isFilled(r) && resultLabel(r) != "" {
					labels[ci] = resultLabel(r)
					low = min(low, r.Confidence)
					break
				}
			}
			if labels[ci] != "" {
				present++
				triggered = triggered || c.trigger(labels[ci])
			}
			parts = append(parts, c.key+"="+firstNonEmpty(labels[ci], "-"))
		}
		if present == 0 || !triggered && present < len(workAuthConditionalClauses) {
			continue
		}
		answer := "No"
		if triggered {
			answer = "Yes"
		}
		cur := pc.Results[i]
		outcome, _ := OutcomeForConfidence(low)
		res := Result{Selector: cur.Selector, Outcome: outcome, Answer: answer, AnswerKey: "work_auth_conditional",
			Source: SourceConditional, Confidence: low,
			Trace: append(append([]string(nil), cur.Trace...), "work-auth-conditional: "+strings.Join(parts, " ")+" -> "+quoteTerm(answer))}
		if o, found := ownYesNo(q.Options, answer == "Yes"); found {
			res.ChosenLabel = o.Label
		}
		pc.Results[i] = res
	}
}

// namedChoiceElseRE is a follow-up that names the choice above it that asks it
// and the answer to give otherwise (HelloFresh: "If you selected, “Two or More
// Race/Ethnicities” above, please select which ... If you did not select, “Two
// or More Race/Ethnicities”, please answer “Does not apply”").
var namedChoiceElseRE = lazyregexp.New(`(?i)\bif you (?:did not|didn't|do not|don't) (?:select|choose|pick|check|answer)\s*,?\s*["“”]([^"“”]+)["“”]\s*,?\s*(?:above\s*,?\s*)?(?:then\s+)?(?:please\s+)?(?:answer|select|choose|pick|check|mark)\s*["“”]([^"“”]+)["“”]`)

// namedChoiceIfRE and namedChoiceOtherwiseRE are such a follow-up told in two
// sentences, as a text box (Squarespace: "If you answered ‘Yes - Consultant or
// Vendor for Squarespace', please elaborate below ... If you did not answer
// this way, please put ‘N/A’.").
var (
	namedChoiceIfRE        = lazyregexp.New(`(?i)\bif you (?:answered|selected|chose|checked|picked)\s*,?\s*["“”‘’']([^"“”‘’']+)["“”‘’']`)
	namedChoiceOtherwiseRE = lazyregexp.New(`(?i)\bif you (?:did not|didn't|do not|don't) (?:answer|select|choose|pick|check) (?:this|that) way\s*,?\s*(?:then\s+)?(?:please\s+)?(?:put|enter|input|write|type|answer)\s*["“”‘’']([^"“”‘’']{1,40})["“”‘’']`)
)

// namedChoiceKey marks the answer of a named-choice follow-up.
const namedChoiceKey = "named_choice_follow_up"

// namedAnswerRE is a follow-up that names the answer before it that takes a
// value the page gives (Squarespace: "If Yes, or if you are not sure, please
// elaborate below. If you answered no, please input ‘N/A’.").
var namedAnswerRE = lazyregexp.New(`(?i)\bif you (?:answered|selected|chose|checked|picked)\s*["“”‘’']?([a-z][a-z ]{0,30}?)["“”‘’']?\s*,\s*(?:then\s+)?(?:please\s+)?(?:put|enter|input|write|type)\s*["“”‘’']([^"“”‘’']{1,40})["“”‘’']`)

// notApplicableRE is a page's word for a follow-up with nothing to say.
var notApplicableRE = lazyregexp.New(`(?i)^\s*(?:n/?a|none|not applicable|does not apply)\s*\.?\s*$`)

// namedChoice reads the choice a follow-up names and the answer the page asks
// for: when that choice is held (held), or when it is not.
func namedChoice(text string) (named, answer string, held, ok bool) {
	if m := namedChoiceElseRE.FindStringSubmatch(text); m != nil {
		return m[1], m[2], false, true
	}
	if c, e := namedChoiceIfRE.FindStringSubmatch(text), namedChoiceOtherwiseRE.FindStringSubmatch(text); c != nil && e != nil {
		return c[1], strings.TrimSpace(e[1]), false, true
	}
	if m := namedAnswerRE.FindStringSubmatch(text); m != nil {
		return strings.TrimSpace(m[1]), strings.TrimSpace(m[2]), true, true
	}
	return "", "", false, false
}

// namedChoiceFollowUps answers such a follow-up from the nearest question
// before it that offers the named choice: when that question holds another
// choice, the follow-up takes the answer the page names for it (its option,
// or for a text box the words themselves), with the confidence of the answer
// it depends on. A question that holds the named choice, or holds nothing
// yet, leaves the follow-up to its own lane. The answer is the page's own and
// its condition the user's own answer, so it replaces even a terminal
// abstain: a self-identification list asked as a follow-up refuses the bank's
// race when it allows several.
func namedChoiceFollowUps(pc *passView) {
	for i := 1; i < len(pc.Questions); i++ {
		q := pc.Questions[i]
		named, answer, whenHeld, ok := namedChoice(q.Text)
		if !ok {
			named, answer, ok = optionCondition(pc, i)
		}
		if !ok {
			unselectedChoicesFollowUp(pc, i)
			continue
		}
		switch {
		case hasRenderedOptions(q.Options):
			o, ok := optionNamed(q.Options, answer)
			if !ok {
				continue
			}
			answer = o.Label
		case q.Kind != KindText || !notApplicableRE.MatchString(answer):
			continue // a text box takes only the page's word for "nothing to say", never a statement
		}
		for j := i - 1; j >= 0; j-- {
			if _, offers := optionNamed(pc.Questions[j].Options, named); !offers {
				continue
			}
			held := selectedLabels(pc.Results[j])
			// The answer the page names is held as a whole; a choice it says to
			// avoid counts held anywhere in what was chosen.
			has := slices.ContainsFunc(held, func(h string) bool {
				if whenHeld {
					return singularWords(h) == singularWords(named)
				}
				return holdsName(h, named)
			})
			if !isFilled(pc.Results[j]) || len(held) == 0 || has != whenHeld {
				break
			}
			rel := "not"
			if whenHeld {
				rel = "which is"
			}
			line := fmt.Sprintf("named-choice follow-up: the question before holds %s, %s %s -> %s", quoteTerm(strings.Join(held, "; ")), rel, quoteTerm(named), quoteTerm(answer))
			res := inherit(pc.Results[i].Selector, answer, pc.Results[j], "", namedChoiceKey, pc.Results[i].Trace, line)
			if hasRenderedOptions(q.Options) {
				res.ChosenLabel = answer
			}
			pc.Results[i] = res
			break
		}
	}
}

// quotedChoicesRE is a condition that quotes the choices that ask a text
// follow-up (Observe.AI: "Please type in your answer if you selected "Current
// Observe.AI employee" or "Other""); quotedChoiceRE is one of them.
var (
	quotedChoicesRE = lazyregexp.New(`(?i)\bif you (?:selected|chose|checked|picked|answered)\s+((?:["“”][^"“”]+["“”]\s*,?\s*(?:(?:or|and/or)\s+)?)+)`)
	quotedChoiceRE  = lazyregexp.New(`["“”]([^"“”]+)["“”]`)
)

// unselectedChoicesFollowUp gives a text follow-up whose condition quotes
// choices of the question before it "N/A" when that question holds none of
// them, as a No gate does; optional, it stays blank. A question that holds
// one of them, or holds nothing yet, leaves the follow-up to its own lane.
func unselectedChoicesFollowUp(pc *passView, i int) {
	q := pc.Questions[i]
	m := quotedChoicesRE.FindStringSubmatch(q.Text)
	if q.Kind != KindText || hasRenderedOptions(q.Options) || m == nil {
		return
	}
	var names []string
	for _, n := range quotedChoiceRE.FindAllStringSubmatch(m[1], -1) {
		names = append(names, strings.TrimSpace(n[1]))
	}
	for j := i - 1; j >= 0; j-- {
		if !slices.ContainsFunc(names, func(n string) bool { _, ok := optionNamed(pc.Questions[j].Options, n); return ok }) {
			continue
		}
		held := selectedLabels(pc.Results[j])
		if !isFilled(pc.Results[j]) || len(held) == 0 || slices.ContainsFunc(held, func(h string) bool {
			return slices.ContainsFunc(names, func(n string) bool { return singularWords(h) == singularWords(n) })
		}) {
			return
		}
		answer, what := "N/A", `"N/A"`
		if !q.IsRequired() {
			answer, what = "", "blank"
		}
		line := fmt.Sprintf("named-choice follow-up: the question before holds %s, none of %s -> %s", quoteTerm(strings.Join(held, "; ")), quoteTerm(strings.Join(names, "; ")), what)
		pc.Results[i] = inherit(pc.Results[i].Selector, answer, pc.Results[j], "", namedChoiceKey, pc.Results[i].Trace, line)
		return
	}
}

// openingConditionRE is the condition a follow-up opens with.
var openingConditionRE = lazyregexp.New(`(?i)^\s*if\b([^.?!;]{3,120})`)

// optionCondition reads a follow-up whose opening condition names, unquoted,
// one option of the nearest question before it that offers options, and whose
// own words give the answer otherwise (Tenex: "If you were referred by a
// Current Employee, please list their name. If this is not applicable, please
// list N/A." under "How did you find out about this position?").
func optionCondition(pc *passView, i int) (named, answer string, ok bool) {
	c, e := openingConditionRE.FindStringSubmatch(pc.Questions[i].Text), otherwiseTypeRE.FindStringSubmatch(pc.Questions[i].Text)
	if c == nil || e == nil {
		return "", "", false
	}
	for j := i - 1; j >= 0; j-- {
		opts := realOptions(pc.Questions[j].Options)
		if len(opts) == 0 {
			continue
		}
		var found []string
		for _, o := range opts {
			if l := strings.TrimSpace(o.Label); len(l) >= 3 && holdsName(c[1], l) {
				found = append(found, l)
			}
		}
		if len(found) != 1 {
			return "", "", false
		}
		return found[0], e[1], true
	}
	return "", "", false
}

// optionNamed is the one option whose words are the name's, a plural read as
// its singular ("Two or More Races/Ethnicities" for "Two or More
// Race/Ethnicities").
func optionNamed(opts []Option, name string) (Option, bool) {
	want := singularWords(name)
	var found []Option
	for _, o := range realOptions(opts) {
		if singularWords(o.Label) == want {
			found = append(found, o)
		}
	}
	if want == "" || len(found) != 1 {
		return Option{}, false
	}
	return found[0], true
}

// holdsName reports whether an answer holds the name's words in order, as one
// of several choices it lists may.
func holdsName(answer, name string) bool {
	return strings.Contains(" "+singularWords(answer)+" ", " "+singularWords(name)+" ")
}

// singularWords is a label's words with a plural "s" cut off.
func singularWords(s string) string {
	toks := tokenize(normalizeLabel(s))
	for i, t := range toks {
		if len(t) > 3 && strings.HasSuffix(t, "s") && !strings.HasSuffix(t, "ss") {
			toks[i] = strings.TrimSuffix(t, "s")
		}
	}
	return strings.Join(toks, " ")
}

// previousPlacesRE is a question about the places an earlier answer chose
// (Stripe: "Are you authorized to work in the location(s) you selected in your
// previous response?").
var previousPlacesRE = lazyregexp.New(`(?i)\bthe (?:locations?|countr(?:y|ies))(?:\((?:s|ies)\))? (?:you )?(?:selected|chose|picked|indicated)\b[^?.]*?\b(?:previous|prior|above)\b(?: (?:response|answer|question))?`)

// thatCountryRE points back at a country (Quora: "Are you legally authorized
// to work in that country?" after "In which of the following ... countries are
// you seeking to work?"): the country a picker just before it holds, read only
// when one does and the question is not answered already.
var thatCountryRE = lazyregexp.New(`(?i)\bth(?:at|ose) countr(?:y|ies)\b`)

var countryAskRE = lazyregexp.New(`(?i)\bcountr(?:y|ies)\b`)

// placesSelectedBeforePass answers a question about the places an earlier
// answer chose: the reference is replaced with the countries the nearest list
// before it holds, and the question is matched again, so the work facts answer
// for those countries as for a question that names them. A list that holds
// anything but countries, and a question already answered, are left as they
// are.
func placesSelectedBeforePass(req *Request, qs []Question, results []Result) {
	for i := range results {
		if i >= len(qs) || isFilled(results[i]) {
			continue
		}
		ref := previousPlacesRE
		if !ref.MatchString(req.Questions[i].Text) {
			if ref = thatCountryRE; !ref.MatchString(req.Questions[i].Text) {
				continue
			}
		}
		names, ok := countriesHeldBefore(qs, results, i)
		if !ok {
			continue
		}
		q := req.Questions[i]
		q.Text = ref.ReplaceAllLiteralString(q.Text, strings.Join(names, " and "))
		rr, err := req.Rematch(q)
		if err != nil || !isFilled(rr) {
			continue
		}
		rr.Selector = results[i].Selector
		rr.Trace = append([]string{fmt.Sprintf("places selected before: %q is about %s, the countries the list before it holds", clip(req.Questions[i].Text, 60), strings.Join(names, ", "))}, rr.Trace...)
		results[i] = rr
	}
}

// countriesHeldBefore is, by name, what the nearest of the three questions
// before i that offers countries holds; false when it holds nothing or
// anything that is not a country. A type-ahead whose options were not read
// (Quora: they load only as one types) offers countries when its question asks
// for one.
func countriesHeldBefore(qs []Question, results []Result, i int) ([]string, bool) {
	for j := i - 1; j >= 0 && j >= i-3; j-- {
		if !offersCountries(qs[j]) && (len(realOptions(qs[j].Options)) > 0 || !countryAskRE.MatchString(qs[j].Text)) {
			continue
		}
		held := selectedLabels(results[j])
		if !isFilled(results[j]) || len(held) == 0 {
			return nil, false
		}
		names := make([]string, 0, len(held))
		for _, h := range held {
			code, ok := CountryCode(h)
			if !ok {
				return nil, false
			}
			names = append(names, countryName(code))
		}
		return names, true
	}
	return nil, false
}

// offersCountries reports a list of two or more countries.
func offersCountries(q Question) bool {
	n := 0
	for _, o := range realOptions(q.Options) {
		if _, ok := CountryCode(o.Label); ok {
			n++
		}
	}
	return n >= 2
}

// thatArrangementRE asks only whether the applicant accepts the arrangement
// the question before it described (Camber: "Are you comfortable with that
// work arrangement?" after the hybrid-office question).
var thatArrangementRE = lazyregexp.New(`(?i)^\W*(?:are|would)\s+you\s+(?:be\s+)?(?:comfortable|ok(?:ay)?|fine|happy)\s+with\s+(?:that|this|the\s+above|such\s+an?)\s+(?:(?:work(?:ing)?|hybrid|in[- ]office|office|on-?site|remote|travel)\s+)?(?:arrangement|schedule|set-?up|requirement|expectation|policy)\W*$`)

// arrangementAcceptances are the entries that record whether the applicant
// accepts a work arrangement.
var arrangementAcceptances = setOf("office-attendance-ok", "open-to-remote", "willing-to-relocate", "willing-to-travel")

// acceptedArrangementPass answers such a Yes/No from the acceptance entry that
// answered the Yes/No just before it, with that answer, or with its opposite
// when that question asked whether the arrangement is a problem. After any
// other question it is left as it is.
func acceptedArrangementPass(req *Request, qs []Question, results []Result) {
	for i := 1; i < len(results) && i < len(qs); i++ {
		if isFilled(results[i]) || !isYesNoGate(qs[i]) || !thatArrangementRE.MatchString(qs[i].Text) {
			continue
		}
		prev := results[i-1]
		if !isYesNoGate(qs[i-1]) || !isFilled(prev) || !arrangementAcceptances[prev.EntryID] {
			continue
		}
		label := resultLabel(prev)
		yes := isYesLabel(label)
		if !yes && !isNoLabel(label) {
			continue
		}
		if problemAsk(qs[i-1].Text) {
			yes = !yes
		}
		opt, ok := ownYesNo(qs[i].Options, yes)
		if !ok {
			continue
		}
		r := inherit(results[i].Selector, opt.Label, prev, prev.EntryID, prev.AnswerKey, results[i].Trace,
			fmt.Sprintf("cross-question: %q accepts the arrangement %q described; %s answered it %q", clip(qs[i].Text, 60), clip(qs[i-1].Text, 60), prev.EntryID, label))
		r.ChosenLabel = opt.Label
		results[i] = r
	}
}
