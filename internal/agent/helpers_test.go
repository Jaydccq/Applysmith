package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"applysmith/internal/agent/pirpc"
	"applysmith/internal/agent/sessions"
	"applysmith/internal/api/wire"
	"applysmith/internal/llm"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
)

const testWorkspaceID = "0123456789abcdef0123456789abcdef"

// fakeKey builds a credential-shaped value at run time; no key literal lives in
// the source.
func fakeKey() string { return "sk-" + strings.Repeat("a", 8) + "agent" + strings.Repeat("b", 24) }

type fixture struct {
	t        *testing.T
	st       *store.Store
	sessions *sessions.Store
	tracker  *tracker.Tracker
	router   *fakeRouter
	launcher *fakeLauncher
	registry *Registry
	guard    *Guard
	bridge   *Bridge
	sup      *Supervisor
	sink     *chanSink
	agent    *Agent
	dir      string
	contexts contextLog
}

// contextLog keeps what each fake process fetched as the turn context.
type contextLog struct {
	mu  sync.Mutex
	got []wire.AgentTurnContext
}

func (c *contextLog) add(v wire.AgentTurnContext) {
	c.mu.Lock()
	c.got = append(c.got, v)
	c.mu.Unlock()
}

func (c *contextLog) all() []wire.AgentTurnContext {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]wire.AgentTurnContext(nil), c.got...)
}

// recordsOf is the text of the turn's records message, the last of a packed
// prefix, or "" when the prefix has none.
func recordsOf(prefix []wire.AgentPrefixMessage) string {
	if n := len(prefix); n > 0 && strings.HasPrefix(prefix[n-1].Text, dataHeader) {
		return prefix[n-1].Text
	}
	return ""
}

// newFixture builds the whole stack over a real database and a fake pi.
func newFixture(t *testing.T, extra ...Tool) *fixture {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "applysmith.db"), store.Options{WorkspaceID: testWorkspaceID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := &fixture{t: t, st: st, sessions: sessions.New(st, nil), dir: dir}
	f.tracker, err = tracker.New(st, tracker.Options{})
	if err != nil {
		t.Fatal(err)
	}
	tools := append(ReadTools(ToolDeps{Tracker: f.tracker, Sessions: f.sessions}), ApplyTools()...)
	// A tool a test passes replaces the built one of the same name, and a later
	// one replaces an earlier one.
	for _, e := range extra {
		tools = slices.DeleteFunc(tools, func(t Tool) bool { return t.Name == e.Name })
		tools = append(tools, e)
	}
	f.registry, err = NewRegistry(tools...)
	if err != nil {
		t.Fatal(err)
	}
	f.guard = NewGuard(f.registry, GuardOptions{Sessions: f.sessions, Salt: "salt"})
	f.router = &fakeRouter{route: llm.AgentRoute{
		Resolved: llm.Resolved{Task: llm.Task{ID: llm.AgentTask}, Provider: "acme", Model: "scripted-1", Thinking: llm.ThinkMedium, MaxOutputTokens: 4000},
		Config:   llm.ProviderConfig{ID: "acme", Kind: llm.KindOpenAI, BaseURL: "http://127.0.0.1:1/v1"},
	}, cap: 2}
	f.bridge = NewBridge(BridgeOptions{Registry: f.registry, Guard: f.guard, Sessions: f.sessions, Packer: ContextPacker{Sessions: f.sessions}, Gateway: f.router})
	f.launcher = &fakeLauncher{t: t, version: pirpc.PinnedVersion + "\n", bridge: f.bridge}
	f.sup = NewSupervisor(SupervisorConfig{AgentDir: filepath.Join(dir, "agent"), BaseURL: func() string { return "http://127.0.0.1:1" },
		Router: f.router, Sessions: f.sessions, Bridge: f.bridge, Launcher: f.launcher, PiPath: "/opt/pi",
		Environ: func() []string {
			return []string{"PATH=/usr/bin", "HOME=/home/alex", "APPLYSMITH_SECRET_API_TOKEN=must-not-reach-pi"}
		}})
	f.sink = newChanSink()
	f.agent = New(Config{Sessions: f.sessions, Supervisor: f.sup, Sink: f.sink, Bridge: f.bridge})
	f.launcher.contexts = &f.contexts
	t.Cleanup(f.agent.Close)
	return f
}

