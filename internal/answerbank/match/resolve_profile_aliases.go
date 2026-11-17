package match

import "strings"

// Alias tables for exception_terms. Listing any one variant of a company or a
// metro matches every other variant, so the user types the name they know.
// Both expanders return the input itself for an unknown term, so a term that
// is neither a company nor a city still matches its own literal. The lookup
// runs only on an entry that carries exception_terms, which is what keeps a
// short form such as a two-letter city code from firing on unrelated text.

// aliasRowsCompany lists each company with its legal-entity names, brands and
// short forms; the first element is the name a person would type.
var aliasRowsCompany = [][]string{
	{"Google", "Alphabet", "YouTube", "DeepMind", "Waymo", "Google LLC"},
	{"Meta", "Facebook", "Instagram", "WhatsApp", "Meta Platforms", "Oculus"},
	{"Amazon", "AWS", "Amazon Web Services", "Amazon.com", "Twitch", "Whole Foods"},
	{"Microsoft", "LinkedIn", "GitHub", "Xbox", "Microsoft Corporation"},
	{"Apple", "Apple Inc"},
	{"Netflix"},
	{"Tesla", "Tesla Motors"},
	{"Nvidia", "NVIDIA Corporation"},
	{"Oracle", "Oracle Corporation"},
	{"Salesforce", "Slack", "Tableau", "Salesforce.com"},
	{"IBM", "International Business Machines", "Red Hat"},
	{"Intel", "Intel Corporation"},
	{"Adobe", "Adobe Systems"},
	{"Cisco", "Cisco Systems"},
	{"Qualcomm"},
	{"SAP"},
	{"Uber", "Uber Technologies"},
	{"Lyft"},
	{"Airbnb"},
	{"Stripe"},
	{"Palantir", "Palantir Technologies"},
	{"Snowflake"},
	{"ByteDance", "TikTok"},
	{"JPMorgan", "JPMorgan Chase", "JP Morgan", "Chase", "JPMC"},
	{"Goldman Sachs", "Goldman"},
	{"Morgan Stanley"},
	{"Bank of America", "BofA", "Merrill", "Merrill Lynch"},
	{"Citigroup", "Citi", "Citibank"},
	{"Wells Fargo"},
	{"Capital One"},
	{"BlackRock"},
	{"Visa", "Visa Inc"},
	{"Mastercard"},
	{"American Express", "Amex"},
	{"PayPal"},
	{"Deloitte"},
	{"PwC", "PricewaterhouseCoopers"},
	{"EY", "Ernst and Young", "Ernst & Young"},
	{"KPMG"},
	{"McKinsey", "McKinsey and Company", "McKinsey & Company"},
	{"Boston Consulting Group", "BCG"},
	{"Bain", "Bain and Company", "Bain & Company"},
	{"Accenture"},
	{"Disney", "Walt Disney", "ESPN", "Hulu"},
	{"Comcast", "NBCUniversal", "NBC"},
	{"AT&T", "ATT"},
	{"Verizon"},
	{"Walmart"},
	{"Boeing"},
	{"Lockheed Martin", "Lockheed"},
	{"Raytheon", "RTX", "Raytheon Technologies"},
	{"Northrop Grumman", "Northrop"},
	{"General Electric", "General Electric Company"},
	{"Johnson & Johnson", "Johnson and Johnson"},
	{"Pfizer"},
	{"UnitedHealth", "UnitedHealth Group", "Optum"},
}

// aliasRowsCity lists US metros with their short forms and neighbours.
var aliasRowsCity = [][]string{
	{"New York", "NYC", "New York City", "NY", "Manhattan"},
	{"San Francisco", "SF", "Bay Area", "San Francisco Bay Area"},
	{"Los Angeles", "LA"},
	{"Washington", "DC", "Washington DC", "Washington D C"},
	{"Chicago", "Chicago IL"},
	{"Boston", "Boston MA", "Cambridge", "Cambridge MA"},
	{"Seattle", "Seattle WA", "Bellevue", "Bellevue WA"},
	{"Austin", "Austin TX", "ATX"},
	{"Denver", "Denver CO"},
	{"Atlanta", "Atlanta GA", "ATL"},
	{"Miami", "Miami FL"},
	{"Houston", "Houston TX"},
	{"Dallas", "Dallas TX", "DFW", "Dallas Fort Worth"},
	{"Philadelphia", "Philly", "Philadelphia PA"},
	{"Portland", "Portland OR", "PDX"},
	{"San Diego", "San Diego CA"},
	{"San Jose", "San Jose CA", "Silicon Valley", "South Bay", "Santa Clara", "Santa Clara CA"},
	{"Phoenix", "Phoenix AZ"},
	{"Minneapolis", "Minneapolis MN", "Twin Cities"},
	{"Detroit", "Detroit MI"},
	{"Pittsburgh", "Pittsburgh PA"},
	{"St Louis", "Saint Louis", "St Louis MO", "STL"},
	{"Salt Lake City", "SLC", "Salt Lake"},
	{"Las Vegas", "Vegas", "Las Vegas NV"},
	{"New Orleans", "NOLA", "New Orleans LA"},
	{"Nashville", "Nashville TN"},
	{"Charlotte", "Charlotte NC"},
	{"Raleigh", "Raleigh NC", "RDU", "Research Triangle"},
	{"Tampa", "Tampa FL"},
	{"Orlando", "Orlando FL"},
	{"Jersey City", "JC", "Jersey City NJ"},
	{"Brooklyn", "Brooklyn NY"},
}

// aliasKey is the case- and punctuation-insensitive lookup key of a name.
func aliasKey(s string) string { return strings.Join(tokenize(normalizeLabel(s)), " ") }

// aliasIndexOf maps every variant's key to the full variant set of its row;
// the first row to claim a key keeps it.
func aliasIndexOf(rows [][]string) map[string][]string {
	idx := map[string][]string{}
	for _, row := range rows {
		for _, v := range row {
			if k := aliasKey(v); k != "" {
				if _, taken := idx[k]; !taken {
					idx[k] = row
				}
			}
		}
	}
	return idx
}

var (
	aliasCompanyIndex = aliasIndexOf(aliasRowsCompany)
	aliasCityIndex    = aliasIndexOf(aliasRowsCity)
)

// expandCompanyAliases returns every variant of the company, or the input
// alone when it is not in the table.
func expandCompanyAliases(in string) []string { return aliasExpand(aliasCompanyIndex, in) }

// expandCityAliases returns every variant of the metro, or the input alone.
func expandCityAliases(in string) []string { return aliasExpand(aliasCityIndex, in) }

func aliasExpand(idx map[string][]string, in string) []string {
	if row, ok := idx[aliasKey(in)]; ok {
		return row
	}
	return []string{in}
}

// exceptionHit is the user term that matched and the variant it matched as.
type exceptionHit struct{ Term, As string }

// matchExceptionTerm finds the first user term whose variant occurs in the
// question as a contiguous run of whole tokens. City and company expansions
// are unioned: the two domains do not overlap, and an unknown term expands to
// itself.
func matchExceptionTerm(question string, terms []string) (exceptionHit, bool) {
	qt := tokenize(normalizeLabel(question))
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		seen := map[string]bool{}
		for _, v := range append(expandCityAliases(term), expandCompanyAliases(term)...) {
			k := aliasKey(v)
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			if containsAllTokens(qt, strings.Fields(k)) {
				return exceptionHit{Term: term, As: v}, true
			}
		}
	}
	return exceptionHit{}, false
}
