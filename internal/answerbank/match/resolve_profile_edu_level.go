package match

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// Degree levels as the rows and lanes of the education family read them.

var degreeLevelRules = []struct {
	level string
	re    *lazyregexp.Regexp
}{
	{"Doctorate", lazyregexp.New(`\b(?:ph\.?\s?d|doctors?|doctorate|doctoral|d\.?phil)\b`)},
	{"Master", lazyregexp.New(`\b(?:masters?|m\.?\s?s|m\.?\s?a|m\.?eng|m\.?b\.?a|mba|msc|postgraduate)\b`)},
	{"Bachelor", lazyregexp.New(`\b(?:bachelors?|b\.?\s?s|b\.?\s?a|b\.?eng|bsc|undergraduate)\b`)},
	{"Associate", lazyregexp.New(`\bassociates?\b`)},
	{"High School", lazyregexp.New(`\b(?:high\s+school|secondary\s+school|diploma|ged)\b`)},
}

// degreeAbbrevRules read the abbreviation that opens a degree field ("B.Tech
// Mechanical", "PharmD", "MFin"). They run on the profile's degree only, never
// on question text or option labels: there "be", "me", "md" and "ma" are words
// or state codes. The two-letter forms need capitals or dots for the same
// reason; the longer forms are read in any case.
var degreeAbbrevRules = []struct {
	level string
	re    *lazyregexp.Regexp
}{
	{"Doctorate", lazyregexp.New(`^(?:(?i:pharm\.?\s?d|d\.?v\.?m|d\.?d\.?s|d\.?m\.?d|ed\.?d|dr\.?\s?ph|d\.?phil)|M\.?D|J\.?D|m\.d|j\.d)\.?(?:$|[^\pL])`)},
	{"Master", lazyregexp.New(`^(?:(?i:m\.?\s?phil|m\.?\s?res|m\.?\s?fin|m\.?\s?acc|m\.?s\.?f|m\.?f\.?e|m\.?p\.?h|m\.?\s?tech|m\.?\s?com|m\.?p\.?a|m\.?p\.?p|m\.?s\.?b\.?a|m\.?\s?sc|ll\.?\s?m)|M\.?E|m\.e)\.?(?:$|[^\pL])`)},
	{"Bachelor", lazyregexp.New(`^(?:(?i:b\.?b\.?a|b\.?\s?com|b\.?\s?tech|b\.?\s?sc|b\.?\s?pharm|ll\.?\s?b)|B\.?E|b\.e)\.?(?:$|[^\pL])`)},
}

// degreeNameLevel is the level of the profile's degree: the level words and
// initials degreeLevel reads, else the abbreviation the degree opens with.
func degreeNameLevel(degree string) string {
	if l := degreeLevel(degree); l != "" {
		return l
	}
	return degreeAbbrevLevel(degree)
}

func degreeAbbrevLevel(degree string) string {
	d := strings.TrimSpace(degree)
	for _, r := range degreeAbbrevRules {
		if r.re.MatchString(d) {
			return r.level
		}
	}
	return ""
}

// stemDegreeFieldRE is a field of study on the DHS STEM Designated Degree
// Program List, the federal definition of a STEM degree, among the fields the
// applicants this product serves study. General finance, accounting, business
// and economics are not on the list, so a field of theirs states nothing. The
// list is wider than the fields a question routes to has-stem-degree by
// (pickStemField): a biology degree answers "a STEM degree?", not "a degree in
// statistics?".
var stemDegreeFieldRE = lazyregexp.New(`(?i)^\s*(?:` + pickStemField +
	`|(?:applied\s+)?(?:mathematics|statistics|physics)|biostatistics|biolog(?:y|ical\s+sciences?)|biochemistry|biophysics` +
	`|(?:molecular|cell(?:ular)?)(?:\s+(?:and|&)\s+(?:molecular|cell(?:ular)?))?\s+biology|chemistry|bioinformatics|computational\s+biology` +
	`|neuroscience|epidemiology|pharmaceutical\s+sciences?` +
	`|(?:biomedical|chemical|mechanical|civil|industrial(?:\s+(?:and|&)\s+systems)?)\s+engineering|operations\s+research|actuarial\s+science` +
	`|financial\s+(?:mathematics|engineering|analytics)|mathematical\s+finance|quantitative\s+(?:finance|economics)|econometrics)\b`)

