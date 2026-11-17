// Package signalsapi serves the hiring signals in the local API: the company
// list and detail, the review queue, the user's actions on a company and the
// signal status. internal/api registers the handlers.
package signalsapi

import (
	"context"
	"errors"
	"strings"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/signals"
)

// Deps is what the signal handlers use from the server.
type Deps struct {
	Signals func() (*signals.Service, error)
	Notify  func(topics ...string)
}

// Handlers serves the signal routes.
type Handlers struct{ Deps }

func New(d Deps) *Handlers { return &Handlers{Deps: d} }

func signalsErr(err error) error {
	var active *signals.ActiveError
	switch {
	case errors.Is(err, signals.ErrNotFound):
		return apikit.NewErr(wire.CodeNotFound, "no such signal")
	case errors.Is(err, signals.ErrDecided):
		return apikit.NewErr(wire.CodeConflict, "this review item was decided already")
	case errors.Is(err, signals.ErrNotUndoable):
		return apikit.NewErr(wire.CodeConflict, "%s", strings.TrimPrefix(err.Error(), signals.ErrNotUndoable.Error()+": "))
	case errors.Is(err, signals.ErrNoEnqueuer):
		return apikit.NewErr(wire.CodeUnavailable, "evaluations cannot be started on this server")
	case errors.As(err, &active):
		return apikit.NewErr(wire.CodeConflict, "%s", err.Error())
	}
	return err
}

func (s *Handlers) List(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Signals()
	if err != nil {
		return apikit.Reply{}, err
	}
	q := apikit.QueryOf(c)
	f := signals.ListFilter{Tier: q.Str("tier"), Source: q.Str("source"), State: q.Str("state")}
	page, size := q.Paging(25)
	for name, dst := range map[string]*time.Time{"from": &f.From, "to": &f.To} {
		if raw := q.Str(name); raw != "" {
			t, perr := time.Parse(time.RFC3339, raw)
			if perr != nil {
				// A date is a whole local day.
				t, perr = time.ParseInLocation(time.DateOnly, raw, time.Local)
				if perr == nil && name == "to" {
					t = t.AddDate(0, 0, 1).Add(-time.Nanosecond)
				}
			}
			if perr != nil {
				q.Fail(apikit.InvalidField(name, "%s must be a date or an RFC 3339 time", name))
				continue
			}
			*dst = t.UTC()
		}
	}
	if q.Err != nil {
		return apikit.Reply{}, q.Err
	}
	rows, err := svc.List(c.R.Context(), f)
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.SignalList{Rows: []wire.SignalRow{}, Page: apikit.Safe(page), PageSize: apikit.Safe(size), Total: apikit.Safe(len(rows))}
	for _, r := range rows[min((page-1)*size, len(rows)):min(page*size, len(rows))] {
		out.Rows = append(out.Rows, toSignalRow(r))
	}
	return apikit.Reply{Data: out}, nil
}

func toSignalRow(r signals.ListRow) wire.SignalRow {
	return wire.SignalRow{CompanyID: apikit.Safe64(r.CompanyID), Company: r.Company, Tier: r.Tier, Momentum: r.Momentum, ScanPriority: r.Priority, State: r.State,
		Blocked: r.Blocked, Sources: append([]string{}, r.Sources...), NewestEvidence: apikit.TimePtr(r.Newest)}
}

func toSignalAssessment(a signals.Assessment) wire.SignalAssessment {
	out := wire.SignalAssessment{ID: apikit.Safe64(a.ID), Status: a.Status, EventType: a.Type, Intent: a.Intent, Teams: append([]string{}, a.Teams...),
		Scale: a.Scale, Strength: apikit.Safe(a.Strength), Confidence: a.Confidence, Reasoning: a.Reasoning, Quotes: []wire.SignalQuote{}}
	for _, q := range a.Quotes {
		out.Quotes = append(out.Quotes, wire.SignalQuote{DocumentID: apikit.Safe64(q.DocumentID), Quote: q.Quote, Verified: a.Status != signals.StatusQuoteFailed && a.Status != signals.StatusSchemaInvalid})
	}
	return out
}

