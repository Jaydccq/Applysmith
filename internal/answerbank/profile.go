package answerbank

import (
	"net/url"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/workspace"
)

// readProfile decodes profile.yml leniently, so a value that fails the
// profile's own validation (a link without a scheme) can still be read. It
// reports false when the file is absent or is not decodable. The decode is
// cached by revision.
func (s *Service) readProfile() (workspace.Profile, bool) {
	c, err := s.ws.Read(workspace.FileProfile)
	if err != nil || c.Revision == workspace.RevisionAbsent {
		return workspace.Profile{}, false
	}
	s.profileMu.Lock()
	defer s.profileMu.Unlock()
	if s.profileRev == c.Revision {
		return s.profile, s.profileOK
	}
	var p workspace.Profile
	ok := yaml.Unmarshal(c.Data, &p) == nil
	s.profileRev, s.profile, s.profileOK = c.Revision, p, ok
	return p, ok
}

// Profile returns the profile as readProfile decodes it, or false when the
// file is absent or not decodable. The result is shared and read-only.
func (s *Service) Profile() (workspace.Profile, bool) { return s.readProfile() }

// Preferences are the profile's work preferences the matcher and the ladder
// read: relocation, office days, travel and target seniorities. They are empty
// while the profile cannot be read.
func (s *Service) Preferences() match.Preferences { return s.preferences() }

func (s *Service) preferences() match.Preferences {
	p, ok := s.readProfile()
	if !ok {
		return match.Preferences{}
	}
	out := preferencesFrom(p)
	out.Statements = statementsFrom(p, s.now())
	return out
}

// preferencesFrom reduces the profile to the values the load stamps.
func preferencesFrom(p workspace.Profile) match.Preferences {
	var out match.Preferences
	if d := p.Preferences.MaxDaysInOffice; d != nil && *d >= 0 && *d <= 7 {
		out.MaxDaysInOffice = d
	}
	if t := p.Preferences.MaxTravelPct; t != nil && *t >= 0 && *t <= 100 {
		out.MaxTravelPercent = t
	}
	out.Relocation = RelocationPolicy(p)
	out.PayCurrency = strings.ToUpper(strings.TrimSpace(p.Targets.Compensation.Currency))
	for _, r := range p.Targets.Roles {
		out.TargetSeniorities = append(out.TargetSeniorities, r.Seniority)
	}
	return out
}

// RelocationPolicy builds the policy from the profile, its places trimmed and
// without blanks or repeats, or nil while preferences.relocation is unset.
func RelocationPolicy(p workspace.Profile) *match.RelocationPolicy {
	r := p.Preferences.Relocation
	if r.Default == "" && len(r.Places) == 0 && len(r.Excluded) == 0 {
		return nil
	}
	policy := &match.RelocationPolicy{
		Default:  match.RelocationOpen,
		Places:   cleanPlaces(r.Places),
		Excluded: cleanPlaces(r.Excluded),
	}
	if r.Default == match.RelocationClosed {
		policy.Default = match.RelocationClosed
	}
	a := p.Candidate.Address
	policy.Home = match.Home{
		City:    strings.TrimSpace(a.City),
		County:  strings.TrimSpace(a.County),
		Region:  strings.TrimSpace(a.Region),
		Country: strings.TrimSpace(a.Country),
	}
	return policy
}

// cleanPlaces trims, drops empties and de-duplicates, keeping order.
func cleanPlaces(places []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range places {
		v = strings.TrimSpace(v)
		if key := strings.ToLower(v); v != "" && !seen[key] {
			seen[key] = true
			out = append(out, v)
		}
	}
	return out
}

// AuthStatuses are the work-authorization statuses; profile.yml stores one in
// work_authorization.visa_status as "<status>[: <detail>]".
var AuthStatuses = []string{"citizen", "permanent_resident", "work_visa", "student_work_authorization", "other", "not_authorized", "prefer_not_to_say"}

// Wordings a person (or the original product) writes for a status: "F-1 OPT",
// "H-1B", "green card holder", "U.S. citizen".
var authWordings = []struct {
	status string
	re     *lazyregexp.Regexp
}{
	{"not_authorized", lazyregexp.New(`(?i)\bnot\s+authori[sz]ed\b`)},
	{"student_work_authorization", lazyregexp.New(`(?i)\b(?:f-?1|j-?1|opt|cpt|optional\s+practical\s+training)\b`)},
	{"permanent_resident", lazyregexp.New(`(?i)\b(?:green\s*card|permanent\s+resident|lawful\s+permanent|lpr)\b`)},
	{"citizen", lazyregexp.New(`(?i)\bcitizen(?:ship)?\b`)},
	{"work_visa", lazyregexp.New(`(?i)\b(?:h-?1\s?b1?|e-?[23]|l-?1|o-?1|tn|h-?4\s+ead|work\s+visa)\b`)},
}

