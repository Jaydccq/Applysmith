package match

import (
	"fmt"
	"strings"

	"applysmith/internal/lazyregexp"
)

// outputGuards runs the guards on the produced answer, in this order: the
// option constraint, then the numeric constraint. It reads the draft through
// Step.Output and replaces it only when a guard changes it.
func outputGuards(s *Step) (Draft, bool) {
	d := s.Output()
	if g, hit := optionConstraint(s, d); hit {
		return g, true
	}
	if g, hit := explainedNo(s, d); hit {
		return g, true
	}
	return numericConstraint(s, d)
}

// explainedNo keeps a bare No out of a text box whose question wants words of
// a No (wantsWordsOnlyOfANo): the explanation is the applicant's to write.
func explainedNo(s *Step, d Draft) (Draft, bool) {
	if s.Q.Kind != KindText || hasRenderedOptions(s.Q.Options) || !plainNoRE.MatchString(d.Answer) || !wantsWordsOnlyOfANo(s.Q.Text) {
		return Draft{}, false
	}
	return abstainDraft(ReasonNarrativeDeferred, "", "a bare No to a question that asks to explain a No -> left for the explanation"), true
}

func producer(s *Step, d Draft) string {
	switch {
	case d.Source != "":
		return string(d.Source)
	case d.Entry != nil && d.Entry.ID != "":
		return d.Entry.ID
	case s.Entry != nil:
		return s.Entry.ID
	}
	return "?"
}

// optionConstraint holds a fill on a select, radio group or checkbox to the
// options the control renders: a value the control cannot hold must never
// report a fill. Text and custom-listbox controls (a typeahead's options are
// often truncated), controls with no options and an intentional blank fill are
// exempt.
func optionConstraint(s *Step, d Draft) (Draft, bool) {
	q := s.Q
	switch q.Kind {
	case KindSelect, KindRadioGroup, KindCheckbox:
	default:
		return Draft{}, false
	}
	if len(q.Options) == 0 {
		return Draft{}, false
	}
	permitted := map[string]bool{}
	for _, o := range q.Options {
		if o.Value != "" {
			permitted[lowerTrim(o.Value)] = true
		}
		if o.Label != "" {
			permitted[lowerTrim(o.Label)] = true
		}
	}
	chosen, answer := lowerTrim(d.ChosenLabel), lowerTrim(d.Answer)
	if chosen == "" && answer == "" && len(d.ChosenLabels) == 0 {
		return Draft{}, false
	}
	if len(d.ChosenLabels) > 0 {
		ok := true
		for _, l := range d.ChosenLabels {
			ok = ok && permitted[lowerTrim(l)]
		}
		if ok {
			return Draft{}, false
		}
	} else if chosen != "" && permitted[chosen] || answer != "" && permitted[answer] {
		return Draft{}, false
	}
	labels := make([]string, len(q.Options))
	for i, o := range q.Options {
		labels[i] = o.Label
	}
	got := d.ChosenLabel
	if got == "" {
		got = d.Answer
	}
	return Draft{Abstain: &Abstain{Reason: ReasonAmbiguous}, Trace: append(append([]string(nil), d.Trace...), fmt.Sprintf(
		"option-constraint: source=%q produced %q which is not one of [%s]: abstain (a value the control cannot hold must never report a fill)",
		producer(s, d), got, strings.Join(labels, ", ")))}, true
}

var (
	yesDirectiveRE = lazyregexp.New(`(?i)\b(?:use|enter|type|put|input)\s+(-?\d+(?:\.\d+)?)\s+for\s+yes\b`)
	noDirectiveRE  = lazyregexp.New(`(?i)\b(?:and\s+)?(-?\d+(?:\.\d+)?)\s+for\s+no\b`)
	bareYesNoRE    = lazyregexp.New(`(?i)^(?:yes|no)$`)
)

// numeralForYesNo is the numeral a question states for a Yes or a No ("Use 1 for
// Yes and 0 for No"). Both must be stated; the directive is read, never guessed.
func numeralForYesNo(questionText, answer string) (string, bool) {
	y, n := yesDirectiveRE.FindStringSubmatch(questionText), noDirectiveRE.FindStringSubmatch(questionText)
	if y == nil || n == nil {
		return "", false
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "yes":
		return y[1], true
	case "no":
		return n[1], true
	}
	return "", false
}

// numericConstraint keeps a bare Yes or No, and any answer with no digit, out of
// a number box. A question that states its own encoding is answered with the
// numeral; otherwise the result is an ambiguous abstain, never a guess. The box
// is read by its input type alone, which can sit on a control that still wants
// units ("130000 USD"), so only the two always-wrong values are blocked.
func numericConstraint(s *Step, d Draft) (Draft, bool) {
	if s.Q.InputType != InputNumber {
		return Draft{}, false
	}
	answer := strings.TrimSpace(d.Answer)
	hasDigit := strings.ContainsAny(answer, "0123456789")
	bare := bareYesNoRE.MatchString(answer)
	if !hasDigit && !bare {
		return Draft{Abstain: &Abstain{Reason: ReasonAmbiguous}, Trace: append(append([]string(nil), d.Trace...), fmt.Sprintf(
			"numeric-constraint: source=%q produced %q, which has no digit, for a number control: abstain", producer(s, d), answer))}, true
	}
	if !bare {
		return Draft{}, false
	}
	if numeral, ok := numeralForYesNo(s.Q.Text, answer); ok {
		d.Answer = numeral
		d.Trace = append(append([]string(nil), d.Trace...), fmt.Sprintf(
			"numeric-constraint: the question states its own encoding: %q written as %q", answer, numeral))
		return d, true
	}
	return Draft{Abstain: &Abstain{Reason: ReasonAmbiguous}, Trace: append(append([]string(nil), d.Trace...), fmt.Sprintf(
		"numeric-constraint: source=%q produced %q for a number control that states no yes/no encoding: abstain", producer(s, d), answer))}, true
}
