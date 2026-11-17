// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package gates

import (
	"flag"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
)

// t0 is the fixed instant every time-based case is measured from.
const t0 int64 = 1_800_000_000_000

var exercised = map[Gate]map[string]bool{}

func record(d Decision) {
	if exercised[d.Gate] == nil {
		exercised[d.Gate] = map[string]bool{}
	}
	exercised[d.Gate][d.Code] = true
}

// allGates lists every gate of the vocabulary in a stable order.
func allGates() []Gate {
	out := make([]Gate, 0, len(vocabulary))
	for g := range vocabulary {
		out = append(out, g)
	}
	slices.Sort(out)
	return out
}

// got fails unless d is a well-formed decision with the wanted gate, verdict
// and code. It also records the code for the matrix coverage check.
func got(t *testing.T, d Decision, g Gate, v Verdict, code string) {
	t.Helper()
	record(d)
	if !d.Valid() {
		t.Fatalf("malformed decision %+v", d)
	}
	if d.Gate != g || d.Verdict != v || d.Code != code {
		t.Fatalf("got %s/%s/%s (%q), want %s/%s/%s", d.Gate, d.Verdict, d.Code, d.Reason, g, v, code)
	}
}

// TestMain turns the suite into a matrix check: when the whole suite runs,
// every reason code in every gate's closed vocabulary must have been returned
// by at least one case.
func TestMain(m *testing.M) {
	code := m.Run()
	if code == 0 && flag.Lookup("test.run").Value.String() == "" && !testing.Short() {
		var missing []string
		for _, g := range allGates() {
			for _, c := range vocabulary[g] {
				if !exercised[g][c] {
					missing = append(missing, fmt.Sprintf("%s/%s", g, c))
				}
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			fmt.Fprintf(os.Stderr, "reason codes never returned by a test: %s\n", strings.Join(missing, ", "))
			code = 1
		}
	}
	os.Exit(code)
}
