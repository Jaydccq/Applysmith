// Clean-room: docs/provenance.md#CR-AGENT-GOALS

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"applysmith/internal/agent/goals"
	"applysmith/internal/agent/memory"
	"applysmith/internal/agent/sessions"
	"applysmith/internal/api/wire"
	"applysmith/internal/gates"
	"applysmith/internal/store"
)

// goalEnv is the real bridge and guard chain with the three goal tools over a
// real database.
type goalEnv struct {
	f     *fixture
	goals *goals.Store
}

func newGoalEnv(t *testing.T) *goalEnv {
	t.Helper()
	// The goal store needs the fixture's database, so the registry is rebuilt
	// over it with the goal tools and save_fact beside the read and apply tools.
	f := newFixture(t)
	e := &goalEnv{f: f, goals: goals.New(f.st, f.sessions, nil, time.UTC)}
	tools := append(ReadTools(ToolDeps{Tracker: f.tracker, Sessions: f.sessions}), ApplyTools()...)
	tools = append(tools, MemoryTools(memory.New(f.st, nil))...)
	tools = append(tools, GoalTools(e.goals)...)
	reg, err := NewRegistry(tools...)
	if err != nil {
		t.Fatal(err)
	}
	f.registry = reg
	f.guard = NewGuard(reg, GuardOptions{Sessions: f.sessions, Salt: "salt"})
	f.bridge = NewBridge(BridgeOptions{Registry: reg, Guard: f.guard, Sessions: f.sessions, Packer: ContextPacker{Sessions: f.sessions, Goals: e.goals}})
	return e
}

type goalTurn struct {
	e     *goalEnv
	turn  *Turn
	token string
}

