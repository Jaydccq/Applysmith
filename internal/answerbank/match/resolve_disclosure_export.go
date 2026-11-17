package match

import (
	"slices"
	"strings"

	"applysmith/internal/lazyregexp"
)

// IDUSPerson is the export-control entry: whether the applicant is a U.S.
// person under ITAR and EAR.
const IDUSPerson = "us-person-itar-ear"

var usPersonRE = lazyregexp.New(`(?i)\bu\.?\s?s\.?\s+persons?\b|\bunited\s+states\s+persons?\b`)

// usPersonLeadRE and foreignPersonLeadRE are an option that leads with its
// status ("U.S. person. This status includes ...", "Foreign person. ...",
// Hadrian): the lead states it, whatever the definition after it negates.
var (
	usPersonLeadRE      = lazyregexp.New(`(?i)^\W*(?:u\.?\s?s\.?|united\s+states)\s+persons?\b\s*[.:\x{2013}\x{2014}-]`)
	foreignPersonLeadRE = lazyregexp.New(`(?i)^\W*(?:foreign|non[-\s]?(?:u\.?\s?s\.?|united\s+states))\s+persons?\b\s*[.:\x{2013}\x{2014}-]`)
)

// statesNotUSPerson reports whether an option naming U.S. person status states
// that the applicant is not one: its lead when it leads with a status, else a
// negation anywhere in it.
func statesNotUSPerson(label string) bool {
	switch {
	case usPersonLeadRE.MatchString(label):
		return false
	case foreignPersonLeadRE.MatchString(label):
		return true
	}
	return hasNegationToken(label)
}

// usPersonStatus is the applicant's U.S. person status: the entry's own Yes or
// No, else derived from the sponsorship facts. U.S. citizens, permanent
// residents and protected individuals never need visa sponsorship, so a user
// who needs it now or in the future is not a U.S. person. A user who needs none
// is unknown: the facts do not tell a citizen from a visa holder.
func usPersonStatus(s *Step, e *Entry) (person, known bool, where string) {
	if e != nil {
		switch own := ownText(e); {
		case yesWordRE.MatchString(own):
			return true, true, "the entry's own answer"
		case noWordRE.MatchString(own):
			return false, true, "the entry's own answer"
		}
	}
	if person, kind, ok := ownUSStatus(s); ok {
		return person, true, idUSCitizenStatus + " " + quoteTerm(firstNonEmpty(kind, "neither"))
	}
	if need, ok := sponsorshipNeed(s.Req.Facts, scopeEither); ok && need {
		return false, true, "sponsorship is needed"
	}
	return false, false, ""
}

// usStatusKinds are the classes of U.S. person an option, or the user's own
// us-citizen-status, names.
var usStatusKinds = []struct {
	kind string
	re   *lazyregexp.Regexp
}{
	{"citizen", lazyregexp.New(`(?i)\b` + usNameRE + `\s+citizen\b|\bcitizen\s+of\s+the\s+` + usNameRE + `|^\W*citizen\W*$`)},
	{"national", lazyregexp.New(`(?i)\b` + usNameRE + `\s+national\b|\bnational\s+of\s+the\s+` + usNameRE)},
	{"permanent resident", lazyregexp.New(`(?i)\bpermanent\s+resident|\bgreen\s+card\b`)},
	{"refugee or asylee", lazyregexp.New(`(?i)\brefugee\b|\basylee\b|\basylum\b`)},
}

// usStatusNoneRE is the user's own "neither": none of the U.S. person classes.
var usStatusNoneRE = lazyregexp.New(`(?i)^\W*(?:neither|none(?:\s+of\s+(?:the\s+above|these))?)\W*$`)

// usStatusKind is the one class a label names, or "".
func usStatusKind(label string) string {
	kind := ""
	for _, k := range usStatusKinds {
		if k.re.MatchString(label) {
			if kind != "" {
				return ""
			}
			kind = k.kind
		}
	}
	return kind
}

