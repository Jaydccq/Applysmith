package match

import (
	"slices"
	"strings"

	"applysmith/internal/lazyregexp"
)

// noCompanyValues are the values that mean "no company". Neither an employer
// list nor a question may match on them.
var noCompanyValues = setOf("na", "n/a", "none", "no", "not applicable", "not available", "not currently employed", "unemployed", "null")

func emplIsNoCompany(v string) bool {
	n := normalizeLabel(v)
	if n == "" || noCompanyValues[n] {
		return true
	}
	compact := strings.Join(tokenize(n), "")
	return compact != "" && noCompanyValues[compact]
}

// employerJob reports whether the profile history holds a job at the bank's
// employer under any of its names, and whether one goes on today.
func employerJob(s *Step, emp Employer) (held, ongoing bool) {
	for _, j := range s.Req.Facts.Employment {
		k := relCompanyKey(j.Employer)
		if k == "" || !slices.ContainsFunc(append([]string{emp.Name}, emp.Aliases...), func(n string) bool { return relCompanyKey(n) == k }) {
			continue
		}
		held, ongoing = true, ongoing || employedAt(j, s.Req.Today)
	}
	return held, ongoing
}

// employerNames lists every name that counts as the applicant's employer: the
// bank's canonical names and aliases, then the employers in the profile
// history. When current, only the jobs that go on today count, and a bank
// employer only with such a job. No-company values are skipped.
func employerNames(s *Step, current bool) []string {
	var out []string
	add := func(v string) {
		if strings.TrimSpace(v) != "" && !emplIsNoCompany(v) {
			out = append(out, v)
		}
	}
	for _, emp := range s.Employers {
		if _, ongoing := employerJob(s, emp); current && !ongoing {
			continue
		}
		add(emp.Name)
		for _, a := range emp.Aliases {
			add(a)
		}
	}
	for _, j := range s.Req.Facts.Employment {
		if !current || employedAt(j, s.Req.Today) {
			add(j.Employer)
		}
	}
	return out
}

// employerNamedIn reports the name in the text, the company the text names
// equal to it but for legal suffixes ("Globex" for "Globex Inc."), or the audit
// firm it is under another of the firm's names ("EY" for "Ernst & Young").
func employerNamedIn(text, name string) bool {
	if containsAllTokens(tokenize(normalizeLabel(text)), tokenize(normalizeLabel(name))) {
		return true
	}
	if firm := othAuditFirmRE.FindString(name); firm != "" {
		for _, m := range othAuditFirmRE.FindAllString(text, -1) {
			if othAuditFirmKey(m) == othAuditFirmKey(firm) {
				return true
			}
		}
	}
	k := relCompanyKey(relNamedCompany(text))
	return k != "" && k == relCompanyKey(name)
}

