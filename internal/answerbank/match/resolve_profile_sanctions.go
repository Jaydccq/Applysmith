package match

import (
	"slices"
	"strings"

	"applysmith/internal/lazyregexp"
)

// A sanctions screen (Databricks: "Please confirm whether any of the below
// applies to you. Select all that apply. Note: This information will only be
// used to ensure compliance with U.S. sanctions and export controls.") lists
// statements of citizenship or residence in the places under comprehensive
// sanctions and one "None of the above"; its follow-up asks what else applies
// to one who chose a statement, with a "Not applicable" for one who chose none.
// They are answered as the question asks, from nationality (every citizenship
// of a dual national) and from where the applicant lives.

var (
	sanctionsAskRE = lazyregexp.New(`(?i)\bsanctions?\b|\bexport\s+controls?\b|\bembargo(?:ed)?\b`)
	// sanctionsCitizenRE is a statement of citizenship; sanctionsResidentRE
	// one of residence, which "permanent resident" also is.
	sanctionsCitizenRE  = lazyregexp.New(`(?i)\b(?:citizen(?:ship)?|national(?:ity)?)\b`)
	sanctionsResidentRE = lazyregexp.New(`(?i)\b(?:residen(?:t|ce|cy)|resides?|residing|lives?|living|located)\b`)
	// sanctionedRegionRE names a region of Ukraine under comprehensive
	// sanctions; a Ukrainian address does not say whether it lies in one.
	sanctionedRegionRE = lazyregexp.New(`(?i)\b(?:crimea|donetsk|luhansk|zaporizhzhia|kherson|sevastopol)\b`)
	// sanctionsFollowUpRE is the follow-up asked of one who chose a statement;
	// sanctionsNotApplicableRE its option for one who chose none.
	sanctionsFollowUpRE      = lazyregexp.New(`(?i)\bif\s+you\s+selected\b.*\bother\s+than\b.*\bnone\s+of\s+the\s+above\b`)
	sanctionsNotApplicableRE = lazyregexp.New(`(?i)^\W*not\s+applicable\b.*\bnone\s+of\s+the\s+above\b`)
	// sanctionsNotOfRE is a follow-up's option for one the screen does not
	// concern, saying why (Contentful: "Not Applicable. I am NOT a citizen,
	// national, or permanent resident of Iran, Cuba, North Korea, or Syria").
	sanctionsNotOfRE = lazyregexp.New(`(?i)^\W*not\s+applicable\W+i\s+am\s+not\s+an?\s`)
	// sanctionsAskSentenceRE is a question's last sentence, the one it asks;
	// sanctionsOpenListRE a list that reaches past the places it names.
	sanctionsAskSentenceRE = lazyregexp.New(`[^.?!]*\?[\s*]*$`)
	sanctionsOpenListRE    = lazyregexp.New(`(?i)\b(?:any|other|another)\s+(?:\w+\s+){0,3}(?:countr(?:y|ies)|nations?|regions?|territor(?:y|ies)|places?)\b|\betc\b`)
	// sanctionsIfSoTailRE is an instruction after the question for one who
	// answers yes (KnowBe4: "If so, indicate which territories."), which asks
	// nothing of one who answers no.
	sanctionsIfSoTailRE = lazyregexp.New(`(?i)\?\s*if\s+(?:so|yes)\b[^?]*$`)
	// sanctionsInlineListRE announces a list whose places follow in the same
	// question, after a colon or a bracket (Planet: "any of the following
	// countries: Cuba, Iran, North Korea, Syria, or the following territories
	// of Ukraine (Luhansk, Donetsk, Crimea)?"), unlike a list above or below.
	sanctionsInlineListRE = lazyregexp.New(`(?i)\b(?:any\s+of\s+)?the\s+following\s+(?:countr(?:y|ies)|territor(?:y|ies))(?:\s+of\s+\w+)?\s*[:(]`)
	// sanctionsReversedRE is a question that asks the applicant to deny or to
	// confirm ("Can you confirm that you are not a citizen ..."): its No does
	// not say what a No to "Are you a citizen ..." says.
	sanctionsReversedRE = lazyregexp.New(`(?i)\b(?:not|neither|nor|confirm|certify|attest|acknowledge)\b`)
	// sanctionsNoLeadRE is an option that answers no in its first word
	// ("No. I am not located in or a national of ...").
	sanctionsNoLeadRE = lazyregexp.New(`(?i)^\W*no\b`)
)

// sanctionedCitizenship are the citizenships a screen's statements name;
// sanctionedResidence adds Ukraine, whose sanctioned regions an address
// cannot tell apart.
var (
	sanctionedCitizenship = setOf("CU", "IR", "KP", "SY", "RU", "BY")
	sanctionedResidence   = setOf("CU", "IR", "KP", "SY", "RU", "BY", "UA")
)

