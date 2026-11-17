package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/store"
	"applysmith/internal/vault"
)

var bg = context.Background()

// A syntactically valid token that exists only in tests.
func fakeToken() string { return "123456789:" + strings.Repeat("Tk_x-", 7) }

const (
	ownerChat = int64(1001)
	ownerUser = int64(2002)
)

// fakeClock advances only when something sleeps, so the poller's pauses and
// backoffs cost no real time.
type fakeClock struct {
	mu     sync.Mutex
	t      time.Time
	sleeps []time.Duration
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.sleeps = append(c.sleeps, d)
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Millisecond):
		return nil
	}
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func (c *fakeClock) slept() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

type sentMsg struct {
	Chat      int64
	Text      string
	ParseMode string
	Markup    *Markup
	ID        int64
	ReplyTo   int64 // reply_parameters.message_id
}

type editedMsg struct {
	Chat, Msg int64
	Text      string
	Markup    *Markup
}

// fakeBot is the Bot API on an httptest server.
type fakeBot struct {
	t   *testing.T
	srv *httptest.Server

	mu          sync.Mutex
	token       string
	queue       []Update
	confirmed   int64
	sent        []sentMsg
	edits       []editedMsg
	answers     []string
	answerTexts []string
	actions     int
	polls       int
	nextMsg     int64
	conflicts   int
	failSends   int
	forbid      int // answer the next sends with 403, as when the owner blocked the bot
	failNth     int // the nth sendMessage from now fails once
	editStatus  int
	failEdits   int           // answer the next edits with 500
	retryAfter  int           // answer the next send with 429
	refuseHTML  int           // refuse the next HTML sends as Telegram refuses bad markup
	gate        chan struct{} // while set, sendMessage waits for it to close
}

func newFakeBot(t *testing.T) *fakeBot {
	f := &fakeBot{t: t, token: fakeToken(), nextMsg: 500}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeBot) reply(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (f *fakeBot) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/sendMessage") {
		f.mu.Lock()
		gate := f.gate
		f.mu.Unlock()
		if gate != nil {
			<-gate
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/bot")
	tok, method, _ := strings.Cut(path, "/")
	if tok != f.token {
		f.reply(w, 401, map[string]any{"ok": false, "error_code": 401, "description": "Unauthorized"})
		return
	}
	var p map[string]any
	_ = json.NewDecoder(r.Body).Decode(&p)
	num := func(k string) int64 { v, _ := p[k].(float64); return int64(v) }
	switch method {
	case "getMe":
		f.reply(w, 200, map[string]any{"ok": true, "result": map[string]any{"id": 999, "is_bot": true, "username": "ExampleBot"}})
	case "getUpdates":
		f.polls++
		if f.conflicts > 0 {
			f.conflicts--
			f.reply(w, 409, map[string]any{"ok": false, "error_code": 409, "description": "Conflict: terminated by other getUpdates request"})
			return
		}
		if off := num("offset"); off > f.confirmed {
			f.confirmed = off
		}
		var out []Update
		for _, u := range f.queue {
			if u.UpdateID >= f.confirmed {
				out = append(out, u)
			}
		}
		if out == nil {
			out = []Update{}
		}
		f.reply(w, 200, map[string]any{"ok": true, "result": out})
	case "sendMessage":
		if f.retryAfter > 0 {
			ra := f.retryAfter
			f.retryAfter = 0
			f.reply(w, 429, map[string]any{"ok": false, "error_code": 429, "description": "Too Many Requests", "parameters": map[string]any{"retry_after": ra}})
			return
		}
		if pm, _ := p["parse_mode"].(string); pm != "" && f.refuseHTML > 0 {
			f.refuseHTML--
			f.reply(w, 400, map[string]any{"ok": false, "error_code": 400, "description": "Bad Request: can't parse entities: Unsupported start tag"})
			return
		}
		if f.failNth > 0 {
			f.failNth--
			if f.failNth == 0 {
				f.reply(w, 500, map[string]any{"ok": false, "error_code": 500, "description": "Internal"})
				return
			}
		}
		if f.forbid > 0 {
			f.forbid--
			f.reply(w, 403, map[string]any{"ok": false, "error_code": 403, "description": "Forbidden: bot was blocked by the user"})
			return
		}
		if f.failSends > 0 {
			f.failSends--
			f.reply(w, 500, map[string]any{"ok": false, "error_code": 500, "description": "Internal"})
			return
		}
		f.nextMsg++
		m := sentMsg{Chat: num("chat_id"), ID: f.nextMsg}
		m.Text, _ = p["text"].(string)
		m.ParseMode, _ = p["parse_mode"].(string)
		m.Markup = decodeMarkup(p["reply_markup"])
		if rp, ok := p["reply_parameters"].(map[string]any); ok {
			v, _ := rp["message_id"].(float64)
			m.ReplyTo = int64(v)
		}
		f.sent = append(f.sent, m)
		f.reply(w, 200, map[string]any{"ok": true, "result": map[string]any{"message_id": m.ID, "chat": map[string]any{"id": m.Chat, "type": "private"}}})
	case "editMessageText":
		if f.failEdits > 0 {
			f.failEdits--
			f.reply(w, 500, map[string]any{"ok": false, "error_code": 500, "description": "Internal"})
			return
		}
		if f.editStatus != 0 {
			f.reply(w, f.editStatus, map[string]any{"ok": false, "error_code": f.editStatus, "description": "Bad Request: message can't be edited"})
			return
		}
		e := editedMsg{Chat: num("chat_id"), Msg: num("message_id")}
		e.Text, _ = p["text"].(string)
		e.Markup = decodeMarkup(p["reply_markup"])
		f.edits = append(f.edits, e)
		f.reply(w, 200, map[string]any{"ok": true, "result": true})
	case "answerCallbackQuery":
		id, _ := p["callback_query_id"].(string)
		txt, _ := p["text"].(string)
		f.answers = append(f.answers, id)
		f.answerTexts = append(f.answerTexts, txt)
		f.reply(w, 200, map[string]any{"ok": true, "result": true})
	case "setMyCommands":
		f.reply(w, 200, map[string]any{"ok": true, "result": true})
	case "sendChatAction":
		f.actions++
		f.reply(w, 200, map[string]any{"ok": true, "result": true})
	default:
		f.reply(w, 404, map[string]any{"ok": false, "error_code": 404, "description": "Not Found"})
	}
}

func decodeMarkup(v any) *Markup {
	if v == nil {
		return nil
	}
	raw, _ := json.Marshal(v)
	var m Markup
	_ = json.Unmarshal(raw, &m)
	return &m
}

func (f *fakeBot) push(u Update) {
	f.mu.Lock()
	f.queue = append(f.queue, u)
	f.mu.Unlock()
}

func (f *fakeBot) sentTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.sent {
		out = append(out, m.Text)
	}
	return out
}

