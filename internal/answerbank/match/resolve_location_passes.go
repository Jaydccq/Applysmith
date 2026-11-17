package match

import (
	"strings"

	"applysmith/internal/lazyregexp"
)

// Request pass 1: the bare-city widening. It only widens a result an earlier
// step already produced and never changes a terminal abstain, turns an abstain
// into a fill or raises a confidence.

var locationLabelRE = lazyregexp.New(`(?i)^\s*(?:current\s+)?location\b|` + rxbWorkFromWhere)

// passView is what the request passes of this family read: the request, the
// prepared questions, the results they may rewrite and the bank entries.
type passView struct {
	Req       *Request
	Questions []Question
	Results   []Result
	Entries   []Entry
}

func viewOf(req *Request, qs []Question, rs []Result) *passView {
	return &passView{Req: req, Questions: qs, Results: rs, Entries: req.Entries()}
}

// widenBareLocationPass and conditionalFollowUpsPass are the post-passes this
// family contributes; their place in the request-wide order is fixed in
// DefaultStages.
func widenBareLocationPass(req *Request, qs []Question, rs []Result) {
	widenBareLocation(viewOf(req, qs, rs))
}

func conditionalFollowUpsPass(req *Request, qs []Question, rs []Result) {
	conditionalFollowUps(viewOf(req, qs, rs))
}

// widenBareLocation is pass 1: an option-less control whose label starts with
// "Location" or "Current location", or asks where the applicant will work
// from, or any option-less search box (Agave's "What city do you live in?",
// whose suggestions list every Charleston), that the city entry filled with the
// bare city becomes "City, State, Country" when the state and country are known. "Location Preference" is the
// preferred-location entry's and is untouched.
func widenBareLocation(pc *passView) {
	step := &Step{Entries: pc.Entries, Req: pc.Req}
	city := partOf(step, "address-city").text
	if city == "" || strings.Contains(city, ",") {
		return
	}
	state, country := stateProse(partOf(step, "address-state").text), partOf(step, "address-country").text
	if state == "" || country == "" {
		return
	}
	for i, r := range pc.Results {
		q := pc.Questions[i]
		if len(q.Options) != 0 || r.Terminal || r.EntryID != "address-city" || r.Outcome != OutcomeFilled {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(r.Answer), city) || !locationLabelRE.MatchString(q.Text) && q.Kind != KindCustomListbox {
			continue
		}
		r.Answer = strings.Join([]string{city, state, country}, ", ")
		// The apply step searches by the chosen label first: a bare city left
		// there would choose among every place of that name.
		if strings.EqualFold(strings.TrimSpace(r.ChosenLabel), city) {
			r.ChosenLabel = r.Answer
		}
		r.Trace = append(append([]string(nil), r.Trace...), "option-less Location typeahead -> widened the city to city, state, country")
		pc.Results[i] = r
	}
}
