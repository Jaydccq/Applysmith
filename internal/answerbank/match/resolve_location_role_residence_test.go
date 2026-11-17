package match

import "testing"

// Nebius (Gravity UI careers page, live): "Do you currently reside in the
// location specified for this role?" for a posting in the "United States" is
// read from the recorded address against the posting's place; a city the
// address does not name, a remote or several-place posting, a place outside
// the United States and an address that does not say are left alone.
func TestAnswerBank_RoleLocationResidence_FromTheAddressAndThePostingsPlace(t *testing.T) {
	m := New(Bank{}, DefaultStages())
	at := func(posting, city, region, country string) func(*Request) {
		return func(r *Request) {
			r.Posting.Location = posting
			r.Facts.Address = AddressFact{City: city, Region: region, Country: country}
		}
	}
	const nebius = "Do you currently reside in the location specified for this role? *"
	cases := []struct {
		name, q string
		mod     func(*Request)
		want    string // "" leaves the question to the entries
	}{
		{"the country holds the address", nebius, at("United States", "Charleston", "SC", "US"), "Yes"},
		{"an address in another country", nebius, at("United States", "Toronto", "ON", "CA"), "No"},
		{"the state holds the address", "Do you live in the area where this role is based?", at("California, United States", "Oakland", "CA", "US"), "Yes"},
		{"another state", nebius, at("California, USA", "Charleston", "SC", "US"), "No"},
		{"the posting's own city", "Are you located in the area where this position is located?", at("Charleston, SC", "Charleston", "South Carolina", "US"), "Yes"},
		{"a city outside its country", nebius, at("Austin, TX", "Toronto", "ON", "CA"), "No"},
		{"a state code that reads as a word", nebius, at("Portland, OR", "Portland", "OR", "US"), "Yes"},
		// The applicant may live in the city's metro.
		{"another city", nebius, at("Austin, TX", "Round Rock", "TX", "US"), ""},
		{"a city in another state", nebius, at("New York, NY", "Jersey City", "NJ", "US"), ""},
		{"a remote posting", nebius, at("Remote - US", "Charleston", "SC", "US"), ""},
		{"several places", nebius, at("New York, NY; San Francisco, CA", "Charleston", "SC", "US"), ""},
		{"a place outside the United States", nebius, at("London, UK", "London", "", "GB"), ""},
		{"no posting place", nebius, at("", "Charleston", "SC", "US"), ""},
		{"no address", nebius, at("United States", "", "", ""), ""},
		{"a willingness", "Do you currently reside in the location specified for this role or are you willing to relocate?", at("United States", "Charleston", "SC", "US"), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := matchReq(m, selectQ(c.q, "Yes", "No"), c.mod)
			derived := r.Source == SourceDerived
			// Answered outright: the review is never asked about it (gates.RoleResidenceAnswer).
			if c.want == "" && derived || c.want != "" && (!derived || r.ChosenLabel != c.want || r.Outcome != OutcomeFilled) {
				t.Fatalf("want %q, got %+v", c.want, r)
			}
		})
	}
}
