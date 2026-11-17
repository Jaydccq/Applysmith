package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"applysmith/internal/api/wire"
	"applysmith/internal/llm"
)

// providerStub answers every chat request, as a reply or an HTTP error.
func providerStub(t *testing.T, status int) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 200 {
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":"refused"}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(s.Close)
	return s
}

// toStub sends every provider request to the stub.
type toStub struct{ target *url.URL }

func (s toStub) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = s.target.Scheme, s.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

func llmEnv(t *testing.T, stub *httptest.Server) *testEnv {
	t.Helper()
	te := newTestEnv(t)
	u, _ := url.Parse(stub.URL)
	te.llmOpts = []llm.Option{llm.WithHTTPClient(&http.Client{Transport: toStub{u}})}
	if out := te.run(context.Background(), "init", "--workspace", te.ws); out.code != 0 {
		t.Fatal(out)
	}
	return te
}

func testKey() string { return "sk-" + strings.Repeat("z", 12) + "k9" + strings.Repeat("y", 20) }

func TestLLMKeyFlagIsRefusedAndNothingIsStored(t *testing.T) {
	te := newTestEnv(t)
	out := te.run(context.Background(), "llm", "configure", "--preset", "openrouter", "--key", testKey(), "--workspace", te.ws)
	if out.code != 2 || !strings.Contains(out.stderr, "--key-stdin") || strings.Contains(out.stderr, testKey()) {
		t.Fatalf("out = %+v", out)
	}
	if len(te.backend.Accounts()) != 0 {
		t.Fatalf("keychain holds %v", te.backend.Accounts())
	}
	out = te.run(context.Background(), "llm", "keys", "set", "openrouter", testKey(), "--workspace", te.ws)
	if out.code != 2 || len(te.backend.Accounts()) != 0 {
		t.Fatalf("keys set with an argument: %+v", out)
	}
}

func TestLLMConfigureTestAndStatusShowPresenceOnly(t *testing.T) {
	te := llmEnv(t, providerStub(t, 200))
	ctx := context.Background()
	te.stdin = testKey() + "\n"
	out := te.run(ctx, "llm", "configure", "--preset", "deepseek", "--key-stdin", "--workspace", te.ws)
	if out.code != 0 {
		t.Fatalf("configure: %+v", out)
	}
	if got := te.status().of("llm"); got != "partial" {
		t.Fatalf("before a test the step is %s", got)
	}
	out = te.run(ctx, "llm", "test", "--workspace", te.ws)
	if out.code != 0 || !strings.Contains(out.stdout, "succeeded") {
		t.Fatalf("test: %+v", out)
	}
	if got := te.status().of("llm"); got != "complete" {
		t.Fatalf("after a test the step is %s", got)
	}
	status := te.run(ctx, "setup", "status", "--json", "--workspace", te.ws)
	if !strings.Contains(status.stdout, `"present": true`) || !strings.Contains(status.stdout, `"savedAt"`) || strings.Contains(status.stdout, testKey()) {
		t.Fatalf("status = %s", status.stdout)
	}
	// The attempt is in the workspace ledger, so the CLI call is accounted for.
	cost := te.run(ctx, "llm", "cost", "--days", "1", "--workspace", te.ws)
	if cost.code != 0 || !strings.Contains(cost.stdout, "feed-classify") {
		t.Fatalf("cost = %+v", cost)
	}
	// Switching preset after a successful test returns the step to partial.
	te.stdin = testKey() + "\n"
	if out := te.run(ctx, "llm", "configure", "--preset", "openai", "--key-stdin", "--workspace", te.ws); out.code != 0 {
		t.Fatalf("switch: %+v", out)
	}
	if got := te.status().of("llm"); got != "partial" {
		t.Fatalf("after switching the step is %s", got)
	}
}

