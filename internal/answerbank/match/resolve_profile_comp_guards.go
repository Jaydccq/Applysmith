package match

import (
	"slices"
	"strings"

	"applysmith/internal/lazyregexp"
)

// currencyGuard blocks a compensation entry whose stored currency does not
// match the denomination the question asks for. A named currency, or the words
// "local currency" with the posting's country (the recorded address's when no
// country places the posting), is context: it is never
// permission to relabel or convert a stored amount. Example amounts in two or
// more currencies ("i.e. $50000, £50000") show the format and name none of
// them, so they only hold the entry to the currency of a posting whose country
// is known.
func currencyGuard(s *Step, e *Entry) (Abstain, string, bool) {
	if e == nil || e.Kind != KindCompensationRange || len(e.CapturedScope) > 0 {
		return Abstain{}, "", false
	}
	text, examples := compWithoutExampleAmounts(s.Q.Text)
	have := strings.ToUpper(strings.TrimSpace(e.Currency))
	named := compNamedCurrencies(text)
	place := compCurrencyOfPlace(s.Req.Posting.Location)
	switch {
	case len(named) > 0:
		if slices.Contains(named, have) {
			return Abstain{}, "", false
		}
	case compLocalCurrencyRE.MatchString(text):
		if have != "" && (have == place || place == "" && have == homeCurrency(s)) {
			return Abstain{}, "", false
		}
	case examples:
		if place == "" || place == have {
			return Abstain{}, "", false
		}
	default:
		return Abstain{}, "", false
	}
	return Abstain{Reason: ReasonContextPolicy, Detail: DetailLocalCurrency},
		"compensation jurisdiction: the question asks for a denomination the entry's currency does not match", true
}

// homeCurrency is the currency of the recorded address's country: "your local
// currency" on a posting no country places (Camunda's "Remote") is the
// applicant's own.
func homeCurrency(s *Step) string {
	p := partOf(s, "address-country")
	if p.askMe {
		return ""
	}
	code, _ := CountryCode(p.text)
	return compCountryCurrency[code]
}

// compCodes are the currency codes a question is read for; compSign is a
// currency sign.
const (
	compCodes = `USD|CAD|EUR|GBP|AUD|NZD|CHF|JPY|CNY|INR|SEK|NOK|DKK|PLN|CZK|HUF|RON|BGN|ISK|TRY|RUB|UAH|BRL|MXN|ARS|CLP|COP|PEN|SGD|HKD|KRW|TWD|THB|IDR|MYR|PHP|VND|PKR|BDT|LKR|ZAR|NGN|KES|EGP|MAD|AED|SAR|QAR|ILS`
	compSign  = `[$\x{a3}\x{20ac}\x{20b9}\x{a5}\x{20a9}\x{20aa}]`
)

var (
	compLocalCurrencyRE = lazyregexp.New(`(?i)\blocal\s+currenc(?:y|ies)\b`)
	compCodeRE          = lazyregexp.New(`\b(?:` + compCodes + `)\b`)
	// compMarkedAmountRE is a figure written with its currency: a sign or a code
	// before it ("$50000", "USD 50,000") or after it ("50.000 €", "50000 EUR").
	// The mark is group 1 or group 2.
	compMarkedAmountRE = lazyregexp.New(`(` + compSign + `|\b(?:` + compCodes + `)\b)\s?[0-9][0-9,.]*` +
		`|[0-9][0-9,.]*\s?(` + compSign + `|\b(?:` + compCodes + `)\b)`)
)

// compWithoutExampleAmounts drops the figures the question writes with their
// currency when they show two or more currencies, and reports that it did:
// they are examples of the format, not the one denomination asked for.
// Amounts in a single currency stay, so "(e.g. £50000)" still names pounds.
func compWithoutExampleAmounts(text string) (string, bool) {
	marks := map[string]bool{}
	for _, m := range compMarkedAmountRE.FindAllStringSubmatch(text, -1) {
		marks[m[1]+m[2]] = true
	}
	if len(marks) < 2 {
		return text, false
	}
	return compMarkedAmountRE.ReplaceAllString(text, " "), true
}

