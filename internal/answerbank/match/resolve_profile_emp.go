package match

import (
	"strconv"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Employment date components, the compound current role and company, and the
// current employer derived from history.employment.

var (
	availabilityAskRE = lazyregexp.New(`(?i)\b(?:available|availability|earliest|soonest|(?:can|could)\s+you\s+(?:start|begin|join)|able\s+to\s+(?:start|begin|join))\b`)
	durationAskRE     = lazyregexp.New(`(?i)\bhow\s+(?:many|long)\b|\b(?:years|months)\b|\bexperience\b`)
	empStartRE        = lazyregexp.New(`(?i)\b(?:start|from|began|begin(?:ning)?|commenc\w*|hired?|joined?)\b`)
	empEndRE          = lazyregexp.New(`(?i)\b(?:end|to|until|left|leaving|departed?|departure|resign\w*|terminat\w*|finish\w*|last\s+day)\b`)
	// stillEmployedRE asks whether the applicant's current or most recent job
	// goes on (Squarespace, after "What is the name of your current or most
	// recent employer?": "Are you currently still employed with this
	// company?"). "Still" presupposes the job, so "this company" is that
	// employer, never the hiring company.
	stillEmployedRE = lazyregexp.New(`(?i)^\s*are\s+you\s+(?:currently\s+|presently\s+)?still\s+(?:employed|working)\b(?:\s+(?:with|at|by|for)\s+(?:this|that|the\s+same)\s+(?:company|employer|organi[sz]ation))?\s*\??\s*$`)
)

// employedAt reports a job that goes on at today: one with no end, or one whose
// recorded end month is still ahead.
func employedAt(e EmploymentFact, today Date) bool {
	if e.OngoingEnd() {
		return true
	}
	end, ok := parseYearMonth(e.End)
	return ok && today.Valid() && end.key() > monthOf(today).key()
}

// stillEmployed answers whether the most recent job of history.employment goes
// on: Yes when it has not ended, No when it has. With no job recorded it is
// left for the user.
func stillEmployed(s *Step) (Draft, bool) {
	if !stillEmployedRE.MatchString(s.Q.Text) {
		return Draft{}, false
	}
	yes, okY := ownYesNo(s.Q.Options, true)
	no, okN := ownYesNo(s.Q.Options, false)
	if !okY || !okN {
		return Draft{}, false
	}
	job, ok := mostRecentEmployment(s.Req.Facts)
	if !ok || strings.TrimSpace(job.Employer) == "" {
		return abstainDraft(ReasonNoGrounding, "", "still employed: no job is recorded in history.employment -> left for you"), true
	}
	pick, why := no, "ended "+job.End
	if employedAt(job, s.Req.Today) {
		pick, why = yes, "has not ended"
	}
	d := derivedDraft(pick.Label, "", startProfileDerive, "still employed: the most recent job, at "+quoteTerm(job.Employer)+", "+why+" -> "+quoteTerm(pick.Label))
	d.ChosenLabel = pick.Label
	d.Deterministic = true
	return d, true
}

// employmentDate answers a bare date-component control in an employment
// context ("Employment start year", "End month of your last job") from the
// most recent employment entry. It is the exact complement of educationDate:
// each requires or forbids employment wording, so neither borrows the other's
// dates. A label that names both the month and the year gets both in a text
// box and is left on a list. A duration, an availability question and a
// checkbox or radio group are never a date component.
func employmentDate(s *Step) (Draft, bool) {
	f, q := s.Req.Facts, s.Q
	if len(f.Employment) == 0 || !isBareControl(q) || isEducationRowControl(q) {
		return Draft{}, false
	}
	text := q.Text
	hasMonth, hasYear := monthWordRE.MatchString(text), yearWordRE.MatchString(text)
	if !hasMonth && !hasYear {
		return Draft{}, false
	}
	if !employmentWordRE.MatchString(text) || availabilityAskRE.MatchString(text) || availableStartRE.MatchString(text) || durationAskRE.MatchString(text) {
		return Draft{}, false
	}
	isStart, isEnd := empStartRE.MatchString(text), empEndRE.MatchString(text)
	if isStart == isEnd {
		return Draft{}, false
	}
	recent, ok := mostRecentEmployment(f)
	if !ok {
		return Draft{}, false
	}
	stamp, dir := recent.Start, "start"
	if isEnd {
		stamp, dir = recent.End, "end"
	}
	ym, ok := parseYearMonth(stamp)
	if !ok {
		return Draft{}, false
	}
	value := strconv.Itoa(ym.Year)
	opts := realOptions(q.Options)
	switch {
	case hasMonth && hasYear && len(opts) > 0:
		return Draft{}, false
	case hasMonth && hasYear:
		value = MonthName(ym.Month) + " " + value
	case hasMonth:
		value = MonthName(ym.Month)
	}
	if len(opts) > 0 {
		chosen := ""
		if hasMonth {
			if o, found := sameMonthOption(opts, ym.Month); found {
				chosen = o.Label
			}
		}
		if chosen == "" {
			if b, found := bindOption(s.bareScorer(), opts, value); found && b.Score >= rowMatchFloor {
				chosen = b.Label
			}
		}
		if chosen == "" {
			return Draft{}, false
		}
		value = chosen
	}
	d := derivedDraft(value, "", startProfileDate, "employment-date: most recent employment "+dir+" -> "+strconv.Quote(value))
	d.ChosenLabel = value
	d.Deterministic = true
	return d, true
}

var (
	roleCompanyRE     = lazyregexp.New(`(?i)^(?:current|present)\s+(?:role|job\s+title|position)\s*(?:&|and|/)\s*(?:company|employer|organization)[*\s:]*$`)
	historySelectorRE = lazyregexp.New(`(?i)workExperience|employmentHistory|educationHistory|NewWorkExperience_`)
)

// compoundRoleCompany answers the compound "Current role & company" box with
// "<title> at <company>", only outside an entity section and only when both
// facts are present. An employment row keeps its own row filler.
func compoundRoleCompany(s *Step) (Draft, bool) {
	q := s.Q
	if !roleCompanyRE.MatchString(q.Text) || classifySection(q.SectionPath) != sectionNone ||
		historySelectorRE.MatchString(s.Raw.Selector) {
		return Draft{}, false
	}
	if q.Kind != KindText || len(q.Options) > 0 {
		return abstainDraft(ReasonInputMismatch, "", "current role and company: a text control with no options is required"), true
	}
	title := entryText(s, IDCurrentJobTitle)
	if title == "" {
		if row, ok := mostRecentEmployment(s.Req.Facts); ok {
			title = strings.TrimSpace(row.Title)
		}
	}
	company := CurrentEmployerAnswer(entryText(s, IDCurrentCompany), s.Req.Facts)
	if title == "" || company == "" {
		return abstainDraft(ReasonPlaceholderEmpty, "", "current role and company: both the current title and the current company are required"), true
	}
	d := derivedDraft(title+" at "+company, "current_role_company", startProfileDerive, "current role and company: combined the title and the company")
	d.Deterministic = true
	return d, true
}

// deriveCurrentlyEmployed answers currently-employed (Vultr: "Are you
// currently employed?"): the user's own answer, else Yes when a
// history.employment position has no end. A history whose every position
// ended does not say there is no job now, so it is left.
func deriveCurrentlyEmployed(s *Step, e *Entry) (Draft, bool) {
	if own := ownText(e); own != "" {
		return bindEntryAnswer(s, e, own, "the user's own answer"), true
	}
	for _, row := range s.Req.Facts.Employment {
		if employedAt(row, s.Req.Today) {
			return bindEntryAnswer(s, e, "Yes", row.Title+" at "+row.Employer+" has not ended -> Yes"), true
		}
	}
	return abstainDraft(ReasonPlaceholderEmpty, "", "%s: no recorded position is ongoing", e.ID), true
}

// CurrentEmployerAnswer is the employer a fill writes and a reviewer checks:
// the user's own non-blank answer, else the employer of the most recent
// history.employment entry, else "". A placeholder or the ask-me marker names
// no employer. The caller passes the entry's answer.
func CurrentEmployerAnswer(own string, f Facts) string {
	if own = strings.TrimSpace(own); own != "" && own != AskMe {
		return own
	}
	if row, ok := mostRecentEmployment(f); ok {
		return strings.TrimSpace(row.Employer)
	}
	return ""
}

// employerVariants lists a company name and, when the bank knows it, every
// alias of that employer.
func employerVariants(employers []Employer, name string) []string {
	out := []string{name}
	key := normalizeLabel(name)
	for _, emp := range employers {
		names := append([]string{emp.Name}, emp.Aliases...)
		known := false
		for _, n := range names {
			if normalizeLabel(n) == key {
				known = true
				break
			}
		}
		if known {
			out = append(out, names...)
		}
	}
	return out
}

// currentEmployer answers the current-company entry: the user's own answer
// wins, a blank entry takes the employer of the most recent employment entry,
// and with neither it abstains. On a list of options the employer, or one of
// its aliases, must bind an option at 0.70; a long employer list that names
// none of them takes its own "Other" (Motional), as the option binder does.
func currentEmployer(s *Step, e *Entry) (Draft, bool) {
	own := ownText(e)
	value := CurrentEmployerAnswer(own, s.Req.Facts)
	if value == "" {
		return abstainDraft(ReasonPlaceholderEmpty, "", "current employer: neither the entry nor the profile history names one"), true
	}
	source, where := SourceEntry, "the entry's own answer"
	if own == "" {
		source, where = SourceDerived, "the most recent employment entry"
	}
	trace := "current employer " + strconv.Quote(value) + " from " + where
	opts := realOptions(s.Q.Options)
	if len(opts) == 0 {
		return Draft{Answer: value, ChosenLabel: value, Source: source, Deterministic: true, Trace: []string{trace}}, true
	}
	var best optionBinding
	found := false
	for _, v := range employerVariants(s.Employers, value) {
		if b, ok := bindOption(s.bareScorer(), opts, v); ok && b.Score >= 0.70 && (!found || b.Score > best.Score) {
			best, found = b, true
		}
	}
	if !found {
		if other, ok := openSetOther(e, s.Q); ok && s.Class.start >= ReviewFillConfidence {
			return Draft{Answer: other.Value, ChosenLabel: other.Label, Source: source, Start: min(s.Class.start, FuzzyConfidenceCap),
				Trace: []string{trace + "; absent from " + strconv.Itoa(len(s.Q.Options)) + " options: select the explicit list-gap choice " + strconv.Quote(other.Label)}}, true
		}
		return abstainDraft(ReasonInputMismatch, "", "%s; no option names it", trace), true
	}
	d := optionDraft(best, nil, source, true)
	d.Trace = []string{trace + "; chose option " + strconv.Quote(best.Label)}
	return d, true
}

var (
	employerWordRE  = lazyregexp.New(`(?i)\b(?:employer|company|organization|organisation)\b`)
	tenureAskRE     = lazyregexp.New(`(?i)\b(?:how\s+long|how\s+many\s+(?:years?|months?|weeks?|days?)|(?:years?|months?|weeks?|days?|time|tenure)\s+(?:at|with|for)|(?:length|duration)\s+of\s+(?:your\s+)?(?:employment|tenure)|(?:employment|employer)\s+(?:duration|tenure))\b`)
	priorEmployerRE = lazyregexp.New(`(?i)\b(?:(?:second|third|2nd|3rd)[\s/-]+(?:(?:most|next)[\s/-]+)?(?:recent[\s/-]+)?|next[\s/-]+most[\s/-]+recent[\s/-]+)(?:employer|company|organization|organisation)\b`)
)

// employerDetailGuard keeps the current-company fact to what it is: a
// company's identity cannot state how long the applicant has worked there, and
// it is not the second or third employer. Either ask is the terminal
// context-policy abstain.
func employerDetailGuard(s *Step, e *Entry) (Abstain, string, bool) {
	if e.ID != IDCurrentCompany {
		return Abstain{}, "", false
	}
	text := s.Q.Text
	switch {
	case employerWordRE.MatchString(text) && tenureAskRE.MatchString(text):
		return Abstain{Reason: ReasonContextPolicy, Detail: DetailEmployerDetail},
			"current-company cannot answer employer tenure; a maintained duration fact is required", true
	case priorEmployerRE.MatchString(text):
		return Abstain{Reason: ReasonContextPolicy, Detail: DetailEmployerDetail},
			"current-company cannot answer a second or third employer; the corresponding employment entry is required", true
	}
	return Abstain{}, "", false
}
