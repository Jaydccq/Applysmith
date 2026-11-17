package match

import (
	"slices"
	"strings"

	"applysmith/internal/lazyregexp"
)

var (
	citizenshipAskRE = lazyregexp.New(`(?i)\b(?:citizenship|citizen|nationality|national\s+of)\b`)
	// countryListRE and countryFollowingRE read the export-control shape: the
	// list of countries sits in the text, before the ask ("listed above") or
	// after it ("any of the following countries").
	countryListRE      = lazyregexp.New(`(?i)\b(?:any\s+of\s+the\s+)?countr(?:y|ies)\b[^?]{0,40}\b(?:listed|list(?:ed)?\s+(?:above|below)|above|below)\b`)
	countryFollowingRE = lazyregexp.New(`(?i)\bfollowing\s+countr(?:y|ies)\b`)
	// listItemBeforeRE and listItemAfterRE are what stands on each side of an
	// item of a list: its start or end, a separator, "and" or "or".
	listItemBeforeRE = lazyregexp.New(`(?i)(?:^|[,:;/(\x{2022}\n]|(?:^|\s)[-*]|\b(?:and|or))\s*$`)
	listItemAfterRE  = lazyregexp.New(`(?i)^\s*(?:$|[,;.?/()\n]|(?:and|or)\b)`)
)

// listsCountry reports the country as an item of a list in the text. A country
// the text names in a sentence ("to comply with United States export control
// laws") is not one of the countries it lists.
func listsCountry(text, country string) bool {
	low, c := strings.ToLower(text), strings.ToLower(country)
	for from := 0; ; {
		i := wordIndex(low[from:], c)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(c)
		if listItemBeforeRE.MatchString(low[:start]) && listItemAfterRE.MatchString(low[end:]) {
			return true
		}
		from = end
	}
}

const idCountryOfCitizenship = "country-of-citizenship"

// listedCountryCitizenship answers an export-control Yes/No that lists countries
// in its text and asks about citizenship: Yes when a citizenship the user
// recorded (in the country-of-citizenship entry or the profile's citizenships,
// every one of a dual national) is an item of the list (listsCountry; after
// "the following countries" when the question says so), and otherwise nothing. A miss cannot be told from a list the page did not
// capture, and a silent No on an export-control question would misstate the
// user, so it is never answered No.
func listedCountryCitizenship(s *Step) (Draft, bool) {
	text := s.Q.Text
	if !citizenshipAskRE.MatchString(text) || !(countryListRE.MatchString(text) || countryFollowingRE.MatchString(text)) {
		return Draft{}, false
	}
	e := entryNamed(s, idCountryOfCitizenship)
	var claims []string
	if e != nil {
		switch e.Kind {
		case KindPickOption:
			claims = append(append(claims, e.Preferred...), e.FallbackText)
		case KindFixed:
			claims = append(claims, e.Answer)
		}
	}
	for _, c := range s.Req.Facts.Citizenships {
		if name := countryByCode[strings.ToUpper(strings.TrimSpace(c))]; name != "" {
			claims = append(claims, name)
		}
	}
	list := text
	if loc := countryFollowingRE.FindStringIndex(text); loc != nil {
		list = text[loc[1]:]
	}
	hit := ""
	for _, c := range claims {
		if c = strings.TrimSpace(c); len([]rune(c)) >= 3 && listsCountry(list, c) {
			hit = c
			break
		}
	}
	if hit == "" {
		return Draft{}, false
	}
	d := Draft{Entry: e, Answer: "Yes", Source: SourceDerived, Start: startProfileDerive,
		Trace: []string{"listed-country-citizenship: question names " + hit + " (a recorded citizenship) -> Yes"}}
	if hasRenderedOptions(s.Q.Options) {
		yes, ok := ownYesNo(s.Q.Options, true)
		if !ok {
			return Draft{}, false
		}
		d.Answer, d.ChosenLabel, d.HasOption, d.OptionScore = yes.Label, yes.Label, true, 1
	}
	return d, true
}

// secondCitizenshipAskRE asks for a second citizenship or nationality.
var secondCitizenshipAskRE = lazyregexp.New(`(?i)\b(?:second|secondary|2nd|additional)\s+(?:country\s+of\s+)?(?:citizenship|nationality)\b`)

