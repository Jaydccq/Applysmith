package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/notify"
	"applysmith/internal/vault"
)

// Timing of the poller and the conversation.
const (
	PollPause        = time.Second
	ConflictBase     = 5 * time.Second
	ConflictCap      = 60 * time.Second
	FailureCap       = 60 * time.Second
	StaleAfter       = LongPollSeconds*time.Second + PollPause + 5*time.Second
	IdleCheck        = 15 * time.Second
	InboundPerMinute = 20
	TypingEvery      = 4 * time.Second
	InterimAfter     = 60 * time.Second
	ConversationIdle = 6 * time.Hour
	AnswerPoll       = 250 * time.Millisecond
	// AnswerPollSlowest is how many times AnswerPoll the pause between two
	// reads of a running turn grows to.
	AnswerPollSlowest = 8
)

// Errors of the Backend's conversation with the Ask Agent.
var (
	// ErrAgentBusy means the conversation's turn is still running.
	ErrAgentBusy = errors.New("telegram: the agent is still working on the previous message")
	// ErrMessageTooLong means the agent refuses the message for its length.
	ErrMessageTooLong = errors.New("telegram: the message is too long for the agent")
	// ErrNoTurn means the conversation has no turn running.
	ErrNoTurn = errors.New("telegram: no turn is running")
)

// Views build the replies of the read-only commands from the same read models
// the dashboard uses. Each returns the text of one message.
type Views interface {
	Status(ctx context.Context) (string, error)
	Tracker(ctx context.Context) (string, error)
	Pipeline(ctx context.Context) (string, error)
	Queue(ctx context.Context) (string, error)
	Gmail(ctx context.Context) (string, error)
	Inbox(ctx context.Context) (string, error)
	// Today is the day summary's text for now: what was done for the user
	// today and what is left for them.
	Today(ctx context.Context) (string, error)
	Open(ctx context.Context, screen string) (string, error)
	Trace(ctx context.Context, sessionID string) (string, error)
	// ScanSources lists up to eight enabled discovery sources, the most
	// recently run first.
	ScanSources(ctx context.Context) ([]string, error)
}

// Started names a background run that was just started.
type Started struct {
	RunID string
	Link  string
}

// AskRequest is one message to the Ask Agent on the telegram channel.
type AskRequest struct {
	SessionID string // empty starts a conversation
	Text      string
}

// Asked names the conversation and the turn a message started.
type Asked struct {
	SessionID string
	TurnID    string
}

// Answer is what a turn came to: Done once it ended, with the text to send
// (its answer, or why it has none).
type Answer struct {
	Done bool
	Text string
}

// Backend is everything the gateway needs from the rest of the product. A
// method whose capability does not exist yet returns errors.ErrUnsupported and
// the gateway tells the owner so.
type Backend interface {
	Views
	// Ask stores the message and starts its turn on the telegram channel, and
	// returns at once. ErrAgentBusy: the conversation's turn still runs;
	// ErrMessageTooLong: the message was refused.
	Ask(ctx context.Context, req AskRequest) (Asked, error)
	// Answer reads what a turn came to; Done is false while it runs.
	Answer(ctx context.Context, sessionID, turnID string) (Answer, error)
	// Stop cancels the conversation's running turn; ErrNoTurn when none runs.
	Stop(ctx context.Context, sessionID string) error
	StartScan(ctx context.Context, source string) (Started, error)
	StartEvaluation(ctx context.Context, postingURL string) (Started, error)
	Ack(ctx context.Context, entryID string) error
	// Approve approves (or declines) the held action of a card for once and
	// returns what happened, in a sentence.
	Approve(ctx context.Context, id string, approve bool) (string, error)
	Rate(ctx context.Context, item, verdict string) error
	// Mail does a mail action (MailActions) on a Gmail thread and says what
	// happened in a sentence; for MailDraft it returns the draft's message.
	// Its error's text is shown to the owner.
	Mail(ctx context.Context, action, thread string) (string, error)
}

