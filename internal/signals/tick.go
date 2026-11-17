package signals

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// staleTick is how long a running tick may go without a heartbeat before a new
// one takes over its claim: the budget plus a margin.
const staleTick = TickBudget + 10*time.Minute

const tickFinishTimeout = 10 * time.Second

var errTickBudget = errors.New("signals: tick budget reached")

// tickReserves are the ends of the budget that earlier stages leave to later
// ones: identification and assessment stop a quarter of the budget early, for
// the posting check and the hand-over, and the check stops a twentieth early,
// for the hand-over and the notices. Either model stage alone can take the
// whole budget, and then no company was ever checked or handed off.
func tickReserves(budget time.Duration) (check, handoff time.Duration) {
	return budget / 4, budget / 20
}

// shareSpent reports whether err only says that a stage's share of the budget
// ran out: the stage's context ended for the budget and the tick's did not.
func shareSpent(tick, stage context.Context, err error) bool {
	return tick.Err() == nil && errors.Is(context.Cause(stage), errTickBudget) && tickDeadlineOnly(err)
}

// SourceResult is one collector's line in the tick summary.
type SourceResult struct {
	ID       string   `json:"id"`
	Status   string   `json:"status"`
	Reason   string   `json:"reason,omitempty"`
	Docs     int      `json:"docs"`
	New      int      `json:"new"`
	Deferred []string `json:"deferred,omitempty"`
	Notes    []string `json:"notes,omitempty"`
}

// TickSummary is the record of one tick. It is filled in place as the tick
// goes, so a tick that fails keeps the counters it reached.
type TickSummary struct {
	TickID        int64          `json:"tickId"`
	RunID         string         `json:"runId,omitempty"`
	Status        string         `json:"status"`
	StartedAt     time.Time      `json:"startedAt"`
	FinishedAt    time.Time      `json:"finishedAt,omitempty"`
	Limited       bool           `json:"limited,omitempty"`
	Sources       []SourceResult `json:"sources"`
	Ingested      int            `json:"ingested"`
	Identified    int            `json:"identified"`
	Events        int            `json:"events"`
	GeoFiltered   int            `json:"geoFiltered"`
	Assess        AssessStats    `json:"assessments"`
	Notes         []string       `json:"notes,omitempty"`
	TierChanges   []TierChange   `json:"tierChanges"`
	Check         CheckStats     `json:"scans"`
	Handoff       HandoffStats   `json:"handoffs"`
	Notified      NotifyStats    `json:"notified"`
	Alarms        []Alarm        `json:"alarms"`
	BudgetReached bool           `json:"budgetReached,omitempty"`
	Error         string         `json:"error,omitempty"`
}

// TickRequest starts a tick.
type TickRequest struct {
	// Sources limits the tick to the named collectors; a limited tick collects
	// and ingests only.
	Sources []string
	// Days makes the EDGAR collector read the past N business days.
	Days int
	// RunID is the run record of the caller, when it has one.
	RunID    string
	Progress func(string)
}

// ActiveError is the refusal of a second tick.
type ActiveError struct{ TickID int64 }

func (e *ActiveError) Error() string {
	return "a tick is already running: " + strconv.FormatInt(e.TickID, 10)
}

// ParseSources validates a comma-separated list of source identifiers.
func ParseSources(list string) ([]string, error) {
	var out []string
	for _, s := range strings.Split(list, ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		if !slices.Contains(SourceIDs, s) {
			return nil, fmt.Errorf("unknown signal source %q; the sources are %s", s, strings.Join(SourceIDs, ", "))
		}
		out = append(out, s)
	}
	return out, nil
}