func (f *fakeBot) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func (f *fakeBot) lastSent() sentMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return sentMsg{}
	}
	return f.sent[len(f.sent)-1]
}

func (f *fakeBot) set(fn func()) {
	f.mu.Lock()
	fn()
	f.mu.Unlock()
}

// fakeBackend records what the gateway asked of the product.
type fakeBackend struct {
	mu        sync.Mutex
	mails     []string
	mailErr   error
	asks      []AskRequest
	answer    string
	askErr    error
	askDelay  time.Duration
	started   []string
	acks      []string
	sources   []string
	evals     []string
	evalErr   error
	status    string
	today     string
	sessionID string
	askCalls  int
	// gate, when set, holds every turn started after it was set until it is
	// closed.
	gate        chan struct{}
	turns       map[string]*fakeTurn
	answerReads int
	stops       []string

	// approvals turns the Approve button on; it records each press.
	approvals  bool
	approved   []string
	approveMsg string
	approveErr error

	// ratings turns the digest rating buttons on; it records each tap.
	ratings bool
	rates   []string
	rateErr error
}

func (b *fakeBackend) Status(ctx context.Context) (string, error) {
	if b.status != "" {
		return b.status, nil
	}
	return "status: ok", nil
}
func (b *fakeBackend) Tracker(context.Context) (string, error)  { return "tracker: 3 applied", nil }
func (b *fakeBackend) Pipeline(context.Context) (string, error) { return "pipeline: 12", nil }
func (b *fakeBackend) Queue(context.Context) (string, error)    { return "queue: 4", nil }
func (b *fakeBackend) Gmail(context.Context) (string, error)    { return "gmail: quiet", nil }
func (b *fakeBackend) Inbox(context.Context) (string, error)    { return "inbox: nothing", nil }
func (b *fakeBackend) Today(context.Context) (string, error) {
	if b.today != "" {
		return b.today, nil
	}
	return "today: 2 found", nil
}
func (b *fakeBackend) Open(_ context.Context, screen string) (string, error) {
	return "open " + screen, nil
}
func (b *fakeBackend) Trace(_ context.Context, sid string) (string, error) {
	return "trace of " + sid, nil
}
func (b *fakeBackend) ScanSources(context.Context) ([]string, error) {
	if b.sources == nil {
		return []string{"greenhouse", "lever"}, nil
	}
	return b.sources, nil
}

