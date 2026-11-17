package improve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/agent"
	"applysmith/internal/agent/goals"
	"applysmith/internal/agent/harness"
	"applysmith/internal/agent/sessions"
	"applysmith/internal/agent/triggers"
	"applysmith/internal/llm"
	"applysmith/internal/prompts"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
	"applysmith/internal/workspace"
)

type rig struct {
	t   *testing.T
	db  *store.Store
	ss  *sessions.Store
	hs  *harness.Store
	reg *agent.Registry
	ran map[string]int // production tool runs
	mu  sync.Mutex
}

func tool(name string, class agent.Class, run agent.ToolFunc) agent.Tool {
	return agent.Tool{Name: name, Label: name, Description: "Does " + name + ".", Class: class, PII: agent.PIINone, Timeout: time.Second,
		Schema: `{"type":"object","additionalProperties":false,"properties":{"q":{"type":"string"}}}`, Run: run}
}

func newRig(t *testing.T) *rig {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "applysmith.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	r := &rig{t: t, db: db, ss: sessions.New(db, nil), hs: harness.New(db, nil), ran: map[string]int{}}
	prod := func(name string) agent.ToolFunc {
		return func(context.Context, agent.ToolCall) (agent.ToolResult, error) {
			r.mu.Lock()
			r.ran[name]++
			r.mu.Unlock()
			return agent.ToolResult{Text: "live " + name}, nil
		}
	}
	r.reg, err = agent.NewRegistry(
		tool("tracker_query", agent.ClassRead, prod("tracker_query")),
		agent.Tool{Name: "save_fact", Label: "save_fact", Description: "Saves.", Class: agent.ClassWriteLocal, PII: agent.PIINone, Timeout: time.Second,
			Schema: `{"type":"object","additionalProperties":false,"properties":{"q":{"type":"string"}}}`, Run: prod("save_fact")},
		agent.Tool{Name: "read_page", Label: "read_page", Description: "Reads a page.", Class: agent.ClassRead, PII: agent.PIINone, Timeout: time.Second, OutsideContent: true,
			Schema: `{"type":"object","additionalProperties":false,"properties":{"q":{"type":"string"}}}`, Run: prod("read_page")},
	)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// turn records a model turn: the user's message, its calls and answer, and
// optionally the user's next message.
func (r *rig) turn(status, reason, msg, answer, next string, calls ...Call) string {
	r.t.Helper()
	ctx := context.Background()
	sess, err := r.ss.Create(ctx, sessions.Dashboard, "")
	if err != nil {
		r.t.Fatal(err)
	}
	id := store.NewID("turn")
	must := func(err error) {
		if err != nil {
			r.t.Fatal(err)
		}
	}
	must(r.ss.StartTurn(ctx, sessions.Turn{ID: id, SessionID: sess.ID, Channel: sessions.Dashboard, Origin: sessions.OriginModel}))
	_, err = r.ss.Append(ctx, sessions.Message{SessionID: sess.ID, TurnID: id, Role: sessions.RoleUser, Body: msg, OK: true})
	must(err)
	for _, c := range calls {
		_, err = r.ss.Append(ctx, sessions.Message{SessionID: sess.ID, TurnID: id, Role: sessions.RoleToolCall, Body: c.Args, ToolName: c.Name, ToolCallID: c.ID, OK: true})
		must(err)
		_, err = r.ss.Append(ctx, sessions.Message{SessionID: sess.ID, TurnID: id, Role: sessions.RoleToolResult, Body: c.Result, ToolName: c.Name, ToolCallID: c.ID, OK: c.OK})
		must(err)
	}
	_, err = r.ss.Append(ctx, sessions.Message{SessionID: sess.ID, TurnID: id, Role: sessions.RoleAssistant, Body: answer, OK: true})
	must(err)
	must(r.ss.FinishTurn(ctx, id, status, reason, ""))
	if next != "" {
		next2 := store.NewID("turn")
		must(r.ss.StartTurn(ctx, sessions.Turn{ID: next2, SessionID: sess.ID, Channel: sessions.Dashboard, Origin: sessions.OriginRouter}))
		_, err = r.ss.Append(ctx, sessions.Message{SessionID: sess.ID, TurnID: next2, Role: sessions.RoleUser, Body: next, OK: true})
		must(err)
		must(r.ss.FinishTurn(ctx, next2, "final", "", ""))
	}
	return id
}

func TestImprove_LoadCopiesTheConversationBeforeTheTurnOnly(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	id := r.turn("final", "", "top roles?", "Here are three.", "", Call{ID: "c1", Name: "tracker_query", Args: `{"step":"reading","q":"top"}`, Result: "3 rows", OK: true})
	rec, err := Load(ctx, r.db, r.ss, id)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Message != "top roles?" || rec.Answer != "Here are three." || len(rec.Calls) != 1 || rec.Calls[0].Args != `{"q":"top"}` || rec.Calls[0].Result != "3 rows" {
		t.Fatalf("loaded %+v", rec)
	}
	dst := sessions.New(r.db, nil)
	sid, err := rec.copyTo(ctx, dst)
	if err != nil {
		t.Fatal(err)
	}
	msgs, _ := dst.Messages(ctx, sid)
	if len(msgs) != 0 {
		t.Fatalf("the copy holds the turn itself: %d messages", len(msgs))
	}
	if _, err := Load(ctx, r.db, r.ss, "turn_missing"); err == nil {
		t.Fatal("a missing turn loaded")
	}
}

func TestImprove_TheOracleNeverRunsAWriteAndAnswersFromTheRecordFirst(t *testing.T) {
	r := newRig(t)
	o := &oracle{prod: r.reg, recorded: []Call{{ID: "c1", Name: "tracker_query", Args: `{"q":"top"}`, Result: "recorded rows", OK: true}}, sources: map[string]string{}}
	call := func(name, id, args string) (agent.ToolResult, error) {
		tl, _ := r.reg.Lookup(name)
		return o.fn(tl)(context.Background(), agent.ToolCall{CallID: id, Args: json.RawMessage(args)})
	}
	if res, _ := call("tracker_query", "p1", `{"q":"top"}`); res.Text != "recorded rows" {
		t.Fatalf("recorded call = %q", res.Text)
	}
	if res, _ := call("tracker_query", "p2", `{"q":"top"}`); res.Text != "live tracker_query" {
		t.Fatalf("a second identical read = %q, want a live read", res.Text)
	}
	if res, _ := call("save_fact", "p3", `{"q":"x"}`); !strings.Contains(res.Text, "replay") {
		t.Fatalf("a write = %q", res.Text)
	}
	if res, _ := call("read_page", "p4", `{"q":"x"}`); !strings.Contains(res.Text, "replay") {
		t.Fatalf("a network read = %q", res.Text)
	}
	if r.ran["save_fact"] != 0 || r.ran["read_page"] != 0 || r.ran["tracker_query"] != 1 {
		t.Fatalf("production runs = %v", r.ran)
	}
	for id, want := range map[string]string{"p1": SourceRecorded, "p2": SourceLive, "p3": SourceSynthetic, "p4": SourceSynthetic} {
		if got, _ := o.source(id); got != want {
			t.Errorf("source of %s = %s, want %s", id, got, want)
		}
	}
}

// Services a registry needs to declare every live read; the probe never calls
// them here, so an embedded nil interface is enough.
type (
	stubBank       struct{ agent.Previewer }
	stubBankEditor struct{ agent.BankEditor }
	stubInbox      struct{ agent.Inbox }
	stubKnowledge  struct{ agent.Knowledge }
)

type stubSchedules struct{}

func (stubSchedules) Schedules(context.Context) ([]agent.ScheduleLine, error) { return nil, nil }

func TestImprove_LiveReadsAreReadClassToolsThatReturnTheProductsOwnText(t *testing.T) {
	r := newRig(t)
	tr, err := tracker.New(r.db, tracker.Options{})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := agent.BuildRegistry(agent.ToolDeps{Tracker: tr, Store: r.db, Sessions: r.ss, Bank: stubBank{}, BankEditor: stubBankEditor{}, Inbox: stubInbox{},
		Knowledge: stubKnowledge{}, Schedules: stubSchedules{}, Prompts: prompts.New(""),
		ApplyLanes: func(context.Context) ([]agent.ApplyLane, error) { return nil, nil },
		Profile:    func(context.Context) (workspace.Profile, error) { return workspace.Profile{}, nil },
		Goals:      goals.New(r.db, nil, nil, time.UTC), ScheduledPrompts: triggers.New(r.db, nil, nil, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	for name := range liveReads {
		if agent.ToolClass(name) != string(agent.ClassRead) {
			t.Errorf("%s is %q, not a read", name, agent.ToolClass(name))
		}
		tl, ok := reg.Lookup(name)
		if !ok {
			t.Errorf("%s is not registered", name)
			continue
		}
		if tl.Class != agent.ClassRead || tl.OutsideContent {
			t.Errorf("%s is %s with outside content %v", name, tl.Class, tl.OutsideContent)
		}
	}
}

// fakeReplayer answers a probe from a function of the turn and the version.
type fakeReplayer struct {
	mu    sync.Mutex
	n     int
	reply func(rec Recorded, h harness.Harness) Trajectory
}

func (f *fakeReplayer) Probe(_ context.Context, rec Recorded, h harness.Harness) (Trajectory, error) {
	f.mu.Lock()
	f.n++
	f.mu.Unlock()
	tr := f.reply(rec, h)
	tr.TurnID, tr.Version = rec.TurnID, h.Version
	return tr, nil
}

// fakeModel answers each task with a scripted function.
type fakeModel struct {
	mu       sync.Mutex
	judge    string // the judge's model
	propose  func() proposal
	fail     error                          // what the proposer's call returns in place of a proposal
	prefer   func(prompt string) [][]string // judge ranking from the prompt
	reflects int
}

func (m *fakeModel) Resolve(_ context.Context, task string) (llm.Resolved, error) {
	return llm.Resolved{Provider: "codex", Model: m.judge}, nil
}

func (m *fakeModel) Structured(_ context.Context, req llm.Request, schema json.RawMessage, out any) (*llm.Response, error) {
	if err := llm.CheckSchema(schema); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var v any
	switch {
	case req.Task == TaskImprove && req.System == proposeSystem:
		if m.fail != nil {
			return nil, m.fail
		}
		v = m.propose()
	case req.Task == TaskJudge:
		v = judgement{Ranking: m.prefer(req.Prompt), Why: "scripted"}
	case req.Task == TaskImprove && req.System == reflectSystem:
		m.reflects++
		v = map[string]any{"verdicts": []any{}, "noEntry": false, "cue": "a ranking question", "lesson": "note tracker_query sorts by score", "evidence": "won 1 of 1", "confidence": "low"}
	default:
		return nil, fmt.Errorf("unexpected task %s", req.Task)
	}
	b, _ := json.Marshal(v)
	return &llm.Response{}, json.Unmarshal(b, out)
}

type fakeRouter struct{ agent.Router }

func (fakeRouter) AgentRoute(context.Context, string) (llm.AgentRoute, error) {
	return llm.AgentRoute{Resolved: llm.Resolved{Provider: "deepseek", Model: "deepseek-flash"}}, nil
}

const good = "Ranked by score: A, B, C."

// service builds a round where the candidate that adds a tracker note answers
// well, the base answers badly on targets, and suite answers do not change
// unless worse is set.
func (r *rig) service(m *fakeModel, worseOnSuite bool) (*Service, *fakeReplayer) {
	rep := &fakeReplayer{reply: func(rec Recorded, h harness.Harness) Trajectory {
		tr := Trajectory{Status: sessions.TurnFinal, Answer: rec.Answer}
		if strings.HasPrefix(rec.Message, "rank") {
			tr.Answer = "I cannot tell."
			if !h.Empty() {
				tr.Answer = good
			}
		} else if worseOnSuite && !h.Empty() {
			tr.Answer = "worse"
		}
		return tr
	}}
	return &Service{DB: r.db, Sessions: r.ss, Harness: r.hs, Replayer: rep, Registry: r.reg, Router: fakeRouter{}, Model: m}, rep
}

func noteProposal() proposal {
	return proposal{Decision: "edit", Candidates: []candidateText{{Summary: "note the score sort", Guidance: []string{},
		ToolNotes: []toolNote{{Tool: "tracker_query", Note: "For a ranking question, sort by score."}}}}}
}

// preferGood ranks the option whose answer is the good one first, and an
// option that says "worse" last.
func preferGood(prompt string) [][]string {
	a := strings.Index(prompt, "OPTION A")
	b := strings.Index(prompt, "OPTION B")
	optA, optB := prompt[a:b], prompt[b:]
	score := func(s string) int {
		switch {
		case strings.Contains(s, good):
			return 0
		case strings.Contains(s, `"worse"`):
			return 2
		}
		return 1
	}
	switch sa, sb := score(optA), score(optB); {
	case sa < sb:
		return [][]string{{"A"}, {"B"}}
	case sb < sa:
		return [][]string{{"B"}, {"A"}}
	}
	return [][]string{{"A", "B"}}
}

func TestImprove_ARoundAdoptsACandidateThatWinsTargetsAndLosesNoSuiteTurn(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	target := r.turn("final", "", "rank my roles", "I cannot tell.", "no, that's wrong", Call{ID: "c1", Name: "tracker_query", Args: `{"q":"all"}`, Result: "rows", OK: true})
	r.turn("final", "", "what did I apply to", "Two roles.", "", Call{ID: "c2", Name: "tracker_query", Args: `{"q":"applied"}`, Result: "2 rows", OK: true})
	m := &fakeModel{judge: "default", propose: noteProposal, prefer: preferGood}
	s, rep := r.service(m, false)
	out, err := s.Round(ctx)
	if err != nil || !strings.HasPrefix(out, "adopted: version 1") {
		t.Fatalf("round = %q, %v", out, err)
	}
	h, _ := r.hs.Active(ctx)
	if h.Version != 1 || h.ToolNotes["tracker_query"] == "" {
		t.Fatalf("active = %+v", h)
	}
	// 2 base replays of the target, 1 suite base, 1 target + 1 suite under the candidate.
	if rep.n != 5 {
		t.Fatalf("probes = %d", rep.n)
	}
	ls, _ := s.Lessons(ctx)
	if len(ls) != 1 || ls[0].Version != 1 {
		t.Fatalf("lessons = %+v", ls)
	}
	rounds, _ := s.Rounds(ctx, 5)
	if len(rounds) != 1 || rounds[0].Outcome != "adopted" || rounds[0].Probes != 5 {
		t.Fatalf("rounds = %+v", rounds)
	}
	// The target was examined under version 0; under version 1 it is a target
	// again, but the same round does not run twice on it under one version.
	var n int
	_ = r.db.Read().QueryRowContext(ctx, `SELECT count(*) FROM agent_improve_examined WHERE turn_id = ? AND version = 0`, target).Scan(&n)
	if n != 1 {
		t.Fatalf("examined rows = %d", n)
	}
	// A restore to 0 empties the harness and marks the lesson undone.
	if _, err := s.Restore(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if h, _ := r.hs.Active(ctx); !h.Empty() {
		t.Fatalf("after restore %+v", h)
	}
	if ls, _ := s.Lessons(ctx); !ls[0].Undone {
		t.Fatal("the lesson of the undone version is not marked")
	}
}

func TestImprove_ARoundWhoseProposalFailedLeavesItsTargetForTheNextRound(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.turn("final", "", "rank my roles", "I cannot tell.", "no, that's wrong", Call{ID: "c1", Name: "tracker_query", Args: `{"q":"all"}`, Result: "rows", OK: true})
	m := &fakeModel{judge: "default", propose: noteProposal, prefer: preferGood, fail: errors.New("bad-request")}
	s, _ := r.service(m, false)
	if out, err := s.Round(ctx); err == nil || !strings.HasPrefix(out, "failed") {
		t.Fatalf("round = %q, %v", out, err)
	}
	m.fail = nil
	if out, err := s.Round(ctx); err != nil || !strings.HasPrefix(out, "adopted: version 1") {
		t.Fatalf("the next round = %q, %v", out, err)
	}
}

func TestImprove_ACandidateThatLosesASuiteTurnIsNotAdopted(t *testing.T) {
	r := newRig(t)
	r.turn("error", "stalled", "rank my roles", "I cannot tell.", "", Call{ID: "c1", Name: "tracker_query", Args: `{"q":"all"}`, Result: "rows", OK: true})
	r.turn("final", "", "what did I apply to", "Two roles.", "", Call{ID: "c2", Name: "tracker_query", Args: `{"q":"applied"}`, Result: "2 rows", OK: true})
	s, _ := r.service(&fakeModel{judge: "default", propose: noteProposal, prefer: preferGood}, true)
	out, err := s.Round(context.Background())
	if err != nil || !strings.HasPrefix(out, "kept") {
		t.Fatalf("round = %q, %v", out, err)
	}
	if h, _ := r.hs.Active(context.Background()); !h.Empty() {
		t.Fatalf("a suite regression was adopted: %+v", h)
	}
}

func TestImprove_HardChecksFailACandidateBeforeTheJudgeSeesIt(t *testing.T) {
	r := newRig(t)
	r.turn("final", "", "rank my roles", "I cannot tell.", "wrong", Call{ID: "c1", Name: "tracker_query", Args: `{"q":"all"}`, Result: "rows", OK: true})
	m := &fakeModel{judge: "default", propose: noteProposal, prefer: func(string) [][]string {
		t.Fatal("the judge was asked about a candidate that failed a hard check")
		return nil
	}}
	s, rep := r.service(m, false)
	base := rep.reply
	rep.reply = func(rec Recorded, h harness.Harness) Trajectory {
		tr := base(rec, h)
		if !h.Empty() {
			tr.Refused = 1
		}
		return tr
	}
	out, err := s.Round(context.Background())
	if err != nil || !strings.HasPrefix(out, "kept") {
		t.Fatalf("round = %q, %v", out, err)
	}
}

func TestImprove_ARoundSkipsWhenTheJudgeWouldGradeItsOwnModelOrIsSwitchedOff(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.turn("error", "stalled", "rank my roles", "", "")
	s, rep := r.service(&fakeModel{judge: "default", propose: noteProposal, prefer: preferGood}, false)
	s.Model = &sameProvider{s.Model.(*fakeModel)}
	if out, err := s.Round(ctx); err != nil || !strings.HasPrefix(out, "skipped") || rep.n != 0 {
		t.Fatalf("round = %q, %v, probes %d", out, err, rep.n)
	}
	if err := r.ss.SetSwitch(ctx, sessions.SettingSelfImprove, false); err != nil {
		t.Fatal(err)
	}
	if out, _ := s.Round(ctx); out != "switched off" {
		t.Fatalf("switched off round = %q", out)
	}
}

// sameProvider resolves the judge to the agent's own provider and model.
type sameProvider struct{ *fakeModel }

func (s *sameProvider) Resolve(context.Context, string) (llm.Resolved, error) {
	return llm.Resolved{Provider: "deepseek", Model: "deepseek-flash"}, nil
}

func TestImprove_ARoundFitsTheDaysProbeBudgetBeforeItStarts(t *testing.T) {
	c := func(n int) []*candidate { return make([]*candidate, n) }
	tg, su := fit(c(3), c(4), 30)
	if len(tg) != 3 || len(su) != 4 { // 6 + 4 + 14 = 24
		t.Fatalf("full budget: %d targets, %d suite", len(tg), len(su))
	}
	tg, su = fit(c(3), c(4), 13)
	if len(tg) != 2 || len(su) != 1 || len(tg)*baseRuns+len(su)+maxCandidates*(len(tg)+len(su)) > 13 {
		t.Fatalf("13 probes: %d targets, %d suite", len(tg), len(su))
	}
	if tg, _ = fit(c(3), c(4), 6); len(tg) != 0 {
		t.Fatalf("6 probes kept %d targets; a round needs 7 with a suite turn", len(tg))
	}
}

func TestImprove_AJudgeThatAlwaysPrefersTheFirstOptionAdoptsNothing(t *testing.T) {
	r := newRig(t)
	r.turn("final", "", "rank my roles", "I cannot tell.", "no, that's wrong", Call{ID: "c1", Name: "tracker_query", Args: `{"q":"all"}`, Result: "rows", OK: true})
	r.turn("final", "", "what did I apply to", "Two roles.", "", Call{ID: "c2", Name: "tracker_query", Args: `{"q":"applied"}`, Result: "2 rows", OK: true})
	first := func(string) [][]string { return [][]string{{"A"}, {"B"}} }
	s, _ := r.service(&fakeModel{judge: "default", propose: noteProposal, prefer: first}, false)
	out, err := s.Round(context.Background())
	if err != nil || !strings.HasPrefix(out, "kept") {
		t.Fatalf("a position-biased judge decided the round: %q, %v", out, err)
	}
}
