package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	harnessv "applysmith/internal/agent/harness"
	"applysmith/internal/agent/sessions"
	"applysmith/internal/api/wire"
	"applysmith/internal/gates"
)

type bridgeEnv struct {
	f      *fixture
	token  string
	run    *run
	turn   *Turn
	events []wire.ChatEvent
	mu     sync.Mutex
}

func newBridgeEnv(t *testing.T, ch Channel, message string, tools ...Tool) *bridgeEnv {
	t.Helper()
	b := &bridgeEnv{f: newFixture(t, append(guardTools(), tools...)...)}
	sess, err := b.f.sessions.Create(context.Background(), sessions.Dashboard, "")
	if err != nil {
		t.Fatal(err)
	}
	b.turn = &Turn{ID: "turn_b", SessionID: sess.ID, Channel: ch, Origin: OriginModel, UserMessage: message}
	b.token, b.run, err = b.f.bridge.open(context.Background(), b.turn, func(e wire.ChatEvent) {
		b.mu.Lock()
		b.events = append(b.events, e)
		b.mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.f.bridge.close(b.token, b.run) })
	return b
}

func (b *bridgeEnv) call(t *testing.T, id, name, args string) wire.AgentToolResult {
	t.Helper()
	res, err := b.f.bridge.ToolCall(context.Background(), b.token, wire.AgentToolCall{ToolCallID: id, Name: name, Args: []byte(args)})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestBridge_CredentialIsPerProcessAndEndsWithIt(t *testing.T) {
	b := newBridgeEnv(t, ChannelDashboard, "hello")
	if !b.f.bridge.Authenticate(b.token) || b.f.bridge.Authenticate("") || b.f.bridge.Authenticate("not-a-token") {
		t.Fatal("authentication")
	}
	if _, err := b.f.bridge.Catalog("not-a-token"); !errors.Is(err, ErrBridgeAuth) {
		t.Fatalf("catalog with a bad token = %v", err)
	}
	if _, err := b.f.bridge.ToolCall(context.Background(), "not-a-token", wire.AgentToolCall{Name: "gmail_signals"}); !errors.Is(err, ErrBridgeAuth) {
		t.Fatalf("tool call with a bad token = %v", err)
	}
	b.f.bridge.close(b.token, b.run)
	if b.f.bridge.Authenticate(b.token) {
		t.Fatal("the credential outlived the process")
	}
	if b.run.callCtx.Err() == nil {
		t.Fatal("calls running for the process were not cancelled")
	}
}

func TestBridge_CatalogAndHelloFollowTheTurnsChannel(t *testing.T) {
	b := newBridgeEnv(t, ChannelScheduled, "digest")
	cat, err := b.f.bridge.Catalog(b.token)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range cat.Tools {
		names = append(names, s.Name)
	}
	if strings.Contains(strings.Join(names, " "), "save_fact") || !strings.Contains(strings.Join(names, " "), "gmail_signals") {
		t.Fatalf("scheduled catalog = %v", names)
	}
	if _, err := b.f.bridge.Hello(b.token, wire.AgentHello{Tools: append(names, "bash")}); !errors.Is(err, ErrCatalogMismatch) {
		t.Fatalf("hello with a built-in tool = %v", err)
	}
	select {
	case <-b.run.helloBadC:
	default:
		t.Fatal("a mismatch did not mark the process unusable")
	}
	if _, err := b.f.bridge.Hello(b.token, wire.AgentHello{Tools: append(names, "save_fact")}); !errors.Is(err, ErrCatalogMismatch) {
		t.Fatalf("hello with a tool outside the envelope = %v", err)
	}
	res, err := b.f.bridge.Hello(b.token, wire.AgentHello{Tools: names})
	if err != nil || int(res.Tools) != len(names) {
		t.Fatalf("matching hello = %+v, %v", res, err)
	}
}

func TestBridge_UnknownToolIsDeniedAndAudited(t *testing.T) {
	b := newBridgeEnv(t, ChannelDashboard, "hello")
	res := b.call(t, "call_1", "shell", `{"cmd":"ls"}`)
	if !res.IsError || !strings.Contains(res.Text, CodeUnknownTool) {
		t.Fatalf("result = %+v", res)
	}
	rows := mustDecisions(t, b.f, "turn_b")
	if len(rows) != 1 || rows[0].Tool != "shell" || rows[0].Code != CodeUnknownTool || rows[0].Verdict != gates.Deny {
		t.Fatalf("audit = %+v", rows)
	}
}

func TestBridge_AToolOutsideTheScheduledEnvelopeIsDeniedEvenIfNamed(t *testing.T) {
	b := newBridgeEnv(t, ChannelScheduled, "digest")
	res := b.call(t, "call_1", "save_fact", `{"body":"x"}`)
	if !res.IsError || !strings.Contains(res.Text, CodeUnknownTool) {
		t.Fatalf("result = %+v", res)
	}
}

func TestBridge_ArgumentsOutsideTheSchemaAreRejectedBeforeExecution(t *testing.T) {
	ran := false
	probe := Tool{Name: "tracker_query", Label: "Counting", Description: "Counts.", Class: ClassRead, PII: PIILow, Timeout: time.Second,
		Schema: `{"type":"object","additionalProperties":false,"properties":{"status":{"type":"string"}}}`,
		Run:    func(context.Context, ToolCall) (ToolResult, error) { ran = true; return ToolResult{Text: "n"}, nil }}
	b := newBridgeEnv(t, ChannelDashboard, "hello", probe)
	res := b.call(t, "call_1", "tracker_query", `{"status":"applied","limit":3}`)
	if !res.IsError || !strings.Contains(res.Text, "limit") || ran {
		t.Fatalf("result = %+v, ran = %v", res, ran)
	}
	rows := mustDecisions(t, b.f, "turn_b")
	if len(rows) != 1 || rows[0].Gate != GateArguments || rows[0].Code != CodeInvalidArguments {
		t.Fatalf("audit = %+v", rows)
	}
	if res := b.call(t, "call_2", "tracker_query", `{"status":"applied","step":"Counting"}`); res.IsError || !ran {
		t.Fatalf("a call with only the reserved step added was refused: %+v", res)
	}
}

func TestBridge_HeldCallDoesNotRunAndTheModelIsTold(t *testing.T) {
	ran := false
	b := newBridgeEnv(t, ChannelDashboard, "what do you think?", Tool{Name: "save_fact", Label: "Saving", Description: "Saves.", Class: ClassWriteLocal, PII: PIINone, Timeout: time.Second,
		Schema: `{"type":"object","additionalProperties":false,"properties":{"body":{"type":"string"}}}`,
		Run:    func(context.Context, ToolCall) (ToolResult, error) { ran = true; return ToolResult{Text: "saved"}, nil }})
	res := b.call(t, "call_1", "save_fact", `{"body":"x"}`)
	if ran || res.IsError || !strings.HasPrefix(res.Text, "Held:") || !strings.Contains(res.Text, "Do not retry") {
		t.Fatalf("result = %+v, ran = %v", res, ran)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.events) != 1 || b.events[0].Type != wire.ChatToolHeld || b.events[0].Tool != "save_fact" || b.events[0].Code != CodeIntentNotStated {
		t.Fatalf("events = %+v", b.events)
	}
}

func TestBridge_RefusalIsAResultAndNothingRuns(t *testing.T) {
	b := newBridgeEnv(t, ChannelTelegram, "apply to https://jobs.example.test/p/1")
	res := b.call(t, "call_1", "apply_from_list", `{"submit":true}`)
	if !res.IsError || !strings.Contains(res.Text, gates.CodeSubmitDashboardOnly) {
		t.Fatalf("result = %+v", res)
	}
	rows := mustDecisions(t, b.f, "turn_b")
	if last := rows[len(rows)-1]; last.Verdict != gates.Deny || last.Code != gates.CodeSubmitDashboardOnly || last.Caller != gates.CallerTelegram {
		t.Fatalf("audit = %+v", rows)
	}
}

// A repeated read is answered from the first result without running or
// counting, until a call that may change what it reads; past MaxRepeats the
// turn is told to answer.
func TestBridge_ARepeatedReadIsAnsweredFromTheFirstAndDoesNotCount(t *testing.T) {
	b := newBridgeEnv(t, ChannelDashboard, "remember that I like Go and check my mail")
	first := b.call(t, "c1", "gmail_signals", `{}`)
	again := b.call(t, "c2", "gmail_signals", `{"step":"Reading the mail again"}`)
	if first.IsError || !strings.HasPrefix(again.Text, "You already made this exact call") || !strings.HasSuffix(again.Text, first.Text) {
		t.Fatalf("first = %+v, again = %+v", first, again)
	}
	if n := b.run.calls.Load(); n != 1 {
		t.Fatalf("calls counted = %d, want 1", n)
	}
	if last := mustDecisions(t, b.f, "turn_b"); last[len(last)-1].Code != CodeRepeatedCall {
		t.Fatalf("audit = %+v", last[len(last)-1])
	}
	if kept, ok := b.run.peekResult("c2"); !ok || kept == "" {
		t.Errorf("the repeat has no recorded result: %q", kept)
	}
	b.call(t, "c3", "save_fact", `{"body":"likes Go"}`)
	if fresh := b.call(t, "c4", "gmail_signals", `{}`); strings.HasPrefix(fresh.Text, "You already made") {
		t.Fatalf("a read after a write was answered from before it: %q", fresh.Text)
	}
	for i := range MaxRepeats - 1 { // c2 was the first repeat
		if res := b.call(t, fmt.Sprintf("r%d", i), "gmail_signals", `{}`); res.FinalOnly {
			t.Fatalf("repeat %d ended the tools: %+v", i, res)
		}
	}
	if res := b.call(t, "r9", "gmail_signals", `{}`); !res.IsError || !res.FinalOnly {
		t.Fatalf("a repeat past the cap = %+v", res)
	}
}

// A draft that did not come back says so plainly, so the model does not write
// one in its place, and the answer carries the limitation either way.
func TestBridge_AFailedDraftSaysNothingWasDrafted(t *testing.T) {
	failing := Tool{Name: "draft_outreach", Label: "Drafting an outreach message", Description: "Drafts.", Class: ClassDraft, PII: PIILow, Timeout: time.Second,
		Schema: `{"type":"object","additionalProperties":false,"properties":{}}`,
		Run: func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{}, errors.New("the model is busy")
		}}
	b := newBridgeEnv(t, ChannelDashboard, "draft a note to the recruiter", failing)
	res := b.call(t, "c1", "draft_outreach", `{}`)
	if !res.IsError || !strings.Contains(res.Text, "The tool failed: the model is busy") || !strings.Contains(res.Text, "do not write the draft yourself") {
		t.Fatalf("result = %+v", res)
	}
	if env := b.turn.EnvelopeFor("Here is a draft I wrote."); len(env.Limitations) != 1 || !strings.Contains(env.Limitations[0], "Drafting an outreach message did not finish (error)") {
		t.Fatalf("limitations = %q", env.Limitations)
	}
	if again := b.call(t, "c2", "draft_outreach", `{}`); strings.HasPrefix(again.Text, "You already made") {
		t.Fatalf("a failed call was answered from before instead of run again: %q", again.Text)
	}
}

