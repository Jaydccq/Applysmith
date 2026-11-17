package tracker

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"applysmith/internal/identity"
)

// MinCohort is the number of applications with event data below which a group
// is too small to compare.
const MinCohort = 10

// OutcomeQuery selects a cohort by application date and fixes the day the
// history is read as of.
type OutcomeQuery struct {
	// From and To bound the application date, inclusive; empty is unbounded.
	From, To string
	// AsOf is the observation cutoff, an ISO date: events after that day are
	// ignored. Empty is today.
	AsOf string
}

// StageReach is one rate: how many of the applications with event data reached
// a stage by the cutoff.
type StageReach struct {
	Stage       Status `json:"stage"`
	Numerator   int    `json:"numerator"`
	Denominator int    `json:"denominator"`
	// Open counts applications that have not reached the stage and are still
	// open, so they could yet; Unknown counts the cohort's applications whose
	// events cannot say.
	Open    int `json:"open"`
	Unknown int `json:"unknown"`
}

// OutcomeGroup is the summary of one slice of the cohort.
type OutcomeGroup struct {
	// Key is the source channel or role family; empty means not recorded.
	Key          string       `json:"key"`
	Applications int          `json:"applications"`
	Known        int          `json:"known"`
	Unknown      int          `json:"unknown"`
	Open         int          `json:"open"`
	Closed       int          `json:"closed"`
	Stages       []StageReach `json:"stages"`
	// Insufficient lists why the group cannot support a comparison; empty when
	// it can.
	Insufficient []string `json:"insufficient"`
}

// Outcomes is the read-only outcome summary of a cohort.
type Outcomes struct {
	From         string         `json:"from"`
	To           string         `json:"to"`
	AsOf         string         `json:"as_of"`
	Overall      OutcomeGroup   `json:"overall"`
	BySource     []OutcomeGroup `json:"by_source"`
	ByRoleFamily []OutcomeGroup `json:"by_role_family"`
	// Folded counts rows that duplicate another row of the same posting.
	Folded int `json:"folded"`
	// NoApplicationDate counts submitted rows with no application date, which
	// no cohort can hold.
	NoApplicationDate int `json:"no_application_date"`
}

// reachStages are the stages a rate is reported for. Responded counts any
// reply from the employer, a rejection included; Rejected counts the
// rejections alone.
var reachStages = []Status{Responded, Interview, Offer, Rejected}

// reachesStage reports whether a set of reached statuses includes the stage;
// an interview or an offer implies the earlier reply, and any decision from
// the employer counts as hearing back.
func reachesStage(reached map[Status]bool, stage Status) bool {
	if stage == Responded {
		return reached[Responded] || reached[Interview] || reached[Offer] || reached[Rejected]
	}
	return reached[stage]
}

// candidacy is one application after duplicate rows are folded together.
type candidacy struct {
	appliedAt string
	source    string
	family    string
	reached   map[Status]bool
	hasEvents bool
	final     Status
	finalAt   string
	finalID   int64
}

func (c *candidacy) known() bool {
	for s := range c.reached {
		if s.Submitted() {
			return true
		}
	}
	return false
}

func (c *candidacy) closed() bool { return c.final == Rejected || c.final == Discarded }

// Outcomes summarizes the applications of a cohort from the status events of
// each row. It reads only: nothing is written and nothing outside the tracker
// tables is consulted except the role family a report recorded.
func (t *Tracker) Outcomes(ctx context.Context, q OutcomeQuery) (Outcomes, error) {
	if q.AsOf == "" {
		q.AsOf = t.today()
	}
	for name, d := range map[string]string{"from": q.From, "to": q.To, "as of": q.AsOf} {
		if d != "" && !validDate(d) {
			return Outcomes{}, fmt.Errorf("%w: %s %q is not an ISO date", ErrInvalid, name, d)
		}
	}
	if q.From != "" && q.To != "" && q.From > q.To {
		return Outcomes{}, fmt.Errorf("%w: from is after to", ErrInvalid)
	}
	tx, err := t.s.Read().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Outcomes{}, err
	}
	defer tx.Rollback()
	cands, all, err := t.loadHistory(ctx, tx, t.dayEnd(q.AsOf))
	if err != nil {
		return Outcomes{}, err
	}
	out := Outcomes{From: q.From, To: q.To, AsOf: q.AsOf}
	var cohort []*candidacy
	for _, c := range cands {
		if c.appliedAt == "" {
			if c.known() {
				out.NoApplicationDate++
			}
			continue
		}
		if c.appliedAt > q.AsOf || (q.From != "" && c.appliedAt < q.From) || (q.To != "" && c.appliedAt > q.To) {
			continue
		}
		// Events that show no submission mean the row is not an application
		// (a correction withdrew it); no events at all means the history is
		// missing, which is reported, not rebuilt.
		if c.hasEvents && !c.known() {
			continue
		}
		cohort = append(cohort, c)
	}
	out.Folded = all - len(cands)
	out.Overall = summarize("", cohort)
	out.BySource = groupBy(cohort, func(c *candidacy) string { return c.source })
	out.ByRoleFamily = groupBy(cohort, func(c *candidacy) string { return c.family })
	return out, nil
}

