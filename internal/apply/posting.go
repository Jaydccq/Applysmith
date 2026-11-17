package apply

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/lazyregexp"
)

// The deterministic answers of the check stage. Each reads only the user's own
// facts or the posting's own words, is narrow, and returns nothing outside its
// shape; whatever it does not answer goes on to the ladder.

var (
	officeListRE   = lazyregexp.New(`(?i)\b(?:identify|specify|list|select|indicate|which)\b[^?]{0,60}\b(?:locations?|offices?|location/s)\b[^?]{0,60}\b(?:for\s+the\s+role|this\s+(?:role|position|job)|specified|posting|position|role)\b`)
	locationLineRE = lazyregexp.New(`(?im)^\s*(?:job\s+|work\s+|office\s+)?location\s*[:\-]\s*(\S[^\n]{0,119})$`)
)

// postingLocation reads the posting's own location line from its text. The
// whole line is the answer, so a state spelled out in full or in two words
// reads like any other.
func postingLocation(jd string) string {
	if m := locationLineRE.FindStringSubmatch(jd); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// OfficeListAnswer answers an office list under a label that defers to the
// posting with the posting's own location, never with every office the user's
// relocation preference accepts. With no posting location, or no option that
// names it, the row stays for the user.
func OfficeListAnswer(label, location string, options []string) (string, bool) {
	location = strings.TrimSpace(location)
	if location == "" || !officeListRE.MatchString(label) {
		return "", false
	}
	if len(options) == 0 {
		return location, true
	}
	if o, ok := resolveOption(location, options); ok {
		return o, true
	}
	city := strings.TrimSpace(strings.SplitN(location, ",", 2)[0])
	var hit []string
	for _, o := range options {
		if city != "" && strings.Contains(normText(o), normText(city)) {
			hit = append(hit, o)
		}
	}
	if len(hit) == 1 {
		return hit[0], true
	}
	return "", false
}

var (
	sponsorWordRE   = lazyregexp.New(`(?i)\b(?:sponsor\w*|visa)\b`)
	presentOnlyRE   = lazyregexp.New(`(?i)\b(?:currently|now|presently|at\s+this\s+time|before\s+you\s+could\s+begin\s+working)\b`)
	futureWordRE    = lazyregexp.New(`(?i)\b(?:future|later|eventually|will\s+you|ever|any\s*time|in\s+the\s+(?:long|near)\s+term)\b`)
	monthYearRE     = lazyregexp.New(`(?i)\bmonth\s+and\s+year\b`)
	availabilityRE  = lazyregexp.New(`(?i)\b(?:availab\w*|start\w*|begin|join)\b`)
	licenseListRE   = lazyregexp.New(`(?i)\b(?:list|name|provide)\b[^?]{0,40}\b(?:licen[sc]es?|certifications?)\b`)
	licenseMentionR = lazyregexp.New(`(?i)\b(?:licen[sc]es?|certif\w*)\b`)
	licenseHeadRE   = lazyregexp.New(`(?im)^\W*(?:licen[sc]es?|certifications?)(?:\s*(?:&|and)\s*[a-z]+)*\s*:?\W*$`)
	builtSystemsRE  = lazyregexp.New(`(?i)\bhave\s+you\s+(?:ever\s+)?(?:built|developed|designed|implemented|shipped)\b[^?]{0,60}\b(?:systems?|services?|pipelines?|platforms?|applications?)\s+in\s+([^?]{3,60})\??\s*$`)
	lastInternRE    = lazyregexp.New(`(?i)\bwhere\s+was\s+your\s+(?:last|most\s+recent|previous)\s+intern(?:ship)?\b`)
	internTitleRE   = lazyregexp.New(`(?i)\bintern(?:ship)?\b|\bco-?op\b`)
)

// SponsorshipNowOnlyAnswer answers No to a question about needing sponsorship
// now when the user's own answers say the need arises later: authorized now,
// sponsorship later. An unset answer is never read as false.
func SponsorshipNowOnlyAnswer(label string, now, future *bool, options []string) (string, bool) {
	if !sponsorWordRE.MatchString(label) || !presentOnlyRE.MatchString(label) || futureWordRE.MatchString(label) {
		return "", false
	}
	if now == nil || *now || future == nil || !*future {
		return "", false
	}
	return yesNoOption("No", options)
}

func yesNoOption(want string, options []string) (string, bool) {
	if len(options) == 0 {
		return want, true
	}
	for _, o := range options {
		if strings.EqualFold(strings.TrimSpace(o), want) {
			return o, true
		}
	}
	return "", false
}

// AvailabilityMonthYearAnswer answers an availability box that asks for month
// and year with the month of the first day to start every matcher lane reads:
// match.StartDate (the earliest start date, or the day thirty days from today
// when it is blank or has passed) moved past unfinished education
// (match.AfterEducation). An education entry without an end month leaves the
// box. Nothing else answers an availability question.
func AvailabilityMonthYearAnswer(label, earliest string, education []match.EducationFact, today time.Time) (string, bool) {
	if !monthYearRE.MatchString(label) || !availabilityRE.MatchString(label) {
		return "", false
	}
	now := match.Date{Year: today.Year(), Month: int(today.Month()), Day: today.Day()}
	d, ok := match.StartDate(earliest, now)
	if ok {
		d, ok = match.AfterEducation(d, now, education)
	}
	if !ok {
		return "", false
	}
	return match.MonthName(d.Month) + " " + strconv.Itoa(d.Year), true
}

// LicenseListAnswer answers a free-text "list your licenses or certifications"
// question: "None" when a CV names none (its silence), the CV's own list when
// it has one. A control with options is never answered, because silence proves
// the record names none and not that the applicant holds none; an empty CV
// proves nothing.
func LicenseListAnswer(label, cv string, options []string) (string, bool) {
	if len(options) > 0 || !licenseListRE.MatchString(label) || strings.TrimSpace(cv) == "" {
		return "", false
	}
	if !licenseMentionR.MatchString(cv) {
		return "None", true
	}
	loc := licenseHeadRE.FindStringIndex(cv)
	if loc == nil {
		return "", false
	}
	var items []string
	for _, line := range strings.Split(cv[loc[1]:], "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			if len(items) > 0 || strings.HasPrefix(t, "#") {
				break
			}
			continue
		}
		items = append(items, strings.TrimSpace(strings.TrimLeft(t, "-*+ \t")))
	}
	if len(items) == 0 {
		return "", false
	}
	return clip(strings.Join(items, "; "), 300), true
}