// ownUSStatus reads the user's own us-citizen-status as a U.S. person class:
// a class it names is a U.S. person of that class, "neither" is none; a bare
// Yes or No speaks of citizenship only and says nothing of the other classes.
func ownUSStatus(s *Step) (person bool, kind string, known bool) {
	stated := entryText(s, idUSCitizenStatus)
	switch {
	case usStatusNoneRE.MatchString(stated):
		return false, "", true
	case hasNegationToken(stated):
		return false, "", false
	}
	if kind := usStatusKind(stated); kind != "" {
		return true, kind, true
	}
	return false, "", false
}

// yesWordRE and noWordRE are a text that opens with Yes or No.
var (
	yesWordRE = lazyregexp.New(`(?i)^\s*yes\b`)
	noWordRE  = lazyregexp.New(`(?i)^\s*no\b`)
)

var (
	// notUSPersonRE names the opposite status: a non-U.S. person, or a foreign
	// person or national.
	notUSPersonRE = lazyregexp.New(`(?i)\b(?:non[-\s]?|not\s+(?:an?\s+)?)(?:u\.?\s?s\.?|united\s+states)\s+persons?\b|\bforeign\s+(?:persons?|nationals?)\b`)
	// usDotsRE is the United States written with periods, which would end a
	// sentence before "Person".
	usDotsRE = lazyregexp.New(`(?i)\bu\.\s?s\.`)
)

// usPersonPolarity reads which status a U.S. person question asks about, for
// a bare Yes or No: negated when it asks whether the applicant is a non-U.S.
// or foreign person. Only the sentences that ask are read (those ending in a
// question mark, or every sentence when none does), so a note that says who
// is not a U.S. person turns nothing. ok is false when those sentences name
// both statuses or hold a negation before the status they name: the question
// is then left to the user.
func usPersonPolarity(text string) (negated, ok bool) {
	all := sentencesOf(usDotsRE.ReplaceAllString(answerHintRE.ReplaceAllString(text, " "), "US"))
	asked := slices.DeleteFunc(slices.Clone(all), func(s string) bool { return !strings.HasSuffix(s, "?") })
	if len(asked) == 0 {
		asked = all
	}
	plain := false
	for _, sentence := range asked {
		rest := notUSPersonRE.ReplaceAllString(sentence, " ")
		negated = negated || rest != sentence
		head := rest
		if loc := usPersonRE.FindStringIndex(rest); loc != nil {
			plain, head = true, rest[:loc[0]]
		}
		if hasNegationToken(head) {
			return false, false
		}
	}
	return negated, !(plain && negated)
}

// usPersonDraft answers a U.S. person question for the status: the one
// statement option that names U.S. person status with the right polarity, or
// the control's own Yes or No, or the bare word on a text control, either
// word turned when the question asks about the opposite status
// (usPersonPolarity) and left to the user when that cannot be read.
func usPersonDraft(s *Step, e *Entry, person bool, where string) Draft {
	opts := realOptions(s.Q.Options)
	trace := "U.S. person=" + boolWord(person) + " from " + where + " -> "
	var stating []Option
	for _, o := range opts {
		if usPersonRE.MatchString(o.Label) && statesNotUSPerson(o.Label) != person {
			stating = append(stating, o)
		}
	}
	negated, read := false, true
	if len(stating) == 0 {
		// A bare Yes or No answers the question as it is asked.
		if negated, read = usPersonPolarity(s.Q.Text); negated {
			trace += "asked as the opposite status -> "
		}
	}
	if len(opts) == 0 {
		if !read {
			d := abstainDraft(ReasonAmbiguous, "", "%sthe question's polarity is not read", trace)
			d.Entry = e
			return d
		}
		answer := "No"
		if person != negated {
			answer = "Yes"
		}
		d := entryDraft(answer, trace+quoteTerm(answer))
		d.Entry = e
		return d
	}
	chosen, ok := Option{}, false
	switch {
	case len(stating) == 1:
		chosen, ok = stating[0], true
	case len(stating) > 1 && !person:
		chosen, ok = sanctionedClauseOption(s, stating)
		if !ok {
			chosen, ok = listedCountriesOption(s, stating)
		}
	case len(stating) == 0:
		chosen, ok = ownYesNoExactlyOne(opts, person != negated)
		if _, other := ownYesNoExactlyOne(opts, person == negated); !other || !read {
			ok = false
		}
	}
	if !ok {
		d := abstainDraft(ReasonAmbiguous, "", "%sno single option states it", trace)
		d.Entry = e
		return d
	}
	d := optionDraft(optionBinding{Label: chosen.Label, Value: chosen.Value, Score: 1}, e, SourceDerived, true)
	d.Start = startProfileDerive
	d.Trace = []string{trace + quoteTerm(chosen.Label)}
	return d
}

