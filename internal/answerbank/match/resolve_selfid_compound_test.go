package match

import "testing"

var rockwellRace = []string{
	"American Indian or Alaska Native (Not Hispanic or Latino) (United States of America)",
	"Asian (Not Hispanic or Latino) (United States of America)",
	"Black or African American (Not Hispanic or Latino) (United States of America)",
	"Hispanic or Latino (United States of America)",
	"Native Hawaiian or Other Pacific Islander (Not Hispanic or Latino) (United States of America)",
	"Not Specified (United States of America)",
	"Two or More Races (Not Hispanic or Latino) (United States of America)",
	"White (Not Hispanic or Latino) (United States of America)",
}

const raceQ = "Please select your race:"

// compoundBank is a bank recording the given race and ethnicity answers: nil
// leaves the entry out, an empty list leaves it present but unanswered.
func compoundBank(race, ethnicity []string) *Matcher {
	var es []Entry
	switch {
	case race == nil:
	case len(race) == 0:
		es = append(es, placeholderEntry("eeo-race"))
	default:
		es = append(es, ent("eeo-race", "pick-option", withPreferred(race...)))
	}
	if ethnicity != nil {
		es = append(es, ent("eeo-ethnicity", "pick-option", withPreferred(ethnicity...)))
	}
	return pickMatcher(es...)
}

func raceListbox(labels ...string) Question {
	q := required(radioQ(raceQ, labels...))
	q.Kind = KindCustomListbox
	return q
}

const notSpecified = "Not Specified (United States of America)"

// The recorded facts alone decide the option: race from the race entry, the
// Hispanic or Latino status from the ethnicity entry.
func TestSelfIDCompound_AnsweredFromBothRecordedAxes(t *testing.T) {
	notHispanic := []string{"Not Hispanic or Latino", "No"}
	for _, c := range []struct {
		name      string
		race      []string
		ethnicity []string
		want      string
	}{
		{"asian not hispanic", []string{"Asian: Chinese", "East Asian", "Asian"}, notHispanic, rockwellRace[1]},
		{"white not hispanic", []string{"White"}, notHispanic, rockwellRace[7]},
		{"black not hispanic", []string{"Black or African American"}, []string{"No"}, rockwellRace[2]},
		{"two or more not hispanic", []string{"Two or More Races"}, notHispanic, rockwellRace[6]},
		{"hispanic takes the bare option whatever the race", []string{"Asian"}, []string{"Hispanic or Latino"}, rockwellRace[3]},
		{"hispanic yes with the race entry unanswered", []string{}, []string{"Yes"}, rockwellRace[3]},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := matchReq(compoundBank(c.race, c.ethnicity), raceListbox(rockwellRace...))
			wantFill(t, r, c.want, OutcomeFilledReview)
		})
	}
}

// A recorded answer that the list cannot express stays a terminal refusal, and
// an axis that is not recorded is never inferred from the other.
func TestSelfIDCompound_UnexpressibleStaysRefused(t *testing.T) {
	asian := []string{"Asian"}
	for _, c := range []struct {
		name      string
		race      []string
		ethnicity []string
	}{
		{"race recorded, ethnicity entry absent", asian, nil},
		{"race recorded, ethnicity unreadable", asian, []string{"Maybe"}},
		{"race recorded, ethnicity contradictory", asian, []string{"Hispanic or Latino", "No"}},
		{"recorded race has no compound option", []string{"Middle Eastern or North African"}, []string{"Not Hispanic or Latino"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := matchReq(compoundBank(c.race, c.ethnicity), raceListbox(rockwellRace...))
			wantAbstain(t, r, ReasonSelfIDPolicy, "")
			if !r.Terminal || r.ChosenLabel != "" {
				t.Fatalf("terminal and nothing chosen: %+v", r)
			}
		})
	}
	t.Run("hispanic recorded but the list has no bare option", func(t *testing.T) {
		r := matchReq(compoundBank(asian, []string{"Hispanic or Latino"}), raceListbox(rockwellRace[:3]...))
		wantAbstain(t, r, ReasonSelfIDPolicy, "")
	})
}

// With nothing recorded the page's own refusal option is taken, as before.
func TestSelfIDCompound_NothingRecordedTakesTheDecline(t *testing.T) {
	m := pickMatcher(placeholderEntry("eeo-race"), placeholderEntry("eeo-ethnicity"))
	r := matchReq(m, raceListbox(rockwellRace...))
	wantFill(t, r, notSpecified, OutcomeFilledReview)
}

func placeholderEntry(id string) Entry {
	e := ent(id, "pick-option")
	e.Preferred, e.Placeholder = nil, true
	return e
}

var gmRace = []string{
	"American Indian or Alaska Native (United States of America)",
	"Asian (United States of America)",
	"Black or African American (United States of America)",
	"Hispanic or Latino (United States of America)",
	"I do not wish to answer. (United States of America)",
	"Native Hawaiian or Other Pacific Islander (United States of America)",
	"Two or More Races (United States of America)",
	"White (United States of America)",
}

