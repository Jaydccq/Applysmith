package match

import (
	"fmt"
	"strconv"
	"strings"

	"applysmith/internal/lazyregexp"
)

// fixedAnswer resolves a fixed entry in the order of the specification: a
// question-negation hit, the location policy, an exception term, a numeric
// threshold, skill screening, the residence translation, a future-only date
// and last the literal answer. The first step that applies decides.
func fixedAnswer(s *Step) (Draft, bool) {
	e, q := s.Entry, s.Q
	if e == nil || e.Kind != KindFixed {
		return Draft{}, false
	}
	if pat, hit := fixedNegationHit(e, q.Text); hit {
		return abstainDraft(ReasonNoEntry, "", "kind=fixed question-negation matched %q -> abstain", pat), true
	}
	// A Yes and Noes split by relocation are read before the entry's policy
	// verdict, whose bare Yes would bind the option that claims residence.
	if d, ok := namedMetroOrRelocate(s, e); ok {
		return d, true
	}
	if e.LocationPolicy != nil {
		if d, ok := locationPolicyAnswer(s, e); ok {
			return fixedFinishHooked(s, e, d), true
		}
	}
	if e.AnswerIfException != "" && len(e.ExceptionTerms) > 0 {
		if hit, ok := matchExceptionTerm(q.Text, e.ExceptionTerms); ok {
			line := "kind=fixed exception " + quoteTerm(hit.Term)
			if aliasKey(hit.As) != aliasKey(hit.Term) {
				line += " (matched via alias " + quoteTerm(hit.As) + ")"
			}
			return bindEntryAnswer(s, e, e.AnswerIfException, line+" -> "+quoteTerm(e.AnswerIfException)), true
		}
	}
	if d, ok := fixedNumericThreshold(s, e); ok {
		return d, true
	}
	if d, ok := officeFrequencyYes(s, e); ok {
		return d, true
	}
	if d, ok := officeResidenceClaim(s, e); ok {
		return d, true
	}
	if d, ok := namedMetroHome(s, e); ok {
		return d, true
	}
	if d, ok := referralNotApplicable(s, e); ok {
		return d, true
	}
	if d, ok := officeBranchPick(s, e); ok {
		return fixedFinishHooked(s, e, d), true
	}
	if e.SkillMode != "" {
		d, _ := skillScreening(s, e)
		return fixedFinishHooked(s, e, d), true
	}
	if e.ID == fixedResidenceEntryID && isYesNoOptionSet(q.Options) && fixedResidenceCountryRE.MatchString(strings.TrimSpace(e.Answer)) {
		return bindEntryAnswer(s, e, "Yes", "kind=fixed residence country "+quoteTerm(e.Answer)+" -> Yes for Yes/No options"), true
	}
	if none, ok := ifAnyNoneOption(q); ok && e.allowsInput(InputYesNo) && mselMultiAnswer(q, len(realOptions(q.Options))) {
		return noneOnANo(e, none), true
	}
	if e.ID == textConsentEntryID && mselMultiAnswer(q, len(realOptions(q.Options))) && channelList(q.Options) {
		return channelsConsented(s, e), true
	}
	if e.ID == textConsentEntryID && s.Q.InputType == InputSingleSelect {
		if _, _, ok := textChannelOptions(q.Options); ok {
			return textChannelConsent(s, e), true
		}
	}
	if e.futureOnlyDate() {
		if d, ok := futureOnlyDate(s, e); ok {
			return fixedFinishHooked(s, e, d), true
		}
	}
	if d, ok := workAuthFixed(s, e); ok {
		return d, true
	}
	if q.InputType == InputSingleSelect && howOftenRE.MatchString(q.Text) {
		if o, ok := frequencyOption(e.Answer, realOptions(q.Options)); ok {
			return bindEntryAnswer(s, e, o.Label, "kind=fixed the answer names the frequency "+quoteTerm(o.Label)), true
		}
	}
	if q.InputType == InputSingleSelect && whichToolAskRE.MatchString(q.Text) {
		if o, ok := toolNamedFirst(e.Answer, realOptions(q.Options)); ok {
			return bindEntryAnswer(s, e, o.Label, "kind=fixed the answer names "+quoteTerm(o.Label)+" first among the options"), true
		}
	}
	if e.ID == idPhoneMobile && q.InputType == InputNumber {
		digits := strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, e.Answer)
		return bindEntryAnswer(s, e, digits, "kind=fixed phone in a number box -> its digits "+quoteTerm(digits)), true
	}
	return bindEntryAnswer(s, e, e.Answer, "kind=fixed -> "+quoteTerm(e.Answer)), true
}