// usPersonClauseRE is an option's leading "I am (not) a U.S. person" and the
// "and" that joins the clause after it.
var usPersonClauseRE = lazyregexp.New(`(?i)^\W*i\s+am\s+(?:not\s+)?an?\s+(?:u\.?\s?s\.?|united\s+states)\s+person\b\s*[,;]?\s*(?:and\s+)?`)

// sanctionedClauseOption parts options that each say "not a U.S. person" by
// the clause after it that names sanctioned countries (Cerebras: "..., and I
// am [not] a current citizen or permanent resident of Cuba, Iran, North Korea,
// or Syria"). The clause holds when a recorded citizenship, or for residence
// the recorded home, is a country it names; the option whose clause says so
// is chosen. Every option needs such a clause, and what it reads must be
// recorded.
func sanctionedClauseOption(s *Step, opts []Option) (Option, bool) {
	cits, home := recordedCitizenships(s), recordedHomeCountry(s)
	var hits []Option
	for _, o := range opts {
		lead := usPersonClauseRE.FindString(o.Label)
		if lead == "" {
			return Option{}, false
		}
		clause := o.Label[len(lead):]
		_, codes := scanCountries(clause, false)
		citizen, resident := sanctionsCitizenRE.MatchString(clause), sanctionsResidentRE.MatchString(clause)
		if len(codes) == 0 || !citizen && !resident {
			return Option{}, false
		}
		holds := false
		if citizen {
			if len(cits) == 0 {
				return Option{}, false
			}
			holds = slices.ContainsFunc(cits, func(c string) bool { return slices.Contains(codes, c) })
		}
		if resident {
			if home == "" {
				return Option{}, false
			}
			holds = holds || slices.Contains(codes, home)
		}
		if holds != hasNegationToken(clause) {
			hits = append(hits, o)
		}
	}
	if len(hits) != 1 {
		return Option{}, false
	}
	return hits[0], true
}

// onlyListedRE is a clause about being a citizen, national or permanent
// resident of only the countries a list names (10 CFR Part 810 List I, Oklo).
var onlyListedRE = lazyregexp.New(`(?i)\b(?:citizen|national|permanent\s+resident)\b[^.]*?\bof\s+only\s+(?:the\s+)?countries\s+listed\b`)

// listedCountriesOption parts two options that each say "not a U.S. person"
// by a clause on citizenship of only the countries a list names (Oklo: B
// names List I and says the applicant is a citizen of only those, C says the
// applicant is not). The list is the countries the stating option names after
// its clause; the other option negates the clause and names none. A recorded
// citizenship outside the list makes the negating option; every recorded
// citizenship on it makes the stating one, for review, since a permanent
// residence elsewhere is not recorded. Nothing recorded leaves it.
func listedCountriesOption(s *Step, opts []Option) (Option, bool) {
	cits := recordedCitizenships(s)
	if len(opts) != 2 || len(cits) == 0 {
		return Option{}, false
	}
	var stating, negating *Option
	var list []string
	for i := range opts {
		label := opts[i].Label
		loc := onlyListedRE.FindStringIndex(label)
		lead := usPersonRE.FindStringIndex(label)
		if loc == nil || lead == nil || lead[1] > loc[0] {
			return Option{}, false
		}
		_, codes := scanCountries(label[loc[1]:], false)
		switch {
		case hasNegationToken(label[lead[1]:loc[1]]) && len(codes) == 0 && negating == nil:
			negating = &opts[i]
		case !hasNegationToken(label[lead[1]:loc[1]]) && len(codes) >= 2 && stating == nil:
			stating, list = &opts[i], codes
		default:
			return Option{}, false
		}
	}
	if stating == nil || negating == nil {
		return Option{}, false
	}
	for _, c := range cits {
		if !slices.Contains(list, c) {
			s.Tracef("citizenship %s is not among the %d countries the list names", c, len(list))
			return *negating, true
		}
	}
	s.reviewOnly = true
	s.Tracef("every recorded citizenship (%s) is among the %d countries the list names; a permanent residence elsewhere is not recorded, for review", strings.Join(cits, ", "), len(list))
	return *stating, true
}