var (
	// authUnheldRE finds, beside a status wording, what says the status is not
	// held now: a negation, or a status that is past or still to come.
	authUnheldRE = lazyregexp.New(`(?i)\b(?:not?|non|never|without|neither|nor|pending|appl(?:ied|ying)|expired|former(?:ly)?|previously|future|next|soon)\b|n't\b`)
	authWordRE   = lazyregexp.New(`(?i)[a-z]+`)
)

// citizenCountryWords are the words a citizen wording may hold beside
// "citizen": they name the United States. Any other word may name another
// country's citizenship.
var citizenCountryWords = []string{"u", "s", "us", "usa", "united", "states", "of", "america", "american", "the"}

// AuthStatus reads the status that visa_status records: the canonical
// "<status>[: <detail>]" the setup writer stores, or a wording that names
// exactly one status. A wording that names two statuses ("Canadian citizen,
// TN"), negates or postpones the one it names ("not a US citizen", "green card
// pending"), or names a citizenship other than the United States' names none.
// The stored text is kept as written.
func AuthStatus(v string) (string, bool) {
	head, _, _ := strings.Cut(v, ":")
	if head = strings.TrimSpace(head); slices.Contains(AuthStatuses, head) {
		return head, true
	}
	status, rest := "", v
	for _, w := range authWordings {
		if !w.re.MatchString(v) {
			continue
		}
		if status != "" {
			return "", false
		}
		status, rest = w.status, w.re.ReplaceAllString(v, " ")
	}
	if status == "" || authUnheldRE.MatchString(rest) {
		return "", false
	}
	if status == "citizen" {
		for _, word := range authWordRE.FindAllString(rest, -1) {
			if !slices.Contains(citizenCountryWords, strings.ToLower(word)) {
				return "", false
			}
		}
	}
	return status, true
}

var (
	// principalVisaRE names a visa class held in the applicant's own right;
	// dependentVisaRE a status that derives from another person's visa.
	principalVisaRE = lazyregexp.New(`(?i)\b(?:h-?1\s?b1?|e-?[23]|l-?1|o-?1|tn)\b`)
	dependentVisaRE = lazyregexp.New(`(?i)\b(?:h-?4|l-?2|e-?[23]s|ead|spouse|dependent)\b`)
	h1bRE           = lazyregexp.New(`(?i)^h-?1\s?b$`)
	optRE           = lazyregexp.New(`(?i)\b(?:opt|optional\s+practical\s+training)\b`)
	cptOrOptRE      = lazyregexp.New(`(?i)\b(?:opt|cpt|optional\s+practical\s+training|curricular\s+practical\s+training)\b`)
)

// principalVisa is the visa class of a work-visa status held in the
// applicant's own right ("H-1B"), or "" when the status names none, names
// "other", or reads as a dependent's status.
func principalVisa(visa, status string) string {
	if status != "work_visa" {
		return ""
	}
	head, detail, canonical := strings.Cut(visa, ":")
	if canonical && strings.TrimSpace(head) == "work_visa" {
		if d := strings.TrimSpace(detail); d != "other" {
			return d
		}
		return ""
	}
	if dependentVisaRE.MatchString(visa) {
		return ""
	}
	return principalVisaRE.FindString(visa)
}

// Setup steps that hold the statements.
const (
	stepIdentity = "identity"
	stepCV       = "cv"
	stepWorkAuth = "work-authorization"
	stepTargets  = "targets"
)

