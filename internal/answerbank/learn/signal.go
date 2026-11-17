package learn

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"applysmith/internal/lazyregexp"
)

// This file decides whether a captured question or value can be learned from
// at all. A capture that is not a question, or a value that is not an answer,
// is stored but never counts. The engine drops the worst captures at the
// source; these checks are the server-side backstop.

var (
	pageChrome = lazyregexp.New(`(?i)\b(?:sign\s+out|candidate\s+profile|back\s+to\s+job\s+posting|indicates\s+a\s+required\s+field|my\s+experience|percent\s+complete|\d+\s*/\s*\d+\s+pages?|reply\s+stop|msg\s*&\s*data\s+rates\s+may\s+apply)\b`)

	captchaQuestion = lazyregexp.New(`(?i)\b(?:re)?captcha\b|\bhcaptcha\b|\b(?:type|enter|retype|re-enter|copy)\s+the\s+(?:code|characters|text|letters|word)\b|\b(?:verification|security|confirmation)\s+code\b|\banti[- ]?spam\b|\bwhat\s+is\s+\d+\s*[x+*-]\s*\d+\b|\bcode\s+(?:shown|above|below|displayed)\b`)

	// boxFurniture is a bare label that asks nothing the bank should hold: the
	// extension beside a phone number, which the profile keeps none of, a
	// search widget's own prompt ("Type to Add Skills") and a honeypot box
	// labelled with the word itself (Oracle).
	boxFurniture = lazyregexp.New(`(?i)^\s*(?:(?:work |office |home |mobile )?(?:phone|telephone)\s+)?(?:extension|ext\.?)\s*\*?\s*$|^\s*type to (?:add|search)\b|^\s*honey[\s_-]?pot\s*\*?\s*$`)

	// Questions that ask for a code the page displayed.
	retypeCode = captchaQuestion

	hexID       = lazyregexp.New(`^[0-9a-f]{8}[0-9a-f-]*$`)
	digitsOnly  = lazyregexp.New(`^\d+$`)
	frameworkID = lazyregexp.New(`^(?:[a-z]+_[0-9a-f]{8,}[0-9a-f_-]*|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)
	rtiSuffix   = lazyregexp.New(`_RTi[A-Za-z0-9]*$`)
	bracketPath = lazyregexp.New(`^[A-Za-z_][\w-]*(?:\[[^\]]+\])+$`)
	snakeID     = lazyregexp.New(`^[A-Za-z0-9]+(?:_[A-Za-z0-9]+){2,}$`)
	capsRun     = lazyregexp.New(`[a-z][A-Z]{2,}`)
	requiredMk  = lazyregexp.New(`(?i)\s*\(?required\)?\s*$|[\s*]+$`)
	elicitStem  = lazyregexp.New(`(?i)^(?:please|describe|explain|list|what|why|how|are|is|do|does|did|will|would|can|could|should|select|choose|if|tell|provide|enter|have|has|which|who|when|where|give|indicate|specify|state)\b`)
	sentenceEnd = lazyregexp.New(`[.!]\s+\S`)
)

// IsNonQuestion reports a captured text that is not a question: empty, a
// selector blob, a CAPTCHA prompt, ATS page chrome, a repeated-token or
// repeated-phrase over-scoop, a framework-generated identifier, a bare
// date-format placeholder, a placeholder option ("--Select One--"), the bare
// label of a repeated-row field or statement prose. Nothing is learned from it.
func IsNonQuestion(text string) bool {
	t := strings.TrimSpace(text)
	switch {
	case t == "":
		return true
	case selectorBlob(t), captchaQuestion.MatchString(t), pageChrome.MatchString(t), boxFurniture.MatchString(t):
		return true
	case isFrameworkID(t), IsDatePlaceholder(t), isStatementProse(t):
		return true
	case IsNothingChosen(t):
		// A placeholder option such as "--Select One--" or "Please select".
		return true
	case IsRowFieldLabel(t):
		return true
	}
	return hasOverScoop(t)
}

// rowFieldLabels are the bare labels of the fields of a repeated work-history
// or education row. A start date alone is left out: it also asks when the
// applicant can begin.
var rowFieldLabels = map[string]bool{
	"company": true, "company name": true, "employer": true, "employer name": true, "name of employer": true,
	"title": true, "job title": true, "position": true, "position title": true,
	"end date": true, "reason for leaving": true, "school name": true, "name of school": true,
}

var rowDatePart = lazyregexp.New(`^(?:from|to|start|end|graduation)(?: date)? (?:month|year)$`)

// IsRowFieldLabel reports a bare row-field label such as "Company Name" or
// "Reason for Leaving". Such a control is answered from the profile's history
// entry for its row and is never an answer-bank question, so an abstain on it
// teaches nothing.
func IsRowFieldLabel(text string) bool {
	n := Normalize(requiredMk.ReplaceAllString(strings.TrimSpace(text), ""))
	n = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(n, " optional"), " required"))
	return rowFieldLabels[n] || rowDatePart.MatchString(n)
}

func selectorBlob(t string) bool {
	if strings.HasPrefix(t, "#") || strings.HasPrefix(t, ".") {
		return true
	}
	for _, s := range []string{"[custom_form_response", ":nth-child(", "> div", "_attributes_"} {
		if strings.Contains(t, s) {
			return true
		}
	}
	return false
}

// IsDatePlaceholder reports a bare date format such as "MM/DD/YYYY" as the
// whole text. The separator must be the same twice, which the engine-free
// scan states as three alternatives, one per separator.
func IsDatePlaceholder(s string) bool {
	return datePlaceholder.MatchString(strings.TrimSpace(s))
}

var datePlaceholder = lazyregexp.New(`^(?:` +
	`[MDYmdy]{1,4}/[MDYmdy]{1,4}/[MDYmdy]{1,4}|` +
	`[MDYmdy]{1,4}\.[MDYmdy]{1,4}\.[MDYmdy]{1,4}|` +
	`[MDYmdy]{1,4}-[MDYmdy]{1,4}-[MDYmdy]{1,4})$`)

func isFrameworkID(t string) bool {
	if strings.HasPrefix(t, "$$") {
		return true
	}
	bare := strings.TrimSpace(requiredMk.ReplaceAllString(t, ""))
	if strings.ContainsAny(bare, " \t") {
		return false
	}
	switch {
	case frameworkID.MatchString(bare), rtiSuffix.MatchString(bare), bracketPath.MatchString(bare), capsRun.MatchString(bare):
		return true
	}
	return snakeID.MatchString(bare) && strings.ContainsAny(bare, "0123456789")
}

// isStatementProse reports a long text that states rather than asks: 180 or
// more characters, no question mark, not opening with an eliciting stem, and a
// sentence terminator followed by more text.
func isStatementProse(t string) bool {
	return utf8.RuneCountInString(t) >= 180 && !strings.Contains(t, "?") && !elicitStem.MatchString(t) && sentenceEnd.MatchString(t)
}

// tokens lowercases the text and keeps the runs of letters, digits,
// underscores and apostrophes, so punctuation never separates two copies.
func tokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '\'')
	})
}

// spaceTokens splits on whitespace and trims punctuation from the edges, so an
// identifier with inner hyphens stays one token.
func spaceTokens(s string) []string {
	var out []string
	for _, f := range strings.Fields(strings.ToLower(s)) {
		f = strings.TrimFunc(f, func(r rune) bool { return !(unicode.IsLetter(r) || unicode.IsDigit(r)) })
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// maxScoopTokens bounds the phrase scan. The scan is quadratic in the token
// count, and pages with long consent blobs would otherwise make it slow.
const maxScoopTokens = 120

// hasOverScoop reports the three over-scoop shapes: one token three times in a
// row, one token three or more times among at most three distinct tokens, and
// a phrase of three or more words repeated back to back (with at most one
// token between the copies).
func hasOverScoop(t string) bool {
	all := tokens(t)
	var long []string
	for _, tok := range all {
		if utf8.RuneCountInString(tok) >= 2 {
			long = append(long, tok)
		}
	}
	if threeInARow(long) || wedgedRepeat(long) {
		return true
	}
	var words []string
	for _, tok := range spaceTokens(t) {
		if !hexID.MatchString(tok) && !digitsOnly.MatchString(tok) {
			words = append(words, tok)
		}
	}
	if len(words) > maxScoopTokens {
		words = words[:maxScoopTokens]
	}
	return repeatedPhrase(words)
}

// threeInARow compares neighbours; the backreference form of this test does
// not exist in RE2.
func threeInARow(toks []string) bool {
	run := 1
	for i := 1; i < len(toks); i++ {
		if toks[i] == toks[i-1] {
			run++
			if run >= 3 {
				return true
			}
		} else {
			run = 1
		}
	}
	return false
}

func wedgedRepeat(toks []string) bool {
	if len(toks) < 4 {
		return false
	}
	count := map[string]int{}
	for _, tok := range toks {
		count[tok]++
	}
	if len(count) > 3 {
		return false
	}
	for _, n := range count {
		if n >= 3 {
			return true
		}
	}
	return false
}

func repeatedPhrase(w []string) bool {
	if len(w) < 6 {
		return false
	}
	for l := 3; 2*l <= len(w); l++ {
		for i := 0; i+2*l <= len(w); i++ {
			if equalSpan(w[i:i+l], w[i+l:i+2*l]) {
				return true
			}
			if i+2*l+1 <= len(w) && equalSpan(w[i:i+l], w[i+l+1:i+2*l+1]) {
				return true
			}
		}
	}
	return false
}

func equalSpan(a, b []string) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var (
	codeToken = lazyregexp.New(`^[A-Za-z0-9]{4,8}$`)
	toggles   = map[string]bool{"on": true, "off": true, "checked": true, "unchecked": true, "true": true, "false": true, "na": true, "n/a": true, "none": true}
)

// IsSubstantiveValue reports whether a typed or chosen value can be an answer.
// It is not when it is only seven or more digits (phone, SSN, account number),
// a short code mixing letters and digits or any value typed into a field that
// asks the user to retype a code, a single character, or a toggle word.
func IsSubstantiveValue(question, value string) bool {
	v := strings.TrimSpace(value)
	if utf8.RuneCountInString(strings.Join(strings.Fields(v), "")) < 2 || toggles[strings.ToLower(v)] {
		return false
	}
	if sensitiveDigits(v) || LooksLikeCaptcha(question, v) {
		return false
	}
	return true
}

func sensitiveDigits(v string) bool {
	d := strings.NewReplacer(" ", "", "(", "", ")", "", "+", "", ".", "", "-", "").Replace(v)
	return len(d) >= 7 && digitsOnly.MatchString(d)
}

// LooksLikeCaptcha reports a value that is a 4 to 8 character token mixing
// letters and digits, or any value typed into a field that asks for a code.
func LooksLikeCaptcha(question, value string) bool {
	if retypeCode.MatchString(question) {
		return true
	}
	v := strings.TrimSpace(value)
	return codeToken.MatchString(v) && strings.IndexFunc(v, unicode.IsLetter) >= 0 && strings.IndexFunc(v, unicode.IsDigit) >= 0
}

var (
	reName     = lazyregexp.New(`^(?:your )?(?:(?:first|last|middle|preferred|full|legal|given|family) )?name$`)
	rePronouns = lazyregexp.New(`^(?:your )?pronouns?$`)
	reCity     = lazyregexp.New(`^(?:your )?city(?: of residence)?$`)
	reEmail    = lazyregexp.New(`^(?:your )?(?:e mail|email)(?: address)?$`)
	rePhone    = lazyregexp.New(`^(?:your )?(?:phone|telephone|mobile|cell)(?: (?:number|no))?$`)
	reZip      = lazyregexp.New(`^(?:your )?(?:zip|postal)(?: ?code)?$`)
	reDate     = lazyregexp.New(`^(?:date|birth ?date|date of birth|dob)$`)

	valueURL   = lazyregexp.New(`(?i)^(?:[a-z][a-z0-9+.-]*://|www\.)`)
	valueEmail = lazyregexp.New(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
	valueNum   = lazyregexp.New(`^[\d\s().+-]*\d[\d\s().+-]*$`)
)

// ValueContradictsLabel reports a recorded value whose shape contradicts a
// short label naming one identity datum: a capture mis-attribution, not the
// user's answer. Only labels of 60 characters or fewer are judged; anything
// else stays unjudged.
func ValueContradictsLabel(label, value string) bool {
	if utf8.RuneCountInString(label) > 60 {
		return false
	}
	l := Normalize(label)
	l = strings.TrimSpace(strings.TrimSuffix(l, " required"))
	v := strings.TrimSpace(value)
	essay := utf8.RuneCountInString(v) > 120 || strings.ContainsAny(v, "\n\r")
	url, email, num := valueURL.MatchString(v), valueEmail.MatchString(v), valueNum.MatchString(v)
	switch {
	case reName.MatchString(l), rePronouns.MatchString(l), reCity.MatchString(l):
		return essay || url || email || num
	case reEmail.MatchString(l):
		return essay || url || num
	case rePhone.MatchString(l), reZip.MatchString(l), reDate.MatchString(l):
		return essay || url || email
	}
	return false
}