// send posts a message and returns the result.
func (f *fixture) send(text string) SendResult {
	f.t.Helper()
	res, err := f.agent.Send(context.Background(), SendRequest{Text: text, Channel: ChannelDashboard})
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

// sendTo posts a message in an existing conversation.
func (f *fixture) sendTo(sessionID, text string) SendResult {
	f.t.Helper()
	res, err := f.agent.Send(context.Background(), SendRequest{SessionID: sessionID, Text: text, Channel: ChannelDashboard})
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

// terminal waits for the turn's one final or error event.
func (f *fixture) terminal() wire.ChatEvent {
	f.t.Helper()
	return f.sink.next(f.t, func(e wire.ChatEvent) bool { return e.Type == wire.ChatFinal || e.Type == wire.ChatError })
}

// idle waits until every turn goroutine has finished, the record included.
func (f *fixture) idle() {
	f.t.Helper()
	f.agent.wg.Wait()
}

type fakeRouter struct {
	mu       sync.Mutex
	route    llm.AgentRoute
	routeErr error
	spend    float64
	cap      float64
	blocked  bool // the daily cap stops the agent
	rows     []llm.AgentUsage
	// light is the agent-light route; nil leaves intent routing off. The
	// classifier answers tierText, or fails with tierErr; asked collects what
	// it was asked, and capAsked the rows the cap was checked for.
	light      *llm.AgentRoute
	lightBlock bool
	tierText   string
	tierErr    error
	asked      []llm.Request
	capAsked   []string
	// calls are the model requests the gateway served; gwErr is its answer.
	calls []llm.AgentCall
	gwErr error
}

func (r *fakeRouter) AgentRoute(_ context.Context, task string) (llm.AgentRoute, error) {
	if task == llm.AgentLightTask {
		if r.light == nil {
			return llm.AgentRoute{}, &llm.Error{Kind: llm.KindNoRoute, Task: task, Message: "no model is routed"}
		}
		return *r.light, nil
	}
	return r.route, r.routeErr
}
func (r *fakeRouter) AgentCapBlocked(_ context.Context, task string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.capAsked = append(r.capAsked, task)
	if task == llm.AgentLightTask {
		return r.lightBlock
	}
	return r.blocked
}
func (r *fakeRouter) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, req)
	if r.tierErr != nil {
		return nil, r.tierErr
	}
	return &llm.Response{Text: r.tierText}, nil
}
func (r *fakeRouter) DailySpend(context.Context) (float64, float64) { return r.spend, r.cap }
func (r *fakeRouter) ServeAgentCall(_ context.Context, c llm.AgentCall, _ http.ResponseWriter) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
	return r.gwErr
}
func (r *fakeRouter) RecordAgentUsage(_ context.Context, _ llm.AgentRoute, u llm.AgentUsage) {
	r.mu.Lock()
	r.rows = append(r.rows, u)
	r.mu.Unlock()
}
func (r *fakeRouter) usage() []llm.AgentUsage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]llm.AgentUsage(nil), r.rows...)
}

// chanSink collects published events and lets a test wait for one by a
// condition, without sleeping.
type chanSink struct {
	mu  sync.Mutex
	all map[string][]wire.ChatEvent
	ch  chan wire.ChatEvent
}

func newChanSink() *chanSink {
	return &chanSink{all: map[string][]wire.ChatEvent{}, ch: make(chan wire.ChatEvent, 4096)}
}

func (s *chanSink) Publish(sessionID string, ev wire.ChatEvent) {
	s.mu.Lock()
	s.all[sessionID] = append(s.all[sessionID], ev)
	s.mu.Unlock()
	s.ch <- ev
}

// next reads events until one satisfies the condition and returns it.
func (s *chanSink) next(t *testing.T, cond func(wire.ChatEvent) bool) wire.ChatEvent {
	t.Helper()
	for ev := range s.ch {
		if cond(ev) {
			return ev
		}
	}
	t.Fatal("the event channel closed")
	return wire.ChatEvent{}
}

func (s *chanSink) events(sessionID string) []wire.ChatEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]wire.ChatEvent(nil), s.all[sessionID]...)
}

func types(evs []wire.ChatEvent) string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Type
	}
	return strings.Join(out, " ")
}

type fakeLauncher struct {
	t          *testing.T
	bridge     *Bridge
	mu         sync.Mutex
	version    string
	versionErr error
	versions   int
	authErr    error
	auths      []string
	procs      []*fakePi
	script     func(p *fakePi)
	startErr   error
	contexts   *contextLog
}

func (l *fakeLauncher) Version(context.Context, string, []string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.versions++
	return l.version, l.versionErr
}

func (l *fakeLauncher) AuthCheck(_ context.Context, _, provider string, _ []string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.auths = append(l.auths, provider)
	return l.authErr
}

func (l *fakeLauncher) Start(spec Spec, _ io.Writer) (Process, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.startErr != nil {
		return nil, l.startErr
	}
	p := newFakePi(l.t, spec, l.bridge)
	p.contexts = l.contexts
	l.procs = append(l.procs, p)
	if l.script != nil {
		go l.script(p)
	}
	return p, nil
}

func (l *fakeLauncher) started() []*fakePi {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]*fakePi(nil), l.procs...)
}