// statementsFrom lists the bank answers the structured setup steps already
// state, so the bank never asks the user the same fact twice. Each one is the
// user's own statement of the entry's fact or follows from it without a guess:
// the identity step's name, contact details, address and links answer the
// entries asking them; the country of residence answers where the user lives; the authorized
// countries, the status and the sponsorship answers answer the United States
// work-authorization questions; a role ending "present" says the user has a
// job now and, when they name one, the current employer; dated education answers the school and enrollment questions; the
// pay target's currency answers the currency question (the figure is a
// disclosure the user decides on). A fact the profile does not hold
// derives nothing. The date of birth is never used (the identity step keeps
// it for age questions only), and no self-identification answer is derived.
func statementsFrom(p workspace.Profile, now time.Time) []match.Statement {
	var out []match.Statement
	fixed := func(id, step, answer string) {
		if answer = strings.TrimSpace(answer); answer != "" {
			out = append(out, match.Statement{ID: id, Step: step, Kind: match.KindFixed, Body: match.Body{Answer: answer}})
		}
	}
	pick := func(id, step, answer string) {
		out = append(out, match.Statement{ID: id, Step: step, Kind: match.KindPickOption, Body: match.Body{Preferred: []string{answer}, FallbackText: answer}})
	}
	yesNo := func(b bool) string {
		if b {
			return "Yes"
		}
		return "No"
	}

	out = append(out, identityStatements(p)...)
	if c := strings.ToUpper(strings.TrimSpace(p.Candidate.Address.Country)); c != "" {
		northAmerica := c == "US" || c == "CA"
		fixed("current-based-country", stepIdentity, match.CountryName(c))
		fixed("currently-living-us-or-canada", stepIdentity, yesNo(northAmerica))
		pick("based-in-us-or-canada", stepIdentity, yesNo(northAmerica))
		fixed("currently-located-outside-us", stepIdentity, yesNo(c != "US"))
		// The fill answers a blank work country with the country of
		// residence (match workCountry); stated here, setup does not ask it.
		fixed("work-country", stepIdentity, match.CountryName(c))
	}

	if code, _ := splitPhone(p.Candidate.Phone); code != "" {
		// The engine picks a phone box's own country picker from the same
		// code (stage-profile); a separate "Country code" list takes it here.
		// Zone 1 is shared, so a country is named only for a US or Canadian
		// address.
		choices := []string{code}
		if c := strings.ToUpper(strings.TrimSpace(p.Candidate.Address.Country)); code == "+1" && (c == "US" || c == "CA") {
			choices = []string{"+1 " + c + " - " + match.CountryName(c), match.CountryName(c), code}
		}
		out = append(out, match.Statement{ID: "phone-country-code", Step: stepIdentity, Kind: match.KindPickOption, Body: match.Body{Preferred: choices, FallbackText: code}})
	}

	wa := p.WorkAuthorization
	// Export control is answered from the recorded citizenships (an owner
	// decision, docs/architecture/answer-bank.md), which list every one the
	// applicant holds.
	if len(wa.Citizenships) > 0 {
		restricted := slices.ContainsFunc(wa.Citizenships, func(c string) bool {
			return slices.Contains([]string{"CU", "IR", "KP", "SY"}, strings.ToUpper(strings.TrimSpace(c)))
		})
		fixed("citizen-cuba-iran-north-korea-syria", stepWorkAuth, yesNo(restricted))
	}
	us := slices.ContainsFunc(wa.AuthorizedCountries, func(c string) bool { return strings.EqualFold(strings.TrimSpace(c), "US") })
	onlyUS := us && len(wa.AuthorizedCountries) == 1
	status, _ := AuthStatus(wa.VisaStatus)
	switch {
	case status == "not_authorized":
		fixed("work-auth-us", stepWorkAuth, "No")
	case us:
		fixed("work-auth-us", stepWorkAuth, "Yes")
	}
	// The same rule answers it at fill time (match deriveEmployerSponsored);
	// stated here, setup's answer-bank step does not ask it again.
	switch {
	case status == "citizen", status == "permanent_resident", status == "student_work_authorization", status == "not_authorized":
		fixed("currently-employer-sponsored", stepWorkAuth, "No")
	case match.EmployerVisa(principalVisa(wa.VisaStatus, status)):
		fixed("currently-employer-sponsored", stepWorkAuth, "Yes")
	}
	if strings.EqualFold(strings.TrimSpace(wa.SecurityClearance), "none") {
		pick("security-clearance", stepWorkAuth, "None")
	}
	if c := strings.ToUpper(strings.TrimSpace(p.Candidate.Address.Country)); c != "" && (c != "US" || us && status != "not_authorized") {
		fixed("us-based-with-work-permit", stepWorkAuth, yesNo(c == "US"))
	}
	if us {
		now, fut := wa.RequiresSponsorshipNow, wa.RequiresSponsorshipFuture
		switch {
		case now != nil && *now, fut != nil && *fut:
			fixed("h1b-sponsorship", stepWorkAuth, "Yes")
		case now != nil && fut != nil:
			fixed("h1b-sponsorship", stepWorkAuth, "No")
		}
		visa := principalVisa(wa.VisaStatus, status)
		permanent := status == "citizen" || status == "permanent_resident"
		temporary := status == "work_visa" || status == "student_work_authorization"
		switch {
		case permanent && onlyUS:
			for _, id := range []string{"permanent-work-authorization", "us-citizen-lpr-asylee-refugee", "us-person-itar-ear", "work-auth-us-any-employer", "unrestricted-right-to-work-us"} {
				fixed(id, stepWorkAuth, "Yes")
			}
			// The status is the U.S. status the entry asks, written as its
			// choice; a citizen has no immigration case of their own.
			label := "Lawful permanent resident"
			if status == "citizen" {
				label = "U.S. citizen"
				fixed("active-immigration-case", stepWorkAuth, "No")
			}
			if c, ok := ShapeOf(match.Entry{ID: "us-citizen-status", Kind: match.KindPickOption}).ChoiceFor(label); ok {
				out = append(out, match.Statement{ID: "us-citizen-status", Step: stepWorkAuth, Kind: match.KindPickOption, Body: match.Body{Preferred: slices.Clone(c.Matches), FallbackText: c.Label}})
			}
		case temporary:
			for _, id := range []string{"permanent-work-authorization", "us-citizen-lpr-asylee-refugee", "us-person-itar-ear"} {
				fixed(id, stepWorkAuth, "No")
			}
		}
		if visa != "" {
			// A class held in the applicant's own right ties the authorization
			// to the sponsoring employer and is not a spouse's status.
			for _, id := range []string{"work-auth-us-any-employer", "unrestricted-right-to-work-us", "work-auth-based-on-visa-spouse"} {
				fixed(id, stepWorkAuth, "No")
			}
			if h1bRE.MatchString(visa) {
				fixed("held-h1b-past-6-years", stepWorkAuth, "Yes")
			}
		}
		if permanent && onlyUS || visa != "" {
			fixed("currently-in-opt", stepWorkAuth, "No")
			fixed("cpt-opt-work-authorization", stepWorkAuth, "No")
		}
		if permanent && onlyUS {
			fixed("work-auth-based-on-visa-spouse", stepWorkAuth, "No")
		}
		// Step 4 records a student authorization's end date as its detail
		// ("student_work_authorization: <YYYY-MM-DD>").
		if head, date, ok := strings.Cut(wa.VisaStatus, ":"); ok && strings.TrimSpace(head) == "student_work_authorization" {
			if d, err := time.Parse("2006-01-02", strings.TrimSpace(date)); err == nil {
				fixed("work-authorization-expiry", stepWorkAuth, d.Format("2006-01-02"))
			}
		}
		if status == "student_work_authorization" && !strings.HasPrefix(strings.TrimSpace(wa.VisaStatus), status) {
			// A wording such as "F-1 OPT" names the training it is on.
			if optRE.MatchString(wa.VisaStatus) {
				fixed("currently-in-opt", stepWorkAuth, "Yes")
			}
			if cptOrOptRE.MatchString(wa.VisaStatus) {
				fixed("cpt-opt-work-authorization", stepWorkAuth, "Yes")
			}
			fixed("work-auth-based-on-visa-spouse", stepWorkAuth, "No")
		}
	}

	if slices.ContainsFunc(p.History.Employment, func(r workspace.Employment) bool { return r.End == "present" }) {
		fixed("currently-employed", stepCV, "Yes")
	}
	if employer, ok := currentEmployer(p.History.Employment); ok {
		fixed(match.IDCurrentCompany, stepCV, employer)
	}
	out = append(out, educationStatements(p.History.Education, now)...)

	// The pay target says what the user wants, not that a form may be told: the
	// figure stays the user's decision, only its currency is stated.
	fixed("salary-currency", stepTargets, strings.ToUpper(p.Targets.Compensation.Currency))
	return out
}

