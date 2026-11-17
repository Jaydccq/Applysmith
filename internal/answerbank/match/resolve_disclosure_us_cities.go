package match

import (
	"regexp"
	"slices"
	"sort"
	"strings"

	"applysmith/internal/lazyregexp"
)

// A US state that is also a country, by two-letter code or by name, is evidence
// of two countries at once. A two-letter code that is both a US state abbreviation and an ISO 3166
// country code ("IN" Indiana or India, "CO" Colorado or Colombia, "MA"
// Massachusetts or Morocco) is evidence of two countries at once. It resolves
// only when another part of the location decides: a US city of that state, or a
// part that names a country. Computed from the two tables, never listed by hand.
var collidingCodes = func() map[string]bool {
	out := map[string]bool{}
	for _, s := range usStateRows {
		if countryByCode[s.Abbr] != "" {
			out[strings.ToLower(s.Abbr)] = true
		}
	}
	return out
}()

// collidingNames maps a US state name that is also a country name ("Georgia")
// to its state abbreviation and the country's code. Computed from the two
// tables, never listed by hand.
var collidingNames = func() map[string]stateCountry {
	out := map[string]stateCountry{}
	for _, s := range usStateRows {
		for code, name := range countryByCode {
			if strings.EqualFold(name, s.Name) {
				out[strings.ToLower(s.Name)] = stateCountry{strings.ToLower(s.Abbr), code}
			}
		}
	}
	return out
}()

type stateCountry struct{ abbr, code string }

var (
	// placeCodeRE is a place that ends in a two-letter code ("Norton, MA").
	placeCodeRE = lazyregexp.New(`,\s*([A-Z]{2})\b`)
	// usNamedRE names the United States.
	usNamedRE = lazyregexp.New(`\bUnited\s+States\b|\bU\.S\.(?:A\.)?|\bUSA\b`)
	// cityCodeRE is up to four words and a two-letter code after them with no
	// comma ("San Francisco CA"), up to the next separator or the end.
	cityCodeRE = lazyregexp.New(`((?:[A-Za-z.'-]+[ \t]+){0,3}?[A-Za-z.'-]+)[ \t]+([A-Z]{2})([ \t]*(?:[,;/|]|$))`)
)

// commaBeforeStateCode puts a comma between a known US city and the code of
// its state written after it without one (Verana Health on Greenhouse: "New
// York NY, San Francisco CA"), so the code reads as the state, as it does in
// "San Francisco, CA". The city is the longest run of the last words before
// the code that the cities table holds for that state; any other code is left
// as it was.
func commaBeforeStateCode(loc string) string {
	return cityCodeRE.ReplaceAllStringFunc(loc, func(m string) string {
		sub := cityCodeRE.FindStringSubmatch(m)
		words, code := strings.Fields(sub[1]), sub[2]
		for k := range words {
			if slices.Contains(usCityStates[strings.ToLower(strings.Join(words[k:], " "))], strings.ToLower(code)) {
				return sub[1] + ", " + code + sub[3]
			}
		}
		return m
	})
}

// settleCollidingStates writes each colliding two-letter code of a location
// as its state's name when the posting's own text names that state or the
// United States and never the other country (Alnylam: "Norton, MA" with a
// description of the site in Norton, Massachusetts), so the place reads as
// the state. Without a text, or with one that names the other country, the
// code is left as it is and stays both.
func settleCollidingStates(loc, text string) string {
	if strings.TrimSpace(text) == "" {
		return loc
	}
	return placeCodeRE.ReplaceAllStringFunc(loc, func(m string) string {
		code := placeCodeRE.FindStringSubmatch(m)[1]
		if !collidingCodes[strings.ToLower(code)] {
			return m
		}
		i := slices.IndexFunc(usStateRows, func(r stateRow) bool { return r.Abbr == code })
		if i < 0 || containsWord(text, countryByCode[code]) || !containsWord(text, usStateRows[i].Name) && !usNamedRE.MatchString(text) {
			return m
		}
		return strings.Replace(m, code, usStateRows[i].Name, 1)
	})
}

// cityMatch is how another part of a location relates to a colliding state.
type cityMatch int

const (
	cityNone cityMatch = iota
	// cityOfState: a known US city of that state.
	cityOfState
	// cityOfOtherState: a known US city, but of other states only.
	cityOfOtherState
)

// collidingAbbr is the lower-case state abbreviation of a part that is both a
// US state and a country, or "" for any other part.
func collidingAbbr(part string) string {
	if collidingCodes[part] {
		return part
	}
	return collidingNames[part].abbr
}