func TestLLMWrongKeyIsClassifiedAndTheKeyCanBeReplaced(t *testing.T) {
	stub := providerStub(t, 401)
	te := llmEnv(t, stub)
	ctx := context.Background()
	te.stdin = testKey() + "\n"
	te.run(ctx, "llm", "configure", "--preset", "deepseek", "--key-stdin", "--workspace", te.ws)
	out := te.run(ctx, "llm", "test", "--json", "--workspace", te.ws)
	if out.code != 1 || !strings.Contains(out.stdout, `"errorClass": "auth"`) || strings.Contains(out.stdout+out.stderr, testKey()) {
		t.Fatalf("test = %+v", out)
	}
	if got := te.status().of("llm"); got != "partial" {
		t.Fatalf("llm = %s", got)
	}
	res := te.run(ctx, "setup", "status", "--workspace", te.ws)
	if !strings.Contains(res.stdout, "auth") {
		t.Fatalf("status does not name the error class:\n%s", res.stdout)
	}
	good := providerStub(t, 200)
	u, _ := url.Parse(good.URL)
	te.llmOpts = []llm.Option{llm.WithHTTPClient(&http.Client{Transport: toStub{u}})}
	te.stdin = testKey() + "x\n"
	if out := te.run(ctx, "llm", "keys", "set", "deepseek", "--workspace", te.ws); out.code != 0 {
		t.Fatalf("keys set: %+v", out)
	}
	if out := te.run(ctx, "llm", "test", "--workspace", te.ws); out.code != 0 {
		t.Fatalf("second test: %+v", out)
	}
	if got := te.status().of("llm"); got != "complete" {
		t.Fatalf("llm = %s", got)
	}
}

func TestLLMConfigureWithoutAKeyNeedsAPerson(t *testing.T) {
	te := llmEnv(t, providerStub(t, 200))
	out := te.run(context.Background(), "llm", "configure", "--preset", "deepseek", "--workspace", te.ws)
	if out.code != 3 || !strings.Contains(out.stderr, "applysmith llm keys set deepseek") {
		t.Fatalf("out = %+v", out)
	}
}

func TestLLMRejectsAPlaceholderKey(t *testing.T) {
	te := llmEnv(t, providerStub(t, 200))
	te.stdin = "your_key_here_please\n"
	out := te.run(context.Background(), "llm", "keys", "set", "deepseek", "--workspace", te.ws)
	if out.code != 2 || len(te.backend.Accounts()) != 0 {
		t.Fatalf("out = %+v accounts %v", out, te.backend.Accounts())
	}
}

func TestLLMShowSetAndReset(t *testing.T) {
	te := llmEnv(t, providerStub(t, 200))
	ctx := context.Background()
	te.stdin = testKey() + "\n"
	te.run(ctx, "llm", "configure", "--preset", "deepseek", "--key-stdin", "--workspace", te.ws)
	show := te.run(ctx, "llm", "show", "--workspace", te.ws)
	if show.code != 0 || !strings.Contains(show.stdout, "deepseek/deepseek-flash") || !strings.Contains(show.stdout, "preset") {
		t.Fatalf("show = %+v", show)
	}
	if out := te.run(ctx, "llm", "set", "narrative", "deepseek/custom-model", "--thinking", "low", "--workspace", te.ws); out.code != 0 {
		t.Fatalf("set: %+v", out)
	}
	if show := te.run(ctx, "llm", "show", "--workspace", te.ws); !strings.Contains(show.stdout, "deepseek/custom-model") || !strings.Contains(show.stdout, "table") {
		t.Fatalf("show after set:\n%s", show.stdout)
	}
	if out := te.run(ctx, "llm", "set", "narrative", "deepseek/custom-model", "--first-token", "12", "--timeout", "60", "--workspace", te.ws); out.code != 0 {
		t.Fatalf("set with a first-token deadline: %+v", out)
	}
	if show := te.run(ctx, "llm", "show", "--json", "--workspace", te.ws); !strings.Contains(show.stdout, `"firstTokenSeconds": 12`) {
		t.Fatalf("show --json after set:\n%s", show.stdout)
	} else {
		assertAPIShape(t, show.stdout)
	}
	if out := te.run(ctx, "llm", "set", "narrative", "deepseek/custom-model", "--first-token", "90", "--timeout", "60", "--workspace", te.ws); out.code != 2 {
		t.Fatalf("a first-token deadline past the timeout was accepted: %+v", out)
	}
	if out := te.run(ctx, "llm", "set", "narrative", "nobody/model", "--workspace", te.ws); out.code != 2 {
		t.Fatalf("set with an unknown provider: %+v", out)
	}
	if out := te.run(ctx, "llm", "reset", "narrative", "--workspace", te.ws); out.code != 0 {
		t.Fatalf("reset: %+v", out)
	}
	if show := te.run(ctx, "llm", "show", "--workspace", te.ws); strings.Contains(show.stdout, "custom-model") {
		t.Fatalf("show after reset:\n%s", show.stdout)
	}
}