// identityStatements answers the identity, contact, address and link entries
// with the values the identity step recorded (links canonical, a US state as
// its name and its "Name (XX)" label, the country by its English name) and the
// current job title with the title of the most recent role. An "@ask-me" the
// step recorded is stated as is, so the entry is left to the user.
func identityStatements(p workspace.Profile) []match.Statement {
	var out []match.Statement
	fixed := func(id, step, answer string) {
		if answer = strings.TrimSpace(answer); answer != "" {
			out = append(out, match.Statement{ID: id, Step: step, Kind: match.KindFixed, Body: match.Body{Answer: answer}})
		}
	}
	ranked := func(id string, choices []string) {
		if len(choices) > 0 {
			out = append(out, match.Statement{ID: id, Step: stepIdentity, Kind: match.KindPickOption, Body: match.Body{Preferred: choices}})
		}
	}
	c, a := p.Candidate, p.Candidate.Address
	fixed(match.IDApplicantFullName, stepIdentity, c.FullName)
	// The owner's rule: the person the profile names can verify who they are.
	if strings.TrimSpace(c.FullName) != "" {
		fixed("identity-verification", stepIdentity, "Yes")
	}
	fixed("preferred-name", stepIdentity, c.PreferredName)
	fixed("preferred-full-name", stepIdentity, preferredFullName(c.FullName, c.PreferredName))
	fixed("email-address", stepIdentity, c.Email)
	fixed("phone-mobile", stepIdentity, c.Phone)
	fixed("linkedin-url", stepIdentity, CanonicalLinkedIn(p.Links.LinkedIn))
	fixed("github-url", stepIdentity, CanonicalLink(p.Links.GitHub))
	fixed("portfolio-url", stepIdentity, CanonicalLink(p.Links.Portfolio))
	fixed("address-street", stepIdentity, a.Line1)
	fixed("address-line2", stepIdentity, a.Line2)
	fixed("address-city", stepIdentity, a.City)
	fixed("address-county", stepIdentity, a.County)
	fixed("address-zip", stepIdentity, a.PostalCode)
	ranked("address-state", match.StateChoices(a.Region))
	if country := strings.TrimSpace(a.Country); country != "" {
		ranked("address-country", []string{match.CountryName(country)})
	}
	if e, ok := mostRecentEmployment(p.History.Employment); ok {
		fixed(match.IDCurrentJobTitle, stepCV, e.Title)
	}
	return out
}

