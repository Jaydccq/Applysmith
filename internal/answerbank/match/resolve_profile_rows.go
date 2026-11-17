package match

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Education and work-history row controls. A control that belongs to an
// identified row is answered only from that row of the profile history and
// never from a global bank entry: a row the profile cannot back is the
// terminal section-policy abstain, so no entry, model lane or learning
// candidate fills it.

// catalogOptionFloor is the option count from which a list that matches
// nothing is read as a truncated searchable catalog rather than a closed set.
const catalogOptionFloor = 40

// catalogPageFloor is the option count from which a list in alphabetical order
// that ends before the value is read as the first page of a searchable catalog:
// Greenhouse's school list shows 25 schools until something is typed.
const catalogPageFloor = 20

// rowMatchFloor is the lowest option score a row value may bind at.
const rowMatchFloor = 0.5

// eduField is the profile field behind an education row control.
type eduField int

const (
	// eduNoField marks a recognized row control the profile has no value for
	// (a summary box, a school location).
	eduNoField eduField = iota
	eduSchool
	eduDegree
	eduFieldOfStudy
	eduStartMonth
	eduStartYear
	eduEndMonth
	eduEndYear
	// eduStartDate and eduEndDate are a whole date box, eduGPA the row's
	// grade average.
	eduStartDate
	eduEndDate
	eduGPA
)

// eduRowRef names one control of the Nth education row (0-based).
type eduRowRef struct {
	index int
	field eduField
}

var (
	greenhouseRowRE = lazyregexp.New(`(?i)^(school|institution|university|college|degree|discipline|major|field-of-study|start-month|start-year|end-month|end-year)--(\d+)$`)
	kulaRowRE       = lazyregexp.New(`(?i)^profile\.education\[(\d+)\]\.([a-z_]+)$`)
	workdayDegreeRE = lazyregexp.New(`(?i)^education-\d+--degree$`)
	ultiProRowRE    = lazyregexp.New(`(?i)^NewWorkExperience_(Organization|JobTitle|FromMonth|FromYear)(\d+)$`)
	// greenhouseWorkRowRE is a numbered control of Greenhouse's employment
	// block (company-name-0, start-date-month-0, current-role-0_1); its
	// education block numbers with a double dash (school--0, start-year--0).
	greenhouseWorkRowRE = lazyregexp.New(`^(company-name|title|start-date-month|start-date-year|end-date-month|end-date-year|current-role)-(\d{1,2})(?:_\d+)?$`)
	sectionRowRE        = lazyregexp.New(`(?i)^education\s+(\d+)$`)
	rowParentheticalRE  = lazyregexp.New(`\([^)]*\)`)
	// nameRowRE is Citadel's row control, named by attribute:
	// input[name="profile[education][new0][from]"].
	nameRowRE = lazyregexp.New(`(?i)\[name="profile\[education\]\[new(\d+)\]\[([a-z_]+)\](?:\[([a-z_]+)\])?"\]`)
)

// rowIndex reads a row number; a number too large for an int is an orphan row
// whatever the profile holds.
func rowIndex(digits string) int {
	n, err := strconv.Atoi(digits)
	if err != nil {
		return 1 << 30
	}
	return n
}

var greenhouseFields = map[string]eduField{
	"school": eduSchool, "institution": eduSchool, "university": eduSchool, "college": eduSchool,
	"degree": eduDegree, "discipline": eduFieldOfStudy, "major": eduFieldOfStudy, "field-of-study": eduFieldOfStudy,
	"start-month": eduStartMonth, "start-year": eduStartYear, "end-month": eduEndMonth, "end-year": eduEndYear,
}

// nameRowFields are the Citadel row controls whose answer is the row's own:
// its dates and its grade average (two rows had both taken the latest GPA).
// The scale beside the GPA is a field the profile does not record. School,
// degree and major stay with the lanes that already answer them.
var nameRowFields = map[string]eduField{
	"from": eduStartDate, "to": eduEndDate, "degree_measure.value": eduGPA, "degree_measure.highest_value": eduNoField,
}

var kulaFields = map[string]eduField{
	"institution": eduSchool, "school": eduSchool, "degree": eduDegree, "discipline": eduFieldOfStudy,
}