func TestBridge_ToolLimit_TwelfthCallEndsToolsAndAThirteenthIsRefused(t *testing.T) {
	b := newBridgeEnv(t, ChannelDashboard, "hello")
	for i := 1; i <= MaxToolCalls; i++ {
		res := b.call(t, "call_"+string(rune('a'+i)), "propose_rule", fmt.Sprintf(`{"text":"rule %d"}`, i))
		if res.IsError || res.FinalOnly != (i == MaxToolCalls) {
			t.Fatalf("call %d = %+v", i, res)
		}
	}
	if env := b.turn.EnvelopeFor(""); len(env.Limitations) != 1 || !strings.Contains(env.Limitations[0], "all 12 tool calls") {
		t.Errorf("limitations at the limit = %q", env.Limitations)
	}
	res := b.call(t, "call_13", "propose_rule", `{"text":"rule 13"}`)
	if !res.IsError || !res.FinalOnly || !strings.Contains(res.Text, "final answer") {
		t.Fatalf("13th call = %+v", res)
	}
	rows := mustDecisions(t, b.f, "turn_b")
	if last := rows[len(rows)-1]; last.Code != CodeToolLimit || last.Verdict != gates.Deny {
		t.Fatalf("audit = %+v", last)
	}
	// Calls pi answered itself count too.
	c := newBridgeEnv(t, ChannelDashboard, "hello")
	for i := 0; i < MaxToolCalls; i++ {
		c.f.bridge.auditUnknown(context.Background(), c.run, "shell", "u"+string(rune('a'+i)), []byte(`{}`))
	}
	if res := c.call(t, "call_x", "gmail_signals", `{}`); !res.IsError || !strings.Contains(res.Text, "final answer") {
		t.Fatalf("a call after twelve unknown ones = %+v", res)
	}
}

