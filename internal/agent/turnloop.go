package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"applysmith/internal/agent/pirpc"
	"applysmith/internal/agent/sessions"
	"applysmith/internal/api/wire"
	"applysmith/internal/llm"
)

// Time bounds of a turn.
const (
	// TurnLimit is how long a turn may run, from the moment it was accepted,
	// and RunTurnLimit how long a turn bound to one run may (Turn.FinishRun).
	TurnLimit    = 10 * time.Minute
	RunTurnLimit = 30 * time.Minute
	// StallLimit is how long pi may stay silent, with no forwarded call in
	// flight, before the turn is ended.
	StallLimit = 120 * time.Second
	// AbortWait is how long an abort may go unanswered before the process is
	// killed; with the cancel sequence it keeps cancellation inside 2 seconds.
	AbortWait = 1500 * time.Millisecond
	// startupWait bounds the handshake with a new process.
	startupWait = 30 * time.Second
	helloWait   = 15 * time.Second
	exitWait    = time.Second
)

// TurnInput is one turn to run.
type TurnInput struct {
	Turn   *Turn
	Prompt string
	// Emit publishes a stream event. The supervisor stamps the turn id.
	Emit   func(wire.ChatEvent)
	Active *ActiveTurn
	// Before runs once the turn has started and pi is known to be usable, and
	// before a process is launched, for work that belongs to the turn but needs
	// no process, such as compaction. Its context ends when the turn is
	// cancelled.
	Before func(ctx context.Context, emit func(wire.ChatEvent))
}

// TurnResult is how a turn ended.
type TurnResult struct {
	Status string // sessions.TurnFinal or sessions.TurnError
	Reason string
	Detail string
	Runs   []string
}

// RunTurn runs one model-loop turn to its terminal event: it ends the turn as
// no_model_route when the agent has no model it can call, answers alone when
// the day's cap is reached, and otherwise starts a pi process on the row the
// message's tier picks (tier.go), drives it, and ends it. It blocks until the turn is over.
func (s *Supervisor) RunTurn(ctx context.Context, in TurnInput) TurnResult {
	l := &loop{s: s, in: in, t: in.Turn, started: time.Now()}
	l.limit = time.NewTimer(in.Turn.timeLimit())
	defer l.limit.Stop()
	l.emit(wire.ChatEvent{Type: wire.ChatTurnStart})
	res := l.run(ctx)
	res.Runs = in.Active.Runs()
	return res
}

// timeLimit is how long the turn may run.
func (t *Turn) timeLimit() time.Duration {
	if t.FinishRun != "" {
		return RunTurnLimit
	}
	return TurnLimit
}

