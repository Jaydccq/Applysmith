package match

import (
	"reflect"
	"testing"
)

// The persona Alex Example lives in Sampleton, Exampleshire (ES); the shipped
// metro and state tables supply the other places.
func alexPolicy(def string, places, excluded []string) *RelocationPolicy {
	return &RelocationPolicy{Default: def, Places: places, Excluded: excluded,
		Home: Home{City: "Sampleton", County: "Sample County", Region: "ES", Country: "XX"}}
}

func TestAnswerBank_Location_FormBoundaries(t *testing.T) {
	for _, c := range []struct {
		text, form string
		want       bool
	}{
		{"Washington D.C.", "Washington D.C.", true},
		{"Based in Washington D.C. area", "Washington D.C.", true},
		{"relocate or work remotely", "OR", false}, // a short form keeps its case
		{"relocate to OR", "OR", true},
		{"relocate to Oregon today", "Oregon", true},
		{"relocate to oregon", "Oregon", true}, // long forms are case-insensitive
		{"Portlandia", "Portland", false},
		{"Portland", "Portland", true},
		{"(Austin)", "Austin", true},
		{"Austin_TX", "Austin", false},
		{"in", "in", true},
		{"within", "in", false},
	} {
		if got := containsForm(c.text, c.form); got != c.want {
			t.Errorf("containsForm(%q, %q) = %t, want %t", c.text, c.form, got, c.want)
		}
	}
}

func TestAnswerBank_Location_WillingToWorkAt(t *testing.T) {
	closed := alexPolicy(RelocationClosed, []string{"Denver"}, nil)
	for _, c := range []struct {
		text string
		job  *jobPlace
		want verdict
	}{
		{"Are you willing to relocate to Denver or Boston?", nil, verdictYes},                 // any named place
		{"Are you willing to relocate to Boston?", nil, verdictNo},                            // all refused
		{"Are you willing to work in Sampleton?", nil, verdictYes},                            // home counts as willing
		{"Are you willing to relocate?", &jobPlace{Remote: true}, verdictYes},                 // remote
		{"Are you willing to relocate?", &jobPlace{City: "Boston", Region: "MA"}, verdictNo},  // the posting's place
		{"Are you willing to relocate?", &jobPlace{City: "Denver", Region: "CO"}, verdictYes}, // listed posting
		{"Are you willing to relocate to Denver?", &jobPlace{City: "Boston"}, verdictYes},     // the question wins
	} {
		if got := willingToWorkAt(closed, c.text, c.job); got != c.want {
			t.Errorf("%q job=%+v: %s, want %s", c.text, c.job, got, c.want)
		}
	}
	if willingToWorkAt(nil, "Are you willing to relocate?", nil) != verdictUnknown {
		t.Fatal("an unconfigured policy is unknown")
	}
}

func TestAnswerBank_Location_PostingGranularity(t *testing.T) {
	p := alexPolicy(RelocationOpen, nil, nil)
	for _, c := range []struct {
		job  jobPlace
		want verdict
	}{
		{jobPlace{City: "Othertown", Region: "ES"}, verdictUnknown}, // a different city in the home state is not home
		{jobPlace{Region: "ES"}, verdictYes},                        // region-only posting in the home state is home
		{jobPlace{Country: "XX"}, verdictUnknown},                   // the country alone never counts
		{jobPlace{City: "Sampleton", Region: "ES"}, verdictYes},
	} {
		j := c.job
		if got := currentlyAt(p, "Are you near the role?", &j); got != c.want {
			t.Errorf("%+v: %s, want %s", c.job, got, c.want)
		}
	}
}

