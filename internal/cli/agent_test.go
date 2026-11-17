package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/agent/sessions"
	"applysmith/internal/store"
)

func (te *testEnv) agentSessions(args ...string) output {
	te.t.Helper()
	return te.run(context.Background(), append([]string{"agent", "sessions"}, append(args, "--workspace", te.ws)...)...)
}

// seedConversations creates a workspace with two conversations; the first has
// a tool call and a checkpoint.
func (te *testEnv) seedConversations() (first, second string) {
	te.t.Helper()
	te.addRow("Example Corp", "Platform Engineer", "https://example.com/jobs/1") // creates the workspace and its database
	te.withStore(func(st *store.Store) {
		ctx := context.Background()
		s := sessions.New(st, nil)
		a, _ := s.Create(ctx, sessions.Dashboard, "Relocation to Exampleville")
		b, _ := s.Create(ctx, sessions.Telegram, "Reply tone")
		first, second = a.ID, b.ID
		for _, m := range []sessions.Message{
			{SessionID: a.ID, TurnID: "t1", Role: sessions.RoleUser, Body: "Should I relocate to Exampleville?", OK: true},
			{SessionID: a.ID, TurnID: "t1", Role: sessions.RoleToolCall, Body: `{"query":"relocation"}`, ToolName: "recall_session", ToolCallID: "c1", OK: true},
			{SessionID: a.ID, TurnID: "t1", Role: sessions.RoleAssistant, Body: "It depends on the offer.", OK: true},
			{SessionID: b.ID, TurnID: "t2", Role: sessions.RoleUser, Body: "Be brief.", OK: true},
		} {
			if _, err := s.Append(ctx, m); err != nil {
				te.t.Fatal(err)
			}
		}
		if _, err := s.AppendResult(ctx, sessions.Message{SessionID: a.ID, TurnID: "t1", ToolName: "recall_session", ToolCallID: "c1", Body: "nothing", OK: true}); err != nil {
			te.t.Fatal(err)
		}
		if _, err := s.AddCheckpoint(ctx, sessions.Checkpoint{SessionID: a.ID, CoveredTo: 3, CoveredTurns: 4, Summary: "Earlier chat about moving.", Tainted: true}); err != nil {
			te.t.Fatal(err)
		}
	})
	return first, second
}

func TestAgentSessionsCLI_ListShowExportDelete(t *testing.T) {
	te := newTestEnv(t)
	first, second := te.seedConversations()

	o := te.agentSessions("list")
	if o.code != 0 || !strings.Contains(o.stdout, "2 conversations") || !strings.Contains(o.stdout, first) || !strings.Contains(o.stdout, "Relocation to Exampleville") || strings.Contains(o.stdout, "[truncated") {
		t.Fatalf("list = %+v", o)
	}
	o = te.agentSessions("list", "--channel", "telegram", "--json")
	var listed struct {
		Total    int          `json:"total"`
		Sessions []sessionRow `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(o.stdout), &listed); err != nil || listed.Total != 1 || listed.Sessions[0].ID != second {
		t.Fatalf("telegram list = %+v, %v", o, err)
	}

	o = te.agentSessions("show", first)
	for _, want := range []string{"[1] user: Should I relocate", "assistant: It depends", "tool_call recall_session", "tool_result recall_session: nothing", "checkpoint 1 covers 4 turns up to message 3 (tainted)"} {
		if o.code != 0 || !strings.Contains(o.stdout, want) {
			t.Fatalf("show lacks %q:\n%+v", want, o)
		}
	}

	out := filepath.Join(t.TempDir(), "chat.json")
	if o = te.agentSessions("export", first, "--out", out); o.code != 0 || !strings.Contains(o.stdout, "wrote 4 messages") {
		t.Fatalf("export = %+v", o)
	}
	raw, _ := os.ReadFile(out)
	var exported sessionExport
	if err := json.Unmarshal(raw, &exported); err != nil || len(exported.Messages) != 4 || len(exported.Checkpoints) != 1 || !exported.Checkpoints[0].Tainted {
		t.Fatalf("export = %s, %v", raw, err)
	}
	if o = te.agentSessions("export", second); o.code != 0 || !strings.Contains(o.stdout, "Be brief.") {
		t.Fatalf("export to stdout = %+v", o)
	}

	if o = te.agentSessions("delete", first); o.code != 0 {
		t.Fatalf("delete = %+v", o)
	}
	if o = te.agentSessions("show", first); o.code != 2 || !strings.Contains(o.stderr, "no such conversation") {
		t.Fatalf("show after delete = %+v", o)
	}
	te.withStore(func(st *store.Store) {
		hits, _ := sessions.New(st, nil).Search(context.Background(), "relocate Exampleville", "")
		if len(hits) != 0 {
			t.Errorf("a deleted conversation is still searchable: %+v", hits)
		}
		var n int
		_ = st.Read().QueryRow(`SELECT count(*) FROM chat_checkpoints`).Scan(&n)
		if n != 0 {
			t.Errorf("%d checkpoints of a deleted conversation remain", n)
		}
	})
}

func TestAgentSessionsCLI_UsageAndBadInput(t *testing.T) {
	te := newTestEnv(t)
	te.seedConversations()
	for name, args := range map[string][]string{
		"no subcommand": {"agent"},
		"unknown":       {"agent", "sessions", "rename"},
		"other group":   {"agent", "memory", "list"},
	} {
		if o := te.run(context.Background(), args...); o.code != 2 || !strings.Contains(o.stderr, "usage:") {
			t.Errorf("%s: %+v", name, o)
		}
	}
	if o := te.agentSessions("show"); o.code != 2 {
		t.Errorf("show without an id: %+v", o)
	}
	if o := te.agentSessions("list", "--channel", "email"); o.code != 2 || !strings.Contains(o.stderr, "not a channel") {
		t.Errorf("bad channel: %+v", o)
	}
	if o := te.agentSessions("delete", "ses_missing"); o.code != 2 {
		t.Errorf("delete of an unknown id: %+v", o)
	}
}

func TestAgentLogin_RunsPiOnTheTerminalInTheProductAgentDirectory(t *testing.T) {
	te := newTestEnv(t)
	te.addRow("Example Corp", "Platform Engineer", "https://example.com/jobs/1")
	var got *exec.Cmd
	te.interactive = func(c *exec.Cmd) error { got = c; return nil }
	te.vars["APPLYSMITH_PI_PATH"] = "/opt/pi/pi"
	out := te.run(context.Background(), "agent", "login", "--workspace", te.ws)
	if out.code != 0 || got == nil {
		t.Fatalf("login = %+v", out)
	}
	dir := filepath.Join(te.ws, store.StateDir, "agent")
	if got.Path != "/opt/pi/pi" || got.Dir != dir {
		t.Fatalf("command = %s in %s", got.Path, got.Dir)
	}
	have := map[string]bool{}
	for _, kv := range got.Env {
		have[kv] = true
	}
	if !have["PI_CODING_AGENT_DIR="+dir] || !have["PI_TELEMETRY=0"] {
		t.Fatalf("env lacks the agent directory: %v", got.Env)
	}
	for _, kv := range got.Env {
		if strings.HasPrefix(kv, "APPLYSMITH_") {
			t.Errorf("a product variable reached pi: %s", strings.SplitN(kv, "=", 2)[0])
		}
	}
	if !strings.Contains(out.stderr, "/login") || !strings.Contains(out.stderr, "pi:openai-codex") {
		t.Fatalf("no instructions: %s", out.stderr)
	}
}