const idPhoneMobile = "phone-mobile"

var (
	howOftenRE = lazyregexp.New(`(?i)\bhow\s+(?:often|frequently)\b`)
	// whichToolAskRE asks which tool, not how often one is used.
	whichToolAskRE = lazyregexp.New(`(?i)\b(?:which|what)\b[^?]{0,60}\btools?\b`)
	// catchAllOptionRE is a list's catch-all ("Other", "None", "N/A"), which an
	// answer never names as a tool.
	catchAllOptionRE = lazyregexp.New(`(?i)^\W*(?:other|none|n\s*/\s*a|not\s+applicable)\b`)
	// frequencyREs are the frequencies an answer or an option may name.
	frequencyREs = []*lazyregexp.Regexp{
		lazyregexp.New(`(?i)\bdaily\b|\bevery\s*day\b|\beach\s+day\b`),
		lazyregexp.New(`(?i)\bweekly\b|\bevery\s+week\b|\b(?:times|days)\s+(?:a|per)\s+week\b`),
		lazyregexp.New(`(?i)\bmonthly\b|\bevery\s+month\b|\btimes\s+(?:a|per)\s+month\b`),
	}
)

// toolNamedFirst is the option the answer names first, by its whole name
// (Regard: "Which AI coding tool do you use most often?" over Cursor, Claude
// Code, ..., answered by a text that names its daily driver first). A catch-all
// is never named; an answer naming no option picks nothing.
func toolNamedFirst(answer string, opts []Option) (Option, bool) {
	text := strings.ToLower(answer)
	best, at := Option{}, -1
	for _, o := range opts {
		label := strings.ToLower(strings.TrimSpace(o.Label))
		if label == "" || catchAllOptionRE.MatchString(label) {
			continue
		}
		if i := wordIndex(text, label); i >= 0 && (at < 0 || i < at) {
			best, at = o, i
		}
	}
	return best, at >= 0
}

// frequencyOption is the one option naming the one frequency the answer
// names; an answer naming none or several, or a frequency two options share,
// picks nothing.
func frequencyOption(answer string, opts []Option) (Option, bool) {
	named := -1
	for i, re := range frequencyREs {
		if re.MatchString(answer) {
			if named >= 0 {
				return Option{}, false
			}
			named = i
		}
	}
	if named < 0 {
		return Option{}, false
	}
	var hits []Option
	for _, o := range opts {
		if frequencyREs[named].MatchString(o.Label) {
			hits = append(hits, o)
		}
	}
	if len(hits) != 1 {
		return Option{}, false
	}
	return hits[0], true
}

const fixedResidenceEntryID = "currently-living-us-or-canada"

// fixedResidenceCountryRE matches the stored countries that read as Yes on a
// Yes/No control; any other value never becomes an affirmative.
var fixedResidenceCountryRE = lazyregexp.New(`(?i)^(?:us|u\.?\s*s\.?|usa|united\s+states|canada)$`)

func quoteTerm(s string) string { return `"` + s + `"` }

// fixedNegationHit tests the entry's question_negation_terms, as the loader
// compiled them, against the question.
func fixedNegationHit(e *Entry, text string) (string, bool) {
	for _, re := range e.NegationPatterns {
		if re.MatchString(text) {
			return re.String(), true
		}
	}
	return "", false
}