func TestAnswerBank_Location_RequiresRelocationComposition(t *testing.T) {
	closed := alexPolicy(RelocationClosed, []string{"Example City"}, nil)
	q := "Will you require relocation?"
	for _, c := range []struct {
		name string
		p    *RelocationPolicy
		job  *jobPlace
		want verdict
	}{
		{"remote posting", closed, &jobPlace{Remote: true}, verdictNo},
		{"posting in the home metro", closed, &jobPlace{City: "Sampleton", Region: "ES"}, verdictNo},
		{"listed posting city", closed, &jobPlace{City: "Example City", Region: "EC"}, verdictYes},
		{"a different city of the home state under closed", closed, &jobPlace{City: "Othertown", Region: "ES"}, verdictNo},
		{"unlisted posting under open", alexPolicy(RelocationOpen, nil, nil), &jobPlace{City: "Othertown", Region: "ES"}, verdictYes},
		{"no posting, open", alexPolicy(RelocationOpen, nil, nil), nil, verdictYes},
		{"no posting, closed with nothing listed", alexPolicy(RelocationClosed, nil, nil), nil, verdictNo},
		{"unconfigured", nil, &jobPlace{City: "Boston"}, verdictUnknown},
	} {
		if got := requiresRelocation(c.p, q, c.job); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	// The question names its own place: an unrelated posting does not decide presence.
	if got := requiresRelocation(alexPolicy(RelocationOpen, nil, nil), "Will you require relocation to Sampleton?", &jobPlace{City: "Boston"}); got != verdictNo {
		t.Errorf("a named home place decides presence whatever the posting says: %s", got)
	}
}

func TestAnswerBank_Location_RankHomeVersusListed(t *testing.T) {
	// An unlisted home ranks before every listed place; a listed home ranks by its list position.
	p := alexPolicy(RelocationClosed, []string{"Denver", "Sampleton"}, nil)
	ranked := rankRenderedPlaces(p, []string{"Sampleton, ES", "Denver, CO"})
	if ranked[0].Label != "Denver, CO" {
		t.Fatalf("a listed home at position 1 ranks below a place at position 0: %+v", ranked)
	}
	p = alexPolicy(RelocationClosed, []string{"Denver"}, nil)
	if got := rankRenderedPlaces(p, []string{"Denver, CO", "Sampleton, ES"}); got[0].Label != "Sampleton, ES" || got[0].Reason != "home" {
		t.Fatalf("an unlisted home ranks first: %+v", got)
	}
}

// A relocation list entry that is a bare code a metro and another state share
// ("LA") is the place the rest of its list makes it; a list that does not
// settle it leaves the question that turns on it to the user.
func TestAnswerBank_Location_ASharedCodeIsReadByItsList(t *testing.T) {
	for _, c := range []struct {
		list, want, open []string
	}{
		{[]string{"SF", "LA", "NYC"}, []string{"SF", "Los Angeles", "NYC"}, nil},
		{[]string{"Austin, TX", "LA"}, []string{"Austin, TX", "Los Angeles"}, nil},
		{[]string{"Texas", "LA", "OH"}, []string{"Texas", "Louisiana", "OH"}, nil},
		{[]string{"LA"}, []string{"LA"}, []string{"Los Angeles", "Louisiana"}},
		{[]string{"LA", "Texas", "Austin"}, []string{"LA", "Texas", "Austin"}, []string{"Los Angeles", "Louisiana"}},
		{[]string{"LA", "New York"}, []string{"LA", "New York"}, []string{"Los Angeles", "Louisiana"}},
		{[]string{"Los Angeles", "Louisiana"}, []string{"Los Angeles", "Louisiana"}, nil},
	} {
		got, open := settleSharedCodes(c.list)
		if !reflect.DeepEqual(got, c.want) || !reflect.DeepEqual(open, c.open) {
			t.Errorf("settleSharedCodes(%v) = %v, open %v; want %v, open %v", c.list, got, open, c.want, c.open)
		}
	}
	cities := alexPolicy(RelocationClosed, []string{"SF", "LA"}, nil)
	if got := willingToWorkAt(cities, "Are you willing to relocate to Louisiana?", nil); got != verdictNo {
		t.Errorf("Louisiana under a list of cities: %s", got)
	}
	if r := rankRenderedPlaces(cities, []string{"Los Angeles, CA"})[0]; !r.Acceptable || r.Rank != 1 {
		t.Errorf("Los Angeles under a list of cities: %+v", r)
	}
	states := alexPolicy(RelocationClosed, []string{"Texas", "LA"}, nil)
	if r := rankRenderedPlaces(states, []string{"Los Angeles, CA"})[0]; r.Acceptable {
		t.Errorf("Los Angeles under a list of states: %+v", r)
	}
	if got := willingToWorkAt(states, "Are you willing to relocate to Louisiana?", nil); got != verdictYes {
		t.Errorf("Louisiana under a list of states: %s", got)
	}
	if r := rankRenderedPlaces(alexPolicy(RelocationOpen, nil, []string{"Ohio", "LA"}), []string{"Los Angeles, CA"})[0]; !r.Acceptable {
		t.Errorf("an excluded Louisiana refused Los Angeles: %+v", r)
	}

	open := alexPolicy(RelocationClosed, []string{"LA"}, nil)
	m := pickMatcher(relocEntry(open))
	for _, text := range []string{"Are you willing to relocate to Los Angeles?", "Are you willing to relocate to Louisiana?", "Are you willing to relocate to LA?"} {
		wantAbstain(t, matchReq(m, radioQ(text, "Yes", "No")), ReasonPlaceholderEmpty, "")
	}
	wantAbstain(t, matchReq(m, radioQ("Are you willing to relocate?", "Yes", "No"), withPosting("Los Angeles, CA")), ReasonPlaceholderEmpty, "")
	wantFill(t, matchReq(m, radioQ("Are you willing to relocate to Chicago?", "Yes", "No")), "No", OutcomeFilled)
	lane := heldOnSharedCode(func(*Step) (Draft, bool) { return derivedDraft("Yes", "", startProfileDerive, ""), true })
	step := &Step{Req: &Request{}, Q: Question{Text: "Can you commute to our Los Angeles office?"}, Entries: []Entry{relocEntry(open)}}
	if d, ok := lane(step); !ok || d.Abstain == nil || d.Abstain.Reason != ReasonPlaceholderEmpty {
		t.Errorf("a lane without a bound entry over an open code: %+v", d)
	}
	step.Q.Text = "Can you commute to our Chicago office?"
	if d, ok := lane(step); !ok || d.Abstain != nil {
		t.Errorf("a lane without a bound entry over another place: %+v", d)
	}
	settled := pickMatcher(relocEntry(cities))
	wantFill(t, matchReq(settled, radioQ("Are you willing to relocate to Los Angeles?", "Yes", "No")), "Yes", OutcomeFilled)
	wantFill(t, matchReq(settled, radioQ("Are you willing to relocate to Louisiana?", "Yes", "No")), "No", OutcomeFilled)
}

func TestAnswerBank_Location_TimeZoneExpansion(t *testing.T) {
	got := expandTimeZoneAliases([]string{"Eastern Time Zone"})
	if got[0] != "Eastern Time Zone" || !containsString(got, "America/New_York") || !containsString(got, "EST") || !containsString(got, "UTC-05:00") {
		t.Fatalf("the user's value first, then every spelling: %v", got)
	}
	// Names win over offsets: the binder walks the list in order.
	if indexOfString(got, "America/New_York") > indexOfString(got, "UTC-05:00") {
		t.Fatalf("names precede offsets: %v", got)
	}
	// An abbreviation matches as a whole word: "daylight" does not carry the Hawaii "HT".
	for _, v := range expandTimeZoneAliases([]string{"Eastern Daylight Time"}) {
		if v == "Pacific/Honolulu" {
			t.Fatal("Eastern Daylight Time must not pull the Hawaii zone in")
		}
	}
	if got := expandTimeZoneAliases([]string{"EST"}); !containsString(got, "Eastern Time") {
		t.Fatalf("an abbreviation identifies its zone: %v", got)
	}
	if got := expandTimeZoneAliases([]string{"UTC-5"}); !reflect.DeepEqual(got, []string{"UTC-5"}) {
		t.Fatalf("an offset never identifies a zone: %v", got)
	}
	if got := expandTimeZoneAliases([]string{"Somewhere Standard"}); !reflect.DeepEqual(got, []string{"Somewhere Standard"}) {
		t.Fatalf("an unknown value is a literal: %v", got)
	}
}

func containsString(list []string, s string) bool { return indexOfString(list, s) >= 0 }
func indexOfString(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}
