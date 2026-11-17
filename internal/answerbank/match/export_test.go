package match

import (
	"slices"
	"strings"
)

// RouteTargets lists every entry id a route of the catalog names, sorted and
// without repeats. It is visible to the external test package only.
func RouteTargets() []string {
	var out []string
	for _, r := range routeCatalog() {
		if !slices.Contains(out, r.Target) && !strings.HasPrefix(r.Target, "$") {
			out = append(out, r.Target)
		}
	}
	slices.Sort(out)
	return out
}

// RouteCaseWalk is one positive table case of a route, walked through the whole
// catalog on one control shape its entry takes.
type RouteCaseWalk struct {
	Priority     int
	Target, Text string
	Input        InputType
	// Winner is the winning route's priority, 0 when no route wins.
	Winner       int
	WinnerTarget string
}

// RouteCaseWalks walks every route's positive cases once per input type the
// route's entry takes; a control accepts an entry that takes its input type.
// entry returns the bank's entry of an id, nil when there is none.
func RouteCaseWalks(entry func(id string) *Entry) []RouteCaseWalk {
	rs, cases := routeCatalog(), allRouteCases()
	var out []RouteCaseWalk
	for i := range rs {
		r, target := &rs[i], entry(rs[i].Target)
		if target == nil {
			continue
		}
		types := defaultInputTypes(target.Kind)
		if len(target.InputTypes) > 0 {
			types = nil
			for _, t := range target.InputTypes {
				types = append(types, InputType(t))
			}
		}
		for _, in := range types {
			optioned := in == InputYesNo || in == InputSingleSelect || in == InputMultiSelect || in == InputCheckbox
			if r.RequiresOptions && !optioned {
				continue
			}
			for _, text := range cases[r.Priority].Pos {
				reworded, _ := routeVocabularyVariant(text)
				// The route claims its own case, so no later route can win it.
				w := walkRoutes(rs[:i+1], text, reworded, routeEnv{hasOptions: optioned, lookup: entry,
					accepts: func(e *Entry) (bool, string) { return e.allowsInput(in), "" }})
				c := RouteCaseWalk{Priority: r.Priority, Target: r.Target, Text: text, Input: in}
				if w.Hit != nil {
					c.Winner, c.WinnerTarget = w.Hit.Priority, w.Hit.Target
				}
				out = append(out, c)
			}
		}
	}
	return out
}
