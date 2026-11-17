package bind

import (
	"context"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/tracker"
)

// TrackerJobs resolves a posting's location from the tracker row the page's URL
// identifies (by identity key or alias, never by company and role).
type TrackerJobs struct{ T *tracker.Tracker }

// Location implements Jobs. A URL the tracker does not know, or cannot read,
// has no location.
func (j TrackerJobs) Location(ctx context.Context, postingURL string) string {
	res, err := j.T.Lookup(ctx, postingURL)
	if err != nil || !res.Found || res.Entry == nil {
		return ""
	}
	return res.Entry.Location
}

// History implements Jobs: the tracker's applied companies as prior
// applications and its interviewed ones as prior interviews. They carry no
// recorded date, so a later application never unsettles a list the user
// marked complete.
func (j TrackerJobs) History(ctx context.Context) []match.RelationshipFact {
	return HistoryFacts(ctx, j.T)
}

// HistoryFacts reads the tracker's history as relationship facts.
func HistoryFacts(ctx context.Context, t *tracker.Tracker) []match.RelationshipFact {
	h, err := t.History(ctx)
	if err != nil {
		return nil
	}
	var out []match.RelationshipFact
	for _, c := range h.Applied {
		out = append(out, match.RelationshipFact{Family: match.FamilyPriorApplication, Company: c})
	}
	for _, c := range h.Interviewed {
		out = append(out, match.RelationshipFact{Family: match.FamilyPriorInterview, Company: c})
	}
	return out
}