// loadHistory reads every row with its status events stamped before cutoff (a
// UTC RFC 3339 instant) and folds rows of one posting into one candidacy. It
// returns the candidacies and the number of rows they came from.
func (t *Tracker) loadHistory(ctx context.Context, q querier, cutoff string) ([]*candidacy, int, error) {
	rows, err := queryEntries(ctx, q, `SELECT `+entryColumns+` FROM tracker_entries ORDER BY id`)
	if err != nil {
		return nil, 0, err
	}
	cands, err := loadCandidates(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	families, err := roleFamilies(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	events, err := statusEvents(ctx, q, cutoff)
	if err != nil {
		return nil, 0, err
	}
	byID := make(map[int64]Entry, len(rows))
	for _, e := range rows {
		byID[e.ID] = e
	}
	var out []*candidacy
	for _, cl := range clusterRows(cands, func(a, b candidate) bool { return identity.SameForDedup(a.posting(), b.posting()) }) {
		c := &candidacy{reached: map[Status]bool{}}
		for _, id := range cl.ids() {
			e := byID[id]
			if e.AppliedAt != "" && (c.appliedAt == "" || e.AppliedAt < c.appliedAt) {
				c.appliedAt = e.AppliedAt
			}
			if c.source == "" {
				c.source = strings.TrimSpace(e.Source)
			}
			if c.family == "" {
				c.family = families[id]
			}
			c.fold(events[id])
		}
		out = append(out, c)
	}
	return out, len(rows), nil
}

// statusEvent is the part of an event the history reads.
type statusEvent struct {
	id         int64
	at         string
	corrected  bool
	prev, next Status
}

// fold adds one row's events, oldest first. A correction withdraws the status
// it replaced, because the user said it was wrong; every other status a row
// ever held stays reached, so an interview followed by a rejection counts as
// both.
func (c *candidacy) fold(events []statusEvent) {
	reached := map[Status]bool{}
	for _, ev := range events {
		if ev.corrected && ev.prev != "" {
			delete(reached, ev.prev)
		}
		reached[ev.next] = true
		c.hasEvents = true
		if ev.at > c.finalAt || (ev.at == c.finalAt && ev.id > c.finalID) {
			c.final, c.finalAt, c.finalID = ev.next, ev.at, ev.id
		}
	}
	for s := range reached {
		c.reached[s] = true
	}
}

func statusEvents(ctx context.Context, q querier, cutoff string) (map[int64][]statusEvent, error) {
	rows, err := q.QueryContext(ctx, `SELECT entry_id, id, at, kind, previous_status, new_status FROM tracker_events
		WHERE kind IN ('created', 'status_changed', 'status_corrected') AND new_status IS NOT NULL AND at < ? ORDER BY at, id`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]statusEvent{}
	for rows.Next() {
		var entry int64
		var ev statusEvent
		var kind string
		var prev sql.NullString
		if err := rows.Scan(&entry, &ev.id, &ev.at, &kind, &prev, &ev.next); err != nil {
			return nil, err
		}
		ev.corrected, ev.prev = kind == string(EventStatusCorrected), Status(prev.String)
		out[entry] = append(out[entry], ev)
	}
	return out, rows.Err()
}

// roleFamilies maps a row to the role family its report recorded; rows with no
// report, or a report that named none, are absent.
func roleFamilies(ctx context.Context, q querier) (map[int64]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT t.id, r.role_family FROM tracker_entries t JOIN report_index r ON r.report_number = t.report_number WHERE r.role_family <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var family string
		if err := rows.Scan(&id, &family); err != nil {
			return nil, err
		}
		out[id] = strings.TrimSpace(family)
	}
	return out, rows.Err()
}

func groupBy(cohort []*candidacy, key func(*candidacy) string) []OutcomeGroup {
	buckets := map[string][]*candidacy{}
	for _, c := range cohort {
		k := key(c)
		buckets[k] = append(buckets[k], c)
	}
	out := make([]OutcomeGroup, 0, len(buckets))
	for k, members := range buckets {
		out = append(out, summarize(k, members))
	}
	slices.SortFunc(out, func(a, b OutcomeGroup) int {
		if a.Applications != b.Applications {
			return b.Applications - a.Applications
		}
		return strings.Compare(a.Key, b.Key)
	})
	return out
}

func summarize(key string, members []*candidacy) OutcomeGroup {
	g := OutcomeGroup{Key: key, Applications: len(members), Stages: make([]StageReach, len(reachStages)), Insufficient: []string{}}
	for i, s := range reachStages {
		g.Stages[i].Stage = s
	}
	for _, c := range members {
		if !c.known() {
			g.Unknown++
			continue
		}
		g.Known++
		if c.closed() {
			g.Closed++
		} else {
			g.Open++
		}
		for i, s := range reachStages {
			switch {
			case reachesStage(c.reached, s):
				g.Stages[i].Numerator++
			case !c.closed():
				g.Stages[i].Open++
			}
		}
	}
	for i := range g.Stages {
		g.Stages[i].Denominator, g.Stages[i].Unknown = g.Known, g.Unknown
	}
	if g.Known < MinCohort {
		g.Insufficient = append(g.Insufficient, fmt.Sprintf("fewer than %d applications with event data", MinCohort))
	}
	if g.Known > 0 && g.Open*2 > g.Known {
		g.Insufficient = append(g.Insufficient, "more than half of the applications are still open")
	}
	return g
}