// Config wires a gateway.
type Config struct {
	State *State
	// Token reads the bot token from the keychain (or its environment
	// override). It fails when none is set.
	Token   func() (string, error)
	API     string
	HTTP    *http.Client
	Backend Backend
	// Digest builds the full pages (and the rating keyboard) of an
	// announcement longer than a message, a Gmail digest or the day summary,
	// from its link. Without it one is sent as its headline.
	Digest func(ctx context.Context, link string) ([]string, *Markup, error)
	// Mail names the Gmail thread of an alert or deadline reminder, from the
	// announcement's kind and link, and the mail actions its buttons offer
	// now. Without it they carry none.
	Mail func(ctx context.Context, kind, link string) (thread string, actions []string)
	// Conversation names the Ask Agent conversation an announcement belongs
	// to (a delegated task's, or the one whose agent started the run), so a
	// reply to it continues there. Without it, or "", a reply goes to the
	// chat's own conversation.
	Conversation func(ctx context.Context, a notify.Announcement) string
	// LinkBase is the dashboard's public address while remote access is on;
	// an announcement then carries the link to its page.
	LinkBase func() (string, bool)
	Clock    Clock
	Log      *slog.Logger
	// Instance names this process in the poller lease.
	Instance string
	// OnPaired is called once a chat has been paired.
	OnPaired func()

	// Timings that tests shorten.
	Pause        time.Duration
	TypingEvery  time.Duration
	InterimAfter time.Duration
	AnswerPoll   time.Duration
	Jitter       func() float64
}

// Gateway is the Telegram bot: one poller, one owner.
type Gateway struct {
	cfg    Config
	client *Client
	sender *Sender
	state  *State
	reconf chan struct{}

	mu         sync.Mutex
	running    bool
	everOK     bool
	lastOK     time.Time
	conflicted bool
	refused    bool // Telegram refuses the saved token: it was revoked
	inbound    []time.Time
	slowAt     time.Time
	// following holds the turns whose answer a follower is waiting for.
	following map[string]bool
	// now times the inbound limit and the health; the sender keeps its own
	// clock, whose sleeps advance a fake one in tests.
	now func() time.Time

	// life bounds the followers; Close ends it and waits for them.
	life context.Context
	halt context.CancelFunc
	wg   sync.WaitGroup
}

// New returns a gateway; Run starts it.
func New(cfg Config) *Gateway {
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Pause == 0 {
		cfg.Pause = PollPause
	}
	if cfg.TypingEvery == 0 {
		cfg.TypingEvery = TypingEvery
	}
	if cfg.InterimAfter == 0 {
		cfg.InterimAfter = InterimAfter
	}
	if cfg.AnswerPoll == 0 {
		cfg.AnswerPoll = AnswerPoll
	}
	if cfg.Jitter == nil {
		cfg.Jitter = rand.Float64
	}
	if cfg.Instance == "" {
		cfg.Instance = fmt.Sprintf("gw-%d", rand.Int64())
	}
	c := NewClient(cfg.API, cfg.Token, cfg.HTTP)
	c.guard = cfg.State.demoGuard
	life, halt := context.WithCancel(context.Background())
	return &Gateway{cfg: cfg, client: c, sender: NewSender(c, cfg.Clock), state: cfg.State, reconf: make(chan struct{}, 1), following: map[string]bool{},
		now: cfg.Clock.Now, life: life, halt: halt}
}

// Close stops the followers of running turns and waits for them; the turns
// stay recorded, so the next start sends their answers.
func (g *Gateway) Close() {
	g.halt()
	g.wg.Wait()
}

// Client returns the Bot API client.
func (g *Gateway) Client() *Client { return g.client }

// Reconfigure tells the poller that the token was set or cleared.
func (g *Gateway) Reconfigure() {
	select {
	case g.reconf <- struct{}{}:
	default:
	}
}

// Run polls until ctx ends. It does nothing while no token is stored and picks
// one up as soon as Reconfigure is called or within fifteen seconds.
func (g *Gateway) Run(ctx context.Context) {
	if g.state.demo() {
		// A demo workspace never talks to Telegram, so it does not poll.
		return
	}
	go g.sender.Run(ctx)
	defer g.Close()
	lease := g.state.NewLease(g.cfg.Instance)
	failures := 0
	for ctx.Err() == nil {
		if _, err := g.cfg.Token(); err != nil {
			g.setRunning(false)
			g.pause(ctx, IdleCheck)
			continue
		}
		ok, err := lease.Acquire(ctx)
		if err != nil || !ok {
			g.setConflict(true, "another process of this workspace holds the Telegram poller lease; polling waits for it")
			g.pause(ctx, g.conflictDelay(failures))
			failures++
			continue
		}
		failures = 0
		g.setRunning(true)
		if err := g.client.SetMyCommands(ctx, botCommands()); err != nil {
			// The menu beside the message box stays as it was; the commands work.
			g.cfg.Log.Warn("the Telegram command menu could not be set", "error", err.Error())
		}
		// The answers a stopped process owed the chat go out now.
		g.resume(ctx)
		if g.state.Bot(ctx) == "" {
			// A token stored without its bot's name (imported, or saved by an
			// older build): Settings and the pairing link need the name.
			if me, err := g.client.GetMe(ctx); err == nil && me.Username != "" {
				_ = g.state.SetBot(ctx, me.Username)
			}
		}
		g.poll(ctx, lease)
		g.setRunning(false)
		lease.Release(context.WithoutCancel(ctx))
	}
}

