package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"unicode/utf8"

	"applysmith/internal/agent/sessions"
	"applysmith/internal/api/wire"
	"applysmith/internal/llm"
	"applysmith/internal/store"
)

// Errors the service returns for the API layer to map.
var (
	// ErrMessageTooLong means a user message is over 8,000 characters.
	ErrMessageTooLong = fmt.Errorf("a message is at most %d characters", wire.MaxChatMessage)
	// ErrEmptyMessage means a user message holds no text.
	ErrEmptyMessage = errors.New("a message needs some text")
	// ErrTurnRunning means the conversation already has a turn running.
	ErrTurnRunning = errors.New("a turn is already running in this conversation")
	// ErrNoTurn means there is no model-loop turn running to steer or cancel.
	ErrNoTurn = errors.New("no model-loop turn is running in this conversation")
)

// DetailServerStopped is the detail of a turn the server's stop ended: the
// error event of a turn running at the stop and the record of one a stopped
// process left running both say it.
const DetailServerStopped = "the server stopped during the turn"

// DetailNoModelRoute is the detail of a turn the missing model route ended. The
// router's own error names a command-line remedy, which belongs to the command
// line; every other front end says where the route is chosen.
var DetailNoModelRoute = llm.NoModel("the Ask Agent")

// DetailOfflineModel is the detail of a turn the offline simulation ended: it
// stands in for evaluations and drafts, and a conversation needs a real model.
const DetailOfflineModel = "the Ask Agent needs a real model: the offline simulation stands in only for evaluations and drafts; choose one under Settings, Models"

// Sink receives the events of a conversation's turns, for its stream.
type Sink interface {
	Publish(sessionID string, ev wire.ChatEvent)
}

// Config wires the service.
type Config struct {
	Sessions   *sessions.Store
	Supervisor *Supervisor
	Sink       Sink
	Log        *slog.Logger
	// Bridge carries out the calls the intent router chooses. Nil sends every
	// message to the model loop.
	Bridge *Bridge
	// Compactor summarises older turns before a model turn starts. Nil means
	// the history is only ever cut to the budget.
	Compactor *Compactor
	// Confirmations expires the cards a stopped server left and sweeps the
	// ones that run out. Nil means no cards.
	Confirmations *Confirmations
	// Tasks carries the delegated tasks on by themselves. Nil means a task
	// never takes a turn of its own.
	Tasks *TaskConfig
}

// Agent is the Ask Agent as the API and the channels use it: it accepts a
// message, runs the turn in the background and keeps the handle that steering
// and cancelling need.
type Agent struct {
	cfg    Config
	ctx    context.Context
	stop   context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.Mutex
	active map[string]*ActiveTurn
	// wake makes the task loop look at the tasks before its next tick.
	wake chan struct{}
}

// New returns a service; Start settles what a stopped process left behind.
func New(cfg Config) *Agent {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	ctx, stop := context.WithCancel(context.Background())
	return &Agent{cfg: cfg, ctx: ctx, stop: stop, active: map[string]*ActiveTurn{}, wake: make(chan struct{}, 1)}
}

// Start ends the turns that were running when the process stopped, as
// runtime_unavailable, and gives their orphaned calls a stand-in result.
func (a *Agent) Start(ctx context.Context) error {
	ids, err := a.cfg.Sessions.EndInterruptedTurns(ctx, "runtime_unavailable", DetailServerStopped)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := RecordStandIns(ctx, a.cfg.Sessions, id); err != nil {
			return err
		}
	}
	if c := a.cfg.Confirmations; c != nil {
		if err := c.Start(ctx); err != nil {
			return err
		}
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			c.sweepLoop(a.ctx)
		}()
	}
	if a.cfg.Tasks != nil {
		a.wg.Add(1)
		go a.taskLoop()
	}
	return nil
}

// Close cancels every running turn and waits for them to end. Each running
// turn publishes its error event (DetailServerStopped) before it ends, so a
// server calls Close before it closes the event streams. A second call returns
// at once.
func (a *Agent) Close() {
	a.stop()
	a.wg.Wait()
}