// selectorID is the element id a control's selector names: the engine writes
// `[id="..."]`, a hand-written selector `#...`.
func selectorID(selector string) string {
	s := strings.TrimSpace(selector)
	if inner, ok := strings.CutPrefix(s, `[id="`); ok {
		if v, ok := strings.CutSuffix(inner, `"]`); ok {
			return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(v)
		}
	}
	return strings.TrimPrefix(s, "#")
}

// parseEducationRowSelector reads a Greenhouse `<base>--N`, a Kula
// `profile.education[N].<field>` or a Citadel `[name="profile[education][newN]
// [<field>]"]` selector. The Greenhouse grammar is anchored on its bases so an
// unrelated `field--N` id never matches.
func parseEducationRowSelector(selector string) (eduRowRef, bool) {
	id := selectorID(selector)
	if m := kulaRowRE.FindStringSubmatch(id); m != nil {
		return eduRowRef{index: rowIndex(m[1]), field: kulaFields[strings.ToLower(m[2])]}, true
	}
	if m := greenhouseRowRE.FindStringSubmatch(id); m != nil {
		return eduRowRef{index: rowIndex(m[2]), field: greenhouseFields[strings.ToLower(m[1])]}, true
	}
	if m := nameRowRE.FindStringSubmatch(selector); m != nil {
		key := strings.ToLower(m[2])
		if m[3] != "" {
			key += "." + strings.ToLower(m[3])
		}
		if f, ok := nameRowFields[key]; ok {
			return eduRowRef{index: rowIndex(m[1]), field: f}, true
		}
	}
	return eduRowRef{}, false
}

var sectionRowLabels = []struct {
	re    *lazyregexp.Regexp
	field eduField
}{
	{lazyregexp.New(`^(?:name of (?:the )?)?(?:school|institution|university|college)(?: or (?:university|college))?(?: name)?$`), eduSchool},
	{lazyregexp.New(`^degree(?: type| name| level)?$`), eduDegree},
	{lazyregexp.New(`^(?:major|discipline|(?:field|area) of study)$`), eduFieldOfStudy},
	{lazyregexp.New(`^(?:from|start)(?: date)? month$`), eduStartMonth},
	{lazyregexp.New(`^(?:from|start)(?: date)? year$`), eduStartYear},
	{lazyregexp.New(`^(?:to|end|graduation)(?: date)? month$`), eduEndMonth},
	{lazyregexp.New(`^(?:to|end|graduation)(?: date)? year$`), eduEndYear},
}

// parseEducationSectionRow reads a control of the Nth education row named by
// its section heading ("Education 2", 1-based) rather than by its id. Only a
// bare row label binds: a longer question asked inside the section takes the
// normal routes.
func parseEducationSectionRow(q Question) (eduRowRef, bool) {
	row := 0
	for _, part := range q.SectionPath {
		if m := sectionRowRE.FindStringSubmatch(strings.TrimSpace(part)); m != nil {
			row = rowIndex(m[1])
			break
		}
	}
	if row < 1 {
		return eduRowRef{}, false
	}
	label := normalizeLabel(collapseSpaces(rowParentheticalRE.ReplaceAllString(q.Text, " ")))
	for _, l := range sectionRowLabels {
		if l.re.MatchString(label) {
			return eduRowRef{index: row - 1, field: l.field}, true
		}
	}
	return eduRowRef{}, false
}

// isEducationRowControl reports a control that belongs to an identified
// education row, which the date and school lanes must leave to the row resolver.
func isEducationRowControl(q Question) bool {
	if _, ok := parseEducationRowSelector(q.Selector); ok {
		return true
	}
	_, ok := parseEducationSectionRow(q)
	return ok
}

// educationRowText is the text a row control holds and, for a month field, the
// month number.
func educationRowText(e EducationFact, f eduField) (string, int) {
	switch f {
	case eduSchool:
		return strings.TrimSpace(e.School), 0
	case eduDegree:
		return strings.TrimSpace(e.Degree), 0
	case eduFieldOfStudy:
		return strings.TrimSpace(e.Field), 0
	case eduStartMonth, eduStartYear:
		return yearMonthText(e.Start, f == eduStartMonth)
	case eduEndMonth, eduEndYear:
		return yearMonthText(e.End, f == eduEndMonth)
	case eduStartDate, eduEndDate:
		v := e.Start
		if f == eduEndDate {
			v = e.End
		}
		if ym, ok := parseYearMonth(v); ok {
			return fmt.Sprintf("%04d-%02d-01", ym.Year, ym.Month), 0
		}
	case eduGPA:
		return strings.TrimSpace(e.GradeAverage), 0
	}
	return "", 0
}