func (g *Gateway) setRunning(v bool) {
	g.mu.Lock()
	g.running = v
	if !v {
		g.everOK, g.conflicted, g.refused = false, false, false
	}
	g.mu.Unlock()
}

// setConflict marks the poller as kept from polling. The start of each such
// spell is logged once, with why, so the other poller can be found afterwards;
// the retries inside it are not.
func (g *Gateway) setConflict(v bool, why string) {
	g.mu.Lock()
	began := v && !g.conflicted
	g.conflicted = v
	g.mu.Unlock()
	if began && why != "" {
		g.cfg.Log.Warn(why)
	}
}

func (g *Gateway) markOK() {
	g.mu.Lock()
	g.everOK, g.conflicted, g.refused, g.lastOK = true, false, false, g.now()
	g.mu.Unlock()
}

// pause waits d, or less when Reconfigure is called.
func (g *Gateway) pause(ctx context.Context, d time.Duration) {
	c, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-g.reconf:
			cancel()
		case <-c.Done():
		}
	}()
	_ = g.cfg.Clock.Sleep(c, d)
}

// conflictDelay is 5 seconds doubling to 60, plus up to a quarter more.
func (g *Gateway) conflictDelay(n int) time.Duration {
	return g.backoff(ConflictBase, ConflictCap, n)
}

// backoff doubles base n times, up to limit, and adds up to a quarter of
// jitter.
func (g *Gateway) backoff(base, limit time.Duration, n int) time.Duration {
	d := base
	for i := 0; i < n && d < limit; i++ {
		d *= 2
	}
	d = min(d, limit)
	return d + time.Duration(float64(d)/4*g.cfg.Jitter())
}

func (g *Gateway) poll(ctx context.Context, lease *Lease) {
	var offset int64
	// failures backs off a poll that cannot reach Telegram (offline, DNS), so
	// an outage is not a request and a warning every second.
	conflicts, failures := 0, 0
	for ctx.Err() == nil {
		if _, err := g.cfg.Token(); err != nil {
			return
		}
		if err := lease.Renew(ctx); err != nil {
			if errors.Is(err, ErrFenced) {
				g.setConflict(true, "")
				g.cfg.Log.Warn("another process holds the Telegram poller lease; polling stopped")
				g.pause(ctx, g.conflictDelay(0))
				return
			}
			g.cfg.Log.Warn("the Telegram poller lease could not be renewed", "error", err.Error())
		}
		updates, err := g.client.GetUpdates(ctx, offset)
		if err != nil {
			var api *APIError
			switch {
			case ctx.Err() != nil:
				return
			case errors.As(err, &api) && api.IsConflict():
				g.setConflict(true, "Telegram answered that another program is polling this bot; each update goes to whichever poller asks first")
				g.pause(ctx, g.conflictDelay(conflicts))
				conflicts++
			case errors.As(err, &api) && api.IsAuth():
				g.cfg.Log.Warn("Telegram refused the bot token")
				g.mu.Lock()
				g.refused = true
				g.mu.Unlock()
				g.pause(ctx, IdleCheck)
			default:
				g.cfg.Log.Warn("polling Telegram failed", "error", err.Error())
				g.pause(ctx, g.backoff(g.cfg.Pause, FailureCap, failures))
				failures++
			}
			continue
		}
		conflicts, failures = 0, 0
		g.markOK()
		for _, u := range updates {
			if !g.handle(ctx, u) {
				break
			}
			offset = u.UpdateID + 1
		}
		g.pause(ctx, g.cfg.Pause)
	}
}

// Health is the poller's health value.
func (g *Gateway) Health() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case !g.running:
		return wire.TelegramStopped
	case g.conflicted:
		return wire.TelegramConflicted
	case g.refused:
		return wire.TelegramRefused
	case !g.everOK:
		return wire.TelegramConfigured
	case g.now().Sub(g.lastOK) > StaleAfter:
		return wire.TelegramStale
	}
	return wire.TelegramHealthy
}

// Status is what the status route and the doctor show. It carries neither the
// token nor the owner's identifiers.
type Status struct {
	TokenSet     bool
	Bot          string
	Paired       bool
	Health       string
	Unauthorized int
}

// Status reads the gateway's status.
func (g *Gateway) Status(ctx context.Context) Status {
	_, tokenErr := g.cfg.Token()
	_, paired := g.state.Owner(ctx)
	return Status{TokenSet: tokenErr == nil, Bot: g.state.Bot(ctx), Paired: paired, Health: g.Health(), Unauthorized: g.state.Unauthorized(ctx)}
}

