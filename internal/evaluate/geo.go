// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"regexp"
	"slices"
	"strings"
	"sync"

	"applysmith/internal/identity"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/usstates"
)

// country is one entry of the built-in dictionary. Names are folded.
type country struct {
	code, currency string
	names          []string
	cities         []string
}

// countries holds the countries the geography rule can name. A city that
// exists in several countries is never listed.
var countries = []country{
	{"US", "USD", []string{"united states", "united states of america", "usa", "us", "america"}, []string{"new york", "san francisco", "seattle", "austin", "boston", "chicago", "los angeles", "denver", "atlanta", "san diego", "san jose", "palo alto", "mountain view", "menlo park", "sunnyvale", "brooklyn", "houston", "dallas", "miami", "philadelphia", "pittsburgh", "minneapolis", "raleigh", "nashville", "salt lake city", "washington dc", "las vegas", "phoenix", "detroit", "cleveland", "baltimore", "charlotte", "santa clara", "cupertino", "redmond", "bellevue"}},
	{"CA", "CAD", []string{"canada"}, []string{"toronto", "vancouver", "montreal", "calgary", "ottawa", "edmonton", "winnipeg", "waterloo", "mississauga", "halifax"}},
	{"GB", "GBP", []string{"united kingdom", "uk", "great britain", "england", "scotland", "wales", "northern ireland"}, []string{"edinburgh", "glasgow", "leeds", "bristol", "cardiff", "belfast"}},
	{"IE", "EUR", []string{"ireland", "republic of ireland"}, []string{"dublin", "cork", "galway"}},
	{"DE", "EUR", []string{"germany", "deutschland"}, []string{"berlin", "munich", "hamburg", "frankfurt", "stuttgart", "cologne", "dusseldorf"}},
	{"FR", "EUR", []string{"france"}, []string{"lyon", "marseille", "toulouse", "nantes"}},
	{"ES", "EUR", []string{"spain"}, []string{"madrid", "barcelona", "valencia", "seville"}},
	{"IT", "EUR", []string{"italy"}, []string{"milan", "rome", "turin", "naples"}},
	{"NL", "EUR", []string{"netherlands", "the netherlands", "holland"}, []string{"amsterdam", "rotterdam", "eindhoven", "utrecht"}},
	{"SE", "SEK", []string{"sweden"}, []string{"stockholm", "gothenburg", "malmo"}},
	{"PL", "PLN", []string{"poland"}, []string{"warsaw", "krakow", "wroclaw", "gdansk"}},
	{"CH", "CHF", []string{"switzerland"}, []string{"zurich", "geneva", "basel", "lausanne"}},
	{"IN", "INR", []string{"india"}, []string{"bengaluru", "bangalore", "hyderabad", "mumbai", "pune", "gurgaon", "gurugram", "noida", "kolkata", "ahmedabad"}},
	{"CN", "CNY", []string{"china", "prc"}, []string{"shanghai", "beijing", "shenzhen", "guangzhou", "hangzhou"}},
	{"JP", "JPY", []string{"japan"}, []string{"tokyo", "osaka", "yokohama", "kyoto"}},
	{"KR", "KRW", []string{"south korea", "korea"}, []string{"seoul", "busan"}},
	{"SG", "SGD", []string{"singapore"}, nil},
	{"AU", "AUD", []string{"australia"}, []string{"sydney", "melbourne", "brisbane", "perth", "canberra"}},
	{"NZ", "NZD", []string{"new zealand"}, []string{"auckland", "wellington", "christchurch"}},
	{"BR", "BRL", []string{"brazil", "brasil"}, []string{"sao paulo", "rio de janeiro", "curitiba"}},
	{"MX", "MXN", []string{"mexico"}, []string{"mexico city", "guadalajara", "monterrey"}},
	{"AR", "ARS", []string{"argentina"}, []string{"buenos aires"}},
	{"IL", "ILS", []string{"israel"}, []string{"tel aviv", "haifa", "herzliya"}},
	{"AE", "AED", []string{"united arab emirates", "uae"}, []string{"dubai", "abu dhabi"}},
	{"SA", "SAR", []string{"saudi arabia"}, []string{"riyadh", "jeddah"}},
	{"PH", "PHP", []string{"philippines"}, []string{"manila", "cebu"}},
	{"ID", "IDR", []string{"indonesia"}, []string{"jakarta"}},
	{"PK", "PKR", []string{"pakistan"}, []string{"karachi", "lahore", "islamabad"}},
	{"NG", "NGN", []string{"nigeria"}, []string{"lagos"}},
	{"ZA", "ZAR", []string{"south africa"}, []string{"johannesburg", "cape town"}},
	{"EG", "EGP", []string{"egypt"}, []string{"cairo"}},
	{"TR", "TRY", []string{"turkey", "turkiye"}, []string{"istanbul", "ankara"}},
	{"UA", "UAH", []string{"ukraine"}, []string{"kyiv", "kiev"}},
	{"RO", "RON", []string{"romania"}, []string{"bucharest"}},
	{"PT", "EUR", []string{"portugal"}, []string{"lisbon", "porto"}},
	{"DK", "DKK", []string{"denmark"}, []string{"copenhagen"}},
	{"NO", "NOK", []string{"norway"}, []string{"oslo"}},
	{"FI", "EUR", []string{"finland"}, []string{"helsinki"}},
	{"BE", "EUR", []string{"belgium"}, []string{"brussels"}},
	{"AT", "EUR", []string{"austria"}, []string{"vienna"}},
	{"CZ", "CZK", []string{"czech republic", "czechia"}, []string{"prague"}},
	{"VN", "VND", []string{"vietnam"}, []string{"hanoi", "ho chi minh"}},
	{"TH", "THB", []string{"thailand"}, []string{"bangkok"}},
	{"MY", "MYR", []string{"malaysia"}, []string{"kuala lumpur"}},
	{"CO", "COP", []string{"colombia"}, []string{"bogota", "medellin"}},
	{"CL", "CLP", []string{"chile"}, []string{"santiago"}},
}

