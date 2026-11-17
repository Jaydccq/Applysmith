// Clean-room: docs/provenance.md#CR-AGENT-TRIGGERS

package triggers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Event is a Gmail signal event as the triggers read it: identifiers and
// classification only, no text.
type Event struct {
	Seq        int64
	MessageID  string
	SignalType string
	Tier       string
	Direction  string
	Employer   string
	Received   time.Time
}

// Mail is the stored text of a message.
type Mail struct {
	From, Subject, Body string
}

// Source is the Gmail capability as the triggers read it.
type Source interface {
	SignalEvents(ctx context.Context, afterSeq int64, limit int) ([]Event, error)
	Mail(ctx context.Context, messageID string) (Mail, error)
}

// Request is one scheduled turn to run.
type Request struct {
	Title  string
	Prompt string
	// Untrusted starts the turn tainted; Withhold names tools it is not offered.
	Untrusted bool
	Withhold  []string
}

// Answer is how a scheduled turn ended: Status is answered, failed or timed_out.
type Answer struct {
	SessionID string
	Status    string
	Detail    string
	Text      string
}

// Runner runs a scheduled turn in a new conversation and returns when it is
// over. The Ask Agent is the one implementation.
type Runner interface {
	RunScheduled(ctx context.Context, req Request) (Answer, error)
}

// Leaser takes the model lease a firing holds for its model calls. ok is
// false while another job holds it.
type Leaser interface {
	Acquire(ctx context.Context) (release func(), ok bool, err error)
}

// Announcement is the notification of a result.
type Announcement struct {
	Key, Title, Body, Link string
	// Trigger tells a trigger result from a cadence one, which is never held
	// in quiet hours because the user chose its time.
	Trigger bool
}

// StatusProposal is a proposal to change where an application stands. It is
// shown in the needs-you inbox and does nothing until the user acts.
type StatusProposal struct {
	Key, Title, Detail, Link string
}

// Notifier delivers results through the notification policy and the board.
type Notifier interface {
	Announce(ctx context.Context, a Announcement) error
	Propose(ctx context.Context, p StatusProposal) error
}

// Config wires an engine.
type Config struct {
	Store    *Store
	Source   Source
	Runner   Runner
	Notifier Notifier
	// Leaser is the llm:evaluation lease; nil takes none.
	Leaser Leaser
	Now    func() time.Time
	Log    *slog.Logger
}

// withheld lists the tools a trigger firing is not offered on top of the
// scheduled envelope: read_page opens a link, and a link in the mail is not
// ours to follow.
var withheld = []string{"read_page"}

// Engine fires the scheduled prompts. One scheduled turn runs at a time.
type Engine struct {
	cfg    Config
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.Mutex
	busy   bool
}

// NewEngine returns an engine; Start settles what a stopped server left.
func NewEngine(cfg Config) *Engine {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{cfg: cfg, ctx: ctx, cancel: cancel}
}

// Start ends the firings that were claimed when the process stopped. They are
// not run again.
func (e *Engine) Start(ctx context.Context) error {
	_, err := e.cfg.Store.SettleInterrupted(ctx)
	return err
}

// Close stops the running turn and waits for the worker.
func (e *Engine) Close() {
	e.cancel()
	e.wg.Wait()
}

// Wait blocks until the running batch, if any, is over.
func (e *Engine) Wait() { e.wg.Wait() }

type item struct {
	prompt Prompt
	event  *Event // nil for a cadence slot
}

func (e *Engine) enter() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.busy {
		return false
	}
	e.busy = true
	return true
}

func (e *Engine) leave() {
	e.mu.Lock()
	e.busy = false
	e.mu.Unlock()
}

// TickPrompts claims the cadence prompts due at now and starts them. While a
// batch runs it does nothing, so a due slot waits inside its window.
func (e *Engine) TickPrompts(ctx context.Context, now time.Time) error {
	if !e.enter() {
		return nil
	}
	due, err := e.cfg.Store.DueCadence(ctx, now)
	if err != nil || len(due) == 0 {
		e.leave()
		return err
	}
	items := make([]item, len(due))
	for i, p := range due {
		items[i] = item{prompt: p}
	}
	e.start(items, func() {})
	return nil
}

// TickTriggers reads the signal events recorded since the high-water mark and
// fires the matching triggers: at most PerTick per tick, none for a message
// older than 24 hours or received before the trigger's activation, and each
// (trigger, message) pair at most once. The mark moves up to the last event
// whose pairs are all claimed or skipped; events past a full tick wait for the
// next one.
func (e *Engine) TickTriggers(ctx context.Context, now time.Time) error {
	if !e.enter() {
		return nil
	}
	items, advance, err := e.collect(ctx, now)
	if err != nil || len(items) == 0 {
		if err == nil {
			err = e.cfg.Store.SetHighWater(ctx, advance)
		}
		e.leave()
		return err
	}
	release := func() {}
	if l := e.cfg.Leaser; l != nil {
		rel, ok, err := l.Acquire(ctx)
		if err != nil || !ok {
			e.leave()
			return err
		}
		release = rel
	}
	var claimed []item
	for _, it := range items {
		made, err := e.cfg.Store.Claim(ctx, it.prompt, *it.event)
		if err != nil {
			release()
			e.leave()
			return err
		}
		if made {
			claimed = append(claimed, it)
		}
	}
	if err := e.cfg.Store.SetHighWater(ctx, advance); err != nil {
		e.cfg.Log.Error("triggers: high-water mark not stored", "error", err.Error())
	}
	if len(claimed) == 0 {
		release()
		e.leave()
		return nil
	}
	e.start(claimed, release)
	return nil
}

