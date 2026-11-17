package match

import (
	"fmt"
	"strings"
	"unicode"

	"applysmith/internal/lazyregexp"
)

var (
	initialsAskRE = lazyregexp.New(`(?i)\binitials?\b`)
	// middleNameAskRE is a box for the middle name alone; noMiddleWordRE reads
	// the word the form asks for when there is none (Rula: "Please write N/A if
	// you do not have a middle name.").
	middleNameAskRE = lazyregexp.New(`(?i)^\W*middle\s+names?\b`)
	noMiddleWordRE  = lazyregexp.New(`(?i)\b(?:write|enter|type|put|use)\s+["'\x{201C}]?(n/?a|none)["'\x{201D}]?\s+if\s+you\s+(?:do\s+not|don['\x{2019}]?t)\s+have\s+(?:a|one)\b`)
	// nameWithInitialRE is a name asked with its middle initial (Block: "your
	// Full Legal First, Middle Initial, and Last Name"): the name, not initials.
	nameWithInitialRE = lazyregexp.New(`(?i)\bmiddle\s+initial\s*,?\s+(?:and\s+)?(?:last|family|sur)\s*name\b`)

	todayAskRE = []*lazyregexp.Regexp{
		lazyregexp.New(`(?i)\btoday'?s?\s+date\b`),
		lazyregexp.New(`(?i)\bcurrent\s+date\b`),
		lazyregexp.New(`(?i)\bdate\s+(?:signed|of\s+signature|of\s+signing)\b`),
		lazyregexp.New(`(?i)\b(?:signature|sign)\s+date\b`),
		// A label that is only "Date" is a signature or acknowledgement date.
		lazyregexp.New(`(?i)^\s*date\s*\*?\s*:?\s*\*?\s*$`),
	}
	notTodayRE = lazyregexp.New(`(?i)\b(?:birth|graduat\w*|start|end|hire|hired|expir\w*|issue|due|interview|available|availability|application)\b`)
	// todayOfApplicationRE is SoFi's "Today's Date of Application": the
	// application dated today, which is today's date.
	todayOfApplicationRE = lazyregexp.New(`(?i)\btoday'?s?\s+date\s+of\s+(?:(?:your|this|the)\s+)?application\b`)

	// signAndDateAskRE is one box asked for the signature and the date
	// together (Abacus: "... Please sign and date below."; "Signature and
	// date"; Cohere Health: "Please type your name and the date.").
	signAndDateAskRE = lazyregexp.New(`(?i)\bsign\s+and\s+date\b|^\W*(?:(?:typed|electronic|e-?)\s*)?signature\s*(?:and|&|/)\s*date\W*$` +
		`|\b(?:type|enter|print|write)\s+(?:in\s+)?your\s+(?:(?:full|legal)\s+)*name\s+and\s+(?:the\s+|today['\x{2019}]?s\s+)?date\b`)

	// dateLayoutRE reads a layout the label itself names, such as DD/MM/YYYY
	// or SoFi's MM/DD/YY.
	dateLayoutRE = lazyregexp.New(`(?i)\b(mm|dd|yyyy|yy)([/.-])(mm|dd|yyyy|yy)([/.-])(mm|dd|yyyy|yy)\b`)
)