// deriveUSPerson is the Derived slot of the export-control entry.
func deriveUSPerson(s *Step, e *Entry) (Draft, bool) {
	person, known, where := usPersonStatus(s, e)
	if !known {
		return abstainDraft(ReasonPlaceholderEmpty, "", "%s is blank and the sponsorship facts do not decide it", e.ID), true
	}
	return usPersonDraft(s, e, person, where), true
}

var (
	// usPersonClassRE is an option naming one class of U.S. person: a citizen
	// or a national (either word order; Planet: "A U.S. national (*)"), a
	// lawful permanent resident (green card holder), a refugee, an asylee or
	// another protected individual, such as a lawful temporary resident.
	usPersonClassRE = lazyregexp.New(`(?i)\b(?:u\.?\s?s\.?|united\s+states)\s+(?:citizen|national)|\b(?:citizen|national)\s+of\s+the\s+(?:u\.?\s?s\b|united\s+states)|\bpermanent\s+resident|\bgreen\s+card\b|\brefugee\b|\basylee\b|\bprotected\s+individuals?\b|\btemporary\s+resident\b`)
	// noneOfTheAboveRE is the option saying no listed class applies ("None of
	// the above", "I am none of the above", or a bare "Other").
	noneOfTheAboveRE = lazyregexp.New(`(?i)^\W*(?:(?:i\s+am|i['\x{2019}]m)\s+)?none\s+of\s+the\s+(?:above|listed|these)\b|^\W*other\W*$`)
)

// usPersonClassNone is the "None of the above" of a list that names only the
// classes of U.S. person (Astranis: citizen, permanent resident, refugee,
// asylee; Dimensional names them all in one option, Shield AI adds a protected
// individual and closes with "Other") and that one catch-all.
func usPersonClassNone(opts []Option) (Option, bool) {
	var none []Option
	classes := 0
	for _, o := range opts {
		switch {
		case noneOfTheAboveRE.MatchString(o.Label):
			none = append(none, o)
		case usPersonClassRE.MatchString(o.Label):
			classes += len(usPersonClassRE.FindAllString(o.Label, -1))
		default:
			return Option{}, false
		}
	}
	if classes < 2 || len(none) != 1 {
		return Option{}, false
	}
	return none[0], true
}

// usPersonStage answers a question whose options state U.S. person status,
// such as Greenhouse's "EXPORT COMPLIANCE" picker, the way the export-control
// entry would. A list of the U.S. person classes is answered "None of the
// above" for an applicant who is not a U.S. person, and with the class the
// user's own us-citizen-status names for one who is (Planet); which class a
// U.S. person belongs to the sponsorship facts do not say, so without it that
// list is left to the user.
func usPersonStage(s *Step) (Draft, bool) {
	opts := realOptions(s.Q.Options)
	stated := false
	for _, o := range opts {
		stated = stated || usPersonRE.MatchString(o.Label)
	}
	none, classList := usPersonClassNone(opts)
	if !stated && !classList {
		return Draft{}, false
	}
	e := entryNamed(s, IDUSPerson)
	person, known, where := usPersonStatus(s, e)
	if !known {
		return Draft{}, false
	}
	if stated {
		return usPersonDraft(s, e, person, where), true
	}
	chosen := none
	if person {
		_, kind, _ := ownUSStatus(s)
		var of []Option
		for _, o := range opts {
			if kind != "" && usStatusKind(o.Label) == kind {
				of = append(of, o)
			}
		}
		if len(of) != 1 {
			return Draft{}, false
		}
		chosen = of[0]
	}
	d := optionDraft(optionBinding{Label: chosen.Label, Value: chosen.Value, Score: 1}, e, SourceDerived, true)
	d.Start = startProfileDerive
	d.Trace = []string{"U.S. person=" + boolWord(person) + " from " + where + " -> " + quoteTerm(chosen.Label) + " of a list naming only the U.S. person classes"}
	return d, true
}