// BuiltSystemsAnswer answers a Yes/No "have you built systems in <domain>"
// question Yes, citing the CV line that names the domain. A CV that names none
// leaves the question to the ladder; No is never answered from silence.
func BuiltSystemsAnswer(label, cv string, options []string) (answer, cite string, ok bool) {
	m := builtSystemsRE.FindStringSubmatch(label)
	if m == nil || len(options) == 0 {
		return "", "", false
	}
	var words []string
	for _, w := range strings.Fields(strings.ToLower(m[1])) {
		if w = strings.Trim(w, ".,;:()"); len(w) >= 3 && w != "the" && w != "and" {
			words = append(words, w)
		}
	}
	if len(words) == 0 {
		return "", "", false
	}
	for _, line := range strings.Split(cv, "\n") {
		l := strings.ToLower(line)
		all := true
		for _, w := range words {
			if !strings.Contains(l, w) {
				all = false
			}
		}
		if all {
			a, found := yesNoOption("Yes", options)
			return a, strings.TrimSpace(line), found
		}
	}
	return "", "", false
}

// LastInternshipAnswer answers "where was your last internship" from the most
// recent employment row whose title names an internship; null when none does.
func LastInternshipAnswer(label string, jobs []match.EmploymentFact) (string, bool) {
	if !lastInternRE.MatchString(label) {
		return "", false
	}
	var interns []match.EmploymentFact
	for _, j := range jobs {
		if internTitleRE.MatchString(j.Title) && strings.TrimSpace(j.Employer) != "" {
			interns = append(interns, j)
		}
	}
	if len(interns) == 0 {
		return "", false
	}
	key := func(j match.EmploymentFact) string {
		if j.OngoingEnd() {
			return "9999-99"
		}
		return j.End
	}
	sort.SliceStable(interns, func(a, b int) bool { return key(interns[a]) > key(interns[b]) })
	return strings.TrimSpace(interns[0].Employer), true
}

var (
	fenceRE       = lazyregexp.New("(?s)```.*?```")
	headingRE     = lazyregexp.New(`(?m)^#{1,4}\s+(.+?)\s*$`)
	fitSectionRE  = lazyregexp.New(`(?i)\b(?:role\s+snapshot|cv\s+match|match\s*&\s*gaps|quick\s+screen|screen\s+summary)\b`)
	costSectionRE = lazyregexp.New(`(?i)\b(?:compensation|salary|level\s+strategy|negotiat\w*|walk[-\s]?away|offer|comp\b)\b`)
)

// ReportFit reads an evaluation report only through its role and fit
// sections. The level-strategy and compensation sections, which hold
// negotiation floors, never reach an application answer, and fenced machine
// blocks are dropped.
func ReportFit(md string) string {
	md = fenceRE.ReplaceAllString(md, "")
	locs := headingRE.FindAllStringSubmatchIndex(md, -1)
	var b strings.Builder
	for i, loc := range locs {
		title := md[loc[2]:loc[3]]
		if !fitSectionRE.MatchString(title) || costSectionRE.MatchString(title) {
			continue
		}
		end := len(md)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		for _, line := range strings.Split(strings.TrimSpace(md[loc[0]:end]), "\n") {
			if !moneyRE.MatchString(line) && !costSectionRE.MatchString(line) {
				b.WriteString(line + "\n")
			}
		}
	}
	return strings.TrimSpace(b.String())
}