// collect lists the firings to claim and the sequence the mark may move to.
func (e *Engine) collect(ctx context.Context, now time.Time) ([]item, int64, error) {
	st := e.cfg.Store
	trs, err := st.ActiveTriggers(ctx)
	if err != nil {
		return nil, 0, err
	}
	mark, err := st.HighWater(ctx)
	if err != nil {
		return nil, 0, err
	}
	advance := mark
	var items []item
	for {
		evs, err := e.cfg.Source.SignalEvents(ctx, advance, 100)
		if err != nil {
			return nil, mark, err
		}
		for _, ev := range evs {
			full := false
			for _, tr := range trs {
				if !tr.Matches(ev) {
					continue
				}
				if _, seen, err := st.FiringOf(ctx, tr.ID, ev.MessageID); err != nil {
					return nil, mark, err
				} else if seen {
					continue
				}
				if now.Sub(ev.Received) > MailWindow {
					if _, err := st.Skip(ctx, tr, ev, "older than 24 hours when first seen"); err != nil {
						return nil, mark, err
					}
					continue
				}
				if len(items) >= PerTick {
					full = true
					break
				}
				ev := ev
				items = append(items, item{prompt: tr, event: &ev})
			}
			if full {
				return items, advance, nil
			}
			advance = ev.Seq
		}
		if len(evs) < 100 {
			return items, advance, nil
		}
	}
}

// start runs the items one after another in the background; the lease, if
// any, is held for the whole batch.
func (e *Engine) start(items []item, release func()) {
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer e.leave()
		defer release()
		for _, it := range items {
			e.run(it)
		}
	}()
}

func (e *Engine) run(it item) {
	ctx := context.WithoutCancel(e.ctx)
	res, announce := e.execute(it)
	msg := ""
	if it.event != nil {
		msg = it.event.MessageID
	}
	if err := e.cfg.Store.Finish(ctx, it.prompt.ID, msg, res); err != nil {
		e.cfg.Log.Error("triggers: result not stored", "prompt", it.prompt.ID, "error", err.Error())
	}
	if e.cfg.Notifier == nil {
		return
	}
	if err := e.cfg.Notifier.Announce(ctx, announce(res)); err != nil {
		e.cfg.Log.Error("triggers: result not announced", "prompt", it.prompt.ID, "error", err.Error())
	}
	if res.ProposedStatus != "" {
		p := StatusProposal{Key: "trigger-status|" + it.prompt.ID + "|" + it.event.MessageID,
			Title:  cutRunes(fmt.Sprintf("Proposed status: %s%s", res.ProposedStatus, forEmployer(it.event.Employer)), 120),
			Detail: res.ProposedReason, Link: "/ask?conversation=" + res.SessionID}
		if err := e.cfg.Notifier.Propose(ctx, p); err != nil {
			e.cfg.Log.Error("triggers: status proposal not raised", "prompt", it.prompt.ID, "error", err.Error())
		}
	}
}

func forEmployer(employer string) string {
	if employer == "" {
		return ""
	}
	return " for " + oneLine(employer)
}

// execute runs the turn and reads its answer.
func (e *Engine) execute(it item) (Result, func(Result) Announcement) {
	req := Request{Title: cutRunes(oneLine(it.prompt.Text), 60), Prompt: it.prompt.Text}
	key, trigger := "prompt|"+it.prompt.ID+"|"+stamp(it.prompt.LastSlot), false
	if it.event != nil {
		trigger, key = true, "trigger|"+it.prompt.ID+"|"+it.event.MessageID
		mail, err := e.cfg.Source.Mail(e.ctx, it.event.MessageID)
		if err != nil {
			return Result{State: FiringFailed, Detail: "the message text is not available: " + cutRunes(err.Error(), 200)}, e.announcer(it, key, trigger)
		}
		req.Prompt = BuildPrompt(it.prompt.Text, *it.event, mail)
		req.Title, req.Untrusted, req.Withhold = "Trigger: "+req.Title, true, withheld
	}
	ans, err := e.cfg.Runner.RunScheduled(e.ctx, req)
	res := Result{State: ans.Status, Detail: ans.Detail, SessionID: ans.SessionID}
	switch {
	case err != nil && !errors.Is(err, context.Canceled):
		res.State, res.Detail = FiringFailed, cutRunes(err.Error(), 200)
	case err != nil:
		res.State, res.Detail = FiringFailed, "the server stopped during the firing"
	case ans.Status == FiringAnswered:
		o := ReadAnswer(ans.Text)
		res.Draft = o.Draft
		if it.event != nil {
			res.ProposedStatus, res.ProposedReason = o.ProposedStatus, o.ProposedReason
		}
	}
	if res.State == "" {
		res.State = FiringFailed
	}
	return res, e.announcer(it, key, trigger)
}

func (e *Engine) announcer(it item, key string, trigger bool) func(Result) Announcement {
	return func(r Result) Announcement {
		title := "Scheduled prompt: " + cutRunes(oneLine(it.prompt.Text), 60)
		if trigger {
			title = "Trigger fired" + forEmployer(it.event.Employer) + ": " + cutRunes(oneLine(it.prompt.Text), 60)
		}
		body := summary(r)
		switch r.State {
		case FiringFailed:
			body = "The run failed. " + body
		case FiringTimedOut:
			body = "The run timed out."
		}
		link := "/ask"
		if r.SessionID != "" {
			link = "/ask?conversation=" + r.SessionID
		}
		return Announcement{Key: key, Title: cutRunes(title, 120), Body: body, Link: link, Trigger: trigger}
	}
}
