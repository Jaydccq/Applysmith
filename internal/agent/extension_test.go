package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"applysmith/internal/api/wire"
)

// The harness stands in for pi: it offers the extension API surface the
// extension uses and drives the lifecycle events. The extension itself and the
// bridge it talks to are the real ones.
const extensionHarness = `
import { pathToFileURL } from "node:url";
const mod = await import(pathToFileURL(process.argv[2]).href);
const handlers = {};
const tools = {};
let active = [];
const pi = {
  registerTool(t) { tools[t.name] = t; active.push(t.name); },
  on(name, fn) { handlers[name] = fn; },
  getActiveTools() { return [...active]; },
  setActiveTools(list) { active = list; },
};
const say = (o) => process.stdout.write(JSON.stringify(o) + "\n");
await mod.default(pi);
say({ step: "registered", names: Object.keys(tools), parameters: tools.slow.parameters });
try { await handlers.session_start({}); say({ step: "hello", ok: true }); } catch (e) { say({ step: "hello", ok: false, error: String(e.message) }); }
const start = await handlers.before_agent_start({});
say({ step: "before_agent_start", systemPrompt: start.systemPrompt });
const ctx = await handlers.context({ messages: [{ role: "user", content: "now" }, { role: "assistant", content: "answer" }] });
say({ step: "context", roles: ctx.messages.map((m) => m.role), first: ctx.messages[0].content });

const slow = tools.slow.execute("call_slow", { step: "Waiting" });
const fast = tools.fast.execute("call_fast", {});
say({ step: "both-called" });
const out = await Promise.allSettled([slow, fast]);
say({ step: "settled", texts: out.map((o) => (o.status === "fulfilled" ? o.value.content[0].text : "rejected:" + o.reason.message)) });

const ac = new AbortController();
ac.abort();
const aborted = await tools.fast.execute("call_aborted", {}, ac.signal).then(() => "ran", (e) => "rejected:" + e.message);
say({ step: "aborted", result: aborted });

const failed = await tools.broken.execute("call_broken", {}).then(() => "ran", (e) => "rejected:" + e.message);
say({ step: "error", result: failed });

const last = await tools.last.execute("call_last", {});
say({ step: "final-only", text: last.content[0].text, active: [...active] });
await handlers.before_agent_start({});
say({ step: "restored", active: [...active] });
`

type harness struct {
	cmd *exec.Cmd
	out *bufio.Scanner
}

func startHarness(t *testing.T, url, token string, env ...string) *harness {
	t.Helper()
	return startScript(t, extensionHarness, url, token, env...)
}

// startScript runs a harness script against the embedded extension under node.
func startScript(t *testing.T, script, url, token string, env ...string) *harness {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; the extension is JavaScript and is exercised under node")
	}
	dir := t.TempDir()
	ext := filepath.Join(dir, "applysmith.mjs")
	if err := os.WriteFile(ext, extensionSource, 0o600); err != nil {
		t.Fatal(err)
	}
	h := filepath.Join(dir, "harness.mjs")
	if err := os.WriteFile(h, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, h, ext)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "APPLYSMITH_AGENT_URL=" + url, "APPLYSMITH_AGENT_TOKEN=" + token}, env...)
	cmd.Stderr = os.Stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return &harness{cmd: cmd, out: bufio.NewScanner(pipe)}
}

func (h *harness) next(t *testing.T, step string) map[string]any {
	t.Helper()
	for h.out.Scan() {
		var m map[string]any
		if err := json.Unmarshal(h.out.Bytes(), &m); err != nil {
			t.Fatalf("harness output %q: %v", h.out.Text(), err)
		}
		if m["step"] == step {
			return m
		}
	}
	t.Fatalf("the harness ended before step %q (%v)", step, h.out.Err())
	return nil
}