// SendRequest is one user message.
type SendRequest struct {
	// SessionID continues a conversation; empty starts one on Channel.
	SessionID string
	Channel   Channel
	Text      string
	// Remote marks a message from a remote device: its turn is offered the
	// read and draft tool classes only.
	Remote bool
	// FinishRun names the stopped application run whose "Let the agent
	// finish" button sent the message for the user. The turn's origin is the
	// button, the model loop answers it (the intent router is not asked), and
	// it finishes that run alone.
	FinishRun string
}

// SendResult names the conversation and the turn started for the message.
type SendResult struct {
	SessionID string
	TurnID    string
}

// Send stores the message and starts its turn in the background. The turn's
// output follows through the sink. A message that fails validation creates
// nothing.
func (a *Agent) Send(ctx context.Context, req SendRequest) (SendResult, error) {
	text := strings.TrimSpace(req.Text)
	switch {
	case text == "":
		return SendResult{}, ErrEmptyMessage
	case utf8.RuneCountInString(text) > wire.MaxChatMessage:
		return SendResult{}, ErrMessageTooLong
	}
	sess, err := a.session(ctx, req)
	if err != nil {
		return SendResult{}, err
	}
	turn := &Turn{ID: store.NewID("turn"), SessionID: sess.ID, Channel: Channel(sess.Channel), Origin: OriginModel, UserMessage: text, Remote: req.Remote}
	if req.Channel != "" {
		turn.Channel = req.Channel
	}
	var route Route
	switch {
	case req.FinishRun != "":
		turn.Origin, turn.ButtonLabel, turn.FinishRun = OriginButton, ButtonFinish, req.FinishRun
		turn.LoadSkill(SkillApply)
	case !req.Remote: // the intent router can name any tool; a remote turn has the model loop alone
		if r, ok := a.route(turn.Channel, text); ok {
			route, turn.Origin = r, OriginRouter
		}
	}
	active, err := a.start(ctx, turn, route.RuleID, func() (sessions.Message, error) {
		if err := a.beginOwnTurn(ctx, turn); err != nil {
			a.cfg.Log.Warn("agent: the message was not bound to its conversation's task", "error", err.Error())
		}
		return sessions.Message{Role: sessions.RoleUser, Body: text, OK: true}, nil
	})
	if err != nil {
		return SendResult{}, err
	}
	if sess.Title == "" {
		if _, err := a.cfg.Sessions.Rename(ctx, sess.ID, firstLine(text)); err != nil && !errors.Is(err, store.ErrNotFound) {
			a.cfg.Log.Warn("agent: conversation not titled", "error", err.Error())
		}
	}
	a.launch(turn, text, active, route)
	return SendResult{SessionID: sess.ID, TurnID: turn.ID}, nil
}

func (a *Agent) session(ctx context.Context, req SendRequest) (sessions.Session, error) {
	if req.SessionID != "" {
		return a.cfg.Sessions.Get(ctx, req.SessionID)
	}
	ch := sessions.Channel(req.Channel)
	if ch == "" {
		ch = sessions.Dashboard
	}
	return a.cfg.Sessions.Create(ctx, ch, "")
}

// start makes t its conversation's running turn and records it: its row, with
// the turn's own channel and origin, then its first message. open runs once
// the conversation is held, before anything is recorded, and returns that
// message. A conversation already running a turn answers ErrTurnRunning; a
// turn that fails to start lets the conversation go. Every way a turn begins
// (a message, a task's own turn, a scheduled prompt) starts here, and run
// finishes it.
func (a *Agent) start(ctx context.Context, t *Turn, ruleID string, open func() (sessions.Message, error)) (*ActiveTurn, error) {
	a.mu.Lock()
	if _, busy := a.active[t.SessionID]; busy {
		a.mu.Unlock()
		return nil, ErrTurnRunning
	}
	active := newActiveTurn(t.ID, t.SessionID)
	active.routed = t.Origin == OriginRouter
	a.active[t.SessionID] = active
	a.mu.Unlock()
	if err := a.record(ctx, t, ruleID, open); err != nil {
		a.release(t.SessionID)
		return nil, err
	}
	return active, nil
}

