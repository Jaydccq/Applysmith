package learn

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"applysmith/internal/lazyregexp"
)

// StripURL removes credentials, query and fragment from a URL. Text that does
// not parse is cut at the first '?' or '#'.
func StripURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		if i := strings.IndexAny(raw, "?#"); i >= 0 {
			raw = raw[:i]
		}
		return stripUserinfo(raw)
	}
	u.User, u.RawQuery, u.Fragment, u.RawFragment, u.ForceQuery = nil, "", "", "", false
	return u.String()
}

// stripUserinfo drops "user:pass@" from a scheme-qualified text that did not
// parse.
func stripUserinfo(s string) string {
	scheme := strings.Index(s, "://")
	if scheme < 0 {
		return s
	}
	rest := s[scheme+3:]
	end := strings.IndexByte(rest, '/')
	if end < 0 {
		end = len(rest)
	}
	if at := strings.LastIndexByte(rest[:end], '@'); at >= 0 {
		return s[:scheme+3] + rest[at+1:]
	}
	return s
}

var urlInText = lazyregexp.New(`(?i)\b[a-z][a-z0-9+.-]*://[^\s<>"'` + "`" + `]+`)

// StripURLsIn applies StripURL to every URL inside free text, so a stored
// question or value never carries a token in a query string or fragment.
func StripURLsIn(s string) string {
	return urlInText.ReplaceAllStringFunc(s, StripURL)
}