// fakeTurn is a turn the fake agent runs: it ends askDelay after it started,
// or when gate is closed, or when Stop cancels it.
type fakeTurn struct {
	session string
	text    string
	readyAt time.Time
	gate    chan struct{}
	stopped bool
}

func (b *fakeBackend) Ask(_ context.Context, req AskRequest) (Asked, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.asks = append(b.asks, req)
	b.askCalls++
	if b.askErr != nil {
		return Asked{}, b.askErr
	}
	sid := b.sessionID
	if sid == "" {
		sid = "sess-1"
		if req.SessionID != "" {
			sid = req.SessionID
		}
	}
	ans := b.answer
	if ans == "" {
		ans = "an answer"
	}
	if b.turns == nil {
		b.turns = map[string]*fakeTurn{}
	}
	id := "turn-" + strconv.Itoa(b.askCalls)
	b.turns[id] = &fakeTurn{session: sid, text: ans, readyAt: time.Now().Add(b.askDelay), gate: b.gate}
	return Asked{SessionID: sid, TurnID: id}, nil
}

func (b *fakeBackend) Answer(_ context.Context, sessionID, turnID string) (Answer, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.answerReads++
	t, ok := b.turns[turnID]
	switch {
	case !ok || t.session != sessionID:
		return Answer{}, errors.New("no such turn")
	case t.stopped:
		return Answer{Done: true, Text: "Stopped."}, nil
	case time.Now().Before(t.readyAt):
		return Answer{}, nil
	}
	if t.gate != nil {
		select {
		case <-t.gate:
		default:
			return Answer{}, nil
		}
	}
	return Answer{Done: true, Text: t.text}, nil
}

func (b *fakeBackend) Stop(_ context.Context, sessionID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stops = append(b.stops, sessionID)
	for _, t := range b.turns {
		if t.session == sessionID && !t.stopped {
			done := !time.Now().Before(t.readyAt)
			if t.gate != nil {
				select {
				case <-t.gate:
				default:
					done = false
				}
			}
			if !done {
				t.stopped = true
				return nil
			}
		}
	}
	return ErrNoTurn
}

func (b *fakeBackend) StartScan(_ context.Context, source string) (Started, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.started = append(b.started, source)
	return Started{RunID: "run-scan", Link: "/runs/run-scan"}, nil
}

func (b *fakeBackend) StartEvaluation(_ context.Context, u string) (Started, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.evalErr != nil {
		return Started{}, b.evalErr
	}
	b.evals = append(b.evals, u)
	return Started{RunID: "run-eval", Link: "/runs/run-eval"}, nil
}

func (b *fakeBackend) Ack(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.acks = append(b.acks, id)
	return nil
}

func (b *fakeBackend) Approve(_ context.Context, id string, approve bool) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.approvals {
		return "", errors.ErrUnsupported
	}
	b.approved = append(b.approved, fmt.Sprintf("%s:%v", id, approve))
	return b.approveMsg, b.approveErr
}
func (b *fakeBackend) Mail(_ context.Context, action, thread string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.mails = append(b.mails, action+":"+thread)
	if b.mailErr != nil {
		return "", b.mailErr
	}
	return "did " + action, nil
}
func (b *fakeBackend) Rate(_ context.Context, item, verdict string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.ratings {
		return errors.ErrUnsupported
	}
	b.rates = append(b.rates, item+":"+verdict)
	return b.rateErr
}

func (b *fakeBackend) askList() []AskRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]AskRequest(nil), b.asks...)
}

type rig struct {
	cfg     Config
	t       *testing.T
	st      *store.Store
	state   *State
	bot     *fakeBot
	clk     *fakeClock
	vault   *vault.Vault
	backend *fakeBackend
	gw      *Gateway
	mgr     *Manager
	dbPath  string
}

