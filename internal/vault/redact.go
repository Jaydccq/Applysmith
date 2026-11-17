package vault

import (
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// The process-wide redactor holds every secret value this process has read or
// stored, and the two quoted forms a log line or an encoded error can carry it
// in. It is bounded: when full, the oldest values are forgotten first.
const maxRemembered = 256

const redactedText = "[redacted]"

var redactor struct {
	mu     sync.RWMutex
	order  []string // insertion order, for eviction
	byLen  []string // longest first, so a value inside another is replaced last
	member map[string]bool
}

func remember(value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	forms := []string{value}
	if b, err := json.Marshal(value); err == nil {
		forms = append(forms, string(b[1:len(b)-1]))
	}
	forms = append(forms, strings.Trim(strconv.Quote(value), `"`))
	redactor.mu.Lock()
	defer redactor.mu.Unlock()
	if redactor.member == nil {
		redactor.member = map[string]bool{}
	}
	changed := false
	for _, f := range forms {
		if f == "" || redactor.member[f] {
			continue
		}
		redactor.member[f] = true
		redactor.order = append(redactor.order, f)
		changed = true
	}
	for len(redactor.order) > maxRemembered {
		delete(redactor.member, redactor.order[0])
		redactor.order = redactor.order[1:]
		changed = true
	}
	if changed {
		redactor.byLen = append(redactor.byLen[:0], redactor.order...)
		sort.SliceStable(redactor.byLen, func(i, j int) bool { return len(redactor.byLen[i]) > len(redactor.byLen[j]) })
	}
}

// Redact replaces every remembered secret value in s by [redacted].
func Redact(s string) string {
	out, _ := RedactAs(s, redactedText)
	return out
}

// RedactAs replaces every remembered secret value in s by marker and says how
// many places it replaced.
func RedactAs(s, marker string) (string, int) {
	redactor.mu.RLock()
	defer redactor.mu.RUnlock()
	n := 0
	for _, v := range redactor.byLen {
		if c := strings.Count(s, v); c > 0 {
			s = strings.ReplaceAll(s, v, marker)
			n += c
		}
	}
	return s, n
}

// RedactWriter wraps a log sink so no remembered secret reaches it. Each Write
// is one log line, so a value is never split across calls.
type RedactWriter struct{ W io.Writer }

func (r RedactWriter) Write(p []byte) (int, error) {
	if _, err := r.W.Write([]byte(Redact(string(p)))); err != nil {
		return 0, err
	}
	return len(p), nil
}
