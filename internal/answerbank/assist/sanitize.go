package assist

import (
	"strings"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/lazyregexp"
)

// Posting and company text is data an employer controls. It is cleaned once, at
// the point it enters a prompt builder, and what was removed is remembered so
// the answer can be checked against the tokens the removed sentences planted.

// Cleaned is posting or company text after sanitising.
type Cleaned struct {
	Text string
	// Planted are the quoted literals of the removed sentences ("include the
	// word 'banana'"); an answer that contains one is rejected.
	Planted []string
}

var sectionMarkerRE = lazyregexp.New(`^\s*===\s*[A-Z][A-Z _]+\s*===\s*$`)

// stripPostingText removes the sentences that address an AI, line by line, and
// keeps every section marker. An AI noun used as a modifier is not touched.
func stripPostingText(text string) Cleaned {
	var out Cleaned
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if sectionMarkerRE.MatchString(line) {
			lines = append(lines, line)
			continue
		}
		var kept []string
		for _, sent := range sentencesKeepingSpace(line) {
			if strings.TrimSpace(match.StripAIDirectives(sent)) == "" && strings.TrimSpace(sent) != "" {
				out.Planted = append(out.Planted, match.QuotedLiterals(sent)...)
				continue
			}
			kept = append(kept, sent)
		}
		lines = append(lines, strings.TrimRight(strings.Join(kept, ""), " "))
	}
	out.Text = strings.Join(lines, "\n")
	return out
}

// sentencesKeepingSpace splits one line after a terminator, keeping the white
// space with the sentence so the pieces rejoin byte for byte.
func sentencesKeepingSpace(line string) []string {
	var out []string
	start := 0
	for i := 0; i < len(line); i++ {
		if (line[i] == '.' || line[i] == '!' || line[i] == '?') && (i+1 == len(line) || line[i+1] == ' ' || line[i+1] == '\t') {
			j := i + 1
			for j < len(line) && (line[j] == ' ' || line[j] == '\t') {
				j++
			}
			out = append(out, line[start:j])
			start = j
			i = j - 1
		}
	}
	if start < len(line) {
		out = append(out, line[start:])
	}
	return out
}

var employerStatRE = lazyregexp.New(`(?i)\b\d{1,3}\s?(?:%|percent)\s+of\s+(?:our\s+)?(?:employees|team\s+members|staff|workers)\b|\b(?:employee\s+(?:satisfaction|engagement)|satisfaction\s+(?:score|rating)|approval\s+(?:score|rating))\b|\b(?:glassdoor|g2|comparably|kununu|indeed)\b[^.!?]{0,40}\b(?:rating|score|rated|reviews?|stars?)\b|\b\d(?:\.\d)?\s*(?:out\s+of\s+5|/\s?5|stars?)\b|\b(?:great|best|top)\s+(?:places?|workplaces?|employers?)\s+to\s+work\b|\b\d{1,3}\s?(?:%|percent)\s+(?:satisfaction|approval|recommend)`)

var firstPersonRE = lazyregexp.New(`\bI\b|\b(?:my|me|myself|mine)\b`)

// isThirdPartyEmployerStatistic reports an answer whose substance is a
// satisfaction figure, a review-site rating or an award about the employer with
// no first-person framing.
func isThirdPartyEmployerStatistic(answer string) bool {
	return employerStatRE.MatchString(answer) && !firstPersonRE.MatchString(answer)
}

// stripEmployerStatistics removes the sentences of company context that state
// employer reputation figures; product facts and section markers stay.
func stripEmployerStatistics(text string) string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if sectionMarkerRE.MatchString(line) {
			lines = append(lines, line)
			continue
		}
		var kept []string
		for _, sent := range sentencesKeepingSpace(line) {
			if !employerStatRE.MatchString(sent) {
				kept = append(kept, sent)
			}
		}
		lines = append(lines, strings.TrimRight(strings.Join(kept, ""), " "))
	}
	return strings.Join(lines, "\n")
}

// containsPlanted reports whether text holds a literal a removed sentence tried
// to plant.
func containsPlanted(text string, planted []string) (string, bool) {
	l := strings.ToLower(text)
	for _, p := range planted {
		if len(p) >= 2 && wordBoundaryContains(l, strings.ToLower(p)) {
			return p, true
		}
	}
	return "", false
}

// delimited makes untrusted text safe to place between the prompt's data
// markers: a line that looks like a marker is defused.
func delimited(s string) string {
	s = strings.ReplaceAll(s, "<<<", "< < <")
	s = strings.ReplaceAll(s, ">>>", "> > >")
	return strings.ReplaceAll(s, "===", "= = =")
}