type rigOpt func(*Config)

func newRig(t *testing.T, opts ...rigOpt) *rig {
	t.Helper()
	clk := &fakeClock{t: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "applysmith.db")
	st, err := store.Open(bg, path, store.Options{Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	v, err := vault.New("ws-test", vault.NewMemory(), vault.WithGetenv(func(string) string { return "" }))
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, st: st, clk: clk, vault: v, bot: newFakeBot(t), backend: &fakeBackend{}, dbPath: path}
	r.state = NewState(st, clk.Now)
	cfg := Config{State: r.state, Token: TokenSource(v), API: r.bot.srv.URL, Backend: r.backend, Clock: clk, Instance: "gw-a",
		TypingEvery: 20 * time.Millisecond, InterimAfter: time.Hour, AnswerPoll: 2 * time.Millisecond, Jitter: func() float64 { return 0.5 }}
	for _, o := range opts {
		o(&cfg)
	}
	r.cfg = cfg
	r.gw = New(cfg)
	// The followers of turns stop before the store closes.
	t.Cleanup(func() { r.gw.Close() })
	r.mgr = &Manager{State: r.state, Vault: v, API: r.bot.srv.URL, Changed: r.gw.Reconfigure}
	return r
}

func (r *rig) setToken() {
	r.t.Helper()
	if _, err := r.vault.Set(TokenSecret, fakeToken()); err != nil {
		r.t.Fatal(err)
	}
}

// pair binds the owner chat directly.
func (r *rig) pair() {
	r.t.Helper()
	r.setToken()
	code, err := r.state.BeginPairing(bg)
	if err != nil {
		r.t.Fatal(err)
	}
	if res, err := r.state.Claim(bg, ownerChat, ownerUser, code.Code); err != nil || res != Claimed {
		r.t.Fatalf("claim = %v %v", res, err)
	}
}

// run starts the gateway and stops it when the test ends.
func (r *rig) run() {
	r.t.Helper()
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() { r.gw.Run(ctx); close(done) }()
	r.t.Cleanup(func() { cancel(); <-done })
}

var nextUpdate int64 = 100

func msgUpdate(chat, user int64, kind, text string) Update {
	nextUpdate++
	return Update{UpdateID: nextUpdate, Message: &Message{MessageID: nextUpdate, Chat: Chat{ID: chat, Type: kind}, From: &User{ID: user}, Text: text}}
}

func ownerMsg(text string) Update { return msgUpdate(ownerChat, ownerUser, "private", text) }

func cbUpdate(user int64, data string, text string) Update {
	nextUpdate++
	return Update{UpdateID: nextUpdate, Callback: &Callback{ID: "cb-" + strconv.Itoa(int(nextUpdate)), From: User{ID: user}, Data: data,
		Message: &Message{MessageID: 77, Chat: Chat{ID: ownerChat, Type: "private"}, Text: text}}}
}

// eventually waits for cond, failing the test after five seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (r *rig) actions() []string {
	rows, err := r.st.Read().Query(`SELECT action FROM telegram_events ORDER BY id`)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		out = append(out, a)
	}
	return out
}

// serve runs only the outbound sender, for tests that hand updates to the
// gateway directly.
func (r *rig) serve() {
	r.t.Helper()
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	sender := r.gw.sender
	go func() { sender.Run(ctx); close(done) }()
	r.t.Cleanup(func() { cancel(); <-done })
}

func (r *rig) handle(u Update) bool {
	r.t.Helper()
	return r.gw.handle(bg, u)
}

// settle waits until every turn the chat started has its answer in the chat.
func (r *rig) settle() {
	r.t.Helper()
	eventually(r.t, "the answers to be in the chat", func() bool {
		open, err := r.state.OpenTurns(bg)
		return err == nil && len(open) == 0
	})
}

// restart stops the gateway's followers, as a stopped process would, and
// builds a new gateway over the same database and backend.
func (r *rig) restart() {
	r.t.Helper()
	r.gw.Close()
	r.gw = New(r.cfg)
}

// ReceiptState returns the state of an update's receipt, or "" when none.
func (s *State) ReceiptState(ctx context.Context, updateID int64) string {
	var st string
	_ = s.st.Read().QueryRowContext(ctx, `SELECT state FROM telegram_receipts WHERE update_id = ?`, updateID).Scan(&st)
	return st
}
