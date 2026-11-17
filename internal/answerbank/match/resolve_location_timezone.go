package match

import (
	"slices"
	"strconv"
	"strings"

	"applysmith/internal/lazyregexp"
)

// The time-zone picker renders one zone in many label formats ("Eastern Time
// Zone (America/Detroit)", "(UTC-05:00) Eastern Time (US & Canada)",
// "America/New_York", "EST"). The user sets one zone; the table turns it into
// every spelling so the option binder finds whichever a form renders. A closed
// domain, so a small explicit table beats fuzzy geography.

type usTimeZone struct {
	names, ianas, cities, abbrevs, offsets []string
	// std is the standard offset from UTC in hours; over a year the zone
	// observes std, and std+1 in summer unless it keeps standard time all year.
	std     int
	allYear bool
}

var usTimeZones = []usTimeZone{
	{
		names:   []string{"Eastern Time Zone", "Eastern Time", "Eastern Standard Time", "Eastern Daylight Time", "US/Eastern"},
		ianas:   []string{"America/New_York", "America/Detroit"},
		cities:  []string{"New York", "Detroit"},
		abbrevs: []string{"EST", "EDT", "ET"},
		offsets: []string{"UTC-5", "UTC-05:00", "UTC -5", "GMT-5", "-05:00", "UTC-4", "UTC-04:00"},
		std:     -5,
	},
	{
		names:   []string{"Central Time Zone", "Central Time", "Central Standard Time", "Central Daylight Time", "US/Central"},
		ianas:   []string{"America/Chicago", "America/Dallas"},
		cities:  []string{"Chicago", "Dallas"},
		abbrevs: []string{"CST", "CDT", "CT"},
		offsets: []string{"UTC-6", "UTC-06:00", "UTC -6", "GMT-6", "-06:00", "UTC-5", "UTC-05:00"},
		std:     -6,
	},
	{
		names:   []string{"Mountain Time Zone", "Mountain Time", "Mountain Standard Time", "Mountain Daylight Time", "US/Mountain"},
		ianas:   []string{"America/Denver", "America/Phoenix"},
		cities:  []string{"Denver", "Phoenix"},
		abbrevs: []string{"MST", "MDT", "MT"},
		offsets: []string{"UTC-7", "UTC-07:00", "UTC -7", "GMT-7", "-07:00", "UTC-6", "UTC-06:00"},
		std:     -7,
	},
	{
		names:   []string{"Pacific Time Zone", "Pacific Time", "Pacific Standard Time", "Pacific Daylight Time", "US/Pacific"},
		ianas:   []string{"America/Los_Angeles", "America/San_Diego"},
		cities:  []string{"Los Angeles", "San Diego", "San Francisco", "Seattle"},
		abbrevs: []string{"PST", "PDT", "PT"},
		offsets: []string{"UTC-8", "UTC-08:00", "UTC -8", "GMT-8", "-08:00", "UTC-7", "UTC-07:00"},
		std:     -8,
	},
	{
		names:   []string{"Alaska Time Zone", "Alaskan Time", "Alaska Standard Time", "Alaska Daylight Time", "US/Alaska"},
		ianas:   []string{"America/Anchorage", "America/Fairbanks", "America/Juneau"},
		cities:  []string{"Anchorage", "Fairbanks", "Juneau"},
		abbrevs: []string{"AKST", "AKDT", "AKT"},
		offsets: []string{"UTC-9", "UTC-09:00", "UTC -9", "GMT-9", "-09:00", "UTC-8", "UTC-08:00"},
		std:     -9,
	},
	{
		names:   []string{"Hawaii-Aleutian Time Zone", "Hawaii Time", "Hawaii Standard Time", "Hawaii-Aleutian Standard Time", "US/Hawaii"},
		ianas:   []string{"Pacific/Honolulu", "America/Honolulu", "America/Adak"},
		cities:  []string{"Honolulu"},
		abbrevs: []string{"HST", "HAST", "HT"},
		offsets: []string{"UTC-10", "UTC-10:00", "UTC -10", "GMT-10", "-10:00"},
		std:     -10,
		allYear: true,
	},
}

var nonAlnumRE = lazyregexp.New(`[^a-z0-9]+`)

func tzNorm(s string) string {
	return strings.TrimSpace(nonAlnumRE.ReplaceAllString(strings.ToLower(s), " "))
}

// aliases lists names, then IANA ids, cities, abbreviations and offsets: the
// binder takes the first option that scores, so an unambiguous name wins over
// an offset a neighbouring zone shares in the other season.
func (z usTimeZone) aliases() []string {
	return slices.Concat(z.names, z.ianas, z.cities, z.abbrevs, z.offsets)
}