// usStateCodes are the state codes that signal the US: usstates.Ambiguous are
// country codes too.
var usStateCodes = slices.DeleteFunc(usstates.Codes(), func(c string) bool { return slices.Contains(usstates.Ambiguous, c) })

var caProvinces = []string{"ontario", "quebec", "british columbia", "alberta", "manitoba", "saskatchewan", "nova scotia", "new brunswick", "newfoundland", "prince edward island"}
var caProvinceCodes = strings.Fields("ON QC BC AB MB SK NS NB NL PE YT NT NU")

var usDots = strings.NewReplacer("u.s.a.", "usa", "u.s.", "us")

var multiRegion = lazyregexp.New(`\b(eu|emea|apac|latam|asia pacific|middle east)\b`)
var segmentSplit = lazyregexp.New(`[,;|/()]|\s[-\x{2013}\x{2014}]\s`)

// Region verdicts of Geography.
type geoVerdict struct {
	Pass   bool
	Detail string
}

// Geography judges a location against the allowed countries. A location that
// names any allowed region passes even if it also names a disallowed country;
// otherwise one that names a disallowed country (by name, code or one of its
// listed cities, as "Munich Office" does) or a multi-country region fails; one
// that names nothing recognizable, including a bare "Remote",
// passes (unless remote postings are not allowed).
func Geography(location string, allowed []string, allowRemote bool) geoVerdict {
	if strings.TrimSpace(location) == "" || len(allowed) == 0 {
		return geoVerdict{Pass: true}
	}
	folded := usDots.Replace(identity.Fold(location))
	allowedSet := map[string]bool{}
	for _, c := range allowed {
		allowedSet[strings.ToUpper(strings.TrimSpace(c))] = true
	}
	segments := segmentSplit.Split(location, -1)

	// States and provinces first, so "New Mexico" is not read as Mexico.
	masked := folded
	usSignal, caSignal := false, false
	for _, s := range usstates.Names() {
		if w := wordRE(s); w.MatchString(masked) {
			usSignal = true
			masked = w.ReplaceAllString(masked, " ")
		}
	}
	for _, p := range caProvinces {
		if w := wordRE(p); w.MatchString(masked) {
			caSignal = true
			masked = w.ReplaceAllString(masked, " ")
		}
	}
	codeCountry := map[string]bool{}
	for i, seg := range segments {
		code := strings.TrimSpace(seg)
		if len(code) != 2 || strings.ToUpper(code) != code || (i == 0 && len(segments) > 1) {
			continue
		}
		switch {
		case contains(usStateCodes, code):
			usSignal = true
		case contains(caProvinceCodes, code):
			caSignal = true
		default:
			codeCountry[code] = true
		}
	}
	if usSignal && allowedSet["US"] || caSignal && allowedSet["CA"] {
		return geoVerdict{Pass: true}
	}

	named := map[string]bool{}
	for _, c := range countries {
		for _, n := range c.names {
			if wordRE(n).MatchString(masked) {
				named[c.code] = true
			}
		}
		if codeCountry[c.code] {
			named[c.code] = true
		}
		for _, city := range c.cities {
			if !wordRE(city).MatchString(masked) {
				continue
			}
			if allowedSet[c.code] {
				return geoVerdict{Pass: true}
			}
			named[c.code] = true
		}
	}
	if (usSignal && !allowedSet["US"]) || (caSignal && !allowedSet["CA"]) {
		if usSignal {
			named["US"] = true
		}
		if caSignal {
			named["CA"] = true
		}
	}
	for code := range named {
		if allowedSet[code] {
			return geoVerdict{Pass: true}
		}
	}
	for _, c := range countries {
		if named[c.code] {
			return geoVerdict{Detail: "location names " + c.names[0] + ", which is not an allowed country"}
		}
	}
	if m := multiRegion.FindString(masked); m != "" {
		return geoVerdict{Detail: "location names the multi-country region " + strings.ToUpper(m)}
	}
	if !allowRemote && wordRE("remote").MatchString(masked) {
		return geoVerdict{Detail: "remote postings are not allowed"}
	}
	return geoVerdict{Pass: true}
}

// wordREs keeps the whole-word expression of each dictionary phrase (a
// state, a province, a country name, a city, "remote"), compiled once.
var wordREs sync.Map

func wordRE(phrase string) *lazyregexp.Regexp {
	if re, ok := wordREs.Load(phrase); ok {
		return re.(*lazyregexp.Regexp)
	}
	re, _ := wordREs.LoadOrStore(phrase, lazyregexp.New(`\b`+regexp.QuoteMeta(phrase)+`\b`))
	return re.(*lazyregexp.Regexp)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func currencyOf(code string) string {
	for _, c := range countries {
		if c.code == code {
			return c.currency
		}
	}
	return ""
}
