package discovery

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"applysmith/internal/lazyregexp"
	"applysmith/internal/usstates"
)

// The location scopes.
const (
	ScopeUS          = "us"
	ScopeCanada      = "canada"
	ScopeUK          = "uk"
	ScopeEurope      = "europe"
	ScopeAsiaPacific = "asia-pacific"
	ScopeWorldwide   = "worldwide"
)

// Scopes lists the valid scope values.
var Scopes = []string{ScopeUS, ScopeCanada, ScopeUK, ScopeEurope, ScopeAsiaPacific, ScopeWorldwide}

// ValidateScope rejects an unknown scope and names the valid ones.
func ValidateScope(s string) error {
	for _, v := range Scopes {
		if v == s {
			return nil
		}
	}
	return fmt.Errorf("unknown location scope %q; valid scopes are %s", s, strings.Join(Scopes, ", "))
}

// ClassElsewhere is the classification of a place outside all five scopes
// (Latin America, the Middle East, Africa): only worldwide passes it.
const ClassElsewhere = "elsewhere"

// classes is the order Classify reports in: the five scopes, then elsewhere.
var classes = []string{ScopeUS, ScopeCanada, ScopeUK, ScopeEurope, ScopeAsiaPacific, ClassElsewhere}

type place struct {
	phrases []string // whole words, case-insensitive
	exact   []string // whole words, case-sensitive (abbreviations)
	codes   []string // two-letter codes, counted only after a comma; countryScope adds the countries'
	re      *lazyregexp.Regexp
	exactRE *lazyregexp.Regexp
	codeSet map[string]bool
}

var places = map[string]*place{
	ScopeUS: {
		phrases: append([]string{"united states", "united states of america", "america", "washington dc", "san francisco", "seattle", "boston", "chicago", "austin", "los angeles", "san jose", "san diego", "denver", "atlanta", "dallas", "houston", "miami", "portland", "pittsburgh", "philadelphia", "nyc", "bay area", "silicon valley", "palo alto", "mountain view", "menlo park"}, usstates.Names()...),
		exact:   []string{"US", "USA"},
		codes:   usstates.Codes(),
	},
	ScopeCanada: {
		phrases: []string{"canada", "ontario", "quebec", "british columbia", "alberta", "manitoba", "saskatchewan", "nova scotia", "new brunswick", "newfoundland", "toronto", "vancouver", "montreal", "ottawa", "calgary", "edmonton", "waterloo"},
		codes:   strings.Fields("ON BC AB QC MB SK NS NB"),
	},
	ScopeUK: {
		phrases: []string{"united kingdom", "great britain", "england", "scotland", "wales", "northern ireland", "london", "edinburgh", "glasgow"},
		exact:   []string{"UK", "GB"},
	},
	ScopeEurope: {
		phrases: []string{"europe", "european union", "germany", "france", "spain", "italy", "netherlands", "belgium", "ireland", "portugal", "sweden", "norway", "denmark", "finland", "poland", "austria", "switzerland", "czechia", "czech republic", "romania", "hungary", "greece", "estonia", "latvia", "lithuania", "luxembourg", "berlin", "paris", "amsterdam", "dublin", "madrid", "barcelona", "munich", "zurich", "stockholm", "copenhagen", "lisbon", "warsaw", "vienna", "prague", "helsinki", "oslo", "brussels", "milan", "rome",
			"ukraine", "serbia", "croatia", "bulgaria", "slovakia", "slovenia", "cyprus", "iceland", "budapest", "bucharest", "cluj", "krakow", "kraków", "wroclaw", "wrocław", "gdansk", "tallinn", "riga", "vilnius", "kyiv", "lviv", "zagreb", "ljubljana", "bratislava", "brno", "sofia", "frankfurt", "düsseldorf", "dusseldorf", "lyon", "porto", "turin", "eindhoven", "utrecht", "the hague", "antwerp", "ghent", "gothenburg", "malmö", "malmo", "aarhus", "espoo", "tampere", "basel", "lausanne", "cork"},
		exact: []string{"EU", "EMEA"},
	},
	ScopeAsiaPacific: {
		phrases: []string{"asia pacific", "apac", "asia", "india", "singapore", "japan", "australia", "new zealand", "china", "hong kong", "south korea", "korea", "taiwan", "indonesia", "philippines", "vietnam", "thailand", "malaysia", "sydney", "melbourne", "tokyo", "bangalore", "bengaluru", "mumbai", "hyderabad", "pune", "delhi", "seoul", "shanghai", "beijing", "auckland",
			"bangladesh", "pakistan", "sri lanka", "nepal", "macau", "ho chi minh", "hanoi", "da nang", "shenzhen", "guangzhou", "hangzhou", "chengdu", "nanjing", "suzhou", "taipei", "hsinchu", "osaka", "kyoto", "busan", "jakarta", "manila", "cebu", "kuala lumpur", "penang", "bangkok", "chennai", "kolkata", "noida", "gurgaon", "gurugram", "ahmedabad", "kochi", "mohali", "chandigarh", "jaipur", "coimbatore", "thiruvananthapuram", "mysore", "dhaka", "karachi", "lahore", "islamabad", "kathmandu", "brisbane", "adelaide", "canberra", "christchurch"},
	},
	ClassElsewhere: {
		phrases: []string{"latin america", "middle east", "africa", "brazil", "argentina", "chile", "colombia", "uruguay", "costa rica", "mexico city", "guadalajara", "monterrey", "são paulo", "sao paulo", "rio de janeiro", "buenos aires", "santiago", "bogota", "bogotá", "medellin", "medellín", "montevideo", "israel", "tel aviv", "jerusalem", "haifa", "united arab emirates", "dubai", "abu dhabi", "saudi arabia", "riyadh", "qatar", "doha", "egypt", "nigeria", "lagos", "kenya", "nairobi", "south africa", "cape town", "johannesburg", "türkiye", "istanbul", "ankara"},
		exact:   []string{"LATAM", "UAE", "MENA"},
		codes:   strings.Fields("MX BR CL UY AE EG NG KE ZA TR"),
	},
}

