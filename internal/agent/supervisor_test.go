package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/agent/pirpc"
	"applysmith/internal/agent/sessions"
	"applysmith/internal/api/wire"
	"applysmith/internal/gates"
	"applysmith/internal/llm"
)

func TestSupervisor_ToolThenAnswer_StreamRecordLedgerAndAudit(t *testing.T) {
	f := newFixture(t)
	f.launcher.script = standard(t, "turn-tool-then-answer.jsonl", replayOptions{})
	res := f.send("Tell me about the Applied rows.")
	end := f.terminal()
	f.idle()

	if end.Type != wire.ChatFinal || end.Text == "" {
		t.Fatalf("terminal = %+v", end)
	}
	evs := f.sink.events(res.SessionID)
	if got := types(evs); !strings.HasPrefix(got, "turn.start tool.call tool.result ") || strings.Count(got, "final")+strings.Count(got, "error") != 1 {
		t.Fatalf("stream = %s", got)
	}
	call := evs[1]
	if call.Tool != "tracker_query" || call.Phrase != "Counting applied rows" || call.ToolCallID != "call_1" {
		t.Fatalf("tool.call = %+v", call)
	}
	if r := evs[2]; r.OK == nil || !*r.OK || !strings.Contains(r.Summary, "Rows by status") {
		t.Fatalf("tool.result = %+v", r)
	}

	msgs, _ := f.sessions.Messages(context.Background(), res.SessionID)
	var roles []string
	for _, m := range msgs {
		roles = append(roles, string(m.Role))
	}
	if strings.Join(roles, " ") != "user tool_call tool_result assistant" {
		t.Fatalf("record = %v", roles)
	}
	if msgs[1].Body != `{"status":"Applied"}` {
		t.Fatalf("the call is recorded with the reserved step removed: %q", msgs[1].Body)
	}
	turns, _ := f.sessions.Turns(context.Background(), res.SessionID)
	if len(turns) != 1 || turns[0].Status != sessions.TurnFinal {
		t.Fatalf("turns = %+v", turns)
	}

	if rows := f.router.usage(); len(rows) != 0 {
		t.Fatalf("the gateway writes the ledger rows of a gateway route, the turn wrote %d", len(rows))
	}

	audit, _ := f.sessions.Decisions(context.Background(), res.TurnID)
	if len(audit) == 0 || audit[0].Tool != "tracker_query" || audit[len(audit)-1].Verdict != gates.Allow {
		t.Fatalf("audit = %+v", audit)
	}
}

func TestSupervisor_LaunchPointsPiAtTheGatewayAndNoKeyLeavesThisProcess(t *testing.T) {
	f := newFixture(t)
	f.launcher.script = standard(t, "turn-tool-then-answer.jsonl", replayOptions{})
	f.send("hello")
	f.terminal()
	f.idle()

	p := f.launcher.started()[0]
	args := strings.Join(p.spec.Args, " ")
	for _, want := range []string{"--mode rpc", "--no-builtin-tools", "--no-extensions", "--no-mcp", "--provider applysmith", "--model scripted-1"} {
		if !strings.Contains(args, want) {
			t.Errorf("args lack %q: %s", want, args)
		}
	}
	env := strings.Join(p.spec.Env, "\n")
	if strings.Contains(env, fakeKey()) || strings.Contains(env, "APPLYSMITH_KEY_") {
		t.Error("a provider key reached the child environment")
	}
	if !strings.Contains(env, "NO_PROXY=127.0.0.1,localhost,[::1]") {
		t.Error("the loopback bridge is not exempt from a proxy")
	}
	for _, bad := range []string{"APPLYSMITH_SECRET_API_TOKEN", "must-not-reach-pi"} {
		if strings.Contains(env, bad) {
			t.Errorf("the parent environment leaked %q", bad)
		}
	}
	for _, want := range []string{"PI_CODING_AGENT_DIR=", "PI_OFFLINE=1", "APPLYSMITH_AGENT_URL=http://127.0.0.1:1", "APPLYSMITH_AGENT_TOKEN="} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %q", want)
		}
	}
	if strings.Contains(args, fakeKey()) {
		t.Error("the key is in argv")
	}
	filepath.Walk(f.sup.cfg.AgentDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			b, _ := os.ReadFile(path)
			if strings.Contains(string(b), fakeKey()) {
				t.Errorf("the key was written to %s", path)
			}
		}
		return nil
	})
	models, err := os.ReadFile(filepath.Join(f.sup.cfg.AgentDir, "models.json"))
	if err != nil || !strings.Contains(string(models), `"apiKey": "$APPLYSMITH_AGENT_TOKEN"`) || !strings.Contains(string(models), `"baseUrl": "http://127.0.0.1:1/internal/agent/llm"`) {
		t.Fatalf("models.json = %s, %v", models, err)
	}
	if info, _ := os.Stat(filepath.Join(f.sup.cfg.AgentDir, "models.json")); info.Mode().Perm() != 0o600 {
		t.Errorf("models.json mode = %v", info.Mode())
	}
	ext, err := os.ReadFile(filepath.Join(f.sup.cfg.AgentDir, "extension", "applysmith.mjs"))
	if err != nil || string(ext) != string(extensionSource) {
		t.Error("the extension on disk is not the embedded one")
	}
	if got := p.spec.Dir; !strings.HasSuffix(got, "cwd") {
		t.Errorf("working directory = %s", got)
	}
}