// A turn bound to one run works an application page by page: it may make
// MaxRunToolCalls calls, with or without a stepwise one.
func TestBridge_ATurnBoundToARunHasTheRunsCallLimit(t *testing.T) {
	b := newBridgeEnv(t, ChannelDashboard, "Finish the application.")
	b.turn.FinishRun = "run_1"
	for i := 1; i <= MaxRunToolCalls; i++ {
		if res := b.call(t, fmt.Sprintf("c%d", i), "propose_rule", fmt.Sprintf(`{"text":"rule %d"}`, i)); res.IsError || res.FinalOnly != (i == MaxRunToolCalls) {
			t.Fatalf("call %d of %d = %+v", i, MaxRunToolCalls, res)
		}
	}
	if res := b.call(t, "past", "propose_rule", `{"text":"one more"}`); !res.IsError || !strings.Contains(res.Text, "final answer") {
		t.Fatalf("a call past the limit = %+v", res)
	}
}

// A turn that called a stepwise tool (one control or one answer per call) may
// make MaxStepwiseToolCalls calls, the last of which ends its tools (AC-5). A
// stepwise tool the turn may not use leaves the shorter limit.
func TestBridge_AStepwiseToolLengthensTheCallLimit(t *testing.T) {
	step := func(class Class) Tool {
		return Tool{Name: "run_questions", Label: "Reading", Description: "Reads.", Class: class, Stepwise: true, PII: PIINone, Timeout: time.Second,
			Schema: `{"type":"object","additionalProperties":false,"properties":{}}`, Run: noop}
	}
	for _, c := range []struct {
		name  string
		ch    Channel
		class Class
		limit int
	}{
		{"used", ChannelDashboard, ClassRead, MaxStepwiseToolCalls},
		{"outside the envelope", ChannelScheduled, ClassExternalAction, MaxToolCalls},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newBridgeEnv(t, c.ch, "hello", step(c.class))
			b.call(t, "c1", "run_questions", `{}`)
			for i := 2; i <= c.limit; i++ {
				if res := b.call(t, fmt.Sprintf("c%d", i), "propose_rule", fmt.Sprintf(`{"text":"rule %d"}`, i)); res.IsError || res.FinalOnly != (i == c.limit) {
					t.Fatalf("call %d of %d = %+v", i, c.limit, res)
				}
			}
			if env := b.turn.EnvelopeFor(""); len(env.Limitations) != 1 || !strings.Contains(env.Limitations[0], fmt.Sprintf("all %d tool calls", c.limit)) {
				t.Errorf("limitations at the limit = %q", env.Limitations)
			}
			if res := b.call(t, "past", "propose_rule", `{"text":"one more"}`); !res.IsError || !strings.Contains(res.Text, "final answer") {
				t.Fatalf("a call past the limit = %+v", res)
			}
		})
	}
}