// A combined list asks race and ethnicity at once: the races plus one bare
// Hispanic or Latino option (GM's Workday). Both recorded axes answer it.
func TestSelfIDCompound_CombinedRaceAndEthnicityList(t *testing.T) {
	q := required(checkboxQ("What is / are your race(s) / ethnicity(ies)?", gmRace...))
	q.InputType = InputSingleSelect // eight boxes, one answer: how the engine sends the group
	notHispanic := []string{"Not Hispanic or Latino", "No"}
	wantFill(t, matchReq(compoundBank([]string{"Asian: Chinese", "East Asian", "Asian"}, notHispanic), q), gmRace[1], OutcomeFilledReview)
	wantFill(t, matchReq(compoundBank([]string{"Asian"}, []string{"Hispanic or Latino"}), q), gmRace[3], OutcomeFilledReview)
	// An axis that is not recorded is never inferred from the other.
	r := matchReq(compoundBank([]string{"Asian"}, nil), q)
	if r.ChosenLabel == gmRace[1] {
		t.Fatalf("a race was asserted without a recorded ethnicity: %+v", r)
	}
}

// Trend Micro's Workday writes each race with the regions of its EEO-1
// definition; they describe the category the recorded race names.
func TestSelfIDCompound_RegionsDescribeTheRace(t *testing.T) {
	trend := []string{
		"American Indian or Alaska Native (North America, South America, Central America) (United States of America)",
		"Asian (Far East, South East Asia, Indian) (United States of America)",
		"Black or African American (Africa) (United States of America)",
		"Hispanic or Latino (United States of America)",
		"I do not wish to answer (United States of America)",
		"White (Europe, Middle East, North Africa) (United States of America)",
	}
	q := required(raceListbox(trend...))
	q.Text = "Race"
	wantFill(t, matchReq(compoundBank([]string{"Asian: Chinese", "East Asian", "Asian"}, []string{"Not Hispanic or Latino"}), q), trend[1], OutcomeFilledReview)
	if sameIdentityLabel(readIdentity("Asian (Chinese)"), readIdentity("Asian")) {
		t.Fatal("a subgroup is not a region")
	}
}

// Capital One's combined list names the status in one race's own EEO-1 name
// only ("Two or More Races (Not Hispanic or Latino)"): the other races are
// still the races of a non-Hispanic applicant.
var capOneRace = []string{
	"American Indian or Alaska Native (United States of America)",
	"Asian (United States of America)",
	"Black or African American (United States of America)",
	"Hispanic or Latino (United States of America)",
	"I do not wish to disclose (United States of America)",
	"Middle Eastern or North African (United States of America)",
	"Native Hawaiian or Other Pacific Islander (United States of America)",
	"Two or More Races (Not Hispanic or Latino) (United States of America)",
	"White (United States of America)",
}

func TestSelfIDCompound_CombinedListWithOneQualifiedRace(t *testing.T) {
	q := raceListbox(capOneRace...)
	notHispanic := []string{"Not Hispanic or Latino", "No"}
	wantFill(t, matchReq(compoundBank([]string{"Asian: Chinese", "East Asian", "Asian"}, notHispanic), q), capOneRace[1], OutcomeFilledReview)
	wantFill(t, matchReq(compoundBank([]string{"Two or More Races"}, notHispanic), q), capOneRace[7], OutcomeFilledReview)
	wantFill(t, matchReq(compoundBank([]string{"Asian"}, []string{"Hispanic or Latino"}), q), capOneRace[3], OutcomeFilledReview)
	if r := matchReq(compoundBank([]string{"Asian"}, nil), q); r.ChosenLabel == capOneRace[1] {
		t.Fatalf("a race was asserted without a recorded ethnicity: %+v", r)
	}
}

// CVS asks its race list under an "ethnicity" label, every option ending in
// "(United States of America)": the recorded race answers it either way.
func TestSelfID_RaceListBehindASharedCountrySuffix(t *testing.T) {
	cvs := []string{"American Indian and Alaska Native (United States of America)", "Asian (United States of America)",
		"Black or African American (United States of America)", "I do not want to answer (United States of America)",
		"Middle Eastern or North African (United States of America)", "Native Hawaiian and Other Pacific Islander (United States of America)",
		"Two or More Races (United States of America)", "White (United States of America)"}
	notHispanic := []string{"Not Hispanic or Latino", "No"}
	for _, text := range []string{"Please select the ethnicity which most accurately describes how you identify yourself.", "Please select your race."} {
		q := required(selectQ(text, cvs...))
		r := matchReq(compoundBank([]string{"Asian"}, notHispanic), q)
		if r.Outcome == OutcomeAbstained || r.ChosenLabel != cvs[1] {
			t.Fatalf("%q: %+v", text, r)
		}
		if r = matchReq(compoundBank([]string{"White"}, notHispanic), q); r.ChosenLabel != cvs[7] {
			t.Fatalf("%q white: %+v", text, r)
		}
		// No recorded race: nothing is asserted.
		if r = matchReq(compoundBank(nil, notHispanic), q); r.ChosenLabel != "" && r.ChosenLabel != cvs[3] {
			t.Fatalf("%q: a race was asserted without a recorded one: %+v", text, r)
		}
	}
}

