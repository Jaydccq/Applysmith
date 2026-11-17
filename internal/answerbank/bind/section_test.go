package bind

import (
	"context"
	"reflect"
	"testing"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/engine"
)

// IN-083: the section path a control name states.
func TestAnswerBank_SectionPath_ControlNameDerivation(t *testing.T) {
	cases := []struct {
		name, selector string
		want           []string
	}{
		// An entity word and an underscore row number.
		{"employer row", `[id="$$employerState_1_RTiCandidate"]`, []string{"Employer 1"}},
		{"education row", `#$$educationCity_1_RTiCandidate`, []string{"Education 1"}},
		{"education row two", `[id="$$educationState_2_RTiCandidate"]`, []string{"Education 2"}},
		{"experience row", `[name="experienceTitle_4_x"]`, []string{"Experience 4"}},
		{"the last group is the row", `[id="$$employerAddress1_2_RTiCandidate"]`, []string{"Employer 2"}},
		{"row two of address line two", `[id="$$employerAddress2_3_RTiCandidate"]`, []string{"Employer 3"}},
		{"adjacent groups read the last", `[id="employerName_1_2_x"]`, []string{"Employer 2"}},
		// No entity word, or no row, or a dashed form another reader owns.
		{"personal address", `[id="address1_RTiCandidate"]`, nil},
		{"entity word without a row", `[id="employerName"]`, nil},
		{"schooling is not a school", `[id="schoolingPreference_1_x"]`, nil},
		{"coworker is not an employer", `[id="coworkerReference_1_x"]`, nil},
		{"row zero is no row", `[id="employerName_0_x"]`, nil},
		{"junk", `[id="$$_1_"]`, nil},
		{"dashed greenhouse form", `[id="school--1"]`, nil},
		// Workday's own row ids and the history spellings.
		{"workday work row", `[id="workExperience-167--jobTitle"]`, []string{"Work History", "Row 167"}},
		{"workday education row", `[id="education-7--degree"]`, []string{"Education History", "Row 7"}},
		{"workday single dash is not a row", `[id="workExperience-jobTitle"]`, nil},
		{"dashed work history", `[id="public-candidate-work-history-address-0-county"]`, []string{"Work History", "Row 0"}},
		{"dotted work history", `[id="workHistory.companyPhone.0"]`, []string{"Work History", "Row 0"}},
		{"dotted education history", `[id="educationHistory.city.0"]`, []string{"Education History", "Row 0"}},
		{"underscore education history", `[id="education_history_city_2"]`, []string{"Education History", "Row 2"}},
		{"history without a row", `[id="work-history-employer-name"]`, []string{"Work History"}},
		// Nested form names (Citadel): the bracket after the entity word is the row.
		{"nested education row", `input[name="profile[education][new0][to]"][type="date"]`, []string{"Education History", "Row 0"}},
		{"nested employment row", `input[name='candidate[employments][2][company]']`, []string{"Work History", "Row 2"}},
		{"nested entity with no field", `input[name="profile[education]"]`, nil},
		{"top-level address", `[id="address-county"]`, nil},
		// Selector shapes.
		{"css id", `#educationHistory\.city\.0`, []string{"Education History", "Row 0"}},
		{"name and type", `input[name="workHistory.city.1"][type="text"]`, []string{"Work History", "Row 1"}},
		{"shadow host chain reads the last segment", `[id="host"] >>> [id="employerCity_3_x"]`, []string{"Employer 3"}},
		{"structural path states nothing", `[id="form"] > div:nth-child(2) > input:nth-child(1)`, nil},
		{"empty", ``, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SectionPath(c.selector); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("SectionPath(%s) = %q, want %q", c.selector, got, c.want)
			}
		})
	}
}

// The derived path reaches the matcher: a bare "County" box in a past
// employer's row is not filled with the applicant's own county, while the same
// label on a top-level control is.
func TestBind_SectionPath_KeepsHomeAddressOutOfAnEmployerRow(t *testing.T) {
	f := newFixture(t, alexProfile)
	b := f.binding(nil)
	ask := func(selector string) engine.Resolution {
		t.Helper()
		resp, err := b.Match(context.Background(), engineRequest(selector, "County"))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Results[0]
	}
	if r := ask(`[id="address-county"]`); r.Outcome != "filled" || r.Value != "Durham County" {
		t.Fatalf("top-level county: %+v", r)
	}
	for _, sel := range []string{`[id="$$employerAddress1_2_RTiCandidate"]`, `[id="public-candidate-work-history-address-0-county"]`, `input[name="profile[education][new0][county]"][type="text"]`} {
		r := ask(sel)
		if r.Outcome != "abstained" || r.Reason != string(match.ReasonSectionPolicy) || !r.Terminal || r.ClientMayOverwrite {
			t.Fatalf("%s: %+v", sel, r)
		}
	}
	// A section the engine already found structurally is not replaced by the name.
	structural := engineRequest(`[id="address-county"]`, "County")
	structural.Questions[0].Section = []string{"Contact"}
	resp, err := b.Match(context.Background(), structural)
	if err != nil || resp.Results[0].Outcome != "filled" {
		t.Fatalf("%+v %v", resp, err)
	}
}