func (s *Handlers) Get(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Signals()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "companyId")
	if e != nil {
		return apikit.Reply{}, e
	}
	d, err := svc.Detail(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, signalsErr(err)
	}
	rows, err := svc.List(c.R.Context(), signals.ListFilter{CompanyID: id})
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.SignalDetail{Events: []wire.SignalEvent{}, Factors: []wire.SignalFactor{}, Postings: []wire.SignalPosting{}, Outcomes: []wire.SignalOutcome{},
		SearchLinks: append([]string{}, d.SearchLinks...), BlockExpires: apikit.TimePtr(d.BlockExpires)}
	out.Row = wire.SignalRow{CompanyID: apikit.Safe64(d.Company.ID), Company: d.Company.Name, Tier: d.Tier.Tier, Momentum: d.Momentum, ScanPriority: d.Tier.Priority, State: d.Company.State,
		Blocked: d.Blocked, Sources: []string{}}
	if out.Row.Tier == "" {
		out.Row.Tier = wire.SignalCold
	}
	for _, r := range rows {
		if r.CompanyID == id {
			out.Row = toSignalRow(r)
		}
	}
	for _, ev := range d.Events {
		e := wire.SignalEvent{ID: apikit.Safe64(ev.Event.ID), Type: ev.Event.Type, Time: wire.Time{Time: ev.Event.Time}, Amount: ev.Event.Amount, Held: ev.Held, Documents: []wire.SignalDocument{}}
		for _, doc := range ev.Documents {
			e.Documents = append(e.Documents, wire.SignalDocument{ID: apikit.Safe64(doc.ID), Adapter: doc.Adapter, URL: doc.URL, Title: doc.Title, Date: wire.Time{Time: doc.EventTime()}})
		}
		if ev.Assessment != nil {
			a := toSignalAssessment(*ev.Assessment)
			e.Assessment = &a
		}
		out.Events = append(out.Events, e)
	}
	for _, f := range d.Decomposition {
		out.Factors = append(out.Factors, wire.SignalFactor{EventID: apikit.Safe64(f.EventID), EventType: f.EventType, EventTime: f.EventTime, Intent: f.Intent, TypeWeight: f.Type, Strength: f.Strength,
			Confidence: f.Confidence, Source: f.Source, TimeDecay: f.Decay, Contribution: f.Contribution, Weak: f.Weak})
	}
	if d.Identity != nil {
		out.Identity = &wire.SignalIdentity{Status: d.Identity.Status, ATS: d.Identity.ATS, Slug: d.Identity.Slug, CareersURL: d.Identity.CareersURL, Lane: d.Identity.Lane}
	}
	for _, p := range d.Postings {
		out.Postings = append(out.Postings, wire.SignalPosting{ID: apikit.Safe64(p.ID), Title: p.Title, Location: p.Location, URL: p.URL, Verdict: p.Verdict, Reasons: append([]string{}, p.Reasons...),
			FirstSeen: wire.Time{Time: p.FirstSeen}, HandedOff: apikit.TimePtr(p.HandedOff), Handoff: p.Handoff})
	}
	for _, o := range d.Outcomes {
		out.Outcomes = append(out.Outcomes, wire.SignalOutcome{EventID: apikit.Safe64(o.EventID), Type: o.Type, Status: o.Status, LeadDays: apikit.Safe(o.LeadDays), WindowEnd: wire.Time{Time: o.End}})
	}
	return apikit.Reply{Data: out}, nil
}

func toReviewItem(it signals.ReviewItem) wire.SignalReviewItem {
	return wire.SignalReviewItem{ID: apikit.Safe64(it.ID), CompanyID: apikit.Safe64(it.CompanyID), Company: it.Company, Reason: it.Reason, State: it.State,
		Assessment: toSignalAssessment(it.Assessment), CreatedAt: wire.Time{Time: it.CreatedAt}}
}

func (s *Handlers) Review(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Signals()
	if err != nil {
		return apikit.Reply{}, err
	}
	items, err := svc.Review(c.R.Context(), false)
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.SignalReviewList{Items: make([]wire.SignalReviewItem, len(items))}
	for i, it := range items {
		out.Items[i] = toReviewItem(it)
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Handlers) DecideReview(approve bool) apikit.Handler {
	return func(c *apikit.Call) (apikit.Reply, error) {
		svc, err := s.Signals()
		if err != nil {
			return apikit.Reply{}, err
		}
		id, e := apikit.PathID(c, "id")
		if e != nil {
			return apikit.Reply{}, e
		}
		it, err := svc.Decide(c.R.Context(), id, approve)
		if err != nil {
			return apikit.Reply{}, signalsErr(err)
		}
		return apikit.Reply{Data: toReviewItem(it)}, nil
	}
}

// RestoreReview is the undo of a rejection: the item waits again.
func (s *Handlers) RestoreReview(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Signals()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	it, err := svc.Reopen(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, signalsErr(err)
	}
	return apikit.Reply{Data: toReviewItem(it)}, nil
}

// Action runs a user action on a company and answers with its row.
func (s *Handlers) Action(act func(*signals.Service, context.Context, int64) error) apikit.Handler {
	return func(c *apikit.Call) (apikit.Reply, error) {
		svc, err := s.Signals()
		if err != nil {
			return apikit.Reply{}, err
		}
		id, e := apikit.PathID(c, "companyId")
		if e != nil {
			return apikit.Reply{}, e
		}
		if err := act(svc, c.R.Context(), id); err != nil {
			return apikit.Reply{}, signalsErr(err)
		}
		rows, err := svc.List(c.R.Context(), signals.ListFilter{CompanyID: id})
		if err != nil {
			return apikit.Reply{}, err
		}
		for _, r := range rows {
			if r.CompanyID == id {
				return apikit.Reply{Data: toSignalRow(r)}, nil
			}
		}
		co, err := svc.Repo().Company(c.R.Context(), id)
		if err != nil {
			return apikit.Reply{}, signalsErr(err)
		}
		return apikit.Reply{Data: wire.SignalRow{CompanyID: apikit.Safe64(id), Company: co.Name, Tier: wire.SignalCold, State: co.State, Sources: []string{}}}, nil
	}
}

func (s *Handlers) EvaluatePostings(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Signals()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "companyId")
	if e != nil {
		return apikit.Reply{}, e
	}
	queued, err := svc.EvaluatePostings(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, signalsErr(err)
	}
	out := wire.SignalEvaluateResult{Queued: make([]wire.SafeInt, len(queued))}
	for i, q := range queued {
		out.Queued[i] = apikit.Safe64(q)
	}
	s.Notify("scans")
	return apikit.Reply{Data: out}, nil
}

