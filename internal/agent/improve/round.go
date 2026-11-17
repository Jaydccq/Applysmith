package improve

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"applysmith/internal/agent"
	"applysmith/internal/agent/harness"
	"applysmith/internal/agent/sessions"
	"applysmith/internal/llm"
	"applysmith/internal/store"
)

// Model tasks of a round.
const (
	TaskImprove = "agent-improve"
	TaskJudge   = "agent-judge"
)

// Bounds of the rounds.
const (
	baseRuns        = 2
	maxCandidates   = 2
	maxProbesPerDay = 30
	maxRoundsPerDay = 3
	maxLessons      = 40
)

// Model is the part of the model router a round uses.
type Model interface {
	Structured(ctx context.Context, req llm.Request, schema json.RawMessage, out any) (*llm.Response, error)
	Resolve(ctx context.Context, taskID string) (llm.Resolved, error)
}

// Replayer replays a recorded turn under a harness version; Prober is the
// product's.
type Replayer interface {
	Probe(ctx context.Context, rec Recorded, h harness.Harness) (Trajectory, error)
}

// Service runs rounds, one at a time.
type Service struct {
	DB       *store.Store
	Sessions *sessions.Store
	Harness  *harness.Store
	Replayer Replayer
	// Registry is the product's catalog, whose tool names a version may note.
	Registry *agent.Registry
	// Router resolves the agent's model, which the judge must differ from.
	Router  agent.Router
	Model   Model
	Now     func() time.Time
	Log     *slog.Logger
	mu      sync.Mutex
	running atomic.Bool
	bgOnce  sync.Once
	bg      context.Context
	stop    context.CancelFunc
	wg      sync.WaitGroup
}

// Start runs a round in the background, for the dashboard's request; the
// result is the round's row.
func (s *Service) Start() error {
	s.bgOnce.Do(func() { s.bg, s.stop = context.WithCancel(context.Background()) })
	if s.bg.Err() != nil {
		return errors.New("improve: the service is closed")
	}
	if s.running.Load() {
		return ErrBusy
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if out, err := s.Round(s.bg); err != nil {
			s.log().Warn("improve: round failed", "error", err.Error())
		} else {
			s.log().Info("improve: round", "result", out)
		}
	}()
	return nil
}

// Running reports whether a round is running.
func (s *Service) Running() bool { return s.running.Load() }

// Close cancels a background round and waits for it.
func (s *Service) Close() {
	s.bgOnce.Do(func() { s.bg, s.stop = context.WithCancel(context.Background()) })
	s.stop()
	s.wg.Wait()
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.Log
}

// ErrBusy means a round is already running.
var ErrBusy = errors.New("improve: a round is already running")

// roundRecord is the evidence a round keeps beside its outcome.
type roundRecord struct {
	Targets    []targetRecord   `json:"targets"`
	Suite      []string         `json:"suite"`
	Candidates []candidateEvent `json:"candidates"`
	Proposer   string           `json:"proposer,omitempty"`
	// Reflection is the reflector's verdict on each edit and whether it kept
	// a lesson.
	Reflection *reflection `json:"reflection,omitempty"`
	CostUSD    float64     `json:"costUsd"`
}

type targetRecord struct {
	TurnID  string       `json:"turnId"`
	Signals string       `json:"signals"`
	Bases   []Trajectory `json:"bases"`
}

type candidateEvent struct {
	Summary  string            `json:"summary"`
	Guidance []string          `json:"guidance"`
	Notes    map[string]string `json:"toolNotes,omitempty"`
	Edits    []string          `json:"edits"`
	// Failed names the hard check a probe failed, empty when all passed.
	Failed   string       `json:"failed,omitempty"`
	Probes   []Trajectory `json:"probes"`
	Verdicts []verdict    `json:"verdicts"`
	Wins     int          `json:"targetWins"`
	Losses   int          `json:"targetLosses"`
	SuiteBad int          `json:"suiteLosses"`
	Adopted  bool         `json:"adopted"`
}

type verdict struct {
	TurnID  string `json:"turnId"`
	Suite   bool   `json:"suite"`
	Outcome string `json:"outcome"` // better, same or worse than the active version
	Why     string `json:"why"`
}