// recordedCitizenships are the citizenships the user recorded: the profile's
// work_authorization.citizenships and the countries the country-of-citizenship
// entry names, together, as ISO alpha-2 codes.
func recordedCitizenships(s *Step) []string {
	var out []string
	add := func(v string) {
		if c, ok := CountryCode(v); ok && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	for _, c := range s.Req.Facts.Citizenships {
		add(c)
	}
	if e := entryNamed(s, idCountryOfCitizenship); e != nil {
		switch e.Kind {
		case KindPickOption:
			for _, p := range e.Preferred {
				add(p)
			}
			add(e.FallbackText)
		case KindFixed:
			add(e.Answer)
		}
	}
	return out
}

// recordedHomeCountry is the recorded address's country code, "" when it is
// blank or held for the applicant.
func recordedHomeCountry(s *Step) string {
	p := partOf(s, "address-country")
	if p.askMe {
		return ""
	}
	home, _ := CountryCode(p.text)
	return home
}

// sanctionsChecklist answers a sanctions screen "None of the above" when no
// statement holds: no recorded citizenship is of a country a citizenship
// statement names, and the recorded address is in no country a residence
// statement names. A statement that holds is left for the applicant, and so is
// a screen whose citizenship or address is not recorded.
func sanctionsChecklist(s *Step) (Draft, bool) {
	q := s.Q
	if !sanctionsAskRE.MatchString(q.Text) {
		return Draft{}, false
	}
	var none []Option
	var statements []Option
	for _, o := range realOptions(q.Options) {
		if noneOfTheAboveRE.MatchString(o.Label) || noneOptions[normalizeLabel(o.Label)] {
			none = append(none, o)
		} else {
			statements = append(statements, o)
		}
	}
	if len(none) != 1 || len(statements) == 0 {
		return Draft{}, false
	}
	citizens, home := recordedCitizenships(s), recordedHomeCountry(s)
	for _, o := range statements {
		_, codes := scanCountries(o.Label, false)
		if sanctionedRegionRE.MatchString(o.Label) && !slices.Contains(codes, "UA") {
			codes = append(codes, "UA")
		}
		citizen, resident := sanctionsCitizenRE.MatchString(o.Label), sanctionsResidentRE.MatchString(o.Label)
		if len(codes) == 0 || slices.ContainsFunc(codes, func(c string) bool { return !sanctionedResidence[c] }) || !citizen && !resident {
			return Draft{}, false
		}
		if citizen {
			if len(citizens) == 0 {
				return abstainDraft(ReasonPlaceholderEmpty, "", "a sanctions screen asks about citizenship and none is recorded (work_authorization.citizenships)"), true
			}
			if slices.ContainsFunc(citizens, func(c string) bool { return slices.Contains(codes, c) }) {
				return abstainDraft(ReasonContextPolicy, DetailDisqualifyingDisclose, "a sanctions statement names a recorded citizenship: left for the applicant"), true
			}
		}
		if resident {
			if home == "" {
				return abstainDraft(ReasonPlaceholderEmpty, "", "a sanctions screen asks where you live and the recorded address names no country"), true
			}
			if slices.Contains(codes, home) {
				return abstainDraft(ReasonContextPolicy, DetailDisqualifyingDisclose, "a sanctions statement names the recorded address's country: left for the applicant"), true
			}
		}
	}
	return chosenOption(s, none[0], "sanctions screen: no statement holds for citizenship "+strings.Join(citizens, "/")+" and an address in "+home), true
}

// sanctionsFollowUp answers the follow-up of a sanctions screen "Not
// applicable" for an applicant the screen's "None of the above" fits: no
// recorded citizenship and no recorded address country is one under
// comprehensive sanctions. Anyone else is left to the applicant.
func sanctionsFollowUp(s *Step) (Draft, bool) {
	if !sanctionsFollowUpRE.MatchString(s.Q.Text) {
		return sanctionsNotOf(s)
	}
	var na []Option
	for _, o := range realOptions(s.Q.Options) {
		if sanctionsNotApplicableRE.MatchString(o.Label) {
			na = append(na, o)
		}
	}
	if len(na) != 1 {
		return Draft{}, false
	}
	citizens, home := recordedCitizenships(s), recordedHomeCountry(s)
	if len(citizens) == 0 || home == "" ||
		slices.ContainsFunc(citizens, func(c string) bool { return sanctionedCitizenship[c] }) || sanctionedResidence[home] {
		return Draft{}, false
	}
	return chosenOption(s, na[0], "sanctions follow-up: citizenship "+strings.Join(citizens, "/")+" and an address in "+home+" chose no statement"), true
}

// sanctionsNotOf answers a follow-up whose question names only places under
// comprehensive sanctions with its own "Not applicable. I am not a citizen ...
// of <places>" (Contentful: "Do you hold an ADDITIONAL citizenship ... of any
// other country that is NOT Iran, Cuba, Syria or North Korea?"), when the
// statement holds: no recorded citizenship is of a place it names and, when it
// names residence, the recorded address is in none. A statement that fails,
// and a citizenship or address not recorded, are left for the applicant.
func sanctionsNotOf(s *Step) (Draft, bool) {
	sanctioned := func(codes []string) bool {
		return len(codes) >= 2 && !slices.ContainsFunc(codes, func(c string) bool { return !sanctionedResidence[c] })
	}
	if _, asked := scanCountries(s.Q.Text, false); !sanctioned(asked) {
		return Draft{}, false
	}
	var na []Option
	for _, o := range realOptions(s.Q.Options) {
		if sanctionsNotOfRE.MatchString(o.Label) {
			na = append(na, o)
		}
	}
	if len(na) != 1 {
		return Draft{}, false
	}
	_, codes := scanCountries(na[0].Label, false)
	citizen, resident := sanctionsCitizenRE.MatchString(na[0].Label), sanctionsResidentRE.MatchString(na[0].Label)
	if !sanctioned(codes) || !citizen && !resident {
		return Draft{}, false
	}
	citizens, home := recordedCitizenships(s), recordedHomeCountry(s)
	if citizen && (len(citizens) == 0 || slices.ContainsFunc(citizens, func(c string) bool { return slices.Contains(codes, c) })) {
		return Draft{}, false
	}
	if resident && (home == "" || slices.Contains(codes, home)) {
		return Draft{}, false
	}
	return chosenOption(s, na[0], "sanctions follow-up: the option's own statement holds for citizenship "+strings.Join(citizens, "/")+" and an address in "+home), true
}

// sanctionsYesNo answers No to a Yes/No whose own question names the places
// under comprehensive sanctions and asks whether the applicant is a citizen or
// a resident of one (Asana: "Are you a citizen or legal permanent resident of
// Cuba, Iran, North Korea, Syria or Ukraine (Crimea region)?"), when no
// recorded citizenship is one of them and, for residence, the recorded address
// is in none. The list is whole in the question, unlike one "above" or
// "below" the page may not have captured (listedCountryCitizenship). A place
// that holds, a citizenship or address not recorded, and a question that asks
// to deny or confirm (sanctionsReversedRE) are left for the applicant. The Yes
// may also be the places themselves, offered to pick from beside one option
// that opens with "No" (KnowBe4).
func sanctionsYesNo(s *Step) (Draft, bool) {
	ask := sanctionsAskSentenceRE.FindString(sanctionsIfSoTailRE.ReplaceAllString(s.Q.Text, "?"))
	ask = sanctionsInlineListRE.ReplaceAllString(ask, " ")
	if ask == "" || countryListRE.MatchString(ask) || countryFollowingRE.MatchString(ask) || sanctionsOpenListRE.MatchString(ask) || sanctionsReversedRE.MatchString(ask) {
		return Draft{}, false
	}
	citizen, resident := sanctionsCitizenRE.MatchString(ask), sanctionsResidentRE.MatchString(ask)
	_, codes := scanCountries(ask, false)
	if sanctionedRegionRE.MatchString(ask) && !slices.Contains(codes, "UA") {
		codes = append(codes, "UA")
	}
	if !citizen && !resident || len(codes) < 2 || slices.ContainsFunc(codes, func(c string) bool { return !sanctionedResidence[c] }) {
		return Draft{}, false
	}
	no, ok := ownYesNo(s.Q.Options, false)
	if _, okY := ownYesNo(s.Q.Options, true); !ok || !okY {
		if no, ok = placesBesideNo(s.Q.Options); !ok {
			return Draft{}, false
		}
	}
	citizens, home := recordedCitizenships(s), recordedHomeCountry(s)
	if citizen && (len(citizens) == 0 || slices.ContainsFunc(citizens, func(c string) bool { return slices.Contains(codes, c) })) {
		return Draft{}, false
	}
	if resident && (home == "" || slices.Contains(codes, home)) {
		return Draft{}, false
	}
	return chosenOption(s, no, "sanctions Yes/No: the places it names hold neither citizenship "+strings.Join(citizens, "/")+" nor an address in "+home), true
}

// placesBesideNo is the one option that opens with "No" among options that
// are otherwise at least two places under comprehensive sanctions and nothing
// else.
func placesBesideNo(opts []Option) (Option, bool) {
	var no []Option
	places := 0
	for _, o := range realOptions(opts) {
		if sanctionsNoLeadRE.MatchString(o.Label) {
			no = append(no, o)
			continue
		}
		_, codes := scanCountries(o.Label, false)
		if len(codes) == 0 && !sanctionedRegionRE.MatchString(o.Label) || slices.ContainsFunc(codes, func(c string) bool { return !sanctionedResidence[c] }) {
			return Option{}, false
		}
		places++
	}
	if len(no) != 1 || places < 2 {
		return Option{}, false
	}
	return no[0], true
}