// employerHistoryEntry answers "yes" when the question names an employer the
// applicant worked for and "no" otherwise. When a name appears only in an
// option label, not in the question, the question is about something else and
// the entry abstains (unless the entry opts out).
func employerHistoryEntry(s *Step) (Draft, bool) {
	e, q := s.Entry, s.Q
	if e == nil || e.Kind != KindEmployerHistory {
		return Draft{}, false
	}
	if relWorkAuthRE.MatchString(q.Text) {
		return abstainDraft(ReasonAmbiguous, "", "kind=employer-history: work-authorization wording -> abstain"), true
	}
	current := relCurrentRE.MatchString(q.Text) && !relEverRE.MatchString(q.Text)
	var matched []string
	var optionOnly []string
	for _, name := range employerNames(s, current) {
		if employerNamedIn(q.Text, name) {
			matched = append(matched, name)
			continue
		}
		for _, o := range q.Options {
			if containsAllTokens(tokenize(normalizeLabel(o.Label)), tokenize(normalizeLabel(name))) {
				optionOnly = append(optionOnly, name)
				break
			}
		}
	}
	if len(optionOnly) > 0 && e.AbstainsWhenAmbiguous() {
		return abstainDraft(ReasonAmbiguous, "", "kind=employer-history: %q appears in an option but not in the question -> abstain", optionOnly[0]), true
	}
	// "this company" / "for us" names the hiring company.
	if len(matched) == 0 && relPronounRE.MatchString(q.Text) {
		company := strings.TrimSpace(s.Req.Posting.Company)
		if company == "" {
			return abstainDraft(ReasonAmbiguous, "", "kind=employer-history: the hiring company is unknown -> abstain"), true
		}
		keys := relNames(s, company, nil)
		for _, name := range employerNames(s, current) {
			if keys[relCompanyKey(name)] {
				matched = append(matched, name)
				break
			}
		}
	}
	// The bank lists employers of any time: one the question asks about now,
	// with no job in the history, is neither a current employer nor a past one.
	if current && len(matched) == 0 {
		posting := relCompanyKey(s.Req.Posting.Company)
		for _, emp := range s.Employers {
			if held, _ := employerJob(s, emp); held {
				continue
			}
			for _, n := range append([]string{emp.Name}, emp.Aliases...) {
				if !emplIsNoCompany(n) && (employerNamedIn(q.Text, n) || posting != "" && relPronounRE.MatchString(q.Text) && relCompanyKey(n) == posting) {
					return abstainDraft(ReasonAmbiguous, "", "kind=employer-history: %q is in the employers list, but no job of the history says whether it goes on -> abstain", n), true
				}
			}
		}
	}
	if len(matched) > 0 && relWindowRE.MatchString(q.Text) {
		return abstainDraft(ReasonAmbiguous, "", "kind=employer-history: %q matched, but the question limits the history to a window -> abstain", matched[0]), true
	}
	answer, line := e.AnswerIfNoMatch, "no match"
	if len(matched) > 0 {
		answer, line = e.AnswerIfMatch, "matched ["+strings.Join(matched, ", ")+"]"
	}
	if strings.TrimSpace(answer) == "" {
		return abstainDraft(ReasonPlaceholderEmpty, "", "kind=employer-history: %s, and the entry has no answer for it", line), true
	}
	return bindEntryAnswer(s, e, answer, "kind=employer-history; question-side employer match: "+line+" -> "+quoteTerm(answer)), true
}

// The company-relationship lane (the last slot of step 4): a question about the
// applicant's own relationship with a company (worked there, a relative or a
// friend works there, applied or interviewed there before) is answered from the
// relationships the user recorded in the profile, never from anything else.
// Absence answers No only under a current completeness attestation for the
// question's family: the user confirmed the family's list complete on a date no
// earlier than the newest relationship recorded in it. Every unresolved verdict
// is non-terminal: the lane leaves "relationship:<reason>" in the trace and the
// walk continues, so the curated and employer-history entries still answer. A
// work-eligibility question is never a relationship question.

// RelationshipFamily is the kind of relationship a question asks about.
type RelationshipFamily string

const (
	FamilyEmployment       RelationshipFamily = "employment"
	FamilyRelative         RelationshipFamily = "relative"
	FamilyFriend           RelationshipFamily = "friend"
	FamilyPriorApplication RelationshipFamily = "prior-application"
	FamilyPriorInterview   RelationshipFamily = "prior-interview"
)

// RelationshipFamilies is the closed set, in the profile schema's order.
var RelationshipFamilies = []RelationshipFamily{FamilyEmployment, FamilyRelative, FamilyFriend, FamilyPriorApplication, FamilyPriorInterview}

// Unresolved reasons of the lane. None is terminal.
const (
	relNoApplicationCompany = "pronoun-without-application-company"
	relWithoutAttestation   = "relationship-without-attestation"
	relAbsenceUnattested    = "absence-without-attestation"
	relDetailWithoutYes     = "detail-without-yes-relationship"
	relDetailNotRecorded    = "detail-not-recorded"
	relNoOption             = "no-option-for-answer"
	relFollowUpBox          = "follow-up-box-takes-no-yes-or-no"
)

