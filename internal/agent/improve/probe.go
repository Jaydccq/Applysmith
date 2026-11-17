package improve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"applysmith/internal/agent"
	"applysmith/internal/agent/approvals"
	"applysmith/internal/agent/goals"
	"applysmith/internal/agent/harness"
	"applysmith/internal/agent/memory"
	"applysmith/internal/agent/sessions"
	"applysmith/internal/api/wire"
	"applysmith/internal/gates"
	"applysmith/internal/store"
)

// liveReads are the tools a probe may run for real when the recorded turn did
// not make the same call: they read local data and nothing else. A test pins
// every name to the read class and to results the product wrote.
var liveReads = map[string]bool{
	"tracker_query": true, "list_pipeline": true, "submissions_recent": true,
	"apply_list_status": true, "kb_search": true, "retrieve_evidence": true, "recall_session": true,
	"profile_summary": true, "load_prompt_mode": true, "list_schedules": true, "preview_schedules": true,
	"list_scheduled_prompts": true, "answer_bank_lookup": true, "answer_bank_find": true, "story_context": true, "list_goals": true,
}

// Sources of a probe's call result.
const (
	SourceRecorded  = "recorded"
	SourceLive      = "live"
	SourceSynthetic = "synthetic"
	SourceGuard     = "guard"
)

// syntheticText is the result of a call a replay cannot make.
const syntheticText = "This is a replay of an earlier conversation, so %s was not run and has no result. Do not call it again; answer from what you already have and say what is missing."

// Prober runs probes. Its fields are the production pieces a probe reads:
// the registry (for the catalog and the live reads), the model router and the
// supervisor whose processes, agent directory and routes a probe's pi uses.
type Prober struct {
	Registry   *agent.Registry
	Router     agent.Router
	Ledger     *store.Store
	Memory     *memory.Store
	Goals      *goals.Store
	Supervisor *agent.Supervisor
	// TempDir holds each probe's private database while it runs.
	TempDir string
	Log     *slog.Logger
}

// Trajectory is what a probe did.
type Trajectory struct {
	TurnID    string  `json:"turnId"`
	Version   int64   `json:"version"`
	Status    string  `json:"status"`
	Reason    string  `json:"reason,omitempty"`
	Answer    string  `json:"answer"`
	Calls     []Call  `json:"calls"`
	Refused   int     `json:"refused"`
	Held      int     `json:"held"`
	Synthetic int     `json:"synthetic"`
	CostUSD   float64 `json:"costUsd"`
	Seconds   float64 `json:"seconds"`
}

// Final reports whether the probe ended with an answer.
func (t Trajectory) Final() bool {
	return t.Status == sessions.TurnFinal && strings.TrimSpace(t.Answer) != ""
}

// Probe replays one recorded turn under a harness version. Nothing it does
// leaves its private database except the model calls, which are recorded in
// the ledger under the probe's own conversation id.
func (p *Prober) Probe(ctx context.Context, rec Recorded, h harness.Harness) (Trajectory, error) {
	log := p.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if err := os.MkdirAll(p.TempDir, 0o700); err != nil {
		return Trajectory{}, err
	}
	dir, err := os.MkdirTemp(p.TempDir, "probe-")
	if err != nil {
		return Trajectory{}, err
	}
	defer os.RemoveAll(dir)
	db, err := store.Open(ctx, filepath.Join(dir, "probe.db"), store.Options{})
	if err != nil {
		return Trajectory{}, err
	}
	defer db.Close()
	ss := sessions.New(db, nil)
	sid, err := rec.copyTo(ctx, ss)
	if err != nil {
		return Trajectory{}, fmt.Errorf("copy the conversation: %w", err)
	}

	or := &oracle{prod: p.Registry, recorded: rec.Calls, sources: map[string]string{}}
	var tools []agent.Tool
	for _, t := range p.Registry.Tools() {
		t.Conflict = nil
		if t.Run != nil {
			t.Run = or.fn(t)
		}
		tools = append(tools, t)
	}
	reg, err := agent.NewRegistry(tools...)
	if err != nil {
		return Trajectory{}, err
	}
	cards := approvals.New(db, ss, nil, nil)
	conf := agent.NewConfirmations(agent.ConfirmationsConfig{Cards: cards, Sessions: ss, Log: log})
	guard := agent.NewGuard(reg, agent.GuardOptions{Sessions: ss, Log: log, Salt: "probe", Grants: conf})
	packer := agent.ContextPacker{Sessions: ss, Memory: p.Memory, Goals: p.Goals, Cards: cards}
	bridge := agent.NewBridge(agent.BridgeOptions{Registry: reg, Guard: guard, Sessions: ss, Packer: packer, Confirmations: conf, Salt: "probe", Log: log, Harness: fixed(h), Gateway: p.Router})

	sup, end := p.Supervisor.Beside(bridge, ss)
	defer end()
	sink := &endSink{done: make(chan struct{})}
	ag := agent.New(agent.Config{Sessions: ss, Supervisor: sup, Sink: sink, Log: log, Bridge: bridge, Confirmations: conf})
	defer ag.Close()

	started := time.Now()
	sent, err := ag.Send(ctx, agent.SendRequest{SessionID: sid, Text: rec.Message})
	if err != nil {
		return Trajectory{}, fmt.Errorf("start the probe turn: %w", err)
	}
	sink.watch(sent.TurnID)
	select {
	case <-sink.done:
	case <-ctx.Done():
		ag.Close()
		return Trajectory{}, ctx.Err()
	case <-time.After(agent.TurnLimit + time.Minute):
		return Trajectory{}, errors.New("improve: the probe turn did not end")
	}
	ag.Close()
	tr, err := readTrajectory(ctx, ss, sid, sent.TurnID, or)
	if err != nil {
		return Trajectory{}, err
	}
	tr.TurnID, tr.Version, tr.Seconds = rec.TurnID, h.Version, time.Since(started).Seconds()
	if p.Ledger != nil {
		_ = p.Ledger.Read().QueryRowContext(ctx, `SELECT coalesce(sum(cost_usd), 0.0) FROM llm_ledger WHERE run_id = ?`, sid).Scan(&tr.CostUSD)
	}
	return tr, nil
}