var (
	emailRE = lazyregexp.New(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)*\.[A-Za-z]{2,}`)
	// phoneRun finds a candidate; whether it is a phone number depends on what
	// touches it, which RE2 cannot say (no lookaround), so phoneRedact checks.
	phoneRun = lazyregexp.New(`[+(]?\d[\d\s().\-]{6,}\d`)
)

// Redact turns contact strings of page text into [email] and [phone]. A phone
// number is a run of digits and phone punctuation holding 10 to 15 digits that
// does not touch a word character, so dates, years, zip codes, counts and ATS
// ids such as question_30011834002 survive.
func Redact(s string) string {
	return phoneRedact(emailRE.ReplaceAllString(s, "[email]"))
}

func phoneRedact(s string) string {
	var b strings.Builder
	last := 0
	for _, loc := range phoneRun.FindAllStringIndex(s, -1) {
		start, end := loc[0], loc[1]
		if start > 0 && isWordByte(s[start-1]) || end < len(s) && isWordByte(s[end]) {
			continue
		}
		digits := 0
		for i := start; i < end; i++ {
			if s[i] >= '0' && s[i] <= '9' {
				digits++
			}
		}
		if digits < 10 || digits > 15 {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString("[phone]")
		last = end
	}
	b.WriteString(s[last:])
	return b.String()
}

func isWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// clampRunes cuts s to at most n runes.
func clampRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// clampBytes cuts s to at most n bytes on a UTF-8 boundary.
func clampBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}

// Report is the user's pointed-at field report, after sanitization.
type Report struct {
	Note           string `json:"note"`
	Tag            string `json:"tag"`
	Role           string `json:"role"`
	AccessibleName string `json:"accessibleName"`
	Path           string `json:"path"`
	NearbyText     string `json:"nearbyText"`
	URL            string `json:"url"`
	FrameURL       string `json:"frameUrl"`
	ShadowDepth    int    `json:"shadowDepth"`
	Snippet        string `json:"snippet"`
	// InputType and Withheld are set only on a report about a value-withheld
	// control, which keeps nothing else of the element.
	InputType string `json:"inputType,omitempty"`
	Withheld  string `json:"withheld,omitempty"`
}

// Report budgets.
const (
	maxNote        = 2000
	maxTag         = 40
	maxRole        = 40
	maxAccessible  = 240
	maxPath        = 400
	maxNearby      = 600
	maxURL         = 500
	maxShadowDepth = 12
	maxSnippet     = 4000
	// maxSnippetIn bounds what is parsed; a larger snippet is refused, not cut
	// before the markup check, so a hostile tail cannot hide behind the cut.
	maxSnippetIn = 64 << 10
)

// ErrReportRejected marks a report refused by the server's own checks.
var ErrReportRejected = errors.New("report rejected")

// SanitizeReport re-applies sanitization whatever the sender did: URLs lose
// credentials, query and fragment, contact strings are redacted, lengths are
// clamped. A snippet that is not plain, allowlisted markup is refused, never
// repaired.
func SanitizeReport(r Report) (Report, error) {
	if r.ShadowDepth < 0 || r.ShadowDepth > maxShadowDepth {
		return Report{}, fmt.Errorf("%w: shadow depth must be 0 to %d", ErrReportRejected, maxShadowDepth)
	}
	if err := ValidateSnippet(r.Snippet); err != nil {
		return Report{}, err
	}
	text := func(s string, n int) string { return clampRunes(Redact(StripURLsIn(strings.ToValidUTF8(s, ""))), n) }
	return Report{
		Note: text(r.Note, maxNote), Tag: clampRunes(strings.TrimSpace(r.Tag), maxTag), Role: clampRunes(strings.TrimSpace(r.Role), maxRole),
		AccessibleName: text(r.AccessibleName, maxAccessible), Path: text(r.Path, maxPath), NearbyText: text(r.NearbyText, maxNearby),
		URL: clampRunes(StripURL(r.URL), maxURL), FrameURL: clampRunes(StripURL(r.FrameURL), maxURL),
		ShadowDepth: r.ShadowDepth,
		Snippet:     clampBytes(Redact(strings.ToValidUTF8(r.Snippet, "")), maxSnippet),
	}, nil
}

// snippetAttrs are the only attributes a snippet may carry.
var snippetAttrs = map[string]bool{
	"id": true, "name": true, "type": true, "role": true, "for": true, "placeholder": true, "autocomplete": true,
	"data-automation-id": true, "data-testid": true, "value": true,
}

var tagNameRE = lazyregexp.New(`^[A-Za-z][A-Za-z0-9:-]*`)

// ValidateSnippet accepts plain markup whose attributes are allowlisted. A
// value attribute is allowed only on option, radio and checkbox elements, and
// script or style markup is never allowed.
func ValidateSnippet(s string) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrReportRejected, fmt.Sprintf(format, args...))
	}
	if len(s) > maxSnippetIn {
		return bad("the snippet is larger than %d bytes", maxSnippetIn)
	}
	low := strings.ToLower(s)
	for _, w := range []string{"<script", "</script", "<style", "</style"} {
		if strings.Contains(low, w) {
			return bad("script and style markup is not allowed")
		}
	}
	for i := 0; i < len(s); {
		lt := strings.IndexByte(s[i:], '<')
		if lt < 0 {
			return nil
		}
		i += lt + 1
		switch {
		case strings.HasPrefix(s[i:], "!--"):
			end := strings.Index(s[i:], "-->")
			if end < 0 {
				return bad("an unterminated comment")
			}
			i += end + 3
			continue
		case strings.HasPrefix(s[i:], "/"):
			end := strings.IndexByte(s[i:], '>')
			if end < 0 {
				return bad("an unterminated tag")
			}
			i += end + 1
			continue
		}
		name := tagNameRE.FindString(s[i:])
		if name == "" {
			return bad("a '<' that does not open a tag")
		}
		i += len(name)
		attrs, n, err := readAttrs(s[i:])
		if err != nil {
			return bad("%v", err)
		}
		i += n
		tag := strings.ToLower(name)
		for k := range attrs {
			if !snippetAttrs[k] && !strings.HasPrefix(k, "aria-") {
				return bad("attribute %q is not allowed", k)
			}
		}
		if _, ok := attrs["value"]; ok && !choiceElement(tag, attrs["type"]) {
			return bad("a value is allowed only on option, radio and checkbox elements")
		}
	}
	return nil
}

func choiceElement(tag, typ string) bool {
	t := strings.ToLower(typ)
	return tag == "option" || tag == "input" && (t == "radio" || t == "checkbox")
}

// readAttrs reads the attributes of an open tag up to its '>' and returns them
// by lowercase name, with the number of bytes consumed.
func readAttrs(s string) (map[string]string, int, error) {
	attrs := map[string]string{}
	i := 0
	for i < len(s) {
		switch c := s[i]; {
		case c == '>':
			return attrs, i + 1, nil
		case c == '/' || c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
			continue
		case c == '<':
			return nil, 0, errors.New("a '<' inside a tag")
		}
		start := i
		for i < len(s) && !strings.ContainsRune(" \t\n\r=/>", rune(s[i])) {
			i++
		}
		name := strings.ToLower(s[start:i])
		if name == "" {
			return nil, 0, errors.New("an attribute without a name")
		}
		val := ""
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
			i++
		}
		if i < len(s) && s[i] == '=' {
			i++
			for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
				i++
			}
			if i < len(s) && (s[i] == '"' || s[i] == '\'') {
				q := s[i]
				end := strings.IndexByte(s[i+1:], q)
				if end < 0 {
					return nil, 0, errors.New("an unterminated attribute value")
				}
				val = s[i+1 : i+1+end]
				i += end + 2
			} else {
				vs := i
				for i < len(s) && !strings.ContainsRune(" \t\n\r>", rune(s[i])) {
					i++
				}
				val = s[vs:i]
			}
		}
		attrs[name] = val
	}
	return nil, 0, errors.New("an unterminated tag")
}

// Closed vocabularies of a withheld control's element: a report about such a
// control keeps only what the page cannot have used to carry text.
var (
	reportTags  = []string{"input", "select", "textarea", "button", "fieldset", "div", "span", "label", "option", "form"}
	reportRoles = []string{"textbox", "combobox", "listbox", "radio", "radiogroup", "checkbox", "switch", "spinbutton", "searchbox", "button", "option", "group"}
	inputTypes  = []string{"text", "password", "email", "tel", "number", "date", "search", "url", "radio", "checkbox", "select", "textarea", "month", "week", "time", "datetime-local"}
)

var snippetTypeRE = lazyregexp.New(`(?i)<input\b[^>]*?\btype\s*=\s*["']?([a-z-]+)`)

// snippetInputType reads the type of the first input in a snippet, so a report
// about a password field is recognised by its element as well as its label.
func snippetInputType(snippet string) string {
	if m := snippetTypeRE.FindStringSubmatch(snippet); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

// withheldReport reduces a sanitized report about a value-withheld control to
// its note, tag, role, input type and category. The user's own note stays; the
// snippet, the accessible name, the nearby text, the path and the URLs are
// dropped, since each can carry what the control holds.
func withheldReport(r Report, inputType, category string) Report {
	closed := func(v string, set []string) string {
		if v = strings.ToLower(strings.TrimSpace(v)); slices.Contains(set, v) {
			return v
		}
		return ""
	}
	return Report{Note: r.Note, Tag: closed(r.Tag, reportTags), Role: closed(r.Role, reportRoles), InputType: closed(inputType, inputTypes), Withheld: category}
}
