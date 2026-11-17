// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import "testing"

func TestRequiresUSPerson(t *testing.T) {
	for _, c := range []struct {
		text string
		want bool
	}{
		{"You must be a U.S. person due to export controls.", true},
		{"Role requires US person status.", true},
		{"US person status is required.", true},
		{"We are an equal opportunity employer. US persons and non-US persons may apply.", false},
		{"US person status is not required.", false},
		{"Export controls may apply to some roles.", false},
	} {
		if got := RequiresUSPerson(c.text); got != c.want {
			t.Errorf("RequiresUSPerson(%q) = %v", c.text, got)
		}
	}
}

func TestSponsorship(t *testing.T) {
	for _, c := range []struct {
		name, text string
		stance     Stance
		note       string
	}{
		{"restricted citizen", "Applicants must be a U.S. citizen.", StanceRestricted, ""},
		{"restricted requires citizenship", "This position requires US citizenship.", StanceRestricted, ""},
		{"restricted green card", "Open to citizens or green card holders only.", StanceRestricted, ""},
		{"restricted us person", "Must be a US person.", StanceRestricted, ""},
		{"citizenship not required", "US citizenship is not required.", StanceSilent, ""},
		{"refuses", "We do not sponsor work visas.", StanceRefuses, ""},
		{"refuses unable", "Unfortunately we are unable to sponsor H-1B.", StanceRefuses, ""},
		{"refuses no", "No visa sponsorship available.", StanceRefuses, ""},
		{"refuses unavailable", "Sponsorship is not available for this role.", StanceRefuses, ""},
		{"qualified refusal voided", "We do not sponsor TN status but do sponsor H-1B visas.", StanceOffers, ""},
		{"offers", "We sponsor H-1B visas for qualified candidates.", StanceOffers, ""},
		{"offers available", "Visa sponsorship is available.", StanceOffers, ""},
		{"sponsor a meetup", "We will sponsor a meetup for the team.", StanceSilent, ""},
		{"mixed", "We do not sponsor visas. Another team will sponsor H-1B visas.", StanceSilent, NoteMixed},
		{"restricted beats refuses", "US citizens only. We cannot sponsor visas.", StanceRestricted, ""},
		{"silent", "Build things with us.", StanceSilent, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, note := Sponsorship(c.text)
			if got != c.stance || note != c.note {
				t.Fatalf("Sponsorship(%q) = %s %q, want %s %q", c.text, got, note, c.stance, c.note)
			}
		})
	}
}

func TestParseCompensation(t *testing.T) {
	for _, c := range []struct {
		name, text string
		lo, hi     float64
		ok         bool
	}{
		{"benefits beside a range", "$90,000 - $110,000 base + 401k match up to 4%", 90000, 110000, true},
		{"one figure and a retirement plan", "$90,000/year plus 401k", 0, 0, false},
		{"monthly", "$7,000 - $9,000/month", 84000, 108000, true},
		{"monthly with annual unit", "$100,000 - $120,000 annually, paid monthly", 100000, 120000, true},
		{"single annual figure", "$120,000 annually, paid monthly", 0, 0, false},
		{"hourly", "$50 - $60 per hour", 104000, 124800, true},
		{"thousands", "$90 - $110", 90000, 110000, true},
		{"k suffix", "$90k-$110k", 90000, 110000, true},
		{"non dollar", "£60,000 - £80,000", 0, 0, false},
		{"code", "CAD 90,000 - 110,000 or $90,000 - $110,000", 0, 0, false},
		{"US and Canadian ranges", "US employees (any location): $166,600 \u2014 $208,300 USD Canadian employees (any location): $157,400 \u2014 $196,800 CAD", 166600, 208300, true},
		{"Canadian range marked by code only", "$120,000 - $140,000 CAD", 0, 0, false},
		{"US$ prefix beside a code", "Base pay US$100,000 - 120,000; Canada CAD 130,000 - 150,000", 100000, 120000, true},
		{"between with codes beside a Canadian range", "USA-based roles only: The annual base salary for this role is between $230,000 USD and $334,000 USD, plus benefits. Canada-based roles only: between $202,000 CAD and $293,000 CAD.", 230000, 334000, true},
		{"code between figures", "$150,000 USD - $180,000 USD per year", 150000, 180000, true},
		{"bare and joins nothing", "A $5,000 signing bonus and $10,000 relocation", 0, 0, false},
		{"placeholder", "$1-$1/hr", 0, 0, false},
		{"placeholder range", "United States Salary Range: $1\u2014$2 USD", 0, 0, false},
		{"placeholder before the real range", "Salary Range: $1-$2 USD. Updated: $140,000 - $160,000 USD", 140000, 160000, true},
		{"hourly at the federal minimum", "$7.25 - $8 per hour", 15080, 16640, true},
		{"percent", "$5 - $10%", 0, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, ok := ParseCompensation(c.text)
			if ok != c.ok || r.Low != c.lo || r.High != c.hi || (ok && r.Currency != "USD") {
				t.Fatalf("ParseCompensation(%q) = %+v %v", c.text, r, ok)
			}
		})
	}
}

