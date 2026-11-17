package extract

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// Distances of the two tiers, in bytes of the blanked text.
const (
	connectorWindow = 20
	proximityWindow = 40
)

var (
	// introducerRE is the code noun itself, however qualified.
	introducerRE = lazyregexp.New(`(?i)\b(?:codes?|passcodes?|otp|pin)\b`)
	// connectorRE is what may sit between a code noun and its code.
	connectorRE = lazyregexp.New(`(?i)^[\s:\-=.,\x{2013}\x{2014}]*(?:(?:is|was)\b[\s:\-=.,\x{2013}\x{2014}]*)?(?:(?:the|your|a|an)\b[\s:\-=.,\x{2013}\x{2014}]*)?$`)
	// noiseRE matches links, markup entities and email addresses. They are
	// blanked, not deleted, so every offset survives.
	noiseRE = lazyregexp.New(`(?i)(?:https?://|www\.)\S+|&#?\w+;|[^\s<>@]+@[^\s<>@]+\.[A-Za-z]{2,}`)
	// sentenceEndRE is a full stop, question or exclamation mark that ends a
	// sentence. A noun or phrase never introduces a candidate across one.
	sentenceEndRE = lazyregexp.New(`[.!?]\s`)
	// spaceRunRE is the layout padding of a message rendered from HTML. It is
	// collapsed first, so the distances measure words, not table cells.
	spaceRunRE = lazyregexp.New(`\s+`)
)

// ContextPhrase is a second-tier phrase with the concrete wordings it reads.
// The search vocabulary of the code lookup must cover every sample, because a
// phrase the extractor reads but the search never returns makes the code
// invisible. The bare code noun is deliberately not a phrase: a copyright year
// near the word "code" would make a real code ambiguous.
type ContextPhrase struct {
	Pattern string
	Samples []string
}

// ContextPhrases lists the second-tier phrases.
var ContextPhrases = []ContextPhrase{
	{`verification\s*code`, []string{"verification code"}},
	{`one[\s-]*time[\s-]*(?:pass[\s-]*)?(?:code|passcode)`, []string{"one-time code", "one time code", "one-time passcode", "one time pass code"}},
	{`passcode`, []string{"passcode"}},
	{`security\s*code`, []string{"security code"}},
	{`confirmation\s*code`, []string{"confirmation code"}},
	{`access\s*code`, []string{"access code"}},
	{`authentication\s*code`, []string{"authentication code"}},
	{`(?:log|sign)[- ]?in\s*code`, []string{"login code", "log in code", "sign-in code", "sign in code"}},
	{`sms[- ]*(?:verification[- ]*)?code`, []string{"sms code", "sms verification code"}},
	{`text(?:[- ]+message)?[- ]*(?:verification[- ]*)?code`, []string{"text code", "text message code", "text message verification code", "text verification code"}},
	{`(?:phone|mobile)[- ]*(?:verification[- ]*)?code`, []string{"phone code", "mobile code", "phone verification code", "mobile verification code"}},
	{`(?:code|passcode)\s+(?:we\s+)?sent\s+(?:to|via|by)\s+(?:your\s+)?(?:phone|mobile|sms|text)`, []string{"code sent to your phone", "code we sent via sms", "passcode sent by text", "code sent to mobile"}},
	{`(?:confirm|verify)\s+your\s+identity`, []string{"confirm your identity", "verify your identity"}},
	{`(?:confirm|verify)\s+(?:your\s+)?(?:phone|mobile)`, []string{"confirm your phone", "verify your phone", "confirm phone", "verify mobile", "verify your mobile", "confirm your mobile"}},
	{`your\s*code`, []string{"your code"}},
	{`this\s*code`, []string{"this code"}},
	{`\botp\b`, []string{"otp"}},
}

var contextRE = func() *lazyregexp.Regexp {
	parts := make([]string, len(ContextPhrases))
	for i, p := range ContextPhrases {
		parts[i] = p.Pattern
	}
	return lazyregexp.New("(?i)" + strings.Join(parts, "|"))
}()

// OTPSearchTerms is the Gmail search vocabulary of the code lookup, OR-ed. It
// over-fetches on purpose; the strict per-message filter and the fail-closed
// extractor decide.
var OTPSearchTerms = []string{
	"verification code", "one-time code", "one-time passcode", "one time code", "pass code", "security code",
	"confirmation code", "access code", "authentication code", "login code", "log in code", "sign-in code",
	"sms code", "sms verification", "text code", "text message code", "text message verification", "text verification",
	"phone code", "mobile code", "phone verification", "mobile verification",
	"code sent", "code we sent", "passcode sent",
	"confirm your identity", "verify your identity", "verify your email", "confirm your email", "email verification",
	"confirm your phone", "verify your phone", "confirm phone", "verify mobile", "verify your mobile", "confirm your mobile",
	"your code", "this code", "code expires", "your pin", "pin code",
	"otp", "passcode",
}

// OTPQueryTerms renders the vocabulary as one Gmail OR group.
func OTPQueryTerms() string {
	var b strings.Builder
	b.WriteByte('{')
	for i, t := range OTPSearchTerms {
		if i > 0 {
			b.WriteByte(' ')
		}
		if strings.ContainsAny(t, " -") {
			b.WriteString(`"` + t + `"`)
		} else {
			b.WriteString(t)
		}
	}
	b.WriteByte('}')
	return b.String()
}

type span struct{ start, end int }

type candidate struct {
	value string
	span  span
	// weak candidates (5 to 10 capital letters) count only in the second tier.
	weak bool
}