// A group is set, listed by show, named by a task row and refused deletion
// while the row names it. Whether each rule holds is the settings' business
// (TestLLM_Groups_*); this checks the commands reach it.
func TestLLMGroupSetShowRouteAndRemove(t *testing.T) {
	te := llmEnv(t, providerStub(t, 200))
	ctx := context.Background()
	te.stdin = testKey() + "\n"
	te.run(ctx, "llm", "configure", "--preset", "deepseek", "--key-stdin", "--workspace", te.ws)
	if out := te.run(ctx, "llm", "group", "set", "pool", "--mode", "rotate", "--members", "deepseek/deepseek-flash,deepseek/deepseek-v4-pro", "--workspace", te.ws); out.code != 0 {
		t.Fatalf("group set: %+v", out)
	}
	if out := te.run(ctx, "llm", "set", "narrative", "group/pool", "--workspace", te.ws); out.code != 0 {
		t.Fatalf("set to a group: %+v", out)
	}
	show := te.run(ctx, "llm", "show", "--workspace", te.ws)
	if !regexp.MustCompile(`narrative\s+group/pool\s`).MatchString(show.stdout) ||
		!strings.Contains(show.stdout, "group/pool (rotate): deepseek/deepseek-flash,deepseek/deepseek-v4-pro") {
		t.Fatalf("show:\n%s", show.stdout)
	}
	if out := te.run(ctx, "llm", "group", "rm", "pool", "--workspace", te.ws); out.code != 2 || !strings.Contains(out.stderr, "routes.narrative") {
		t.Fatalf("rm while a row names it: %+v", out)
	}
	if out := te.run(ctx, "llm", "group", "set", "bad", "--mode", "random", "--members", "deepseek/x", "--workspace", te.ws); out.code != 2 {
		t.Fatalf("an unknown mode was accepted: %+v", out)
	}
	te.run(ctx, "llm", "reset", "narrative", "--workspace", te.ws)
	if out := te.run(ctx, "llm", "group", "rm", "pool", "--workspace", te.ws); out.code != 0 {
		t.Fatalf("rm: %+v", out)
	}
}

// A budget and the cap set from the command line are what the routing view
// reports; 0 removes a budget and turns the cap off. Their range is the
// settings' (TestDailyCapAndTaskBudgetsAreEditable).
func TestLLMBudgetAndCapAreStored(t *testing.T) {
	te := llmEnv(t, providerStub(t, 200))
	ctx := context.Background()
	te.stdin = testKey() + "\n"
	te.run(ctx, "llm", "configure", "--preset", "deepseek", "--key-stdin", "--workspace", te.ws)
	view := func() (budget, capUSD float64) {
		var v wire.LLMRouting
		if err := json.Unmarshal([]byte(te.run(ctx, "llm", "show", "--json", "--workspace", te.ws).stdout), &v); err != nil {
			t.Fatal(err)
		}
		for _, r := range v.Tasks {
			if r.Task == "deep-eval" {
				budget = r.BudgetUSD
			}
		}
		return budget, v.DailyUSDCap
	}
	for _, step := range []struct {
		args        []string
		budget, cap float64
	}{
		{[]string{"budget", "deep-eval", "1.5"}, 1.5, llm.DefaultDailyCapUSD},
		{[]string{"cap", "5"}, 1.5, 5},
		{[]string{"budget", "deep-eval", "0"}, 0, 5},
		{[]string{"cap", "0"}, 0, 0},
	} {
		if out := te.run(ctx, append(append([]string{"llm"}, step.args...), "--workspace", te.ws)...); out.code != 0 {
			t.Fatalf("%v: %+v", step.args, out)
		}
		if b, c := view(); b != step.budget || c != step.cap {
			t.Fatalf("after %v: budget %v cap %v", step.args, b, c)
		}
	}
}