// Round runs one round and returns a one-line summary, the result line of the
// scheduled run.
func (s *Service) Round(ctx context.Context) (string, error) {
	if !s.mu.TryLock() {
		return "", ErrBusy
	}
	defer s.mu.Unlock()
	s.running.Store(true)
	defer s.running.Store(false)
	set, err := s.Sessions.Settings(ctx)
	if err != nil {
		return "", err
	}
	if !set.SelfImprove {
		return "switched off", nil
	}
	base, err := s.Harness.Active(ctx)
	if err != nil {
		return "", err
	}
	rounds, probes, err := s.today(ctx)
	if err != nil {
		return "", err
	}
	if rounds >= maxRoundsPerDay {
		return s.skip(ctx, base.Version, fmt.Sprintf("%d rounds ran in the last 24 hours", rounds))
	}
	budget := maxProbesPerDay - probes
	if same, why := s.sameModel(ctx); same {
		return s.skip(ctx, base.Version, "skipped: "+why)
	}
	cands, suiteC, err := s.selectTurns(ctx, base.Version)
	if err != nil {
		return "", err
	}
	if len(cands) == 0 {
		return s.skip(ctx, base.Version, "nothing to improve: no recent turn went wrong")
	}
	cands, suiteC = fit(cands, suiteC, budget)
	if len(cands) == 0 {
		return s.skip(ctx, base.Version, fmt.Sprintf("skipped: %d of the day's %d probes are left, too few for a round", max(budget, 0), maxProbesPerDay))
	}
	r := &round{s: s, id: store.NewID("round"), base: base, budget: budget}
	if err := s.startRound(ctx, r); err != nil {
		return "", err
	}
	outcome, detail, adopted, err := r.run(ctx, cands, suiteC)
	if err != nil {
		outcome, detail = "failed", clip(err.Error(), 300)
	}
	if ferr := s.finishRound(context.WithoutCancel(ctx), r, outcome, detail, adopted); ferr != nil {
		return "", errors.Join(err, ferr)
	}
	return outcome + ": " + detail, err
}

// fit trims a round to the probes it may spend: the base replays of the
// targets, the suite once under the active version and every candidate on
// every turn. The suite keeps at least one turn when it had one, so no round
// adopts without a regression check because the budget ran short.
func fit(targets, suite []*candidate, budget int) ([]*candidate, []*candidate) {
	need := func(t, s int) int { return t*baseRuns + s + maxCandidates*(t+s) }
	minSuite := min(len(suite), 1)
	for need(len(targets), len(suite)) > budget && len(suite) > minSuite {
		suite = suite[:len(suite)-1]
	}
	for need(len(targets), len(suite)) > budget && len(targets) > 0 {
		targets = targets[:len(targets)-1]
	}
	return targets, suite
}

// sameModel reports whether the judge would grade the agent with its own
// model, which the original project measured to miss its own failures.
func (s *Service) sameModel(ctx context.Context) (bool, string) {
	judge, err := s.Model.Resolve(ctx, TaskJudge)
	if err != nil {
		return true, "the judge has no model route: " + err.Error()
	}
	kernel, err := s.Router.AgentRoute(ctx, llm.AgentTask)
	if err != nil {
		return true, "the agent has no model route: " + err.Error()
	}
	if judge.Provider == kernel.Provider && judge.Model == kernel.Model {
		return true, fmt.Sprintf("the judge and the agent both use %s/%s; route %s to another model", judge.Provider, judge.Model, TaskJudge)
	}
	return false, ""
}

type round struct {
	s       *Service
	id      string
	base    harness.Harness
	budget  int
	probes  int
	record  roundRecord
	tools   []string
	catalog string
}

var errBudget = errors.New("the day's probe budget is spent")

func (r *round) probe(ctx context.Context, rec Recorded, h harness.Harness) (Trajectory, error) {
	if r.probes >= r.budget {
		return Trajectory{}, errBudget
	}
	r.probes++
	tr, err := r.s.Replayer.Probe(ctx, rec, h)
	r.record.CostUSD += tr.CostUSD
	return tr, err
}