// identifies reports that a preferred value names this zone. Offsets never
// identify a zone (they are seasonally ambiguous). An abbreviation matches as a
// whole word only, so "Eastern Daylight Time" does not pull in the Hawaii "HT"
// from the letters of "daylight".
func (z usTimeZone) identifies(preferred string) bool {
	np := tzNorm(preferred)
	if np == "" {
		return false
	}
	words := strings.Fields(np)
	for _, a := range slices.Concat(z.names, z.ianas, z.cities) {
		na := tzNorm(a)
		if na == np || strings.Contains(np, na) || len(np) >= 4 && strings.Contains(na, np) {
			return true
		}
	}
	for _, a := range z.abbrevs {
		if slices.Contains(words, tzNorm(a)) {
			return true
		}
	}
	return false
}

// expandTimeZoneAliases is the preferred values first, then the aliases of every
// zone they name, de-duplicated by normalized form. A value that names no zone
// is returned unchanged.
func expandTimeZoneAliases(preferred []string) []string {
	var zones []usTimeZone
	for _, z := range usTimeZones {
		if slices.ContainsFunc(preferred, z.identifies) {
			zones = append(zones, z)
		}
	}
	if len(zones) == 0 {
		return slices.Clone(preferred)
	}
	seen := map[string]bool{}
	var out []string
	add := func(a string) {
		if k := tzNorm(a); k != "" && !seen[k] {
			seen[k] = true
			out = append(out, a)
		}
	}
	for _, p := range preferred {
		add(p)
	}
	for _, z := range zones {
		for _, a := range z.aliases() {
			add(a)
		}
	}
	return out
}

var (
	tzRangeQuestionRE = lazyregexp.New(`(?i)^\W*(?:are|is)\s+you\s+(?:currently\s+)?(?:located|based|living|residing)\s+(?:within|in)\s+[^?]*\btime\s*zones?\b`)
	tzRangeRE         = lazyregexp.New(`(?i)\b(?:between|from)\s+(?:GMT|UTC)\s*([+-]\d{1,2})(?::?00)?\s+(?:and|to)\s+(?:GMT|UTC)\s*([+-]\d{1,2})(?::?00)?\b`)
)

// timeZoneRangeStage answers "Are you located within a ... time zone? (Between
// GMT-7 and GMT-3)" from the stored time zone: Yes when every offset the zone
// observes over a year is in the range, No when none is. A zone that is in the
// range for part of the year (Pacific time against GMT-7) is not decided, and
// neither is a stored value that names no single US zone.
func timeZoneRangeStage(s *Step) (Draft, bool) {
	q := s.Q
	m := tzRangeRE.FindStringSubmatch(q.Text)
	if m == nil || !tzRangeQuestionRE.MatchString(q.Text) || hasNegationToken(q.Text) {
		return Draft{}, false
	}
	if _, ok := ownYesNoExactlyOne(q.Options, true); !ok {
		return Draft{}, false
	}
	if _, ok := ownYesNoExactlyOne(q.Options, false); !ok {
		return Draft{}, false
	}
	e := entryNamed(s, "time-zone")
	if e == nil {
		return Draft{}, false
	}
	if ownText(e) == "" {
		return abstainDraft(ReasonPlaceholderEmpty, "", "time-zone is blank"), true
	}
	var zones []usTimeZone
	for _, z := range usTimeZones {
		if slices.ContainsFunc(e.Preferred, z.identifies) {
			zones = append(zones, z)
		}
	}
	if len(zones) != 1 {
		return abstainDraft(ReasonAmbiguous, "", "the stored time zone does not name one US zone: left for review"), true
	}
	a, _ := strconv.Atoi(strings.ReplaceAll(m[1], "+", ""))
	b, _ := strconv.Atoi(strings.ReplaceAll(m[2], "+", ""))
	lo, hi := min(a, b), max(a, b)
	z := zones[0]
	first, last := z.std, z.std+1
	if z.allYear {
		last = first
	}
	trace := "time-zone range: the stored zone is UTC" + strconv.Itoa(first) + " to UTC" + strconv.Itoa(last) +
		" over a year; the question's range is " + strconv.Itoa(lo) + " to " + strconv.Itoa(hi)
	switch {
	case first >= lo && last <= hi:
		trace += " -> inside"
		return finishDerived(bindEntryAnswer(s, e, "Yes", trace)), true
	case last < lo || first > hi:
		trace += " -> outside"
		return finishDerived(bindEntryAnswer(s, e, "No", trace)), true
	}
	return abstainDraft(ReasonAmbiguous, "", "the stored zone is inside the question's range for only part of the year: left for review"), true
}

