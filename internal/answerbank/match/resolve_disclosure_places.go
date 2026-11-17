package match

import (
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"applysmith/internal/lazyregexp"
)

// Places a posting location can name. A city maps to the one country it almost
// always means in a job posting; a city that is as likely to be in two countries
// ("Cambridge", "Birmingham", "Athens", "Santiago", "San Jose") is left out, so a
// location that names only it stays unresolved. Subdivision names and
// abbreviations may belong to several countries ("SC", "WA", "NT").

// cityRows is "CC: city, city" per country, accent-folded lower case.
const cityRows = `
US: new york, new york city, nyc, brooklyn, san francisco, san francisco bay, bay area, san diego, los angeles,
 seattle, bellevue, redmond, kirkland, austin, boston, chicago, denver, boulder, atlanta, dallas, houston,
 miami, orlando, tampa, portland, philadelphia, pittsburgh, phoenix, salt lake city, las vegas, minneapolis,
 detroit, raleigh, charlotte, nashville, mountain view, palo alto, sunnyvale, santa clara, menlo park,
 redwood city, cupertino, oakland, irvine, santa monica, columbus, indianapolis, st louis, kansas city,
 baltimore, honolulu, san mateo
CA: toronto, vancouver, montreal, ottawa, calgary, edmonton, waterloo,, mississauga, winnipeg,
 quebec city, halifax
GB: london, manchester, edinburgh, glasgow, bristol, leeds, belfast, cardiff, sheffield, liverpool, nottingham
IE: dublin, cork, galway
FR: paris, lyon, marseille, toulouse, sophia antipolis
DE: berlin, munich, hamburg, frankfurt, cologne, stuttgart, dusseldorf, dresden, leipzig, karlsruhe, nuremberg
NL: amsterdam, rotterdam, the hague, utrecht, eindhoven
ES: madrid, barcelona, malaga, seville
PT: lisbon, porto
IT: milan, rome, turin
CH: zurich, geneva, basel, lausanne
SE: stockholm, gothenburg, malmo
DK: copenhagen
NO: oslo
FI: helsinki
PL: warsaw, krakow, wroclaw, gdansk
CZ: prague, brno
AT: vienna
BE: brussels
HU: budapest
RO: bucharest
UA: kyiv, kiev
EE: tallinn
LT: vilnius
LV: riga
GE: tbilisi, batumi
TR: istanbul, ankara
IL: tel aviv, jerusalem, haifa, herzliya
AE: dubai, abu dhabi
SA: riyadh
EG: cairo
NG: lagos
KE: nairobi
ZA: cape town, johannesburg
IN: bengaluru, bangalore, hyderabad, mumbai, pune,, delhi, new delhi, gurgaon, gurugram, noida, kolkata,
 ahmedabad
PK: karachi, lahore, islamabad
BD: dhaka
JP: tokyo, osaka, kyoto
KR: seoul
CN: beijing, shanghai, shenzhen, guangzhou, hangzhou
TW: taipei
VN: ho chi minh city, hanoi
TH: bangkok
ID: jakarta
MY: kuala lumpur
PH: manila
AU: sydney, melbourne, brisbane, perth, canberra, adelaide
NZ: auckland, wellington
MX: mexico city, guadalajara, monterrey, tijuana
BR: sao paulo, rio de janeiro, brasilia, belo horizonte, curitiba, porto alegre
AR: buenos aires
CO: bogota, medellin
PE: lima
UY: montevideo
`

// subdivisionRows is "CC: name=ABBR, name" per country: the provinces, states
// and territories a posting writes after a city. The United States is read from
// the US state table (isUSState).
const subdivisionRows = `
CA: ontario=ON, quebec=QC, british columbia=BC, alberta=AB, manitoba=MB, saskatchewan=SK, nova scotia=NS,
 new brunswick=NB, newfoundland and labrador=NL, prince edward island=PE, northwest territories=NT,
 nunavut=NU, yukon=YT
AU: new south wales=NSW, victoria=VIC, queensland=QLD, western australia=WA, south australia=SA,
 tasmania=TAS, northern territory=NT, australian capital territory=ACT
IN: karnataka, maharashtra, telangana, tamil nadu, delhi, haryana, uttar pradesh, gujarat, kerala, west bengal,
 rajasthan, andhra pradesh, madhya pradesh, odisha
MX: jalisco, nuevo leon, queretaro, yucatan, puebla, guanajuato, baja california, chihuahua, sonora, coahuila,
 veracruz, quintana roo, cdmx, estado de mexico
BR: sao paulo=SP, rio de janeiro=RJ, minas gerais=MG, parana=PR, bahia=BA, pernambuco=PE, ceara=CE,
 rio grande do sul=RS, santa catarina=SC, goias=GO, espirito santo=ES, distrito federal=DF, para=PA,
 amazonas=AM, mato grosso=MT, mato grosso do sul=MS, maranhao=MA, paraiba=PB, rio grande do norte=RN,
 alagoas=AL, sergipe=SE, piaui=PI, tocantins=TO, rondonia=RO, acre=AC, amapa=AP, roraima=RR
`

