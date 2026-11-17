package kb

import (
	"regexp"
	"strings"
	"sync"
)

// Limits on triggers: a pattern is compiled once, so its size and the number
// of patterns per snippet are bounded to keep the cost of an index and of a
// query small.
const (
	maxTriggerBytes = 512
	maxTriggers     = 32
)

// trigger is a compiled trigger phrase: a literal, or an RE2 pattern written
// as /pattern/flags.
type trigger struct {
	raw     string
	literal string
	re      *regexp.Regexp
	invalid bool
}

func normalizeSpace(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// triggerCache holds each distinct trigger once. Triggers are bounded in size
// and count per snippet, and the table is cleared when it grows past the
// bound, so it cannot grow without limit.
var (
	triggerMu    sync.Mutex
	triggerCache = map[string]trigger{}
)

const triggerCacheMax = 8192

// compileTrigger never fails: a pattern that does not compile, is too long, or
// matches the empty string is marked invalid and never matches.
func compileTrigger(raw string) trigger {
	triggerMu.Lock()
	t, ok := triggerCache[raw]
	triggerMu.Unlock()
	if ok {
		return t
	}
	t = buildTrigger(raw)
	triggerMu.Lock()
	if len(triggerCache) >= triggerCacheMax {
		clear(triggerCache)
	}
	triggerCache[raw] = t
	triggerMu.Unlock()
	return t
}

func buildTrigger(raw string) trigger {
	t := trigger{raw: raw}
	if len(raw) > maxTriggerBytes {
		t.invalid = true
		return t
	}
	if strings.HasPrefix(raw, "/") {
		if end := strings.LastIndex(raw, "/"); end > 0 {
			pattern, flags := raw[1:end], raw[end+1:]
			prefix := ""
			ok := true
			for _, f := range flags {
				switch f {
				case 'i', 'm', 's', 'U':
					prefix += string(f)
				case 'g':
				default:
					ok = false
				}
			}
			if prefix != "" {
				pattern = "(?" + prefix + ")" + pattern
			}
			re, err := regexp.Compile(pattern)
			if !ok || err != nil || raw[1:end] == "" || re.MatchString("") {
				t.invalid = true
				return t
			}
			t.re = re
			return t
		}
	}
	t.literal = normalizeSpace(raw)
	return t
}

func (t trigger) isLiteral() bool { return t.re == nil && !t.invalid }

// matches reports whether the trigger fires for the query.
func (t trigger) matches(query string) bool {
	switch {
	case t.invalid:
		return false
	case t.re != nil:
		return t.re.MatchString(query)
	}
	return t.literal != "" && strings.Contains(normalizeSpace(query), t.literal)
}

func compileTriggers(raw []string) []trigger {
	out := make([]trigger, 0, len(raw))
	for _, r := range raw {
		out = append(out, compileTrigger(r))
	}
	return out
}

func anyTrigger(ts []trigger, query string) bool {
	for _, t := range ts {
		if t.matches(query) {
			return true
		}
	}
	return false
}
