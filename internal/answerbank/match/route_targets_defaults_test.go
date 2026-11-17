package match_test

import (
	"strings"
	"testing"

	"applysmith/internal/answerbank"
	"applysmith/internal/answerbank/match"
)

// userOnly reports the ids the specification says exist only in the user's
// bank ("Embedded default bank and first-run seeding"): the two consent
// entries and the years-experience family. A route naming one is skipped
// silently while the entry is absent ("Pattern route catalog").
func userOnly(id string) bool {
	return id == "terms-and-conditions-consent" || id == "assessment-proctoring-consent" ||
		strings.HasPrefix(id, match.YearsExperiencePrefix)
}

func TestDefaultBank_EveryRouteTargetExistsUnlessUserOnly(t *testing.T) {
	r, err := answerbank.DefaultBank()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, e := range r.Bank.Entries {
		have[e.ID] = true
	}
	targets := match.RouteTargets()
	if len(targets) < 100 {
		t.Fatalf("only %d route targets listed; the catalog walk is broken", len(targets))
	}
	for _, id := range targets {
		switch {
		case userOnly(id):
			if have[id] {
				t.Errorf("%s exists only in the user's bank, yet the default bank ships it", id)
			}
		case !have[id]:
			t.Errorf("a route targets %q and the default bank has no such entry", id)
		}
	}
}