// cancellable returns a context that also ends when the turn is cancelled.
func (l *loop) cancellable(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-l.in.Active.cancelCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// before runs the turn's preparation, giving up when the turn is cancelled.
func (l *loop) before(ctx context.Context) {
	ctx, cancel := l.cancellable(ctx)
	defer cancel()
	l.in.Before(ctx, l.emit)
}

type loop struct {
	s       *Supervisor
	in      TurnInput
	t       *Turn
	started time.Time
	limit   *time.Timer

	route llm.AgentRoute
	pt    pirpc.Turn
	r     *run
	proc  Process
	recs  chan pirpc.Record
	done  chan struct{} // ends the reader goroutine
	wmu   sync.Mutex
	seq   int
	stall *time.Timer

	backlog  []pirpc.Record
	promptID string

	settled    bool
	closed     bool // pi's stdout ended
	needAbort  bool
	killed     bool
	res        TurnResult
	terminal   bool
	msgStart   time.Time
	firstTok   time.Time
	lastText   string
	callsOpen  map[string]bool
	results    []string // one line per successful tool result, for an empty final answer
	promptSize int
	tg         tokenGate
}

func (l *loop) emit(ev wire.ChatEvent) {
	ev.TurnID = l.t.ID
	l.in.Emit(ev)
}

func (l *loop) store() *sessions.Store { return l.s.cfg.Sessions }

func (l *loop) note(err error, what string) {
	if err != nil {
		l.s.cfg.Log.Error("agent: "+what, "turn", l.t.ID, "error", err.Error())
	}
}

// reply ends the turn with text produced without a model: the answer to a
// reached cap or to a route that could not be read.
func (l *loop) reply(text string) TurnResult {
	env, _ := l.s.cfg.Bridge.Disclose(context.Background(), l.t, Envelope{Text: text})
	_, err := l.store().Append(context.Background(), sessions.Message{SessionID: l.t.SessionID, TurnID: l.t.ID, Role: sessions.RoleAssistant, Body: env.Text, OK: true})
	l.note(err, "reply not stored")
	l.emit(wire.ChatEvent{Type: wire.ChatToken, Text: env.Text})
	l.emit(finalEvent(l.t.ID, env))
	return TurnResult{Status: sessions.TurnFinal}
}

// fail ends the turn with an error event before a process exists.
func (l *loop) fail(reason, detail string) TurnResult {
	l.emit(wire.ChatEvent{Type: wire.ChatError, Reason: reason, Detail: detail})
	return TurnResult{Status: sessions.TurnError, Reason: reason, Detail: detail}
}

func (l *loop) run(ctx context.Context) TurnResult {
	t, s, a := l.t, l.s, l.in.Active
	l.note(func() error { _, err := RecordStandIns(ctx, l.store(), t.SessionID); return err }(), "stand-in results not recorded")

	route, err := l.chooseRoute(ctx)
	if err != nil && ctx.Err() != nil {
		return l.fail(pirpc.ReasonUnavailable, DetailServerStopped)
	}
	if errors.Is(err, llm.ErrAgentOffline) {
		return l.fail(wire.ReasonNoModelRoute, DetailOfflineModel)
	}
	if err != nil {
		var le *llm.Error
		if errors.As(err, &le) && (le.Kind == llm.KindNoRoute || le.Kind == llm.KindUnconfigured) {
			s.cfg.Log.Info("agent: no usable model route", "turn", t.ID, "reason", clipRunes(err.Error(), 300))
			return l.fail(wire.ReasonNoModelRoute, DetailNoModelRoute)
		}
		return l.reply("The agent loop is not ready: " + clipRunes(err.Error(), 300))
	}
	// Over the cap the gateway serves each call from a cheaper model; the turn
	// is refused only when there is none.
	if route.PiLogin == "" && s.cfg.Router.AgentCapBlocked(ctx, t.modelTask()) {
		spend, capUSD := s.cfg.Router.DailySpend(ctx)
		return l.reply(fmt.Sprintf("The daily model spend cap of USD %.2f is reached (spend USD %.2f) and no cheaper model is routed for the agent, so the agent loop is paused until tomorrow or until you raise the cap in the model settings.", capUSD, spend))
	}
	l.route = route
	t.TakesImages = llm.TakesImages(route.Model)
	l.pt = pirpc.Turn{Phrase: l.phrase}

	if err := s.ready(ctx); err != nil {
		return l.fail(pirpc.ReasonUnavailable, clipRunes(err.Error(), 300))
	}
	if route.PiLogin != "" {
		if err := s.CheckLogin(ctx, route.PiLogin); err != nil {
			return l.fail(pirpc.ReasonUnavailable, clipRunes(err.Error(), 300))
		}
	}
	if l.in.Before != nil {
		l.before(ctx)
	}
	background := s.holdsBackground(t)
	if err := s.acquire(ctx, a.cancelCh, background); err != nil {
		if errors.Is(err, errCancelled) {
			return l.fail(pirpc.ReasonCancelled, "")
		}
		return l.fail(pirpc.ReasonUnavailable, DetailServerStopped)
	}
	defer s.release(background)

	token, r, err := s.cfg.Bridge.open(ctx, t, l.emit)
	if err != nil {
		return l.fail(pirpc.ReasonUnavailable, "no credential for the process")
	}
	l.r = r
	a.run.Store(r)
	defer s.cfg.Bridge.close(token, r)

	spec, err := s.prepare(route, token)
	if err != nil {
		s.failed()
		return l.fail(pirpc.ReasonUnavailable, clipRunes(err.Error(), 300))
	}
	proc, err := s.cfg.Launcher.Start(spec, &stderrLog{log: s.cfg.Log, redact: s.cfg.Redact})
	if err != nil {
		s.failed()
		return l.fail(pirpc.ReasonUnavailable, "pi did not start: "+clipRunes(err.Error(), 200))
	}
	l.proc = proc
	l.recs, l.done = make(chan pirpc.Record, 64), make(chan struct{})
	l.callsOpen = map[string]bool{}
	go l.read()
	defer l.cleanup(ctx)

	l.stall = time.NewTimer(StallLimit)
	defer l.stall.Stop()

	if res, ok := l.handshake(ctx); !ok {
		return res
	}
	l.promptSize = len(l.in.Prompt)
	l.promptID = l.send("prompt", map[string]any{"message": l.in.Prompt})
	return l.drive(ctx)
}

// read hands pi's records to the loop and closes the channel when stdout ends.
func (l *loop) read() {
	defer close(l.recs)
	err := pirpc.ReadRecords(l.proc.Stdout(), func(rec pirpc.Record) {
		select {
		case l.recs <- rec:
		case <-l.done:
		}
	})
	if err != nil && !errors.Is(err, io.ErrClosedPipe) {
		l.s.cfg.Log.Warn("agent: pi output ended with an error", "turn", l.t.ID, "error", cutRunes(err.Error(), 200))
	}
}

func (l *loop) send(cmd string, fields map[string]any) string {
	l.seq++
	id := fmt.Sprintf("c%d", l.seq)
	m := map[string]any{"id": id, "type": cmd}
	for k, v := range fields {
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		return id
	}
	l.wmu.Lock()
	_, err = l.proc.Write(append(b, '\n'))
	l.wmu.Unlock()
	if err != nil {
		l.s.cfg.Log.Warn("agent: command not delivered", "turn", l.t.ID, "command", cmd, "error", err.Error())
	}
	return id
}

var errClosed = errors.New("pi's output ended")

// await reads records until the response to a command arrives; records that
// come first wait in the backlog for the main loop.
func (l *loop) await(ctx context.Context, id string, d time.Duration) (pirpc.Record, error) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case rec, ok := <-l.recs:
			if !ok {
				return pirpc.Record{}, errClosed
			}
			if rec.Type == pirpc.TypeResponse && rec.ID == id {
				if !rec.Success {
					return rec, fmt.Errorf("pi refused the command: %s", cutRunes(rec.Error, 200))
				}
				return rec, nil
			}
			l.backlog = append(l.backlog, rec)
		case <-timer.C:
			return pirpc.Record{}, errors.New("pi did not answer in time")
		case <-l.in.Active.cancelCh:
			return pirpc.Record{}, errCancelled
		case <-ctx.Done():
			return pirpc.Record{}, ctx.Err()
		}
	}
}

