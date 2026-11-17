package agent

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"applysmith/internal/agent/pirpc"
	"applysmith/internal/agent/sessions"
	"applysmith/internal/api/wire"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/llm"
)

// phrase is the status line of a call: the model's step phrase when it is safe,
// else the tool's fixed label.
func (l *loop) phrase(tool, raw string) string {
	label := tool
	if t, ok := l.s.cfg.Bridge.opt.Registry.Lookup(tool); ok {
		label = t.Label
	}
	return StepPhrase(raw, label)
}

// handle processes one record from pi: the bookkeeping the mapping does not do
// (what the record keeps, the clocks of a response, the audit of a call pi
// answered itself), then the mapping.
func (l *loop) handle(rec pirpc.Record) {
	switch rec.Type {
	case pirpc.TypeResponse:
		l.onResponse(rec)
		return
	case pirpc.TypeMessageStart:
		if rec.Message != nil && rec.Message.Role == "assistant" {
			l.msgStart, l.firstTok = time.Now(), time.Time{}
		}
	case pirpc.TypeMessageUpdate:
		if l.firstTok.IsZero() && !l.msgStart.IsZero() {
			l.firstTok = time.Now()
		}
	case pirpc.TypeToolExecStart:
		l.onToolStart(rec)
	case pirpc.TypeMessageEnd:
		l.onMessageEnd(rec)
	case pirpc.TypeAgentSettled:
		l.settled = true
	}
	l.apply(l.pt.Feed(rec))
}

func (l *loop) onResponse(rec pirpc.Record) {
	switch {
	case rec.ID == l.promptID && !rec.Success:
		l.apply(l.pt.StopWith(pirpc.ReasonUnavailable, "pi refused the prompt: "+cutRunes(rec.Error, 200)))
		l.needAbort = false
		l.kill()
	case !rec.Success:
		l.s.cfg.Log.Warn("agent: pi refused a command", "turn", l.t.ID, "command", rec.Command, "error", cutRunes(rec.Error, 200))
	}
}

func (l *loop) onToolStart(rec pirpc.Record) {
	args, _, err := splitStep(rec.Args)
	if err != nil {
		args = rec.Args
	}
	l.callsOpen[rec.ToolCallID] = true
	_, err = l.store().Append(context.Background(), sessions.Message{SessionID: l.t.SessionID, TurnID: l.t.ID, Role: sessions.RoleToolCall,
		Body: cutBytes(string(args), 8<<10), ToolName: rec.ToolName, ToolCallID: rec.ToolCallID, OK: true})
	l.note(err, "tool call not stored")
	// pi answers a name it never registered itself, so the forwarding route
	// never sees it; the audit row comes from here.
	reg := l.s.cfg.Bridge.opt.Registry
	if t, ok := reg.Lookup(rec.ToolName); !ok || !t.Offered() || !l.t.permits(t) {
		l.s.cfg.Bridge.auditUnknown(context.Background(), l.r, rec.ToolName, rec.ToolCallID, args)
	}
}

// onMessageEnd keeps what the assistant said in a finished step.
func (l *loop) onMessageEnd(rec pirpc.Record) {
	m := rec.Message
	if m == nil || m.Role != "assistant" {
		return
	}
	l.lastText = m.Text()
	l.flushTokens()
	if (m.StopReason == "stop" || m.StopReason == "toolUse") && strings.TrimSpace(l.lastText) != "" && !toolCallMarkup(l.lastText) {
		safe, _ := l.s.cfg.Bridge.Disclose(context.Background(), l.t, Envelope{Text: l.lastText})
		_, err := l.store().Append(context.Background(), sessions.Message{SessionID: l.t.SessionID, TurnID: l.t.ID, Role: sessions.RoleAssistant, Body: safe.Text, OK: true})
		l.note(err, "assistant text not stored")
	}
}

// apply carries what the mapping produced to the record, the stream, the
// ledger and pi.
func (l *loop) apply(out pirpc.Output) {
	for _, ev := range out.Stream {
		l.stream(ev)
	}
	for _, u := range out.Usage {
		l.usage(u)
	}
	if out.Nudge {
		l.send("steer", map[string]any{"message": pirpc.NudgeText})
	}
	if out.Abort != "" {
		l.needAbort = true
		l.r.cancelCalls()
	}
}