// handle runs one update through its receipt. It reports whether the polling
// offset may move past it.
func (g *Gateway) handle(ctx context.Context, u Update) bool {
	cb := ""
	if u.Callback != nil {
		cb = u.Callback.ID
	}
	skip, err := g.state.Begin(ctx, u.UpdateID, cb)
	if err != nil {
		g.cfg.Log.Warn("an update receipt could not be written", "error", err.Error())
		return false
	}
	if skip {
		return true
	}
	action, chat, herr := g.dispatch(ctx, u)
	if herr != nil {
		g.cfg.Log.Warn("an update failed", "update", u.UpdateID, "action", action, "error", herr.Error())
		terminal, ferr := g.state.Fail(ctx, u.UpdateID, chat)
		if ferr != nil {
			return false
		}
		if terminal {
			g.state.Audit(ctx, u.UpdateID, action+":failed", chat)
			if o, ok := g.state.Owner(ctx); ok && chat == o.ChatID {
				_, _ = g.sender.Send(ctx, chat, "That request failed. Please try again.", nil, false)
			}
			return true
		}
		return false
	}
	if err := g.state.Complete(ctx, u.UpdateID, action, chat); err != nil {
		return false
	}
	g.state.Audit(ctx, u.UpdateID, action, chat)
	return true
}

func (g *Gateway) dispatch(ctx context.Context, u Update) (action string, chat int64, err error) {
	switch {
	case u.Callback != nil:
		return g.handleCallback(ctx, u)
	case u.Message != nil:
		return g.handleMessage(ctx, u)
	}
	return "ignored", 0, nil
}

func (g *Gateway) handleMessage(ctx context.Context, u Update) (string, int64, error) {
	m := u.Message
	chat := m.Chat.ID
	private := m.Chat.Type == "private"
	owner, paired := g.state.Owner(ctx)
	if !paired {
		if private && m.From != nil {
			res, err := g.state.Claim(ctx, chat, m.From.ID, claimText(m.Text))
			if err != nil {
				return "pair", chat, err
			}
			switch res {
			case Claimed:
				if _, err := g.sender.Send(ctx, chat, "Paired with this Applysmith workspace.\n\n"+welcome, menu(), false); err != nil {
					return "pair", chat, err
				}
				if g.cfg.OnPaired != nil {
					g.cfg.OnPaired()
				}
				return "pair", chat, nil
			}
		}
		return "unauthorized", chat, nil
	}
	if !private || m.From == nil || chat != owner.ChatID || m.From.ID != owner.UserID {
		return "unauthorized", chat, nil
	}
	if g.overLimit() {
		g.slowDown(ctx, chat)
		return "rate-limited", chat, nil
	}
	text := strings.TrimSpace(m.Text)
	switch {
	case text == "":
		// A photo, voice note, sticker or file: only text reaches the agent.
		return "text:none", chat, g.reply(ctx, chat, "I read text only. Write your question, or paste the posting's link or text.", nil)
	case strings.HasPrefix(text, "/"):
		return g.command(ctx, u.UpdateID, m, text)
	}
	return g.freeText(ctx, u.UpdateID, m, text)
}

// claimText is the text of a message that may carry a pairing code, alone or
// after /start.
func claimText(s string) string {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "/start"); ok {
		s = strings.TrimSpace(rest)
	}
	return s
}

// overLimit counts the message against 20 per minute.
func (g *Gateway) overLimit() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	kept := g.inbound[:0]
	for _, t := range g.inbound {
		if now.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	g.inbound = kept
	if len(g.inbound) >= InboundPerMinute {
		return true
	}
	g.inbound = append(g.inbound, now)
	return false
}

// slowDown tells the owner once per minute that messages are being dropped.
func (g *Gateway) slowDown(ctx context.Context, chat int64) {
	g.mu.Lock()
	now := g.now()
	quiet := now.Sub(g.slowAt) < time.Minute
	if !quiet {
		g.slowAt = now
	}
	g.mu.Unlock()
	if !quiet {
		// Not awaited: the notice queues behind the outbound limit and must not
		// hold the poller.
		go func() {
			_, _ = g.sender.Send(ctx, chat, "Slow down: I answer at most 20 messages a minute.", nil, false)
		}()
	}
}

func (g *Gateway) reply(ctx context.Context, chat int64, text string, markup *Markup) error {
	_, err := g.sender.Send(ctx, chat, vault.Redact(text), markup, false)
	return err
}