// fixedNumericThreshold applies the days-per-week threshold stamped from the
// profile. A value above the maximum answers the above-threshold answer; an
// extractor miss abstains only when the entry says so.
func fixedNumericThreshold(s *Step, e *Entry) (Draft, bool) {
	nt := e.NumericThreshold
	if nt == nil || nt.MaxValue == nil || nt.Extractor != ExtractorDaysPerWeekOnsite {
		return Draft{}, false
	}
	days, _, ok := extractDaysPerWeek(s.Q.Text)
	if !ok {
		if nt.MissMode() == ParseMissAbstain {
			return abstainDraft(ReasonNoEntry, "", "kind=fixed numeric-threshold parse-miss on_parse_miss=abstain"), true
		}
		s.Tracef("kind=fixed numeric-threshold parse-miss on_parse_miss=%s -> fall through", nt.MissMode())
		return Draft{}, false
	}
	if days <= *nt.MaxValue {
		s.Tracef("kind=fixed numeric-threshold %d <= %d -> fall through", days, *nt.MaxValue)
		return Draft{}, false
	}
	answer := firstNonEmpty(nt.AboveAnswer, e.AnswerIfException, e.Answer, "No")
	return bindEntryAnswer(s, e, answer, "kind=fixed numeric-threshold "+strconv.Itoa(days)+" > "+strconv.Itoa(*nt.MaxValue)+" -> "+quoteTerm(answer)), true
}

