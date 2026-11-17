// Package usstates is the one table of US states that location matching reads.
package usstates

// State is a postal code and its folded (lower-case) name.
type State struct{ Code, Name string }

// All is the fifty states and the District of Columbia.
var All = []State{
	{"AL", "alabama"}, {"AK", "alaska"}, {"AZ", "arizona"},
	{"AR", "arkansas"}, {"CA", "california"}, {"CO", "colorado"},
	{"CT", "connecticut"}, {"DE", "delaware"}, {"FL", "florida"},
	{"GA", "georgia"}, {"HI", "hawaii"}, {"ID", "idaho"},
	{"IL", "illinois"}, {"IN", "indiana"}, {"IA", "iowa"},
	{"KS", "kansas"}, {"KY", "kentucky"}, {"LA", "louisiana"},
	{"ME", "maine"}, {"MD", "maryland"}, {"MA", "massachusetts"},
	{"MI", "michigan"}, {"MN", "minnesota"}, {"MS", "mississippi"},
	{"MO", "missouri"}, {"MT", "montana"}, {"NE", "nebraska"},
	{"NV", "nevada"}, {"NH", "new hampshire"}, {"NJ", "new jersey"},
	{"NM", "new mexico"}, {"NY", "new york"}, {"NC", "north carolina"},
	{"ND", "north dakota"}, {"OH", "ohio"}, {"OK", "oklahoma"},
	{"OR", "oregon"}, {"PA", "pennsylvania"}, {"RI", "rhode island"},
	{"SC", "south carolina"}, {"SD", "south dakota"}, {"TN", "tennessee"},
	{"TX", "texas"}, {"UT", "utah"}, {"VT", "vermont"},
	{"VA", "virginia"}, {"WA", "washington"}, {"WV", "west virginia"},
	{"WI", "wisconsin"}, {"WY", "wyoming"}, {"DC", "district of columbia"},
}

// Ambiguous are the state codes that are also country codes (Arkansas and
// Argentina, Delaware and Germany, Idaho and Indonesia, Illinois and Israel,
// Indiana and India): on their own they are not a US signal.
var Ambiguous = []string{"AR", "DE", "ID", "IL", "IN"}

// Names are the folded names that signal the US. Georgia is left out because
// it is also a country.
func Names() []string {
	out := make([]string, 0, len(All))
	for _, s := range All {
		if s.Name != "georgia" {
			out = append(out, s.Name)
		}
	}
	return out
}

// Codes are all the postal codes, Ambiguous ones included.
func Codes() []string {
	out := make([]string, len(All))
	for i, s := range All {
		out[i] = s.Code
	}
	return out
}
