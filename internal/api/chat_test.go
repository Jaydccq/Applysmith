package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/agent"
	"applysmith/internal/api/wire"
)

// scriptedPi stands in for the pi process: it answers commands and runs a
// script that talks to the real server over HTTP the way the extension does.
type scriptedPi struct {
	spec    agent.Spec
	r       *io.PipeReader
	w       *io.PipeWriter
	done    chan struct{}
	once    sync.Once
	prompt  chan struct{}
	abort   chan struct{}
	mu      sync.Mutex
	partial bytes.Buffer
	wmu     sync.Mutex
	url     string
	token   string
	gate    <-chan struct{}
	message string // the prompt's message, once pi was prompted
}

func (p *scriptedPi) Write(b []byte) (int, error) {
	p.mu.Lock()
	p.partial.Write(b)
	var lines []string
	for {
		i := bytes.IndexByte(p.partial.Bytes(), '\n')
		if i < 0 {
			break
		}
		lines = append(lines, string(p.partial.Next(i+1)))
	}
	p.mu.Unlock()
	for _, line := range lines {
		var cmd map[string]any
		if json.Unmarshal([]byte(line), &cmd) != nil {
			continue
		}
		typ, _ := cmd["type"].(string)
		switch typ {
		case "prompt":
			p.mu.Lock()
			p.message, _ = cmd["message"].(string)
			p.mu.Unlock()
			close(p.prompt)
		case "abort":
			close(p.abort)
		}
		go p.emit(map[string]any{"id": cmd["id"], "type": "response", "command": typ, "success": true, "data": map[string]any{}})
	}
	return len(b), nil
}

// prompted is the message pi was prompted with.
func (p *scriptedPi) prompted() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.message
}

func (p *scriptedPi) Stdout() io.Reader     { return p.r }
func (p *scriptedPi) Done() <-chan struct{} { return p.done }
func (p *scriptedPi) CloseInput()           { p.exit() }
func (p *scriptedPi) Kill()                 { p.exit() }
func (p *scriptedPi) exit() {
	p.once.Do(func() {
		_ = p.w.Close()
		close(p.done)
	})
}

func (p *scriptedPi) emit(v any) {
	b, _ := json.Marshal(v)
	p.wmu.Lock()
	defer p.wmu.Unlock()
	select {
	case <-p.done:
	default:
		_, _ = p.w.Write(append(b, '\n'))
	}
}

// bridge calls a route of the server with the process credential.
func (p *scriptedPi) bridge(method, path string, body any) (int, map[string]any) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(jsonBody(body))
	}
	req, _ := http.NewRequest(method, p.url+path, rd)
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	var env map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&env)
	d, _ := env["data"].(map[string]any)
	return resp.StatusCode, d
}

type scriptedLauncher struct {
	mu      sync.Mutex
	procs   []*scriptedPi
	script  func(p *scriptedPi)
	started chan *scriptedPi
	// hold, when set, keeps every script from producing events until it is closed.
	hold chan struct{}
	once sync.Once
}

func (l *scriptedLauncher) release() { l.once.Do(func() { close(l.hold) }) }

func (l *scriptedLauncher) AuthCheck(context.Context, string, string, []string) error { return nil }

func (l *scriptedLauncher) Version(context.Context, string, []string) (string, error) {
	return "1.0.4\n", nil
}

func (l *scriptedLauncher) Start(spec agent.Spec, _ io.Writer) (agent.Process, error) {
	r, w := io.Pipe()
	p := &scriptedPi{spec: spec, r: r, w: w, done: make(chan struct{}), prompt: make(chan struct{}), abort: make(chan struct{}), gate: l.hold}
	for _, kv := range spec.Env {
		if v, ok := strings.CutPrefix(kv, "APPLYSMITH_AGENT_URL="); ok {
			p.url = v
		}
		if v, ok := strings.CutPrefix(kv, "APPLYSMITH_AGENT_TOKEN="); ok {
			p.token = v
		}
	}
	l.mu.Lock()
	l.procs = append(l.procs, p)
	l.mu.Unlock()
	l.started <- p
	go l.script(p)
	return p, nil
}