// collidingEvidence is the countries a colliding part points at. Only a city of
// the state settles it as the United States; a city of another state ("Boston,
// AL") leaves the state reading unsettled, so it cannot agree with the city. A
// state name carries its country reading as a distinct "?" value that no
// explicit country matches, so a foreign city cannot settle a bare name either
// ("Tbilisi, Georgia").
func collidingEvidence(part string, city cityMatch) []string {
	foreign := []string{collidingNames[part].code + "?"}
	if collidingCodes[part] {
		foreign = []string{strings.ToUpper(part)}
		for _, c := range subdivisionAbbr[part] {
			foreign = appendUnique(foreign, c)
		}
	}
	switch city {
	case cityOfState:
		return []string{"US"}
	case cityOfOtherState:
		return append([]string{"US?"}, foreign...)
	}
	return append([]string{"US"}, foreign...)
}

// usCityRows is "ST: city, city" per state, accent-folded lower case: the
// largest US cities and the major tech hubs. A city that exists in several
// states is listed under each; one listed under a state is a US city when the
// location names that state.
const usCityRows = `
AL: birmingham, montgomery, mobile, huntsville, tuscaloosa, auburn
AK: anchorage, fairbanks, juneau
AZ: phoenix, tucson, mesa, chandler, scottsdale, gilbert, tempe, glendale, peoria, flagstaff, goodyear, surprise, avondale, buckeye, yuma
AR: little rock, fayetteville, fort smith, springdale, bentonville, jonesboro, conway
CA: los angeles, san diego, san jose, san francisco, fresno, sacramento, long beach, oakland, bakersfield,
 anaheim, santa ana, riverside, stockton, irvine, chula vista, fremont, san bernardino, modesto, fontana,
 oxnard, huntington beach, glendale, santa clarita, garden grove, oceanside, rancho cucamonga, santa rosa,
 elk grove, corona, lancaster, palmdale, salinas, pomona, hayward, escondido, torrance, sunnyvale, pasadena,
 orange, fullerton, thousand oaks, visalia, roseville, concord, simi valley, santa clara, berkeley,
 mountain view, palo alto, menlo park, redwood city, cupertino, san mateo, santa monica, santa barbara,
 san luis obispo, milpitas, burbank, el segundo, culver city, carlsbad, davis, pleasanton, foster city,
 south san francisco, san ramon, san carlos, burlingame, los gatos, campbell, los altos, santa cruz,
 newport beach, costa mesa, playa vista, emeryville, brisbane
CO: denver, colorado springs, aurora, fort collins, lakewood, thornton, arvada, westminster, boulder,
 centennial, longmont, broomfield, louisville, golden, englewood, littleton
CT: bridgeport, new haven, stamford, hartford, waterbury, norwalk, danbury, greenwich
DE: wilmington, dover, newark
DC: washington
FL: jacksonville, miami, tampa, orlando, st petersburg, hialeah, tallahassee, fort lauderdale, cape coral,
 hollywood, gainesville, clearwater, west palm beach, boca raton, pensacola, sarasota, naples, lakeland
GA: atlanta, columbus, augusta, macon, savannah, athens, sandy springs, roswell, alpharetta, marietta,
 johns creek, duluth, norcross, peachtree city
HI: honolulu, hilo, kailua
ID: boise, meridian, nampa, idaho falls, eagle
IL: chicago, aurora, joliet, naperville, rockford, springfield, peoria, elgin, champaign, evanston,
 schaumburg, oak brook, urbana, bloomington, deerfield
IN: indianapolis, fort wayne, evansville, south bend, carmel, fishers, bloomington, hammond, gary,
 lafayette, west lafayette, muncie, columbus
IA: des moines, cedar rapids, davenport, sioux city, iowa city, ames, waterloo
KS: wichita, overland park, kansas city, topeka, olathe, lawrence
KY: louisville, lexington, bowling green
LA: new orleans, baton rouge, shreveport, lafayette, lake charles, metairie
ME: portland, lewiston, bangor, augusta
MD: baltimore, columbia, germantown, silver spring, rockville, frederick, annapolis, bethesda, gaithersburg,
 towson, hunt valley
MA: boston, worcester, springfield, cambridge, lowell, brockton, quincy, lynn, new bedford, waltham,
 burlington, somerville, woburn, framingham, andover, watertown, marlborough, natick, bedford, lexington
MI: detroit, grand rapids, warren, sterling heights, ann arbor, lansing, flint, dearborn, troy, southfield,
 kalamazoo, auburn hills
MN: minneapolis, st paul, rochester, duluth, bloomington, brooklyn park, plymouth, eden prairie
MS: jackson, gulfport, southaven, biloxi, hattiesburg, tupelo
MO: kansas city, st louis, springfield, columbia, independence, lees summit, st charles, jefferson city
MT: billings, missoula, great falls, bozeman, helena
NE: omaha, lincoln, bellevue, grand island
NV: las vegas, henderson, reno, north las vegas, carson city
NH: manchester, nashua, concord, portsmouth
NJ: newark, jersey city, paterson, elizabeth, edison, princeton, hoboken, trenton, cherry hill, parsippany,
 morristown, red bank
NM: albuquerque, las cruces, rio rancho, santa fe
NY: new york, new york city, buffalo, rochester, yonkers, syracuse, albany, white plains, brooklyn, queens,
 long island city, ithaca, armonk, tarrytown
NC: charlotte, raleigh, greensboro, durham, winston-salem, fayetteville, cary, wilmington, high point,
 asheville, chapel hill, morrisville, research triangle park
ND: fargo, bismarck, grand forks
OH: columbus, cleveland, cincinnati, toledo, akron, dayton, dublin, canton, mason
OK: oklahoma city, tulsa, norman, broken arrow, edmond
OR: portland, eugene, salem, gresham, hillsboro, beaverton, bend
PA: philadelphia, pittsburgh, allentown, erie, reading, scranton, harrisburg, lancaster, king of prussia,
 malvern, state college, bethlehem, exton, wayne
RI: providence, warwick, cranston
SC: charleston, columbia, north charleston, mount pleasant, greenville, spartanburg, rock hill
SD: sioux falls, rapid city
TN: nashville, memphis, knoxville, chattanooga, clarksville, murfreesboro, franklin, brentwood
TX: houston, san antonio, dallas, austin, fort worth, el paso, arlington, corpus christi, plano, laredo,
 lubbock, garland, irving, amarillo, mckinney, frisco, brownsville, round rock, richardson, college station,
 the woodlands, waco, denton
UT: salt lake city, west valley city, provo, west jordan, orem, sandy, ogden, lehi, draper, park city
VT: burlington, south burlington, montpelier
VA: virginia beach, norfolk, chesapeake, richmond, newport news, alexandria, hampton, roanoke, reston,
 mclean, arlington, herndon, fairfax, tysons, charlottesville, ashburn
WA: seattle, spokane, tacoma, vancouver, bellevue, kent, everett, renton, redmond, kirkland, bothell,
 olympia, bellingham, issaquah
WV: charleston, huntington, morgantown
WI: milwaukee, madison, green bay, kenosha, appleton
WY: cheyenne, casper, laramie
`

