// Clean-room: docs/provenance.md#CR-AGENT-GRANTS

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"applysmith/internal/agent/approvals"
	"applysmith/internal/agent/sessions"
	"applysmith/internal/api/wire"
)

// ConfirmationsConfig wires the confirmation service.
type ConfirmationsConfig struct {
	Cards    *approvals.Store
	Sessions *sessions.Store
	Redactor *Redactor
	Log      *slog.Logger
	// Describe names the company and role of a posting URL for the card; nil
	// leaves them blank.
	Describe func(ctx context.Context, postingURL string) (company, role string)
}

// Confirmations turns a held call into a card, runs the call once when the
// user approves the card, and tells the guard chain whether a standing approval
// covers a call. It is the only code that creates an approval, and it does so
// only from Approve, which only a user's decision on a card reaches.
type Confirmations struct {
	cfg    ConfirmationsConfig
	bridge *Bridge
	// OnCard is called after a new card is recorded; the server uses it to put
	// a Telegram card in front of the owner.
	OnCard func(ctx context.Context, c approvals.Card)
}

// NewConfirmations returns the service. The bridge it runs approved calls
// through is the one built with it in BridgeOptions.
func NewConfirmations(cfg ConfirmationsConfig) *Confirmations {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Redactor == nil {
		cfg.Redactor = NewRedactor()
	}
	c := &Confirmations{cfg: cfg}
	cfg.Cards.OnExpired = c.noteExpired
	return c
}

// standingCodes are the holds a standing approval may lift: missing intent and
// taint. A hold for any other reason is lifted by a card, once.
var standingCodes = []string{CodeIntentNotStated, CodeActionNotNamed, CodeOutsideContent}

// Hold records a held call as a pending card and returns its id, or an empty
// string when the card could not be recorded (the call stays held either way).
func (c *Confirmations) Hold(ctx context.Context, t *Turn, tool Tool, callID string, args json.RawMessage, d Decision) string {
	n := approvals.NewCard{
		SessionID: t.SessionID, TurnID: t.ID, CallID: callID, Channel: string(t.Channel), Origin: string(t.Origin), Tool: tool.Name,
		Tier: d.Tier, Code: d.Code, Reason: d.Reason, Args: string(args), ArgHash: argHash(args), Purpose: purposeOf(t), Remote: t.Remote,
	}
	if u := postingURL(args); u != "" {
		n.Host, _ = approvals.PostingHost(u)
		if c.cfg.Describe != nil {
			n.Company, n.Role = c.cfg.Describe(ctx, u)
		}
	}
	n.Summary = summaryOf(tool, n)
	shown, _ := c.cfg.Redactor.Redact(string(args))
	n.ArgsShown = cutBytes(shown, 4000)
	if d.Gate == GateConfirmation && slices.Contains(standingCodes, d.Code) && (t.Channel == ChannelDashboard || t.Channel == ChannelTelegram) {
		n.Target, _ = approvals.Target(tool.Name, args)
	}
	card, err := c.cfg.Cards.Create(ctx, n)
	if err != nil {
		c.cfg.Log.Error("agent: confirmation card not recorded", "tool", tool.Name, "error", err.Error())
		return ""
	}
	if c.OnCard != nil && card.CallID == callID {
		c.OnCard(ctx, card)
	}
	return card.ID
}

// purposeOf is what the user asked for: their message of the turn, or the
// label of the button they pressed. Text the model wrote is never read.
func purposeOf(t *Turn) string {
	text := strings.TrimSpace(t.UserMessage)
	if t.Origin == OriginButton && t.ButtonLabel != "" {
		text = t.ButtonLabel
	}
	if utf8.RuneCountInString(text) <= approvals.PurposeLimit {
		return text
	}
	return string([]rune(text)[:approvals.PurposeLimit-1]) + "…"
}

func summaryOf(tool Tool, n approvals.NewCard) string {
	s := tool.Label
	switch {
	case n.Company != "" && n.Role != "":
		s += ": " + n.Role + " at " + n.Company
	case n.Host != "":
		s += " on " + n.Host
	}
	return clipRunes(oneLine(s), 160)
}

// Covers implements Grants: an active standing approval for exactly this tool
// and target, usable in this conversation, lifts a tier 2 or 3 hold. An error
// reads as no approval, so the call stays held.
func (c *Confirmations) Covers(ctx context.Context, t *Turn, tool Tool, args json.RawMessage) (string, bool) {
	if t.Channel != ChannelDashboard && t.Channel != ChannelTelegram {
		return "", false
	}
	if tier := tierOf(tool, args); tier != 2 && tier != 3 {
		return "", false
	}
	target, ok := approvals.Target(tool.Name, args)
	if !ok {
		return "", false
	}
	a, ok, err := c.cfg.Cards.Covers(ctx, tool.Name, target, t.SessionID)
	if err != nil {
		c.cfg.Log.Error("agent: standing approval not read", "tool", tool.Name, "error", err.Error())
		return "", false
	}
	return a.ID, ok
}

// Approved is the outcome of approving a card.
type Approved struct {
	Card     approvals.Card
	Approval *approvals.Approval
	// Result is the text the call produced, or why it did not run.
	Result string
}

// Approve records the user's decision and runs the held call once through the
// whole guard chain, with the confirmation flag. Only the dashboard session may
// pass a scope wider than once; the store refuses any other actor.
func (c *Confirmations) Approve(ctx context.Context, id string, by approvals.Actor, scope approvals.Scope) (Approved, error) {
	card, ap, err := c.cfg.Cards.Approve(ctx, id, by, scope)
	if err != nil {
		return Approved{}, err
	}
	text := c.bridge.RunConfirmed(ctx, card, by)
	c.note(ctx, card, fmt.Sprintf("You approved \"%s\". %s", card.Summary, text))
	return Approved{Card: card, Approval: ap, Result: text}, nil
}