// bridgeServer serves the four routes of the bridge over HTTP the way the API
// does: the credential in a bearer header and the answer in the success envelope.
func bridgeServer(t *testing.T, b *Bridge) *httptest.Server {
	t.Helper()
	write := func(w http.ResponseWriter, v any, err error) {
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": v})
	}
	token := func(r *http.Request) string { return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") }
	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/agent/catalog", func(w http.ResponseWriter, r *http.Request) {
		v, err := b.Catalog(token(r))
		write(w, v, err)
	})
	mux.HandleFunc("POST /internal/agent/hello", func(w http.ResponseWriter, r *http.Request) {
		var h wire.AgentHello
		_ = json.NewDecoder(r.Body).Decode(&h)
		v, err := b.Hello(token(r), h)
		write(w, v, err)
	})
	mux.HandleFunc("GET /internal/agent/turn-context", func(w http.ResponseWriter, r *http.Request) {
		v, err := b.TurnContext(r.Context(), token(r))
		write(w, v, err)
	})
	mux.HandleFunc("POST /internal/agent/tool-call", func(w http.ResponseWriter, r *http.Request) {
		var c wire.AgentToolCall
		_ = json.NewDecoder(r.Body).Decode(&c)
		v, err := b.ToolCall(r.Context(), token(r), c)
		write(w, v, err)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func extensionTools(started chan<- string, releaseSlow <-chan struct{}) []Tool {
	mk := func(name string, run ToolFunc) Tool {
		return Tool{Name: name, Label: name, Description: name + ".", Class: ClassRead, PII: PIINone, Timeout: 10 * time.Second,
			Schema: `{"type":"object","additionalProperties":false,"properties":{}}`, Run: run}
	}
	// fast reports itself as fast-early when it starts before slow has answered.
	var slowDone atomic.Bool
	return []Tool{
		mk("slow", func(ctx context.Context, c ToolCall) (ToolResult, error) {
			started <- "slow"
			<-releaseSlow
			slowDone.Store(true)
			return ToolResult{Text: "slow done"}, nil
		}),
		mk("fast", func(ctx context.Context, c ToolCall) (ToolResult, error) {
			if !slowDone.Load() {
				started <- "fast-early"
			} else {
				started <- "fast"
			}
			return ToolResult{Text: "fast done"}, nil
		}),
		mk("broken", func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, context.DeadlineExceeded }),
		mk("last", func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{Text: "last done"}, nil }),
	}
}