func (r *round) run(ctx context.Context, cands, suiteC []*candidate) (outcome, detail string, adopted int64, err error) {
	s := r.s
	for _, t := range s.Registry.Tools() {
		if t.Offered() {
			r.tools = append(r.tools, t.Name)
			r.catalog += t.Name + ": " + clip(t.Description, 300) + "\n"
		}
	}
	var targets []target
	for _, c := range cands {
		rec, err := Load(ctx, s.DB, s.Sessions, c.turnID)
		if err != nil {
			s.log().Warn("improve: target not loaded", "turn", c.turnID, "error", err.Error())
			continue
		}
		t := target{c: c, rec: rec}
		for range baseRuns {
			tr, err := r.probe(ctx, rec, r.base)
			if err != nil {
				return "", "", 0, fmt.Errorf("base replay of %s: %w", c.turnID, err)
			}
			t.bases = append(t.bases, tr)
		}
		targets = append(targets, t)
		r.record.Targets = append(r.record.Targets, targetRecord{TurnID: c.turnID, Signals: c.signalText(), Bases: t.bases})
	}
	if len(targets) == 0 {
		return "kept", "no target could be replayed", 0, nil
	}
	lessons, err := s.Lessons(ctx)
	if err != nil {
		return "", "", 0, err
	}
	props, err := r.propose(ctx, targets, lessons)
	if err != nil {
		return "", "", 0, err
	}
	// A target is examined once the proposer has read it: a round whose
	// proposal failed leaves its targets for the next one. Live, two rounds
	// that ended in the proposer's bad request had spent three targets.
	if err := s.markExamined(ctx, r, targets); err != nil {
		return "", "", 0, err
	}
	if len(props) == 0 {
		return "kept", "the proposer kept the current harness", 0, nil
	}
	var suite []Recorded
	var suiteBase []Trajectory
	for _, c := range suiteC {
		rec, err := Load(ctx, s.DB, s.Sessions, c.turnID)
		if err != nil {
			continue
		}
		tr, err := r.probe(ctx, rec, r.base)
		if err != nil {
			if errors.Is(err, errBudget) {
				break
			}
			return "", "", 0, err
		}
		suite, suiteBase = append(suite, rec), append(suiteBase, tr)
		r.record.Suite = append(r.record.Suite, rec.TurnID)
	}
	for i := range props {
		if err := r.evaluate(ctx, &props[i], targets, suite, suiteBase); err != nil {
			return "", "", 0, err
		}
	}
	best := -1
	for i, p := range props {
		ok := p.ev.Failed == "" && p.ev.Wins > p.ev.Losses && p.ev.SuiteBad == 0
		if ok && (best < 0 || p.ev.Wins-p.ev.Losses > props[best].ev.Wins-props[best].ev.Losses) {
			best = i
		}
	}
	for _, p := range props {
		r.record.Candidates = append(r.record.Candidates, p.ev)
	}
	if best < 0 {
		detail = "no candidate beat the current harness"
		r.reflect(ctx, 0)
		return "kept", detail, 0, nil
	}
	r.record.Candidates[best].Adopted = true
	evidence, _ := json.Marshal(r.record)
	id, err := s.Harness.Activate(ctx, harness.Draft{Harness: props[best].h, Source: harness.SourceImprove, Summary: props[best].ev.Summary,
		Evidence: evidence, RoundID: r.id}, r.base.Version, r.tools)
	if err != nil {
		return "", "", 0, fmt.Errorf("adopt: %w", err)
	}
	r.reflect(ctx, id)
	p := props[best].ev
	return "adopted", fmt.Sprintf("version %d: %s (won %d of %d targets, no suite loss)", id, p.Summary, p.Wins, len(targets)), id, nil
}

type prop struct {
	h  harness.Harness
	ev candidateEvent
}