// A turn with no model it can call ends as no_model_route and starts no
// process. The shapes are what Router.AgentRoute makes: no row at all, a row
// the agent runtime cannot call (a fake provider, a provider with no key), and
// the offline simulation, which says what it stands in for. The router's
// messages name a command-line remedy; the dashboard must not.
func TestSupervisor_NoUsableRoute_EndsAsNoModelRouteWithoutACommand(t *testing.T) {
	for name, routeErr := range map[string]error{
		"no row": &llm.Error{Kind: llm.KindNoRoute, Task: llm.AgentTask, Message: "no model is routed for agent-kernel; run `applysmith llm configure --preset <name> --key-stdin`"},
		"a row the runtime cannot call": &llm.Error{Kind: llm.KindUnconfigured, Task: llm.AgentTask,
			Message: "the agent-kernel route names provider f, which the agent runtime cannot call; route it to an API provider with `applysmith llm configure`"},
		"the offline simulation": llm.ErrAgentOffline,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.router.routeErr = routeErr
			f.send("hello")
			end := f.terminal()
			f.idle()
			if end.Type != wire.ChatError || end.Reason != wire.ReasonNoModelRoute || end.Detail == "" || strings.Contains(end.Detail, "applysmith") {
				t.Fatalf("end = %+v", end)
			}
			if offline := strings.Contains(end.Detail, "offline simulation"); offline != (routeErr == llm.ErrAgentOffline) {
				t.Fatalf("detail %q for %s", end.Detail, name)
			}
			if n := len(f.launcher.started()); n != 0 || f.launcher.versions != 0 {
				t.Fatalf("a process or a version check ran: %d, %d", n, f.launcher.versions)
			}
		})
	}
}

func TestSupervisor_CostCapReachedBeforeTheTurn_NamesTheCapAndStartsNoProcess(t *testing.T) {
	f := newFixture(t)
	f.router.spend, f.router.cap, f.router.blocked = 2.05, 2, true
	f.send("hello")
	end := f.terminal()
	f.idle()
	if end.Type != wire.ChatFinal || !strings.Contains(end.Text, "USD 2.00") || !strings.Contains(end.Text, "cap") {
		t.Fatalf("reply = %+v", end)
	}
	if len(f.launcher.started()) != 0 {
		t.Fatal("a process started over the cap")
	}
	// A subscription route adds nothing to the spend, so the cap does not stop it.
	f.router.route = piLoginRoute()
	f.launcher.script = standard(t, "turn-tool-then-answer.jsonl", replayOptions{})
	f.send("again")
	f.terminal()
	f.idle()
	if len(f.launcher.started()) != 1 {
		t.Fatal("a subscription route was stopped by the metered cap")
	}
}