// compSymbolCurrencies are the unambiguous currency signs; "$" and a bare yen
// sign name more than one currency, so they are read as every candidate.
var compSymbolCurrencies = []struct {
	sign       string
	currencies []string
}{
	{"\u20ac", []string{"EUR"}}, {"\u00a3", []string{"GBP"}}, {"\u20b9", []string{"INR"}},
	{"\u00a5", []string{"JPY", "CNY"}}, {"\u20a9", []string{"KRW"}}, {"\u20aa", []string{"ILS"}},
}

// compWordCurrencies are currencies a question names in words ("in Euros"). A
// bare "dollar", "peso" or "pound" names more than one and is not read.
var compWordCurrencies = []struct {
	currency string
	re       *lazyregexp.Regexp
}{
	{"EUR", lazyregexp.New(`(?i)\beuros?\b`)},
	{"GBP", lazyregexp.New(`(?i)\b(?:pounds?\s+sterling|british\s+pounds?|sterling)\b`)},
	{"INR", lazyregexp.New(`(?i)\b(?:indian\s+)?rupees?\b`)},
	{"JPY", lazyregexp.New(`(?i)\bjapanese\s+yen\b`)},
	{"CNY", lazyregexp.New(`(?i)\b(?:renminbi|yuan|rmb)\b`)},
	{"CHF", lazyregexp.New(`(?i)\bswiss\s+francs?\b`)},
	{"CAD", lazyregexp.New(`(?i)\bcanadian\s+dollars?\b`)},
	{"AUD", lazyregexp.New(`(?i)\baustralian\s+dollars?\b`)},
	{"USD", lazyregexp.New(`(?i)\b(?:us|u\.s\.|american)\s+dollars?\b`)},
	{"PLN", lazyregexp.New(`(?i)\b(?:polish\s+)?zlot(?:y|ys|ych)\b`)},
	{"SGD", lazyregexp.New(`(?i)\bsingapore\s+dollars?\b`)},
}