func TestClearlySenior(t *testing.T) {
	for _, c := range []struct {
		title, label string
		want         bool
	}{
		{"Senior Software Engineer", "", true},
		{"Staff Engineer", "", true},
		{"Engineering Manager", "", true},
		{"Senior Engineer Intern", "", false},
		{"New Grad Software Engineer", "senior", false},
		{"Software Engineer", "director", true},
		{"Software Engineer", "mid", false},
		{"Software Engineer II", "", false},
		// Member of Technical Staff is a title family at every level; a level
		// beside it still reads.
		{"Member of Technical Staff, Inference", "", false},
		{"Member of the Technical Staff - Applied AI", "", false},
		{"Senior Member of Technical Staff", "", true},
	} {
		if got := ClearlySenior(c.title, c.label); got != c.want {
			t.Errorf("ClearlySenior(%q, %q) = %v", c.title, c.label, got)
		}
	}
}

func TestRequiredYears(t *testing.T) {
	for _, c := range []struct {
		text string
		want int
		ok   bool
	}{
		{"5+ years of experience in Go.", 5, true},
		{"3-5 years experience with databases. 2 years experience with Go.", 3, true},
		{"7 years of experience is a plus.", 0, false},
		{"We have 10 years of history.", 0, false},
		{"Despite 30+ years of combined AI experience on the founding team, we keep learning.", 0, false},
		{"Must be at least 18 years of age with 2 years of retail experience.", 2, true},
		{"", 0, false},
	} {
		got, ok := RequiredYears(c.text)
		if got != c.want || ok != c.ok {
			t.Errorf("RequiredYears(%q) = %d %v", c.text, got, ok)
		}
	}
}

func TestYearsThreshold_ReadsAFloorOfYearsOfExperience(t *testing.T) {
	cases := []struct {
		label  string
		floor  int
		strict bool
		ok     bool
	}{
		{"Do you have a minimum of 3+ years of experience coding in Python?", 3, false, true},
		{"Do you have at least 10 years of professional development experience?", 10, false, true},
		{"Do you have more than 2 years of experience with React?", 2, true, true},
		{"Do you have 5 years or more of experience in Go?", 5, false, true},
		{"Do you have 3 years of experience with Python?", 0, false, false},
		{"Do you have 3-5 years of experience?", 0, false, false},
		{"Do you have less than 2 years of experience?", 0, false, false},
		{"Are you at least 18 years of age?", 0, false, false},
		{"Do you have 3+ years of experience or a master's degree?", 0, false, false},
		{"Do you have 2+ years of experience with Python and 1+ years with Go?", 0, false, false},
		{"Have you used Python with at least 2 years of experience in the past 3 years?", 0, false, false},
		// H Company (Ashby, live): the figure spelled out and no word experience, only what the years were spent doing.
		{"Do you have more than three years deploying solutions at customers ?", 3, true, true},
		{"Do you have at least five years managing engineers?", 5, false, true},
		{"Have you lived here for more than three years?", 0, false, false},
		{"Do you have more than two years living in the United States?", 0, false, false},
		{"Do you have someone who can vouch for you?", 0, false, false},
	}
	for _, c := range cases {
		floor, strict, _, ok := YearsThreshold(c.label)
		if ok != c.ok || floor != c.floor || strict != c.strict {
			t.Errorf("YearsThreshold(%q) = %d, %v, %v; want %d, %v, %v", c.label, floor, strict, ok, c.floor, c.strict, c.ok)
		}
	}
	// New Relic (Greenhouse, live): a bare "or" between fields is a choice of
	// subjects; a degree, a credential or an equivalent is still no threshold.
	for label, want := range map[string]bool{
		"Do you have 2+ years of professional experience with Go or Python programming": true,
		"Do you have 3+ years of experience or a master's degree?":                      false,
		"Do you have 2+ years of experience with Go or equivalent?":                     false,
		"Do you have 3-5 years of experience in Go or Python?":                          false,
	} {
		if _, _, _, ok := YearsThresholdAnyOf(label); ok != want {
			t.Errorf("YearsThresholdAnyOf(%q) = %v, want %v", label, ok, want)
		}
	}
	if _, _, _, ok := YearsThreshold("Do you have 2+ years of professional experience with Go or Python programming"); ok {
		t.Error("a choice of fields is not a plain threshold")
	}
	q := "Do you have a minimum of 3+ years of experience coding in Python?"
	rows := SelectReviewRows([]ReviewRow{
		{Label: q, Value: "No", WrittenByRun: true, FromRecord: true},
		{Label: q + " ", Value: "Yes", WrittenByRun: true},
		{Label: "How many years of experience do you have with Python?", Value: "1", WrittenByRun: true, FromRecord: true},
	})
	if len(rows) != 2 || rows[0].Value != "Yes" || rows[1].Value != "1" {
		t.Fatalf("the review receives a years threshold's Yes or No written from the record, or misses a model's: %+v", rows)
	}
}
