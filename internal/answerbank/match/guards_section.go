package match

import (
	"fmt"
	"strings"
	"unicode"

	"applysmith/internal/lazyregexp"
)

// sectionKind classifies an ordered section path.
type sectionKind int

const (
	sectionNone sectionKind = iota
	sectionEducation
	sectionWork
)

func (k sectionKind) String() string {
	switch k {
	case sectionEducation:
		return "education"
	case sectionWork:
		return "work"
	}
	return "none"
}

// sectionCaptionWords keeps complete Unicode words, including combining
// marks and underscores, and the explicit row marker. Layout separators do
// not become part of a caption's vocabulary.
func sectionCaptionWords(caption string) []string {
	caption = strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, caption)
	var words []string
	start := -1
	for i, r := range caption {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) || r == '_' {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			words = append(words, caption[start:i])
			start = -1
		}
		if r == '#' {
			words = append(words, "#")
		}
	}
	if start >= 0 {
		words = append(words, caption[start:])
	}
	return words
}

func ignoredSectionCaption(words []string) bool {
	if len(words) == 0 {
		return true
	}
	first := 0
	if words[0] == "select" {
		first++
	}
	if first < len(words) {
		switch words[first] {
		case "which", "will", "do", "does", "are", "is", "have", "has", "can", "could", "would", "what", "where", "why", "how":
			return true
		}
	}
	if len(words) > 1 && words[0] == "apply" {
		switch words[1] {
		case "now", "for", "to":
			return true
		}
	}
	return false
}

func sectionArrangement(word string) bool {
	switch strings.TrimSuffix(word, "s") {
	case "model", "location", "arrangement", "preference", "style", "environment", "authorization", "authorisation", "eligibility", "schedule", "setting", "mode", "type":
		return true
	}
	return false
}

// sectionCaptionRole is shared by section names and numbered entity labels;
// a new entity word cannot silently miss repeated-row protection.
func sectionCaptionRole(words []string, i int) sectionKind {
	next := ""
	if i+1 < len(words) {
		next = words[i+1]
	}
	switch words[i] {
	case "education", "school", "university", "college", "academic", "degree":
		return sectionEducation
	case "employment":
		// "Employment Information" (evlo) or "Employment Eligibility" is a step of
		// the application, not the history of past jobs.
		if next != "information" && !sectionArrangement(next) {
			return sectionWork
		}
	case "employer", "experience", "position":
		return sectionWork
	case "work":
		if !sectionArrangement(next) {
			return sectionWork
		}
	case "job":
		if next == "history" {
			return sectionWork
		}
	}
	return sectionNone
}

func sectionNumber(word string) bool {
	if word == "" {
		return false
	}
	for _, r := range word {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func indexedSectionCaption(words []string) bool {
	for i, word := range words {
		if len(word) > 2 && sectionNumber(word[:len(word)-2]) {
			switch word[len(word)-2:] {
			case "st", "nd", "rd", "th":
				return true
			}
		}
		if i+1 == len(words) {
			continue
		}
		next := i + 1
		if words[next] == "#" {
			next++
		}
		if next == len(words) || !sectionNumber(words[next]) {
			continue
		}
		if sectionCaptionRole(words, i) != sectionNone || (word == "history" && i > 0 && words[i-1] == "job") {
			return true
		}
		switch word {
		case "#", "entry", "row", "item", "no", "number", "job", "company", "section":
			return true
		}
	}
	return false
}

// readSectionPath parses each retained caption once. Education has priority
// across the entire path; an index matters only when a retained caption names
// an entity section. Ignored page/question captions contribute neither.
func readSectionPath(path []string) (kind sectionKind, repeated bool, parts []string) {
	indexed := false
	for _, part := range path {
		part = strings.TrimSpace(part)
		words := sectionCaptionWords(part)
		if ignoredSectionCaption(words) {
			continue
		}
		parts = append(parts, part)
		indexed = indexed || indexedSectionCaption(words)
		for i := range words {
			role := sectionCaptionRole(words, i)
			if role == sectionEducation || kind == sectionNone {
				kind = role
			}
		}
	}
	return kind, indexed && kind != sectionNone, parts
}

func classifySection(path []string) sectionKind {
	kind, _, _ := readSectionPath(path)
	return kind
}

func isRepeatedRow(path []string) bool {
	_, repeated, _ := readSectionPath(path)
	return repeated
}

var gpaSegmentRE = lazyregexp.New(`(?:^|-)gpa(?:-|$)`)

// personalEntryLabel names why an entry is refused in any entity section.
func personalEntryLabel(e *Entry) string {
	id := e.ID
	switch {
	case strings.HasPrefix(id, "address-"), strings.HasPrefix(id, "phone-"), strings.HasPrefix(id, "email-"),
		strings.HasPrefix(id, "personal-email"), e.Category == "address" || e.Category == "contact":
		return "personal contact"
	case strings.HasPrefix(id, "preferred-work-location"):
		return "global preference"
	case id == "earliest-start-date" || id == "full-time-start-availability" || strings.HasPrefix(id, "available-to-start"):
		return "start availability"
	}
	return ""
}

// smearLabel names the single-value global history entries that must not be
// copied across the rows of a repeated section.
func smearLabel(e *Entry) string {
	id := e.ID
	switch {
	case strings.Contains(id, "graduation"):
		return "graduation"
	case strings.Contains(id, "current-job-title"):
		return "job-title"
	case id == "current-company":
		return "company"
	case gpaSegmentRE.MatchString(id):
		return "GPA"
	case strings.Contains(id, "degree-focus"):
		return "degree-focus"
	case id == "field-of-study-choice", id == "undergraduate-major":
		return "field of study"
	case id == "highest-level-education":
		return "degree"
	case id == "most-recent-school", id == "undergraduate-institution", id == "undergraduate-school":
		return "school"
	}
	return ""
}

// sectionPolicy applies the entity-section rules to a classified entry: personal
// entries in any education or work section, and single-value history entries in
// a repeated row, are a terminal abstain.
func sectionPolicy(e *Entry, q Question) (Abstain, string, bool) {
	kind, repeated, parts := readSectionPath(q.SectionPath)
	if kind == sectionNone {
		return Abstain{}, "", false
	}
	path := strings.Join(parts, " > ")
	if label := personalEntryLabel(e); label != "" {
		return Abstain{Reason: ReasonSectionPolicy},
			fmt.Sprintf("section-path policy: %s entry %q inside %s section [%s] -> abstain", label, e.ID, kind, path), true
	}
	if repeated {
		if label := smearLabel(e); label != "" {
			return Abstain{Reason: ReasonSectionPolicy},
				fmt.Sprintf("section-path policy: %s entry %q inside repeated %s row [%s] -> abstain (per-row %s must not smear the global answer)", label, e.ID, kind, path, label), true
		}
	}
	return Abstain{}, "", false
}