// Capgemini (SuccessFactors) asks "Ethnicity" over races beside one bare
// "Hispanic or Latino", and one race joins two neighbouring groups.
var capgeminiEthnicity = []string{"Black or African American", "East or Southeast Asian (e.g. Japanese)", "Hispanic or Latino", "I do not wish to answer",
	"Multiracial / Multiethnic", "Native American or Alaskan Native", "Native Hawaiian or Other Pacific Island", "North African or Middle Eastern", "South Asian (e.g. Indian)", "White"}

func TestSelfIDCompound_AJoinedGroupHoldsTheRecordedRace(t *testing.T) {
	q := required(radioQ("Ethnicity", capgeminiEthnicity...))
	q.Kind = KindCustomListbox
	notHispanic := []string{"Not Hispanic or Latino", "No"}
	wantFill(t, matchReq(compoundBank([]string{"Asian: Chinese", "East Asian", "Asian"}, notHispanic), q), capgeminiEthnicity[1], OutcomeFilled)
	wantFill(t, matchReq(compoundBank([]string{"Southeast Asian"}, notHispanic), q), capgeminiEthnicity[1], OutcomeFilled)
	// "Asian" alone may be either Asian option: it is never guessed.
	if r := matchReq(compoundBank([]string{"Asian"}, notHispanic), q); r.Outcome != OutcomeAbstained {
		t.Fatalf("a bare Asian took %q", r.ChosenLabel)
	}
	// An answer naming both groups never takes an option naming one.
	narrow := required(radioQ("Ethnicity", "East Asian", "Southeast Asian", "Hispanic or Latino", "White", "I do not wish to answer"))
	narrow.Kind = KindCustomListbox
	if r := matchReq(compoundBank([]string{"East or Southeast Asian"}, notHispanic), narrow); r.Outcome != OutcomeAbstained {
		t.Fatalf("a joined answer took %q", r.ChosenLabel)
	}
}

// Sourcegraph's Greenhouse glosses each race "(inclusive of ...)": the gloss
// is no part of the group's name.
func TestSelfIDCompound_AnInclusiveOfGlossNamesTheSameGroup(t *testing.T) {
	sourcegraph := []string{"Black / of African descent", "East Asian (inclusive of Chinese, Japanese, Korean, Mongolian, Tibetan, and Taiwanese)",
		"Middle Eastern", "South Asian (inclusive of Bangladeshi, Bhutanese, Indian, Nepali, Pakistani, and Sri Lankan)",
		"Southeast Asian (inclusive of Burmese, Cambodian, Filipino, Hmong, Indonesian, Laotian, Malaysian, Mien, Singaporean, Thai, and Vietnamese)",
		"White", "I don't wish to answer"}
	q := raceListbox(sourcegraph...)
	notHispanic := []string{"Not Hispanic or Latino", "No"}
	if r := matchReq(compoundBank([]string{"Asian: Chinese", "East Asian", "Asian"}, notHispanic), q); r.Outcome == OutcomeAbstained || r.ChosenLabel != sourcegraph[1] {
		t.Fatalf("%+v", r)
	}
	if r := matchReq(compoundBank([]string{"Asian"}, notHispanic), q); r.Outcome != OutcomeAbstained && r.ChosenLabel != sourcegraph[6] {
		t.Fatalf("a bare Asian took %q", r.ChosenLabel)
	}
}

// Visa's Workday qualifies every race "(Not Latinx or Hispanic)" beside a bare
// "Latinx or Hispanic".
func TestSelfIDCompound_LatinxQualifier(t *testing.T) {
	visa := []string{"American Indian or Alaska Native (Not Latinx or Hispanic) (United States of America)", "Asian – Not Listed (Not Latinx or Hispanic) (United States of America)",
		"East Asian (Not Latinx or Hispanic) (United States of America)", "Latinx or Hispanic (United States of America)", "Prefer Not To Disclose (United States of America)",
		"South Asian (Not Latinx or Hispanic) (United States of America)", "White (Not Latinx or Hispanic) (United States of America)"}
	q := raceListbox(visa...)
	notHispanic := []string{"Not Hispanic or Latino", "No"}
	wantFill(t, matchReq(compoundBank([]string{"East Asian"}, notHispanic), q), visa[2], OutcomeFilledReview)
	wantFill(t, matchReq(compoundBank([]string{"White"}, notHispanic), q), visa[6], OutcomeFilledReview)
	wantFill(t, matchReq(compoundBank([]string{"Asian"}, []string{"Hispanic or Latino"}), q), visa[3], OutcomeFilledReview)
}