// propose asks the improve model for up to two candidates and validates them,
// giving a refused set one retry with the validator's message.
func (r *round) propose(ctx context.Context, targets []target, lessons []Lesson) ([]prop, error) {
	prompt := proposePrompt(agent.FixedPrompt(), r.catalog, r.base, targets, lessons)
	var out []prop
	for attempt := 0; attempt < 2; attempt++ {
		var p proposal
		if _, err := r.s.Model.Structured(ctx, llm.Request{Task: TaskImprove, System: proposeSystem, Prompt: prompt, RunID: r.id}, proposeSchema, &p); err != nil {
			return nil, fmt.Errorf("propose: %w", err)
		}
		if p.Decision == "keep_base" {
			r.record.Proposer = "keep_base"
			return nil, nil
		}
		var refused []string
		out = out[:0]
		for i, c := range p.Candidates[:min(len(p.Candidates), maxCandidates)] {
			h := c.harness()
			if h.Equal(r.base) {
				continue
			}
			if err := harness.Validate(h, r.tools); err != nil {
				refused = append(refused, fmt.Sprintf("candidate %d: %v", i+1, err))
				continue
			}
			ev := candidateEvent{Summary: clip(c.Summary, 200), Guidance: h.Guidance, Notes: h.ToolNotes}
			for _, e := range c.Edits {
				ev.Edits = append(ev.Edits, clip(e.Change+" (cites "+e.Cites+")", 400))
			}
			out = append(out, prop{h: h, ev: ev})
		}
		if len(refused) == 0 || attempt == 1 {
			if len(refused) > 0 {
				r.record.Proposer = "refused: " + clip(strings.Join(refused, " | "), 600)
			}
			return out, nil
		}
		prompt += "\nYOUR PREVIOUS ATTEMPT WAS REJECTED by the validator:\n" + strings.Join(refused, "\n") + "\nAnswer again, fixing these.\n"
	}
	return out, nil
}

// evaluate probes a candidate on every target and suite turn, applies the
// hard checks and has the judge compare it with the active version.
func (r *round) evaluate(ctx context.Context, p *prop, targets []target, suite []Recorded, suiteBase []Trajectory) error {
	check := func(rec Recorded, base, got Trajectory) string {
		switch {
		case base.Final() && !got.Final():
			return fmt.Sprintf("%s: ended without an answer (%s)", rec.TurnID, trajStatus(got))
		case got.Refused > base.Refused:
			return fmt.Sprintf("%s: the guard refused %d call(s), the active version %d", rec.TurnID, got.Refused, base.Refused)
		case submits(got.Calls) > submits(rec.Calls):
			return fmt.Sprintf("%s: a call asked to submit", rec.TurnID)
		}
		return ""
	}
	type pair struct {
		rec       Recorded
		base, got Trajectory
		suite     bool
	}
	var pairs []pair
	for _, t := range targets {
		got, err := r.probe(ctx, t.rec, p.h)
		if err != nil {
			return err
		}
		pairs = append(pairs, pair{t.rec, t.bases[0], got, false})
	}
	for i, rec := range suite {
		got, err := r.probe(ctx, rec, p.h)
		if err != nil {
			return err
		}
		pairs = append(pairs, pair{rec, suiteBase[i], got, true})
	}
	for _, x := range pairs {
		p.ev.Probes = append(p.ev.Probes, x.got)
		if f := check(x.rec, x.base, x.got); f != "" && p.ev.Failed == "" {
			p.ev.Failed = f
		}
	}
	if p.ev.Failed != "" {
		return nil
	}
	for _, x := range pairs {
		out, why, err := r.judge(ctx, x.rec, x.base, x.got)
		if err != nil {
			return err
		}
		p.ev.Verdicts = append(p.ev.Verdicts, verdict{TurnID: x.rec.TurnID, Suite: x.suite, Outcome: out, Why: clip(why, 400)})
		switch {
		case x.suite && out == "worse":
			p.ev.SuiteBad++
		case !x.suite && out == "better":
			p.ev.Wins++
		case !x.suite && out == "worse":
			p.ev.Losses++
		}
	}
	return nil
}

// judge compares a candidate's execution with the active version's twice,
// once in each order, and returns better or worse for the candidate only when
// both orders agree; otherwise same. One order alone carries the judge's
// position bias, and a single noisy "worse" on a suite turn would block every
// candidate.
func (r *round) judge(ctx context.Context, rec Recorded, base, got Trajectory) (string, string, error) {
	var outs, whys []string
	for _, candFirst := range []bool{false, true} {
		opts, cand, act := []Trajectory{base, got}, "B", "A"
		if candFirst {
			opts, cand, act = []Trajectory{got, base}, "A", "B"
		}
		var j judgement
		labels := []string{"A", "B"}
		if _, err := r.s.Model.Structured(ctx, llm.Request{Task: TaskJudge, System: judgeSystem, Prompt: judgePrompt(agent.FixedPrompt(), rec, labels, opts), RunID: r.id}, judgeSchema, &j); err != nil {
			return "", "", fmt.Errorf("judge: %w", err)
		}
		rank := j.rankOf(labels)
		out := "same"
		switch {
		case rank[cand] < rank[act]:
			out = "better"
		case rank[cand] > rank[act]:
			out = "worse"
		}
		outs, whys = append(outs, out), append(whys, j.Why)
	}
	why := strings.Join(whys, " / ")
	if outs[0] == outs[1] {
		return outs[0], why, nil
	}
	return "same", why, nil
}

