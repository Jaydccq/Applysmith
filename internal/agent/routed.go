package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"applysmith/internal/agent/pirpc"
	"applysmith/internal/agent/sessions"
	"applysmith/internal/api/wire"
	"applysmith/internal/store"
)

// Routable reports whether the router's choice can be carried out: the tool
// is in the registry and the channel's envelope contains its class. A tool the
// build does not have yet is left to the model loop.
func (b *Bridge) Routable(ch Channel, r Route) bool {
	t, ok := b.opt.Registry.Lookup(r.Tool)
	return ok && ch.allows(t.Class)
}

// RunRouted carries out the call the router chose and answers from its
// result, with no model call. The call passes the same guard chain as a call
// the model chose, and every decision is audited; the turn's origin tells the
// chain the arguments came from the user's own message.
func (b *Bridge) RunRouted(ctx context.Context, t *Turn, rt Route, emit func(wire.ChatEvent)) TurnResult {
	emit(wire.ChatEvent{Type: wire.ChatTurnStart, TurnID: t.ID})
	tool, _ := b.opt.Registry.Lookup(rt.Tool)
	callID := store.NewID("call")
	store := b.opt.Sessions
	bg := context.WithoutCancel(ctx)
	_, err := store.Append(bg, sessions.Message{SessionID: t.SessionID, TurnID: t.ID, Role: sessions.RoleToolCall, Body: string(rt.Args), ToolName: tool.Name, ToolCallID: callID, OK: true})
	b.note(err, "routed call not stored")
	emit(wire.ChatEvent{Type: wire.ChatToolCall, TurnID: t.ID, ToolCallID: callID, Tool: tool.Name, Class: string(tool.Class), Phrase: tool.Label})

	r := newRun(ctx, t, func(ev wire.ChatEvent) { ev.TurnID = t.ID; emit(ev) })
	defer r.cancelCalls()
	res, answer := b.callRouted(ctx, r, tool, callID, rt)
	full, ok := r.takeResult(callID)
	if !ok {
		full = kept{text: res.Text}
	}
	_, err = store.AppendResult(bg, sessions.Message{SessionID: t.SessionID, TurnID: t.ID, ToolName: tool.Name, ToolCallID: callID, Body: full.text, OK: !res.IsError, OutsideContent: full.outside, Envelope: sessions.Envelope{RunID: full.runID}})
	b.note(err, "routed result not stored")
	okv := !res.IsError
	emit(wire.ChatEvent{Type: wire.ChatToolResult, TurnID: t.ID, ToolCallID: callID, Tool: tool.Name, Class: string(tool.Class), OK: &okv, Summary: clipRunes(oneLine(full.text), 300), RunID: full.runID})

	if ctx.Err() != nil {
		emit(wire.ChatEvent{Type: wire.ChatError, TurnID: t.ID, Reason: pirpc.ReasonCancelled})
		return TurnResult{Status: sessions.TurnError, Reason: pirpc.ReasonCancelled}
	}
	env, _ := b.Disclose(ctx, t, t.EnvelopeFor(answer))
	_, err = store.Append(bg, sessions.Message{SessionID: t.SessionID, TurnID: t.ID, Role: sessions.RoleAssistant, Body: env.Text, OK: true, Envelope: recorded(env)})
	b.note(err, "routed answer not stored")
	emit(wire.ChatEvent{Type: wire.ChatToken, TurnID: t.ID, Text: env.Text})
	emit(finalEvent(t.ID, env))
	return TurnResult{Status: sessions.TurnFinal}
}

// callRouted is the chain for a routed call and the text the user is shown.
func (b *Bridge) callRouted(ctx context.Context, r *run, tool Tool, callID string, rt Route) (wire.AgentToolResult, string) {
	args, _, err := splitStep(rt.Args)
	if err == nil {
		err = validateArgs(tool.params, args)
	}
	if err != nil {
		d := b.opt.Guard.Deny(ctx, r.turn, tool.Name, callID, rt.Args, GateArguments, CodeInvalidArguments, cutRunes(err.Error(), 200))
		return wire.AgentToolResult{IsError: true, Text: d.Reason}, "I could not run that: " + d.Reason
	}
	d := b.opt.Guard.Decide(ctx, r.turn, tool, callID, args)
	if d.Allowed() && !tool.Offered() {
		res := b.deliver(ctx, r, delivery{tool: tool, callID: callID, args: args}, "This tool cannot run in this build.", true)
		if tool.Idempotent {
			if err := b.opt.Sessions.SettleClaim(context.WithoutCancel(ctx), tool.Name, callID, "unavailable"); err != nil {
				b.note(err, "claim not settled")
			}
		}
		return res, "Starting " + strings.ToLower(tool.Label) + " from chat is not available in this build yet."
	}
	res, c := b.carryOut(ctx, r, tool, callID, args, d, func(reason, _ string) string { return "Held: " + reason })
	switch {
	case c.held:
		return res, fmt.Sprintf("That is waiting for your confirmation (%s). Nothing has run.", c.why)
	case c.refused:
		return res, fmt.Sprintf("I cannot do that here: %s.", strings.TrimSuffix(c.why, "."))
	}
	// The user is shown the tool's own text, not the fenced copy a model gets.
	plain, _ := r.peekResult(callID)
	if strings.TrimSpace(plain) == "" {
		plain = "Done."
	}
	return res, cutRunes(plain, ModelResultLimit)
}

func (b *Bridge) note(err error, what string) {
	if err != nil && !errors.Is(err, context.Canceled) {
		b.opt.Log.Error("agent: "+what, "error", err.Error())
	}
}