// preferredFullName is the full name the applicant goes by: the full name with
// the preferred first name in its first name's place and the middle names
// left out, or the full name itself when no preferred name is recorded. A
// preferred name left to the user leaves this one to them too.
func preferredFullName(full, preferred string) string {
	parts, preferred := strings.Fields(full), strings.TrimSpace(preferred)
	switch {
	case preferred == match.AskMe:
		return match.AskMe
	case preferred == "" || len(parts) < 2:
		return strings.Join(parts, " ")
	}
	return preferred + " " + parts[len(parts)-1]
}

// mostRecentEmployment picks the current role (end: present), else the role
// with the latest end or start month; the first of equals wins and a role
// without dates ranks last.
func mostRecentEmployment(rows []workspace.Employment) (workspace.Employment, bool) {
	best, found := workspace.Employment{}, false
	bestKey := ""
	for _, r := range rows {
		key := recencyKey(r)
		if !found || key > bestKey {
			best, bestKey, found = r, key, true
		}
	}
	return best, found
}

func recencyKey(r workspace.Employment) string {
	if r.End == "present" {
		return "1" + r.Start
	}
	return "0" + max(r.End, r.Start)
}

var (
	schemeRe    = lazyregexp.New(`(?i)^[a-z][a-z0-9+.-]*://`)
	linkedinRe  = lazyregexp.New(`(?i)^([a-z0-9-]+\.)?linkedin\.com$`)
	profilePath = lazyregexp.New(`^/in/([^/]+)/?$`)
)

// CanonicalLinkedIn writes a LinkedIn profile URL in the form sites accept:
// https://www.linkedin.com/in/<slug>/. Any other value is treated as a plain
// link.
func CanonicalLinkedIn(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	raw := v
	if !schemeRe.MatchString(raw) {
		raw = "https://" + raw
	}
	if u, err := url.Parse(raw); err == nil && linkedinRe.MatchString(u.Hostname()) {
		if m := profilePath.FindStringSubmatch(u.Path); m != nil {
			return "https://www.linkedin.com/in/" + m[1] + "/"
		}
	}
	return CanonicalLink(v)
}

// CanonicalLink adds https:// to a link that has no scheme; an empty value
// stays empty.
func CanonicalLink(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || schemeRe.MatchString(v) {
		return v
	}
	return "https://" + v
}

