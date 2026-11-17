package api

import (
	"applysmith/internal/agent"
	"applysmith/internal/agent/approvals"
	"applysmith/internal/api/wire"
	"applysmith/internal/notify/telegram"
	"applysmith/internal/store"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// botAPI is a Telegram Bot API on a local server: it queues updates for
// getUpdates and records what the gateway sends.
type botAPI struct {
	srv    *httptest.Server
	token  string
	mu     sync.Mutex
	queue  []map[string]any
	sent   []map[string]any
	edits  []map[string]any
	nextID float64
}

func newBotAPI(t *testing.T) *botAPI {
	b := &botAPI{token: "123456789:" + strings.Repeat("Tk_x-", 7), nextID: 900}
	b.srv = httptest.NewServer(http.HandlerFunc(b.serve))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *botAPI) serve(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	tok, method, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/bot"), "/")
	out := func(status int, v map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	if tok != b.token {
		out(401, map[string]any{"ok": false, "error_code": 401, "description": "Unauthorized"})
		return
	}
	var p map[string]any
	_ = json.NewDecoder(r.Body).Decode(&p)
	switch method {
	case "getMe":
		out(200, map[string]any{"ok": true, "result": map[string]any{"id": 1, "is_bot": true, "username": "ExampleBot"}})
	case "getUpdates":
		off, _ := p["offset"].(float64)
		var rest []map[string]any
		res := []map[string]any{}
		for _, u := range b.queue {
			if u["update_id"].(float64) >= off {
				res = append(res, u)
				rest = append(rest, u)
			}
		}
		b.queue = rest
		if len(res) == 0 {
			b.mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			b.mu.Lock()
		}
		out(200, map[string]any{"ok": true, "result": res})
	case "sendMessage":
		b.nextID++
		p["message_id"] = b.nextID
		b.sent = append(b.sent, p)
		out(200, map[string]any{"ok": true, "result": map[string]any{"message_id": b.nextID, "chat": map[string]any{"id": p["chat_id"], "type": "private"}}})
	case "editMessageText":
		b.edits = append(b.edits, p)
		out(200, map[string]any{"ok": true, "result": true})
	default:
		out(200, map[string]any{"ok": true, "result": true})
	}
}

var botSeq = 5000.0

func (b *botAPI) say(chat, user int, text string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	botSeq++
	b.queue = append(b.queue, map[string]any{"update_id": botSeq, "message": map[string]any{"message_id": botSeq, "text": text,
		"chat": map[string]any{"id": chat, "type": "private"}, "from": map[string]any{"id": user}}})
}

func (b *botAPI) tap(chat, user int, data, text string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	botSeq++
	b.queue = append(b.queue, map[string]any{"update_id": botSeq, "callback_query": map[string]any{"id": "cb" + itoa(int(botSeq)), "data": data,
		"from": map[string]any{"id": user}, "message": map[string]any{"message_id": 42, "text": text, "chat": map[string]any{"id": chat, "type": "private"}}}})
}

func (b *botAPI) texts() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, m := range b.sent {
		out = append(out, m["text"].(string))
	}
	return out
}

func (b *botAPI) waitText(t *testing.T, sub string) string {
	t.Helper()
	// The gateway sends one message a second, so a busy test needs patience.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range b.texts() {
			if strings.Contains(s, sub) {
				return s
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for a Telegram message containing %q; got %q", sub, b.texts())
	return ""
}

// telegramTurn plays an Ask Agent turn that tries to start and submit an
// application, then answers.
func telegramTurn(p *scriptedPi) {
	piStart(p)
	for i, call := range []struct {
		name string
		args map[string]any
	}{
		{"apply_from_list", map[string]any{"submit": true, "step": "Applying"}},
		{"autofill_application", map[string]any{"url": "https://jobs.example.test/p/1", "submit": true, "step": "Filling"}},
	} {
		id := "call_" + itoa(i+1)
		p.emit(assistantLine([]map[string]any{{"type": "toolCall", "id": id, "name": call.name, "arguments": call.args}}, "toolUse"))
		p.emit(map[string]any{"type": "tool_execution_start", "toolCallId": id, "toolName": call.name, "args": call.args})
		_, res := p.bridge("POST", "/internal/agent/tool-call", map[string]any{"toolCallId": id, "name": call.name, "args": call.args})
		p.emit(map[string]any{"type": "tool_execution_end", "toolCallId": id, "toolName": call.name,
			"result": map[string]any{"content": []map[string]any{{"type": "text", "text": res["text"]}}}, "isError": res["isError"]})
	}
	const answer = "Submission is confirmed in the dashboard, so I did not apply."
	p.emit(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": answer}})
	p.emit(assistantLine([]map[string]any{{"type": "text", "text": answer}}, "stop"))
	p.emit(map[string]any{"type": "agent_settled"})
}

func TestTelegram_Gateway_PairingCommandsFreeTextAndDeniedApply(t *testing.T) {
	bot := newBotAPI(t)
	l := &scriptedLauncher{script: telegramTurn, started: make(chan *scriptedPi, 8), hold: make(chan struct{})}
	h := startDomainHarness(t, false, func(c *Config) {
		c.Domain.PiLauncher = l
		c.Domain.PiPath = "/opt/pi"
		c.Domain.TelegramAPI = bot.srv.URL
	})
	h.configurePreset("openrouter")
	l.release()

	// Setup: the token is validated, kept in the keychain and never echoed.
	if r := h.send("PUT", "/api/telegram/token", map[string]any{"token": "not a token"}); r.StatusCode != 422 || strings.Contains(string(r.body), "not a token") {
		t.Errorf("bad token: %d %s", r.StatusCode, r.body)
	}
	r := h.send("PUT", "/api/telegram/token", map[string]any{"token": bot.token})
	var st wire.Success[wire.TelegramStatus]
	strictInto(t, r.body, &st)
	if r.StatusCode != 200 || !st.Data.TokenSet || st.Data.BotUsername != "ExampleBot" || st.Data.Paired || strings.Contains(string(r.body), bot.token) {
		t.Fatalf("set token: %d %s", r.StatusCode, r.body)
	}
	if v, err := h.vault.Get("telegram-bot-token"); err != nil || v != bot.token {
		t.Fatalf("keychain: %v", err)
	}

	// Pairing from the dashboard's code.
	pc := h.send("POST", "/api/telegram/pairing-code", nil)
	var code wire.Success[wire.TelegramPairingCode]
	strictInto(t, pc.body, &code)
	if pc.StatusCode != 201 || len(code.Data.Code) != 10 || code.Data.BotUsername != "ExampleBot" {
		t.Fatalf("pairing code: %d %s", pc.StatusCode, pc.body)
	}
	// The link opens the bot with the code as its start parameter.
	if want := "https://t.me/ExampleBot?start=" + code.Data.Code; code.Data.Link != want {
		t.Errorf("link = %q, want %q", code.Data.Link, want)
	}
	bot.say(7, 8, code.Data.Code)
	bot.waitText(t, "Paired")
	waitLong(t, "the status to show a pairing", func() bool {
		strictInto(t, h.get("/api/telegram/status").body, &st)
		return st.Data.Paired
	})
	var cfg wire.Success[wire.NotificationSettings]
	strictInto(t, h.get("/api/notifications/settings").body, &cfg)
	if !cfg.Data.TelegramPaired || strings.Contains(string(h.get("/api/telegram/status").body), `"7"`) {
		t.Errorf("settings = %+v", cfg.Data)
	}
	if r := h.send("POST", "/api/telegram/pairing-code", nil); r.StatusCode != 409 {
		t.Errorf("a second code while paired: %d", r.StatusCode)
	}

	// Commands are built from the dashboard's read models.
	bot.say(7, 8, "/status")
	if text := bot.waitText(t, "Runs:"); !strings.Contains(text, "Server:") {
		t.Errorf("status = %q", text)
	}

	// Free text reaches the agent on the telegram channel, which refuses both
	// apply tools and submits nothing.
	bot.say(7, 8, "Please apply to https://jobs.example.test/p/1 for me")
	// The router answers this one itself and says why it will not.
	bot.waitText(t, "Telegram cannot start one")
	// A request the model has to handle: its apply calls are refused by name.
	bot.say(7, 8, "Could you quietly send in my application for the Example role")
	bot.waitText(t, "Submission is confirmed in the dashboard")
	var denied int
	if err := h.srv.store.Load().Read().QueryRow(`SELECT count(DISTINCT tool) FROM gate_decisions WHERE caller = 'telegram' AND verdict = 'deny' AND tool IN ('apply_from_list', 'autofill_application')`).Scan(&denied); err != nil || denied != 2 {
		t.Errorf("tools refused for telegram = %d %v", denied, err)
	}
	var channel string
	_ = h.srv.store.Load().Read().QueryRow(`SELECT channel FROM chat_sessions ORDER BY created_at DESC LIMIT 1`).Scan(&channel)
	if channel != "telegram" {
		t.Errorf("conversation channel = %q", channel)
	}
	var claims int
	_ = h.srv.store.Load().Read().QueryRow(`SELECT count(*) FROM submissions`).Scan(&claims)
	if claims != 0 {
		t.Errorf("a submission claim exists: %d", claims)
	}

	// Unpairing and clearing the token answer the dashboard; the gateway's own
	// tests cover what it ignores afterwards.
	if r := h.send("DELETE", "/api/telegram/pairing", nil); r.StatusCode != 200 {
		t.Fatalf("unpair: %d %s", r.StatusCode, r.body)
	}
	if r := h.send("DELETE", "/api/telegram/token", nil); r.StatusCode != 200 {
		t.Fatalf("clear: %d %s", r.StatusCode, r.body)
	}
}

func waitLong(t *testing.T, what string, ok func() bool) {
	t.Helper()
	pollUntil(t, what, 30*time.Second, ok)
}

// askAndWait asks as the gateway does and reads the turn until it ended.
func askAndWait(t *testing.T, b telegramBackend, req telegram.AskRequest) (telegram.Asked, string) {
	t.Helper()
	ctx := context.Background()
	asked, err := b.Ask(ctx, req)
	if err != nil {
		t.Fatalf("ask %q: %v", req.Text, err)
	}
	var text string
	waitLong(t, "the turn to end", func() bool {
		a, err := b.Answer(ctx, asked.SessionID, asked.TurnID)
		text = a.Text
		return err == nil && a.Done
	})
	return asked, text
}

// The pairing-code route says what the owner can do about a refusal (store a
// token, unpair) as a conflict; a failure of the server's own is an internal
// error that does not repeat the error's text.
func TestTelegram_PairingCode_OnlyARefusalIsAConflict(t *testing.T) {
	h := newDomainHarness(t)
	r := h.send("POST", "/api/telegram/pairing-code", nil)
	if r.StatusCode != 409 || !strings.Contains(string(r.body), "store the bot token first") {
		t.Fatalf("no token: %d %s", r.StatusCode, r.body)
	}
	if _, err := h.vault.Set(telegram.TokenSecret, "123456789:"+strings.Repeat("Tk_x-", 7)); err != nil {
		t.Fatal(err)
	}
	err := h.srv.store.Load().Write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`DROP TABLE telegram_pairing`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	r = h.send("POST", "/api/telegram/pairing-code", nil)
	if r.StatusCode != 500 || r.errCode() != "internal" || strings.Contains(string(r.body), "telegram_pairing") {
		t.Fatalf("a failed write: %d %s", r.StatusCode, r.body)
	}
}

// A card being sent to Telegram when the server stops belongs to the
// notification stack: closing the stack ends the send and waits for it.
func TestTelegram_ApprovalCard_TheSendEndsWhenTheStackCloses(t *testing.T) {
	asked, gaveUp, over := make(chan struct{}), make(chan struct{}), make(chan struct{})
	token := "123456789:" + strings.Repeat("Tk_x-", 7)
	var once sync.Once
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// The server notices a client that left only once the body is read.
		_, _ = io.Copy(io.Discard, r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			once.Do(func() { close(asked) })
			select {
			case <-r.Context().Done():
				close(gaveUp)
			case <-over:
			}
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			time.Sleep(20 * time.Millisecond)
			_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"username":"ExampleBot"}}`))
		}
	}))
	t.Cleanup(api.Close)
	t.Cleanup(func() { close(over) })
	h := startDomainHarness(t, false, func(c *Config) { c.Domain.TelegramAPI = api.URL })
	if _, err := h.vault.Set(telegram.TokenSecret, token); err != nil {
		t.Fatal(err)
	}
	ns := h.domainOf().notify
	code, err := ns.tgState.BeginPairing(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if res, err := ns.tgState.Claim(t.Context(), 7, 8, code.Code); err != nil || res != telegram.Claimed {
		t.Fatalf("pairing: %v %v", res, err)
	}
	ns.announceCard(t.Context(), approvals.Card{ID: "card_1", SessionID: "chat_1", Channel: string(agent.ChannelTelegram), Summary: "Apply to the posting", Tier: 1})
	select {
	case <-asked:
	case <-time.After(10 * time.Second):
		t.Fatal("the card was never sent")
	}
	ns.close()
	select {
	case <-gaveUp:
	case <-time.After(5 * time.Second):
		t.Fatal("the stack closed while the card was still being sent")
	}
}

func TestTelegram_Ask_ADeletedRollingConversationStartsANewOne(t *testing.T) {
	h := newDomainHarness(t)
	b := telegramBackend{s: h.srv}
	ctx := context.Background()
	first, text := askAndWait(t, b, telegram.AskRequest{Text: "how many applications are in interview?"})
	if first.SessionID == "" || text == "" {
		t.Fatalf("first = %+v, %q", first, text)
	}
	same, _ := askAndWait(t, b, telegram.AskRequest{SessionID: first.SessionID, Text: "how many applications are in interview?"})
	if same.SessionID != first.SessionID {
		t.Fatalf("the rolling conversation was not reused: %+v", same)
	}
	d, _ := h.srv.dom()
	if err := d.sessions.Delete(ctx, first.SessionID); err != nil {
		t.Fatal(err)
	}
	// The turn of a deleted conversation reads as done, and says why.
	if a, err := b.Answer(ctx, first.SessionID, first.TurnID); err != nil || !a.Done || !strings.Contains(a.Text, "deleted") {
		t.Errorf("answer of a deleted conversation = %+v, %v", a, err)
	}
	next, text := askAndWait(t, b, telegram.AskRequest{SessionID: first.SessionID, Text: "how many applications are in interview?"})
	if next.SessionID == "" || next.SessionID == first.SessionID || text == "" {
		t.Fatalf("after the delete = %+v, %q", next, text)
	}
	sess, err := d.sessions.Get(ctx, next.SessionID)
	if err != nil || string(sess.Channel) != "telegram" {
		t.Fatalf("the new conversation = %+v, %v", sess, err)
	}
	if err := b.Stop(ctx, next.SessionID); !errors.Is(err, telegram.ErrNoTurn) {
		t.Errorf("stop with no turn running = %v", err)
	}
}

// A run reaches the phone only when Telegram asked for it: a button's run
// under the telegram principal, or the agent's run in a Telegram
// conversation. The same work started at the desk, or by the agent in a
// dashboard conversation, does not.
func TestTelegram_AskedFromPhone_OnlyRunsTheChatAskedFor(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "applysmith.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	run := func(principal, key string) string {
		ref, err := st.CreateRun(ctx, store.RunRequest{Principal: principal, Operation: "scan", Key: key, Kind: "scan", Input: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		return ref.RunID
	}
	button, desk := run(telegramPrincipal, "tg-1"), run("local-token", "k-1")
	const t0, t1, t2, t3 = "2026-11-04T10:00:00.000Z", "2026-11-04T10:00:05.000Z", "2026-11-04T10:01:00.000Z", "2026-11-04T10:01:05.000Z"
	for _, q := range []string{
		`INSERT INTO chat_sessions (id, channel, created_at, updated_at) VALUES ('s-tg', 'telegram', '` + t0 + `', '` + t0 + `'),
			('s-dash', 'dashboard', '` + t0 + `', '` + t0 + `'), ('s-task', 'dashboard', '` + t0 + `', '` + t0 + `'), ('s-idea', 'dashboard', '` + t0 + `', '` + t0 + `')`,
		// A Telegram conversation; a dashboard one; a task's conversation whose
		// first turn was the desk's and whose second came from a phone reply.
		`INSERT INTO chat_turns (id, session_id, channel, origin, status, started_at, finished_at) VALUES
			('turn-tg', 's-tg', 'telegram', 'model', 'final', '` + t0 + `', '` + t1 + `'),
			('turn-dash', 's-dash', 'dashboard', 'model', 'final', '` + t0 + `', '` + t1 + `'),
			('turn-task-1', 's-task', 'dashboard', 'model', 'final', '` + t0 + `', '` + t1 + `'),
			('turn-task-2', 's-task', 'telegram', 'model', 'final', '` + t2 + `', '` + t3 + `'),
			('turn-idea', 's-idea', 'dashboard', 'model', 'final', '` + t2 + `', '` + t3 + `')`,
		`INSERT INTO agent_run_origins (run_id, session_id, tool, created_at) VALUES
			('run-agent-tg', 's-tg', 'evaluate_job', '` + t0 + `'), ('run-agent-dash', 's-dash', 'evaluate_job', '` + t0 + `'),
			('run-task-desk', 's-task', 'evaluate_job', '` + t1 + `'), ('run-task-phone', 's-task', 'evaluate_job', '` + t2 + `'),
			('run-queued-phone', 's-task', 'scan_portal', '2026-11-04T10:09:00.000Z'), ('run-idea', 's-idea', 'evaluate_job', '` + t0 + `')`,
	} {
		if err := st.Write(ctx, func(tx *sql.Tx) error { _, err := tx.ExecContext(ctx, q); return err }); err != nil {
			t.Fatal(err)
		}
	}
	asked := askedFromPhone(st)
	for id, want := range map[string]bool{button: true, desk: false, "run-agent-tg": true, "run-agent-dash": false,
		// The turn that started the run decides, not the conversation: a reply
		// from the phone into a task's conversation asked for its run, and a
		// start queued in that turn that began after it ended too.
		"run-task-desk": false, "run-task-phone": true, "run-queued-phone": true,
		// A dashboard press recorded the run before the conversation's first turn.
		"run-idea": false, "needs-schedule-3-sign-in": false} {
		if got := asked(ctx, id); got != want {
			t.Errorf("asked(%s) = %v, want %v", id, got, want)
		}
	}
}

// askFromPhone records a board entry's run as the agent's in a Telegram
// conversation, so the policy tells the phone of it.
func askFromPhone(t *testing.T, st *store.Store, entryID string) {
	t.Helper()
	const at = "2026-11-04T10:00:00.000Z"
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO chat_sessions (id, channel, created_at, updated_at) VALUES ('s-phone', 'telegram', ?, ?)`, at, at); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO chat_turns (id, session_id, channel, origin, status, started_at, finished_at)
			VALUES ('turn-phone', 's-phone', 'telegram', 'model', 'final', ?, ?)`, at, at); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO agent_run_origins (run_id, session_id, tool, created_at) VALUES (?, 's-phone', 'evaluate_job', ?)`, entryID, at)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