var (
	// clearanceAskRE is a question about a security clearance.
	// OpenAI: a "Full Scope Polygraph clearance" is one level of it.
	// Manifold spells it "clearence". Sphinx Defense: "an active U.S. Secret
	// clearance", the level for the clearance. Captivation: "Is Your
	// Clearance from the MD Agency?" and "Please Confirm Which Polygraph
	// Level You Have:", follow-ups about the applicant's own.
	clearanceAskRE = lazyregexp.New(`(?i)\b(?:security|government)\s+clear[ae]nces?\b|\bclear[ae]nce\s+(?:level|status)\b|` +
		`\b(?:(?:full[\s-]scope|lifestyle|counter[\s-]?intelligence|ci)\s+)?polygraph\s+clear[ae]nces?\b|\b(?:ts\s*/\s*sci|(?:top\s+)?secret)\s+clear[ae]nces?\b|` +
		`\byour\s+clear[ae]nce\b|\bpolygraph\s+level\b`)
	// clearanceObtainRE asks about being eligible for, able to obtain or
	// willing to obtain a clearance, which the history of holding one never
	// answers (Manifold: "elegible").
	clearanceObtainRE = lazyregexp.New(`(?i)\b(?:eligib\w*|elegib\w*|obtain\w*|acquir\w*|qualif\w*|willing|granted|able\s+to\s+(?:get|receive))\b`)
	// bareNoAnswerRE is an option that only answers No.
	bareNoAnswerRE = lazyregexp.New(`(?i)^\W*(?:none|no|n/?a)\W*$`)
	// usClearanceRE places the clearance in the U.S. government.
	usClearanceRE = lazyregexp.New(`(?i)\b` + usNameRE + `(?:\s+|\W)|\b(?:department\s+of\s+(?:defen[cs]e|energy)|dod|doe|federal)\b`)
	// neverHeldOptionRE is the option stating no clearance was ever held.
	neverHeldOptionRE = lazyregexp.New(`(?i)\bnever\s+(?:held|had|obtained)\b`)
	// noClearanceRE is a value or option stating no clearance is held now
	// (Onebrief: "NO, I currently do not hold active security clearance.").
	noClearanceRE = lazyregexp.New(`(?i)^\W*(?:none|no|n/?a|no\s+clearance|(?:no\W+)?i\s+(?:currently\s+)?(?:do\s+not|don'?t)\s+(?:currently\s+)?(?:have|hold|possess)\s+(?:a\s+|an\s+|any\s+)?(?:active\s+|current\s+)?(?:security\s+)?clearance)\W*$`)
	// noClearanceInterestRE is a No option that also says whether the
	// applicant would seek a clearance (Scale AI: "No - I am NOT interested in
	// obtaining one."); notInterestedRE is the negation in it.
	noClearanceInterestRE = lazyregexp.New(`(?i)^\W*no\b(.*\b(?:interested|willing|open)\b.*\b(?:obtain|get|pursu|acquir|apply)\w*.*)$`)
	notInterestedRE       = lazyregexp.New(`(?i)\bnot\b|n't\b`)
)

