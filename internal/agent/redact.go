// Clean-room: docs/provenance.md#CR-AGENT-TAINT

package agent

import (
	"fmt"
	"sort"
	"strings"

	"applysmith/internal/lazyregexp"
	"applysmith/internal/vault"
)

// Categories a redaction is counted under.
const (
	CategoryCredential = "credential"
	CategoryVault      = "vault"
	// CategoryRuleFailure is counted when a rule broke and the whole result
	// was withheld.
	CategoryRuleFailure = "rule-failure"
)

// RedactionFailedNotice replaces a result when a redaction rule fails: a
// result that could not be checked is not given on.
const RedactionFailedNotice = "[The tool result was withheld because it could not be checked for secrets.]"

// RedactReport says what a redaction replaced: the categories and how many
// spans each. It never holds the matched text.
type RedactReport struct{ Counts map[string]int }

// Total is the number of replaced spans.
func (r RedactReport) Total() int {
	n := 0
	for _, c := range r.Counts {
		n += c
	}
	return n
}

// String lists the categories in name order, such as "credential=2 vault=1".
func (r RedactReport) String() string {
	names := make([]string, 0, len(r.Counts))
	for k := range r.Counts {
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, k := range names {
		parts[i] = fmt.Sprintf("%s=%d", k, r.Counts[k])
	}
	return strings.Join(parts, " ")
}

type redactRule struct {
	category string
	apply    func(string) (string, int)
}

// Redactor replaces, in a tool result, the spans shaped like a credential and
// every secret value the credentials vault holds. It replaces spans only: a
// path or a company name a tool reports stays, unlike in the disclosure gate,
// which replaces a whole answer.
type Redactor struct{ rules []redactRule }

// NewRedactor returns the process's redactor: the vault's remembered values
// first, then the credential shapes.
func NewRedactor() *Redactor {
	return &Redactor{rules: []redactRule{
		{CategoryVault, func(s string) (string, int) { return vault.RedactAs(s, marker(CategoryVault)) }},
		{CategoryCredential, redactCredentialShapes},
	}}
}

func marker(category string) string { return "[REDACTED:" + category + "]" }

// Redact applies every rule. A rule that panics replaces the whole result with
// the fixed notice.
func (r *Redactor) Redact(text string) (out string, rep RedactReport) {
	defer func() {
		if recover() != nil {
			out, rep = RedactionFailedNotice, RedactReport{Counts: map[string]int{CategoryRuleFailure: 1}}
		}
	}()
	rep.Counts = map[string]int{}
	for _, rule := range r.rules {
		var n int
		text, n = rule.apply(text)
		if n > 0 {
			rep.Counts[rule.category] += n
		}
	}
	return text, rep
}

// shape is one pattern of the credential shapes. check, when set, decides
// whether a match is a credential or ordinary text that happens to fit;
// assignment marks the shape whose matches assignedSecret redacts.
type shape struct {
	re         *lazyregexp.Regexp
	check      func(match string) bool
	assignment bool
}

var credentialShapes = []shape{
	{re: lazyregexp.New(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|\z)`)},
	{re: lazyregexp.New(`\b(?:sk-[A-Za-z0-9_-]{16,}|AIza[A-Za-z0-9_-]{30,}|nvapi-[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{16,}|xox[abprs]-[A-Za-z0-9-]{16,}|AKIA[0-9A-Z]{16}|eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,})`)},
	// A bearer token has digits or token punctuation; the word after "bearer"
	// in a sentence does not.
	{re: lazyregexp.New(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{12,}`), check: func(m string) bool { return strings.ContainsAny(m[6:], "0123456789._~+/=-") }},
	{re: lazyregexp.New(`(?i)\b(?:proxy-)?authorization\s*[:=]\s*["']?(?:basic|bearer|token)?\s*[A-Za-z0-9._~+/=-]{8,}`)},
	{re: lazyregexp.New(`(?i)\b(?:x-api-key|api-key)\s*[:=]\s*["']?[^\s"',}]{6,}`)},
	// An assignment to a name that says it is a secret. The value must look
	// like one (a digit or a symbol), so "password: required" is left alone.
	// The optional word before it lets assignedSecret read "entry key".
	{re: lazyregexp.New(`(?i)\b(?:(?:` + keyQualifiers + `) )?[a-z0-9_.-]*(?:key|token|secret|passw(?:or)?d)[a-z0-9_.-]*\s*[:=]\s*["']?[^\s"',;]{8,}`), assignment: true},
}

var secretName = lazyregexp.New(`(?i)secret|passw(?:or)?d`)

// keyQualifiers name a key that is a database or lookup key, not a credential.
const keyQualifiers = `entry|primary|sort|foreign|partition|lookup|row|cache|index`

// plainKeyName is a name with "key" in it that never names a credential.
var plainKeyName = lazyregexp.New(`(?i)^(?:(?:` + keyQualifiers + `)[ _.-]keys?|keywords?|keyboard)$`)

// slugValue says whether v is lowercase words joined by - or _, such as
// years-full-time-work-experience: an id or a slug, not a generated secret.
func slugValue(v string) bool {
	parts := strings.FieldsFunc(v, func(r rune) bool { return r == '-' || r == '_' })
	if len(parts) < 2 || strings.Trim(v, "abcdefghijklmnopqrstuvwxyz-_") != "" {
		return false
	}
	for _, p := range parts {
		if !strings.ContainsAny(p, "aeiouy") {
			return false
		}
	}
	return true
}

// assignedSecret redacts an assignment match. The value must look like a
// secret (a digit or a symbol). A value of digits alone under a key or token
// name is an id, not a credential: Greenhouse names a posting with
// token=4114318009. In a query string the id ends at the next "&", and the
// parameters after it are checked on their own. A password or secret is
// redacted whatever it holds; any other name is not a credential when it is a
// plain key name or its value is a slug.
func assignedSecret(match string) (string, int) {
	i := strings.IndexAny(match, ":=")
	val := strings.TrimLeft(match[i+1:], " \t\"'")
	if name := strings.TrimSpace(match[:i]); !secretName.MatchString(name) && (plainKeyName.MatchString(name) || slugValue(strings.TrimRight(val, ".!?:)]"))) {
		return match, 0
	}
	id, rest, _ := strings.Cut(val, "&")
	if digits := strings.TrimRight(id, ".!?:)]"); digits != "" && strings.Trim(digits, "0123456789") == "" && !secretName.MatchString(match[:i]) {
		if rest == "" {
			return match, 0
		}
		out, n := redactCredentialShapes(rest)
		return match[:len(match)-len(rest)] + out, n
	}
	if !strings.ContainsAny(val, "0123456789_-/+=@!#$%^&*") {
		return match, 0
	}
	return marker(CategoryCredential), 1
}

func redactCredentialShapes(s string) (string, int) {
	n := 0
	for _, sh := range credentialShapes {
		s = sh.re.ReplaceAllStringFunc(s, func(m string) string {
			if sh.assignment {
				out, k := assignedSecret(m)
				n += k
				return out
			}
			if sh.check != nil && !sh.check(m) {
				return m
			}
			n++
			return marker(CategoryCredential)
		})
	}
	return s, n
}

// hasCredentialShape says whether s holds a span shaped like a credential.
func hasCredentialShape(s string) bool {
	_, n := redactCredentialShapes(s)
	return n > 0
}
