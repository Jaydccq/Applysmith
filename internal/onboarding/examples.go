package onboarding

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// A value is an example when it is the shipped persona, a reserved-domain
// email, a fictional phone number or a placeholder. The persona is the only
// kind a user can acknowledge as their own.

var (
	personaValues  = []string{"alex example", "northwind labs", "contoso cloud", "fabrikam robotics", "example university"}
	reservedHosts  = []string{"example.com", "example.org", "example.net"}
	reservedSuffix = []string{".test", ".example", ".invalid", ".localhost"}
	// A fictional number ends in 555 01xx, with or without a leading 1 and area code.
	fictionalPhone = lazyregexp.New(`^1?(\d{3})?55501\d\d$`)
	todoWord       = lazyregexp.New(`\b(TODO|TBD)\b`)
	angleToken     = lazyregexp.New(`<[A-Za-z][^<>\n]{0,40}>`)
	keyPlaceholder = lazyregexp.New(`(?i)your[_-]?key[_-]?here`)
)

func isPersona(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, p := range personaValues {
		if s == p {
			return true
		}
	}
	return false
}

// containsPersona finds a persona value inside longer text such as a CV.
func containsPersona(text string) []string {
	low := strings.ToLower(text)
	var out []string
	for _, p := range personaValues {
		if strings.Contains(low, p) {
			out = append(out, p)
		}
	}
	return out
}

func emailDomain(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return ""
	}
	return strings.ToLower(email[at+1:])
}

// reservedEmail reports whether the address is on a reserved example domain.
func reservedEmail(email string) bool {
	d := emailDomain(email)
	if d == "" {
		return false
	}
	for _, h := range reservedHosts {
		if d == h {
			return true
		}
	}
	if d == "localhost" {
		return true
	}
	for _, s := range reservedSuffix {
		if strings.HasSuffix(d, s) {
			return true
		}
	}
	return false
}

// digitsOnly keeps the digits of a phone number.
func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isFictionalPhone(phone string) bool { return fictionalPhone.MatchString(digitsOnly(phone)) }

// placeholderTokens lists the bracketed tokens, TODO/TBD words and key
// placeholders in text. A markdown link, an autolink, a comment and a checkbox
// are not tokens.
func placeholderTokens(text string) []string {
	var out []string
	for i := 0; i < len(text); i++ {
		if text[i] != '[' {
			continue
		}
		end := strings.IndexByte(text[i:], ']')
		if end < 0 {
			break
		}
		inner := text[i+1 : i+end]
		next := i + end + 1
		followedByLink := next < len(text) && (text[next] == '(' || text[next] == '[' || text[next] == ':')
		if inner != "" && len(inner) <= 60 && !strings.ContainsAny(inner, "\n") && !followedByLink && strings.TrimSpace(inner) != "x" && strings.TrimSpace(inner) != "" {
			out = append(out, text[i:i+end+1])
		}
		i += end
	}
	for _, m := range angleToken.FindAllString(text, -1) {
		if !strings.ContainsAny(m, "/@:") && !strings.HasPrefix(m, "<!--") {
			out = append(out, m)
		}
	}
	out = append(out, todoWord.FindAllString(text, -1)...)
	out = append(out, keyPlaceholder.FindAllString(text, -1)...)
	return out
}

func hasPlaceholder(s string) bool { return len(placeholderTokens(s)) > 0 }