var (
	// tzNamedAskRE asks whether the applicant is located in a named time zone
	// (Starburst: "Are you located in PST?"), or in one of several ("Do you
	// currently reside in a Central or Eastern Time Zone (U.S.)?"); the zones
	// are the first capture, the words "time zone" the second.
	tzNamedAskRE = lazyregexp.New(`(?i)^\W*(?:are|do)\s+you\s+(?:currently\s+)?(?:located|based|living|live|reside|residing)\s+(?:in|within)\s+(?:the\s+|an?\s+)?([a-z][a-z.,\s-]{0,60}?)(\s+time\s*zones?)?(?:\s*\((?:u\.?s\.?a?|united\s+states)\))?\s*\??\s*$`)
	// tzNamedOtherRE marks working hours or a willingness: other questions.
	tzNamedOtherRE = lazyregexp.New(`(?i)\b(?:willing|able|hours?|comfortable|overlap)\b`)
	// tzAlternativesRE parts the named zones.
	tzAlternativesRE = lazyregexp.New(`(?i)\s*,\s*(?:or\s+)?|\s+or\s+`)
	// tzAnyUSRE names the US as the zone ("a US or equivalent timezone"): any
	// US zone is in it. "Or equivalent" beside it widens it.
	tzAnyUSRE      = lazyregexp.New(`(?i)^(?:us|u\.s\.?|usa|united\s+states)$`)
	tzEquivalentRE = lazyregexp.New(`(?i)^(?:an?\s+)?equivalent$`)
)

// namedZones reads the zones a named-zone question asks about: one US zone
// per part, or the US as a whole when the question says "time zone". Any
// other part makes it another question.
func namedZones(m []string) (asked []usTimeZone, anyUS, ok bool) {
	for _, part := range tzAlternativesRE.Split(strings.TrimSpace(m[1]), -1) {
		part = strings.TrimSpace(part)
		switch {
		case tzAnyUSRE.MatchString(part) && m[2] != "":
			anyUS = true
		case tzEquivalentRE.MatchString(part):
		default:
			i := slices.IndexFunc(usTimeZones, func(z usTimeZone) bool { return z.namesZone(part) })
			if i < 0 || slices.IndexFunc(usTimeZones[i+1:], func(z usTimeZone) bool { return z.namesZone(part) }) >= 0 {
				return nil, false, false
			}
			asked = append(asked, usTimeZones[i])
		}
	}
	return asked, anyUS, len(asked) > 0 || anyUS
}

// tzCoasts are the coasts named for a zone (Synthesia: "US East Coast or
// Central timezone"); tzCountryLeadRE is the country written before a zone.
var (
	tzCoasts        = map[string]string{"east coast": "eastern", "west coast": "pacific"}
	tzCountryLeadRE = lazyregexp.New(`^(?:us|u s|usa)\s+`)
)

// namesZone reports that a phrase is this zone's name or abbreviation, or its
// leading word ("Pacific"), or its coast ("East Coast"), with or without "US"
// before it; a city or an offset is not a zone's name.
func (z usTimeZone) namesZone(phrase string) bool {
	p := tzCountryLeadRE.ReplaceAllString(tzNorm(phrase), "")
	if lead, ok := tzCoasts[p]; ok {
		p = lead
	}
	for _, n := range append(append([]string(nil), z.names...), z.abbrevs...) {
		if p == tzNorm(n) {
			return true
		}
	}
	lead, _, _ := strings.Cut(tzNorm(z.names[0]), " ")
	return p == lead
}

// timeZoneNamedStage answers "Are you located in PST?" from the stored time
// zone: Yes when it is a named zone, or the question names the US's zones, No
// when it is another single US zone.
func timeZoneNamedStage(s *Step) (Draft, bool) {
	q := s.Q
	m := tzNamedAskRE.FindStringSubmatch(q.Text)
	if m == nil || tzNamedOtherRE.MatchString(q.Text) || hasNegationToken(q.Text) {
		return Draft{}, false
	}
	if _, ok := ownYesNoExactlyOne(q.Options, true); !ok {
		return Draft{}, false
	}
	if _, ok := ownYesNoExactlyOne(q.Options, false); !ok {
		return Draft{}, false
	}
	asked, anyUS, ok := namedZones(m)
	e := entryNamed(s, "time-zone")
	if !ok || e == nil {
		return Draft{}, false
	}
	if ownText(e) == "" {
		return abstainDraft(ReasonPlaceholderEmpty, "", "time-zone is blank"), true
	}
	var stored []usTimeZone
	for _, z := range usTimeZones {
		if slices.ContainsFunc(e.Preferred, z.identifies) {
			stored = append(stored, z)
		}
	}
	if len(stored) != 1 {
		return abstainDraft(ReasonAmbiguous, "", "the stored time zone does not name one US zone: left for review"), true
	}
	answer, names := "No", make([]string, 0, len(asked)+1)
	if anyUS {
		answer, names = "Yes", append(names, "a US time zone")
	}
	for _, z := range asked {
		if z.std == stored[0].std {
			answer = "Yes"
		}
		names = append(names, z.names[0])
	}
	trace := "time-zone named: the question names " + strings.Join(names, " or ") + ", the stored zone is " + stored[0].names[0] + " -> " + answer
	return finishDerived(bindEntryAnswer(s, e, answer, trace)), true
}

func finishDerived(d Draft) Draft {
	if d.Abstain == nil {
		d.Source, d.Start = SourceDerived, startProfileDerive
	}
	return d
}