// submits counts calls whose arguments ask to submit.
func submits(calls []Call) int {
	n := 0
	for _, c := range calls {
		var a map[string]any
		if json.Unmarshal([]byte(c.Args), &a) == nil && a["submit"] == true {
			n++
		}
	}
	return n
}

// reflect asks for at most one lesson and stores it. A failure only loses the
// lesson; the round's outcome stands.
func (r *round) reflect(ctx context.Context, adopted int64) {
	if len(r.record.Candidates) == 0 {
		return
	}
	rec, _ := json.Marshal(struct {
		Adopted    int64            `json:"adoptedVersion"`
		Candidates []candidateEvent `json:"candidates"`
	}{adopted, slimCandidates(r.record.Candidates)})
	var out reflection
	if _, err := r.s.Model.Structured(ctx, llm.Request{Task: TaskImprove, System: reflectSystem, Prompt: "ROUND RECORD:\n" + string(rec), RunID: r.id}, reflectSchema, &out); err != nil {
		r.s.log().Warn("improve: no reflection", "round", r.id, "error", err.Error())
		return
	}
	r.record.Reflection = &out
	if out.NoEntry || strings.TrimSpace(out.Cue) == "" || strings.TrimSpace(out.Lesson) == "" || strings.TrimSpace(out.Evidence) == "" {
		return
	}
	if !slices.Contains([]string{"low", "medium", "high"}, out.Confidence) {
		out.Confidence = "low"
	}
	if err := r.s.addLesson(ctx, Lesson{Cue: clip(out.Cue, 300), Lesson: clip(out.Lesson, 400), Evidence: clip(out.Evidence, 600), Confidence: out.Confidence, RoundID: r.id, Version: adopted}); err != nil {
		r.s.log().Warn("improve: lesson not stored", "round", r.id, "error", err.Error())
	}
}

// slimCandidates drops the probes' full calls, which the reflector does not
// need, to keep its prompt small.
func slimCandidates(cs []candidateEvent) []candidateEvent {
	out := make([]candidateEvent, len(cs))
	for i, c := range cs {
		c.Probes = nil
		out[i] = c
	}
	return out
}

// ---- persistence ----

func (s *Service) today(ctx context.Context) (rounds, probes int, err error) {
	since := s.now().Add(-24 * time.Hour).UTC().Format(time.RFC3339Nano)
	err = s.DB.Read().QueryRowContext(ctx, `SELECT count(*), coalesce(sum(probes), 0) FROM agent_improve_rounds WHERE started_at >= ? AND outcome <> 'skipped'`, since).Scan(&rounds, &probes)
	return rounds, probes, err
}

// skip records a round that stopped before its first probe, so a round asked
// for from the dashboard always leaves a row, and returns its line.
func (s *Service) skip(ctx context.Context, base int64, line string) (string, error) {
	now := s.now().UTC().Format(time.RFC3339Nano)
	detail := strings.TrimPrefix(line, "skipped: ")
	err := s.DB.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO agent_improve_rounds (id, started_at, finished_at, base_version, outcome, detail) VALUES (?,?,?,?,'skipped',?)`,
			store.NewID("round"), now, now, base, detail)
		return err
	})
	return line, err
}

func (s *Service) startRound(ctx context.Context, r *round) error {
	return s.DB.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO agent_improve_rounds (id, started_at, base_version) VALUES (?,?,?)`,
			r.id, s.now().UTC().Format(time.RFC3339Nano), r.base.Version)
		return err
	})
}

func (s *Service) finishRound(ctx context.Context, r *round, outcome, detail string, adopted int64) error {
	rec, _ := json.Marshal(r.record)
	return s.DB.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE agent_improve_rounds SET finished_at = ?, outcome = ?, detail = ?, adopted = ?, probes = ?, record = ? WHERE id = ?`,
			s.now().UTC().Format(time.RFC3339Nano), outcome, detail, adopted, r.probes, string(rec), r.id)
		return err
	})
}

func (s *Service) markExamined(ctx context.Context, r *round, targets []target) error {
	return s.DB.Write(ctx, func(tx *sql.Tx) error {
		for _, t := range targets {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO agent_improve_examined (turn_id, version, round_id) VALUES (?,?,?)`, t.rec.TurnID, r.base.Version, r.id); err != nil {
				return err
			}
		}
		return nil
	})
}