func (l *loop) stream(ev pirpc.StreamEvent) {
	ctx := context.Background()
	switch ev.Type {
	case pirpc.StreamToken:
		l.pushToken(ev.Text)
	case pirpc.StreamToolCall:
		l.flushTokens()
		l.emit(wire.ChatEvent{Type: wire.ChatToolCall, ToolCallID: ev.ToolID, Tool: ev.Tool, Class: ToolClass(ev.Tool), Phrase: ev.Phrase})
	case pirpc.StreamToolResult:
		delete(l.callsOpen, ev.ToolID)
		res, ok := l.r.takeResult(ev.ToolID)
		if !ok {
			// pi answered the call itself, so nothing passed the bridge's
			// redaction: its text does here.
			res.text, _ = l.s.cfg.Bridge.opt.Redactor.Redact(ev.Summary)
		}
		_, err := l.store().AppendResult(ctx, sessions.Message{SessionID: l.t.SessionID, TurnID: l.t.ID, ToolName: ev.Tool, ToolCallID: ev.ToolID, Body: res.text, OK: ev.OK, OutsideContent: res.outside, Envelope: sessions.Envelope{RunID: res.runID}})
		l.note(err, "tool result not stored")
		if ev.OK {
			l.results = append(l.results, ev.Tool+": "+clipRunes(oneLine(res.text), 200))
		}
		okv := ev.OK
		l.emit(wire.ChatEvent{Type: wire.ChatToolResult, ToolCallID: ev.ToolID, Tool: ev.Tool, Class: ToolClass(ev.Tool), OK: &okv, Summary: clipRunes(oneLine(res.text), 300), RunID: res.runID})
	case pirpc.StreamSteer:
		if ev.Text != pirpc.NudgeText { // the nudge never counted against the five queued
			l.in.Active.delivered()
			l.t.Steer(ev.Text) // the user's own words from now on, for the gates
		}
		_, err := l.store().Append(ctx, sessions.Message{SessionID: l.t.SessionID, TurnID: l.t.ID, Role: sessions.RoleSteer, Body: ev.Text, OK: true})
		l.note(err, "steering message not stored")
		l.emit(wire.ChatEvent{Type: wire.ChatSteerEvent, Text: ev.Text})
	case pirpc.StreamFinal:
		l.terminal, l.res = true, TurnResult{Status: sessions.TurnFinal}
		l.flushTokens()
		text := ev.Text
		if strings.TrimSpace(text) == "" || toolCallMarkup(text) {
			text = l.noAnswerText(text)
		}
		env, _ := l.s.cfg.Bridge.Disclose(ctx, l.t, l.t.EnvelopeFor(text))
		if text != ev.Text {
			_, err := l.store().Append(ctx, sessions.Message{SessionID: l.t.SessionID, TurnID: l.t.ID, Role: sessions.RoleAssistant, Body: env.Text, OK: true})
			l.note(err, "assistant text not stored")
		}
		l.note(l.store().KeepEnvelope(ctx, l.t.SessionID, l.t.ID, recorded(env)), "answer envelope not stored")
		l.emit(finalEvent(l.t.ID, env))
	case pirpc.StreamError:
		if ev.Reason == pirpc.ReasonProvider && l.r != nil && l.r.capStop.Load() {
			// The gateway refused a call for the daily cap.
			ev.Reason = pirpc.ReasonCostCap
		}
		l.terminal, l.res = true, TurnResult{Status: sessions.TurnError, Reason: ev.Reason, Detail: ev.Detail}
		l.emit(wire.ChatEvent{Type: wire.ChatError, Reason: ev.Reason, Detail: ev.Detail})
	}
}

// noAnswerText stands in for a final answer the model left empty or wrote as
// tool-call markup. It says why there is no answer (the call limit, a tool
// call written out), and a turn that already holds tool results shows them
// instead of claiming nothing was found.
func (l *loop) noAnswerText(raw string) string {
	head := "The model ended the turn without writing an answer."
	switch {
	case l.r != nil && l.r.calls.Load() >= l.r.callLimit():
		head = fmt.Sprintf("The agent stopped at the limit of %d tool calls in one turn without writing an answer.", l.r.callLimit())
	case toolCallMarkup(raw):
		head = "The model wrote a tool call as text instead of an answer, so it is not shown."
	}
	if len(l.results) == 0 {
		return head + " Ask again, or rephrase the question."
	}
	return head + " What the tools returned:\n- " + strings.Join(l.results, "\n- ")
}