func (s *Service) claimTick(ctx context.Context, runID string) (int64, error) {
	var id int64
	err := s.d.Store.Write(ctx, func(tx *sql.Tx) error {
		var cur int64
		var beat string
		switch err := tx.QueryRowContext(ctx, `SELECT id, heartbeat_at FROM signal_ticks WHERE status = 'running'`).Scan(&cur, &beat); {
		case err == nil:
			if s.now().Sub(parseTS(beat)) < staleTick {
				return &ActiveError{TickID: cur}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE signal_ticks SET status = 'failed', finished_at = ?, summary_json = json_set(CASE WHEN json_valid(summary_json) THEN summary_json ELSE '{}' END, '$.status', 'failed', '$.error', 'interrupted') WHERE id = ?`, s.repo.stamp(), cur); err != nil {
				return err
			}
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO signal_ticks (run_id, status, started_at, heartbeat_at) VALUES (?, 'running', ?, ?)`, runID, s.repo.stamp(), s.repo.stamp())
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		return nil
	})
	return id, err
}

func (s *Service) saveTick(ctx context.Context, sum *TickSummary) error {
	b, err := json.Marshal(sum)
	if err != nil {
		return err
	}
	return s.d.Store.Write(ctx, func(tx *sql.Tx) error {
		fin := any(nil)
		if sum.Status != TickRunning {
			fin = s.repo.stamp()
		}
		_, err := tx.ExecContext(ctx, `UPDATE signal_ticks SET status = ?, heartbeat_at = ?, finished_at = ?, summary_json = ? WHERE id = ?`, sum.Status, s.repo.stamp(), fin, string(b), sum.TickID)
		return err
	})
}

// tickDeadlineOnly distinguishes interrupted work from an independent failure
// delivered at the deadline. A joined failure must keep every independent cause.
func tickDeadlineOnly(err error) bool {
	if err == nil {
		return true
	}
	switch e := err.(type) {
	case interface{ Unwrap() []error }:
		for _, cause := range e.Unwrap() {
			if !tickDeadlineOnly(cause) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		return tickDeadlineOnly(e.Unwrap())
	default:
		return err == context.Canceled || err == context.DeadlineExceeded || err == errTickBudget
	}
}

// Tick runs one pass of the pipeline. It holds at most one active instance,
// is bounded by the 20 minute budget, and writes its summary even when a stage
// fails: the summary then carries the counters reached and status failed.
func (s *Service) Tick(ctx context.Context, req TickRequest) (sum TickSummary, err error) {
	id, err := s.claimTick(ctx, req.RunID)
	if err != nil {
		return TickSummary{}, err
	}
	parent := ctx
	budget := s.budget()
	start := s.now()
	tickEnd := start.Add(budget)
	ctx, cancel := s.deadline(ctx, tickEnd)
	defer cancel()
	sum = TickSummary{TickID: id, RunID: req.RunID, Status: TickRunning, StartedAt: start.UTC(), Limited: len(req.Sources) > 0,
		Sources: []SourceResult{}, TierChanges: []TierChange{}, Alarms: []Alarm{}}
	progress := func(line string) {
		if req.Progress != nil {
			req.Progress(line)
		}
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("signals: the tick panicked: %v", r)
		} else if parent.Err() != nil {
			if !errors.Is(err, parent.Err()) {
				err = errors.Join(err, parent.Err())
			}
		} else if errors.Is(context.Cause(ctx), errTickBudget) {
			sum.BudgetReached = true
			if tickDeadlineOnly(err) {
				err = nil
			}
		}
		sum.FinishedAt = s.now().UTC()
		if err != nil {
			sum.Status, sum.Error = TickFailed, err.Error()
		} else {
			sum.Status = TickCompleted
		}
		end, finish := context.WithTimeout(context.WithoutCancel(parent), tickFinishTimeout)
		defer finish()
		if serr := s.saveTick(end, &sum); serr != nil {
			err = errors.Join(err, serr)
			sum.Status, sum.Error = TickFailed, err.Error()
		}
	}()
	for _, src := range req.Sources {
		if !slices.Contains(SourceIDs, src) {
			return sum, fmt.Errorf("unknown signal source %q", src)
		}
	}
	st, err := s.settings(ctx)
	if err != nil {
		return sum, err
	}
	checkReserve, handoffReserve := tickReserves(budget)

	var alarmInputs []alarmInput
	for _, c := range collectors() {
		if len(req.Sources) > 0 && !slices.Contains(req.Sources, c.id) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		res, produced, action := s.runCollector(ctx, c, st, req)
		sum.Sources = append(sum.Sources, res)
		sum.Ingested += res.New
		alarmInputs = append(alarmInputs, alarmInput{res: res, enabled: SourceEnabled(st.Portals, c.id), action: action, produced: produced})
		progress(fmt.Sprintf("%s: %s %d documents (%d new)", c.id, res.Status, res.Docs, res.New))
		if err := s.saveTick(ctx, &sum); err != nil {
			return sum, err
		}
	}
	if sum.Limited {
		for _, in := range alarmInputs {
			if err := s.repo.NoteLane(ctx, in.res.ID, in.produced, laneStatus(in.res)); err != nil {
				return sum, err
			}
		}
		return sum, nil
	}

	geo := NewGeo(st.Profile.Targets.Locations, st.Profile.WorkAuthorization.AuthorizedCountries)
	models, stopModels := s.deadline(ctx, tickEnd.Add(-checkReserve))
	defer stopModels()
	if err := s.modelStages(models, &sum, geo, progress); err != nil {
		if !shareSpent(ctx, models, err) {
			return sum, err
		}
		sum.BudgetReached = true
	}

	if err := s.stage(ctx, "score"); err != nil {
		return sum, err
	}
	if err := s.scoreTiers(ctx, &sum, st); err != nil {
		return sum, err
	}

	if err := s.stage(ctx, "check"); err != nil {
		return sum, err
	}
	checking, stopChecking := s.deadline(ctx, tickEnd.Add(-handoffReserve))
	defer stopChecking()
	cs, err := s.checkDue(checking, id, st, tickEnd.Add(-handoffReserve))
	sum.Check = cs
	if err != nil && !shareSpent(ctx, checking, err) {
		return sum, err
	}
	sum.BudgetReached = sum.BudgetReached || cs.BudgetReached || err != nil
	progress(fmt.Sprintf("checked %d companies, %d deferred", cs.Scanned, cs.Deferred))

	if err := s.stage(ctx, "handoff"); err != nil {
		return sum, err
	}
	hs, err := s.handoffPostings(ctx, st)
	sum.Handoff = hs
	if err != nil {
		return sum, err
	}
	if err := s.repo.UpdateOutcomes(ctx); err != nil {
		return sum, err
	}

	for _, in := range alarmInputs {
		a, err := s.laneAlarm(ctx, in.res, in.enabled, in.action)
		if err != nil {
			return sum, err
		}
		if a != nil {
			sum.Alarms = append(sum.Alarms, *a)
			if err := s.repo.Audit(ctx, "alarm", 0, a.Source+": "+a.Status); err != nil {
				return sum, err
			}
		}
		if err := s.repo.NoteLane(ctx, in.res.ID, in.produced, laneStatus(in.res)); err != nil {
			return sum, err
		}
	}

	if err := s.stage(ctx, "notify"); err != nil {
		return sum, err
	}
	n, err := s.notifyHot(ctx)
	sum.Notified.Hot = n
	if err != nil {
		return sum, err
	}
	for _, a := range sum.Alarms {
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		if s.d.Notifier == nil {
			break
		}
		if err := s.d.Notifier.NeedsYou(ctx, Alert{Key: "signal-alarm|" + a.Source + "|" + s.now().UTC().Format("2006-01-02"), Title: "Signal source " + a.Source + " went quiet",
			Detail: strings.TrimSpace(a.Status + " " + a.RequiredAction), Link: "/leads?tab=signals"}); err != nil {
			s.log.Warn("a lane alarm could not be raised", "source", a.Source, "error", err.Error())
		}
	}
	if sum.Notified.Digest, err = s.sendDigest(ctx, sum.Alarms); err != nil {
		s.log.Warn("the daily digest could not be sent", "error", err.Error())
		err = nil
	}
	return sum, nil
}

func nonNilChanges(c []TierChange) []TierChange {
	if c == nil {
		return []TierChange{}
	}
	return c
}

type alarmInput struct {
	res      SourceResult
	enabled  bool
	action   string
	produced bool
}

func laneStatus(r SourceResult) string {
	if r.Reason != "" {
		return r.Status + ": " + r.Reason
	}
	return r.Status
}

// stage is the seam tests use to fail a stage; in production it checks the
// context.
// scoreTiers re-tiers the companies from their stored assessments and audits
// each change. It reads and writes the database only.
func (s *Service) scoreTiers(ctx context.Context, sum *TickSummary, st Settings) error {
	changes, err := s.rescore(ctx, st)
	sum.TierChanges = nonNilChanges(changes)
	return err
}

// rescore recomputes every company's momentum and tier from stored events and
// assessments under the current policy and audits each tier change. It makes
// no model or network call. The changes come back with an audit failure too.
func (s *Service) rescore(ctx context.Context, st Settings) ([]TierChange, error) {
	var titles []string
	for _, r := range st.Profile.Targets.Roles {
		titles = append(titles, r.Title)
	}
	changes, err := s.repo.Rescore(ctx, s.policy, roleKeywords(titles))
	if err != nil {
		return nil, err
	}
	for _, c := range changes {
		if err := s.repo.Audit(ctx, "tier-change", c.CompanyID, fmt.Sprintf("%s: %s -> %s (%.2f)", c.Company, c.From, c.To, c.Momentum)); err != nil {
			return changes, err
		}
	}
	return changes, nil
}

// modelStages identifies the pending documents and assesses the pending
// events, the two stages that call a model.
func (s *Service) modelStages(ctx context.Context, sum *TickSummary, geo *Geo, progress func(string)) error {
	if err := s.stage(ctx, "identify"); err != nil {
		return err
	}
	ids, err := s.identifyPending(ctx, geo)
	sum.Identified, sum.Events, sum.GeoFiltered = ids.Identified, ids.Events, ids.GeoDropped
	if ids.Skipped != "" {
		sum.Notes = append(sum.Notes, ids.Skipped)
	}
	if err != nil {
		return err
	}
	progress(fmt.Sprintf("identified %d documents, %d events", ids.Identified, ids.Events))
	if err := s.stage(ctx, "assess"); err != nil {
		return err
	}
	as, err := s.assessPending(ctx)
	sum.Assess = as
	if as.Skipped != "" {
		sum.Notes = append(sum.Notes, as.Skipped)
	}
	if err != nil {
		return err
	}
	progress(fmt.Sprintf("assessed: %d ok, %d held", as.OK, as.QuoteFailed+as.MidConfidence+as.Contradictory+as.SchemaInvalid))
	return s.saveTick(ctx, sum)
}

func (s *Service) stage(ctx context.Context, name string) error {
	if s.hook != nil {
		if err := s.hook(name); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// runCollector runs one collector with failure isolation: a collector that
// fails, panics or times out is recorded with its reason and nothing else
// stops.
func (s *Service) runCollector(ctx context.Context, c collector, st Settings, req TickRequest) (res SourceResult, produced bool, action string) {
	res = SourceResult{ID: c.id, Status: SrcOK}
	if !SourceEnabled(st.Portals, c.id) {
		res.Status = SrcDisabled
		return res, false, ""
	}
	env := &collectEnv{Fetch: s.fetch, Repo: s.repo, Now: s.now, Endpoints: s.d.Endpoints, Secret: s.d.Secret, Profile: st.Profile,
		Queries: st.Portals.Sources[c.id].Queries, Search: s.searchProvider()}
	if c.id == SourceEdgar {
		env.Days = req.Days
	}
	red := s.redactor(st)
	var out Collected
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("the collector panicked: %v", r)
			}
		}()
		out, err = c.run(ctx, env)
	}()
	var skip *SkipError
	var ce *CollectError
	switch {
	case errors.As(err, &skip):
		res.Status, res.Reason = SrcSkipped, skip.Reason
		return res, false, ""
	case errors.As(err, &ce):
		res.Status, res.Reason, action = SrcFailed, red(ce.Reason), ce.RequiredAction
		return res, false, action
	case err != nil:
		res.Status, res.Reason = SrcFailed, red(err.Error())
		return res, false, ""
	}
	res.Docs, res.Deferred = len(out.Docs), out.Deferred
	for _, n := range out.Notes {
		res.Notes = append(res.Notes, red(n))
	}
	for _, d := range out.Docs {
		_, created, err := s.repo.InsertDocument(ctx, d)
		if err != nil {
			res.Status = SrcFailed
			if res.Reason == "" {
				res.Reason = "documents could not be stored: " + red(err.Error())
			}
			res.Notes = append(res.Notes, "a document was not stored: "+red(err.Error()))
			continue
		}
		if created {
			res.New++
		}
	}
	// Keep all checkpoints unchanged until every emitted document is durable.
	// Previously stored rows deduplicate when the collector retries the batch.
	if res.Status != SrcFailed && out.Commit != nil {
		if err := out.Commit(ctx); err != nil {
			res.Status, res.Reason = SrcFailed, "the cursor could not be saved: "+red(err.Error())
		}
	}
	return res, res.Docs > 0, ""
}