// A window changes on its own, so a fresh read made again runs again instead
// of being answered from the first look.
func TestBridge_AFreshReadRunsAgainWhenRepeated(t *testing.T) {
	runs := 0
	look := Tool{Name: "desktop_look", Label: "Looking", Description: "Looks.", Class: ClassRead, Fresh: true, PII: PIINone, Timeout: time.Second,
		Schema: `{"type":"object","additionalProperties":false,"properties":{}}`,
		Run: func(context.Context, ToolCall) (ToolResult, error) {
			runs++
			return ToolResult{Text: fmt.Sprintf("look %d", runs)}, nil
		}}
	b := newBridgeEnv(t, ChannelDashboard, "look at the window", look)
	b.call(t, "c1", "desktop_look", `{}`)
	if again := b.call(t, "c2", "desktop_look", `{}`); runs != 2 || strings.HasPrefix(again.Text, "You already made") || !strings.Contains(again.Text, "look 2") {
		t.Fatalf("runs = %d, the repeat = %q", runs, again.Text)
	}
}

// A tool's images reach the model only when the turn's model takes images
// (AC-3); a text-only model gets the text alone.
func TestBridge_ImagesReachOnlyAModelThatTakesImages(t *testing.T) {
	png := []byte("\x89PNG\r\n")
	look := Tool{Name: "desktop_look", Label: "Looking", Description: "Looks.", Class: ClassRead, Fresh: true, PII: PIINone, Timeout: time.Second,
		Schema: `{"type":"object","additionalProperties":false,"properties":{}}`,
		Run: func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{Text: "controls", Images: []Image{{MIMEType: "image/png", Data: png}}}, nil
		}}
	for _, takes := range []bool{true, false} {
		b := newBridgeEnv(t, ChannelDashboard, "look at the window", look)
		b.turn.TakesImages = takes
		res := b.call(t, "c1", "desktop_look", `{}`)
		want := []wire.AgentToolImage(nil)
		if takes {
			want = []wire.AgentToolImage{{MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(png)}}
		}
		if !slices.Equal(res.Images, want) || !strings.Contains(res.Text, "controls") {
			t.Fatalf("takes images %v: result = %+v", takes, res)
		}
	}
}