// next waits for the next process the server started.
func (l *scriptedLauncher) next() *scriptedPi { return <-l.started }

func (l *scriptedLauncher) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.procs)
}

func piStart(p *scriptedPi) {
	status, cat := p.bridge("GET", "/internal/agent/catalog", nil)
	tools, ok := cat["tools"].([]any)
	if status != 200 || !ok {
		// The server already ended this process (its token was revoked or the
		// server closed), so the script ends the way a killed process would; a
		// test that needed this turn fails on its own wait.
		runtime.Goexit()
	}
	var names []string
	for _, t := range tools {
		names = append(names, t.(map[string]any)["name"].(string))
	}
	p.bridge("POST", "/internal/agent/hello", map[string]any{"tools": names})
	<-p.prompt
	p.bridge("GET", "/internal/agent/turn-context", nil)
	if p.gate != nil {
		<-p.gate
	}
}

func assistantLine(content []map[string]any, stop string) map[string]any {
	return map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": content, "stopReason": stop,
		"provider": "applysmith-openrouter", "model": "openai/gpt-6.1-sol", "usage": map[string]any{"input": 100, "output": 10, "totalTokens": 110, "cost": map[string]any{"total": 0.0002}}}}
}

// trackerTurn plays: one tracker_query call forwarded to the server, then an answer.
func trackerTurn(p *scriptedPi) {
	piStart(p)
	args := map[string]any{"status": "applied", "step": "Counting applied rows"}
	p.emit(assistantLine([]map[string]any{{"type": "toolCall", "id": "call_1", "name": "tracker_query", "arguments": args}}, "toolUse"))
	p.emit(map[string]any{"type": "tool_execution_start", "toolCallId": "call_1", "toolName": "tracker_query", "args": args})
	_, res := p.bridge("POST", "/internal/agent/tool-call", map[string]any{"toolCallId": "call_1", "name": "tracker_query", "args": args})
	p.emit(map[string]any{"type": "tool_execution_end", "toolCallId": "call_1", "toolName": "tracker_query",
		"result": map[string]any{"content": []map[string]any{{"type": "text", "text": res["text"]}}}, "isError": res["isError"]})
	p.emit(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "No rows yet."}})
	p.emit(assistantLine([]map[string]any{{"type": "text", "text": "No rows yet."}}, "stop"))
	p.emit(map[string]any{"type": "agent_settled"})
}

func chatHarness(t *testing.T, script func(p *scriptedPi), mods ...func(*Config)) (*harness, *scriptedLauncher) {
	t.Helper()
	l := &scriptedLauncher{script: script, started: make(chan *scriptedPi, 8), hold: make(chan struct{})}
	h := startDomainHarness(t, false, append([]func(*Config){func(c *Config) {
		c.Domain.PiLauncher = l
		c.Domain.PiPath = "/opt/pi"
	}}, mods...)...)
	h.configurePreset("openrouter")
	return h, l
}

func (h *harness) chat(cookie, method, path string, body any) result {
	h.t.Helper()
	c := call{method: method, path: path, cookie: cookie, headers: map[string]string{"Origin": h.srv.Origin()}}
	if body != nil {
		c.contentType, c.body = "application/json", jsonBody(body)
	}
	return h.do(c)
}

type sseItem struct {
	id    string
	event string
	chat  wire.ChatEvent
}

// readStream reads a conversation's event stream until stop accepts an event,
// then closes the connection (a conversation stream stays open after a turn).
func readStream(t *testing.T, h *harness, sessionID, lastID string, stop func(sseItem) bool, onOpen ...func()) []sseItem {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", h.srv.Origin()+"/api/chat/sessions/"+sessionID+"/events", nil)
	req.Header.Set("Authorization", "Bearer "+h.token())
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("stream: %d %s", resp.StatusCode, b)
	}
	var out []sseItem
	var cur sseItem
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "id: "):
			cur.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			cur.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			if cur.event == "chat" {
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &cur.chat)
			}
		case line == "":
			if cur.event != "" {
				out = append(out, cur)
				if len(out) == 1 {
					for _, f := range onOpen {
						f()
					}
				}
				if stop(cur) {
					return out
				}
			}
			cur = sseItem{}
		}
	}
	return out
}

