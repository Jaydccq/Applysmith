package signals

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// marker is one precise location or currency clue and the country it names.
type marker struct {
	re      *lazyregexp.Regexp
	country string
}

func m(country string, words ...string) marker {
	return marker{re: lazyregexp.New(`(?i)(?:^|[^\p{L}])(?:` + strings.Join(words, "|") + `)(?:[^\p{L}]|$)`), country: country}
}

// markers are the precise clues: country names, major cities and currencies.
// A bare "Remote" is not one.
var markers = []marker{
	m("US", `united states`, `usa`, `u\.s\.`, `new york`, `nyc`, `san francisco`, `bay area`, `seattle`, `boston`, `austin`, `chicago`,
		`los angeles`, `denver`, `atlanta`, `california`, `texas`, `washington dc`, `usd`, `\$`),
	m("CA", `canada`, `toronto`, `vancouver`, `montreal`, `ottawa`, `cad`),
	m("GB", `united kingdom`, `\buk\b`, `london`, `manchester`, `edinburgh`, `gbp`, "£"),
	m("DE", `germany`, `berlin`, `munich`, `hamburg`),
	m("FR", `france`, `paris`, `lyon`),
	m("NL", `netherlands`, `amsterdam`, `rotterdam`),
	m("ES", `spain`, `madrid`, `barcelona`),
	m("IE", `ireland`, `dublin`),
	m("SE", `sweden`, `stockholm`),
	m("CH", `switzerland`, `zurich`, `chf`),
	m("IN", `india`, `bangalore`, `bengaluru`, `mumbai`, `delhi`, `hyderabad`, `inr`),
	m("SG", `singapore`, `sgd`),
	m("AU", `australia`, `sydney`, `melbourne`, `aud`),
	m("JP", `japan`, `tokyo`, `jpy`),
	m("CN", `china`, `beijing`, `shanghai`, `shenzhen`, `cny`),
	m("IL", `israel`, `tel aviv`, `ils`),
	m("BR", `brazil`, `sao paulo`, `brl`),
	m("MX", `mexico`, `mxn`),
	m("PL", `poland`, `warsaw`, `krakow`, `pln`),
	m("PT", `portugal`, `lisbon`),
	m("EU", "€", `eur`, `europe`, `emea`),
}

// Geo decides whether a free-text post is inside the regions of the user.
type Geo struct {
	countries map[string]bool
	literals  []string
	on        bool
}

// NewGeo builds the filter from targets.locations and the authorised
// countries. With neither set the filter is off.
func NewGeo(locations, countries []string) *Geo {
	g := &Geo{countries: map[string]bool{}}
	for _, c := range countries {
		if c = strings.ToUpper(strings.TrimSpace(c)); c != "" {
			g.countries[c] = true
			g.on = true
		}
	}
	for _, l := range locations {
		l = strings.ToLower(strings.TrimSpace(l))
		if l == "" {
			continue
		}
		g.on = true
		g.literals = append(g.literals, l)
		for _, mk := range markers {
			if mk.re.MatchString(l) {
				g.countries[mk.country] = true
			}
		}
	}
	if g.countries["GB"] || g.countries["DE"] || g.countries["FR"] || g.countries["NL"] || g.countries["ES"] || g.countries["IE"] {
		g.countries["EU"] = true
	}
	return g
}

// Keep reports whether a post survives: it is dropped only when its header
// carries a precise marker outside the regions and none inside them.
func (g *Geo) Keep(header string) bool {
	if g == nil || !g.on {
		return true
	}
	low := strings.ToLower(header)
	for _, l := range g.literals {
		if strings.Contains(low, l) {
			return true
		}
	}
	outside := false
	for _, mk := range markers {
		if !mk.re.MatchString(header) {
			continue
		}
		if g.countries[mk.country] {
			return true
		}
		outside = true
	}
	return !outside
}
