// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

// Package gates holds two kinds of pure decision. The evaluation predicates
// (US-person and sponsorship wording, stated compensation, required years and
// title seniority) read text. The apply safety gates (docs/architecture/gates.md)
// decide whether an action that sends, creates, clicks or solves may happen.
// Both are pure: inputs are plain values, time is passed in as Unix
// milliseconds, and nothing reads a profile, a clock, a file or the network.
// The clearance predicate lives in discovery, which owns it.
package gates

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

var abbreviations = strings.NewReplacer(
	"U.S.A.", "USA", "u.s.a.", "usa", "U.S.", "US", "u.s.", "us",
	"e.g.", "eg", "i.e.", "ie", "etc.", "etc",
)

var sentenceBreak = lazyregexp.New(`[.!?;]+\s+|[\n\r\x{2022}]+`)

// Sentences splits text into sentence-sized pieces. Abbreviations that carry a
// period (U.S., e.g.) do not end a sentence.
func Sentences(text string) []string {
	text = abbreviations.Replace(text)
	var out []string
	for _, s := range sentenceBreak.Split(text, -1) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func anyMatch(res []*lazyregexp.Regexp, s string) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

func compileAll(patterns ...string) []*lazyregexp.Regexp {
	out := make([]*lazyregexp.Regexp, len(patterns))
	for i, p := range patterns {
		out[i] = lazyregexp.New("(?i)" + p)
	}
	return out
}
