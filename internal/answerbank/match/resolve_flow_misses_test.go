package match

import "testing"

// usWorkAuthStatusEntry is the shipped us-work-authorization-status entry with
// the answers a user would record on it.
func usWorkAuthStatusEntry() Entry {
	e := ent("us-work-authorization-status", "pick-option", withTypes(InputSingleSelect), withPreferred(
		"I am authorized to work in this country for any employer",
		"authorized to work in the united states for any employer",
		"for any employer"))
	e.Category = CategoryWorkAuthorization
	return e
}

var workAuthSponsorshipLabels = []string{
	"1. I am authorized to work for any employer in the country where the position is based without any renewal or sponsorship.",
	"2. I require/will require Hover's sponsorship to obtain work authorization in the country in which this position is based (e.g. H1-B, TN, etc)",
	"3. My status to work in the country in which this position is based is unknown.",
}

func TestAnswerBank_WorkAuthStatus_NeedsSponsorshipTakesTheRequirementOption(t *testing.T) {
	m := pickMatcher(usWorkAuthStatusEntry(), sponsorEntry())
	q := selectQ("Work Authorization", workAuthSponsorshipLabels...)
	for _, f := range []func(*Request){
		withSponsorship(boolP(true), boolP(true)),
		withSponsorship(boolP(false), boolP(true)),
	} {
		r := matchReq(m, q, f)
		if r.ChosenLabel != workAuthSponsorshipLabels[1] || r.Outcome != OutcomeFilledReview {
			t.Fatalf("a user who needs sponsorship takes the one option stating it, for review: %+v", r)
		}
	}
}

const tzRangeQuestion = "Are you located within a Continental US/Americas timezone? (Between GMT-7 and GMT-3)"

func tzRangeMatcher(zone string) *Matcher {
	e := ent("time-zone", "pick-option", withTypes(InputSingleSelect), withPreferred(zone))
	e.Category = CategoryAddress
	return pickMatcher(e)
}

func TestAnswerBank_TimeZoneRangeQuestionIsDerivedFromTheStoredZone(t *testing.T) {
	q := Question{Selector: "s", Text: tzRangeQuestion, Kind: KindRadioGroup, InputType: InputYesNo, Options: opts("YES", "NO")}
	// Every offset of the zone is in the range, or none is.
	wantFill(t, matchReq(tzRangeMatcher("Eastern Time"), q), "YES", OutcomeFilled)
	wantFill(t, matchReq(tzRangeMatcher("America/Chicago"), q), "YES", OutcomeFilled)
	wantFill(t, matchReq(tzRangeMatcher("Hawaii Time"), q), "NO", OutcomeFilled)
	// Pacific time is in the range only while daylight time is in force.
	wantAbstain(t, matchReq(tzRangeMatcher("Pacific Time"), q), ReasonAmbiguous, "")
	// A value that names no US zone, or no value at all, is never guessed.
	wantAbstain(t, matchReq(tzRangeMatcher("Asia/Shanghai"), q), ReasonAmbiguous, "")
	blank := ent("time-zone", "pick-option", withTypes(InputSingleSelect), withPreferred())
	blank.Placeholder = true
	wantAbstain(t, matchReq(pickMatcher(blank), q), ReasonPlaceholderEmpty, "")
}