func blankNoise(s string) string {
	return noiseRE.ReplaceAllStringFunc(s, func(m string) string { return strings.Repeat(" ", len(m)) })
}

func isAlnum(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// wordShaped is a lowercase prefix followed by capitalized segments: a name or
// a camel-case product word, never a random code.
var wordShapedRE = lazyregexp.New(`^[a-z]*(?:[A-Z][a-z]+)+$`)

// compoundPart reports a token that is one piece of a punctuation-joined
// compound such as a company domain name.
func compoundPart(text string, start, end int) bool {
	joined := func(punct, neighbour byte) bool {
		return punct != 0 && strings.IndexByte(".-_/@+", punct) >= 0 && neighbour != 0 && isAlnum(neighbour)
	}
	at := func(i int) byte {
		if i < 0 || i >= len(text) {
			return 0
		}
		return text[i]
	}
	return joined(at(end), at(end+1)) || joined(at(start-1), at(start-2))
}

func classify(tok string) (strong, weak bool) {
	var upper, lower, digit int
	for i := 0; i < len(tok); i++ {
		switch c := tok[i]; {
		case c >= 'A' && c <= 'Z':
			upper++
		case c >= 'a' && c <= 'z':
			lower++
		default:
			digit++
		}
	}
	n := len(tok)
	if digit == n {
		return n >= 4 && n <= 8, false
	}
	if n < 4 || n > 10 {
		return false, false
	}
	if digit > 0 && upper+lower > 0 {
		return true, false
	}
	if upper > 0 && lower > 0 {
		return !wordShapedRE.MatchString(tok), false
	}
	return false, n >= 5 && upper == n
}

func collectCandidates(text string) []candidate {
	var out []candidate
	for i := 0; i < len(text); {
		if !isAlnum(text[i]) {
			i++
			continue
		}
		j := i
		for j < len(text) && isAlnum(text[j]) {
			j++
		}
		strong, weak := classify(text[i:j])
		letters := !strings.ContainsAny(text[i:j], "0123456789")
		if (strong || weak) && letters && compoundPart(text, i, j) {
			strong, weak = false, false
		}
		if strong || weak {
			out = append(out, candidate{text[i:j], span{i, j}, weak})
		}
		i = j
	}
	return out
}

func gap(a, b span) int {
	switch {
	case a.end <= b.start:
		return b.start - a.end
	case b.end <= a.start:
		return a.start - b.end
	}
	return 0
}

func touches(text string, kw, c span) bool {
	first, second := kw, c
	if kw.end > c.start {
		first, second = c, kw
	}
	if second.start < first.end || second.start-first.end > connectorWindow {
		return false
	}
	between := text[first.end:second.start]
	return connectorRE.MatchString(between) && !sentenceEndRE.MatchString(between)
}

// sameSentence reports that no sentence ends between two spans.
func sameSentence(text string, a, b span) bool {
	if a.end > b.start {
		a, b = b, a
	}
	return b.start <= a.end || !sentenceEndRE.MatchString(text[a.end:b.start])
}

func sole(values map[string]bool) string {
	if len(values) != 1 {
		return ""
	}
	for v := range values {
		return v
	}
	return ""
}

// Code extracts the one verification code from a message's text and subject.
// It fails closed: it returns "" when there is no candidate or when the winning
// tier holds two or more distinct candidates. A value repeated identically
// counts once, which is how the same code in a plain and an HTML part is one
// candidate.
//
// Digits that read as a date, a phone number, an amount, a time, a ZIP code or
// an order or reference number are never candidates, and a code noun that a
// qualifier turns into something else ("ZIP code", "promo code") is not a code
// noun.
//
// Tier one: a candidate directly beside a code noun (code, passcode, OTP, PIN),
// on either side, with at most 20 characters of connector text between them. A
// candidate made only of capital letters never counts here. Only when tier one
// is empty does tier two accept a candidate within 40 characters of a specific
// context phrase; a four-digit year never counts there. Neither tier reaches
// across the end of a sentence.
func Code(body, subject string) string {
	text := body
	if subject != "" {
		text = subject + "\n" + body
	}
	text = blankNoise(spaceRunRE.ReplaceAllString(text, " "))
	cands := collectCandidates(text)
	kept := cands[:0]
	for _, c := range cands {
		if !nonCodeDigits(text, c) {
			kept = append(kept, c)
		}
	}
	cands = kept
	if len(cands) == 0 {
		return ""
	}
	var intros []span
	for _, m := range introducerRE.FindAllStringIndex(text, -1) {
		if k := (span{m[0], m[1]}); !nonVerificationNoun(text, k) {
			intros = append(intros, k)
		}
	}
	introduced := map[string]bool{}
	for _, c := range cands {
		if c.weak {
			continue
		}
		for _, k := range intros {
			if touches(text, k, c.span) {
				introduced[c.value] = true
				break
			}
		}
	}
	if len(introduced) > 0 {
		return sole(introduced)
	}
	var ctxs []span
	for _, m := range contextRE.FindAllStringIndex(text, -1) {
		ctxs = append(ctxs, span{m[0], m[1]})
	}
	if len(ctxs) == 0 {
		return ""
	}
	near := map[string]bool{}
	for _, c := range cands {
		if isYear(c) {
			continue
		}
		for _, k := range ctxs {
			if gap(k, c.span) <= proximityWindow && sameSentence(text, k, c.span) {
				near[c.value] = true
				break
			}
		}
	}
	return sole(near)
}
