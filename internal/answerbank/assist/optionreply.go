package assist

import (
	"encoding/json"
	"strconv"
	"strings"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/lazyregexp"
)

// reply is what the option prompt asks the model for: one option and the ids of
// the facts it relies on, or an abstain.
type reply struct {
	Option  string
	Facts   []string
	Abstain bool
}

var (
	listMarkerRE = lazyregexp.New(`^[\s\-\x{2022}*]*\d+[.)]\s+`)
	abstainWord  = lazyregexp.New(`(?i)\babstain\b`)
	factIDRE     = lazyregexp.New(`\bf\d{1,2}\b`)
	factsLineRE  = lazyregexp.New(`(?im)^\s*facts?\s*[:=]\s*(.*)$`)
	optionLineRE = lazyregexp.New(`(?im)^\s*(?:option|answer|choice)\s*[:=]\s*(.*)$`)
)

// parseReply reads the model's text: a JSON object {"option","facts","abstain"}
// is the contract; a plain reply is read leniently, its first line the option
// and any "facts:" line the citation.
func parseReply(text string) reply {
	text = strings.TrimSpace(text)
	if i, j := strings.Index(text, "{"), strings.LastIndex(text, "}"); i >= 0 && j > i {
		var raw struct {
			Option  json.RawMessage `json:"option"`
			Facts   []string        `json:"facts"`
			Abstain bool            `json:"abstain"`
		}
		if json.Unmarshal([]byte(text[i:j+1]), &raw) == nil {
			r := reply{Facts: raw.Facts, Abstain: raw.Abstain}
			var s string
			if json.Unmarshal(raw.Option, &s) != nil {
				var n json.Number
				if json.Unmarshal(raw.Option, &n) == nil {
					s = n.String()
				}
			}
			r.Option = strings.TrimSpace(s)
			if abstainWord.MatchString(r.Option) && len(r.Option) <= 40 {
				r.Abstain, r.Option = true, ""
			}
			return r
		}
	}
	r := reply{}
	if len(text) > 40 {
		r.Abstain = abstainWord.MatchString(text[:40])
	} else {
		r.Abstain = abstainWord.MatchString(text)
	}
	if m := factsLineRE.FindStringSubmatch(text); m != nil {
		r.Facts = factIDRE.FindAllString(strings.ToLower(m[1]), -1)
		text = strings.TrimSpace(factsLineRE.ReplaceAllString(text, ""))
	}
	if m := optionLineRE.FindStringSubmatch(text); m != nil {
		text = m[1]
	}
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	r.Option = strings.TrimSpace(text)
	if r.Abstain {
		r.Option = ""
	}
	return r
}

func unquote(s string) string {
	return strings.Trim(strings.TrimSpace(s), "\"'`\u201c\u201d\u2018\u2019")
}

// pickOption parses the reply's option against the rendered options: exact
// label, case-insensitive label, unique containment, then a bare 1-based index
// into the prompt's numbering. It yields the index of exactly one option.
func pickOption(replyOption string, opts []match.Option) (int, bool) {
	s := unquote(listMarkerRE.ReplaceAllString(strings.TrimSpace(replyOption), ""))
	if s == "" {
		return 0, false
	}
	for i, o := range opts {
		if strings.TrimSpace(o.Label) == s {
			return i, true
		}
	}
	for i, o := range opts {
		if strings.EqualFold(strings.TrimSpace(o.Label), s) {
			return i, true
		}
	}
	rn := normalizeSupport(s)
	found := -1
	for i, o := range opts {
		ln := normalizeSupport(o.Label)
		if strings.TrimSpace(ln) == "" {
			continue
		}
		if strings.Contains(rn, ln) || strings.Contains(ln, rn) {
			if found >= 0 {
				found = -2
				break
			}
			found = i
		}
	}
	if found >= 0 {
		return found, true
	}
	if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(opts) {
		return n - 1, true
	}
	return 0, false
}

// significant lists the words of a label that count toward support: stopwords
// removed, single letters dropped unless numeric.
func significant(s string) []string {
	var out []string
	for _, t := range nonAlnumRE.Split(strings.ToLower(s), -1) {
		if t == "" || stopwords[t] {
			continue
		}
		if len(t) < 2 && !pureDigitRE.MatchString(t) {
			continue
		}
		if d, ok := spelledNumbers[t]; ok {
			t = d
		}
		out = append(out, t)
	}
	return out
}

// supports reports whether a cited fact supports the label: the label occurs in
// the fact after normalization, or, for a label of at least three significant
// words, at least two of them occur in that one fact.
func supports(label, fact string) bool {
	ln := normalizeSupport(label)
	if strings.TrimSpace(ln) == "" {
		return false
	}
	fn := normalizeSupport(fact)
	if strings.Contains(fn, ln) {
		return true
	}
	words := distinct(significant(label))
	if len(words) < 3 {
		return false
	}
	have := map[string]bool{}
	for _, w := range significant(fact) {
		have[w] = true
	}
	n := 0
	for _, w := range words {
		if have[w] {
			n++
		}
	}
	return n >= 2
}