func (a *Agent) record(ctx context.Context, t *Turn, ruleID string, open func() (sessions.Message, error)) error {
	first, err := open()
	if err != nil {
		return err
	}
	if err := a.cfg.Sessions.StartTurn(ctx, sessions.Turn{ID: t.ID, SessionID: t.SessionID, Channel: sessions.Channel(t.Channel), Origin: string(t.Origin), RuleID: ruleID}); err != nil {
		return err
	}
	first.SessionID, first.TurnID = t.SessionID, t.ID
	_, err = a.cfg.Sessions.Append(ctx, first)
	return err
}

// release lets the conversation take its next turn.
func (a *Agent) release(sessionID string) {
	a.mu.Lock()
	delete(a.active, sessionID)
	a.mu.Unlock()
}

// launch runs a started turn in the background.
func (a *Agent) launch(t *Turn, prompt string, active *ActiveTurn, route Route) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.run(a.ctx, t, prompt, active, route)
	}()
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return clipRunes(oneLine(line), sessions.MaxTitle)
}

// route asks the intent router about a message and reports whether its answer
// can be carried out in this build.
func (a *Agent) route(ch Channel, text string) (Route, bool) {
	if a.cfg.Bridge == nil {
		return Route{}, false
	}
	r, _ := RouteMessage(text)
	if r.Tool == "" || !a.cfg.Bridge.Routable(ch, r) {
		return Route{}, false
	}
	return r, true
}

// run carries out a started turn until ctx ends, then finishes it: its record,
// the conversation's running turn and its task's state.
func (a *Agent) run(ctx context.Context, t *Turn, prompt string, active *ActiveTurn, route Route) TurnResult {
	emit := func(ev wire.ChatEvent) { a.cfg.Sink.Publish(t.SessionID, ev) }
	var res TurnResult
	if t.Origin == OriginRouter {
		res = a.runRouted(ctx, t, route, active, emit)
	} else {
		in := TurnInput{Turn: t, Prompt: prompt + languageNote(t.UserMessage), Emit: emit, Active: active}
		if c := a.cfg.Compactor; c != nil {
			in.Before = func(ctx context.Context, emit func(wire.ChatEvent)) { c.Run(ctx, t, emit) }
		}
		res = a.cfg.Supervisor.RunTurn(ctx, in)
	}
	if err := a.cfg.Sessions.FinishTurn(context.WithoutCancel(ctx), t.ID, res.Status, res.Reason, res.Detail); err != nil {
		a.cfg.Log.Error("agent: turn not finished in the record", "turn", t.ID, "error", err.Error())
	}
	a.release(t.SessionID)
	a.afterTurn(t, res)
	return res
}

// runRouted carries out a routed call; a cancel request ends it as cancelled.
func (a *Agent) runRouted(parent context.Context, t *Turn, route Route, active *ActiveTurn, emit func(wire.ChatEvent)) TurnResult {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	go func() {
		select {
		case <-active.cancelCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	return a.cfg.Bridge.RunRouted(ctx, t, route, emit)
}

// ActiveTurn returns the id of the conversation's running turn, if any.
func (a *Agent) ActiveTurn(sessionID string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t, ok := a.active[sessionID]; ok {
		return t.ID, true
	}
	return "", false
}

// Steer queues a steering message for the conversation's running turn.
func (a *Agent) Steer(sessionID, text string) (turnID string, queued int, err error) {
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return "", 0, ErrEmptyMessage
	case utf8.RuneCountInString(text) > wire.MaxChatMessage:
		return "", 0, ErrMessageTooLong
	}
	a.mu.Lock()
	t := a.active[sessionID]
	a.mu.Unlock()
	if t == nil || t.routed {
		return "", 0, ErrNoTurn
	}
	n, err := t.TrySteer(text)
	return t.ID, n, err
}

// CancelResult says what a cancel did.
type CancelResult struct {
	TurnID string
	Runs   []string
}

// Cancel stops the conversation's running turn. Background runs it started
// continue and are named in the result.
func (a *Agent) Cancel(sessionID string) (CancelResult, error) {
	a.mu.Lock()
	t := a.active[sessionID]
	a.mu.Unlock()
	if t == nil {
		return CancelResult{}, ErrNoTurn
	}
	t.Cancel()
	return CancelResult{TurnID: t.ID, Runs: t.Runs()}, nil
}