// toolCallShape is the markup of a tool call some models write as text
// (Hermes and Qwen style) when they can no longer call tools.
var toolCallShape = lazyregexp.New(`(?i)<\s*/?\s*tool_call\b|<\s*function\s*=`)

// toolCallMarkup reports text that is a tool call written out, which is never
// an answer to show.
func toolCallMarkup(text string) bool { return toolCallShape.MatchString(text) }

// usage writes the ledger row of one response pi made on its own login; the
// gateway writes the rows of every other call. When pi reports no counts the
// row is estimated from text length and flagged.
func (l *loop) usage(u pirpc.UsageRecord) {
	if l.route.PiLogin == "" {
		return
	}
	lu := llm.Usage{
		Input: u.Usage.Input + u.Usage.CacheRead + u.Usage.CacheWrite, Output: u.Usage.Output,
		CachedInput: u.Usage.CacheRead, CacheWrite: u.Usage.CacheWrite, Reasoning: u.Usage.Reasoning,
	}
	if u.Estimated {
		lu.Input = (l.promptSize + int(l.r.packedChars.Load())) / 4
		lu.Output = len(l.lastText) / 4
	}
	row := llm.AgentUsage{StartedAt: time.Now(), SessionID: l.t.SessionID, Outcome: u.Outcome, Usage: lu,
		FinishReason: u.StopReason, ErrorText: llm.Redact(cutRunes(u.Error, 200)), Estimated: u.Estimated}
	if !l.msgStart.IsZero() {
		row.StartedAt = l.msgStart
		row.Latency = time.Since(l.msgStart)
		if !l.firstTok.IsZero() {
			ttft := l.firstTok.Sub(l.msgStart)
			row.TTFT = &ttft
		}
	}
	l.s.cfg.Router.RecordAgentUsage(context.WithoutCancel(context.Background()), l.route, row)
}

// tokenGate releases the streamed text of one message only after the
// disclosure rules passed over everything so far, and keeps back the word still
// being written, because a key, a marker or a path is one unbroken word and
// must not be shown in part. A message end or a tool call sends the rest. Once
// a rule blocks, nothing more of the message is sent and the final event
// carries the notice.
type tokenGate struct {
	text string
	sent int
	// read is how much of text the rules have read.
	read    int
	blocked bool
}

// streamReadShare paces the rules over a message being written. They read the
// whole message, so reading it at every chunk costs work in the square of its
// length (54 s of processor time for a 16,000-character answer in 4-character
// chunks). They read it again once it has grown by 1/streamReadShare of its
// length since the last read, and before the rest is sent: a message of a few
// lines is read at every chunk, a long one every sentence or so, and nothing
// is sent that a read has not passed.
const streamReadShare = 64

// pushToken adds a chunk and sends what is safe and not held back.
func (l *loop) pushToken(chunk string) {
	g := &l.tg
	if g.blocked || chunk == "" {
		return
	}
	g.text += chunk
	if (len(g.text)-g.read)*streamReadShare < len(g.text) || !l.tokensPass() {
		return
	}
	cut := strings.LastIndexAny(g.text, " \t\r\n") + 1
	l.sendTokens(cut)
}

// tokensPass reads the message so far with the rules when it has grown since
// they last read it, and reports whether no rule has blocked it.
func (l *loop) tokensPass() bool {
	g := &l.tg
	if !g.blocked && g.read < len(g.text) {
		g.read = len(g.text)
		g.blocked = toolCallMarkup(g.text) || l.s.cfg.Bridge.opt.Disclosure.Check(Envelope{Text: g.text}, l.t.reportedPaths()) != nil
	}
	return !g.blocked
}

// flushTokens sends the rest of the message when it passes, and starts the
// next message.
func (l *loop) flushTokens() {
	g := &l.tg
	if l.tokensPass() {
		l.sendTokens(len(g.text))
	}
	*g = tokenGate{}
}

func (l *loop) sendTokens(upTo int) {
	g := &l.tg
	for upTo > g.sent && upTo < len(g.text) && !utf8.RuneStart(g.text[upTo]) {
		upTo--
	}
	if upTo <= g.sent {
		return
	}
	l.emit(wire.ChatEvent{Type: wire.ChatToken, Text: g.text[g.sent:upTo]})
	g.sent = upTo
}
