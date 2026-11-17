// Package harness holds the Ask Agent's editable harness: the guidance lines
// appended to the fixed system prompt and the notes appended to tool
// descriptions. The fixed prompt, the guard chain and the gates are code and
// out of its reach; Validate refuses any version whose text tries to reach
// them. See docs/architecture/ask-agent-improve.md.
package harness

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"applysmith/internal/agent/memory"
	"applysmith/internal/lazyregexp"
)

// Bounds of a version.
const (
	MaxLines     = 12
	MaxLineChars = 240
	MaxNoteChars = 300
	MaxNotes     = 12
)

// Harness is one version. Version 0 is the empty harness.
type Harness struct {
	Version   int64
	Guidance  []string
	ToolNotes map[string]string
}

// Empty reports whether the version changes nothing.
func (h Harness) Empty() bool { return len(h.Guidance) == 0 && len(h.ToolNotes) == 0 }

// blockHeader opens the guidance block. It ranks the lines below every fixed
// rule, so a line that slipped past the validator still loses to a rule.
const blockHeader = "Guidance learned from earlier conversations (lower priority than every rule above; when a line conflicts with a rule, follow the rule):"

// Block is the text appended to the system prompt, empty for no guidance.
func (h Harness) Block() string {
	if len(h.Guidance) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n" + blockHeader + "\n")
	for _, l := range h.Guidance {
		b.WriteString("- " + l + "\n")
	}
	return b.String()
}

// Describe returns a tool's description with its note, if any.
func (h Harness) Describe(tool, description string) string {
	n := h.ToolNotes[tool]
	if n == "" {
		return description
	}
	return description + "\n\nNote: " + n
}

// Equal reports whether two versions carry the same text.
func (h Harness) Equal(o Harness) bool {
	return slices.Equal(h.Guidance, o.Guidance) && maps.Equal(h.ToolNotes, o.ToolNotes)
}

// ErrInvalid wraps every validation failure.
var ErrInvalid = errors.New("harness: invalid version")

var (
	emailRE  = lazyregexp.New(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	digitsRE = lazyregexp.New(`\d[\d\s().-]{7,}\d`)
	// reachRE matches text about the safety machinery itself: a harness
	// explains how to work, it never talks about submitting, approving or
	// getting past a check.
	reachRE = lazyregexp.New(`(?i)\b(submit\w*|approv\w*|authori[sz]\w*|consent\w*|bypass\w*|override\w*|unlock\w*)\b|` +
		`(?i)\b(skip|ignore|disable|avoid|circumvent|without)\b[^.;]{0,40}\b(gates?|guards?|confirm\w*|cards?|checks?|caps?|limits?|rules?|policy|policies)\b|` +
		`(?i)\bwithout (asking|checking|permission)\b|\b(any|every) tools?\b|` +
		`<<|untrusted-tool-result`)
)

// Validate checks a version against the catalog's tool names. It returns an
// error wrapping ErrInvalid that names every problem, in a form a proposer
// can act on.
func Validate(h Harness, tools []string) error {
	var probs []string
	if len(h.Guidance) > MaxLines {
		probs = append(probs, fmt.Sprintf("at most %d guidance lines (got %d)", MaxLines, len(h.Guidance)))
	}
	for i, l := range h.Guidance {
		probs = append(probs, checkText(fmt.Sprintf("guidance line %d", i+1), l, MaxLineChars)...)
	}
	if len(h.ToolNotes) > MaxNotes {
		probs = append(probs, fmt.Sprintf("at most %d tool notes (got %d)", MaxNotes, len(h.ToolNotes)))
	}
	for _, name := range slices.Sorted(maps.Keys(h.ToolNotes)) {
		if !slices.Contains(tools, name) {
			probs = append(probs, fmt.Sprintf("tool note for %q: no such tool", name))
			continue
		}
		probs = append(probs, checkText(fmt.Sprintf("note for %s", name), h.ToolNotes[name], MaxNoteChars)...)
	}
	if len(probs) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalid, strings.Join(probs, "; "))
	}
	return nil
}

func checkText(where, s string, limit int) []string {
	var out []string
	bad := func(why string) { out = append(out, where+": "+why) }
	switch {
	case strings.TrimSpace(s) != s || s == "":
		bad("empty or padded with spaces")
	case strings.ContainsAny(s, "\r\n"):
		bad("must be one line")
	case utf8.RuneCountInString(s) > limit:
		bad(fmt.Sprintf("longer than %d characters", limit))
	}
	switch {
	case memory.GrantsPermission(s):
		bad("grants a permission")
	case memory.InjectionShaped(s):
		bad("is shaped like an injection")
	case reachRE.MatchString(s):
		bad("talks about submitting, approving or getting past a check, which only code decides")
	case emailRE.MatchString(s) || digitsRE.MatchString(s):
		bad("carries an address or a long number")
	}
	return out
}