// fakePi is a scripted stand-in for the pi process: it answers commands the way
// pi does and plays recorded transcripts.
type fakePi struct {
	t      *testing.T
	spec   Spec
	bridge *Bridge
	token  string
	r      *io.PipeReader
	w      *io.PipeWriter
	done   chan struct{}
	once   sync.Once
	cmds   chan map[string]any
	// unanswered lists command types the fake never answers.
	unanswered sync.Map
	killed     chan struct{}
	killOnce   sync.Once
	inBuf      bytes.Buffer
	inMu       sync.Mutex
	seen       []string
	thinking   string // the level of the last set_thinking_level
	autoRetry  string // "true" or "false", from the last set_auto_retry
	prompts    []string
	contexts   *contextLog
	wmu        sync.Mutex
}

func newFakePi(t *testing.T, spec Spec, b *Bridge) *fakePi {
	r, w := io.Pipe()
	p := &fakePi{t: t, spec: spec, bridge: b, r: r, w: w, done: make(chan struct{}), cmds: make(chan map[string]any, 256), killed: make(chan struct{})}
	for _, kv := range spec.Env {
		if v, ok := strings.CutPrefix(kv, "APPLYSMITH_AGENT_TOKEN="); ok {
			p.token = v
		}
	}
	return p
}

func (p *fakePi) Write(b []byte) (int, error) {
	select {
	case <-p.done:
		return 0, io.ErrClosedPipe
	default:
	}
	p.inMu.Lock()
	p.inBuf.Write(b)
	var lines []string
	for {
		i := bytes.IndexByte(p.inBuf.Bytes(), '\n')
		if i < 0 {
			break
		}
		lines = append(lines, string(p.inBuf.Next(i+1)))
	}
	p.inMu.Unlock()
	for _, line := range lines {
		var cmd map[string]any
		if err := json.Unmarshal([]byte(line), &cmd); err != nil {
			p.t.Errorf("fake pi: bad command %q: %v", line, err)
			continue
		}
		p.cmds <- cmd
		typ, _ := cmd["type"].(string)
		p.inMu.Lock()
		p.seen = append(p.seen, typ)
		if level, ok := cmd["level"].(string); ok && typ == "set_thinking_level" {
			p.thinking = level
		}
		if on, ok := cmd["enabled"].(bool); ok && typ == "set_auto_retry" {
			p.autoRetry = fmt.Sprint(on)
		}
		if msg, ok := cmd["message"].(string); ok && typ == "prompt" {
			p.prompts = append(p.prompts, msg)
		}
		p.inMu.Unlock()
		if _, skip := p.unanswered.Load(typ); skip {
			continue
		}
		data := map[string]any{}
		switch typ {
		case "prompt":
			data["disposition"] = "started"
		case "steer":
			data["disposition"] = "queued"
		}
		go p.emit(map[string]any{"id": cmd["id"], "type": "response", "command": typ, "success": true, "data": data})
	}
	return len(b), nil
}

// commandTypes lists the commands received so far, in order.
func (p *fakePi) commandTypes() []string {
	p.inMu.Lock()
	defer p.inMu.Unlock()
	return append([]string(nil), p.seen...)
}

func (p *fakePi) Stdout() io.Reader     { return p.r }
func (p *fakePi) Done() <-chan struct{} { return p.done }
func (p *fakePi) CloseInput()           { p.exit() }
func (p *fakePi) Kill() {
	p.killOnce.Do(func() { close(p.killed) })
	p.exit()
}

func (p *fakePi) exit() {
	p.once.Do(func() {
		_ = p.w.Close()
		close(p.done)
	})
}

// emit writes one record to stdout; it fails quietly once the process is gone.
func (p *fakePi) emit(v any) {
	b, _ := json.Marshal(v)
	p.line(string(b))
}

func (p *fakePi) line(s string) {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	select {
	case <-p.done:
		return
	default:
	}
	_, _ = io.WriteString(p.w, s+"\n")
}

// command waits for the next command of a type, discarding others.
func (p *fakePi) command(typ string) map[string]any {
	p.t.Helper()
	for {
		select {
		case c := <-p.cmds:
			if c["type"] == typ {
				return c
			}
		case <-p.done:
			return nil
		}
	}
}

// hello does what the extension does at session start: it fetches the catalog
// and reports it as the active tool set.
func (p *fakePi) hello(extra ...string) {
	cat, err := p.bridge.Catalog(p.token)
	if errors.Is(err, ErrBridgeAuth) {
		return // the turn ended and the credential was revoked while pi started
	}
	if err != nil {
		p.t.Errorf("fake pi: catalog: %v", err)
		return
	}
	names := append([]string(nil), extra...)
	for _, tl := range cat.Tools {
		names = append(names, tl.Name)
	}
	_, _ = p.bridge.Hello(p.token, wire.AgentHello{Tools: names})
}