// Rescore recomputes every tier under the current policy. It makes no
// model or network call.
func (s *Handlers) Rescore(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Signals()
	if err != nil {
		return apikit.Reply{}, err
	}
	if err := svc.Rescore(c.R.Context()); err != nil {
		return apikit.Reply{}, err
	}
	tiers, err := svc.Repo().Tiers(c.R.Context())
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.SignalRescoreResult{Companies: apikit.Safe(len(tiers)), Tiers: map[string]wire.SafeInt{}}
	for _, t := range tiers {
		out.Tiers[t.Tier]++
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Handlers) Status(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Signals()
	if err != nil {
		return apikit.Reply{}, err
	}
	st, err := svc.Status(c.R.Context())
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: WireSignalStatus(st)}, nil
}

func WireSignalStatus(st signals.Status) wire.SignalStatus {
	out := wire.SignalStatus{Tiers: apikit.SafeMap(st.Tiers), ReviewOpen: apikit.Safe(st.ReviewOpen), Lanes: []wire.SignalLane{},
		Outcomes: wire.SignalOutcomes{Hits: apikit.Safe(st.Outcomes.Hits), Misses: apikit.Safe(st.Outcomes.Misses), Pending: apikit.Safe(st.Outcomes.Pending), HitRate: st.Outcomes.HitRate,
			MedianLead: st.Outcomes.MedianLead, ByType: []wire.SignalTypeHit{}}}
	for _, typ := range []string{signals.TypeFunding, signals.TypeExpansion, signals.TypeProduct, signals.TypeLeadership, signals.TypeOther} {
		if h, ok := st.Outcomes.ByType[typ]; ok {
			out.Outcomes.ByType = append(out.Outcomes.ByType, wire.SignalTypeHit{Type: typ, Hits: apikit.Safe(h.Hits), Misses: apikit.Safe(h.Misses), Pending: apikit.Safe(h.Pending), HitRate: h.HitRate})
		}
	}
	if st.Active != 0 {
		a := apikit.Safe64(st.Active)
		out.ActiveTick = &a
	}
	for _, l := range st.Lanes {
		lane := wire.SignalLane{Source: l.Source, Title: signalTitle(l.Source), Enabled: l.Enabled, LastProduced: apikit.TimePtr(l.LastProduced), LastStatus: l.LastStatus}
		if l.Source == signals.SourceEdgar {
			lane.Disclosure = signals.EdgarDisclosure
		}
		out.Lanes = append(out.Lanes, lane)
	}
	if st.LastTick != nil {
		sum := WireSignalTick(*st.LastTick)
		out.LastTick = &sum
	}
	return out
}

// signalTitle is the name shown for a signal source; an id it does not know
// is shown as it is.
func signalTitle(id string) string {
	if t, ok := signals.SourceTitles[id]; ok {
		return t
	}
	return id
}

func WireSignalTick(t signals.TickSummary) wire.SignalTickSummary {
	sum := wire.SignalTickSummary{TickID: apikit.Safe64(t.TickID), Status: t.Status, StartedAt: wire.Time{Time: t.StartedAt}, Limited: t.Limited, Ingested: apikit.Safe(t.Ingested),
		Events: apikit.Safe(t.Events), Assessed: apikit.Safe(t.Assess.OK), Held: apikit.Safe(t.Assess.QuoteFailed + t.Assess.MidConfidence + t.Assess.Contradictory + t.Assess.SchemaInvalid),
		TierChanges: apikit.Safe(len(t.TierChanges)), Handoffs: apikit.Safe(t.Handoff.Inserted),
		BudgetReached: t.BudgetReached, Notes: t.Notes, Error: t.Error, Sources: []wire.SignalSourceResult{}, Alarms: []wire.SignalAlarm{}}
	for _, r := range t.Sources {
		sum.Sources = append(sum.Sources, wire.SignalSourceResult{ID: r.ID, Status: r.Status, Reason: r.Reason, Docs: apikit.Safe(r.Docs), New: apikit.Safe(r.New), Deferred: r.Deferred})
	}
	for _, a := range t.Alarms {
		sum.Alarms = append(sum.Alarms, wire.SignalAlarm{Source: a.Source, Title: signalTitle(a.Source), Status: a.Status, LastProduced: wire.Time{Time: a.LastProduced}, RequiredAction: a.RequiredAction, Retryable: a.Retryable})
	}
	return sum
}