// RoundRow is a round as the dashboard lists it.
type RoundRow struct {
	ID          string          `json:"id"`
	StartedAt   string          `json:"startedAt"`
	FinishedAt  string          `json:"finishedAt"`
	BaseVersion int64           `json:"baseVersion"`
	Outcome     string          `json:"outcome"`
	Detail      string          `json:"detail"`
	Adopted     int64           `json:"adopted"`
	Probes      int             `json:"probes"`
	Record      json.RawMessage `json:"record,omitempty"`
}

// Rounds lists the newest rounds first, without their records.
func (s *Service) Rounds(ctx context.Context, limit int) ([]RoundRow, error) {
	rows, err := s.DB.Read().QueryContext(ctx, `SELECT id, started_at, finished_at, base_version, outcome, detail, adopted, probes FROM agent_improve_rounds ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RoundRow{}
	for rows.Next() {
		var x RoundRow
		if err := rows.Scan(&x.ID, &x.StartedAt, &x.FinishedAt, &x.BaseVersion, &x.Outcome, &x.Detail, &x.Adopted, &x.Probes); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// RoundRecord returns one round with its record.
func (s *Service) RoundRecord(ctx context.Context, id string) (RoundRow, error) {
	var x RoundRow
	var rec string
	err := s.DB.Read().QueryRowContext(ctx, `SELECT id, started_at, finished_at, base_version, outcome, detail, adopted, probes, record FROM agent_improve_rounds WHERE id = ?`, id).
		Scan(&x.ID, &x.StartedAt, &x.FinishedAt, &x.BaseVersion, &x.Outcome, &x.Detail, &x.Adopted, &x.Probes, &rec)
	if errors.Is(err, sql.ErrNoRows) {
		return RoundRow{}, store.ErrNotFound
	}
	x.Record = json.RawMessage(rec)
	return x, err
}

// Lesson is one kept lesson.
type Lesson struct {
	ID         string `json:"id"`
	Cue        string `json:"cue"`
	Lesson     string `json:"lesson"`
	Evidence   string `json:"evidence"`
	Confidence string `json:"confidence"`
	RoundID    string `json:"roundId"`
	Version    int64  `json:"version"`
	Undone     bool   `json:"undone"`
	CreatedAt  string `json:"createdAt"`
}

// Lessons lists the kept lessons, newest first.
func (s *Service) Lessons(ctx context.Context) ([]Lesson, error) {
	rows, err := s.DB.Read().QueryContext(ctx, `SELECT id, cue, lesson, evidence, confidence, round_id, version, undone, created_at FROM agent_lessons ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Lesson{}
	for rows.Next() {
		var l Lesson
		if err := rows.Scan(&l.ID, &l.Cue, &l.Lesson, &l.Evidence, &l.Confidence, &l.RoundID, &l.Version, &l.Undone, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// addLesson stores a lesson and keeps at most maxLessons: an undone lesson
// goes first, then the oldest.
func (s *Service) addLesson(ctx context.Context, l Lesson) error {
	return s.DB.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_lessons (id, cue, lesson, evidence, confidence, round_id, version, created_at) VALUES (?,?,?,?,?,?,?,?)`,
			store.NewID("lesson"), l.Cue, l.Lesson, l.Evidence, l.Confidence, l.RoundID, l.Version, s.now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM agent_lessons WHERE id IN (
			SELECT id FROM agent_lessons ORDER BY undone DESC, created_at ASC
			LIMIT max(0, (SELECT count(*) FROM agent_lessons) - ?))`, maxLessons)
		return err
	})
}

// Restore makes an earlier version active again and marks the lessons of the
// versions it undid.
func (s *Service) Restore(ctx context.Context, id int64) (int64, error) {
	var tools []string
	for _, t := range s.Registry.Tools() {
		if t.Offered() {
			tools = append(tools, t.Name)
		}
	}
	nid, err := s.Harness.Restore(ctx, id, tools)
	if err != nil {
		return 0, err
	}
	err = s.DB.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE agent_lessons SET undone = 1 WHERE version > ? AND version < ?`, id, nid)
		return err
	})
	return nid, err
}