var (
	// Questions that only look like relationship questions.
	relWorkAuthRE = lazyregexp.New(`(?i)\b(?:sponsor(?:ship|ing)?|visas?|work(?:ing)?\s+authori[sz]|authori[sz](?:ed|ation)\s+to\s+(?:\w+\s+){0,2}work|eligib(?:le|ility)\s+(?:to|for)\s+(?:\w+\s+){0,2}work|eligible\s+for\s+employment|employment\s+eligibilit(?:y|ies)|legally\s+(?:able|permitted|entitled)\s+to\s+work|work\s+permit|right\s+to\s+work|h-?1b|opt|cpt|ead|green\s+card|citizenship)\b`)
	relVeteranRE  = lazyregexp.New(`(?i)\bprotected\s+veterans?\b|\buserra\b|\bvevraa\b|\buniformed\s+services\b`)
	// "If you are invited to interview ..." is about this process, not a past
	// one; SoFi's "any FINRA licenses if employed by SoFi" is about the job
	// applied for.
	relHypotheticalRE = lazyregexp.New(`(?i)\bif\s+you\s+(?:were\s+to|are|do|should|would)\b[^.?!]{0,140}\binterview|\b(?:should\s+you\s+(?:be\s+)?|if\s+)(?:invited|selected|asked|chosen)\b[^.?!]{0,140}\binterview` +
		`|\b(?:if|once|when|upon)\s+(?:you\s+are\s+|you['\x{2019}]re\s+|being\s+)?(?:employed|hired)\s+(?:by|at|with)\b`)
	relNarrativeRE = lazyregexp.New(`(?i)\b(?:describe|tell\s+us\s+about|walk\s+us\s+through|in\s+your\s+own\s+words|briefly\s+(?:describe|explain|share|tell)|please\s+(?:describe|explain|share|elaborate|tell\s+us))\b`)
	// A request to tick an acknowledgement ("Please select \"yes\" below to
	// acknowledge": LiveKit) is a declaration too, whatever it mentions.
	relDeclarationRE = lazyregexp.New(`(?i)\bi\s+(?:hereby\s+)?(?:acknowledge|attest|certify|declare|affirm)\b|\bi\s+have\s+read\b|` +
		`\b(?:select|choose|check|tick|click|answer)\b[^.?!]{0,20}\bto\s+(?:acknowledge|confirm\s+(?:you\s+have\s+read|that\s+you\s+understand))\b`)
	relNonPolarRE = lazyregexp.New(`(?i)^\W*(?:why|what|which|how|where|when|who|whom|whose)\b`)
	// relEmployerKindRE asks about kinds of employer, never one company
	// (Snowflake: "any government or military entity, state-owned enterprise,
	// or publicly-funded institution"): the recorded list names employers, not
	// whether they are public.
	relEmployerKindRE = lazyregexp.New(`(?i)\b(?:government|military|armed\s+forces|state[-\s]owned|publicly[-\s]funded|public[-\s]sector)\b`)

	// The families. A past interview or application is asked with its
	// relation ("interviewed with", "applied to", "applied before"): an
	// interview to come, or a method applied, is another question.
	relRelativeRE  = lazyregexp.New(`(?i)\b(?:relatives?|family\s+members?|spouses?|husbands?|wife|wives|parents?|siblings?|child|children|related\s+to)\b`)
	relFriendRE    = lazyregexp.New(`(?i)\b(?:friends?|acquaintances?|personal\s+relationship|know\s+anyone)\b`)
	relInterviewRE = lazyregexp.New(`(?i)\binterviewed\W+(?:with|at|for|by|here|before|previously)\b|\b(?:previous(?:ly)?|prior|past|ever|already|had)\b[^.?!]{0,30}\binterview(?:ed|s)?\W+(?:with|at|for|by|here)\b`)
	relAppliedRE   = lazyregexp.New(`(?i)\bapplied\W+(?:here|before|previously|to|at|with\s+(?:us|this|our)|(?:in|within|during)\s+the\s+(?:past|last)|for\s+(?:(?:a|an|any|another|this|the|other)\s+)?(?:\w+\s+){0,3}(?:positions?|jobs?|roles?|employment|work|openings?))\b|\bre-?appl(?:ying|ied)\b|\bprevious(?:ly)?\s+(?:submitted\s+)?(?:an\s+)?application\b`)
	relEmployedRE  = lazyregexp.New(`(?i)\b(?:work(?:ed|ing)?|employ(?:ed|ment|ee|ees)?|current\s+employer|contractors?|interns?)\b`)
	// "Worked with Databricks" is use of a product, not a job there: work
	// "with" a name is employment only beside wording that says so.
	relWorkedWithRE = lazyregexp.New(`(?i)\bwork(?:ed|ing|s)?\s+with\b`)
	relJobWordingRE = lazyregexp.New(`(?i)\bwork(?:ed|ing|s)?\s+(?:for|at)\b|\b(?:employ(?:ed|ment|ee|ees)?|current\s+employer|contractors?|interns?)\b`)
	// Employment wording that is about something else: attendance, skills, a
	// portfolio, a self-description or the applicant's own current role.
	relNotEmploymentRE = lazyregexp.New(`(?i)\b(?:in[-\s]?person|on[-\s]?site|onsite|in\s+(?:the\s+|our\s+)?office|hybrid|relocat\w*|(?:move|moving)\s+to|commute|working\s+knowledge|knowledge\s+of|familiar(?:ity)?\s+with|proficien(?:t|cy)\s+(?:in|with)|hands[-\s]?on|experience\s+(?:working\s+)?with|skilled\s+(?:in|with)|expertise\s+(?:in|with)|repositor(?:y|ies)|portfolio|github|work\s+samples?|best\s+describes|(?:in|at|during|for)\s+(?:your|the)\s+(?:most\s+recent|current|last|previous|prior|latest|former)\s+(?:role|position|job|employer|company|team))\b`)

	// An internal candidate is a current employee of the company that posts
	// the job (Boeing's "Internal Candidate").
	relInternalRE = lazyregexp.New(`(?i)\binternal\s+(?:candidate|applicant)\b`)
	// relHistoryLabelRE is a label about the applicant's record with a company.
	relHistoryLabelRE = lazyregexp.New(`(?i)\b(?:employment|work|employee)\s+(?:history|status|record)\b`)
	// An identifier only the company's own employees hold, which the question
	// says to answer N/A when it does not apply (Boeing's "Boeing BEMSID (type
	// N/A if not applicable)").
	relCompanyIDRE = lazyregexp.New(`\b[A-Z]{2,}ID\b|(?i:\b(?:employee|staff|badge)\s+(?:id|number|no\.?|#))`)
	relSaysNARE    = lazyregexp.New(`(?i)\bN/?A\s+if\s+(?:not\s+applicable|it\s+does\s+not\s+apply|none)\b`)

	// PathAI: "please list them here" lists the people.
	relNameDetailRE = lazyregexp.New(`(?i)\b(?:name\s+of|their\s+name|who\s+is|please\s+provide\s+the\s+name|(?:specify|provide|list|give|state|enter)\s+(?:(?:the|their|his\s+or\s+her|full)\s+)*names?|list\s+them)\b`)
	// relOwnConditionRE is a box that applies only if the applicant has the
	// tie it asks to name (PathAI: "If you do have any relatives currently
	// employed by PathAI, please list them here:").
	relOwnConditionRE = lazyregexp.New(`(?i)^\W*if\s+you\s+(?:do\s+)?(?:currently\s+)?have\b`)
	// relBothDetailRE asks for the person and how they are related at once
	// (Qualtrics: "please specify name and relationship").
	relBothDetailRE     = lazyregexp.New(`(?i)\bnames?\s+and\s+(?:the\s+|their\s+)?relationships?\b`)
	relCompanyWhoIsRE   = lazyregexp.New(`(?i)\bwho\s+is\s+(?:our|the)\s+(?:independent\s+|external\s+)?(?:auditor|vendor|client|customer|supplier|partner|contractor|parent|subsidiary|affiliate)\b`)
	relRelationDetailRE = lazyregexp.New(`(?i)\b(?:relationship\s+to|how\s+are\s+you\s+related|what\s+is\s+your\s+relation)\b`)
	relCurrentRE        = lazyregexp.New(`(?i)\b(?:currently|presently|right\s+now|at\s+this\s+time|now\s+work|existing)\b`)
	relEverRE           = lazyregexp.New(`(?i)\b(?:ever|previously|formerly|in\s+the\s+(?:past|last))\b`)
	// relWindowRE limits the history to a recent window ("in the past two
	// years"), which the profile's dates are not read against.
	relWindowRE  = lazyregexp.New(`(?i)\b(?:in|within|during)\s+the\s+(?:past|last|previous)\s+(?:\w+\s+)?(?:years?|months?)\b`)
	relPronounRE = lazyregexp.New(`(?i)\b(?:for\s+us|with\s+us|our\s+company|our\s+organi[sz]ation|this\s+company|this\s+organi[sz]ation|our\s+firm)\b`)

	// A company written in the question: a capitalised name after a preposition,
	// or before "employee", "contractor" or "intern".
	relNamedRE = lazyregexp.New(`\b(?:at|by|for|with|to|of)\s+([A-Z][A-Za-z0-9&.\- ]{1,40}?)(?:[?.,(]|\s+(?:before|previously|and|or|in|on|as|who|that|which|where|whose|department)\b|$)`)
	// "an existing HP employee" (HP) names the company after its adjective.
	// SoFi: "a SoFi or SoFi Tech Solutions (formerly Galileo) employee" names
	// SoFi first; a subsidiary after "or" and a bracketed former name follow it.
	relAttributeRE   = lazyregexp.New(`\b(?:an?|the)\s+(?:(?:existing|current|former)\s+)?([A-Z][A-Za-z0-9&.\-]{1,40}(?:\s+[A-Z][A-Za-z0-9&.\-]{1,40})?)(?:\s+or\s+[A-Z][A-Za-z0-9&.\-]{1,40}(?:\s+[A-Z][A-Za-z0-9&.\-]{1,40}){0,3})?(?:\s*\([^()]{1,60}\))?\s+(?:employees?|contractors?|interns?)\b`)
	relPronounNameRE = lazyregexp.New(`(?i)^(?:us|them|our|the\s+company|this\s+company)\b`)
	// relGenericNameRE is a capitalised label word, not a company: Trend
	// Micro's "Desired Type of Employment".
	relGenericNameRE = lazyregexp.New(`(?i)^(?:employment|employers?|work|jobs?|positions?|roles?)\b`)
	relPlaceRE       = lazyregexp.New(`(?i)^(?:the\s+)?(?:america|americas|united\s+states(?:\s+of\s+america)?|u\.?s\.?a?\.?|canada|mexico|uk|united\s+kingdom|great\s+britain|england|ireland|europe|eu|european\s+union|emea|apac|latam|india|china|japan|australia|new\s+zealand|singapore|germany|france|spain|italy|netherlands|switzerland|brazil)$`)
	relTechRE        = lazyregexp.New(`(?i)^(?:node(?:\.js)?|next(?:\.js)?|react(?:\.js)?|vue(?:\.js)?|angular|mysql|postgres(?:ql)?|mongodb|redis|kafka|graphql|docker|kubernetes|terraform|python|java|javascript|typescript|go|golang|rust|ruby|php|swift|kotlin|scala|aws|azure|gcp|linux|salesforce|sap|excel|html5?|css3?|spark|tableau)$`)

	// Options that mean "no relationship" on a form with no literal No.
	relNotApplicableRE = lazyregexp.New(`(?i)^(?:not\s+applicable|n/?a|none\s+of\s+the\s+above)$`)
	relNeverRE         = lazyregexp.New(`(?i)^(?:never|none|i\s+(?:have\s+not|haven't|have\s+never|never(?:\s+have)?)|i've\s+never)\b`)
	relPartialRE       = lazyregexp.New(`(?i)\b(?:since|until|currently|but|except|recently|last|within)\b`)

	relCompanySuffixRE = lazyregexp.New(`\b(?:inc|incorporated|corp|corporation|co|llc|ltd|limited|plc|gmbh|the)\b`)
	relNonAlnumRE      = lazyregexp.New(`[^a-z0-9]+`)
)