// usCityStates maps a folded city to the lower-case abbreviations of the
// states that have it.
var usCityStates = func() map[string][]string {
	out := map[string][]string{}
	var cur string
	for _, row := range strings.Split(usCityRows, "\n") {
		list := strings.TrimSpace(row)
		if head, rest, ok := strings.Cut(list, ":"); ok && len(head) == 2 {
			cur, list = strings.ToLower(head), rest
		}
		for _, c := range strings.Split(list, ",") {
			if c = strings.TrimSpace(c); c != "" {
				out[c] = appendUnique(out[c], cur)
			}
		}
	}
	return out
}()

// cityNamedRE is a US place whose name ends in "City" (Redwood City, Salt Lake
// City), from the cities table, or New York City.
var cityNamedRE = func() *lazyregexp.Regexp {
	names := []string{"new york city"}
	for c := range usCityStates {
		if strings.HasSuffix(c, " city") {
			names = append(names, regexp.QuoteMeta(c))
		}
	}
	sort.Slice(names, func(i, j int) bool {
		return len(names[i]) > len(names[j]) || len(names[i]) == len(names[j]) && names[i] < names[j]
	})
	return lazyregexp.New(`(?i)\b(?:` + strings.Join(names, "|") + `)\b`)
}()

var cityWordRE = lazyregexp.New(`(?i)\bcity\b`)

// onlyCityNames reports a text whose every "city" is part of a place's name.
func onlyCityNames(text string) bool {
	return cityNamedRE.MatchString(text) && !cityWordRE.MatchString(cityNamedRE.ReplaceAllString(text, " "))
}

// usCityReading reads the other parts of a location for a US city relative to
// the state abbr that parts[i] names. The st/saint spellings of a city and a
// trailing "area" are read as the city.
func usCityReading(parts []string, i int, abbr string) cityMatch {
	match := cityNone
	for j, p := range parts {
		if j == i {
			continue
		}
		states := usCityStates[strings.ReplaceAll(locAreaRE.FindStringSubmatch(p)[1], "st. ", "st ")]
		if slices.Contains(states, abbr) {
			return cityOfState
		}
		if len(states) > 0 {
			match = cityOfOtherState
		}
	}
	return match
}