// yearMonthText is the month name and number, or the year, of a profile month.
func yearMonthText(s string, month bool) (string, int) {
	ym, ok := parseYearMonth(s)
	if !ok {
		return "", 0
	}
	if month {
		return MonthName(ym.Month), ym.Month
	}
	return strconv.Itoa(ym.Year), 0
}

// rowValue is the profile value of one row control and how it binds options.
type rowValue struct {
	text string
	// month is the month number for a month field, else 0.
	month int
	// scoreAs are the answers tried against the options, best score wins.
	scoreAs []string
	// catalog marks a field backed by a searchable list of every school, field,
	// employer or title: only these fall back to type-and-pick.
	catalog bool
	label   string
}

func rowDraft(answer string, trace string) Draft {
	d := derivedDraft(answer, "", startProfileRow, trace)
	d.ChosenLabel = answer
	d.Deterministic = true
	return d
}

// sameMonthOption finds the option naming the month, by number, name or
// abbreviation ("Aug" for August).
func sameMonthOption(opts []Option, month int) (Option, bool) {
	for _, o := range realOptions(opts) {
		if monthFromWord(o.Label) == month {
			return o, true
		}
	}
	return Option{}, false
}

// bindRowControl answers a row control from its value: the option naming the
// same month, else the best option scoring at least 0.5; a catalog control
// that matches nothing among 40 or more options, or whose options are a first
// page the value lies beyond, gets the value for the engine's type-and-pick; a
// control with no options gets the value.
func bindRowControl(s *Step, rv rowValue) Draft {
	opts := realOptions(s.Q.Options)
	if len(opts) == 0 {
		return rowDraft(rv.text, rv.label+" -> "+strconv.Quote(rv.text))
	}
	if rv.month > 0 {
		if o, ok := sameMonthOption(opts, rv.month); ok {
			return rowDraft(o.Label, rv.label+": chose the option naming the same month "+strconv.Quote(o.Label))
		}
	}
	var best optionBinding
	found := false
	for _, a := range rv.scoreAs {
		if b, ok := bindOption(s.bareScorer(), opts, a); ok && b.Score >= rowMatchFloor && (!found || b.Score > best.Score) {
			best, found = b, true
		}
	}
	if found {
		return rowDraft(best.Label, rv.label+": chose option "+strconv.Quote(best.Label))
	}
	if rv.catalog && len(opts) >= catalogOptionFloor {
		return rowDraft(rv.text, rv.label+": no option scored 0.5 across "+strconv.Itoa(len(opts))+" catalog options -> type-and-pick "+strconv.Quote(rv.text))
	}
	if rv.catalog && firstPageBefore(opts, rv.text) {
		return rowDraft(rv.text, rv.label+": "+strconv.Itoa(len(opts))+" options in alphabetical order end before "+strconv.Quote(rv.text)+", the first page of a catalog -> type-and-pick")
	}
	return abstainDraft(ReasonInputMismatch, "", "%s: no option binds %q; the row control stays on its own row", rv.label, rv.text)
}

// firstPageBefore reports at least catalogPageFloor options in alphabetical
// order whose last one sorts before the value: the first page of a catalog,
// not a closed list that leaves the value out.
func firstPageBefore(opts []Option, value string) bool {
	if len(opts) < catalogPageFloor {
		return false
	}
	keys := make([]string, len(opts))
	for i, o := range opts {
		keys[i] = strings.ToLower(strings.TrimSpace(o.Label))
	}
	return slices.IsSorted(keys) && keys[len(keys)-1] < strings.ToLower(strings.TrimSpace(value))
}

// educationRowDraft resolves one education row control, or abstains with the
// terminal section-policy reason when the profile cannot back it.
func educationRowDraft(s *Step, ref eduRowRef) Draft {
	where := "education row " + strconv.Itoa(ref.index)
	if ref.field == eduNoField {
		return abstainDraft(ReasonSectionPolicy, "", "%s control %s has no education field behind it -> abstain", where, s.Raw.Selector)
	}
	rows := orderedEducation(s.Req.Facts)
	if ref.index >= len(rows) {
		return abstainDraft(ReasonSectionPolicy, "", "%s control %s: the profile has no such education entry -> abstain (orphan row)", where, s.Raw.Selector)
	}
	text, month := educationRowText(rows[ref.index], ref.field)
	if text == "" {
		return abstainDraft(ReasonSectionPolicy, "", "%s control %s: the profile entry has no value for it -> abstain (orphan row)", where, s.Raw.Selector)
	}
	rv := rowValue{text: text, month: month, scoreAs: []string{text}, label: "education-row " + s.Raw.Selector}
	switch ref.field {
	case eduDegree:
		if d, ok := sameLevelDegree(s, text, rv.label); ok {
			return d
		}
		if lvl := degreeLevel(text); lvl != "" {
			rv.scoreAs = []string{lvl}
		}
	case eduSchool, eduFieldOfStudy:
		rv.catalog = true
	}
	return bindRowControl(s, rv)
}

