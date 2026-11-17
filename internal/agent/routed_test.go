package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/agent/pirpc"
	"applysmith/internal/agent/sessions"
	"applysmith/internal/api/wire"
	"applysmith/internal/gates"
)

// evaluateStub is an evaluate_job that records what it was asked.
func evaluateStub(ran *[]string) Tool {
	return Tool{Name: "evaluate_job", Label: "Evaluating a posting", Description: "Evaluates.", Class: ClassExternalAction, Idempotent: true, OutsideContent: true, PII: PIINone, Timeout: time.Second,
		Schema: `{"type":"object","additionalProperties":false,"required":["url"],"properties":{"url":{"type":"string"}}}`,
		Run: func(_ context.Context, c ToolCall) (ToolResult, error) {
			var a struct{ URL string }
			_ = json.Unmarshal(c.Args, &a)
			*ran = append(*ran, a.URL)
			return ToolResult{Text: "Started an evaluation of " + a.URL + ".", RunID: "run_7"}, nil
		}}
}

func gateSequence(rows []gates.AuditRow) []string {
	var out []string
	for _, r := range rows {
		out = append(out, string(r.Gate))
	}
	return out
}

func TestRouter_PastedURL_IsAnsweredWithoutAModelAndRecordsItsRule(t *testing.T) {
	var ran []string
	f := newFixture(t, evaluateStub(&ran))
	f.launcher.versionErr = errors.New("pi must not be asked")
	const posting = "https://jobs.example.test/postings/123?ref=board"
	res := f.send(posting)
	end := f.terminal()
	f.idle()
	if end.Type != wire.ChatFinal || !strings.Contains(end.Text, posting) {
		t.Fatalf("terminal = %+v", end)
	}
	if !slices.Equal(ran, []string{posting}) {
		t.Fatalf("the tool ran with %v; want the exact URL once", ran)
	}
	if len(f.launcher.started()) != 0 || f.launcher.versions != 0 {
		t.Fatal("a pi process was involved in a routed turn")
	}
	turns, _ := f.sessions.Turns(context.Background(), res.SessionID)
	if len(turns) != 1 || turns[0].Origin != sessions.OriginRouter || turns[0].RuleID != RuleURLOnly || turns[0].Status != sessions.TurnFinal {
		t.Fatalf("turn record = %+v", turns)
	}
	got := types(f.sink.events(res.SessionID))
	if got != "turn.start tool.call tool.result token final" {
		t.Fatalf("events = %s", got)
	}
	msgs, _ := f.sessions.Messages(context.Background(), res.SessionID)
	var roles []string
	for _, m := range msgs {
		roles = append(roles, string(m.Role))
	}
	if strings.Join(roles, " ") != "user tool_call tool_result assistant" {
		t.Fatalf("stored rows = %v", roles)
	}
	rows, _ := f.sessions.Decisions(context.Background(), res.TurnID)
	var confirm gates.AuditRow
	for _, r := range rows {
		if r.Gate == GateConfirmation {
			confirm = r
		}
	}
	if confirm.Verdict != gates.AllowWithTrace || confirm.Code != CodeActionNamed {
		t.Fatalf("router-chosen evaluation: %+v, want allow-with-trace without a confirmation", confirm)
	}
}