// noClearanceStage answers a clearance list for a user who recorded holding
// none: the option stating none was ever held (Astranis's "Never held a
// clearance") from a No to security-clearance-history ("Have you ever held a
// security clearance?"), a bare "None" from that or from a recorded level of
// None; one box of a checkbox group. A list that splits No by interest in
// obtaining one takes the side the willing-to-obtain-clearance entry states.
// A level the user recorded is the security-clearance entry's to answer.
func noClearanceStage(s *Step) (Draft, bool) {
	q := s.Q
	if q.Kind == KindText || !clearanceAskRE.MatchString(q.Text) {
		return Draft{}, false
	}
	// A bare No or None answers what is asked; asked about obtaining one, it
	// is not the history's to give. An option stating the history still is.
	asksObtain := clearanceObtainRE.MatchString(q.Text)
	level, hist := entryNamed(s, "security-clearance"), entryNamed(s, "security-clearance-history")
	own := ownText(level)
	if own != "" && !noClearanceRE.MatchString(own) {
		return Draft{}, false
	}
	never := noWordRE.MatchString(ownText(hist))
	if !never && own == "" {
		return Draft{}, false
	}
	willing := ownText(entryNamed(s, "willing-to-obtain-clearance"))
	seeks := yesWordRE.MatchString(willing)
	var hits []Option
	for _, o := range realOptions(q.Options) {
		if asksObtain && bareNoAnswerRE.MatchString(o.Label) {
			continue
		}
		if never && neverHeldOptionRE.MatchString(o.Label) || noClearanceRE.MatchString(o.Label) {
			hits = append(hits, o)
		}
		if m := noClearanceInterestRE.FindStringSubmatch(o.Label); m != nil && (seeks || noWordRE.MatchString(willing)) && seeks != notInterestedRE.MatchString(m[1]) {
			hits = append(hits, o)
		}
	}
	if len(hits) != 1 {
		return Draft{}, false
	}
	from, e := "security-clearance is "+quoteTerm(own), level
	if never {
		from, e = "security-clearance-history is No", hist
	}
	if noClearanceInterestRE.MatchString(hits[0].Label) {
		from += ", willing-to-obtain-clearance is " + quoteTerm(willing)
	}
	d := optionDraft(optionBinding{Label: hits[0].Label, Value: hits[0].Value, Score: 1}, e, SourceDerived, true)
	d.Start = startProfileDerive
	if q.Kind == KindCheckbox {
		d.ChosenLabels = []string{hits[0].Label}
	}
	d.Trace = []string{"no clearance: " + from + " -> " + quoteTerm(hits[0].Label)}
	return d, true
}

var (
	// clearanceEligibleRE asks whether the applicant is eligible for, or able
	// to obtain, a clearance; clearanceWillingRE asks whether they would seek
	// one, which their citizenship does not answer.
	clearanceEligibleRE = lazyregexp.New(`(?i)\b(?:eligib\w*|elegib\w*|qualif\w*|able\s+to\s+(?:get|receive|obtain|acquire)|be\s+granted|can\s+you\s+(?:get|obtain))\b`)
	clearanceWillingRE  = lazyregexp.New(`(?i)\b(?:willing|open\s+to|interested|prepared)\b`)
)

// clearanceEligibilityStage answers a Yes/No about being eligible for, or able
// to obtain, a U.S. security clearance (or holding one) from the recorded
// citizenship: a U.S. clearance is granted to U.S. citizens only, so a recorded
// status that is not a citizen's answers No (Manifold: "Are you elegible for a
// U.S. Department of Defense security clearence or currently hold one?"). A
// citizen's eligibility turns on more than the record and stays the user's, as
// does a clearance the question does not place in the U.S.
func clearanceEligibilityStage(s *Step) (Draft, bool) {
	q := s.Q
	if q.Kind == KindText || !clearanceAskRE.MatchString(q.Text) || !clearanceEligibleRE.MatchString(q.Text) ||
		clearanceWillingRE.MatchString(q.Text) || !usClearanceRE.MatchString(q.Text) {
		return Draft{}, false
	}
	e := entryNamed(s, idUSCitizenStatus)
	stated := strings.TrimSpace(ownText(e))
	if stated == "" || !citizenStatusNoRE.MatchString(stated) {
		return Draft{}, false
	}
	no, okN := ownYesNo(q.Options, false)
	if _, okY := ownYesNo(q.Options, true); !okY || !okN {
		return Draft{}, false
	}
	return Draft{Entry: e, Answer: no.Label, ChosenLabel: no.Label, HasOption: true, OptionScore: 1, Source: SourceDerived, Start: startProfileDerive,
		Trace: []string{"clearance eligibility: a U.S. security clearance is granted to U.S. citizens only; the recorded us-citizen-status is " +
			quoteTerm(stated) + " -> " + no.Label}}, true
}