// A route that names a user-only entry is skipped while the user has not
// created it: the question is never filled from another entry's answer and the
// result is an ordinary abstain. The one stand-in is the total years, which the
// full-time years answer once the user wrote them (the default bank has not).
func TestDefaultBank_UserOnlyRouteTargetsAbstainCleanly(t *testing.T) {
	r, err := answerbank.DefaultBank()
	if err != nil {
		t.Fatal(err)
	}
	yn := []match.Option{{Value: "Yes", Label: "Yes"}, {Value: "No", Label: "No"}}
	for _, c := range []struct {
		target string
		q      match.Question
	}{
		{"terms-and-conditions-consent", match.Question{Selector: "#q", Kind: match.KindRadioGroup, Options: yn, Text: "Do you agree to the Terms and Conditions?"}},
		{"assessment-proctoring-consent", match.Question{Selector: "#q", Kind: match.KindRadioGroup, Options: yn, Text: "Do you consent to being proctored during the assessment?"}},
		{"years-experience-total", match.Question{Selector: "#q", Kind: match.KindText, Text: "How many years of total experience do you have?"}},
	} {
		t.Run(c.target, func(t *testing.T) {
			resp, err := match.New(r.Bank, match.DefaultStages()).Match(match.Request{Host: "jobs.example.com", Questions: []match.Question{c.q}})
			if err != nil || len(resp.Results) != 1 {
				t.Fatalf("err %v, %d results", err, len(resp.Results))
			}
			got := resp.Results[0]
			if got.Outcome != match.OutcomeAbstained || got.Answer != "" || got.ChosenLabel != "" || got.EntryID == c.target {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

// CACI's required "Terms and Conditions" box, ticked "Yes": with no terms entry
// of the user's it is the form's acknowledgement, ticked under the legal
// setting and left for the user without it; the user's own entry still wins.
func TestDefaultBank_ABareTermsBoxFollowsTheLegalSetting(t *testing.T) {
	r, err := answerbank.DefaultBank()
	if err != nil {
		t.Fatal(err)
	}
	q := match.Question{Selector: "#t", Kind: match.KindCheckbox, Required: true, Text: "Terms and Conditions", Options: []match.Option{{Value: "Yes", Label: "Yes"}}}
	run := func(b match.Bank, on bool) match.Result {
		resp, err := match.New(b, match.DefaultStages()).Match(match.Request{Host: "jobs.example.com", Questions: []match.Question{q}, AutoAcceptLegalDeclarations: on})
		if err != nil || len(resp.Results) != 1 {
			t.Fatalf("err %v", err)
		}
		return resp.Results[0]
	}
	if got := run(r.Bank, true); got.Outcome == match.OutcomeAbstained || got.Answer != "Yes" || got.EntryID != "mandatory-acknowledge" {
		t.Fatalf("setting on: %+v", got)
	}
	if got := run(r.Bank, false); got.Outcome != match.OutcomeAbstained || got.Reason != match.ReasonContextPolicy {
		t.Fatalf("setting off: %+v", got)
	}
	own := r.Bank
	own.Entries = append(append([]match.Entry(nil), r.Bank.Entries...), match.Entry{ID: "terms-and-conditions-consent", Kind: match.KindFixed, Body: match.Body{Answer: "Yes"}, Meta: match.Meta{Category: match.CategoryConsent, InputTypes: []string{"checkbox"}}})
	if got := run(own, true); got.EntryID != "terms-and-conditions-consent" {
		t.Fatalf("own entry: %+v", got)
	}
}

// A disclosure the absence of a mention never answers (F-10) reaches the
// shipped entry of its family, so the answer the user wrote there is the one
// given.
func TestDefaultBank_DisclosureShapesReachTheirFamilysEntry(t *testing.T) {
	r, err := answerbank.DefaultBank()
	if err != nil {
		t.Fatal(err)
	}
	b := r.Bank
	for i, e := range b.Entries {
		b.Entries[i].Placeholder, b.Entries[i].NeedsReview = false, false
		if e.Kind == match.KindPickOption {
			b.Entries[i].Preferred = []string{"Yes"}
		} else {
			b.Entries[i].Answer = "Yes"
		}
	}
	yesNo := []match.Option{{Value: "Yes", Label: "Yes"}, {Value: "No", Label: "No"}}
	cases := map[string]string{
		"Have you ever served in the military?":                  "military-service-status",
		"Do you have a criminal background?":                     "criminal-conviction-history",
		"Have you held a job from which you were fired?":         "terminated-suspended",
		"Have you ever been terminated or suspended from a job?": "terminated-suspended",
		"Have you ever been terminated or asked to resign?":      "terminated-suspended",
		"Is your spouse in the military?":                        "military-spouse",
		"Have you worked under a non-compete agreement?":         "non-compete-or-non-solicitation",
		"Have you held a security clearance?":                    "security-clearance-history",
		"Are you excluded from federal healthcare programs?":     "federal-healthcare-exclusion",
		"Do you have relatives working at Northwind Example?":    "relatives-at-employer",
	}
	var qs []match.Question
	for q := range cases {
		qs = append(qs, match.Question{Selector: q, Text: q, Kind: match.KindRadioGroup, Options: yesNo})
	}
	resp, err := match.New(b, match.DefaultStages()).MatchContext(t.Context(), match.Request{Host: "jobs.example.com", Questions: qs})
	if err != nil {
		t.Fatal(err)
	}
	for _, res := range resp.Results {
		if res.EntryID != cases[res.Selector] || res.Answer != "Yes" {
			t.Errorf("%q: %s %s %q", res.Selector, res.Outcome, res.EntryID, res.Answer)
		}
	}
}

// routeYields are the intended fall-throughs, "first>second": the first
// entry's route claims the wording, and the second entry's route follows when
// the first is absent or cannot answer the control. referral-source leaves a
// source question whose options it binds none of; total-cash-compensation has
// salary-expectation as its twin (route 222); the age threshold computes a
// Yes/No that the fixed age entries hold for other controls.
var routeYields = map[string]bool{
	"referral-source>job-source": true, "total-cash-compensation>salary-expectation": true,
	"age-minimum-threshold>age-18": true, "age-minimum-threshold>age-21": true,
}

// containedRoutes win no example of their own: an earlier route of the same
// entry takes every wording they match. They stay because the parity map names
// their case rows.
var containedRoutes = map[int]bool{145: true, 155: true, 232: true, 233: true, 238: true, 240: true, 369: true}

// Every route's own examples win the walk. On each control its entry takes,
// the whole catalog gives an example to the route's entry, and every route
// wins at least one example itself, so no route is shadowed by an earlier one.
func TestDefaultBank_EveryRouteWinsItsOwnExamples(t *testing.T) {
	r, err := answerbank.DefaultBank()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]*match.Entry{}
	for i := range r.Bank.Entries {
		byID[r.Bank.Entries[i].ID] = &r.Bank.Entries[i]
	}
	// The user-only entries, shaped as a user's bank holds them.
	for _, id := range match.RouteTargets() {
		if byID[id] == nil && userOnly(id) {
			e := &match.Entry{ID: id, Kind: match.KindFixed, Meta: match.Meta{InputTypes: []string{"checkbox", "yes_no"}}}
			if strings.HasPrefix(id, match.YearsExperiencePrefix) {
				e.InputTypes = []string{"text", "number", "single_select"}
			}
			byID[id] = e
		}
	}
	seen, won := map[int]bool{}, map[int]bool{}
	for _, w := range match.RouteCaseWalks(func(id string) *match.Entry { return byID[id] }) {
		seen[w.Priority] = true
		yields := routeYields[w.WinnerTarget+">"+w.Target]
		won[w.Priority] = won[w.Priority] || w.Winner == w.Priority || yields
		if w.WinnerTarget != w.Target && !yields {
			t.Errorf("route %d (%s), %q on %s: route %d (%s) wins", w.Priority, w.Target, w.Text, w.Input, w.Winner, w.WinnerTarget)
		}
	}
	if len(seen) < 500 {
		t.Fatalf("only %d routes walked", len(seen))
	}
	for p := range seen {
		if won[p] == containedRoutes[p] {
			t.Errorf("route %d: wins an example of its own = %v, listed as contained = %v", p, won[p], containedRoutes[p])
		}
	}
}
