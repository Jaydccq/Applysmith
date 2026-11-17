package apply

import (
	"context"
	"testing"

	"applysmith/internal/engine"
)

// A profile link that a site's own check refuses (a LinkedIn box that wants
// linkedin.com) is never forced in and never replaced by a guess: the row stays
// failed, the page is not written to again, and when the box is required the
// run reports it to the user with the engine's reason.
func TestApply_Check_ALinkTheSiteRefusesIsLeftEmptyAndReportedNeedsUser(t *testing.T) {
	for _, c := range []struct {
		name, label string
		required    bool
		wantLeft    bool
	}{
		{"required LinkedIn box", "LinkedIn URL", true, true},
		{"required GitHub box", "GitHub URL", true, true},
		{"optional LinkedIn box", "LinkedIn Profile", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newSubmit(t, nil)
			row := engine.Row{Selector: "#link", Label: c.label, Required: c.required, Outcome: "failed", Reason: "format-not-accepted", Kind: "text"}
			x.page.rows = func() []engine.InspectRow {
				return []engine.InspectRow{{Selector: "#link", Label: c.label, Required: c.required, Kind: "text", Value: ""}}
			}
			ps := &pageState{}
			x.s.check(context.Background(), doneResult(row), ps)
			if len(x.page.answers) != 0 {
				t.Fatalf("answers %+v: a refused link is never written another way", x.page.answers)
			}
			if !c.wantLeft {
				if len(ps.leftovers) != 0 {
					t.Fatalf("leftovers %+v: an optional box is not a gap", ps.leftovers)
				}
				return
			}
			if len(ps.leftovers) != 1 {
				t.Fatalf("leftovers %+v, want the one refused link", ps.leftovers)
			}
			got := ps.leftovers[0]
			if got.Reason != ReasonNeedsUser || got.Detail != "format-not-accepted" || got.Label != c.label || got.Draft != nil {
				t.Fatalf("leftover %+v, want needs-user with the engine's reason and no draft answer", got)
			}
			// The record keeps it, so the agent finishing the run names it.
			ps.recordLeftovers()
			if r := ps.report.Leftovers; len(r) != 1 || r[0].Label != c.label || r[0].Draft != nil {
				t.Fatalf("recorded leftovers %+v", r)
			}
		})
	}
}