func TestExtension_RegistersTheCatalogAndForwardsCallsInTheModelsOrder(t *testing.T) {
	started := make(chan string, 8)
	release := make(chan struct{})
	b := newBridgeEnv(t, ChannelDashboard, "hello", extensionTools(started, release)...)
	// The twelfth call is the last one the model may make, so the extension must
	// switch tools off after it: make the earlier calls count.
	for i := 0; i < MaxToolCalls-4; i++ { // slow, fast, broken and last are the other four
		b.run.calls.Add(1)
	}
	srv := bridgeServer(t, b.f.bridge)
	h := startHarness(t, srv.URL, b.token)

	reg := h.next(t, "registered")
	names, _ := json.Marshal(reg["names"])
	for _, want := range []string{"slow", "fast", "tracker_query", "recall_session"} {
		if !strings.Contains(string(names), `"`+want+`"`) {
			t.Errorf("tool %s not registered: %s", want, names)
		}
	}
	params, _ := reg["parameters"].(map[string]any)
	if props, _ := params["properties"].(map[string]any); props["step"] == nil {
		t.Errorf("the registered schema lacks the step argument: %v", params)
	}
	if hello := h.next(t, "hello"); hello["ok"] != true {
		t.Fatalf("hello = %v", hello)
	}
	if start := h.next(t, "before_agent_start"); !strings.Contains(start["systemPrompt"].(string), "Applysmith") {
		t.Fatalf("system prompt = %v", start)
	}
	ctx := h.next(t, "context")
	if roles, _ := json.Marshal(ctx["roles"]); string(roles) != `["user","assistant"]` {
		t.Fatalf("context roles = %s", roles)
	}

	// pi starts both calls at once; the extension must let the second reach the
	// product only after the first has been answered.
	if got := <-started; got != "slow" {
		t.Fatalf("first forwarded call = %s", got)
	}
	h.next(t, "both-called")
	select {
	case got := <-started:
		t.Fatalf("%s reached the product while the call before it was still running", got)
	default:
	}
	close(release)
	if got := <-started; got != "fast" {
		t.Fatalf("second forwarded call = %s", got)
	}
	settled := h.next(t, "settled")
	if texts, _ := settled["texts"].([]any); len(texts) != 2 || !strings.Contains(asString(texts[0]), "slow done") || !strings.Contains(asString(texts[1]), "fast done") {
		t.Fatalf("results = %v", settled["texts"])
	}
	if got := h.next(t, "aborted")["result"]; got != "rejected:The operation was aborted." {
		t.Fatalf("a call aborted while queued = %v", got)
	}
	select {
	case got := <-started:
		t.Fatalf("the aborted call was forwarded: %s", got)
	default:
	}
	if got := h.next(t, "error")["result"].(string); !strings.HasPrefix(got, "rejected:") || !strings.Contains(got, "The tool failed") {
		t.Fatalf("an error result must reject so pi marks it as an error: %v", got)
	}
	last := h.next(t, "final-only")
	if !strings.Contains(asString(last["text"]), "last done") || len(last["active"].([]any)) != 0 {
		t.Fatalf("after the twelfth call tools must be switched off: %v", last)
	}
	if restored := h.next(t, "restored"); len(restored["active"].([]any)) == 0 {
		t.Fatalf("the next turn must get its tools back: %v", restored)
	}
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

// The harness of the skill scenario: the turn starts with every tool active, a
// call to load_skill narrows them and adds the skill's block to every later
// model request, and the next turn starts clean.
const skillHarness = `
import { pathToFileURL } from "node:url";
const mod = await import(pathToFileURL(process.argv[2]).href);
const handlers = {};
const tools = {};
let active = [];
const pi = {
  registerTool(t) { tools[t.name] = t; active.push(t.name); },
  on(name, fn) { handlers[name] = fn; },
  getActiveTools() { return [...active]; },
  setActiveTools(list) { active = list; },
};
const say = (o) => process.stdout.write(JSON.stringify(o) + "\n");
await mod.default(pi);
await handlers.session_start({});
const turn = [{ role: "user", content: "finish run_1" }];
await handlers.before_agent_start({});
say({ step: "start", active: [...active], context: (await handlers.context({ messages: turn })).messages.map((m) => m.role) });
const loaded = await tools.load_skill.execute("call_1", { name: "apply" });
say({ step: "loaded", text: loaded.content[0].text, active: [...active],
  context: (await handlers.context({ messages: turn })).messages.map((m) => (typeof m.content === "string" ? m.content : m.content[0].text)) });
await handlers.before_agent_start({});
say({ step: "next-turn", active: [...active], context: (await handlers.context({ messages: turn })).messages.length });
`

func TestExtension_ALoadedSkillNarrowsTheToolsAndGivesEveryLaterRequestItsBlock(t *testing.T) {
	b := newBridgeEnv(t, ChannelDashboard, "finish run_1", append(ApplyRunTools(&fakeApplyRun{}), loadSkillTool())...)
	srv := bridgeServer(t, b.f.bridge)
	h := startScript(t, skillHarness, srv.URL, b.token)
	sk, _ := skillNamed(SkillApply)

	start := h.next(t, "start")
	if len(start["active"].([]any)) < len(sk.Tools)/2 || strings.Join(strOf(start["context"]), ",") != "user" {
		t.Fatalf("a turn without a skill: %v", start)
	}
	loaded := h.next(t, "loaded")
	for _, name := range strOf(loaded["active"]) {
		if !slices.Contains(sk.Tools, name) {
			t.Errorf("%s stayed active though the skill does not name it", name)
		}
	}
	if !slices.Contains(strOf(loaded["active"]), ToolApplyAnswer) || len(strOf(loaded["active"])) >= len(strOf(start["active"])) {
		t.Fatalf("the tools were not narrowed to the skill's: %v", loaded["active"])
	}
	ctx := strOf(loaded["context"])
	if len(ctx) != 2 || !strings.Contains(ctx[0], sk.Block) || ctx[1] != "finish run_1" {
		t.Fatalf("the later request lacks the block before the turn's own messages: %q", ctx)
	}
	// Another start of an agent run clears the block the extension held, and
	// takes the tools from the turn context: the turn is under the skill, so
	// the skill's tools are the ones the product names.
	next := h.next(t, "next-turn")
	if next["context"].(float64) != 1 || !slices.Equal(strOf(next["active"]), strOf(loaded["active"])) {
		t.Fatalf("a new start: %v", next)
	}
}

func strOf(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

// The harness of the virtual model: pi calls route() before every request and,
// after a failed one, again with reason "retry" and the model that failed.
const virtualModelHarness = `
import { pathToFileURL } from "node:url";
const mod = await import(pathToFileURL(process.argv[2]).href);
let virtual;
const pi = {
  registerTool() {}, on() {}, getActiveTools() { return []; }, setActiveTools() {},
  registerVirtualModel(def) { virtual = def; },
};
const say = (o) => process.stdout.write(JSON.stringify(o) + "\n");
await mod.default(pi);
if (!virtual) { say({ step: "none" }); process.exit(0); }
say({ step: "registered", provider: virtual.provider, id: virtual.id, levels: virtual.thinkingLevels });
const ctx = { modelRegistry: { find: (provider, id) => (id === "missing" ? undefined : { provider, id }) } };
const m = (provider, id) => ({ provider, id });
const key = (r) => r.model.provider + "/" + r.model.id;
const call = (request) => {
  try { return key(virtual.route({ thinkingLevel: "medium", ...request }, ctx)); } catch (e) { return "throws: " + e.message; }
};
const plan = m("applysmith-responses", "gpt-6.1-sol");
const chat = m("applysmith-chat", "deepseek-flash");
const seq = [];
seq.push(call({ reason: "user" }));
seq.push(call({ reason: "continuation", previous: { model: plan } }));
seq.push(call({ reason: "retry", failed: { model: plan, message: { errorMessage: "usage limit" } } }));
seq.push(call({ reason: "continuation", previous: { model: chat } }));
seq.push(call({ reason: "continuation", previous: { model: plan } }));
seq.push(call({ reason: "user" }));
seq.push(call({ reason: "retry", failed: { model: chat, message: { errorMessage: "overloaded" } } }));
say({ step: "sequence", seq });
`

func TestExtension_AVirtualModelPicksTheNextWireAfterAFailureAndNeverAsksAFailedModelAgain(t *testing.T) {
	b := newBridgeEnv(t, ChannelDashboard, "hello")
	srv := bridgeServer(t, b.f.bridge)
	spec := `{"provider":"applysmith","id":"agent","models":[{"provider":"applysmith-responses","id":"gpt-6.1-sol"},{"provider":"applysmith-chat","id":"deepseek-flash"}]}`
	h := startScript(t, virtualModelHarness, srv.URL, b.token, "APPLYSMITH_AGENT_ROUTE="+spec)
	reg := h.next(t, "registered")
	if reg["provider"] != "applysmith" || reg["id"] != "agent" || len(reg["levels"].([]any)) != 6 {
		t.Fatalf("registered = %v", reg)
	}
	got := strOf(h.next(t, "sequence")["seq"])
	want := []string{
		"applysmith-responses/gpt-6.1-sol", // a user message starts on the first model
		"applysmith-responses/gpt-6.1-sol", // a tool result stays on the model that answered
		"applysmith-chat/deepseek-flash",   // the plan failed: the next wire
		"applysmith-chat/deepseek-flash",   // and the turn stays there
		"applysmith-chat/deepseek-flash",   // a model that failed is not used again
		"applysmith-chat/deepseek-flash",   // a steering message does not go back to the failed model
	}
	if !slices.Equal(got[:6], want) || !strings.HasPrefix(got[6], "throws: Every model of the agent route failed") {
		t.Fatalf("routes:\n%q\nwant\n%q then an error", got, want)
	}
}