// STEMField reports whether a field of study is a science, technology,
// engineering or mathematics field (stemDegreeFieldRE).
func STEMField(field string) bool { return stemDegreeFieldRE.MatchString(field) }

// degreeLevel reduces a degree name or a question to the level keyword that
// fixed-option lists label by; the first rule wins, and "" means no level.
func degreeLevel(text string) string {
	t := foldText(apostrophes.Replace(text))
	t = strings.ReplaceAll(t, "'", "")
	for _, r := range degreeLevelRules {
		if r.re.MatchString(t) {
			return r.level
		}
	}
	return ""
}

// degreeLevels is every level a text names ("a Bachelor's, Master's or PhD
// degree"), in the order of the rules; "graduate" alone is a master's or a
// doctorate.
func degreeLevels(text string) []string {
	t := strings.ReplaceAll(foldText(apostrophes.Replace(text)), "'", "")
	var out []string
	for _, r := range degreeLevelRules {
		if r.re.MatchString(t) {
			out = append(out, r.level)
		}
	}
	if _, grad := levelScope(text); grad && len(out) == 0 {
		out = []string{"Doctorate", "Master"}
	}
	return out
}

var (
	undergradScopeRE = lazyregexp.New(`(?i)\b(?:undergrad\w*|bachelor'?s?)\b`)
	gradScopeRE      = lazyregexp.New(`(?i)\b(?:postgrad\w*|graduate|master'?s?|doctora\w*|ph\.?\s?d)\b`)
	doctorScopeRE    = lazyregexp.New(`(?i)\b(?:doctora\w*|ph\.?\s?d)\b`)
	masterScopeRE    = lazyregexp.New(`(?i)\b(?:postgrad\w*|graduate|master'?s?)\b`)
)

// doctoralScope reports wording that names a doctorate and no master's or
// general graduate degree: "GPA (Doctorate)" is not about a master's.
func doctoralScope(text string) bool {
	return doctorScopeRE.MatchString(text) && !masterScopeRE.MatchString(text)
}

// graduatePersonRE is "graduate" as a person: after "recent", "college" and
// the like unless the next word (group 1) makes it graduate study by
// graduateStudiesRE ("university graduate program"), and "a graduate" before
// "of", "from", "who" or the end of its clause (group 2).
var graduatePersonRE = lazyregexp.New(`(?i)\b(?:recent|new|college|university|high\s+school)\s+graduates?\b(\s+[a-z]+)?|\ban?\s+graduate\b(\s*[,.?!;:)]|\s+(?:of|from|who)\b|\s*$)`)

// levelScope reads which degree a question names: undergraduate wording, or
// graduate (master, doctoral) wording. Both or neither means unscoped.
// "Graduate" as a verb or a person names no graduate degree.
func levelScope(text string) (undergrad, grad bool) {
	text = graduatePersonRE.ReplaceAllStringFunc(graduateVerbRE.ReplaceAllString(text, "you finish"), func(m string) string {
		if graduateStudiesRE.MatchString(m) {
			return m
		}
		sub := graduatePersonRE.FindStringSubmatch(m)
		return " " + sub[1] + sub[2]
	})
	return undergradScopeRE.MatchString(text), gradScopeRE.MatchString(text)
}

// educationOfScope picks the entry a scoped question is about: the first
// bachelor entry for undergraduate wording, the first master or doctorate
// entry for graduate wording, the first doctorate for doctoral wording, in
// recency order.
func educationOfScope(rows []EducationFact, undergrad, doctoral bool) (EducationFact, bool) {
	for _, r := range rows {
		lvl := degreeNameLevel(r.Degree)
		if (undergrad && lvl == "Bachelor") || (!undergrad && (lvl == "Doctorate" || lvl == "Master" && !doctoral)) {
			return r, true
		}
	}
	return EducationFact{}, false
}