// relIntent is what a relationship question asks.
type relIntent struct {
	family RelationshipFamily
	// current is a question scoped to the present ("currently").
	current bool
	// detail is "name", "relation" or "both" for a follow-up that asks for a
	// person.
	detail  string
	pronoun bool
	named   string
	// idNA is an identifier of the company's employees that the question
	// says to answer N/A when it does not apply.
	idNA bool
}

func relClassify(text string) (relIntent, bool) {
	for _, re := range []*lazyregexp.Regexp{relWorkAuthRE, relVeteranRE, relHypotheticalRE, relNarrativeRE, relDeclarationRE, relEmployerKindRE} {
		if re.MatchString(text) {
			return relIntent{}, false
		}
	}
	switch {
	case relInternalRE.MatchString(text):
		return relIntent{family: FamilyEmployment, current: true, pronoun: true}, true
	case relCompanyIDRE.MatchString(text) && relSaysNARE.MatchString(text):
		return relIntent{family: FamilyEmployment, pronoun: true, idNA: true}, true
	}
	var in relIntent
	switch {
	case relRelativeRE.MatchString(text):
		in.family = FamilyRelative
	case relFriendRE.MatchString(text):
		in.family = FamilyFriend
	case relInterviewRE.MatchString(text):
		in.family = FamilyPriorInterview
	case relAppliedRE.MatchString(text):
		in.family = FamilyPriorApplication
	case relEmployedRE.MatchString(text) && !relNotEmploymentRE.MatchString(text):
		in.family = FamilyEmployment
	default:
		return relIntent{}, false
	}
	switch {
	case relBothDetailRE.MatchString(text):
		in.detail = "both"
	case relNameDetailRE.MatchString(text) && !relCompanyWhoIsRE.MatchString(text):
		in.detail = "name"
	case relRelationDetailRE.MatchString(text):
		in.detail = "relation"
	}
	if in.detail == "" && relNonPolarRE.MatchString(text) {
		return relIntent{}, false
	}
	in.current = relCurrentRE.MatchString(text) && !relEverRE.MatchString(text)
	in.pronoun = relPronounRE.MatchString(text)
	if in.family == FamilyEmployment && !in.pronoun && relWorkedWithRE.MatchString(text) && !relJobWordingRE.MatchString(text) {
		return relIntent{}, false
	}
	in.named = relNamedCompany(text)
	return in, true
}