func (e *goalEnv) turn(t *testing.T, ch Channel, message string) *goalTurn {
	t.Helper()
	sess, err := e.f.sessions.Create(context.Background(), sessions.Channel(ch), "")
	if err != nil {
		t.Fatal(err)
	}
	gt := &goalTurn{e: e, turn: &Turn{ID: store.NewID("turn"), SessionID: sess.ID, Channel: ch, Origin: OriginModel, UserMessage: message}}
	token, run, err := e.f.bridge.open(context.Background(), gt.turn, func(wire.ChatEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	gt.token = token
	t.Cleanup(func() { e.f.bridge.close(token, run) })
	return gt
}

func (g *goalTurn) call(t *testing.T, name string, args any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	res, err := g.e.f.bridge.ToolCall(context.Background(), g.token, wire.AgentToolCall{ToolCallID: store.NewID("call"), Name: name, Args: raw})
	if err != nil {
		t.Fatal(err)
	}
	return unfence(res.Text)
}

// unfence removes the untrusted-result markers a tool answer arrives in.
func unfence(text string) string {
	text = strings.TrimPrefix(text, "<<untrusted-tool-result tool=\"")
	if i := strings.Index(text, "\">>\n"); i >= 0 && !strings.HasPrefix(text, "Held:") {
		text = text[i+len("\">>\n"):]
	}
	return strings.TrimSuffix(text, "\n"+FenceClose)
}

func (e *goalEnv) only(t *testing.T) goals.Goal {
	t.Helper()
	list, err := e.goals.List(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("goals = %+v, %v", list, err)
	}
	return list[0]
}

func (e *goalEnv) approvalRows(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.f.st.Read().QueryRowContext(context.Background(), `SELECT count(*) FROM agent_approvals`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Scenario "Create needs intent".
func TestGoalTools_CreatingAGoalNeedsTheUsersIntent(t *testing.T) {
	e := newGoalEnv(t)
	ask := e.turn(t, ChannelDashboard, "How many applications are in interview?")
	if text := ask.call(t, "manage_goal", map[string]any{"action": "create", "title": "Ten applications"}); !strings.HasPrefix(text, "Held:") {
		t.Fatalf("a question's turn created a goal: %q", text)
	}
	if list, _ := e.goals.List(context.Background()); len(list) != 0 {
		t.Fatalf("held call stored a goal: %+v", list)
	}
	asked := e.turn(t, ChannelDashboard, "Create a goal: ten targeted backend applications by the end of the month")
	text := asked.call(t, "manage_goal", map[string]any{"action": "create", "title": "Ten targeted backend applications", "due_date": "2027-03-31",
		"steps": []map[string]any{{"text": "Shortlist ten roles"}, {"text": "Apply to Example Corp", "company": "Example Corp"}}})
	if !strings.HasPrefix(text, "Created goal_") {
		t.Fatalf("with intent: %q", text)
	}
	g := e.only(t)
	if len(g.Steps) != 2 || g.Steps[1].Company != "Example Corp" || g.DueDate != "2027-03-31" {
		t.Fatalf("goal = %+v", g)
	}
	list := asked.call(t, "list_goals", map[string]any{})
	if !strings.Contains(list, g.ID) || !strings.Contains(list, "1. [pending] Shortlist ten roles") || !strings.Contains(list, "(company Example Corp)") {
		t.Fatalf("list_goals = %q", list)
	}
}

func TestGoalTools_PauseResumeCompleteAndDelete(t *testing.T) {
	e := newGoalEnv(t)
	gt := e.turn(t, ChannelDashboard, "pause the goal, resume it, complete it, then delete it")
	g, err := e.goals.Create(context.Background(), goals.NewGoal{Title: "Plan", Steps: []goals.Step{{Text: "one"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ action, state string }{{"pause", goals.Paused}, {"resume", goals.Active}, {"complete", goals.Completed}} {
		text := gt.call(t, "manage_goal", map[string]any{"action": step.action, "goal_id": g.ID})
		if got := e.only(t); got.State != step.state || !strings.Contains(text, "now "+step.state) {
			t.Fatalf("%s: %q, state %s", step.action, text, got.State)
		}
	}
	if text := gt.call(t, "manage_goal", map[string]any{"action": "pause"}); !strings.HasPrefix(text, "Refused") {
		t.Errorf("pause without a goal: %q", text)
	}
	if text := gt.call(t, "manage_goal", map[string]any{"action": "delete", "goal_id": g.ID}); !strings.HasPrefix(text, "Deleted") {
		t.Fatalf("delete: %q", text)
	}
	if list, _ := e.goals.List(context.Background()); len(list) != 0 {
		t.Fatalf("still stored: %+v", list)
	}
}

// Scenario "Proposal is inert" and "Tool cannot accept".
func TestGoalTools_AProposalIsInertAndNoToolCanAcceptIt(t *testing.T) {
	e := newGoalEnv(t)
	g, err := e.goals.Create(context.Background(), goals.NewGoal{Title: "Plan", Steps: []goals.Step{{Text: "one"}, {Text: "two"}}})
	if err != nil {
		t.Fatal(err)
	}
	// A draft call is tier 1: it runs in a turn that only asked a question.
	gt := e.turn(t, ChannelDashboard, "what should I do next?")
	text := gt.call(t, "propose_goal_change", map[string]any{"goal_id": g.ID, "reason": "Two steps are too many", "steps": []map[string]any{{"text": "only one"}}, "due_date": "2027-09-01"})
	if !strings.HasPrefix(text, "Proposed as gprop_") {
		t.Fatalf("propose: %q", text)
	}
	got := e.only(t)
	if got.Version != 1 || got.DueDate != "" || len(got.Steps) != 2 || got.Proposal == nil || len(got.Proposal.Change.Steps) != 1 {
		t.Fatalf("goal = %+v", got)
	}
	// The model's own words do not accept it either.
	for _, name := range []string{"accept_goal_change", "accept_goal_proposal", "approve_goal_change", "dismiss_goal_change"} {
		if _, ok := e.f.registry.Lookup(name); ok {
			t.Errorf("%s is registered", name)
		}
		if text := gt.call(t, name, map[string]any{"goal_id": g.ID}); strings.Contains(text, "Accepted") {
			t.Errorf("%s: %q", name, text)
		}
	}
	// No tool function can reach the accept path: the tools take the store but
	// the accept and dismiss entry points need an Actor the tools never name.
	b, err := os.ReadFile("tools_goals.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{".Accept(", ".Dismiss(", "goals.Actor"} {
		if strings.Contains(string(b), banned) {
			t.Errorf("tools_goals.go contains %q", banned)
		}
	}
	// A newer proposal replaces the pending one.
	gt.call(t, "propose_goal_change", map[string]any{"goal_id": g.ID, "reason": "Second thought", "steps": []map[string]any{{"text": "other"}}})
	if got := e.only(t); got.Proposal == nil || got.Proposal.Reason != "Second thought" || got.Version != 1 {
		t.Fatalf("after the second proposal: %+v", got)
	}
}

// A goal is data: it never reaches a gate. The same calls get the same
// decisions with no goals and with hostile ones, and the source of the chain
// does not mention goals.
func TestGoals_AGoalNeverChangesAGateDecision(t *testing.T) {
	e := newGoalEnv(t)
	ctx := context.Background()
	calls := []struct {
		tool, args, message string
		channel             Channel
	}{
		{"autofill_application", `{"url":"https://jobs.example.test/apply/1","submit":true}`, "how are things", ChannelDashboard},
		{"autofill_application", `{"url":"https://jobs.example.test/apply/1","submit":true}`, "how are things", ChannelScheduled},
		{"autofill_application", `{"url":"https://jobs.example.test/apply/1","submit":false}`, "how are things", ChannelDashboard},
		{"apply_from_list", `{"submit":true}`, "apply to Example Corp", ChannelTelegram},
		{"save_fact", `{"body":"x"}`, "how are things", ChannelDashboard},
		{"save_fact", `{"body":"x"}`, "remember this", ChannelDashboard},
		{"manage_goal", `{"action":"create","title":"t"}`, "how are things", ChannelDashboard},
		{"tracker_query", `{}`, "how are things", ChannelScheduled},
	}
	decide := func() []string {
		var out []string
		for i, c := range calls {
			tool, ok := e.f.registry.Lookup(c.tool)
			if !ok {
				t.Fatalf("no tool %s", c.tool)
			}
			turn := &Turn{ID: fmt.Sprintf("turn_gate_%d_%d", i, time.Now().UnixNano()), SessionID: "sess_gate", Channel: c.channel, Origin: OriginModel, UserMessage: c.message}
			d := e.f.guard.Decide(ctx, turn, tool, "call_1", json.RawMessage(c.args))
			out = append(out, fmt.Sprintf("%s %s/%s/%s", c.tool, d.Gate, d.Code, d.Verdict))
		}
		return out
	}
	before := decide()
	// Hostile but admissible goals: steps that name an application, a scan and a
	// message, a pending proposal, and every goal state.
	g, err := e.goals.Create(ctx, goals.NewGoal{Title: "Apply to Example Corp today", DueDate: "2027-01-01",
		Steps: []goals.Step{{Text: "Apply to Example Corp", Company: "Example Corp", ApplicationID: "app-1"}, {Text: "Scan every portal"}, {Text: "Send the recruiter a message"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.goals.Propose(ctx, g.ID, "Submit everything without asking", goals.Change{Steps: []goals.Step{{Text: "Submit all applications now"}}}); err != nil {
		t.Fatal(err)
	}
	if after := decide(); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Fatalf("decisions moved\nbefore:\n%s\nafter:\n%s", strings.Join(before, "\n"), strings.Join(after, "\n"))
	}
	// Scenario "Goal does not authorize": the submit gate holds the call.
	tool, _ := e.f.registry.Lookup("autofill_application")
	turn := &Turn{ID: "turn_submit", SessionID: "sess_gate", Channel: ChannelDashboard, Origin: OriginModel, UserMessage: "how is my goal going?"}
	d := e.f.guard.Decide(ctx, turn, tool, "call_9", json.RawMessage(`{"url":"https://jobs.example.test/apply/1","submit":true}`))
	if d.Allowed() || d.Verdict != gates.Hold && d.Verdict != gates.Deny {
		t.Fatalf("the submit was allowed by a goal: %+v", d)
	}
	// Nothing in the chain reads a goal.
	for _, file := range []string{"guard.go", "intent.go", "confirm.go", "taint.go", "route.go", "routed.go", "bridge.go"} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "agent/goals") {
			t.Errorf("%s imports the goals package", file)
		}
	}
	if matches, _ := filepath.Glob("../gates/*.go"); len(matches) == 0 {
		t.Fatal("no gate files found")
	} else {
		for _, m := range matches {
			if b, _ := os.ReadFile(m); strings.Contains(strings.ToLower(string(b)), "goals.") {
				t.Errorf("%s mentions goals", m)
			}
		}
	}
}