// initialsOf is the first letter of each part of a name, uppercased.
func initialsOf(name string) string {
	var b strings.Builder
	for _, part := range strings.Fields(name) {
		r := []rune(part)[0]
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

// typedInitials fills an e-sign box that asks for initials from the user's
// full-name entry. The initials are derived, never stored, so a name change
// cannot leave a stale pair. A blank name abstains rather than letting a
// signature route type the full name into an initials box.
func typedInitials(s *Step) (Draft, bool) {
	q := s.Q
	if q.Kind != KindText || hasRenderedOptions(q.Options) || !initialsAskRE.MatchString(q.Text) || nameWithInitialRE.MatchString(q.Text) {
		return Draft{}, false
	}
	e := entryNamed(s, IDApplicantFullName)
	if e == nil {
		return Draft{}, false
	}
	name := ownText(e)
	if name == "" {
		return abstainDraft(ReasonPlaceholderEmpty, "", "initials need %s, which is blank", IDApplicantFullName), true
	}
	ini := initialsOf(name)
	d := derivedDraft(ini, "", startProfileDerive, fmt.Sprintf("typed initials from %s: %q", IDApplicantFullName, ini))
	d.Deterministic = true
	return d, true
}

// typedMiddleName fills a middle-name box from the user's full-name entry: the
// words between the first and the last. With none it types the word the form
// asks for when there is none; with no such word the box is left, as "Middle
// name (leave blank if none)" asks.
func typedMiddleName(s *Step) (Draft, bool) {
	q := s.Q
	if q.Kind != KindText || hasRenderedOptions(q.Options) || !middleNameAskRE.MatchString(q.Text) {
		return Draft{}, false
	}
	e := entryNamed(s, IDApplicantFullName)
	if e == nil {
		return Draft{}, false
	}
	w := strings.Fields(ownText(e))
	switch {
	case len(w) == 0:
		return abstainDraft(ReasonPlaceholderEmpty, "", "the middle name needs %s, which is blank", IDApplicantFullName), true
	case len(w) > 2:
		middle := strings.Join(w[1:len(w)-1], " ")
		d := derivedDraft(middle, "", startProfileDerive, fmt.Sprintf("middle name from %s: %q", IDApplicantFullName, middle))
		d.Deterministic = true
		return d, true
	}
	m := noMiddleWordRE.FindStringSubmatch(q.Text)
	if m == nil {
		return abstainDraft(ReasonPlaceholderEmpty, "", "%s has no middle name and the form names no word for none", IDApplicantFullName), true
	}
	d := derivedDraft(m[1], "", startProfileDerive, fmt.Sprintf("%s has no middle name: the word the form asks for, %q", IDApplicantFullName, m[1]))
	d.Deterministic = true
	return d, true
}

// signAndDate fills one box asked for the signature and the date with the
// user's full name and today's date, in the layout the label names.
func signAndDate(s *Step) (Draft, bool) {
	q := s.Q
	if q.Kind != KindText || hasRenderedOptions(q.Options) || !signAndDateAskRE.MatchString(q.Text) || !s.Req.Today.Valid() {
		return Draft{}, false
	}
	e := entryNamed(s, IDApplicantFullName)
	if e == nil {
		return Draft{}, false
	}
	name := ownText(e)
	if name == "" {
		return abstainDraft(ReasonPlaceholderEmpty, "", "the signature needs %s, which is blank", IDApplicantFullName), true
	}
	ans := name + ", " + formatDeclared(s.Req.Today, q.Text)
	d := derivedDraft(ans, "", startProfileDerive, fmt.Sprintf("signed and dated: %s and today's date from the injected clock: %q", IDApplicantFullName, ans))
	d.Deterministic = true
	return d, true
}

// isTodaysDateQuestion reports a signature or "today's date" label, and never a
// date that belongs to a subject (birth, graduation, start, expiry).
func isTodaysDateQuestion(text string) bool {
	text = todayOfApplicationRE.ReplaceAllString(text, "today's date")
	if notTodayRE.MatchString(text) {
		return false
	}
	for _, re := range todayAskRE {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// formatDeclared renders a date in the layout the label names (any order of
// MM, DD and YYYY or YY with one separator), defaulting to MM/DD/YYYY.
func formatDeclared(d Date, label string) string {
	m := dateLayoutRE.FindStringSubmatch(label)
	if m == nil || m[2] != m[4] {
		return d.US()
	}
	parts := [3]string{strings.ToLower(m[1]), strings.ToLower(m[3]), strings.ToLower(m[5])}
	seen := map[string]bool{}
	var out [3]string
	for i, p := range parts {
		part := p[:1] // YY and YYYY are both the year
		if seen[part] {
			return d.US()
		}
		seen[part] = true
		switch p {
		case "mm":
			out[i] = fmt.Sprintf("%02d", d.Month)
		case "dd":
			out[i] = fmt.Sprintf("%02d", d.Day)
		case "yy":
			out[i] = fmt.Sprintf("%02d", d.Year%100)
		default:
			out[i] = fmt.Sprintf("%04d", d.Year)
		}
	}
	return out[0] + m[2] + out[1] + m[2] + out[2]
}

// todaysDate answers a signature-date control with the injected current date.
func todaysDate(s *Step) (Draft, bool) {
	q := s.Q
	today := s.Req.Today
	if q.Kind != KindText || hasRenderedOptions(q.Options) || !today.Valid() || !isTodaysDateQuestion(q.Text) {
		return Draft{}, false
	}
	ans := formatDeclared(today, q.Text)
	d := derivedDraft(ans, "", startProfileDerive, fmt.Sprintf("today's date from the injected clock: %q", ans))
	d.Source, d.Deterministic = SourceFormText, true
	return d, true
}