// secondCitizenshipNone answers a second-citizenship picker with its one
// option for none ("Not Applicable") when the profile records the applicant's
// citizenships and they, with the country-of-citizenship entry, name one
// country. A list sent without its options (over the engine's cap) is answered
// "Not Applicable", with the other ways a list writes none for the apply step
// to search in turn. An unrecorded list cannot say there is no other, and a
// dual national's second is left to the user.
func secondCitizenshipNone(s *Step) (Draft, bool) {
	if !secondCitizenshipAskRE.MatchString(s.Q.Text) || len(s.Req.Facts.Citizenships) == 0 {
		return Draft{}, false
	}
	cits := recordedCitizenships(s)
	if len(cits) != 1 {
		return Draft{}, false
	}
	trace := "second-citizenship: the profile records one citizenship (" + cits[0] + ") -> "
	e := entryNamed(s, idCountryOfCitizenship)
	if !hasRenderedOptions(s.Q.Options) {
		if s.Q.Kind != KindCustomListbox && s.Q.Kind != KindSelect {
			return Draft{}, false
		}
		return Draft{Entry: e, Answer: "Not Applicable", Alternatives: []string{"N/A", "None"}, Source: SourceDerived, Start: startProfileDerive,
			Trace: []string{trace + "\"Not Applicable\" on a list sent without its options"}}, true
	}
	var none []Option
	for _, o := range realOptions(s.Q.Options) {
		if listNoneRE.MatchString(o.Label) {
			none = append(none, o)
		}
	}
	if len(none) != 1 {
		return Draft{}, false
	}
	n := none[0]
	return Draft{Entry: e, Answer: n.Value, ChosenLabel: n.Label, HasOption: true, OptionScore: 1,
		Source: SourceDerived, Start: startProfileDerive, Trace: []string{trace + quoteTerm(n.Label)}}, true
}

var (
	// citizenshipCountryAskRE asks which country the applicant is a citizen
	// of; citizenshipResidenceRE brings in a permanent residence, which a
	// list of citizenships does not answer ("Since obtaining your most recent
	// citizenship, did you afterwards become a permanent resident ...").
	citizenshipCountryAskRE = lazyregexp.New(`(?i)\b(?:which|what)\s+countr(?:y|ies)(?:\s*/\s*regions?)?\b[^?]{0,40}\b(?:citizenships?|citizen|nationality)\b|\bcountr(?:y|ies)(?:\s*\(ies\))?(?:\s*/\s*regions?)?\s+of\s+(?:citizenship|nationality)\b|^\W*(?:citizenships?|nationality|nationalities)\W*$`)
	citizenshipResidenceRE  = lazyregexp.New(`(?i)\bpermanent(?:ly)?\s+residen\w*`)
)

// citizenshipList answers a multi-value list that asks the applicant's country
// of citizenship (Twitch: "In which country/region do you have citizenship?",
// every country, so a dual national picks each) with every citizenship
// recorded: each one's option when the list arrives with its options, else the
// countries' names for the apply step to pick. A list that lacks one, an
// export-control list, a second citizenship's or one that brings in a
// permanent residence is left.
func citizenshipList(s *Step) (Draft, bool) {
	q := s.Q
	if q.InputType != InputMultiSelect || q.Kind != KindCustomListbox && q.Kind != KindSelect || !citizenshipCountryAskRE.MatchString(q.Text) ||
		secondCitizenshipAskRE.MatchString(q.Text) || citizenshipResidenceRE.MatchString(q.Text) || countryListRE.MatchString(q.Text) || countryFollowingRE.MatchString(q.Text) {
		return Draft{}, false
	}
	cits := recordedCitizenships(s)
	if len(cits) == 0 {
		return Draft{}, false
	}
	real := realOptions(q.Options)
	var labels []string
	for _, c := range cits {
		if len(real) == 0 {
			labels = append(labels, countryName(c))
			continue
		}
		i := slices.IndexFunc(real, func(o Option) bool { code, ok := CountryCode(o.Label); return ok && code == c })
		if i < 0 {
			return Draft{}, false
		}
		labels = append(labels, real[i].Label)
	}
	answer := strings.Join(labels, ", ")
	return Draft{Entry: entryNamed(s, idCountryOfCitizenship), Answer: answer, ChosenLabel: labels[0], ChosenLabels: labels,
		HasOption: len(real) > 0, OptionScore: 1, Source: SourceDerived, Start: startProfileDerive, Deterministic: true,
		Trace: []string{"citizenship list: every recorded citizenship -> " + quoteTerm(answer)}}, true
}