func terminalItem(it sseItem) bool {
	return it.event == "chat" && (it.chat.Type == wire.ChatFinal || it.chat.Type == wire.ChatError)
}

func (h *harness) waitIdle(sessionID string) {
	h.t.Helper()
	h.srv.domain.Load().agent.Close() // waits for the running turns; the server shuts down at cleanup anyway
}

func TestChatAPI_ATurnRunsOverHTTPAndStreamsThroughTheSession(t *testing.T) {
	h, l := chatHarness(t, trackerTurn)
	cookie := h.session()
	r := h.chat(cookie, "POST", "/api/chat/sessions/new/messages", map[string]any{"text": "Tell me about the Applied rows."})
	var acc wire.Success[wire.ChatTurnAccepted]
	strictInto(t, r.body, &acc)
	if r.StatusCode != 202 || acc.Data.SessionID == "" || acc.Data.TurnID == "" || r.Header.Get("Location") != "/api/chat/sessions/"+acc.Data.SessionID+"/events" {
		t.Fatalf("post: %d %s %v", r.StatusCode, r.body, r.Header)
	}
	items := readStream(t, h, acc.Data.SessionID, "", terminalItem, l.release)
	var kinds []string
	for _, it := range items {
		kinds = append(kinds, it.chat.Type)
		if it.event != "chat" || it.id == "" && it.chat.Type != wire.ChatState {
			t.Errorf("item %+v", it)
		}
	}
	got := strings.Join(kinds, " ")
	if !strings.HasPrefix(got, "state ") || !strings.Contains(got, "turn.start tool.call tool.result") || !strings.HasSuffix(got, "token final") {
		t.Fatalf("stream = %s", got)
	}
	for _, it := range items {
		if it.chat.Type == wire.ChatToolCall && (it.chat.Tool != "tracker_query" || it.chat.Class != "read" || it.chat.Phrase != "Counting applied rows") {
			t.Errorf("tool.call = %+v", it.chat)
		}
	}

	// What the process was given.
	p := l.next()
	env := strings.Join(p.spec.Env, "\n")
	if strings.Contains(env, testSecret) || !strings.Contains(env, "APPLYSMITH_AGENT_URL="+h.srv.Origin()) {
		t.Errorf("child environment: %s", env)
	}
	if strings.Contains(strings.Join(p.spec.Args, " "), testSecret) {
		t.Error("the key is in argv")
	}

	// The record, the audit and the ledger.
	detail := h.get("/api/chat/sessions/" + acc.Data.SessionID)
	var d wire.Success[wire.ChatSessionDetail]
	strictInto(t, detail.body, &d)
	var roles []string
	for _, m := range d.Data.Messages {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, " ") != "user tool_call tool_result assistant" || len(d.Data.Turns) != 1 || d.Data.Turns[0].Status != "final" {
		t.Fatalf("detail = %+v", d.Data)
	}
	for _, m := range d.Data.Messages {
		wantClass := ""
		if m.Role == "tool_call" || m.Role == "tool_result" {
			wantClass = "read"
		}
		if m.Class != wantClass {
			t.Errorf("%s row class = %q, want %q", m.Role, m.Class, wantClass)
		}
	}
	if d.Data.Session.Title != "Tell me about the Applied rows." {
		t.Errorf("title = %q", d.Data.Session.Title)
	}
	var gatesRun, ledger int
	st := h.srv.store.Load().Read()
	if err := st.QueryRow(`SELECT count(*) FROM gate_decisions WHERE turn_id = ? AND tool = 'tracker_query'`, acc.Data.TurnID).Scan(&gatesRun); err != nil {
		t.Fatal(err)
	}
	// The gateway writes the rows of the model calls; pi's own usage events
	// write none (the scripted pi made no model call).
	if err := st.QueryRow(`SELECT count(*) FROM llm_ledger WHERE task = 'agent-kernel' AND run_id = ?`, acc.Data.SessionID).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if gatesRun == 0 || ledger != 0 {
		t.Fatalf("audit rows %d, ledger rows %d", gatesRun, ledger)
	}
}