// redactor keeps a secret out of the summary: the contact email and the keys
// of the keyed sources never appear in a reason or a note.
func (s *Service) redactor(st Settings) func(string) string {
	secrets := []string{st.Profile.Candidate.Email, s.d.Secret(KeyX), s.d.Secret(KeyExa)}
	return func(text string) string {
		for _, sec := range secrets {
			if sec = strings.TrimSpace(sec); len(sec) >= 4 {
				text = strings.ReplaceAll(text, sec, "[redacted]")
			}
		}
		return capRunes(strings.TrimSpace(text), 500)
	}
}

// Line is the one-line summary of a tick, for the scheduler run and the
// command line.
func (t TickSummary) Line() string {
	line := fmt.Sprintf("%d new documents, %d events, %d assessed, %d scanned, %d handed off, %d notified",
		t.Ingested, t.Events, t.Assess.OK, t.Check.Scanned, t.Handoff.Inserted, t.Notified.Hot)
	if t.Limited {
		line = fmt.Sprintf("%d new documents from %d sources", t.Ingested, len(t.Sources))
	}
	failed := 0
	for _, src := range t.Sources {
		if src.Status == SrcFailed {
			failed++
		}
	}
	if failed > 0 {
		line += fmt.Sprintf(", %d failed sources", failed)
	}
	if n := len(t.Alarms); n > 0 {
		line += fmt.Sprintf(", %d alarms", n)
	}
	if t.BudgetReached {
		line += ", budget-reached"
	}
	return line
}

// SweepOrphans closes the tick record of a stopped process: the server calls it
// at start, when no tick of this process can be running.
func (s *Service) SweepOrphans(ctx context.Context) (int, error) {
	n := 0
	err := s.d.Store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE signal_ticks SET status = 'failed', finished_at = ?,
			summary_json = json_set(CASE WHEN json_valid(summary_json) THEN summary_json ELSE '{}' END, '$.status', 'failed', '$.error', 'interrupted') WHERE status = 'running'`, s.repo.stamp())
		if err != nil {
			return err
		}
		c, _ := res.RowsAffected()
		n = int(c)
		return nil
	})
	return n, err
}