func TestBridge_ResultsAreCutForTheModelAndKeptForTheRecord(t *testing.T) {
	big := strings.Repeat("x", 300<<10)
	b := newBridgeEnv(t, ChannelDashboard, "hello", Tool{Name: "tracker_query", Label: "Counting", Description: "Counts.", Class: ClassRead, PII: PIILow, Timeout: time.Second,
		Schema: `{"type":"object","additionalProperties":false,"properties":{}}`,
		Run:    func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{Text: big}, nil }})
	res := b.call(t, "call_1", "tracker_query", `{}`)
	if n := len([]rune(res.Text)); n > ModelResultLimit+150 || !strings.Contains(res.Text, "[truncated:") {
		t.Fatalf("the model saw %d characters", n)
	}
	kept, ok := b.run.takeResult("call_1")
	if !ok || len(kept.text) != sessions.MaxResultBytes {
		t.Fatalf("the record keeps %d bytes", len(kept.text))
	}
}

func TestBridge_ToolFailuresTimeoutsAndCancellationAreResults(t *testing.T) {
	started := make(chan struct{})
	tools := []Tool{
		{Name: "tracker_query", Label: "Counting", Description: "Counts.", Class: ClassRead, PII: PIILow, Timeout: time.Second,
			Schema: `{"type":"object","additionalProperties":false,"properties":{}}`,
			Run: func(context.Context, ToolCall) (ToolResult, error) {
				return ToolResult{}, errors.New("database is locked")
			}},
		{Name: "find_report", Label: "Finding", Description: "Finds.", Class: ClassRead, PII: PIILow, Timeout: 30 * time.Millisecond,
			Schema: `{"type":"object","additionalProperties":false,"properties":{}}`,
			Run: func(ctx context.Context, _ ToolCall) (ToolResult, error) {
				<-ctx.Done()
				return ToolResult{}, ctx.Err()
			}},
		{Name: "scan_portal", Label: "Scanning", Description: "Scans.", Class: ClassExternalAction, Idempotent: true, PII: PIINone, Timeout: time.Second,
			Schema: `{"type":"object","additionalProperties":false,"properties":{}}`,
			Run: func(ctx context.Context, _ ToolCall) (ToolResult, error) {
				close(started)
				<-ctx.Done()
				return ToolResult{}, ctx.Err()
			}},
	}
	b := newBridgeEnv(t, ChannelDashboard, "please scan it", tools...)
	if res := b.call(t, "c1", "tracker_query", `{}`); !res.IsError || !strings.Contains(res.Text, "database is locked") {
		t.Fatalf("failure = %+v", res)
	}
	if res := b.call(t, "c2", "find_report", `{}`); !res.IsError || !strings.Contains(res.Text, "did not finish") {
		t.Fatalf("timeout = %+v", res)
	}
	type answer struct {
		res wire.AgentToolResult
		err error
	}
	done := make(chan answer, 1)
	go func() {
		res, err := b.f.bridge.ToolCall(context.Background(), b.token, wire.AgentToolCall{ToolCallID: "c3", Name: "scan_portal", Args: []byte(`{}`)})
		done <- answer{res, err}
	}()
	select {
	case <-started:
	case a := <-done:
		t.Fatalf("the scan answered before it ran: %+v, %v", a.res, a.err)
	}
	b.run.cancelCalls()
	if a := <-done; a.err != nil || !a.res.IsError || !strings.Contains(a.res.Text, "cancelled") {
		t.Fatalf("cancellation = %+v, %v", a.res, a.err)
	}
	var outcome string
	row := b.f.st.Read().QueryRow(`SELECT outcome FROM agent_claims WHERE tool = 'scan_portal' AND key = 'c3'`)
	if err := row.Scan(&outcome); err != nil || outcome != "cancelled" {
		t.Fatalf("claim outcome = %q, %v", outcome, err)
	}
}