func readTrajectory(ctx context.Context, ss *sessions.Store, sid, turnID string, or *oracle) (Trajectory, error) {
	_, msgs, turns, err := ss.Detail(ctx, sid)
	if err != nil {
		return Trajectory{}, err
	}
	var tr Trajectory
	for _, t := range turns {
		if t.ID == turnID {
			tr.Status, tr.Reason = t.Status, t.Reason
		}
	}
	byID := map[string]int{}
	for _, m := range msgs {
		if m.TurnID != turnID {
			continue
		}
		switch m.Role {
		case sessions.RoleToolCall:
			byID[m.ToolCallID] = len(tr.Calls)
			tr.Calls = append(tr.Calls, Call{ID: m.ToolCallID, Name: m.ToolName, Args: canonical(m.Body)})
		case sessions.RoleToolResult:
			if i, ok := byID[m.ToolCallID]; ok {
				tr.Calls[i].Result, tr.Calls[i].OK = clip(m.Body, 2000), m.OK
			}
		case sessions.RoleAssistant:
			tr.Answer = m.Body
		}
	}
	for i := range tr.Calls {
		src, ok := or.source(tr.Calls[i].ID)
		if !ok {
			src = SourceGuard
		}
		tr.Calls[i].Source = src
		if src == SourceSynthetic {
			tr.Synthetic++
		}
	}
	rows, err := ss.Decisions(ctx, turnID)
	if err != nil {
		return Trajectory{}, err
	}
	for _, r := range rows {
		switch r.Verdict {
		case gates.Deny:
			tr.Refused++
		case gates.Hold:
			tr.Held++
		}
	}
	return tr, nil
}

// oracle answers a probe's calls: the recorded result of the same call, a
// live local read, or a result saying the call was not made.
type oracle struct {
	prod     *agent.Registry
	mu       sync.Mutex
	recorded []Call
	used     []bool
	sources  map[string]string // probe call id -> source
}

func (o *oracle) fn(t agent.Tool) agent.ToolFunc {
	return func(ctx context.Context, c agent.ToolCall) (agent.ToolResult, error) {
		args := canonical(string(c.Args))
		if rc, ok := o.take(t.Name, args); ok {
			o.note(c.CallID, SourceRecorded)
			if !rc.OK {
				return agent.ToolResult{}, errors.New(rc.Result)
			}
			return agent.ToolResult{Text: rc.Result}, nil
		}
		if liveReads[t.Name] {
			if pt, ok := o.prod.Lookup(t.Name); ok && pt.Run != nil {
				o.note(c.CallID, SourceLive)
				return pt.Run(ctx, c)
			}
		}
		o.note(c.CallID, SourceSynthetic)
		return agent.ToolResult{Text: fmt.Sprintf(syntheticText, t.Name), Clean: true}, nil
	}
}

// take returns the first unused recorded call with the same tool and
// arguments.
func (o *oracle) take(name, args string) (Call, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.used == nil {
		o.used = make([]bool, len(o.recorded))
	}
	for i, rc := range o.recorded {
		if !o.used[i] && rc.Name == name && rc.Args == args {
			o.used[i] = true
			return rc, true
		}
	}
	return Call{}, false
}

func (o *oracle) note(id, src string) {
	o.mu.Lock()
	o.sources[id] = src
	o.mu.Unlock()
}

func (o *oracle) source(id string) (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	s, ok := o.sources[id]
	return s, ok
}

type fixedSource struct{ h harness.Harness }

func (f fixedSource) Active(context.Context) (harness.Harness, error) { return f.h, nil }

func fixed(h harness.Harness) agent.HarnessSource { return fixedSource{h} }

// endSink closes done when the watched turn publishes its final or error
// event.
type endSink struct {
	mu   sync.Mutex
	turn string
	seen map[string]bool
	once sync.Once
	done chan struct{}
}

func (s *endSink) Publish(_ string, ev wire.ChatEvent) {
	if ev.Type != wire.ChatFinal && ev.Type != wire.ChatError {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	s.seen[ev.TurnID] = true
	if s.turn != "" && s.seen[s.turn] {
		s.once.Do(func() { close(s.done) })
	}
}

func (s *endSink) watch(turn string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turn = turn
	if s.seen[turn] {
		s.once.Do(func() { close(s.done) })
	}
}

// clip shortens s to at most n characters, the last an ellipsis when
// something was left out.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