// degreeNameWords are the words of a degree option that name no subject: the
// levels, their joins and the generic "or Equivalent First-Degree" phrasing.
var degreeNameWords = setOf("master", "masters", "bachelor", "bachelors", "doctor", "doctors", "doctorate", "doctoral",
	"associate", "associates", "phd", "s", "degree", "degrees", "or", "of", "in", "and", "the", "equivalent",
	"first", "second", "third", "cycle", "short", "tertiary", "qualification", "level", "isced")

// subjectAlternatives are the words of a degree option outside its parentheses
// that name a subject or a specific degree ("Business Administration", "MBA"),
// one list per alternative an "or" or a slash joins: "Masters of Arts or
// Science or Business Administration" names arts, science, and business
// administration. An alternative of level words alone is empty.
func subjectAlternatives(label string) [][]string {
	alts := [][]string{nil}
	for _, w := range tokenize(apostrophes.Replace(strings.ReplaceAll(parenRE.ReplaceAllString(label, ""), "/", " or "))) {
		switch _, err := strconv.Atoi(w); {
		case w == "or":
			alts = append(alts, nil)
		case err != nil && !degreeNameWords[w]:
			alts[len(alts)-1] = append(alts[len(alts)-1], w)
		}
	}
	return alts
}

// sameLevelDegree chooses among several options of the degree's level: an
// option qualifies when every subject word of one of its alternatives is in
// the profile's own degree (its initials joined, so "M.S" holds "MS"); the
// qualifying option naming the most subject words wins, else the one generic
// option; otherwise nothing is chosen, since another degree's name is a false
// statement of qualification. One option of the level, or none, is left to the
// level binding.
func sameLevelDegree(s *Step, text, label string) (Draft, bool) {
	lvl := degreeLevel(text)
	if lvl == "" {
		return Draft{}, false
	}
	var same []Option
	for _, o := range realOptions(s.Q.Options) {
		if degreeLevel(o.Label) == lvl {
			same = append(same, o)
		}
	}
	if len(same) < 2 {
		return Draft{}, false
	}
	own := map[string]bool{}
	initials := ""
	for _, w := range tokenize(text) {
		own[w] = true
		if len(w) == 1 {
			initials += w
		}
	}
	own[initials] = true
	var best Option
	var generic []Option
	bestN, ties := 0, 0
	for _, o := range same {
		n, named := 0, false
		for _, alt := range subjectAlternatives(o.Label) {
			named = named || len(alt) > 0
			if len(alt) > n && !slices.ContainsFunc(alt, func(w string) bool { return !own[w] }) {
				n = len(alt)
			}
		}
		switch {
		case !named:
			generic = append(generic, o)
		case n == 0:
			continue
		case n > bestN:
			best, bestN, ties = o, n, 1
		case n == bestN:
			ties++
		}
	}
	switch {
	case bestN > 0 && ties == 1:
		return rowDraft(best.Label, label+": chose the option naming this degree "+strconv.Quote(best.Label)), true
	case bestN == 0 && len(generic) == 1:
		return rowDraft(generic[0].Label, label+": chose the level's generic option "+strconv.Quote(generic[0].Label)), true
	}
	return abstainDraft(ReasonInputMismatch, "", "%s: several %s options, none names %q alone; another degree's name is never chosen", label, lvl, text), true
}

// identifiedEducationRow answers a Greenhouse or Kula education row control.
func identifiedEducationRow(s *Step) (Draft, bool) {
	ref, ok := parseEducationRowSelector(s.Raw.Selector)
	if !ok {
		return Draft{}, false
	}
	return educationRowDraft(s, ref), true
}

