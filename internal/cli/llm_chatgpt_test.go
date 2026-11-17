package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"applysmith/internal/api/wire"
)

func TestLLMSigninAndSignoutBelongToTheRunningServer(t *testing.T) {
	te := newTestEnv(t)
	ctx := context.Background()
	if out := te.run(ctx, "init", "--workspace", te.ws); out.code != 0 {
		t.Fatal(out)
	}
	for _, cmd := range []string{"signin", "signout"} {
		if o := te.run(ctx, "llm", cmd, "chatgpt", "--workspace", te.ws); o.code != 1 || !strings.Contains(o.stderr, "applysmith serve") {
			t.Errorf("%s with no server: %+v", cmd, o)
		}
		if o := te.run(ctx, "llm", cmd, "gemini", "--workspace", te.ws); o.code != 2 {
			t.Errorf("%s of another provider: %+v", cmd, o)
		}
		// A workspace that does not exist is rejected input, as for every command.
		if o := te.run(ctx, "llm", cmd, "chatgpt", "--workspace", te.ws+"-missing"); o.code != 2 || !strings.Contains(o.stderr, "applysmith init") {
			t.Errorf("%s of a missing workspace: %+v", cmd, o)
		}
	}
}

func TestLLMSigninPrintsTheAddressAndLeavesTheAttemptWithTheServer(t *testing.T) {
	te := newTestEnv(t)
	rs := te.startServe()
	token := strings.TrimSpace(te.run(context.Background(), "pair", "--workspace", te.ws).stdout)

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	var out, errw bytes.Buffer
	e := te.env(&out, &errw)
	var opened string
	e.openURL = func(u string) error { opened = u; stop(); return nil } // the user closes the terminal once the browser opens
	if code := e.run(ctx, []string{"llm", "signin", "chatgpt", "--open", "--workspace", te.ws}); code != 1 {
		t.Fatalf("exit %d: %s", code, errw.String())
	}
	printed := strings.TrimSpace(out.String())
	u, err := url.Parse(printed)
	if err != nil || u.Host != "auth.openai.com" || opened != printed || !strings.Contains(errw.String(), "keeps waiting in the server") {
		t.Fatalf("printed %q opened %q: %s", printed, opened, errw.String())
	}
	code, body := rs.call("GET", "/api/llm/chatgpt", token, nil)
	var st struct{ Data struct{ Pending bool } }
	if err := json.Unmarshal(body, &st); err != nil || code != 200 || !st.Data.Pending {
		t.Fatalf("status %d %s", code, body)
	}

	o := te.run(context.Background(), "llm", "signout", "chatgpt", "--forget", "--workspace", te.ws)
	if o.code != 0 || !strings.Contains(o.stdout, "signed out") || strings.Contains(o.stderr, "did not confirm") {
		t.Fatalf("signout: %+v", o)
	}
	_, body = rs.call("GET", "/api/llm/chatgpt", token, nil)
	if err := json.Unmarshal(body, &st); err != nil || st.Data.Pending {
		t.Fatalf("after sign-out: %s", body)
	}
}

// While the app holds the workspace, the routing commands send their change to
// it, the way Settings, Models does, instead of refusing because the
// workspace is taken.
func TestLLMRoutingCommandsChangeTheRunningServer(t *testing.T) {
	te := newTestEnv(t)
	ctx := context.Background()
	if o := te.run(ctx, "init", "--workspace", te.ws); o.code != 0 {
		t.Fatal(o)
	}
	te.stdin = testKey() + "\n"
	if o := te.run(ctx, "llm", "configure", "--preset", "deepseek", "--key-stdin", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("configure: %+v", o)
	}
	rs := te.startServe()
	token := strings.TrimSpace(te.run(ctx, "pair", "--workspace", te.ws).stdout)
	routing := func() wire.LLMRouting {
		t.Helper()
		_, body := rs.call("GET", "/api/llm/routing", token, nil)
		var r struct{ Data wire.LLMRouting }
		if err := json.Unmarshal(body, &r); err != nil {
			t.Fatalf("routing %s", body)
		}
		return r.Data
	}
	row := func(task string) wire.LLMTaskRoute {
		for _, r := range routing().Tasks {
			if r.Task == task {
				return r
			}
		}
		return wire.LLMTaskRoute{}
	}
	o := te.run(ctx, "llm", "set", "agent-judge", "deepseek/deepseek-flash", "--thinking", "low", "--fallback", "deepseek/deepseek-v4-pro", "--workspace", te.ws)
	if o.code != 0 || !strings.Contains(o.stdout, "agent-judge now uses deepseek/deepseek-flash") {
		t.Fatalf("set: %+v", o)
	}
	if r := row("agent-judge"); r.Provider != "deepseek" || r.Model != "deepseek-flash" || r.Thinking != "low" || r.Source != "table" || len(r.Fallbacks) != 1 || r.Fallbacks[0].Model != "deepseek-v4-pro" {
		t.Errorf("after set: %+v", r)
	}
	if o := te.run(ctx, "llm", "reset", "agent-judge", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("reset: %+v", o)
	}
	if r := row("agent-judge"); r.Source != "preset" || len(r.Fallbacks) != 0 {
		t.Errorf("after reset the row does not follow the preset: %+v", r)
	}
	if o := te.run(ctx, "llm", "cap", "3", "--workspace", te.ws); o.code != 0 || routing().DailyUSDCap != 3 {
		t.Errorf("cap: %+v, routing cap %v", o, routing().DailyUSDCap)
	}
	if o := te.run(ctx, "llm", "group", "set", "quick", "--mode", "order", "--members", "deepseek/deepseek-flash,deepseek/deepseek-v4-pro", "--workspace", te.ws); o.code != 0 || len(routing().Groups["quick"].Members) != 2 {
		t.Errorf("group set: %+v, groups %v", o, routing().Groups)
	}
	if o := te.run(ctx, "llm", "group", "rm", "quick", "--workspace", te.ws); o.code != 0 {
		t.Errorf("group rm: %+v", o)
	}
	if _, ok := routing().Groups["quick"]; ok {
		t.Error("the group is still there after rm")
	}
	if o := te.run(ctx, "llm", "set", "no-such-task", "deepseek/deepseek-flash", "--workspace", te.ws); o.code == 0 || !strings.Contains(o.stderr, "unknown task") {
		t.Errorf("an unknown task: %+v", o)
	}
}