var (
	// neverEmployedRE is an option saying the applicant was never employed by
	// the employers it lists.
	neverEmployedRE = lazyregexp.New(`(?i)^\W*i\s+have\s+never\s+(?:been\s+employed\s+by|worked\s+for)\s+(.+?)\W*$`)
	// publicStatusRE is an option about being or having been the employee of a
	// government, a party, a royal family or a candidate.
	publicStatusRE = lazyregexp.New(`(?i)\b(?:employed\s+by|employee\s+of)\b.*\b(?:government|` + publicEmployers + `)\b`)
	listSepRE      = lazyregexp.New(`(?i)\s*,\s*(?:or\s+)?|\s+or\s+`)
	governmentRE   = lazyregexp.New(`(?i)^(?:(?:the|a|an)\s+)?(?:(?:u\.?\s?s\.?|united\s+states)\s+)?(?:federal\s+|state\s+|local\s+)?government(?:\s+entity)?$`)
	partyRoyalRE   = lazyregexp.New(`(?i)^(?:(?:a|an)\s+)?(?:political\s+party|member\s+of\s+(?:a\s+)?royal\s+family|candidate\s+for\s+(?:political\s+)?office)$`)
	quoteFold      = strings.NewReplacer("‘", "", "’", "", "“", "", "”", "", "'", "", `"`, "")
)

// neverPublicStage answers an employment status list from the user's Nos: the
// one option saying the applicant was never employed by the employers it
// lists, beside options about being or having been their employee (Booz
// Allen's "I have never been employed by the U.S. Federal Government."). A
// government needs a No to government-employee-history ("Have you ever been a
// government employee?"), a party, a royal family or a candidate one to
// party-royal-candidate-employment; any other employer in the list leaves it.
// The question is the list's own; its text may be only a note.
func neverPublicStage(s *Step) (Draft, bool) {
	q := s.Q
	if q.Kind == KindText || q.Kind == KindCheckbox {
		return Draft{}, false
	}
	var never []Option
	var names []string
	status := false
	for _, o := range realOptions(q.Options) {
		label := quoteFold.Replace(o.Label)
		if m := neverEmployedRE.FindStringSubmatch(label); m != nil {
			never, names = append(never, o), listSepRE.Split(m[1], -1)
		} else if publicStatusRE.MatchString(label) {
			status = true
		}
	}
	if len(never) != 1 || !status {
		return Draft{}, false
	}
	var from []string
	var e *Entry
	for _, n := range names {
		id := "party-royal-candidate-employment"
		switch {
		case governmentRE.MatchString(n):
			id = "government-employee-history"
		case !partyRoyalRE.MatchString(n):
			return Draft{}, false
		}
		ent := entryNamed(s, id)
		if !noWordRE.MatchString(ownText(ent)) {
			return Draft{}, false
		}
		if !slices.Contains(from, id) {
			from = append(from, id)
		}
		if e == nil {
			e = ent
		}
	}
	d := optionDraft(optionBinding{Label: never[0].Label, Value: never[0].Value, Score: 1}, e, SourceDerived, true)
	d.Start = startProfileDerive
	d.Trace = []string{"never employed: " + strings.Join(from, " and ") + " No -> " + quoteTerm(never[0].Label)}
	return d, true
}