// A tool marked OutsideContent taints only when its result holds outside text:
// an empty set or a status the product wrote does not, and a failed call does.
func TestBridge_AResultWithNoOutsideTextDoesNotTaint(t *testing.T) {
	reader := func(name string, run ToolFunc) Tool {
		return Tool{Name: name, Label: name, Description: name + ".", Class: ClassRead, OutsideContent: true, PII: PIINone, Timeout: time.Second,
			Schema: `{"type":"object","additionalProperties":false,"properties":{}}`, Run: run}
	}
	b := newBridgeEnv(t, ChannelDashboard, "check the mail, then evaluate https://jobs.example.test/p/1",
		reader("mail_empty", func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{Text: "Nothing in the inbox matches.", Clean: true}, nil
		}),
		reader("mail_rows", func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{Text: "- subject from a stranger"}, nil
		}),
		reader("mail_broken", func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{Clean: true}, errors.New("boom")
		}))
	if res := b.call(t, "c1", "mail_empty", `{}`); res.IsError {
		t.Fatalf("empty read = %+v", res)
	}
	if b.turn.Untrusted() {
		t.Fatal("an empty result tainted the turn")
	}
	if res := b.call(t, "c2", "evaluate_job", `{"url":"https://jobs.example.test/p/1"}`); strings.HasPrefix(res.Text, "Held:") {
		t.Fatalf("a model-chosen action after an empty read was held: %+v", res)
	}
	if res := b.call(t, "c3", "mail_broken", `{}`); !res.IsError {
		t.Fatalf("failed read = %+v", res)
	}
	if !b.turn.Untrusted() {
		t.Fatal("a failed call must keep the taint")
	}
}