var (
	commaCodeRE = lazyregexp.New(`,\s*([A-Z]{2})\b`)
	dottedRE    = lazyregexp.New(`\b(?:[A-Za-z]\.){2,}`)
	boundaryL   = `(?:^|[^\p{L}\p{N}])`
	boundaryR   = `(?:$|[^\p{L}\p{N}])`
)

func init() {
	for _, p := range places {
		// Longest first so "washington dc" wins over "washington".
		sort.Slice(p.phrases, func(i, j int) bool { return len(p.phrases[i]) > len(p.phrases[j]) })
		var alts []string
		for _, ph := range p.phrases {
			alts = append(alts, strings.Join(strings.Fields(regexp.QuoteMeta(ph)), `\s+`))
		}
		p.re = lazyregexp.New(`(?i)` + boundaryL + `(?:` + strings.Join(alts, "|") + `)` + boundaryR)
		if len(p.exact) > 0 {
			p.exactRE = lazyregexp.New(boundaryL + `(?:` + strings.Join(p.exact, "|") + `)` + boundaryR)
		}
		p.codeSet = map[string]bool{}
		for _, c := range p.codes {
			p.codeSet[c] = true
		}
	}
	// A country's code after a comma names its scope, unless a US state or a
	// Canadian province has the same code ("Wilmington, DE" is Delaware).
	for code, scope := range countryScope {
		if scope != ScopeUS && !places[ScopeUS].codeSet[code] && !places[ScopeCanada].codeSet[code] {
			places[scope].codeSet[code] = true
		}
	}
	for _, c := range usstates.Ambiguous {
		delete(places[ScopeUS].codeSet, c)
	}
}

// Classify returns the scopes a location text names, and elsewhere for a place
// outside all five, in a fixed order. An empty result is `unknown`.
func Classify(loc string) []string {
	text := dottedRE.ReplaceAllStringFunc(loc, func(m string) string { return strings.ReplaceAll(m, ".", "") })
	var out []string
	for _, scope := range classes {
		p := places[scope]
		hit := p.re.MatchString(text) || (p.exactRE != nil && p.exactRE.MatchString(text))
		if !hit && len(p.codeSet) > 0 {
			for _, m := range commaCodeRE.FindAllStringSubmatch(text, -1) {
				if p.codeSet[m[1]] {
					hit = true
					break
				}
			}
		}
		if hit {
			out = append(out, scope)
		}
	}
	return out
}

// ScopeResult is the verdict of a scope on one location.
type ScopeResult struct {
	Pass       bool
	Unverified bool // kept because the location is unknown
	InScope    bool // the classification equals the scope (ranking)
}

// ApplyScope passes a posting when its classification equals the scope, when
// it is mixed and includes the scope, or when it is unknown. Worldwide passes
// everything; elsewhere fails every other scope.
func ApplyScope(scope, loc string) ScopeResult {
	if scope == ScopeWorldwide {
		return ScopeResult{Pass: true}
	}
	scopes := Classify(loc)
	if len(scopes) == 0 {
		return ScopeResult{Pass: true, Unverified: true}
	}
	for _, s := range scopes {
		if s == scope {
			return ScopeResult{Pass: true, InScope: len(scopes) == 1}
		}
	}
	return ScopeResult{}
}

var countryScope = map[string]string{
	"US": ScopeUS, "CA": ScopeCanada, "GB": ScopeUK,
	"DE": ScopeEurope, "FR": ScopeEurope, "ES": ScopeEurope, "IT": ScopeEurope, "NL": ScopeEurope, "BE": ScopeEurope, "IE": ScopeEurope,
	"PT": ScopeEurope, "SE": ScopeEurope, "NO": ScopeEurope, "DK": ScopeEurope, "FI": ScopeEurope, "PL": ScopeEurope, "AT": ScopeEurope,
	"CH": ScopeEurope, "CZ": ScopeEurope, "RO": ScopeEurope, "HU": ScopeEurope, "GR": ScopeEurope, "EE": ScopeEurope, "LV": ScopeEurope,
	"LT": ScopeEurope, "LU": ScopeEurope,
	"IN": ScopeAsiaPacific, "SG": ScopeAsiaPacific, "JP": ScopeAsiaPacific, "AU": ScopeAsiaPacific, "NZ": ScopeAsiaPacific, "CN": ScopeAsiaPacific,
	"HK": ScopeAsiaPacific, "KR": ScopeAsiaPacific, "TW": ScopeAsiaPacific, "ID": ScopeAsiaPacific, "PH": ScopeAsiaPacific, "VN": ScopeAsiaPacific,
	"TH": ScopeAsiaPacific, "MY": ScopeAsiaPacific,
}

// ScopeFromCountries derives a scope from the allowed countries when exactly
// one scope matches all of them, else worldwide.
func ScopeFromCountries(codes []string) string {
	found := ""
	for _, c := range codes {
		s, ok := countryScope[strings.ToUpper(strings.TrimSpace(c))]
		if !ok || (found != "" && found != s) {
			return ScopeWorldwide
		}
		found = s
	}
	if found == "" {
		return ScopeWorldwide
	}
	return found
}