// handshake brings a new process to a state where the prompt can be sent:
// ready, compaction off, thinking level set, fresh session, and the extension
// has reported exactly the catalog.
func (l *loop) handshake(ctx context.Context) (TurnResult, bool) {
	steps := []struct {
		cmd    string
		fields map[string]any
	}{
		{"get_state", nil},
		{"set_auto_compaction", map[string]any{"enabled": false}},
	}
	if l.route.Thinking != "" {
		steps = append(steps, struct {
			cmd    string
			fields map[string]any
		}{"set_thinking_level", map[string]any{"level": l.route.Thinking}})
	}
	// Sent for every start, so no process depends on settings.json (which all
	// processes share): the gateway walks the candidates of a wire, so a retry
	// by pi would walk them again; a row that spans wires needs the retry,
	// which its virtual model sends to the next wire; a pi login is served by
	// pi alone and keeps its retry.
	steps = append(steps, struct {
		cmd    string
		fields map[string]any
	}{"set_auto_retry", map[string]any{"enabled": l.route.PiLogin != "" || spansWires(l.route)}})
	steps = append(steps, struct {
		cmd    string
		fields map[string]any
	}{"new_session", nil})
	for _, st := range steps {
		if _, err := l.await(ctx, l.send(st.cmd, st.fields), startupWait); err != nil {
			return l.startFailure(err), false
		}
	}
	timer := time.NewTimer(helloWait)
	defer timer.Stop()
	select {
	case <-l.r.helloCh:
		return TurnResult{}, true
	case <-l.r.helloBadC:
		bad, _ := l.r.helloBad.Load().(string)
		l.s.failed()
		return l.fail(pirpc.ReasonUnavailable, "the tools pi reported are not the catalog: "+bad), false
	case <-timer.C:
		return l.startFailure(errors.New("the extension did not report its tools")), false
	case <-l.in.Active.cancelCh:
		return l.fail(pirpc.ReasonCancelled, ""), false
	case <-ctx.Done():
		return l.fail(pirpc.ReasonUnavailable, DetailServerStopped), false
	}
}

func (l *loop) startFailure(err error) TurnResult {
	switch {
	case errors.Is(err, errCancelled):
		return l.fail(pirpc.ReasonCancelled, "")
	case errors.Is(err, context.Canceled):
		return l.fail(pirpc.ReasonUnavailable, DetailServerStopped)
	}
	l.s.failed()
	return l.fail(pirpc.ReasonUnavailable, "pi did not become ready: "+clipRunes(err.Error(), 200))
}