// sectionHeadingEducationRow answers a control of an "Education N" section.
func sectionHeadingEducationRow(s *Step) (Draft, bool) {
	ref, ok := parseEducationSectionRow(s.Q)
	if !ok {
		return Draft{}, false
	}
	return educationRowDraft(s, ref), true
}

// opaqueWorkdayDegree abstains on a Workday per-row Degree control: its row
// number is a widget id, so the entry it belongs to cannot be identified, and
// a wrong degree level is a false statement of qualification.
func opaqueWorkdayDegree(s *Step) (Draft, bool) {
	if !workdayDegreeRE.MatchString(selectorID(s.Raw.Selector)) {
		return Draft{}, false
	}
	return abstainDraft(ReasonSectionPolicy, "", "workday education row %s: the row number is a widget id, the degree entry cannot be identified -> abstain", s.Raw.Selector), true
}

// workField is the profile field behind an UltiPro work row control.
type workField int

const (
	workEmployer workField = iota
	workTitle
	workStartMonth
	workStartYear
)

func workRowText(e EmploymentFact, f workField) (string, int) {
	switch f {
	case workEmployer:
		return strings.TrimSpace(e.Employer), 0
	case workTitle:
		return strings.TrimSpace(e.Title), 0
	}
	return yearMonthText(e.Start, f == workStartMonth)
}

// identifiedWorkRow answers an UltiPro work-experience row control from the
// row of history.employment in profile order. Workday's own ids are not read:
// their number is the widget's, not a row position.
func identifiedWorkRow(s *Step) (Draft, bool) {
	m := ultiProRowRE.FindStringSubmatch(selectorID(s.Raw.Selector))
	if m == nil {
		return Draft{}, false
	}
	field := map[string]workField{"organization": workEmployer, "jobtitle": workTitle, "frommonth": workStartMonth, "fromyear": workStartYear}[strings.ToLower(m[1])]
	idx := rowIndex(m[2])
	jobs := s.Req.Facts.Employment
	if idx >= len(jobs) {
		return abstainDraft(ReasonSectionPolicy, "", "work row %s: the profile has no such employment entry -> abstain (orphan row)", s.Raw.Selector), true
	}
	text, month := workRowText(jobs[idx], field)
	if text == "" {
		return abstainDraft(ReasonSectionPolicy, "", "work row %s: the profile entry has no value for it -> abstain (orphan row)", s.Raw.Selector), true
	}
	rv := rowValue{text: text, month: month, scoreAs: []string{text}, label: "work-experience-row " + s.Raw.Selector,
		catalog: field == workEmployer || field == workTitle}
	if month > 0 {
		// The month picker may label its options with three letters.
		rv.scoreAs = []string{text, text[:3]}
	}
	return bindRowControl(s, rv), true
}

// greenhouseWorkLabels are the labels Greenhouse gives the controls of an
// employment row; the id and the label together name the field.
var greenhouseWorkLabels = map[string]string{
	"company-name": "company name", "title": "title", "start-date-month": "start date month", "start-date-year": "start date year",
	"end-date-month": "end date month", "end-date-year": "end date year", "current-role": "current role",
}

// greenhouseWorkRow answers a control of Greenhouse's employment block from
// the row of history.employment in profile order (Lyft, Dropbox): the
// employer, the title, the start month and year, a past job's end month and
// year, and Current role, ticked for a current job, whose end boxes are left
// to it. The latest school's dates never answer an employment row.
func greenhouseWorkRow(s *Step) (Draft, bool) {
	m := greenhouseWorkRowRE.FindStringSubmatch(selectorID(s.Raw.Selector))
	if m == nil || normalizeLabel(s.Raw.Text) != greenhouseWorkLabels[m[1]] {
		return Draft{}, false
	}
	jobs := s.Req.Facts.Employment
	idx := rowIndex(m[2])
	if idx >= len(jobs) {
		return abstainDraft(ReasonSectionPolicy, "", "work row %s: the profile has no such employment entry -> abstain (orphan row)", s.Raw.Selector), true
	}
	job := jobs[idx]
	current := job.OngoingEnd()
	var rv rowValue
	switch m[1] {
	case "current-role":
		answer := "No"
		if current {
			answer = "Yes"
		}
		return derivedDraft(answer, "", startProfileRow, "work row "+s.Raw.Selector+": the job is current="+boolWord(current)+" -> "+answer), true
	case "end-date-month", "end-date-year":
		if current {
			return abstainDraft(ReasonSectionPolicy, "", "work row %s: the job is current; Current role stands for its end -> left blank", s.Raw.Selector), true
		}
		rv.text, rv.month = yearMonthText(job.End, m[1] == "end-date-month")
	case "start-date-month", "start-date-year":
		rv.text, rv.month = yearMonthText(job.Start, m[1] == "start-date-month")
	case "company-name":
		rv.text, rv.catalog = strings.TrimSpace(job.Employer), true
	case "title":
		rv.text, rv.catalog = strings.TrimSpace(job.Title), true
	}
	if rv.text == "" {
		return abstainDraft(ReasonSectionPolicy, "", "work row %s: the profile entry has no value for it -> abstain (orphan row)", s.Raw.Selector), true
	}
	rv.scoreAs, rv.label = []string{rv.text}, "greenhouse-work-row "+s.Raw.Selector
	if rv.month > 0 {
		rv.scoreAs = []string{rv.text, rv.text[:3]}
	}
	return bindRowControl(s, rv), true
}

