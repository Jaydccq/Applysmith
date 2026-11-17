package documents

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// cvSection is one heading of cv.md with the bullets and prose under it.
type cvSection struct {
	Level   int
	Heading string
	Group   string // lower-cased level-2 ancestor heading
	Bullets []string
	Prose   []string
}

var (
	headingRE = lazyregexp.New(`^(#{1,6})\s+(.+?)\s*#*$`)
	bulletRE  = lazyregexp.New(`^\s*[-*+]\s+(.*\S)\s*$`)
	// "Title, Organization (2020-01 to present)"
	entryHeadingRE = lazyregexp.New(`^(.+?),\s*(.+?)\s*\(([^)]*)\)\s*$`)
	rangeSplitRE   = lazyregexp.New(`(?i)\s+(?:to|-)\s+|\s*[\x{2013}\x{2014}]\s*`)
	yearRangeRE    = lazyregexp.New(`(?i)^(\d{4}(?:-\d{1,2})?)-((?:\d{4}|present|current|now)\b.*)$`) // "2018-2021"; "2021-03" is one date
	linkRE         = lazyregexp.New(`\[([^\]]*)\]\([^)]*\)`)
	codeSpanRE     = lazyregexp.New("`([^`]*)`")
	starRE         = lazyregexp.New(`(\*\*|\*)(\S(?:[^*]*?\S)?)(\*\*|\*)`)
	underscoreRE   = lazyregexp.New(`(^|\W)(__|_)(\S(?:[^_]*?\S)?)(__|_)(\W|$)`)
)

// parseCV splits cv.md into sections. Bullets that wrap onto an indented
// continuation line are joined.
func parseCV(md string) []cvSection {
	var out []cvSection
	var group string
	cur := -1
	// inProse is true while the lines read so far form one prose paragraph:
	// a hard-wrapped paragraph is one source line, not one per printed line.
	inProse := false
	for _, raw := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		line := strings.TrimRight(raw, " \t")
		if m := headingRE.FindStringSubmatch(line); m != nil {
			level, text := len(m[1]), strings.TrimSpace(m[2])
			if level <= 2 {
				group = strings.ToLower(text)
			}
			out = append(out, cvSection{Level: level, Heading: text, Group: group})
			cur, inProse = len(out)-1, false
			continue
		}
		if cur < 0 || strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "<!--") {
			inProse = false
			continue
		}
		s := &out[cur]
		if m := bulletRE.FindStringSubmatch(line); m != nil {
			s.Bullets, inProse = append(s.Bullets, m[1]), false
		} else if len(s.Bullets) > 0 && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
			s.Bullets[len(s.Bullets)-1] += " " + strings.TrimSpace(line)
		} else if inProse {
			s.Prose[len(s.Prose)-1] += " " + strings.TrimSpace(line)
		} else {
			s.Prose, inProse = append(s.Prose, strings.TrimSpace(line)), true
		}
	}
	return out
}

// plain removes Markdown decoration from a source line and normalizes it.
func plain(s string) string {
	s = linkRE.ReplaceAllString(s, "$1")
	s = codeSpanRE.ReplaceAllString(s, "$1")
	s = starRE.ReplaceAllString(s, "$2")
	s = underscoreRE.ReplaceAllString(s, "$1$3$5")
	return Normalize(strings.TrimSpace(s))
}

// headingEntry reads "Title, Organization (start to end)". ok is false for a
// heading that does not have that shape.
func headingEntry(h string) (title, org, start, end string, ok bool) {
	m := entryHeadingRE.FindStringSubmatch(h)
	if m == nil {
		return "", "", "", "", false
	}
	dates := strings.TrimSpace(m[3])
	parts := rangeSplitRE.Split(dates, 2)
	if y := yearRangeRE.FindStringSubmatch(dates); len(parts) == 1 && y != nil {
		parts = y[1:]
	}
	start = strings.TrimSpace(parts[0])
	if len(parts) == 2 {
		end = strings.ToLower(strings.TrimSpace(parts[1]))
	}
	return strings.TrimSpace(m[1]), strings.TrimSpace(m[2]), start, end, true
}

func isExperienceGroup(g string) bool {
	return strings.Contains(g, "experience") || strings.Contains(g, "employment") || strings.Contains(g, "work history")
}
func isProjectGroup(g string) bool   { return strings.Contains(g, "project") }
func isEducationGroup(g string) bool { return strings.Contains(g, "education") }
func isSkillGroup(g string) bool {
	return strings.Contains(g, "skill") || strings.Contains(g, "competenc")
}
func isSummaryGroup(g string) bool {
	return strings.Contains(g, "summary") || strings.Contains(g, "profile") || strings.Contains(g, "objective")
}