// forward sends a tool call to the bridge as the extension would.
func (p *fakePi) forward(id, name string, args json.RawMessage) wire.AgentToolResult {
	res, err := p.bridge.ToolCall(context.Background(), p.token, wire.AgentToolCall{ToolCallID: id, Name: name, Args: args})
	if err != nil {
		p.t.Errorf("fake pi: tool call: %v", err)
	}
	return res
}

func hasCommandType(p *fakePi, typ string) bool {
	for _, c := range p.commandTypes() {
		if c == typ {
			return true
		}
	}
	return false
}

// fetchContext does what the extension does before the agent starts.
func (p *fakePi) fetchContext() {
	tc, err := p.bridge.TurnContext(context.Background(), p.token)
	if err != nil {
		p.t.Errorf("fake pi: turn context: %v", err)
		return
	}
	if p.contexts != nil {
		p.contexts.add(tc)
	}
}

// Hand-built records for scripts the recorded transcripts do not cover.
func lineToolCall(id, name string, args any) string {
	b, _ := json.Marshal(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant",
		"content":  []map[string]any{{"type": "toolCall", "id": id, "name": name, "arguments": args}},
		"provider": "applysmith-acme", "model": "scripted-1", "stopReason": "toolUse",
		"usage": map[string]any{"input": 10, "output": 5, "totalTokens": 15, "cost": map[string]any{"total": 0.00001}}}})
	return string(b)
}

func lineToolStart(id, name string, args any) string {
	b, _ := json.Marshal(map[string]any{"type": "tool_execution_start", "toolCallId": id, "toolName": name, "args": args})
	return string(b)
}

func lineAnswer(text string) string {
	b, _ := json.Marshal(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant",
		"content":  []map[string]any{{"type": "text", "text": text}},
		"provider": "applysmith-acme", "model": "scripted-1", "stopReason": "stop",
		"usage": map[string]any{"input": 20, "output": 6, "totalTokens": 26, "cost": map[string]any{"total": 0.00002}}}})
	return string(b)
}

const lineSettled = `{"type":"agent_settled"}`

// transcript loads the recorded lines of a pirpc transcript, without its
// command responses.
func transcript(t *testing.T, name string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join("pirpc", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		var head struct{ Type string }
		if json.Unmarshal(sc.Bytes(), &head) == nil && head.Type != "response" {
			out = append(out, sc.Text())
		}
	}
	return out
}

// replayOptions steers a replay. Before runs ahead of each line and returns
// false to stop the replay there.
type replayOptions struct {
	Before func(p *fakePi, i int, rec pirpc.Record) bool
	// AfterAbort keeps playing after pi was told to abort, for a response that
	// was already under way.
	AfterAbort bool
}

// replay plays transcript lines as pi's output. A tool_execution_start is
// answered by the real bridge: the transcript's own tool_execution_end is
// replaced by what the bridge returns, as pi would report it.
func (p *fakePi) replay(lines []string, o replayOptions) {
	for i, line := range lines {
		rec, err := pirpc.Decode([]byte(line))
		if err != nil {
			p.t.Errorf("fake pi: transcript line %d: %v", i, err)
			return
		}
		if !o.AfterAbort && hasCommandType(p, "abort") {
			return // pi stops producing once it is told to abort
		}
		if o.Before != nil && !o.Before(p, i, rec) {
			return
		}
		if rec.Type == pirpc.TypeToolExecEnd {
			continue
		}
		p.line(line)
		if rec.Type == pirpc.TypeToolExecStart {
			p.finishCall(rec)
		}
	}
}

// finishCall reports the end of a call the way pi does: a name pi never
// registered is answered by pi itself, a catalog name is forwarded.
func (p *fakePi) finishCall(rec pirpc.Record) {
	cat, _ := p.bridge.Catalog(p.token)
	registered := false
	for _, tl := range cat.Tools {
		registered = registered || tl.Name == rec.ToolName
	}
	text, isErr := "Tool "+rec.ToolName+" not found", true
	if registered {
		res := p.forward(rec.ToolCallID, rec.ToolName, rec.Args)
		text, isErr = res.Text, res.IsError
	}
	p.emit(map[string]any{"type": "tool_execution_end", "toolCallId": rec.ToolCallID, "toolName": rec.ToolName,
		"result": map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "details": map[string]any{}}, "isError": isErr})
}

// standard is the script of an ordinary process: report the tools, wait for
// the prompt, play the transcript.
func standard(t *testing.T, name string, o replayOptions) func(p *fakePi) {
	lines := transcript(t, name)
	return func(p *fakePi) {
		p.hello()
		if p.command("prompt") == nil {
			return
		}
		p.fetchContext()
		p.replay(lines, o)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }
