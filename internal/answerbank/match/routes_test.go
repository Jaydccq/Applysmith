package match

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// ruleCases are the table cases of one route. Pos must be claimed by the route
// on its own (pattern hit, no veto); Neg must not be.
type ruleCases struct {
	Pos []string
	Neg []string
}

func allRouteCases() map[int]ruleCases {
	return mergeRuleCases(routeCasesFixedA(), routeCasesFixedB(), routeCasesPick(), routeCasesOther(), routeCasesPhrasing())
}

// mergeRuleCases joins case tables; a priority two tables both hold would
// drop one table's cases, so it panics.
func mergeRuleCases(parts ...map[int]ruleCases) map[int]ruleCases {
	all := map[int]ruleCases{}
	for _, part := range parts {
		for k, v := range part {
			if _, dup := all[k]; dup {
				panic(fmt.Sprintf("duplicate route cases for priority %d", k))
			}
			all[k] = v
		}
	}
	return all
}

func TestRoutes_MergingCaseTablesRefusesADuplicatePriority(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("two tables holding priority 7 merged")
		}
	}()
	mergeRuleCases(map[int]ruleCases{7: {}}, map[int]ruleCases{7: {}})
}

func TestRoutes_AllCompileAndPrioritiesUnique(t *testing.T) {
	if _, err := compileRoutes(routeCatalogRaw()); err != nil {
		t.Fatal(err)
	}
}

func TestRoutes_EveryRuleHasExplicitTarget(t *testing.T) {
	for _, r := range routeCatalogRaw() {
		if r.Target == "" {
			t.Errorf("route %d has no target", r.Priority)
		}
	}
}

func TestRoutes_EveryRuleHasPositiveAndNegativeCase(t *testing.T) {
	cases := allRouteCases()
	seen := map[int]bool{}
	for i := range routeCatalog() {
		r := &routeCatalog()[i]
		seen[r.Priority] = true
		t.Run(fmt.Sprintf("p%d", r.Priority), func(t *testing.T) {
			c, ok := cases[r.Priority]
			if !ok || len(c.Pos) == 0 || len(c.Neg) == 0 {
				t.Errorf("route %d (%s) needs at least one positive and one negative case", r.Priority, r.Target)
				return
			}
			for _, p := range c.Pos {
				if !r.Matches(p) {
					t.Errorf("route %d (%s) must claim %q", r.Priority, r.Target, p)
				}
			}
			for _, n := range c.Neg {
				if r.Matches(n) {
					t.Errorf("route %d (%s) must not claim %q", r.Priority, r.Target, n)
				}
			}
		})
	}
	var orphans []int
	for k := range cases {
		if !seen[k] {
			orphans = append(orphans, k)
		}
	}
	sort.Ints(orphans)
	if len(orphans) > 0 {
		t.Errorf("cases for priorities with no route: %v", orphans)
	}
}

// Question text reaches every route folded once: a route claims a question
// written with no-break or ideographic spaces and curly apostrophes exactly as
// it claims the plain text.
func TestRoutes_CatalogIgnoresUnicodeSpacesAndCurlyApostrophes(t *testing.T) {
	cases := allRouteCases()
	spaces := strings.NewReplacer(" ", " ")
	ideographic := strings.NewReplacer(" ", "　")
	curly := strings.NewReplacer("'", "’")
	checked := 0
	for i := range routeCatalog() {
		r := &routeCatalog()[i]
		for _, p := range cases[r.Priority].Pos {
			if !r.Matches(p) {
				continue // the positive-case test reports it
			}
			for name, v := range map[string]string{"no-break space": spaces.Replace(p), "ideographic space": ideographic.Replace(p), "curly apostrophe": curly.Replace(p)} {
				checked++
				if !r.Matches(v) {
					t.Errorf("route %d (%s) lost %q when written with a %s", r.Priority, r.Target, p, name)
				}
			}
		}
	}
	if checked < 1000 {
		t.Fatalf("only %d variants checked", checked)
	}
}