// regionRE are the multi-country regions a posting can be open to.
var regionRE = lazyregexp.New(`\b(?:emea|apac|latam|north america|south america|the americas|americas|worldwide|global|anywhere|international|asia|africa|middle east|dach|nordics?|benelux)\b`)

var (
	cityCountry = map[string]string{}
	// subdivisionName is a subdivision's folded name or longer abbreviation;
	// subdivisionAbbr its two-letter abbreviation. Each maps to the countries
	// that use it.
	subdivisionName = map[string][]string{}
	subdivisionAbbr = map[string][]string{}

	// Orb's "HQ-San Francisco" joins its HQ label to the city with a bare hyphen.
	locSplitRE = lazyregexp.New(`\s*[,;/|()\[\]&+]\s*|\s+[-\x{2013}\x{2014}]\s+|\s+(?:or|and)\s+|\b(?:hq|headquarters)\s*[-\x{2013}\x{2014}:]\s*`)
	// A place may name its office ("San Francisco office", Ashby's Plain).
	locAreaRE = lazyregexp.New(`^(?:greater\s+)?(.*?)(?:\s+(?:metropolitan\s+area|metro\s+area|metro|area|office|offices|hq|headquarters|campus))?$`)
	// hyphenLocRE is the "US-CA-San Jose" form: ISO country, subdivision, city.
	hyphenLocRE = lazyregexp.New(`\b([A-Z]{2})-[A-Z0-9]{1,3}-([^,;/|()\[\]]+)`)
)

