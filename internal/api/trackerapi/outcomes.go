package trackerapi

import (
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/tracker"
)

func toOutcomeGroup(g tracker.OutcomeGroup) wire.OutcomeGroup {
	out := wire.OutcomeGroup{Key: g.Key, Applications: apikit.Safe(g.Applications), Known: apikit.Safe(g.Known), Unknown: apikit.Safe(g.Unknown), Open: apikit.Safe(g.Open), Closed: apikit.Safe(g.Closed),
		Stages: make([]wire.OutcomeStageReach, len(g.Stages)), Insufficient: apikit.NonNil(g.Insufficient)}
	for i, s := range g.Stages {
		out.Stages[i] = wire.OutcomeStageReach{Stage: string(s.Stage), Numerator: apikit.Safe(s.Numerator), Denominator: apikit.Safe(s.Denominator), Open: apikit.Safe(s.Open), Unknown: apikit.Safe(s.Unknown)}
	}
	return out
}

func toOutcomeGroups(gs []tracker.OutcomeGroup) []wire.OutcomeGroup {
	out := make([]wire.OutcomeGroup, len(gs))
	for i, g := range gs {
		out[i] = toOutcomeGroup(g)
	}
	return out
}

// Outcomes serves the descriptive outcome summary of an application
// date cohort. It reads the tracker and writes nothing.
func (s *Handlers) Outcomes(c *apikit.Call) (apikit.Reply, error) {
	tr, err := s.Tracker()
	if err != nil {
		return apikit.Reply{}, err
	}
	q := apikit.QueryOf(c)
	oq := tracker.OutcomeQuery{From: q.Str("from"), To: q.Str("to"), AsOf: q.Str("asOf")}
	o, err := tr.Outcomes(c.R.Context(), oq)
	if err != nil {
		return apikit.Reply{}, TrackerError(err)
	}
	return apikit.Reply{Data: WireOutcomes(o)}, nil
}

func WireOutcomes(o tracker.Outcomes) wire.TrackerOutcomes {
	return wire.TrackerOutcomes{From: o.From, To: o.To, AsOf: o.AsOf, Overall: toOutcomeGroup(o.Overall),
		BySource: toOutcomeGroups(o.BySource), ByRoleFamily: toOutcomeGroups(o.ByRoleFamily), Folded: apikit.Safe(o.Folded), NoApplicationDate: apikit.Safe(o.NoApplicationDate)}
}