func TestBridge_AToolThatReturnsOutsideContentTaintsTheTurn(t *testing.T) {
	b := newBridgeEnv(t, ChannelDashboard, "scan it then evaluate https://jobs.example.test/p/1")
	if b.turn.Untrusted() {
		t.Fatal("a fresh turn is trusted")
	}
	if res := b.call(t, "c1", "evaluate_job", `{"url":"https://jobs.example.test/p/1"}`); res.IsError || !strings.Contains(res.Text, "\nok\n") {
		t.Fatalf("first evaluation = %+v", res)
	}
	if !b.turn.Untrusted() {
		t.Fatal("outside content was returned and the turn is still trusted")
	}
	if res := b.call(t, "c2", "evaluate_job", `{"url":"https://jobs.example.test/p/2"}`); !strings.HasPrefix(res.Text, "Held:") {
		t.Fatalf("the next evaluation = %+v", res)
	}
	if res := b.call(t, "c3", "gmail_signals", `{}`); res.IsError {
		t.Fatalf("a read after taint = %+v", res)
	}
}

func TestBridge_AnIdempotentCallRunsOnceEvenWhenSentTwice(t *testing.T) {
	runs := 0
	b := newBridgeEnv(t, ChannelDashboard, "please scan it", Tool{Name: "scan_portal", Label: "Scanning", Description: "Scans.", Class: ClassExternalAction, Idempotent: true, PII: PIINone, Timeout: time.Second,
		Schema: `{"type":"object","additionalProperties":false,"properties":{}}`,
		Run: func(context.Context, ToolCall) (ToolResult, error) {
			runs++
			return ToolResult{Text: "started", RunID: "run_1"}, nil
		}})
	if res := b.call(t, "call_1", "scan_portal", `{}`); res.IsError {
		t.Fatalf("first = %+v", res)
	}
	if res := b.call(t, "call_1", "scan_portal", `{}`); !res.IsError || !strings.Contains(res.Text, CodeIdempotencyClaim) {
		t.Fatalf("replay = %+v", res)
	}
	if runs != 1 {
		t.Fatalf("ran %d times", runs)
	}
	if got := b.run.startedRuns(); len(got) != 1 || got[0] != "run_1" {
		t.Fatalf("runs = %v", got)
	}
}

type fixedHarness struct {
	h     harnessv.Harness
	reads int
}

func (f *fixedHarness) Active(context.Context) (harnessv.Harness, error) {
	f.reads++
	return f.h, nil
}

func TestBridge_TheHarnessIsPinnedPerProcessAndRecordedOnTheTurn(t *testing.T) {
	b := newBridgeEnv(t, ChannelDashboard, "hello")
	ctx := context.Background()
	if err := b.f.sessions.StartTurn(ctx, sessions.Turn{ID: b.turn.ID, SessionID: b.turn.SessionID, Channel: sessions.Dashboard, Origin: string(OriginModel)}); err != nil {
		t.Fatal(err)
	}
	src := &fixedHarness{h: harnessv.Harness{Version: 7, Guidance: []string{"Prefer one call when one is enough."}, ToolNotes: map[string]string{"gmail_signals": "Newest first."}}}
	b.f.bridge.opt.Harness = src
	cat, err := b.f.bridge.Catalog(b.token)
	if err != nil {
		t.Fatal(err)
	}
	noted := false
	for _, s := range cat.Tools {
		noted = noted || (s.Name == "gmail_signals" && strings.HasSuffix(s.Description, "\n\nNote: Newest first."))
	}
	if !noted {
		t.Fatal("the catalog lacks the tool note")
	}
	src.h = harnessv.Harness{Version: 8}
	tc, err := b.f.bridge.TurnContext(ctx, b.token)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tc.SystemPrompt, "- Prefer one call when one is enough.") || src.reads != 1 {
		t.Fatalf("the prompt did not keep the pinned version (reads %d)", src.reads)
	}
	var v int64
	if err := b.f.st.Read().QueryRowContext(ctx, `SELECT harness_version FROM chat_turns WHERE id = ?`, b.turn.ID).Scan(&v); err != nil || v != 7 {
		t.Fatalf("recorded version = %d, %v", v, err)
	}
}