// relNamedCompany reads a company written in the question, never a place, a
// technology or the company's own pronoun.
func relNamedCompany(text string) string {
	for _, m := range relNamedRE.FindAllStringSubmatchIndex(text, -1) {
		if strings.HasSuffix(strings.ToLower(text[:m[0]]), "due ") {
			continue
		}
		if c := strings.TrimSpace(text[m[2]:m[3]]); relIsCompany(c) {
			return c
		}
	}
	if m := relAttributeRE.FindStringSubmatch(text); m != nil && relIsCompany(m[1]) {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func relIsCompany(c string) bool {
	c = strings.TrimSpace(c)
	return c != "" && !relPronounNameRE.MatchString(c) && !relPlaceRE.MatchString(c) && !relTechRE.MatchString(c) && !relGenericNameRE.MatchString(c)
}

// relCompanyKey is a company name compared without case, punctuation or legal
// suffixes.
func relCompanyKey(s string) string {
	s = relCompanySuffixRE.ReplaceAllString(strings.ToLower(strings.ReplaceAll(s, ".", " ")), " ")
	return strings.TrimSpace(relNonAlnumRE.ReplaceAllString(s, " "))
}

// relNames is the company and its aliases as keys, widened by the bank's
// canonical employer whose name or alias is one of them.
func relNames(s *Step, company string, aliases []string) map[string]bool {
	keys := map[string]bool{}
	for _, n := range append([]string{company}, aliases...) {
		if k := relCompanyKey(n); k != "" {
			keys[k] = true
		}
	}
	for _, emp := range s.Employers {
		group := append([]string{emp.Name}, emp.Aliases...)
		hit := false
		for _, n := range group {
			if keys[relCompanyKey(n)] {
				hit = true
				break
			}
		}
		if hit {
			for _, n := range group {
				if k := relCompanyKey(n); k != "" {
					keys[k] = true
				}
			}
		}
	}
	return keys
}

// relRecorded is the family's relationships: the recorded ones, and for
// employment the jobs of the profile's history, current when they go on at
// today (employedAt).
func relRecorded(f Facts, family RelationshipFamily, today Date) []RelationshipFact {
	var out []RelationshipFact
	for _, r := range f.Relationships {
		if r.Family == family {
			out = append(out, r)
		}
	}
	if family == FamilyEmployment {
		for _, j := range f.Employment {
			if !emplIsNoCompany(j.Employer) {
				out = append(out, RelationshipFact{Family: family, Company: j.Employer, Current: employedAt(j, today)})
			}
		}
	}
	return out
}

// relAttested reports whether the user's completeness statement for the family
// is current: made on or after the newest relationship recorded in it.
func relAttested(f Facts, family RelationshipFamily) bool {
	asOf, ok := f.RelationshipsComplete[family]
	if !ok || strings.TrimSpace(asOf) == "" {
		return false
	}
	for _, r := range f.Relationships {
		if r.Family == family && r.Recorded > asOf {
			return false
		}
	}
	return true
}

// relNoChoice picks the option that answers No on a control with no literal
// No: "Not applicable" or "None of the above", else the one first-person
// "never" statement.
func relNoChoice(opts []Option) (Option, bool) {
	real := realOptions(opts)
	for _, o := range real {
		if relNotApplicableRE.MatchString(strings.TrimSpace(o.Label)) {
			return o, true
		}
	}
	var never []Option
	for _, o := range real {
		l := strings.ReplaceAll(strings.TrimSpace(o.Label), "’", "'")
		if relNeverRE.MatchString(l) && !relPartialRE.MatchString(l) {
			never = append(never, o)
		}
	}
	if len(never) == 1 {
		return never[0], true
	}
	return Option{}, false
}

// IsRelationshipQuestion reports a question about the applicant's own
// relationship with a company it names or means ("this company"): the
// relationship lane's questions, which only the profile's lists and the user's
// own entries answer, never a model choosing from other evidence.
func IsRelationshipQuestion(text string) bool {
	in, ok := relClassify(text)
	return ok && (in.named != "" || in.pronoun || in.family == FamilyPriorApplication || in.family == FamilyPriorInterview)
}

// MayAskRelationship reports whether the relationship lane reads the question
// at all: the only lane whose answer a relationship list's completeness mark
// changes.
func MayAskRelationship(text string) bool {
	_, ok := relClassify(text)
	return ok
}

// relationshipLane is the Relationship slot of step 4.
func relationshipLane(s *Step) (Draft, bool) {
	in, ok := relClassify(s.Q.Text)
	if !ok {
		return Draft{}, false
	}
	asked := in.named
	company := strings.TrimSpace(s.Req.Posting.Company)
	if asked == "" && (in.pronoun || in.family == FamilyPriorApplication || in.family == FamilyPriorInterview) {
		asked = company
	}
	// A history or status label that names the posting's own company asks
	// about it ("SpaceX & SpaceXAI Employment History"); a question that only
	// names the company's office is about attendance (Rebar).
	if asked == "" && in.family == FamilyEmployment && company != "" && relHistoryLabelRE.MatchString(s.Q.Text) && employerNamedIn(s.Q.Text, company) {
		asked = company
	}
	unresolved := func(reason string) (Draft, bool) {
		s.Tracef("relationship:%s (family %s) -> later lanes", reason, in.family)
		return Draft{}, false
	}
	if in.pronoun && company == "" {
		return unresolved(relNoApplicationCompany)
	}
	if asked == "" || in.idNA && !employerNamedIn(s.Q.Text, company) {
		return Draft{}, false
	}
	keys := relNames(s, asked, nil)
	var matches []RelationshipFact
	for _, r := range relRecorded(s.Req.Facts, in.family, s.Req.Today) {
		for k := range relNames(s, r.Company, r.Aliases) {
			if keys[k] {
				matches = append(matches, r)
				break
			}
		}
	}
	if !relAttested(s.Req.Facts, in.family) {
		if len(matches) > 0 {
			return unresolved(relWithoutAttestation)
		}
		return unresolved(relAbsenceUnattested)
	}
	var yes *RelationshipFact
	for i := range matches {
		if !in.current || matches[i].Current {
			yes = &matches[i]
			break
		}
	}
	line := "relationship: the recorded " + string(in.family) + " list is attested complete; " + quoteTerm(asked)
	if in.idNA {
		switch {
		case yes != nil:
			return unresolved(relDetailNotRecorded)
		case s.Q.Kind != KindText || hasRenderedOptions(s.Q.Options):
			return unresolved(relNoOption)
		}
		return Draft{Answer: "N/A", Source: SourceRelationship, Deterministic: true, Trace: []string{line + " is not recorded -> N/A, as the question says"}}, true
	}
	if in.detail != "" {
		if yes == nil && relOwnConditionRE.MatchString(s.Q.Text) && s.Q.Kind == KindText && !hasRenderedOptions(s.Q.Options) {
			// The attested list holds no such tie, so the box does not apply:
			// a required box says N/A, an optional one is left blank.
			line += " is not recorded and the box applies only if there is one"
			if s.Q.IsRequired() {
				return Draft{Answer: "N/A", Source: SourceRelationship, Deterministic: true, Trace: []string{line + " -> N/A"}}, true
			}
			return abstainDraft(ReasonNoEntry, "", "%s -> left blank", line), true
		}
		if yes == nil {
			return unresolved(relDetailWithoutYes)
		}
		v := yes.Person
		switch in.detail {
		case "relation":
			v = yes.Relation
		case "both":
			if strings.TrimSpace(yes.Relation) == "" {
				v = ""
			} else if v != "" {
				v += " (" + yes.Relation + ")"
			}
		}
		if strings.TrimSpace(v) == "" {
			return unresolved(relDetailNotRecorded)
		}
		if hasRenderedOptions(s.Q.Options) {
			return unresolved(relNoOption)
		}
		return Draft{Answer: v, Source: SourceRelationship, Deterministic: true, Trace: []string{line + " has a recorded " + in.detail}}, true
	}
	answer, has := "No", false
	if yes != nil {
		answer, has = "Yes", true
		line += " is recorded -> Yes"
	} else {
		line += " is not recorded -> No"
	}
	if !hasRenderedOptions(s.Q.Options) {
		if s.Q.Kind != KindText {
			return unresolved(relNoOption)
		}
		// The box under a Yes/No asks for what the Yes would name: its own
		// answer is never the gate's Yes or No.
		if isFollowUpSlot(s.Q.Text) {
			return unresolved(relFollowUpBox)
		}
		return Draft{Answer: answer, Source: SourceRelationship, Deterministic: true, Trace: []string{line}}, true
	}
	o, found := ownYesNo(s.Q.Options, has)
	if !found && !has {
		o, found = relNoChoice(s.Q.Options)
		// A "never" statement that lists more companies ("never worked for
		// SpaceX, xAI, X, or Twitter") is false if any of them is recorded.
		for _, r := range relRecorded(s.Req.Facts, in.family, s.Req.Today) {
			if found && employerNamedIn(o.Label, r.Company) {
				return unresolved(relNoOption)
			}
		}
	}
	if !found {
		return unresolved(relNoOption)
	}
	d := optionDraft(optionBinding{Label: o.Label, Value: o.Value, Score: 1}, nil, SourceRelationship, true)
	d.Trace = []string{line + " (" + quoteTerm(o.Label) + ")"}
	return d, true
}