func init() {
	// A row may wrap onto an indented line that continues the country before it.
	var curCity string
	for _, row := range strings.Split(cityRows, "\n") {
		list := strings.TrimSpace(row)
		if head, rest, ok := strings.Cut(list, ":"); ok && len(head) == 2 {
			curCity, list = head, rest
		}
		for _, c := range strings.Split(list, ",") {
			if c = strings.TrimSpace(c); c != "" {
				cityCountry[c] = curCity
			}
		}
	}
	var cur string
	for _, row := range strings.Split(subdivisionRows, "\n") {
		list := strings.TrimSpace(row)
		if head, rest, ok := strings.Cut(list, ":"); ok && len(head) == 2 {
			cur, list = head, rest
		}
		for _, item := range strings.Split(list, ",") {
			name, abbr, _ := strings.Cut(strings.TrimSpace(item), "=")
			if name = strings.TrimSpace(name); name == "" {
				continue
			}
			subdivisionName[name] = appendUnique(subdivisionName[name], cur)
			if abbr = strings.ToLower(strings.TrimSpace(abbr)); len(abbr) == 2 {
				subdivisionAbbr[abbr] = appendUnique(subdivisionAbbr[abbr], cur)
			} else if abbr != "" {
				subdivisionName[abbr] = appendUnique(subdivisionName[abbr], cur)
			}
		}
	}
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// foldPlace lower-cases a place and removes accents.
func foldPlace(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// placeEvidence is the set of countries one part of a location points at,
// empty when the part says nothing about a country. base is the reading as a US
// state; a part's other readings count only when another part of the location
// points at a country (hasOther) or the part is not a US state at all, so
// "Toronto, CA" is Canada. A two-letter code or a name that is both a US state
// and a country always carries both readings: it is the United States only when
// city says another part is a city of that state, and otherwise another part
// must decide. last allows the part to be a two-letter ISO country code.
func placeEvidence(part string, last, hasOther bool, city cityMatch) []string {
	if code, ok := cityCountry[locAreaRE.FindStringSubmatch(part)[1]]; ok {
		return []string{code}
	}
	if collidingAbbr(part) != "" {
		return collidingEvidence(part, city)
	}
	var base, other []string
	if isUSState(part) {
		base = []string{"US"}
	}
	if len(part) == 2 {
		other = subdivisionAbbr[part]
		if iso := strings.ToUpper(part); last && countryByCode[iso] != "" {
			other = appendUnique(append([]string(nil), other...), iso)
		}
	} else {
		other = subdivisionName[part]
	}
	if len(base) == 1 && !hasOther && len(part) == 2 {
		return base
	}
	out := append([]string(nil), base...)
	for _, c := range other {
		out = appendUnique(out, c)
	}
	return out
}

// locationState is what a posting location says about its country.
type locationState int

const (
	// locationEmpty: the posting gives no location text at all.
	locationEmpty locationState = iota
	// locationUnresolved: there is a location but nothing in it names a country.
	locationUnresolved
	// locationAmbiguous: it names a region, several countries, or evidence that
	// disagrees.
	locationAmbiguous
	// locationResolved: exactly one country.
	locationResolved
)

// placeFillerRE is a location part that names no place: a work arrangement or
// a joining word.
var placeFillerRE = lazyregexp.New(`^(?:(?:or|and|in|within|from|the|remote|hybrid|on-?site|in-?office|office|offices|locations?|based|friendly|first|only)\s*)+$`)

// locationCountries is every country a posting location can mean, read part by
// part: the union of each part's readings, so a place a part could mean in two
// countries counts both. It reads nothing (ok false) for a region, or when a
// part names neither a place nor a country and is no work-arrangement word, so
// the set never leaves out a country the posting may be in.
func locationCountries(loc string) (codes []string, ok bool) {
	if strings.TrimSpace(loc) == "" || regionRE.MatchString(foldPlace(loc)) {
		return nil, false
	}
	_, named := scanCountries(loc, true)
	for _, c := range named {
		if c == regionEU || c == regionEEA || c == regionEurope {
			return nil, false
		}
		codes = appendUnique(codes, c)
	}
	var parts []string
	for _, p := range locSplitRE.Split(foldPlace(loc), -1) {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	for i, p := range parts {
		ev := placeEvidence(p, i == len(parts)-1, true, cityNone)
		_, named := scanCountries(p, true)
		if len(ev) == 0 && len(named) == 0 && !placeFillerRE.MatchString(p) {
			return nil, false
		}
		for _, c := range append(ev, named...) {
			if countryByCode[c] != "" {
				codes = appendUnique(codes, c)
			}
		}
	}
	return codes, len(codes) > 0
}

// LocationCountry is the one country a posting location names; ok is false
// when it names none or several, or its parts disagree.
func LocationCountry(loc string) (country string, ok bool) {
	c, st := resolveLocation(loc)
	return c, st == locationResolved
}

// resolveLocation reads a posting location for exactly one country. Every part
// that names a country (an explicit country, a known city, a subdivision, a
// trailing country code) is a set of candidate countries and the location is the
// intersection of them, so a city and a state of different countries
// ("Tbilisi, Georgia") disagree and are never resolved.
func resolveLocation(loc string) (string, locationState) {
	loc = strings.TrimSpace(loc)
	if loc == "" {
		return "", locationEmpty
	}
	var hyphenCodes []string
	loc = hyphenLocRE.ReplaceAllStringFunc(loc, func(m string) string {
		sub := hyphenLocRE.FindStringSubmatch(m)
		if countryByCode[sub[1]] == "" {
			return m
		}
		hyphenCodes = append(hyphenCodes, sub[1])
		return sub[2]
	})
	_, codes := scanCountries(loc, true)
	codes = dedupe(append(codes, hyphenCodes...))
	for _, c := range codes {
		if c == regionEU || c == regionEEA || c == regionEurope {
			return "", locationAmbiguous
		}
	}
	if regionRE.MatchString(foldPlace(loc)) {
		return "", locationAmbiguous
	}
	var parts []string
	for _, p := range locSplitRE.Split(foldPlace(loc), -1) {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	var evidence [][]string
	for _, c := range codes {
		evidence = append(evidence, []string{c})
	}
	// A part's other readings depend on whether any other part points at a
	// country, so each part is first read on its own.
	alone := make([][]string, len(parts))
	for i, p := range parts {
		alone[i] = placeEvidence(p, i == len(parts)-1, false, cityNone)
	}
	// A town named for a foreign city (Booz Allen's "Rome, NY"): a state or
	// province of one country only, beside it, outvotes the city table's other
	// country, as does a country the location names outright that such a part
	// agrees with. A part of several countries ("Georgia", "WA") backs none.
	var backed []string
	for j, p := range parts {
		if _, city := cityCountry[locAreaRE.FindStringSubmatch(p)[1]]; city || collidingAbbr(p) != "" {
			continue
		}
		for _, c := range codes {
			if slices.Contains(alone[j], c) {
				backed = appendUnique(backed, c)
			}
		}
		if ev := placeEvidence(p, j == len(parts)-1, true, cityNone); len(codes) == 0 && len(ev) == 1 {
			backed = appendUnique(backed, ev[0])
		}
	}
	for i, p := range parts {
		hasOther := len(codes) > 0
		for j := range parts {
			hasOther = hasOther || j != i && len(alone[j]) > 0
		}
		city := cityNone
		if abbr := collidingAbbr(p); abbr != "" {
			city = usCityReading(parts, i, abbr)
		}
		ev := placeEvidence(p, i == len(parts)-1, hasOther, city)
		if _, isCity := cityCountry[locAreaRE.FindStringSubmatch(p)[1]]; isCity && len(backed) == 1 {
			ev = appendUnique(ev, backed[0])
		}
		if len(ev) > 0 {
			evidence = append(evidence, ev)
		}
	}
	if len(evidence) == 0 {
		return "", locationUnresolved
	}
	common := evidence[0]
	for _, ev := range evidence[1:] {
		var next []string
		for _, c := range common {
			for _, d := range ev {
				if c == d {
					next = append(next, c)
				}
			}
		}
		common = next
	}
	if len(common) != 1 {
		return "", locationAmbiguous
	}
	return common[0], locationResolved
}