// bareWorkFields are the bare labels of a work-experience block whose rows
// carry no number (SmartRecruiters' Experience: Title, Company, From, To).
var bareWorkFields = map[string]workField{
	"title": workTitle, "job title": workTitle, "position title": workTitle,
	"company": workEmployer, "company name": workEmployer, "employer": workEmployer, "employer name": workEmployer,
}

func bareWorkField(q Question) (workField, bool) {
	if !workBlockBox(q) {
		return 0, false
	}
	f, ok := bareWorkFields[normalizeLabel(q.Text)]
	return f, ok
}

// workBlockBox is a free-text box outside any section but the work one.
func workBlockBox(q Question) bool {
	if q.Kind != KindText || len(q.Options) > 0 {
		return false
	}
	k := classifySection(q.SectionPath)
	return k == sectionNone || k == sectionWork
}

// numberedEmployerRE is a company box that carries its row's number (GoodLeap:
// "Company 1", "Company 2", each before a bare "Title").
var numberedEmployerRE = lazyregexp.New(`(?i)^\W*(?:company|employer|organi[sz]ation)(?:\s+name)?\s*#?\s*([1-9])\W*$`)

// numberedEmployer is the row a numbered company box names, counted from 0.
func numberedEmployer(q Question) (int, bool) {
	if !workBlockBox(q) {
		return 0, false
	}
	m := numberedEmployerRE.FindStringSubmatch(q.Text)
	if m == nil {
		return 0, false
	}
	return int(m[1][0] - '1'), true
}

// pairedWorkRow answers a bare Title or Company box of an unnumbered work
// block. A page that holds both a bare title box and a bare company box is a
// block of employment rows; the Nth box of a field is the Nth job of
// history.employment in profile order, as identifiedWorkRow reads it. A
// company box may carry its row's number instead ("Company 2"), which names
// its job. A lone box of one field is not a block, and a box past the history
// is the orphan row.
func pairedWorkRow(s *Step) (Draft, bool) {
	field, ok := bareWorkField(s.Raw)
	nth := -1
	if row, numbered := numberedEmployer(s.Raw); !ok && numbered {
		field, ok, nth = workEmployer, true, row
	}
	if !ok {
		return Draft{}, false
	}
	count := map[workField]int{}
	for _, q := range s.Req.Questions {
		if _, numbered := numberedEmployer(q); numbered {
			count[workEmployer]++
			continue
		}
		f, ok := bareWorkField(q)
		if !ok {
			continue
		}
		if f == field && q.Selector == s.Raw.Selector {
			nth = count[f]
		}
		count[f]++
	}
	if nth < 0 || count[workTitle] == 0 || count[workEmployer] == 0 {
		return Draft{}, false
	}
	jobs := s.Req.Facts.Employment
	if nth >= len(jobs) {
		return abstainDraft(ReasonSectionPolicy, "", "work block %s: row %d has no employment entry -> abstain (orphan row)", s.Raw.Selector, nth+1), true
	}
	text, _ := workRowText(jobs[nth], field)
	if text == "" {
		return abstainDraft(ReasonSectionPolicy, "", "work block %s: employment entry %d has no value for it -> abstain (orphan row)", s.Raw.Selector, nth+1), true
	}
	return bindRowControl(s, rowValue{text: text, scoreAs: []string{text}, label: "work-experience-block " + s.Raw.Selector}), true
}