// drive is the turn proper: it feeds pi's records to the mapping and watches the
// clocks, the cancel request and the steering queue until the turn is over.
func (l *loop) drive(ctx context.Context) TurnResult {
	for _, rec := range l.backlog {
		l.handle(rec)
	}
	l.backlog = nil
	for !l.over() {
		select {
		case rec, ok := <-l.recs:
			if !ok {
				l.closed = true
				l.apply(l.pt.StopWith(pirpc.ReasonUnavailable, "pi exited before the turn finished"))
				if !l.settled {
					l.s.failed()
				}
				continue
			}
			l.resetStall()
			l.handle(rec)
		case <-l.stall.C:
			if l.r.inflight.Load() > 0 {
				l.stall.Reset(StallLimit)
				continue
			}
			l.apply(l.pt.StopWith(pirpc.ReasonUnavailable, "stalled"))
			l.s.failed()
			l.kill()
		case text := <-l.in.Active.steerCh:
			if l.pt.Finished() {
				l.in.Active.delivered() // the turn is over; the message will not be delivered
				continue
			}
			l.send("steer", map[string]any{"message": text})
		case <-l.limit.C:
			l.stop(pirpc.ReasonTimeLimit, "")
		case <-l.in.Active.cancelCh:
			l.stop(pirpc.ReasonCancelled, "")
		case <-ctx.Done():
			l.stop(pirpc.ReasonUnavailable, DetailServerStopped)
		}
	}
	if l.needAbort && !l.killed && !l.closed {
		l.abortSequence()
	}
	if !l.terminal {
		l.apply(l.pt.StopWith(pirpc.ReasonUnavailable, "the turn ended without a result"))
	}
	if l.res.Status == sessions.TurnFinal && l.settled {
		l.s.healthy()
	}
	return l.res
}

func (l *loop) over() bool {
	if l.killed || l.closed {
		return true
	}
	if !l.pt.Finished() {
		return false
	}
	return l.settled || l.needAbort
}

func (l *loop) resetStall() {
	if !l.stall.Stop() {
		select {
		case <-l.stall.C:
		default:
		}
	}
	l.stall.Reset(StallLimit)
}

// stop ends the turn with an error the system chose and arranges the abort.
// The terminal event is published before anything is sent to pi, so the user
// sees the turn end at once; the calls running for the turn are cancelled in
// the same breath.
func (l *loop) stop(reason, detail string) {
	l.apply(l.pt.StopWith(reason, detail))
	l.needAbort = true
	l.r.cancelCalls()
}

// abortSequence asks pi to drop its queue and abort, and reads what it still
// writes (the last response's usage) until the abort is answered. An abort
// that is not answered in time kills the process.
func (l *loop) abortSequence() {
	l.send("clear_queue", nil)
	id := l.send("abort", nil)
	timer := time.NewTimer(AbortWait)
	defer timer.Stop()
	for {
		select {
		case rec, ok := <-l.recs:
			if !ok {
				l.closed = true
				return
			}
			if rec.Type == pirpc.TypeResponse && rec.ID == id {
				return
			}
			l.handle(rec)
		case <-timer.C:
			l.kill()
			return
		}
	}
}

func (l *loop) kill() {
	if !l.killed {
		l.killed = true
		l.proc.Kill()
	}
}

// cleanup ends the process, gives calls that never got a result a system-made
// one, and frees the reader.
func (l *loop) cleanup(ctx context.Context) {
	if !l.killed && !l.closed {
		l.proc.CloseInput()
		select {
		case <-l.proc.Done():
		case <-time.After(exitWait):
			l.kill()
		}
	}
	close(l.done)
	text := StandInText
	if l.res.Reason == pirpc.ReasonCancelled || l.res.Reason == pirpc.ReasonTimeLimit || l.res.Reason == pirpc.ReasonCostCap || l.res.Reason == pirpc.ReasonStuck {
		text = "The turn ended before this call finished. The call may or may not have run. Check the runs board for its state."
	}
	if len(l.callsOpen) > 0 {
		_, err := l.store().RecordStandIns(context.WithoutCancel(ctx), l.t.SessionID, text)
		l.note(err, "stand-in results not recorded")
	}
}

// stderrLog carries pi's diagnostics to the log with secrets scrubbed.
type stderrLog struct {
	log    interface{ Warn(string, ...any) }
	redact func(string) string
	mu     sync.Mutex
	buf    []byte
}

func (w *stderrLog) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := strings.IndexByte(string(w.buf), '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSpace(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
		if line != "" {
			w.log.Warn("agent: pi", "stderr", cutRunes(w.redact(line), 300))
		}
	}
	if len(w.buf) > 4096 {
		w.buf = nil
	}
	return len(p), nil
}
