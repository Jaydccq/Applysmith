package bind

import (
	"strconv"
	"strings"

	"applysmith/internal/lazyregexp"
)

// SectionPath states the repeated entity section a control belongs to when its
// own name says so and the page has no structure that does. A form with no
// fieldset, legend or heading inside a row (every field a bare div) still
// names its row in the control name, and the matcher's entity-section policies
// key on the section path. Nil means the name states nothing.
//
// Two readings, tried in order. A name with an entity word and an underscore
// row number ("employerState_1_x") is row N of that entity. A name that spells
// an "education history" or "work history" section states it, with the row
// number when it carries one.
func SectionPath(selector string) []string {
	name := controlNameFromSelector(selector)
	if name == "" {
		return nil
	}
	if label, n, ok := entityRow(name); ok {
		return []string{label + " " + strconv.Itoa(n)}
	}
	return historySection(name)
}

var (
	camelHump     = lazyregexp.New(`([a-z0-9])([A-Z])`)
	separators    = lazyregexp.New(`[-_.]+`)
	educationWord = lazyregexp.New(`\b(?:education|school)\b`)
	employerWord  = lazyregexp.New(`\b(?:employer|employment)\b`)
	experienceWrd = lazyregexp.New(`\bexperience\b`)
	educationHist = lazyregexp.New(`\beducation\s?history\b`)
	workHist      = lazyregexp.New(`\bwork\s?history\b`)
	workdayRow    = lazyregexp.New(`^(education|workExperience)-(\d+)--`)
	trailingNum   = lazyregexp.New(`(?:^|\s)(\d+)(?:\s|$)`)
	idSelector    = lazyregexp.New(`^#((?:\\.|[^\s>.:\[\],])+)$`)
	idAttr        = lazyregexp.New(`^\[id="((?:\\.|[^"\\])*)"\]$`)
	// A quoted name may hold brackets of its own ("profile[education][new0][to]").
	nameAttr = lazyregexp.New(`\[name\s*=\s*(?:"([^"]*)"|'([^']*)'|([^"'\]\s]+))\]`)
	// nestedRow is a nested form name: the bracket after the entity word is
	// the row, and the number in it marks the row ("new0" is row 0).
	nestedRow = lazyregexp.New(`\[(educations?|employments?|experiences?|work_?histor(?:y|ies)|work_?experiences?)\]\[([^\]]+)\]\[[^\]]+\]`)
	digits    = lazyregexp.New(`\d+`)
	bareName  = lazyregexp.New(`^[A-Za-z][\w.:-]*$`)
	cssEscape = lazyregexp.New(`\\(.)`)
)

// entityRow reads "<entity word> ... _<row>_" out of a control name. Camel
// humps are split before matching so employerState has the word employer while
// schoolingPreference does not have school. The row is the last underscore
// group, because employerAddress1_2 is address line 1 of employer 2. Dashed
// forms belong to other readers with a different row base and are refused.
func entityRow(name string) (label string, row int, ok bool) {
	words := strings.ToLower(camelHump.ReplaceAllString(name, "$1 $2"))
	switch {
	case educationWord.MatchString(words):
		label = "Education"
	case employerWord.MatchString(words):
		label = "Employer"
	case experienceWrd.MatchString(words):
		label = "Experience"
	default:
		return "", 0, false
	}
	last := ""
	for i := 0; i < len(name); i++ {
		if name[i] != '_' {
			continue
		}
		j := i + 1
		for j < len(name) && name[j] >= '0' && name[j] <= '9' {
			j++
		}
		if j > i+1 && (j == len(name) || name[j] == '_') {
			last = name[i+1 : j]
		}
	}
	n, err := strconv.Atoi(last)
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return label, n, true
}

// historySection reads Workday's "education-<N>--field" and
// "workExperience-<N>--field" row ids, and the "<entity> history" spellings of
// other boards. The row marker repeats the number the name states, without
// choosing a base: it only has to make the matcher see a repeated row.
func historySection(name string) []string {
	if m := nestedRow.FindStringSubmatch(name); m != nil {
		section := "Work History"
		if strings.HasPrefix(m[1], "education") {
			section = "Education History"
		}
		if n := digits.FindString(m[2]); n != "" {
			return []string{section, "Row " + n}
		}
		return []string{section}
	}
	if m := workdayRow.FindStringSubmatch(name); m != nil {
		section := "Work History"
		if m[1] == "education" {
			section = "Education History"
		}
		return []string{section, "Row " + m[2]}
	}
	words := strings.ToLower(separators.ReplaceAllString(camelHump.ReplaceAllString(name, "$1 $2"), " "))
	var section string
	switch {
	case educationHist.MatchString(words):
		section = "Education History"
	case workHist.MatchString(words):
		section = "Work History"
	default:
		return nil
	}
	nums := trailingNum.FindAllStringSubmatch(words, -1)
	if len(nums) == 0 {
		return []string{section}
	}
	return []string{section, "Row " + nums[len(nums)-1][1]}
}

// controlNameFromSelector reads the id or name the engine's last selector
// segment states: [id="x"], #x, [name="x"] or a bare name. A structural path
// states no name and returns "": guessing one would invent evidence.
func controlNameFromSelector(selector string) string {
	raw := strings.TrimSpace(selector)
	if i := strings.LastIndex(raw, " >>> "); i >= 0 {
		raw = strings.TrimSpace(raw[i+len(" >>> "):])
	}
	if raw == "" {
		return ""
	}
	if m := idAttr.FindStringSubmatch(raw); m != nil {
		return cssEscape.ReplaceAllString(m[1], "$1")
	}
	if m := idSelector.FindStringSubmatch(raw); m != nil {
		return cssEscape.ReplaceAllString(m[1], "$1")
	}
	if m := nameAttr.FindStringSubmatch(raw); m != nil && !strings.Contains(raw, " > ") {
		return strings.TrimSpace(m[1] + m[2] + m[3])
	}
	if bareName.MatchString(raw) {
		return raw
	}
	return ""
}
