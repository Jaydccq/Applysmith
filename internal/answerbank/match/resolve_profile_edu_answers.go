package match

import (
	"math"
	"slices"
	"strconv"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Field of study, current school and grade-average answers from history.education.

var (
	fieldOfStudyRE  = lazyregexp.New(`(?i)\b(?:field|area|course)\s+of\s+study\b|\b(?:degree|college|university|academic|undergrad\w*|postgrad\w*|graduate|bachelor'?s?|master'?s?|ph\.?\s?d|doctora\w*|studied|studying)\b[^?!]{0,30}\b(?:major|discipline)\b|\b(?:major|discipline)\b[^?!]{0,30}\b(?:degree|study|studied|studying|college|university)\b`)
	describeProgRE  = lazyregexp.New(`(?i)\b(?:describe|tell\s+us\s+(?:about|more)|explain)\b|\bin\s+(?:\d|one|two|a\s+few)\b[^?.]{0,8}\bsentences?\b`)
	currentSchoolRE = lazyregexp.New(`(?i)\b(?:currently\s+attend(?:ing)?|current(?:ly)?\s+(?:university|school|college|institution)|re-?confirm\s+the\s+(?:university|school|college|institution))\b`)
	// schoolNameAskRE is a label that asks only for a school's name (Equifax's
	// "Enter school name below:" after its school list's "Other").
	schoolNameAskRE = lazyregexp.New(`(?i)^\W*(?:please\s+)?(?:enter|type|provide|write|list)?\s*(?:the\s+|your\s+)?(?:school|university|college)(?:'s)?\s+name(?:\s+below)?\W*$`)
)

// majorAdjectiveRE is "major" as an adjective ("a major project"), which names
// no field of study.
var majorAdjectiveRE = lazyregexp.New(`(?i)\bmajor\s+(?:projects?|challenges?|accomplishments?|achievements?|contributions?|obstacles?|problems?|issues?|decisions?|initiatives?|milestones?|responsibilit(?:y|ies)|roles?|impact|changes?|events?|setbacks?|failures?|success(?:es)?|goals?|factors?|reasons?|influences?)\b`)

var (
	fieldUndergradRE = lazyregexp.New(`\b(?:undergraduate|undergrad|bachelor'?s?|b\.?\s?s|b\.?\s?a)\b`)
	fieldGradRE      = lazyregexp.New(`\b(?:graduate|postgrad\w*|master'?s?|m\.?\s?s|m\.?\s?a|m\.?eng|mba|ph\.?\s?d|doctoral|doctorate)\b`)
)

// fieldScope reads the degree a field-of-study question names, including the
// degree initials a program name carries ("my MS program").
func fieldScope(lower string) (undergrad, grad bool) {
	return fieldUndergradRE.MatchString(lower), fieldGradRE.MatchString(lower)
}

// bindEducationValue answers with a stored value on a control: the best option
// scoring at least 0.5, or the value on a control with no options. A control
// whose options take no part of the value is left to the later lanes.
func bindEducationValue(s *Step, value, answerKey, trace string, start float64) (Draft, bool) {
	opts := realOptions(s.Q.Options)
	answer := value
	if len(opts) > 0 {
		b, ok := bindOption(s.bareScorer(), opts, value)
		if !ok || b.Score < rowMatchFloor {
			return Draft{}, false
		}
		answer = b.Label
		trace += "; chose option " + strconv.Quote(b.Label)
	}
	d := derivedDraft(answer, answerKey, start, trace)
	d.ChosenLabel = answer
	d.Deterministic = true
	return d, true
}

// fieldOfStudy answers a "field of study" or major question from the entry
// the wording names: undergraduate wording the bachelor entry, graduate
// wording the master or doctorate entry, otherwise the most recent. A level no
// entry holds borrows no other degree's field.
func fieldOfStudy(s *Step) (Draft, bool) {
	rows := orderedEducation(s.Req.Facts)
	if len(rows) == 0 || isEducationRowControl(s.Q) || !fieldOfStudyRE.MatchString(majorAdjectiveRE.ReplaceAllString(s.Q.Text, "")) {
		return Draft{}, false
	}
	under, grad := fieldScope(strings.ToLower(s.Q.Text))
	entry, lane, ok := rows[0], "most recent", true
	switch {
	case under:
		lane = "undergraduate"
		entry, ok = educationOfScope(rows, true, false)
	case grad:
		lane = "graduate"
		entry, ok = educationOfScope(rows, false, doctoralScope(s.Q.Text))
	}
	if !ok {
		return Draft{}, false
	}
	value := strings.TrimSpace(entry.Field)
	if value == "" {
		return Draft{}, false
	}
	trace := "field-of-study (" + lane + "): " + strconv.Quote(value)
	if len(realOptions(s.Q.Options)) == 0 {
		if sentence, ok := describeProgram(s.Q.Text, entry, value, s.Req.Today); ok {
			d := derivedDraft(sentence, "", startProfileRow, trace+"; describe-your-program wording")
			d.ChosenLabel = sentence
			d.Deterministic = true
			return d, true
		}
	}
	return bindEducationValue(s, value, "", trace, startProfileRow)
}

// describeProgram writes one sentence from a single education entry: degree,
// school, dates and field, and nothing it does not state. The tense comes from
// the clock, not from the question's wording.
func describeProgram(question string, e EducationFact, field string, today Date) (string, bool) {
	if !describeProgRE.MatchString(question) {
		return "", false
	}
	degree := strings.TrimSpace(e.Degree)
	if degree == "" {
		degree = "degree"
	}
	when := func(stamp string) string {
		if ym, ok := parseYearMonth(stamp); ok {
			return MonthName(ym.Month) + " " + strconv.Itoa(ym.Year)
		}
		return ""
	}
	var span []string
	for _, p := range []string{when(e.Start), when(e.End)} {
		if p != "" {
			span = append(span, p)
		}
	}
	end, hasEnd := parseYearMonth(e.End)
	verb := "I am pursuing my"
	if hasEnd && graduated(end, today) {
		verb = "I completed my"
	}
	out := verb + " " + degree
	if school := strings.TrimSpace(e.School); school != "" {
		out += " at " + school
	}
	if len(span) > 0 {
		out += " (" + strings.Join(span, " \u2013 ") + ")"
	}
	return out + ", with " + field + " as my primary area of study.", true
}

// currentSchool answers "which university do you currently attend" from the
// in-progress entry, else the most recent, and so does a box outside the
// education rows that asks only for a school's name. A Yes/No select binds no
// option and is left alone; a list of schools is read by schoolListPick.
func currentSchool(s *Step) (Draft, bool) {
	rows := orderedEducation(s.Req.Facts)
	if len(rows) == 0 || isEducationRowControl(s.Q) || !currentSchoolRE.MatchString(s.Q.Text) && !bareSchoolNameAsk(s.Q) {
		return Draft{}, false
	}
	value := strings.TrimSpace(rows[0].School)
	if value == "" {
		return Draft{}, false
	}
	trace := "current-school: " + strconv.Quote(value)
	if d, ok, list := schoolListPick(s, value, trace); list {
		return d, ok
	}
	return bindEducationValue(s, value, "", trace, startProfileRow)
}

var (
	// graduatedSchoolRE asks which school the applicant graduated from
	// (Reducto: "What institution(s) did you graduate from?"); group 1 is the
	// school word, plural when it asks for all of them.
	graduatedSchoolRE = lazyregexp.New(`(?i)\b(?:what|which)\s+((?:institution|school|universit(?:y|ie)|college)s?(?:\s*\((?:s|ies)\))?)\s+(?:did|have)\s+you\s+graduat\w*\s+from\b`)
	pluralSchoolRE    = lazyregexp.New(`(?i)s\b|\((?:s|ies)\)`)
)

// graduatedSchool answers that in a text box from the education entries that
// have ended against the clock, most recent first: every such school for a
// plural ask, the most recent for a singular one. An education in progress is
// no school graduated from; an entry without an end month, or none ended,
// leaves it to the user; with no education on record the school entry answers.
// A question naming a degree level is another lane's.
func graduatedSchool(s *Step) (Draft, bool) {
	m := graduatedSchoolRE.FindStringSubmatch(s.Q.Text)
	if m == nil || len(s.Req.Facts.Education) == 0 || s.Q.Kind != KindText || len(realOptions(s.Q.Options)) > 0 || isEducationRowControl(s.Q) || degreeLevel(s.Q.Text) != "" {
		return Draft{}, false
	}
	var schools []string
	for _, e := range orderedEducation(s.Req.Facts) {
		end, ok := parseYearMonth(e.End)
		if !ok {
			return abstainDraft(ReasonPlaceholderEmpty, "", "graduated-school: %q has no end month, so it cannot be dated -> abstain", e.School), true
		}
		school := strings.TrimSpace(e.School)
		if graduated(end, s.Req.Today) && school != "" && !slices.Contains(schools, school) {
			schools = append(schools, school)
		}
	}
	if len(schools) == 0 {
		return abstainDraft(ReasonAmbiguous, "", "graduated-school: no education has ended -> left for you"), true
	}
	if !pluralSchoolRE.MatchString(m[1]) {
		schools = schools[:1]
	}
	value := strings.Join(schools, "; ")
	return bindEducationValue(s, value, "", "graduated-school: the ended education -> "+strconv.Quote(value), startProfileRow)
}

// educationAttainedRE asks for every education the applicant completed in a
// stated format, the school first and the degree after a separator (AHEAD:
// "Provide All Post-Secondary Education Attained - Formatted as: College Name;
// Degree Obtained ..."); group 1 is the separator.
var educationAttainedRE = lazyregexp.New(`(?i)\b(?:list|provide|enter)\s+(?:all\s+)?(?:(?:of\s+)?your\s+)?(?:post[-\s]?secondary\s+)?(?:education|degrees?)\s+(?:attained|completed|earned|obtained)\b[^?]*?\bformatted\s+as\s*:?\s*(?:college|school|university|institution)\s+name\s*([;,|])\s*degree\b`)

// educationAttained answers that in a text box with one line per education
// that has ended against the clock, most recent first, the school and the
// degree joined by the asked separator. An entry without an end month, or none
// ended, leaves it to the user; a one-line box takes one education only.
func educationAttained(s *Step) (Draft, bool) {
	m := educationAttainedRE.FindStringSubmatch(s.Q.Text)
	if m == nil || len(s.Req.Facts.Education) == 0 || s.Q.Kind != KindText || len(realOptions(s.Q.Options)) > 0 || isEducationRowControl(s.Q) {
		return Draft{}, false
	}
	var lines []string
	for _, e := range orderedEducation(s.Req.Facts) {
		end, ok := parseYearMonth(e.End)
		if !ok {
			return abstainDraft(ReasonPlaceholderEmpty, "", "education-attained: %q has no end month, so it cannot be dated -> abstain", e.School), true
		}
		school, degree := strings.TrimSpace(e.School), strings.TrimSpace(e.Degree)
		if !graduated(end, s.Req.Today) {
			continue
		}
		if school == "" || degree == "" {
			return abstainDraft(ReasonPlaceholderEmpty, "", "education-attained: an ended education lacks its school or degree -> abstain"), true
		}
		lines = append(lines, school+m[1]+" "+degree)
	}
	if len(lines) == 0 {
		return abstainDraft(ReasonAmbiguous, "", "education-attained: no education has ended -> left for you"), true
	}
	if len(lines) > 1 && s.Q.InputType != InputLongText {
		return abstainDraft(ReasonInputMismatch, "", "education-attained: %d educations do not fit a one-line box -> abstain", len(lines)), true
	}
	value := strings.Join(lines, "\n")
	return bindEducationValue(s, value, "", "education-attained: the ended educations as asked -> "+strconv.Quote(value), startProfileRow)
}

var (
	// undergradSchoolAskRE asks the school of the undergraduate degree
	// (Aven: "Which university did you attend for undergrad?"), and
	// undergradSchoolVetoRE asks something else about it.
	undergradSchoolAskRE  = lazyregexp.New(`(?i)\bwhere\b[^?]{0,60}\bundergrad(?:uate)?\b|\b(?:institutions?|schools?|universit(?:y|ies)|colleges?)\b[^?]{0,80}\bundergrad(?:uate)?\b|\bundergrad(?:uate)?\s+(?:institutions?|schools?|universit(?:y|ies)|colleges?)\b`)
	undergradSchoolVetoRE = lazyregexp.New(`(?i)\b(?:major|minor|gpa|grades?|concentration|field\s+of\s+study|when|years?|dates?|live[ds]?|living|located)\b`)
)

// undergradSchool answers a list of schools that asks the undergraduate
// degree's school from the bachelor's row, by the school-list rule
// (schoolListPick): the school word for word, else the list's none-of-these
// choice, else its one "Other". A box is the undergraduate-school entry's.
func undergradSchool(s *Step) (Draft, bool) {
	if len(realOptions(s.Q.Options)) == 0 || isEducationRowControl(s.Q) || !undergradSchoolAskRE.MatchString(s.Q.Text) || undergradSchoolVetoRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	for _, r := range orderedEducation(s.Req.Facts) {
		if degreeNameLevel(r.Degree) != "Bachelor" {
			continue
		}
		school := strings.TrimSpace(r.School)
		if school == "" {
			return Draft{}, false
		}
		d, ok, _ := schoolListPick(s, school, "undergrad-school: "+strconv.Quote(school))
		return d, ok
	}
	return Draft{}, false
}

var (
	// schoolNameRE is an option naming a school ("Kennesaw State University").
	schoolNameRE = lazyregexp.New(`(?i)\b(?:university|college|institute|school|academy|polytechnic)\b`)
	// noneOfTheseRE is a list's own choice for an applicant it does not cover.
	noneOfTheseRE = lazyregexp.New(`(?i)^\s*(?:none(?:\s+of\s+(?:the\s+)?(?:above|these|those|listed))?|not\s+applicable|n/?a|i\s+(?:do\s+not|don't|am\s+not)\s+(?:currently\s+)?(?:attend|attending|enrolled)\b.*)\s*[.!]?\s*$`)
	// otherOptionRE is a list's catch-all "Other" ("Other (please specify)").
	otherOptionRE = lazyregexp.New(`(?i)^\s*other\b`)
	// schoolGenericWords name no school; schoolWeakWords are shared by
	// unrelated schools ("Georgia State" and "Example State").
	schoolGenericWords = setOf("university", "college", "institute", "school", "academy", "polytechnic", "of", "the", "at", "in", "and", "for")
	schoolWeakWords    = setOf("state", "technology", "tech", "science", "sciences", "arts", "community", "city", "national", "saint", "st", "new", "north", "south", "east", "west", "central")
)

// bareSchoolNameAsk reports a box that asks only for a school's name outside
// any education section: inside one ("Education 0", an unknown row) the name
// belongs to a row the profile cannot place.
func bareSchoolNameAsk(q Question) bool {
	if !schoolNameAskRE.MatchString(q.Text) {
		return false
	}
	return !slices.ContainsFunc(q.SectionPath, func(part string) bool { return educationWordRE.MatchString(part) })
}

var educationWordRE = lazyregexp.New(`(?i)\beducation\b`)

// schoolWords are the words that tell a school name from another.
func schoolWords(name string) []string {
	var out []string
	for _, w := range wordRE.FindAllString(foldText(name), -1) {
		if !schoolGenericWords[w] {
			out = append(out, w)
		}
	}
	return out
}

// schoolInitials is the acronym a school name may go by ("MIT" for
// "Massachusetts Institute of Technology"), "" for a name of one word.
func schoolInitials(name string) string {
	var b strings.Builder
	n := 0
	for _, w := range wordRE.FindAllString(foldText(name), -1) {
		if w == "of" || w == "the" || w == "at" || w == "in" || w == "and" || w == "for" {
			continue
		}
		b.WriteString(w[:1])
		n++
	}
	if n < 2 {
		return ""
	}
	return b.String()
}

// sameSchoolMaybe reports two school names that may name one school: a
// telling word in common, or one written as the other's acronym.
func sameSchoolMaybe(a, b string) bool {
	wa, wb := schoolWords(a), schoolWords(b)
	for _, w := range wa {
		if !schoolWeakWords[w] && slices.Contains(wb, w) {
			return true
		}
	}
	ia, ib := schoolInitials(a), schoolInitials(b)
	return ia != "" && slices.Contains(wb, ia) || ib != "" && slices.Contains(wa, ib)
}

// schoolListPick answers a list of schools (two or more options naming one)
// with the option that is the applicant's school word for word. A list that
// does not hold it takes its own none-of-these choice, else its "Other" (the
// owner's rule, Equifax's "Are you currently attending one of the following
// schools?"); a list with an option that may name the school otherwise (a
// telling word in common, an acronym) abstains. list is false for any other
// control.
func schoolListPick(s *Step, school, trace string) (d Draft, ok, list bool) {
	own := schoolWords(school)
	var schools, same, none, other []string
	shared := false
	for _, o := range realOptions(s.Q.Options) {
		switch {
		case schoolNameRE.MatchString(o.Label):
			schools = append(schools, o.Label)
			if len(own) > 0 && slices.Equal(schoolWords(o.Label), own) {
				same = append(same, o.Label)
			}
			shared = shared || sameSchoolMaybe(school, o.Label)
		case noneOfTheseRE.MatchString(o.Label):
			none = append(none, o.Label)
		case otherOptionRE.MatchString(o.Label):
			other = append(other, o.Label)
		}
	}
	if len(schools) < 2 {
		return Draft{}, false, false
	}
	pick, why := same, "the list holds it"
	switch {
	case len(same) > 0:
	case shared:
		return abstainDraft(ReasonAmbiguous, "", "%s; an option shares a word with it but is not it", trace), true, true
	case len(none) > 0:
		pick, why = none, "the list does not hold it"
	default:
		pick, why = other, "the list does not hold it and has no none-of-these choice"
	}
	if len(pick) != 1 {
		return Draft{}, false, true
	}
	d = derivedDraft(pick[0], "", startProfileRow, trace+"; "+why+" -> "+strconv.Quote(pick[0]))
	d.ChosenLabel, d.Deterministic = pick[0], true
	return d, true, true
}

var (
	gpaWordRE     = lazyregexp.New(`(?i)\bgpa\b|\bgrade\s+point\s+average\b`)
	gpaBracketAsk = lazyregexp.New(`(?i)\bgpa\b|\bgrade\s+point\s+average\b|\bacademic\s+performance\b`)
	// gpaScaleAsk asks what the grade average is out of, which the entry does
	// not state: Citadel's "GPA Scale" offers 4.0, 5.0 and 0-100. A question
	// that only states its scale ("GPA (on a 4.0 scale)") still asks the GPA.
	gpaScaleAsk    = lazyregexp.New(`(?i)\b(?:gpa|cgpa|grade\s+point\s+average|grading)\s+(?:scale|system|out\s+of|maximum|max)\b|\bmax(?:imum)?\s+(?:possible\s+)?(?:gpa|grade)\b|\b(?:what|which)\s+(?:is\s+)?(?:the\s+|your\s+)?(?:gpa\s+|grading\s+)?scale\b|\bscale\s+(?:of|for)\s+(?:your\s+|the\s+)?(?:gpa|grade)`)
	gpaAllLevelsRE = lazyregexp.New(`(?i)\bgpa\s*\(\s*undergraduate\s*/\s*graduate\s*/\s*doctorate\s*\)`)
	gpaNumberRE    = lazyregexp.New(`\d+(?:\.\d+)?`)
	// gpaNotApplicableRE is the option a grade-average list offers for a degree
	// the applicant does not hold ("Not applicable/Do not recall", "N/A").
	gpaNotApplicableRE = lazyregexp.New(`(?i)^\s*(?:not\s+applicable|n/a|does\s+not\s+apply)\b`)
	gpaLowRE           = lazyregexp.New(`\b(?:less\s+than|below|under|lower\s+than)\b|<`)
	gpaHighRE          = lazyregexp.New(`\+|\bor\s+(?:above|higher|more|greater)\b|\babove\b|\bat\s+least\b|\bgreater\s+than\b|>|\x{2265}`)
	// gpaInclusiveRE is an open-ended bracket that holds the number it names.
	gpaInclusiveRE = lazyregexp.New(`\+|\bor\s+(?:above|higher|more|greater|equal)\b|\bat\s+least\b|[<>]=|\x{2265}`)
)

// gpaRange is a grade-average range, inclusive unless strict: an open-ended
// bracket that leaves out the number it names ("below 3.0", "> 3.5").
type gpaRange struct {
	low, high float64
	strict    bool
}

func (r gpaRange) holds(v float64) bool {
	return v >= r.low && v <= r.high && !(r.strict && (v == r.low || v == r.high))
}

// parseGpaBracket reads an option label as a range: two numbers are an
// inclusive span, one number with "less than" is open below, with "+", "or
// above" and the like open above, and a lone number is a point. No digits is
// not a bracket.
func parseGpaBracket(label string) (gpaRange, bool) {
	text := strings.ToLower(label)
	var nums []float64
	for _, n := range gpaNumberRE.FindAllString(text, -1) {
		if v, err := strconv.ParseFloat(n, 64); err == nil {
			nums = append(nums, v)
		}
	}
	switch {
	case len(nums) == 0:
		return gpaRange{}, false
	case len(nums) >= 2:
		return gpaRange{low: min(nums[0], nums[1]), high: max(nums[0], nums[1])}, true
	case gpaLowRE.MatchString(text):
		return gpaRange{math.Inf(-1), nums[0], !gpaInclusiveRE.MatchString(text)}, true
	case gpaHighRE.MatchString(text):
		return gpaRange{nums[0], math.Inf(1), !gpaInclusiveRE.MatchString(text)}, true
	}
	return gpaRange{low: nums[0], high: nums[0]}, true
}

// gradeAverageFor picks the grade average the wording names: the degree it
// scopes to, or the most recent entry's when it names none. A question that
// names both an undergraduate and a graduate degree is unscoped here and
// answers nothing. The first result is the first number of the entry's grade
// average, "" when there is none; absent reports that the profile lists
// education but no degree of the level the wording names.
func gradeAverageFor(text string, f Facts) (num, source string, ok, absent bool) {
	rows := orderedEducation(f)
	if len(rows) == 0 {
		return "", "", false, false
	}
	under, grad := levelScope(text)
	if gpaAllLevelsRE.MatchString(text) {
		under, grad = true, false
	}
	if under && grad {
		return "", "", false, false
	}
	entry, label := rows[0], "most recent entry"
	if under || grad {
		e, found := educationOfScope(rows, under, grad && doctoralScope(text))
		if !found {
			return "", "", false, true
		}
		entry = e
		label = "undergraduate entry"
		if grad {
			label = "graduate entry"
		}
	}
	n := gpaNumberRE.FindString(entry.GradeAverage)
	return n, label, n != "", false
}

// gpaBracket answers a grade-average bracket list with the bracket that
// contains the recorded grade average, the tightest range winning on a shared
// edge. It stands down when fewer than two options read as ranges.
func gpaBracket(s *Step) (Draft, bool) {
	opts := realOptions(s.Q.Options)
	if len(opts) < 2 || !gpaBracketAsk.MatchString(s.Q.Text) || gpaScaleAsk.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	num, label, ok, absent := gradeAverageFor(s.Q.Text, s.Req.Facts)
	if absent {
		for _, o := range opts {
			if gpaNotApplicableRE.MatchString(o.Label) {
				d := derivedDraft(o.Label, "", startProfileRow, "gpa-bracket: the profile lists no degree of the level the question names -> "+strconv.Quote(o.Label))
				d.ChosenLabel = o.Label
				d.Deterministic = true
				return d, true
			}
		}
	}
	if !ok {
		return Draft{}, false
	}
	gpa, _ := strconv.ParseFloat(num, 64)
	type bracket struct {
		opt Option
		r   gpaRange
	}
	var all, containing []bracket
	for _, o := range opts {
		if r, parsed := parseGpaBracket(o.Label); parsed {
			all = append(all, bracket{o, r})
		}
	}
	if len(all) < 2 {
		return Draft{}, false
	}
	for _, b := range all {
		if b.r.holds(gpa) {
			containing = append(containing, b)
		}
	}
	if len(containing) == 0 {
		return Draft{}, false
	}
	slices.SortStableFunc(containing, func(a, b bracket) int {
		wa, wb := a.r.high-a.r.low, b.r.high-b.r.low
		switch {
		case wa < wb:
			return -1
		case wa > wb:
			return 1
		}
		return 0
	})
	pick := containing[0].opt
	d := derivedDraft(pick.Label, "", startProfileRow, "gpa-bracket: "+label+" grade average "+num+" -> "+strconv.Quote(pick.Label))
	d.ChosenLabel = pick.Label
	d.Deterministic = true
	return d, true
}

// gpaExplainRE asks for words about the grade average, not the number.
var gpaExplainRE = lazyregexp.New(`(?i)\b(?:explain|explanation|why|describe|elaborate|reasons?|circumstances)\b`)

// scopedGPA answers a GPA text box with the grade-average number of the degree
// the label names, or of the most recent entry when it names none. A box for
// the scale or for an explanation is not the number's.
func scopedGPA(s *Step) (Draft, bool) {
	if s.Q.Kind != KindText || !gpaWordRE.MatchString(s.Q.Text) || gpaScaleAsk.MatchString(s.Q.Text) || gpaExplainRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	num, label, ok, _ := gradeAverageFor(s.Q.Text, s.Req.Facts)
	if !ok {
		return Draft{}, false
	}
	d := derivedDraft(num, "", startProfileRow, "gpa: "+label+" grade average "+num)
	d.Deterministic = true
	return d, true
}