// compNamedCurrencies lists the distinct currency codes the question names, in
// order. A code is read only in capitals so the word "chf" or "inr" inside
// prose is not a currency.
func compNamedCurrencies(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range compCodeRE.FindAllString(text, -1) {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	for _, w := range compWordCurrencies {
		if w.re.MatchString(text) && !seen[w.currency] {
			seen[w.currency] = true
			out = append(out, w.currency)
		}
	}
	for _, sym := range compSymbolCurrencies {
		if strings.Contains(text, sym.sign) {
			for _, c := range sym.currencies {
				if !seen[c] {
					seen[c] = true
					out = append(out, c)
				}
			}
		}
	}
	return out
}

// compCountryCurrency maps a posting's country to the currency in use there.
var compCountryCurrency = map[string]string{
	"US": "USD", "CA": "CAD", "GB": "GBP", "AU": "AUD", "NZ": "NZD", "CH": "CHF", "JP": "JPY", "CN": "CNY", "IN": "INR",
	"SE": "SEK", "NO": "NOK", "DK": "DKK", "PL": "PLN", "BR": "BRL", "MX": "MXN", "SG": "SGD", "HK": "HKD",
	"KR": "KRW", "ZA": "ZAR", "AE": "AED", "IL": "ILS",
	"DE": "EUR", "FR": "EUR", "IT": "EUR", "ES": "EUR", "NL": "EUR", "IE": "EUR", "BE": "EUR", "AT": "EUR", "FI": "EUR",
	"PT": "EUR", "GR": "EUR", "LU": "EUR",
}

// compCurrencyOfPlace is the currency of a posting location's country, or ""
// when it is unknown. The country is the one the whole location resolves to,
// read as the work-authorization guard reads it (resolveLocation): a country
// named anywhere in it ("Canada - Remote", "Remote (US)"), or the one country
// its city or state is in. A location whose parts point at different countries
// resolves to none, so a town named like a country ("Greece, NY") is never read
// as that country; its last part still decides when it writes out one country
// and nothing else ("Manchester, NH, United States", whose city is also a city
// of the United Kingdom).
func compCurrencyOfPlace(location string) string {
	code, st := resolveLocation(location)
	if st != locationResolved {
		parts := strings.Split(location, ",")
		rest, codes := scanCountries(parts[len(parts)-1], true)
		if len(codes) != 1 || strings.TrimSpace(rest) != "" {
			return ""
		}
		code = codes[0]
	}
	return compCountryCurrency[code]
}

// heldCompensationGuard refuses to answer a question about pay the applicant
// already holds with a target figure. An entry that states the held fact
// itself still answers.
func heldCompensationGuard(s *Step, e *Entry) (Abstain, string, bool) {
	if e == nil || !asksHeldCompensation(s.Q.Text) {
		return Abstain{}, "", false
	}
	target := e.Kind == KindCompensationRange || (e.Kind == KindFixed && compTargetAnswerRE.MatchString(e.Answer))
	if !target {
		return Abstain{}, "", false
	}
	return Abstain{Reason: ReasonContextPolicy, Detail: DetailHeldCompensation},
		"held compensation: entry \"" + e.ID + "\" states a target compensation, not compensation the applicant holds", true
}

var compTargetAnswerRE = lazyregexp.New(`(?i)\b(?:target|expected|desired|requested)\s+(?:(?:total|annual|base|cash)\s+){0,3}(?:compensation|comp|salary|pay)\b|\b(?:salary|compensation|pay)\s+expectations?\b`)

var compHeldPatterns = []*lazyregexp.Regexp{
	lazyregexp.New(`(?i)\bdeferred\s+(?:compensation|comp|bonus(?:es)?|pay)\b`),
	lazyregexp.New(`(?i)\bunvested\b`),
	lazyregexp.New(`(?i)\bforms?\s+of\s+(?:compensation|equity|incentives?)\b[^.?!]{0,60}\b(?:currently|presently)\b`),
	lazyregexp.New(`(?i)\b(?:currently|presently)\s+(?:hold|have|own|receive)\b[^.?!]{0,60}\b(?:equity|rsus?|restricted\s+stock|stock\s+options?|long\W?term\s+incentive)`),
	lazyregexp.New(`(?i)\b(?:equity|rsus?|restricted\s+stock(?:\s+units?)?|stock\s+options?|long\W?term\s+incentive\s+plans?)\b[^.?!]{0,60}\byou\s+(?:currently|presently)\s+(?:hold|have|own|receive)\b`),
}

// compCurrentPayRE finds the pay the applicant earns today. The pay noun must
// not be followed by "expectation", "requirement" or "range": that is the
// target question. RE2 has no lookahead, so the caller reads the following word.
var compCurrentPayRE = lazyregexp.New(`(?i)\b(?:current|present)\s+(?:(?:monthly|annual|yearly|hourly|base|basic|net|gross|total)\s+|\([^)]{0,40}\)\s+){0,4}(?:salary|pay|compensation|remuneration|earnings|wages?)\b`)

var compTargetFollowRE = lazyregexp.New(`(?i)^\s+(?:expectations?|requirements?|range)`)

// asksHeldCompensation reports a question about deferred or unvested pay,
// equity the applicant holds, or the pay they receive now.
func asksHeldCompensation(question string) bool {
	for _, re := range compHeldPatterns {
		if re.MatchString(question) {
			return true
		}
	}
	for _, loc := range compCurrentPayRE.FindAllStringIndex(question, -1) {
		if !compTargetFollowRE.MatchString(question[loc[1]:]) {
			return true
		}
	}
	return false
}