func TestSupervisor_VersionMismatch_NamesBothVersionsAndTheInstallCommand(t *testing.T) {
	f := newFixture(t)
	f.launcher.version = "0.83.0\n"
	f.send("hello")
	end := f.terminal()
	f.idle()
	if end.Type != wire.ChatError || end.Reason != pirpc.ReasonUnavailable {
		t.Fatalf("terminal = %+v", end)
	}
	for _, want := range []string{"0.83.0", pirpc.PinnedVersion, "github.com/earendil-works/pi/releases"} {
		if !strings.Contains(end.Detail, want) {
			t.Errorf("detail lacks %q: %s", want, end.Detail)
		}
	}
	if len(f.launcher.started()) != 0 {
		t.Fatal("a mismatched pi was started")
	}
	if _, err := f.sup.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "applysmith doctor") {
		t.Fatalf("doctor check = %v", err)
	}
}

func piLoginRoute() llm.AgentRoute {
	return llm.AgentRoute{
		Resolved: llm.Resolved{Provider: "pi:openai-codex", Model: "gpt-6.1-sol", Thinking: llm.ThinkMedium, MaxOutputTokens: 4000},
		Config:   llm.ProviderConfig{ID: "pi:openai-codex", Kind: llm.LedgerKindPi}, PiLogin: "openai-codex",
	}
}

func TestSupervisor_PiLoginRoute_LaunchesPiOnItsOwnProviderWithNoKeyAndNoProviderOfOurs(t *testing.T) {
	f := newFixture(t)
	f.router.route = piLoginRoute()
	f.launcher.script = standard(t, "turn-tool-then-answer.jsonl", replayOptions{})
	f.send("hello")
	f.terminal()
	f.idle()

	p := f.launcher.started()[0]
	args := strings.Join(p.spec.Args, " ")
	if !strings.Contains(args, "--provider openai-codex") || strings.Contains(args, "--provider applysmith") || !strings.Contains(args, "--model gpt-6.1-sol") {
		t.Fatalf("args = %s", args)
	}
	for _, kv := range p.spec.Env {
		if strings.HasPrefix(kv, "APPLYSMITH_KEY_") {
			t.Errorf("a key variable reached the process: %s", strings.SplitN(kv, "=", 2)[0])
		}
	}
	models, err := os.ReadFile(filepath.Join(f.sup.cfg.AgentDir, "models.json"))
	if err != nil || strings.Contains(string(models), `"applysmith"`) || strings.Contains(string(models), "baseUrl") {
		t.Fatalf("models.json = %s, %v", models, err)
	}
	if got := f.launcher.auths; len(got) != 1 || got[0] != "openai-codex" {
		t.Fatalf("login checks = %v", got)
	}
}

func TestSupervisor_PiLoginRoute_WithoutALoginTheTurnEndsUnavailableAndNamesTheFix(t *testing.T) {
	f := newFixture(t)
	f.router.route = piLoginRoute()
	f.launcher.authErr = errors.New("not ready")
	res := f.send("hello")
	end := f.terminal()
	f.idle()
	if end.Type != wire.ChatError || end.Reason != pirpc.ReasonUnavailable || !strings.Contains(end.Detail, "applysmith agent login") || !strings.Contains(end.Detail, "openai-codex") {
		t.Fatalf("terminal = %+v", end)
	}
	if len(f.launcher.started()) != 0 {
		t.Fatal("pi started without a login")
	}
	_ = res
}

func TestSupervisor_PiLoginRoute_ASubscriptionTurnIgnoresTheCapAndLedgersAsSubscription(t *testing.T) {
	f := newFixture(t)
	f.router.route = piLoginRoute()
	f.router.spend, f.router.cap, f.router.blocked = 5, 2, true // the cap is reached, but a subscription adds nothing to it
	f.launcher.script = standard(t, "turn-tool-then-answer.jsonl", replayOptions{})
	f.send("hello")
	end := f.terminal()
	f.idle()
	if end.Type != wire.ChatFinal {
		t.Fatalf("terminal = %+v", end)
	}
	if rows := f.router.usage(); len(rows) == 0 {
		t.Fatal("no usage rows")
	}
}