// officeFrequencyYes answers office-attendance-ok's Yes over Yes options that
// differ in how often (Rocket Money: "Yes - regularly (2+ days per week)", "Yes
// - occasionally", "No"). The one Yes that states days a week is the answer
// when the applicant can meet them (no recorded maximum, or one at or above
// the days); days above the maximum take the one Yes that states none.
func officeFrequencyYes(s *Step, e *Entry) (Draft, bool) {
	if e.ID != IDOfficeAttendanceOK || !isYesLabel(e.Answer) {
		return Draft{}, false
	}
	var stated, plain []Option
	days := 0
	for _, o := range realOptions(s.Q.Options) {
		if !isYesLabel(o.Label) {
			continue
		}
		if n, _, ok := extractDaysPerWeek(o.Label); ok {
			stated, days = append(stated, o), n
		} else {
			plain = append(plain, o)
		}
	}
	if len(stated) != 1 || len(plain) == 0 {
		return Draft{}, false
	}
	pick, line := stated[0], "no recorded maximum"
	if mx := e.NumericThreshold; mx != nil && mx.MaxValue != nil {
		line = fmt.Sprintf("the maximum %d", *mx.MaxValue)
		if days > *mx.MaxValue {
			if len(plain) != 1 {
				return Draft{}, false
			}
			pick = plain[0]
		}
	}
	d := optionDraft(optionBinding{Label: pick.Label, Value: pick.Value, Score: 1}, e, SourceEntry, true)
	d.Trace = []string{fmt.Sprintf("kind=fixed Yes over Yes options by frequency: %d days a week against %s -> %s", days, line, quoteTerm(pick.Label))}
	return d, true
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// fixedFinishHooked completes a draft another family produced: an abstain or a
// draft that already chose its option stands, and a draft that carries only an
// answer is bound to the control like the entry's own answer.
func fixedFinishHooked(s *Step, e *Entry, d Draft) Draft {
	if d.Abstain != nil || d.ChosenLabel != "" || d.HasOption {
		return d
	}
	ent := d.Entry
	if ent == nil {
		ent = e
	}
	out := bindEntryAnswer(s, ent, d.Answer, "")
	out.Trace = append(append([]string(nil), d.Trace...), out.Trace...)
	if d.Source != "" {
		out.Source = d.Source
	}
	if d.Start != 0 {
		out.Start = d.Start
	}
	return out
}

// bindEntryAnswer turns a stored answer into a draft for the control. An
// option control binds the answer to a rendered option; a control that
// reports no options defers the choice to the apply step with the answer as
// the chosen label; a text box gets the answer after the text
// transformations.
func bindEntryAnswer(s *Step, e *Entry, answer, trace string) Draft {
	q := s.Q
	d := Draft{Entry: e, Source: SourceEntry, Deterministic: true}
	if trace != "" {
		d.Trace = []string{trace}
	}
	if !hasRenderedOptions(q.Options) {
		if q.Kind == KindText {
			out, lines := finishTextAnswer(e, q, answer)
			d.Answer = out
			d.Trace = append(d.Trace, lines...)
			return d
		}
		d.Answer, d.ChosenLabel = answer, answer
		d.Trace = append(d.Trace, "control reports no options: choice deferred to the apply step")
		return d
	}
	if isYearsEntry(e) {
		if o, found := yearsEntryBucket(answer, q.Options); found {
			out := optionDraft(optionBinding{Label: o.Label, Value: o.Value, Score: 0.96}, e, SourceEntry, true)
			out.Trace = append(d.Trace, "years bucket: "+quoteTerm(o.Label))
			return out
		}
	}
	out := bindAnswerToOptions(s, e, answer)
	out.Entry, out.Source = e, SourceEntry
	out.Trace = append(d.Trace, out.Trace...)
	return out
}

func scoreText(f float64) string {
	n := int(f*100 + 0.5)
	return strconv.Itoa(n/100) + "." + strconv.Itoa(n%100/10) + strconv.Itoa(n%10)
}

var (
	// ifAnyRE asks which ones apply while admitting none ("What FINRA
	// license(s), if any, do you currently hold?").
	ifAnyRE = lazyregexp.New(`(?i)\bif\s+any\b`)
	// listNoneRE is a list's option for none.
	listNoneRE = lazyregexp.New(`(?i)^\s*(?:n/?a|none|not\s+applicable|none\s+of\s+(?:the\s+)?(?:above|these))\s*\.?\s*$`)
)

// ifAnyNoneOption is the one option for none of a list that asks which ones
// apply "if any"; ok is false for any other list.
func ifAnyNoneOption(q Question) (Option, bool) {
	if !ifAnyRE.MatchString(q.Text) {
		return Option{}, false
	}
	real := realOptions(q.Options)
	var none []Option
	for _, o := range real {
		if listNoneRE.MatchString(o.Label) {
			none = append(none, o)
		}
	}
	if len(none) != 1 || len(real) < 3 {
		return Option{}, false
	}
	return none[0], true
}

// noneOnANo ticks the option for none of a which-ones-if-any list when the
// entry records No; a Yes leaves which ones to the user.
func noneOnANo(e *Entry, none Option) Draft {
	if normalizeLabel(e.Answer) != "no" {
		return abstainDraft(ReasonInputMismatch, "", "%s answers %q: which ones the list asks is the user's", e.ID, e.Answer)
	}
	return Draft{Entry: e, Source: SourceEntry, Deterministic: true,
		Answer: none.Value, ChosenLabel: none.Label, ChosenLabels: []string{none.Label}, HasOption: true, OptionScore: 0.96,
		Trace: []string{"kind=fixed No on a which-ones-if-any list -> its none option " + quoteTerm(none.Label)}}
}

var (
	// referredIfRE asks something only of an applicant someone referred
	// (Tenex: "If you were referred by a Current Employee, please list their
	// name.").
	referredIfRE = lazyregexp.New(`(?i)\bif\s+(?:you\s+(?:were|was|have\s+been)\s+)?referred\b`)
	// notApplicableNARE tells the applicant to write N/A when it does not apply
	// ("If this is not applicable, please list N/A", "N/A if none").
	notApplicableNARE = lazyregexp.New(`(?i)\bif\s+(?:this\s+(?:is|does)\s+)?not\s+(?:applicable|apply)\b[^.?!]*\bn\s*/\s*a\b|\bn\s*/\s*a\s+if\s+(?:none|not\s+applicable)\b`)
)

// referralNotApplies reports a referrer-name question asked only of a
// referred applicant when the user recorded that no current employee referred
// them, and the question asks for "N/A" then or must be answered. A Yes, a
// blank record or any other question does not.
func referralNotApplies(s *Step, e *Entry) bool {
	if e == nil || e.ID != "referrer-name" || !referredIfRE.MatchString(s.Q.Text) {
		return false
	}
	ref := entryNamed(s, "referred-by-current-employee")
	if ref == nil || !plainNoRE.MatchString(ownText(ref)) {
		return false
	}
	return notApplicableNARE.MatchString(s.Q.Text) || s.Q.IsRequired()
}

// referralNotApplicable answers such a question "N/A", whatever the blank
// referrer-name entry holds.
func referralNotApplicable(s *Step, e *Entry) (Draft, bool) {
	if !referralNotApplies(s, e) {
		return Draft{}, false
	}
	return bindEntryAnswer(s, e, "N/A", "referrer name: no current employee referred the applicant (referred-by-current-employee) -> \"N/A\""), true
}