func mustDecisions(t *testing.T, f *fixture, turnID string) []gates.AuditRow {
	t.Helper()
	rows, err := f.sessions.Decisions(context.Background(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestRouter_ApplyURL_GoesThroughTheChainAndSaysThatChatCannotStartItYet(t *testing.T) {
	f := newFixture(t)
	res := f.send("apply to https://jobs.example.test/p/9?gh_jid=77")
	end := f.terminal()
	f.idle()
	turns, _ := f.sessions.Turns(context.Background(), res.SessionID)
	if turns[0].Origin != sessions.OriginRouter || turns[0].RuleID != RuleApplyURL {
		t.Fatalf("turn = %+v", turns[0])
	}
	if end.Type != wire.ChatFinal || !strings.Contains(end.Text, "not available in this build") {
		t.Fatalf("terminal = %+v", end)
	}
	rows := mustDecisions(t, f, res.TurnID)
	if len(rows) != 6 || rows[0].Tool != "autofill_application" {
		t.Fatalf("audit = %v", gateSequence(rows))
	}
	if len(f.launcher.started()) != 0 {
		t.Fatal("a model was asked about an apply request")
	}
}

func TestRouter_ScheduledChannelNeverRoutesAnAction(t *testing.T) {
	var ran []string
	f := newFixture(t, evaluateStub(&ran))
	f.launcher.script = standard(t, "turn-tool-then-answer.jsonl", replayOptions{})
	sess, _ := f.sessions.Create(context.Background(), sessions.Scheduled, "")
	res, err := f.agent.Send(context.Background(), SendRequest{SessionID: sess.ID, Channel: ChannelScheduled, Text: "https://jobs.example.test/p/2"})
	if err != nil {
		t.Fatal(err)
	}
	f.terminal()
	f.idle()
	turns, _ := f.sessions.Turns(context.Background(), res.SessionID)
	if len(ran) != 0 || turns[0].Origin != sessions.OriginModel {
		t.Fatalf("ran %v, turn %+v", ran, turns[0])
	}
}

func TestRouter_HeldRoutedCallRunsNothingAndSaysWhy(t *testing.T) {
	ran := false
	gated := Tool{Name: "answer_bank_save", Label: "Adding an answer", Description: "Adds.", Class: ClassWriteLocal, RequiresApproval: true, PII: PIIHigh, Timeout: time.Second,
		Schema: `{"type":"object","additionalProperties":false,"properties":{}}`,
		Run:    func(context.Context, ToolCall) (ToolResult, error) { ran = true; return ToolResult{}, nil }}
	f := newFixture(t, gated)
	ctx := context.Background()
	sess, _ := f.sessions.Create(ctx, sessions.Dashboard, "")
	turn := &Turn{ID: "turn_h", SessionID: sess.ID, Channel: ChannelDashboard, Origin: OriginRouter, UserMessage: "add it"}
	var evs []wire.ChatEvent
	end := f.bridge.RunRouted(ctx, turn, Route{RuleID: "test", Tool: "answer_bank_save", Args: json.RawMessage(`{}`)}, func(e wire.ChatEvent) { evs = append(evs, e) })
	if ran || end.Status != sessions.TurnFinal {
		t.Fatalf("ran %v, end %+v", ran, end)
	}
	if got := types(evs); got != "turn.start tool.call tool.held tool.result token final" {
		t.Fatalf("events = %s", got)
	}
	if last := evs[len(evs)-1]; !strings.Contains(last.Text, "waiting for your confirmation") || !strings.Contains(last.Text, "Nothing has run") {
		t.Fatalf("final = %+v", last)
	}
}

// A routed call the guard would hold is first put to its tool's check, as a
// call the model chose is: a card for a call the tool then refuses would ask
// the user to approve what changes nothing.
func TestRouter_AHeldRoutedCallItsToolWouldRefuseMakesNoCard(t *testing.T) {
	ran := false
	gated := Tool{Name: "answer_bank_save", Label: "Adding an answer", Description: "Adds.", Class: ClassWriteLocal, RequiresApproval: true, PII: PIIHigh, Timeout: time.Second,
		Schema: `{"type":"object","additionalProperties":false,"properties":{}}`,
		Conflict: func(context.Context, ToolCall) error {
			return fmt.Errorf("%w: the answer must be one of: Yes, No", ErrRefused)
		},
		Run: func(context.Context, ToolCall) (ToolResult, error) { ran = true; return ToolResult{}, nil }}
	f := newFixture(t, gated)
	ctx := context.Background()
	sess, _ := f.sessions.Create(ctx, sessions.Dashboard, "")
	turn := &Turn{ID: "turn_r", SessionID: sess.ID, Channel: ChannelDashboard, Origin: OriginRouter, UserMessage: "add it"}
	var evs []wire.ChatEvent
	end := f.bridge.RunRouted(ctx, turn, Route{RuleID: "test", Tool: "answer_bank_save", Args: json.RawMessage(`{}`)}, func(e wire.ChatEvent) { evs = append(evs, e) })
	if ran || end.Status != sessions.TurnFinal {
		t.Fatalf("ran %v, end %+v", ran, end)
	}
	if got := types(evs); got != "turn.start tool.call tool.result token final" {
		t.Fatalf("events = %s; want no hold", got)
	}
	if last := evs[len(evs)-1]; !strings.Contains(last.Text, "must be one of: Yes, No") || strings.Contains(last.Text, "waiting for your confirmation") {
		t.Fatalf("final = %+v", last)
	}
	refusedByTool := false
	for _, r := range mustDecisions(t, f, turn.ID) {
		refusedByTool = refusedByTool || r.Code == CodeToolRefused
	}
	if !refusedByTool {
		t.Fatalf("decisions = %+v; want the tool's refusal audited", mustDecisions(t, f, turn.ID))
	}
}

func TestRouter_ToolMissingFromTheBuildFallsBackToTheModelLoop(t *testing.T) {
	f := newFixture(t) // no evaluate_job registered
	f.launcher.script = standard(t, "turn-tool-then-answer.jsonl", replayOptions{})
	res := f.send("https://jobs.example.test/p/3")
	f.terminal()
	f.idle()
	if turns, _ := f.sessions.Turns(context.Background(), res.SessionID); turns[0].Origin != sessions.OriginModel {
		t.Fatalf("turn = %+v", turns[0])
	}
}

func TestRouter_RoutedTurnRefusesSteeringAndCancelStopsIt(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	slow := Tool{Name: "submissions_recent", Label: "Reading", Description: "Lists.", Class: ClassRead, PII: PIILow, Timeout: 5 * time.Second,
		Schema: `{"type":"object","additionalProperties":false,"properties":{}}`,
		Run: func(ctx context.Context, _ ToolCall) (ToolResult, error) {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return ToolResult{}, ctx.Err()
			}
			return ToolResult{Text: "late"}, nil
		}}
	f := newFixture(t, slow)
	res := f.send("what did I submit today?")
	<-started
	if _, _, err := f.agent.Steer(res.SessionID, "also yesterday"); !errors.Is(err, ErrNoTurn) {
		t.Fatalf("steering a routed turn = %v; want the conflict error", err)
	}
	if _, err := f.agent.Cancel(res.SessionID); err != nil {
		t.Fatal(err)
	}
	end := f.terminal()
	f.idle()
	if end.Type != wire.ChatError || end.Reason != pirpc.ReasonCancelled {
		t.Fatalf("terminal = %+v", end)
	}
	close(release)
	turns, _ := f.sessions.Turns(context.Background(), res.SessionID)
	if turns[0].Status != sessions.TurnError || turns[0].Reason != pirpc.ReasonCancelled {
		t.Fatalf("turn = %+v", turns[0])
	}
}
