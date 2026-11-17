package match

import "testing"

// Compliance and background disclosures are facts about the user: the entries
// ship blank, so an unconfigured install abstains and leaves the control to
// them; a curated answer the user wrote is applied as written.

func blankDisclosure(id string, examples ...string) Entry {
	e := blankEntry(id, KindFixed)
	e.InputTypes, e.Examples = []string{"yes_no", "text"}, examples
	return e
}

func TestAnswerBank_Compliance_BlankDefaultsAbstain(t *testing.T) {
	for _, c := range []struct{ id, q string }{
		{"criminal-conviction-history", "Have you been convicted of a criminal offence in the past 7 years in any country?"},
		{"federal-healthcare-exclusion", "Are you currently excluded from participation in Medicare or Medicaid?"},
		{"governmental-inquiry-pending", "Is there a governmental inquiry pending against you?"},
		{"us-licensed-physician", "Are you a US licensed physician?"},
	} {
		r := matchReq(pickMatcher(blankDisclosure(c.id, c.q)), radioQ(c.q, "Yes", "No"))
		wantAbstain(t, r, ReasonPlaceholderEmpty, "")
		if r.Terminal {
			t.Errorf("%s: a blank default is not terminal: %+v", c.id, r)
		}
	}
	// The user's own answer is applied as written.
	own := disclosureEntry("criminal-conviction-history", "No", "Have you been convicted of a criminal offence in the past 7 years in any country?")
	wantFill(t, matchReq(pickMatcher(own), radioQ("Have you been convicted of a criminal offence in the past 7 years in any country?", "Yes", "No")), "No", OutcomeFilled)
	// The broad conviction question never routes to the state entry.
	state := ent("address-state", "pick-option", withPreferred("Example State (ES)"))
	state.Placeholder = false
	r := matchReq(pickMatcher(blankDisclosure("criminal-conviction-history"), state), selectQ("Have you been convicted of a criminal offence in the past 7 years in any country/region?", "Yes", "No"))
	if r.EntryID == "address-state" {
		t.Fatalf("%+v", r)
	}
}
