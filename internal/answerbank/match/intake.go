package match

import "fmt"

// prepared is a question after intake: the sanitized text, normalized options
// and the input type. The caller's Question is never mutated; the raw text stays
// in Raw and in the trace.
type prepared struct {
	Raw Question
	Q   Question
	// base holds the options before the Y/N and rung rewrites; a chosen label is
	// mapped back to the page's own label through it.
	base  []Option
	trace []string
	// legend marks options read through the question's legend; a chosen
	// label maps back to the page's number.
	legend bool
}

// prepareQuestion runs intake in its fixed order: sanitize, collapse duplicate
// option labels, strip position counters, rewrite numbered choices, read a Y/N
// pair as Yes/No, score rung-labelled options on the rung, then infer the input
// type from the cleaned text.
func prepareQuestion(raw Question) prepared {
	p := prepared{Raw: raw, Q: raw}
	q := &p.Q
	q.Options = append([]Option(nil), raw.Options...)

	q.Text = Sanitize(raw.Text, raw.Options)
	if q.Text != raw.Text {
		p.trace = append(p.trace, fmt.Sprintf("sanitized questionText: %q -> %q", raw.Text, q.Text))
	}
	if kept, ok := StripQuestionDirectives(q.Text); ok {
		p.trace = append(p.trace, fmt.Sprintf("an instruction addressed to an AI dropped: %q -> %q", q.Text, kept))
		q.Text = kept
	}

	q.Options = dedupeOptionLabels(q.Options)
	q.Options = stripPositionCounters(q.Options)
	if rewritten, ok := rewriteNumberedChoices(*q); ok {
		*q = rewritten
		p.trace = append(p.trace, "option-less text box enumerates numbered choices -> single select")
	}
	p.base = append([]Option(nil), q.Options...)
	if read, ok := rewriteOptionLegend(*q); ok {
		*q, p.legend = read, true
		p.trace = append(p.trace, "numbered options read through the question's legend")
	}
	if read, ok := rewriteLetterDefinitions(*q); ok {
		*q, p.legend = read, true
		p.trace = append(p.trace, "lettered options read through the definitions in the description")
	}
	if spelled, ok := spellLetterYesNo(q.Options); ok {
		q.Options = spelled
		p.trace = append(p.trace, "bare Y/N pair read as Yes/No")
	}
	if rung, ok := scoreOptionsOnRung(q.Options); ok {
		q.Options = rung
		p.trace = append(p.trace, "options scored on their rung")
	}
	if pair, ok := permitPairOnRungs(q.Options); ok {
		q.Options = pair
		p.trace = append(p.trace, "a Yes and a No that restate the work-permit question read as Yes and No")
	}
	q.InputType = inferInputType(*q)
	if q.InputType == InputText && isLinkBox(q.Text) {
		q.InputType = InputURL
		p.trace = append(p.trace, "a text box whose label names only a link is a URL box")
	}
	return p
}

// restoreLabel maps a chosen matching label back to the page's own label when
// the Y/N, rung or work-permit pair rewrite produced it. The option values are never changed.
func (p prepared) restoreLabel(chosen string) string {
	if chosen == "" {
		return chosen
	}
	for i, o := range p.Q.Options {
		if o.Label != chosen || i >= len(p.base) {
			continue
		}
		page := p.base[i].Label
		if page == chosen || p.legend {
			return page
		}
		if letterYesNo(page) == chosen {
			return page
		}
		if m := rungShape.FindStringSubmatch(page); m != nil && trimmedEqual(m[1], chosen) {
			return page
		}
		if m := yesNoConditionRE.FindStringSubmatch(page); m != nil && trimmedEqual(m[1], chosen) {
			return page
		}
	}
	return chosen
}

func trimmedEqual(a, b string) bool {
	return normalizeLabel(a) == normalizeLabel(b)
}