// Decline records that the user declined the card; the call never runs.
func (c *Confirmations) Decline(ctx context.Context, id string, by approvals.Actor) (approvals.Card, error) {
	card, err := c.cfg.Cards.Decline(ctx, id, by)
	if err != nil {
		return card, err
	}
	c.note(ctx, card, fmt.Sprintf("You declined \"%s\". It did not run.", card.Summary))
	return card, nil
}

// Pending lists the cards waiting for the user.
func (c *Confirmations) Pending(ctx context.Context) ([]approvals.Card, error) {
	return c.cfg.Cards.Pending(ctx)
}

// Active lists the standing approvals that have not ended.
func (c *Confirmations) Active(ctx context.Context) ([]approvals.Approval, error) {
	return c.cfg.Cards.Active(ctx)
}

// Revoke ends a standing approval; the next matching call is held again.
func (c *Confirmations) Revoke(ctx context.Context, id string, by approvals.Actor) (approvals.Approval, error) {
	return c.cfg.Cards.Revoke(ctx, id, by)
}

// Start expires the cards the previous server left pending and tells their
// conversations.
func (c *Confirmations) Start(ctx context.Context) error {
	_, err := c.cfg.Cards.ExpireAll(ctx)
	return err
}

// Sweep expires the cards that waited 12 hours.
func (c *Confirmations) Sweep(ctx context.Context) error {
	_, err := c.cfg.Cards.ExpireDue(ctx)
	return err
}

// sweepEvery is how often the service looks for cards that ran out.
const sweepEvery = time.Minute

func (c *Confirmations) sweepLoop(ctx context.Context) {
	tick := time.NewTicker(sweepEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := c.Sweep(ctx); err != nil && ctx.Err() == nil {
				c.cfg.Log.Error("agent: expired cards not swept", "error", err.Error())
			}
		}
	}
}

func (c *Confirmations) noteExpired(ctx context.Context, cards []approvals.Card) {
	for _, card := range cards {
		c.note(ctx, card, fmt.Sprintf("The held action \"%s\" expired without an answer and was declined.", card.Summary))
	}
}

// note puts a system-made line in the card's conversation. A conversation that
// was deleted meanwhile has nowhere to hold it.
func (c *Confirmations) note(ctx context.Context, card approvals.Card, text string) {
	_, err := c.cfg.Sessions.Append(context.WithoutCancel(ctx), sessions.Message{SessionID: card.SessionID, TurnID: card.TurnID, Role: sessions.RoleNote, Body: clipRunes(text, 2000), OK: true, SystemMade: true})
	if err != nil && !errors.Is(err, context.Canceled) {
		c.cfg.Log.Warn("agent: card note not stored", "card", card.ID, "error", err.Error())
	}
}

// RunConfirmed runs the call of an approved card once, through the whole guard
// chain with the confirmation flag, and returns what to tell the user. The
// chain sees the card's channel and origin as they were, so a refusal there
// (an envelope, a missing explicit request, a claim already taken) still
// stands.
func (b *Bridge) RunConfirmed(ctx context.Context, card approvals.Card, by approvals.Actor) string {
	tool, ok := b.opt.Registry.Lookup(card.Tool)
	if !ok {
		return "The tool is no longer available, so nothing ran."
	}
	t := &Turn{ID: card.TurnID, SessionID: card.SessionID, Channel: Channel(card.Channel), Origin: Origin(card.Origin), UserMessage: card.Purpose, Remote: card.Remote, Confirmed: true}
	args := json.RawMessage(card.Args)
	bg := context.WithoutCancel(ctx)
	d := b.opt.Guard.DecideConfirmed(bg, t, tool, card.CallID, args, Confirmation{CardID: card.ID, Actor: by})
	switch {
	case !d.Allowed():
		return fmt.Sprintf("It did not run: %s (%s).", strings.TrimSuffix(d.Reason, "."), d.Code)
	case !tool.Offered():
		if tool.Idempotent {
			b.note(b.opt.Sessions.SettleClaim(bg, tool.Name, card.CallID, "unavailable"), "claim not settled")
		}
		return "It cannot run in this build yet."
	}
	r := newRun(bg, t, func(wire.ChatEvent) {})
	defer r.cancelCalls()
	res := b.execute(bg, r, tool, card.CallID, args)
	plain, _ := r.peekResult(card.CallID)
	if strings.TrimSpace(plain) == "" {
		plain = res.Text
	}
	if res.IsError {
		return "It failed: " + clipRunes(oneLine(plain), 300)
	}
	return "Result: " + clipRunes(oneLine(plain), 600)
}

// describeFromLanes names the company and role of a posting that is on today's
// apply list.
func describeFromLanes(lanes ApplyLanes) func(context.Context, string) (string, string) {
	if lanes == nil {
		return nil
	}
	return func(ctx context.Context, raw string) (string, string) {
		want, ok := approvals.PostingURL(raw)
		if !ok {
			return "", ""
		}
		ls, err := lanes(ctx)
		if err != nil {
			return "", ""
		}
		for _, l := range ls {
			for _, it := range l.Items {
				if got, ok := approvals.PostingURL(it.URL); ok && got == want {
					return it.Company, it.Role
				}
			}
		}
		return "", ""
	}
}

var _ Grants = (*Confirmations)(nil)