// currentEmployer is the employer of the roles that end "present", when they
// name exactly one.
func currentEmployer(rows []workspace.Employment) (string, bool) {
	name := ""
	for _, r := range rows {
		if r.End != "present" || strings.TrimSpace(r.Employer) == "" {
			continue
		}
		if e := strings.TrimSpace(r.Employer); name == "" {
			name = e
		} else if !strings.EqualFold(name, e) {
			return "", false
		}
	}
	return name, name != ""
}

// educationStatements answers the school and enrollment entries from dated
// education. Every entry needs an end month: one without makes the enrollment
// and the most recent school unknown, so nothing is derived.
func educationStatements(rows []workspace.Education, now time.Time) []match.Statement {
	if len(rows) == 0 {
		return nil
	}
	for _, r := range rows {
		if strings.TrimSpace(r.End) == "" || strings.TrimSpace(r.School) == "" {
			return nil
		}
	}
	ordered := slices.Clone(rows)
	slices.SortStableFunc(ordered, func(a, b workspace.Education) int { return strings.Compare(b.End, a.End) })
	fixed := func(id, answer string) match.Statement {
		return match.Statement{ID: id, Step: stepCV, Kind: match.KindFixed, Body: match.Body{Answer: strings.TrimSpace(answer)}}
	}
	pick := func(id string, choices ...string) match.Statement {
		return match.Statement{ID: id, Step: stepCV, Kind: match.KindPickOption, Body: match.Body{Preferred: choices, FallbackText: choices[0]}}
	}
	stem := false
	month := now.Format("2006-01")
	enrolled := ordered[0].End > month
	out := []match.Statement{
		fixed("currently-enrolled-student", map[bool]string{true: "Yes", false: "No"}[enrolled]),
		fixed("most-recent-school", ordered[0].School),
	}
	if end, err := time.Parse("2006-01", ordered[0].End); err == nil && !enrolled {
		out = append(out, fixed("graduation-year-month", end.Format("January 2006")))
	}
	if g := strings.TrimSpace(ordered[0].GradeAverage); g != "" {
		out = append(out, fixed("latest-gpa-score", g))
	}
	for _, r := range ordered {
		if match.DegreeLevel(r.Degree) == "Bachelor" {
			out = append(out, fixed("undergraduate-school", r.School))
			if f := strings.TrimSpace(r.Field); f != "" {
				out = append(out, pick("undergraduate-major", f))
			}
			break
		}
	}
	// The fill answers these from the education rows (fieldOfStudy,
	// userEducationLevel); stated here, setup does not ask them again.
	if f := strings.TrimSpace(ordered[0].Field); f != "" {
		out = append(out, pick("field-of-study-choice", f))
	}
	if !enrolled {
		out = append(out, fixed("did-you-graduate", "Yes"))
	}
	best, phd := -1, false
	for _, r := range ordered {
		if r.End > month {
			continue
		}
		if i := slices.Index(levelOrder, match.DegreeLevel(r.Degree)); i >= 0 && (best < 0 || i < best) {
			best = i
		}
		phd = phd || phdRE.MatchString(r.Degree)
		// Only a Yes is stated: a field outside the list may still be STEM.
		if match.STEMField(r.Field) {
			stem = true
		}
	}
	if best >= 0 {
		labels := levelLabels[levelOrder[best]]
		if levelOrder[best] == "Doctorate" && !phd {
			labels = labels[:len(labels)-1]
		}
		out = append(out, pick("highest-level-education", labels...))
	}
	if stem {
		out = append(out, pick("has-stem-degree", "Yes"))
	}
	return out
}

// levelOrder ranks match.DegreeLevel's levels, highest first; levelLabels are
// the option wordings each is preferred as. The doctorate's last label, "Ph.D.",
// is preferred only for a degree that is one (phdRE): a JD, MD or PharmD is a
// doctorate but no Ph.D.
var (
	phdRE       = lazyregexp.New(`(?i)\b(?:ph\.?\s?d|d\.?\s?phil|doctor\s+of\s+philosophy)\b`)
	levelOrder  = []string{"Doctorate", "Master", "Bachelor", "Associate", "High School"}
	levelLabels = map[string][]string{
		"Doctorate":   {"Doctorate", "Doctoral Degree", "Ph.D."},
		"Master":      {"Master's Degree", "Master's"},
		"Bachelor":    {"Bachelor's Degree", "Bachelor's"},
		"Associate":   {"Associate's Degree", "Associate's"},
		"High School": {"High School Diploma", "High School"},
	}
)