func TestLLMDebugCaptureOnOffStatus(t *testing.T) {
	te := llmEnv(t, providerStub(t, 200))
	ctx := context.Background()
	if out := te.run(ctx, "llm", "debug-capture", "status", "--workspace", te.ws); out.code != 0 || !strings.Contains(out.stdout, "off") {
		t.Fatalf("status before: %+v", out)
	}
	if out := te.run(ctx, "llm", "debug-capture", "on", "--minutes", "10", "--workspace", te.ws); out.code != 0 {
		t.Fatalf("on: %+v", out)
	}
	if out := te.run(ctx, "llm", "debug-capture", "status", "--workspace", te.ws); out.code != 0 || !strings.Contains(out.stdout, "on until") {
		t.Fatalf("status while on: %+v", out)
	}
	if out := te.run(ctx, "llm", "debug-capture", "off", "--workspace", te.ws); out.code != 0 {
		t.Fatalf("off: %+v", out)
	}
	if out := te.run(ctx, "llm", "debug-capture", "status", "--workspace", te.ws); !strings.Contains(out.stdout, "off") || strings.Contains(out.stdout, "until") {
		t.Fatalf("status after off: %+v", out)
	}
}

// The status is a read, so it answers beside a running server; switching
// capture is a write the server's workspace lock refuses.
func TestLLMDebugCaptureStatusReadsBesideAServer(t *testing.T) {
	te := llmEnv(t, providerStub(t, 200))
	ctx := context.Background()
	te.startServe()
	if out := te.run(ctx, "llm", "debug-capture", "status", "--workspace", te.ws); out.code != 0 || !strings.Contains(out.stdout, "debug capture: off") {
		t.Fatalf("status beside a server: %+v", out)
	}
	if out := te.run(ctx, "llm", "debug-capture", "on", "--workspace", te.ws); out.code != 4 {
		t.Fatalf("on beside a server: %+v", out)
	}
}

func TestLLMBenchPrintsTheTableAndAppliesTheWinner(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"option\\\":\\\"Yes\\\"}\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":4}}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(stub.Close)
	te := llmEnv(t, stub)
	ctx := context.Background()
	te.stdin = testKey() + "\n"
	te.run(ctx, "llm", "configure", "--preset", "deepseek", "--key-stdin", "--workspace", te.ws)
	cases := filepath.Join(t.TempDir(), "cases.json")
	if err := os.WriteFile(cases, []byte(`[{"question":"Is the sky blue?","options":["Yes","No"],"facts":["The sky is blue."],"expect":"Yes"},{"question":"Pick one","options":["Yes","No"],"expect":"NONE"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	out := te.run(ctx, "llm", "bench", "option-pick", "--candidates", "deepseek/model-a,deepseek/model-b", "--cases", cases, "--apply", "--workspace", te.ws)
	if out.code != 0 || !strings.Contains(out.stdout, "CANDIDATE") || !strings.Contains(out.stdout, "deepseek/model-a") || !strings.Contains(out.stdout, "1/2") ||
		!strings.Contains(out.stdout, "option-pick now uses deepseek/model-") || !strings.Contains(out.stderr, "4 model calls") {
		t.Fatalf("bench: %+v", out)
	}
	if show := te.run(ctx, "llm", "show", "--workspace", te.ws); !strings.Contains(show.stdout, "option-pick") || !strings.Contains(show.stdout, "table") {
		t.Fatalf("show after apply:\n%s", show.stdout)
	}
	for name, args := range map[string][]string{
		"no candidates":    {"llm", "bench", "option-pick"},
		"another task":     {"llm", "bench", "narrative", "--candidates", "deepseek/m"},
		"unknown provider": {"llm", "bench", "option-pick", "--candidates", "nobody/m"},
		"bad candidate":    {"llm", "bench", "option-pick", "--candidates", "nomodel"},
	} {
		if out := te.run(ctx, append(args, "--workspace", te.ws)...); out.code == 0 {
			t.Errorf("%s was accepted: %+v", name, out)
		}
	}
}

// Input the router refuses exits 2 whether the command writes the workspace
// itself or a running server refuses it.
func TestLLMSetRejectedInputExitsTwoWithOrWithoutAServer(t *testing.T) {
	te := newInitTestEnv(t)
	args := []string{"llm", "set", "no-such-task", "openai/gpt-x", "--workspace", te.ws}
	if o := te.run(context.Background(), args...); o.code != 2 {
		t.Fatalf("without a server: %+v", o)
	}
	te.startServe()
	if o := te.run(context.Background(), args...); o.code != 2 || !strings.Contains(o.stderr, "refused") {
		t.Fatalf("with a server: %+v", o)
	}
}