func TestChatAPI_MessageLimitAndNoRowForARefusedMessage(t *testing.T) {
	h, l := chatHarness(t, trackerTurn)
	cookie := h.session()
	r := h.chat(cookie, "POST", "/api/chat/sessions/new/messages", map[string]any{"text": strings.Repeat("a", 8001)})
	if r.StatusCode != 422 || r.errCode() != "validation_failed" || !contains(string(r.body), "8000") {
		t.Fatalf("8,001 characters: %d %s", r.StatusCode, r.body)
	}
	var n int
	if err := h.srv.store.Load().Read().QueryRow(`SELECT count(*) FROM chat_sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 || l.count() != 0 {
		t.Fatalf("%d conversations, %d processes after a refused message", n, l.count())
	}
	if r := h.chat(cookie, "POST", "/api/chat/sessions/new/messages", map[string]any{"text": ""}); r.StatusCode != 422 {
		t.Fatalf("empty: %d %s", r.StatusCode, r.body)
	}
	if r := h.chat(cookie, "POST", "/api/chat/sessions/ses_missing/messages", map[string]any{"text": "hi"}); r.StatusCode != 404 {
		t.Fatalf("missing conversation: %d %s", r.StatusCode, r.body)
	}
}

func TestChatAPI_CredentialsDoNotCross(t *testing.T) {
	release := make(chan struct{})
	h, l := chatHarness(t, func(p *scriptedPi) {
		piStart(p)
		<-release
		p.emit(assistantLine([]map[string]any{{"type": "text", "text": "ok"}}, "stop"))
		p.emit(map[string]any{"type": "agent_settled"})
	})
	cookie := h.session()
	var acc wire.Success[wire.ChatTurnAccepted]
	strictInto(t, h.chat(cookie, "POST", "/api/chat/sessions/new/messages", map[string]any{"text": "hi"}).body, &acc)
	p := l.next()
	<-p.prompt // the script is past its handshake: the process holds a live credential
	l.release()

	// A user's token is no process credential, and a missing one is none either.
	for _, c := range []call{{path: "/internal/agent/catalog", useToken: true}, {path: "/internal/agent/catalog"}, {path: "/internal/agent/catalog", cookie: cookie}} {
		if r := h.do(c); r.StatusCode != 401 {
			t.Errorf("%+v reached a process route: %d %s", c, r.StatusCode, r.body)
		}
	}
	// A process credential works there and nowhere else.
	if r := h.do(call{path: "/internal/agent/catalog", bearer: p.token}); r.StatusCode != 200 {
		t.Fatalf("process credential: %d %s", r.StatusCode, r.body)
	}
	for _, path := range []string{"/api/chat/sessions", "/api/tracker/summary", "/api/llm/routing", "/api/llm/cost"} {
		if r := h.do(call{path: path, bearer: p.token}); r.StatusCode != 401 {
			t.Errorf("the process credential reached %s: %d", path, r.StatusCode)
		}
	}
	close(release)
	readStream(t, h, acc.Data.SessionID, "", terminalItem)
	h.srv.domain.Load().agent.Close()
	if r := h.do(call{path: "/internal/agent/catalog", bearer: p.token}); r.StatusCode != 401 {
		t.Fatalf("the credential outlived the process: %d", r.StatusCode)
	}
}

func TestChatAPI_WritesNeedTheDashboardSession(t *testing.T) {
	h, _ := chatHarness(t, trackerTurn)
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/chat/sessions/new/messages"}, {"POST", "/api/chat/sessions/ses_1/steer"}, {"POST", "/api/chat/sessions/ses_1/cancel"},
		{"POST", "/api/chat/sessions/ses_1/fork"}, {"PATCH", "/api/chat/sessions/ses_1"}, {"DELETE", "/api/chat/sessions/ses_1"},
	} {
		var body any
		if c.method != "DELETE" && !strings.HasSuffix(c.path, "cancel") {
			body = map[string]any{"text": "x"}
		}
		r := h.send(c.method, c.path, body)
		if r.StatusCode != 403 || r.errReason() != wire.ReasonSessionOnly {
			t.Errorf("%s %s with the bearer token: %d %s", c.method, c.path, r.StatusCode, r.body)
		}
	}
	for _, c := range []string{"/api/chat/sessions", "/api/chat/sessions/ses_none"} {
		if r := h.get(c); r.StatusCode == 403 {
			t.Errorf("reading %s needs no session: %d", c, r.StatusCode)
		}
	}
}

func TestChatAPI_SteerAndCancelAnswerConflictWithoutATurnAndCancelStopsOne(t *testing.T) {
	hung := func(p *scriptedPi) {
		piStart(p)
		p.emit(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "thinking"}})
		<-p.abort
		p.emit(map[string]any{"type": "agent_settled"})
	}
	h, l := chatHarness(t, hung)
	l.release()
	cookie := h.session()
	var acc wire.Success[wire.ChatTurnAccepted]
	strictInto(t, h.chat(cookie, "POST", "/api/chat/sessions/new/messages", map[string]any{"text": "go"}).body, &acc)
	id := acc.Data.SessionID
	if r := h.chat(cookie, "POST", "/api/chat/sessions/"+id+"/messages", map[string]any{"text": "again"}); r.StatusCode != 409 {
		t.Fatalf("a second message during a turn: %d %s", r.StatusCode, r.body)
	}
	if r := h.chat(cookie, "DELETE", "/api/chat/sessions/"+id, nil); r.StatusCode != 409 {
		t.Fatalf("deleting a conversation with a turn running: %d %s", r.StatusCode, r.body)
	}
	steer := h.chat(cookie, "POST", "/api/chat/sessions/"+id+"/steer", map[string]any{"text": "be brief"})
	var sa wire.Success[wire.ChatSteerAccepted]
	strictInto(t, steer.body, &sa)
	if steer.StatusCode != 202 || sa.Data.Queued != 1 || sa.Data.TurnID != acc.Data.TurnID {
		t.Fatalf("steer: %d %s", steer.StatusCode, steer.body)
	}
	cancel := h.chat(cookie, "POST", "/api/chat/sessions/"+id+"/cancel", nil)
	var ca wire.Success[wire.ChatCancelResult]
	strictInto(t, cancel.body, &ca)
	if cancel.StatusCode != 202 || ca.Data.TurnID != acc.Data.TurnID || ca.Data.Runs == nil || !strings.Contains(ca.Data.Note, "cancelled") {
		t.Fatalf("cancel: %d %s", cancel.StatusCode, cancel.body)
	}
	// Wait for the turn to settle on its own: closing the agent here would
	// cancel its context too, and the turn may then report the server stopping
	// instead of the cancel request.
	waitLong(t, "the cancelled turn to end", func() bool {
		_, running := h.srv.domain.Load().agent.ActiveTurn(id)
		return !running
	})
	var detail wire.Success[wire.ChatSessionDetail]
	strictInto(t, h.get("/api/chat/sessions/"+id).body, &detail)
	if turn := detail.Data.Turns[0]; turn.Status != "error" || turn.Reason != "cancelled" || turn.FinishedAt == nil {
		t.Fatalf("turn = %+v", turn)
	}
	if r := h.chat(cookie, "POST", "/api/chat/sessions/"+id+"/cancel", nil); r.StatusCode != 409 || r.errCode() != "conflict" {
		t.Errorf("cancel with no turn running: %d %s", r.StatusCode, r.body)
	}
}

func TestChatAPI_ListRenameForkAndDelete(t *testing.T) {
	h, l := chatHarness(t, trackerTurn)
	cookie := h.session()
	var acc wire.Success[wire.ChatTurnAccepted]
	strictInto(t, h.chat(cookie, "POST", "/api/chat/sessions/new/messages", map[string]any{"text": "count them"}).body, &acc)
	id := acc.Data.SessionID
	readStream(t, h, id, "", terminalItem, l.release)
	h.srv.domain.Load().agent.Close()

	var page wire.Success[wire.ChatSessionPage]
	strictInto(t, h.get("/api/chat/sessions").body, &page)
	if page.Data.Total != 1 || page.Data.Sessions[0].ID != id || page.Data.Sessions[0].Channel != "dashboard" {
		t.Fatalf("list = %+v", page.Data)
	}
	if r := h.get("/api/chat/sessions?channel=email"); r.StatusCode != 422 {
		t.Errorf("unknown channel: %d", r.StatusCode)
	}
	var ren wire.Success[wire.ChatSession]
	strictInto(t, h.chat(cookie, "PATCH", "/api/chat/sessions/"+id, map[string]any{"title": "Tracker counts"}).body, &ren)
	if ren.Data.Title != "Tracker counts" {
		t.Fatalf("rename = %+v", ren.Data)
	}

	var detail wire.Success[wire.ChatSessionDetail]
	strictInto(t, h.get("/api/chat/sessions/"+id).body, &detail)
	second := detail.Data.Messages[1] // the tool call
	fr := h.chat(cookie, "POST", "/api/chat/sessions/"+id+"/fork", map[string]any{"messageId": second.ID})
	var fork wire.Success[wire.ChatSession]
	strictInto(t, fr.body, &fork)
	if fr.StatusCode != 201 || fork.Data.ParentID != id || fork.Data.ParentMessageID != second.ID {
		t.Fatalf("fork: %d %s", fr.StatusCode, fr.body)
	}
	var fd wire.Success[wire.ChatSessionDetail]
	strictInto(t, h.get("/api/chat/sessions/"+fork.Data.ID).body, &fd)
	if len(fd.Data.Messages) != 2 {
		t.Fatalf("fork holds %d messages, want the first two", len(fd.Data.Messages))
	}
	if r := h.chat(cookie, "POST", "/api/chat/sessions/"+id+"/fork", map[string]any{"messageId": 99999}); r.StatusCode != 404 {
		t.Errorf("fork at a message of nobody: %d", r.StatusCode)
	}

	if r := h.chat(cookie, "DELETE", "/api/chat/sessions/"+id, nil); r.StatusCode != 200 {
		t.Fatalf("delete: %d %s", r.StatusCode, r.body)
	}
	if r := h.get("/api/chat/sessions/" + id); r.StatusCode != 404 {
		t.Errorf("deleted conversation: %d", r.StatusCode)
	}
	if r := h.get("/api/chat/sessions/" + fork.Data.ID); r.StatusCode != 200 {
		t.Errorf("the fork survives its parent: %d", r.StatusCode)
	}
}

// kept reports whether the conversation still has a stream.
func (c *chatStreams) kept(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.items[id] != nil
}

// A conversation's stream is looked at again every retention period, so one
// that was being read when its turn ended is still dropped once the reader
// has left and nothing was published since.
func TestChatStreamIsRetiredOnceItHasBeenIdleEvenWhenItWasReadAtTheTurnsEnd(t *testing.T) {
	c := newChatStreams("e", time.Now)
	c.retention = 30 * time.Millisecond
	es := c.stream("chat_read")
	_, sub, _ := es.subscribe("", 0, false)
	c.Publish("chat_read", wire.ChatEvent{Type: wire.ChatFinal})
	time.Sleep(4 * c.retention)
	if !c.kept("chat_read") {
		t.Fatal("a stream with a reader was dropped")
	}
	es.unsubscribe(sub)
	deadline := time.Now().Add(3 * time.Second)
	for c.kept("chat_read") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if c.kept("chat_read") {
		t.Error("the stream was kept after its reader left and the retention period passed")
	}
}
