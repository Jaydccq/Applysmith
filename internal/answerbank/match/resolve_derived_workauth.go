package match

import (
	"fmt"
	"slices"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Work-authorization detail is one sentence composed from the recorded status
// and the expiry dates, so it cannot go stale into a false statement.

const (
	idEADCategoryCode          = "ead-category-code"
	idStemOptExtension         = "stem-opt-24-month-extension"
	idCurrentImmigrationStatus = "current-immigration-status"
	idWorkAuthorizationExpiry  = "work-authorization-expiry"
	idStemOptExpiry            = "stem-opt-extension-expiry"
)

var statusProse = map[string]string{
	"f-1 opt":                     "F-1 visa, currently on OPT",
	"f1 opt":                      "F-1 visa, currently on OPT",
	"opt":                         "F-1 visa, currently on OPT",
	"optional practical training": "F-1 visa, currently on OPT",
	"stem opt":                    "F-1 visa, currently on a STEM OPT extension",
	"f-1 cpt":                     "F-1 visa, currently on CPT",
	"cpt":                         "F-1 visa, currently on CPT",
}

// canonicalStatusProse renders the setup's canonical statuses
// ("<status>[: <detail>]", docs/architecture/setup.md) in the words a form
// reads; a machine id is never typed into a form.
var canonicalStatusProse = map[string]string{
	"citizen":                    "Citizen",
	"permanent_resident":         "Permanent resident",
	"work_visa":                  "Work visa",
	"student_work_authorization": "Student work authorization",
	"not_authorized":             "Not authorized to work",
	"prefer_not_to_say":          "Prefer not to say",
}

// VisaStatusProse is the recorded visa status in plain words: a canonical
// status becomes its prose ("work_visa: H-1B" is "H-1B visa"; a student
// authorization's end date is left to the expiry entry), and any other
// wording ("F-1 OPT") is returned as the user wrote it.
func VisaStatusProse(status string) string {
	status = strings.TrimSpace(status)
	head, detail, _ := strings.Cut(status, ":")
	head, detail = strings.TrimSpace(head), strings.TrimSpace(detail)
	switch prose, ok := canonicalStatusProse[head]; {
	case !ok && head == "other" && detail != "":
		return detail
	case !ok:
		return status
	case head == "work_visa" && detail != "" && detail != "other":
		return detail + " visa"
	default:
		return prose
	}
}

// employerVisaRE is a work visa a company petitions for and holds the
// applicant to: H-1B, H-1B1, L-1, E-3, O-1, TN.
var employerVisaRE = lazyregexp.New(`(?i)\b(?:h-?1b1?|l-?1[ab]?|e-?3|o-?1a?|tn)\b`)

// EmployerVisa reports whether a visa class is one a company sponsors.
func EmployerVisa(class string) bool { return employerVisaRE.MatchString(class) }

// deriveEmployerSponsored answers currently-employer-sponsored: the user's
// own answer, else the recorded visa status. A company-sponsored work visa is
// Yes; a citizen, a permanent resident, a student's work authorization (F-1 OPT
// or CPT) and no authorization are No; any other status is left to the user.
func deriveEmployerSponsored(s *Step, e *Entry) (Draft, bool) {
	if own := ownText(e); own != "" {
		return bindEntryAnswer(s, e, own, "the user's own answer"), true
	}
	raw := strings.TrimSpace(s.Req.Facts.VisaStatus)
	head, detail, _ := strings.Cut(raw, ":")
	head = strings.TrimSpace(head)
	answer := ""
	switch {
	case head == "work_visa" && employerVisaRE.MatchString(detail), canonicalStatusProse[head] == "" && head != "other" && employerVisaRE.MatchString(raw):
		answer = "Yes"
	case head == "citizen", head == "permanent_resident", head == "student_work_authorization", head == "not_authorized", statusProse[strings.ToLower(raw)] != "":
		answer = "No"
	}
	if answer == "" {
		return abstainDraft(ReasonPlaceholderEmpty, "", "%s: the recorded visa status does not say whether a company sponsors it", e.ID), true
	}
	return bindEntryAnswer(s, e, answer, "recorded visa status -> "+answer), true
}

// derivePermanentWorkAuth answers permanent-work-authorization: the user's own
// answer, else the record. A citizen or a permanent resident holds it; a
// recorded need for sponsorship, now or later, or a visa status does not
// (Dave: "Are you permanently authorized to work in the United States without
// visa sponsorship?" is No on F-1 OPT).
func derivePermanentWorkAuth(s *Step, e *Entry) (Draft, bool) {
	if own := ownText(e); own != "" {
		return bindEntryAnswer(s, e, own, "the user's own answer"), true
	}
	status := statusOf(s)
	standing, ok := applicantStanding(status)
	answer, why := "", ""
	switch need, known := sponsorshipNeed(s.Req.Facts, scopeEither); {
	case known && need:
		answer, why = "No", "a recorded need for sponsorship"
	case ok && (standing == standingCitizen || standing == standingResident):
		answer, why = "Yes", "the recorded status "+quoteTerm(status)
	case ok && standing == standingVisa:
		answer, why = "No", "the recorded visa status "+quoteTerm(status)
	}
	if answer == "" {
		return abstainDraft(ReasonPlaceholderEmpty, "", "%s: the record does not say whether the authorization is permanent", e.ID), true
	}
	return bindEntryAnswer(s, e, answer, why+" -> "+answer), true
}

// statusOf is the recorded visa status: the profile's visa_status, else the
// current-immigration-status entry the user wrote.
func statusOf(s *Step) string {
	if v := strings.TrimSpace(s.Req.Facts.VisaStatus); v != "" {
		return VisaStatusProse(v)
	}
	return entryText(s, idCurrentImmigrationStatus)
}

// workAuthSentence composes the detail sentence. An expiry in the past is not
// asserted (it would be a false statement), and a missing or unparseable one
// gives no sentence at all rather than a status with no dates. The extension
// is stated as available, not granted, and only when it ends after the expiry:
// an unparseable extension date, or one no later than the expiry (past, or
// already running), silently drops that clause.
func workAuthSentence(status, expiry, stem string, today Date) (string, bool) {
	status = strings.TrimSpace(status)
	if status == "" {
		return "", false
	}
	exp, ok := parseCalendarDate(expiry)
	if !ok || exp.Before(today) {
		return "", false
	}
	prose, known := statusProse[strings.ToLower(status)]
	if !known {
		prose = status
	}
	out := fmt.Sprintf("%s; work authorization valid through %s", prose, exp.ISO())
	if ext, ok := parseCalendarDate(stem); ok && exp.Before(ext) {
		out += fmt.Sprintf(", with a 24-month STEM OPT extension available through %s", ext.ISO())
	}
	return out, true
}

var (
	// The three asks of one text box (Exa: "Do you require Visa sponsorship
	// to work in your selected location? If so, which one? And when does your
	// Visa expire?"): the need, the visa, and when the current one expires.
	sponsorshipNeedAskRE = lazyregexp.New(`(?i)\b(?:require|need)\b[^?]{0,30}\bsponsor`)
	whichVisaAskRE       = lazyregexp.New(`(?i)\bwhich\s+(?:one|visa|type)\b|\bwhat\s+(?:type|kind)\s+of\b`)
	visaExpiryAskRE      = lazyregexp.New(`(?i)\bwhen\b[^?]{0,40}\bexpire`)
)

// sponsorshipVisaAndExpiry answers a text box that asks the sponsorship need,
// the visa, and when the current one expires with the user's own sponsorship
// statement (sponsorship-type-description) and the recorded expiry dates
// (the owner's decision): the statement names the need and the visa, the dates
// the expiry. Either missing, or an expiry past, leaves it to the entries.
func sponsorshipVisaAndExpiry(s *Step) (Draft, bool) {
	q := s.Q.Text
	if hasRenderedOptions(s.Q.Options) || !sponsorshipNeedAskRE.MatchString(q) || !whichVisaAskRE.MatchString(q) || !visaExpiryAskRE.MatchString(q) {
		return Draft{}, false
	}
	statement := strings.TrimSpace(entryText(s, idSponsorshipTypeDescription))
	exp, ok := parseCalendarDate(entryText(s, idWorkAuthorizationExpiry))
	if statement == "" || !ok || exp.Before(s.Req.Today) {
		return Draft{}, false
	}
	dates := "My current work authorization is valid through " + exp.ISO()
	if ext, ok := parseCalendarDate(entryText(s, idStemOptExpiry)); ok && exp.Before(ext) {
		dates += ", with a 24-month STEM OPT extension available through " + ext.ISO()
	}
	if !strings.HasSuffix(statement, ".") {
		statement += "."
	}
	return derivedDraft(statement+" "+dates+".", "", startProfileDerive,
		"sponsorship, visa and expiry in one box: the user's sponsorship statement and the recorded expiry dates"), true
}

var (
	// usWorkAskRE and visaExplainAskRE are the two asks of a text box that
	// asks whether the applicant may work in the U.S. and to explain a visa
	// need (Close: "Can you legally work in the US? Please explain if you
	// require a visa.").
	usWorkAskRE      = lazyregexp.New(`(?i)\b(?:can|are|do)\s+you\b[^?]{0,40}\bwork\s+in\s+the\s+(?:US|U\.S\.(?:A\.)?|USA|United\s+States)\b`)
	visaExplainAskRE = lazyregexp.New(`(?i)\b(?:explain|describe|specify|elaborate)\b[^.?!]{0,40}\b(?:visa|sponsor\w*)\b`)
	// h1bTransferAskRE asks whether an H-1B the applicant holds would need a
	// transfer (Quo: "... would you require support to transfer an active
	// H-1B Visa to work at Quo?").
	h1bTransferAskRE = lazyregexp.New(`(?i)\b(?:require|need)\b[^?]{0,30}\btransfer\b[^?]{0,30}\bH-?1-?B\b`)
	// sponsorTypeAskRE asks the need and its type in one question (Vizcom: "Do
	// you now or will you in the future require Visa sponsorship, and if so
	// which type?"); visaClarifyRE asks a visa need and then for clarity
	// (NICE: "Would you need a visa / work permit for the role in the location
	// you are applying for? Please clarify").
	sponsorTypeAskRE = lazyregexp.New(`(?i)\b(?:sponsor\w*|visa)\b[^?]{0,40}\bif\s+so\b[^?]{0,20}\b(?:which|what)\s+(?:type|kind)\b`)
	visaClarifyRE    = lazyregexp.New(`(?i)\b(?:need|require)\b[^?]{0,20}\bvisa\b[^?]*\?\s*(?:please\s+)?(?:clarify|explain|specify|elaborate)\b`)
)

// visaOrPermitAskRE asks whether working in a place needs a visa or a work
// authorization at all (Sim: "Do you require any visas or work authorizations
// to work in California?").
var visaOrPermitAskRE = lazyregexp.New(`(?i)^\W*do\s+you\s+(?:currently\s+)?(?:require|need)\s+(?:any\s+|a\s+)?visas?\s+(?:or|and/or|/)\s+(?:a\s+)?work\s+(?:authori[sz]ations?|permits?)\s+to\s+work\s+in\s+([^?]{2,60}?)\s*\?\s*$`)

// visaOrPermitNeed answers that question for a place in the United States
// from the applicant's standing: a visa holder needs one, a citizen or a
// permanent resident does not. The standing is read from a status string, so
// the answer is in the review band; no recorded status leaves it.
func visaOrPermitNeed(s *Step) (Draft, bool) {
	m := visaOrPermitAskRE.FindStringSubmatch(s.Q.Text)
	if m == nil {
		return Draft{}, false
	}
	if code, state := resolveLocation(m[1]); state != locationResolved || code != "US" {
		return Draft{}, false
	}
	status := statusOf(s)
	standing, ok := applicantStanding(status)
	if !ok {
		return Draft{}, false
	}
	o, ok := ownYesNo(s.Q.Options, standing == standingVisa)
	if !ok {
		return Draft{}, false
	}
	d := chosenOption(s, o, "a visa or work authorization needed to work in the United States, from the standing of "+quoteTerm(status))
	d.Start = 0.8
	return d, true
}

// sponsorshipStatementBox answers a text box that asks the applicant's U.S.
// work authorization and to explain a visa need, whether an H-1B would need a
// transfer, or a need of sponsorship with its type or clarified, with the
// user's own sponsorship statement (sponsorship-type-description): it states
// what the user holds and what they will need, which answers each. Details
// asked after the need ("If yes, please explain") are the details lane's. A
// blank statement leaves it to the entries.
func sponsorshipStatementBox(s *Step) (Draft, bool) {
	q := s.Q.Text
	asks := usWorkAskRE.MatchString(q) && visaExplainAskRE.MatchString(q) || h1bTransferAskRE.MatchString(q) ||
		(sponsorTypeAskRE.MatchString(q) || visaClarifyRE.MatchString(q)) && !sponsorDetailRE.MatchString(q)
	if hasRenderedOptions(s.Q.Options) || !asks {
		return Draft{}, false
	}
	statement := strings.TrimSpace(entryText(s, idSponsorshipTypeDescription))
	if statement == "" {
		return Draft{}, false
	}
	return derivedDraft(statement, "", startProfileDerive, "U.S. work authorization, an H-1B transfer or a need of sponsorship and its kind asked in a text box: the user's sponsorship statement"), true
}

// deriveWorkAuthDetail answers work-authorization-detail.
func deriveWorkAuthDetail(s *Step, e *Entry) (Draft, bool) {
	sentence, ok := workAuthSentence(statusOf(s), entryText(s, idWorkAuthorizationExpiry), entryText(s, idStemOptExpiry), s.Req.Today)
	if !ok {
		return abstainDraft(ReasonPlaceholderEmpty, "", "the visa status or an unexpired authorization date is missing"), true
	}
	return entryDraft(sentence, "work-authorization detail composed from the recorded status and dates"), true
}

// workAuthStanding is the immigration standing a label or a status names.
type workAuthStanding int

const (
	standingCitizen workAuthStanding = iota + 1
	standingResident
	standingVisa
)

var standingPatterns = []struct {
	standing workAuthStanding
	re       *lazyregexp.Regexp
}{
	{standingResident, lazyregexp.New(`(?i)\b(?:green\s*card|permanent\s+resident(?:s|cy)?|lawful\s+permanent|lpr)\b`)},
	{standingCitizen, lazyregexp.New(`(?i)\b(?:citizens?(?:hip)?|national)\b`)},
	{standingVisa, lazyregexp.New(`(?i)\b(?:visas?|work\s+permit|ead|employment\s+authori[sz]ation\s+(?:card|document)|opt|cpt|h-?1\s?b|tn|e-?3|o-?1|j-?1|l-?1)\b`)},
}

// standingsIn is every standing a text names.
func standingsIn(text string) map[workAuthStanding]bool {
	out := map[workAuthStanding]bool{}
	for _, p := range standingPatterns {
		if p.re.MatchString(text) {
			out[p.standing] = true
		}
	}
	return out
}

// applicantStanding reads the applicant's own standing from the recorded
// status. A status naming several is not a clean discriminator, and a status
// that names none is a visa of some kind, since every other authorization is.
func applicantStanding(status string) (workAuthStanding, bool) {
	status = strings.TrimSpace(status)
	if status == "" || status == canonicalStatusProse["not_authorized"] || status == canonicalStatusProse["prefer_not_to_say"] {
		return 0, false
	}
	named := standingsIn(status)
	switch len(named) {
	case 0:
		return standingVisa, true
	case 1:
		for k := range named {
			return k, true
		}
	}
	return 0, false
}

// standingOptions are the real options that name the standing and no other
// class of it, and no negation ("No Visa; Seeking work authorization"). An
// option naming citizenship or permanent residence also fits the other
// ("United States Citizen or Permanent Resident").
func standingOptions(opts []Option, standing workAuthStanding) []Option {
	var out []Option
	for _, o := range realOptions(opts) {
		named := standingsIn(o.Label)
		fits := named[standing] && (standing == standingVisa && len(named) == 1 || standing != standingVisa && !named[standingVisa])
		if fits && !hasNegationToken(o.Label) {
			out = append(out, o)
		}
	}
	return out
}

// standingList reports a list of plain standings: an option naming a visa
// alone beside one naming citizenship or permanent residence (AngelList: "US
// Work Visa Holder", "United States Citizen or Permanent Resident"). Such a
// list is a handful of options, so a long list is never read for it.
func standingList(opts []Option) bool {
	return len(opts) <= standingListMaxOptions && len(standingOptions(opts, standingVisa)) > 0 &&
		len(standingOptions(opts, standingCitizen))+len(standingOptions(opts, standingResident)) > 0
}

// standingListMaxOptions bounds the options a list of plain standings has.
const standingListMaxOptions = 8

// standingOption picks, on the status picker, the one option naming the
// applicant's standing (AngelList: an F-1 OPT holder is the "US Work Visa
// Holder"), in the review band: the standing is inferred from a status string.
func standingOption(s *Step, e *Entry, required bool) (Draft, bool) {
	status := statusOf(s)
	standing, ok := applicantStanding(status)
	if !ok {
		return Draft{}, false
	}
	opts := standingOptions(s.Q.Options, standing)
	why := "the only option naming the standing of the recorded status "
	if standing == standingVisa && len(opts) > 1 {
		opts, why = visaScopeOption(status, opts), "the visa option of the recorded status's scope of employers "
	}
	if len(opts) != 1 || contradictsSponsorship(opts[0].Label, required) {
		return Draft{}, false
	}
	d := optionDraft(optionBinding{Label: opts[0].Label, Value: opts[0].Value, Score: 0.8}, e, SourceDerived, false)
	d.Start = 0.8
	d.Trace = []string{why + quoteTerm(status) + " -> " + quoteTerm(opts[0].Label)}
	return d, true
}

var (
	// employerSpecificOptionRE and openAuthOptionRE split the visa options by
	// the employers the authorization covers (Instructure: "Temporary visa
	// with employer-specific authorization", "Temporary visa with open /
	// transferable work authorization").
	employerSpecificOptionRE = lazyregexp.New(`(?i)\bemployer[- ]specific\b|\btied\s+to\s+(?:an?\s+|one\s+|a\s+specific\s+)?employer\b`)
	openAuthOptionRE         = lazyregexp.New(`(?i)\bopen\b|\btransferable\b|\bany\s+employer\b`)
	// openStatusRE is a status whose authorization is an EAD, good for any
	// employer; employerStatusRE one whose visa is held for one employer.
	openStatusRE     = lazyregexp.New(`(?i)\b(?:opt|stem|optional\s+practical\s+training|ead|employment\s+authori[sz]ation\s+document)\b`)
	employerStatusRE = lazyregexp.New(`(?i)\bh-?1-?b\b|\bl-?1\b|\bo-?1\b|\btn\b|\be-?3\b`)
)

// visaScopeOption is, among visa options split by the employers they cover,
// the one of the recorded status's scope: an OPT or other EAD is open, an
// H-1B, L-1, O-1, TN or E-3 is held for one employer. A status of neither, or
// a split that is not one option of each, picks nothing.
func visaScopeOption(status string, opts []Option) []Option {
	var specific, open []Option
	for _, o := range opts {
		switch s, op := employerSpecificOptionRE.MatchString(o.Label), openAuthOptionRE.MatchString(o.Label); {
		case s && !op:
			specific = append(specific, o)
		case op && !s:
			open = append(open, o)
		}
	}
	if len(specific) != 1 || len(open) != 1 {
		return nil
	}
	switch o, e := openStatusRE.MatchString(status), employerStatusRE.MatchString(status); {
	case o && !e:
		return open
	case e && !o:
		return specific
	}
	return nil
}

// qualifiedAffirmativeTie breaks a tie between qualified affirmatives ("Yes -
// I hold a valid visa") by the applicant's standing. It applies only when two
// or more options share the top Yes score, every one of them names exactly one
// standing, and exactly one of them is the applicant's. The binding is in the
// review band because the standing is inferred from a status string.
func qualifiedAffirmativeTie(s *Step, opts []Option) (optionBinding, bool) {
	standing, ok := applicantStanding(statusOf(s))
	if !ok {
		return optionBinding{}, false
	}
	real := realOptions(opts)
	top := 0.0
	for _, o := range real {
		top = max(top, optionScore("Yes", o.Label))
	}
	if top <= 0 {
		return optionBinding{}, false
	}
	var tied []int
	for i, o := range real {
		if optionScore("Yes", o.Label) == top {
			tied = append(tied, i)
		}
	}
	if len(tied) < 2 {
		return optionBinding{}, false
	}
	pick := -1
	for _, i := range tied {
		named := standingsIn(real[i].Label)
		if len(named) != 1 {
			return optionBinding{}, false
		}
		if named[standing] {
			if pick >= 0 {
				return optionBinding{}, false
			}
			pick = i
		}
	}
	if pick < 0 {
		return optionBinding{}, false
	}
	idx := slices.IndexFunc(opts, func(o Option) bool { return o.Label == real[pick].Label })
	return optionBinding{Label: real[pick].Label, Value: real[pick].Value, Index: idx, Score: 0.85}, true
}

// eadCategories map a recorded status to the category its Employment
// Authorization Document carries (USCIS Form I-765), the first wording that
// matches deciding. An F-1 student on OPT holds (c)(3)(B), or (c)(3)(C) with
// the STEM extension, which deriveEADCategory reads from its own entry too.
var eadCategories = []struct {
	re   *lazyregexp.Regexp
	code string
}{
	{lazyregexp.New(`(?i)\bstem\b`), "(c)(3)(C)"},
	{lazyregexp.New(`(?i)\bpre-?completion\b`), "(c)(3)(A)"},
	{lazyregexp.New(`(?i)\b(?:opt|optional\s+practical\s+training)\b`), "(c)(3)(B)"},
	{lazyregexp.New(`(?i)\bh-?4\b`), "(c)(26)"},
	{lazyregexp.New(`(?i)\bl-?2\b`), "(a)(18)"},
	{lazyregexp.New(`(?i)\be-?[123]s\b|\be-?[123]\s+(?:spouse|dependent)\b`), "(a)(17)"},
	{lazyregexp.New(`(?i)\bj-?2\b`), "(c)(5)"},
	{lazyregexp.New(`(?i)\bdaca\b|\bdeferred\s+action\s+for\s+childhood\b`), "(c)(33)"},
	{lazyregexp.New(`(?i)\btps\b|\btemporary\s+protected\s+status\b`), "(a)(12)"},
	{lazyregexp.New(`(?i)\basylee\b|\bgranted\s+asylum\b`), "(a)(5)"},
	{lazyregexp.New(`(?i)\brefugee\b`), "(a)(3)"},
	{lazyregexp.New(`(?i)\badjustment\s+of\s+status\b|\bi-?485\b`), "(c)(9)"},
}

var (
	// noEADStatusRE is a status no EAD carries: citizenship, a green card, a
	// visa class held in the applicant's own right, CPT.
	noEADStatusRE = lazyregexp.New(`(?i)^\s*(?:citizen|permanent_resident)\b|\bcitizen\b|\bgreen\s*card\b|\bpermanent\s+resident\b|\bcpt\b|\bcurricular\s+practical\s+training\b`)
	// eadIfYesRE asks whether the authorization rests on an EAD before the code.
	eadIfYesRE = lazyregexp.New(`(?i)\bif\s+(?:yes|so)\b`)
)

// deriveEADCategory answers ead-category-code (owner decision): the user's own
// code, else the one the recorded status carries; a status no EAD carries is
// "N/A", and any status the table does not read stays with the user. A
// question that first asks whether the authorization rests on an EAD ("If
// yes, please indicate ...") is answered "Yes, <code>".
func deriveEADCategory(s *Step, e *Entry) (Draft, bool) {
	// The status as recorded: the prose of a canonical status drops its
	// detail ("student_work_authorization: F-1 OPT").
	status := strings.TrimSpace(s.Req.Facts.VisaStatus)
	if status == "" {
		status = entryText(s, idCurrentImmigrationStatus)
	}
	code, why := ownText(e), "the user's own code"
	if code == "" {
		stem, _ := stemOptNow(s)
		code, why = eadCategoryOf(status, stem)
	}
	if code == "" {
		return abstainDraft(ReasonPlaceholderEmpty, "", "%s: the recorded status %q names no EAD category", e.ID, status), true
	}
	answer := code
	if code != "N/A" && eadIfYesRE.MatchString(s.Q.Text) && !hasRenderedOptions(s.Q.Options) {
		answer = "Yes, " + code
	}
	return bindEntryAnswer(s, e, answer, why+" -> "+answer), true
}

// stemOptNow reads whether a recorded OPT is in its STEM extension now: the
// extension entry's Yes, unless the recorded dates put the extension after an
// initial OPT period that still runs (the authorization's expiry not yet past
// and the extension's expiry after it), when the applicant holds plain OPT
// with the extension ahead. known is false while the extension entry is blank.
func stemOptNow(s *Step) (now, known bool) {
	switch normalizeLabel(entryText(s, idStemOptExtension)) {
	case "no":
		return false, true
	case "yes":
		exp, okExp := parseCalendarDate(entryText(s, idWorkAuthorizationExpiry))
		ext, okExt := parseCalendarDate(entryText(s, idStemOptExpiry))
		return !(okExp && okExt && !exp.Before(s.Req.Today) && exp.Before(ext)), true
	}
	return false, false
}

// eadCategoryOf is the category the status carries, "N/A" for a status no EAD
// carries, and "" for one the table does not read.
func eadCategoryOf(status string, stem bool) (string, string) {
	raw := strings.TrimSpace(status)
	if raw == "" {
		return "", ""
	}
	for _, c := range eadCategories {
		if c.re.MatchString(raw) {
			if c.code == "(c)(3)(B)" && stem {
				return "(c)(3)(C)", "the recorded status " + quoteTerm(raw) + " with the STEM extension"
			}
			return c.code, "the recorded status " + quoteTerm(raw)
		}
	}
	if noEADStatusRE.MatchString(raw) || employerVisaRE.MatchString(raw) {
		return "N/A", "the recorded status " + quoteTerm(raw) + " carries no EAD"
	}
	return "", ""
}
