package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	harnessv "applysmith/internal/agent/harness"
	"applysmith/internal/agent/pirpc"
	"applysmith/internal/agent/sessions"
	"applysmith/internal/api/wire"
	"applysmith/internal/gates"
	"applysmith/internal/llm"
	"applysmith/internal/store"
)

// Errors the bridge returns for the API layer to map.
var (
	// ErrBridgeAuth means the credential is not one a live process holds.
	ErrBridgeAuth = errors.New("agent: not a process credential")
	// ErrCatalogMismatch means pi reported a tool set other than the catalog.
	ErrCatalogMismatch = errors.New("agent: the tool set pi reported is not the catalog")
)

// GateArguments is the gate that rejects arguments outside a tool's schema.
const GateArguments gates.Gate = "argument_schema"

// BridgeOptions wires the bridge to the registry, the chain and the record.
type BridgeOptions struct {
	Registry *Registry
	Guard    *Guard
	Sessions *sessions.Store
	Packer   Packer
	// Confirmations records a card for each held call and runs an approved
	// call; nil leaves a held call as a result the model reads and nothing else.
	Confirmations *Confirmations
	// Redactor rewrites every tool result before the model, the record and the
	// stream see it; nil means the process's own, NewRedactor.
	Redactor *Redactor
	// Disclosure gates every public field of a final answer; nil means the
	// default table with no protected roots.
	Disclosure *DisclosureGate
	// Salt is the audit salt the disclosure rows are written with.
	Salt string
	Log  *slog.Logger
	// Harness supplies the active harness version (guidance and tool notes);
	// nil runs every turn with the empty harness.
	Harness HarnessSource
	// Gateway serves the model calls of the processes' turns; nil refuses them.
	Gateway Gateway
}

// Gateway serves one model request of an agent turn (llm.Router.ServeAgentCall).
type Gateway interface {
	ServeAgentCall(ctx context.Context, call llm.AgentCall, w http.ResponseWriter) error
}

// HarnessSource returns the harness a new turn runs with.
type HarnessSource interface {
	Active(ctx context.Context) (harnessv.Harness, error)
}

// harnessOf returns the harness pinned to a process, reading it on first use.
// A failed read runs the turn with the empty harness: the fixed prompt is
// complete without it.
func (b *Bridge) harnessOf(ctx context.Context, r *run) harnessv.Harness {
	r.harnessOnce.Do(func() {
		if b.opt.Harness == nil {
			return
		}
		h, err := b.opt.Harness.Active(ctx)
		if err != nil {
			b.opt.Log.Error("agent: harness not read", "turn", r.turn.ID, "error", err.Error())
			return
		}
		r.harness = h
	})
	return r.harness
}

// Bridge is the Go end of the protocol the embedded extension speaks: it
// serves the catalog, checks the tool set pi reports, packs the turn's
// context and runs forwarded calls through the guard chain. A process holds a
// credential issued by Open, valid until Close.
type Bridge struct {
	opt BridgeOptions
	mu  sync.Mutex
	run map[[sha256.Size]byte]*run
	// guests are the bridges of other stacks whose processes call this
	// bridge's routes (lend).
	guests []*Bridge
}

// NewBridge returns a bridge with no open credentials.
func NewBridge(opt BridgeOptions) *Bridge {
	if opt.Log == nil {
		opt.Log = slog.New(slog.DiscardHandler)
	}
	if opt.Redactor == nil {
		opt.Redactor = NewRedactor()
	}
	if opt.Disclosure == nil {
		opt.Disclosure = NewDisclosureGate(DisclosureOptions{})
	}
	b := &Bridge{opt: opt, run: map[[sha256.Size]byte]*run{}}
	if opt.Confirmations != nil {
		opt.Confirmations.bridge = b
	}
	return b
}

// open issues the credential of a process that will run the given turn.
func (b *Bridge) open(parent context.Context, t *Turn, emit func(wire.ChatEvent)) (string, *run, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	r := newRun(parent, t, emit)
	b.mu.Lock()
	b.run[sha256.Sum256([]byte(token))] = r
	b.mu.Unlock()
	return token, r, nil
}

// close revokes a credential and ends the calls running under it.
func (b *Bridge) close(token string, r *run) {
	b.mu.Lock()
	delete(b.run, sha256.Sum256([]byte(token)))
	b.mu.Unlock()
	r.cancelCalls()
}

// Authenticate reports whether the token belongs to a live process. The map is
// keyed by digest, so the comparison does not depend on the token's bytes.
func (b *Bridge) Authenticate(token string) bool {
	_, _, ok := b.holder(token)
	return ok
}

// lend answers the processes of another bridge on this one's routes until the
// returned function is called.
func (b *Bridge) lend(guest *Bridge) (end func()) {
	b.mu.Lock()
	b.guests = append(b.guests, guest)
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		b.guests = slices.DeleteFunc(b.guests, func(g *Bridge) bool { return g == guest })
		b.mu.Unlock()
	}
}

// holder returns the bridge that issued a process's credential, this one or a
// guest, with the run it belongs to. Each route's method goes on as that
// bridge, so a guest's process gets the guest's catalog, guard and record.
func (b *Bridge) holder(token string) (*Bridge, *run, bool) {
	if r, ok := b.lookup(token); ok {
		return b, r, true
	}
	b.mu.Lock()
	guests := slices.Clone(b.guests)
	b.mu.Unlock()
	for _, g := range guests {
		if r, ok := g.lookup(token); ok {
			return g, r, true
		}
	}
	return b, nil, false
}

func (b *Bridge) lookup(token string) (*run, bool) {
	if token == "" {
		return nil, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.run[sha256.Sum256([]byte(token))]
	return r, ok
}

// ModelCall serves one model request of the process's turn through the
// gateway, on the turn's row and under its conversation for the ledger and the
// affinity. A
// refusal for the daily cap is kept, so the turn ends as cost-cap.
func (b *Bridge) ModelCall(ctx context.Context, token string, call llm.AgentCall, w http.ResponseWriter) error {
	b, r, ok := b.holder(token)
	if !ok {
		return ErrBridgeAuth
	}
	if b.opt.Gateway == nil {
		return &llm.Error{Kind: llm.KindNoRoute, Task: llm.AgentTask, Message: "no model gateway"}
	}
	call.SessionID, call.TurnID, call.Task = r.turn.SessionID, r.turn.ID, r.turn.modelTask()
	err := b.opt.Gateway.ServeAgentCall(ctx, call, w)
	if llm.KindOf(err) == llm.KindCostCap {
		r.capStop.Store(true)
	}
	return err
}

// Catalog answers the extension's catalog request for the process's turn.
func (b *Bridge) Catalog(token string) (wire.AgentCatalog, error) {
	b, r, ok := b.holder(token)
	if !ok {
		return wire.AgentCatalog{}, ErrBridgeAuth
	}
	h := b.harnessOf(context.Background(), r)
	specs := b.opt.Registry.SpecsFor(r.turn)
	for i := range specs {
		specs[i].Description = h.Describe(specs[i].Name, specs[i].Description)
	}
	return wire.AgentCatalog{Tools: specs}, nil
}

// Hello checks the tool set pi reports against the catalog of the turn. A set
// that differs marks the process unusable.
func (b *Bridge) Hello(token string, h wire.AgentHello) (wire.AgentHelloResult, error) {
	b, r, ok := b.holder(token)
	if !ok {
		return wire.AgentHelloResult{}, ErrBridgeAuth
	}
	var want []string
	for _, s := range b.opt.Registry.SpecsFor(r.turn) {
		want = append(want, s.Name)
	}
	got := slices.Clone(h.Tools)
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(want, got) {
		r.helloBad.Store(fmt.Sprintf("pi reported %d tools, the catalog has %d", len(got), len(want)))
		r.badOnce.Do(func() { close(r.helloBadC) })
		return wire.AgentHelloResult{}, ErrCatalogMismatch
	}
	r.helloOnce.Do(func() { close(r.helloCh) })
	return wire.AgentHelloResult{Tools: wire.SafeInt(len(want))}, nil
}

// TurnContext returns the system prompt and the packed earlier turns.
func (b *Bridge) TurnContext(ctx context.Context, token string) (wire.AgentTurnContext, error) {
	b, r, ok := b.holder(token)
	if !ok {
		return wire.AgentTurnContext{}, ErrBridgeAuth
	}
	system, prefix, err := b.opt.Packer.Pack(ctx, r.turn)
	if err != nil {
		return wire.AgentTurnContext{}, err
	}
	var active []string
	if sk, ok := skillNamed(r.turn.Skill()); ok {
		// A turn that starts under a skill carries its block in the system prompt,
		// above the harness's guidance, and offers the skill's tools alone.
		system += skillPrompt(sk)
		active = b.opt.Registry.activeToolsOf(r.turn, sk)
	}
	h := b.harnessOf(ctx, r)
	system += h.Block()
	if err := b.opt.Sessions.SetTurnHarness(ctx, r.turn.ID, h.Version); err != nil {
		b.opt.Log.Error("agent: harness version not recorded", "turn", r.turn.ID, "error", err.Error())
	}
	if prefix == nil {
		prefix = []wire.AgentPrefixMessage{}
	}
	n := len(system)
	for _, m := range prefix {
		n += len(m.Text)
	}
	r.packedChars.Store(int64(n))
	return wire.AgentTurnContext{SystemPrompt: system, Prefix: prefix, ActiveTools: active}, nil
}

// ToolCall runs one forwarded call. A refusal, a hold and a tool failure are
// results the model reads; only a bad credential is an error.
func (b *Bridge) ToolCall(ctx context.Context, token string, call wire.AgentToolCall) (wire.AgentToolResult, error) {
	b, r, ok := b.holder(token)
	if !ok {
		return wire.AgentToolResult{}, ErrBridgeAuth
	}
	// A read (a call of tier 0) gives the same answer to the same call within
	// a turn, so a repeat is answered from the first result; a fresh read (a
	// window's controls) changes on its own and runs again. Any other call
	// writes or asks a model (a draft records a proposal, a task's step, an
	// interview reply or a generated digest), so it runs each time and forgets
	// those results.
	tool, _ := b.opt.Registry.Lookup(call.Name)
	reads := tierOf(tool, call.Args) == 0
	reusable := reads && !tool.Fresh
	key := pirpc.CallKey(call.Name, call.Args)
	if reusable {
		if prev, n, ok := r.repeat(key); ok {
			return b.repeated(ctx, r, call, prev, n), nil
		}
	}
	if tool.Stepwise && tool.Offered() && r.turn.permits(tool) {
		r.stepwise.Store(true)
	}
	n, limit := r.calls.Add(1), r.callLimit()
	if n > limit {
		b.opt.Guard.Deny(ctx, r.turn, call.Name, call.ToolCallID, call.Args, GatePolicyFloor, CodeToolLimit, fmt.Sprintf("a turn makes at most %d tool calls", limit))
		return wire.AgentToolResult{IsError: true, Text: "The tool limit for this turn is reached. Give your final answer now.", FinalOnly: true}, nil
	}
	res := b.call(ctx, r, call)
	switch {
	case !reads:
		r.forgetAnswers()
	case reusable && !res.IsError && !res.FinalOnly:
		// A failure may be passing and is answered afresh; a hold ends the
		// turn's tools.
		kept, recorded := r.peekKept(call.ToolCallID)
		r.remember(key, answer{res: res, kept: kept, recorded: recorded})
	}
	if n == limit {
		res.FinalOnly = true
		r.turn.Collect(ToolResult{Limitations: []string{fmt.Sprintf("The turn used all %d tool calls it may make, so the answer rests on what those calls returned.", limit)}})
	}
	return res, nil
}

// MaxRepeats is how many calls a turn may repeat before it is told to answer.
const MaxRepeats = 3

// repeated answers a read made again in the same turn, same tool and same
// arguments, with the first call's result. It neither runs nor counts toward
// the call limit: a model once spent the whole limit on recall_session calls
// with the same query. The first call passed the guard chain in this turn,
// and a read is not held for what the turn has read since. Past MaxRepeats
// the turn is told to answer.
func (b *Bridge) repeated(ctx context.Context, r *run, call wire.AgentToolCall, prev answer, n int) wire.AgentToolResult {
	if n > MaxRepeats {
		b.opt.Guard.Deny(ctx, r.turn, call.Name, call.ToolCallID, call.Args, GatePolicyFloor, CodeRepeatedCall, "the turn keeps repeating calls it already made")
		return wire.AgentToolResult{IsError: true, Text: "This turn keeps repeating calls it already made. Give your final answer now from the results you have.", FinalOnly: true}
	}
	b.opt.Guard.Note(ctx, r.turn, call.Name, call.ToolCallID, call.Args, Note{Gate: GatePolicyFloor, Verdict: gates.AllowWithTrace, Code: CodeRepeatedCall,
		Reason: "answered with the result of the same call earlier in the turn"})
	if prev.recorded {
		r.keepResult(call.ToolCallID, prev.kept)
	}
	res := prev.res
	res.Text = "You already made this exact call in this turn; its result is repeated below. Do not make it again.\n" + res.Text
	return res
}

func (b *Bridge) call(ctx context.Context, r *run, call wire.AgentToolCall) wire.AgentToolResult {
	tool, ok := b.opt.Registry.Lookup(call.Name)
	if !ok || !tool.Offered() || !r.turn.permits(tool) {
		d := b.opt.Guard.Deny(ctx, r.turn, call.Name, call.ToolCallID, call.Args, GatePolicyFloor, CodeUnknownTool, "no such tool is registered for this turn")
		return refused(d)
	}
	args, _, err := splitStep(call.Args)
	if err == nil {
		err = validateArgs(tool.params, args)
	}
	if err != nil {
		d := b.opt.Guard.Deny(ctx, r.turn, call.Name, call.ToolCallID, call.Args, GateArguments, CodeInvalidArguments, cutRunes(err.Error(), 200))
		return wire.AgentToolResult{IsError: true, Text: "The call was rejected before it ran: " + d.Reason}
	}
	d := b.opt.Guard.Decide(ctx, r.turn, tool, call.ToolCallID, args)
	res, _ := b.carryOut(ctx, r, tool, call.ToolCallID, args, d, heldText)
	return res
}

// carried is what became of a call once the guard decided it, and the reason
// when it did not run.
type carried struct {
	held, refused bool
	why           string
}

// carryOut does what the guard decided about a call, the same for a call the
// model chose and one the router chose. A hold first asks the tool whether it
// would refuse the call (refusedBeforeHold); otherwise it records the card and
// its result, worded by held, ends the turn's tools. A refusal is the call's
// answer, and an allowed call runs.
func (b *Bridge) carryOut(ctx context.Context, r *run, tool Tool, callID string, args json.RawMessage, d Decision, held func(reason, card string) string) (wire.AgentToolResult, carried) {
	at := delivery{tool: tool, callID: callID, args: args}
	switch {
	case d.Verdict == gates.Hold:
		if reason, ok := b.refusedBeforeHold(ctx, r, tool, callID, args, d); ok {
			text := reason + " This call made no confirmation card and changed nothing; tell the user why, and ask them before trying another."
			return b.deliver(ctx, r, at, text, false), carried{refused: true, why: reason}
		}
		card := b.hold(ctx, r, tool, callID, args, d)
		r.emit(wire.ChatEvent{Type: wire.ChatToolHeld, TurnID: r.turn.ID, ToolCallID: callID, Tool: tool.Name, Class: string(tool.Class), Code: d.Code, Reason: d.Reason, ConfirmationID: card})
		// The model kept calling tools after a hold, so the result says plainly
		// that the turn ends here, and no further tool is offered.
		res := b.deliver(ctx, r, at, held(d.Reason, card), false)
		res.FinalOnly = true
		return res, carried{held: true, why: d.Reason}
	case !d.Allowed():
		return refused(d), carried{refused: true, why: d.Reason}
	}
	return b.execute(ctx, r, tool, callID, args), carried{}
}

// heldText is what the model reads for a held call: the hold, whether a card
// is waiting, and that it must stop and tell the user.
func heldText(reason, card string) string {
	text := reason
	if !strings.HasPrefix(text, "Held:") {
		text = "Held: " + strings.TrimSuffix(text, ".") + ". Do not retry in this turn."
	}
	if card != "" {
		text += " A confirmation card is now waiting for the user in the dashboard; the call runs only if they approve it there."
	} else {
		text += " No confirmation card could be recorded, so nothing is waiting for the user."
	}
	return text + " Stop here: make no more tool calls in this turn, and tell the user what is held and why."
}

// refusedBeforeHold asks a held call's tool whether it would refuse the call
// now (its Conflict). A card for such a call would ask the user to approve
// what then changes nothing: live, an answer outside a Yes/No question's
// choices was held, approved, and only then refused. The model is answered
// with the refusal instead, so it can ask the user; the call made no card and
// did not run. A check that cannot decide lets the hold stand. It returns the
// tool's reason.
func (b *Bridge) refusedBeforeHold(ctx context.Context, r *run, tool Tool, callID string, args json.RawMessage, d Decision) (string, bool) {
	if tool.Conflict == nil {
		return "", false
	}
	cctx, cancel := context.WithTimeout(ctx, tool.Timeout)
	defer cancel()
	err := tool.Conflict(cctx, ToolCall{CallID: callID, Args: args, Turn: r.turn})
	if !errors.Is(err, ErrRefused) {
		return "", false
	}
	reason := strings.TrimPrefix(err.Error(), ErrRefused.Error()+": ")
	b.opt.Guard.Note(ctx, r.turn, tool.Name, callID, args, Note{Gate: d.Gate, Verdict: gates.Deny, Code: CodeToolRefused, Reason: cutRunes(reason, 200)})
	return reason, true
}

// hold records the card of a held call and returns its id; empty when the
// bridge has no confirmation service or the card could not be recorded.
func (b *Bridge) hold(ctx context.Context, r *run, tool Tool, callID string, args json.RawMessage, d Decision) string {
	if b.opt.Confirmations == nil {
		return ""
	}
	return b.opt.Confirmations.Hold(ctx, r.turn, tool, callID, args, d)
}

func refused(d Decision) wire.AgentToolResult {
	return wire.AgentToolResult{IsError: true, Text: fmt.Sprintf("Refused (%s): %s", d.Code, d.Reason)}
}

func (b *Bridge) execute(ctx context.Context, r *run, tool Tool, callID string, args json.RawMessage) wire.AgentToolResult {
	r.inflight.Add(1)
	defer r.inflight.Add(-1)
	cctx, cancel := context.WithTimeout(ctx, tool.Timeout)
	defer cancel()
	stop := context.AfterFunc(r.callCtx, cancel)
	defer stop()

	out, err := runBounded(cctx, tool, ToolCall{CallID: callID, Args: args, Turn: r.turn})
	if res, ok := demoRefusal(err); ok {
		out, err = res, nil
	}
	outside := tool.OutsideContent && !(err == nil && out.Clean) || err == nil && out.Outside
	if outside {
		r.turn.MarkOutside()
	}
	if err == nil {
		r.turn.Collect(out)
	}
	if out.Note != nil {
		b.opt.Guard.Note(ctx, r.turn, tool.Name, callID, args, *out.Note)
	}
	r.noteRun(out.RunID)
	outcome := "ok"
	text := out.Text
	switch {
	case err == nil:
	case errors.Is(r.callCtx.Err(), context.Canceled):
		outcome, text = "cancelled", "The call was cancelled."
	case errors.Is(cctx.Err(), context.DeadlineExceeded):
		outcome, text = "timeout", fmt.Sprintf("The tool did not finish within %s.", tool.Timeout)
	default:
		outcome, text = "error", "The tool failed: "+cutRunes(err.Error(), 300)
	}
	if outcome != "ok" && tool.Class == ClassDraft {
		// A draft that did not come back must not be replaced by one the model
		// writes itself; the answer says so even when the model does.
		text += " " + noStandIn
		r.turn.Collect(ToolResult{Limitations: []string{fmt.Sprintf("%s did not finish (%s), so nothing in this answer comes from it.", tool.Label, outcome)}})
	}
	if tool.Idempotent {
		if err := b.opt.Sessions.SettleClaim(context.WithoutCancel(ctx), tool.Name, callID, outcome); err != nil {
			b.opt.Log.Error("agent: claim not settled", "tool", tool.Name, "error", err.Error())
		}
	}
	d := delivery{tool: tool, callID: callID, args: args, ran: true, outside: outside, runID: out.RunID}
	if err == nil && out.Skill != "" {
		sk, ok := skillNamed(out.Skill)
		if active := b.opt.Registry.activeToolsOf(r.turn, sk); ok && len(active) > 0 {
			r.turn.LoadSkill(sk.Name)
			d.active, d.skill = active, skillPrompt(sk)
		} else {
			text = fmt.Sprintf("Refused: the %s skill's tools are not available in this conversation.", out.Skill)
		}
	}
	if err == nil && r.turn.TakesImages {
		d.images = out.Images
	}
	res := b.deliver(ctx, r, d, text, outcome != "ok")
	if err == nil && out.Final {
		res.FinalOnly = true
	}
	return res
}

// demoRefusal answers a call the demo workspace's gate refused (the demo never
// reads mail, for one) with the gate's reason. The gate refused before anything
// was read, so the result is clean and the call is not a failure; the gate
// wrote its own audit row.
func demoRefusal(err error) (ToolResult, bool) {
	var d *store.DemoRefusal
	if !errors.As(err, &d) {
		return ToolResult{}, false
	}
	return ToolResult{Text: "Refused: " + d.Decision.Reason + ". This is expected here; do not retry.", Clean: true}, true
}

// runBounded runs a tool and gives up when its context ends, even if the tool
// ignores the context: a call that cannot be stopped must still not hold the
// turn. The tool's goroutine is left to finish on its own and its result is
// dropped, and a panic in it is an error of the call.
func runBounded(ctx context.Context, tool Tool, call ToolCall) (ToolResult, error) {
	type outcome struct {
		res ToolResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- outcome{err: fmt.Errorf("the tool panicked: %v", p)}
			}
		}()
		res, err := tool.Run(ctx, call)
		done <- outcome{res, err}
	}()
	select {
	case o := <-done:
		return o.res, o.err
	case <-ctx.Done():
		select {
		case o := <-done:
			return o.res, o.err
		default:
			return ToolResult{}, ctx.Err()
		}
	}
}

// delivery is the call a result belongs to. ran says the tool's own code
// produced the text; a refusal or a hold is the system speaking.
type delivery struct {
	tool   Tool
	callID string
	args   json.RawMessage
	ran    bool
	// outside says the result holds text from outside the product.
	outside bool
	runID   string
	// images go to the model beside the text; the record never keeps them.
	images []Image
	// active and skill are set by a call that loaded a skill: the tools left
	// active and the block the extension adds to the turn.
	active []string
	skill  string
}

// deliver redacts a result, keeps the record's copy and cuts the model's. What
// a tool produced reaches the model inside the untrusted fence, and the record
// remembers whether it came from a tool that reads outside text.
func (b *Bridge) deliver(ctx context.Context, r *run, d delivery, text string, isErr bool) wire.AgentToolResult {
	text, rep := b.opt.Redactor.Redact(text)
	r.turn.ReportPaths(text)
	if rep.Total() > 0 {
		b.opt.Guard.Note(ctx, r.turn, d.tool.Name, d.callID, d.args, Note{Gate: GateResultRedaction, Verdict: gates.AllowWithTrace, Code: CodeRedacted,
			Reason: fmt.Sprintf("%d spans replaced (%s)", rep.Total(), rep)})
	}
	r.keepResult(d.callID, kept{cutBytes(text, sessions.MaxResultBytes), d.outside, d.runID})
	text = cutLines(text, ModelResultLimit)
	if d.ran {
		text = fenceText(d.tool.Name, text, false)
	}
	res := wire.AgentToolResult{IsError: isErr, Text: text, ActiveTools: d.active, Skill: d.skill}
	for _, im := range d.images {
		res.Images = append(res.Images, wire.AgentToolImage{MimeType: im.MIMEType, Data: base64.StdEncoding.EncodeToString(im.Data)})
	}
	return res
}

// auditUnknown records a call to a name outside the catalog that pi answered
// itself, seen from its tool_execution_start. It counts toward the call limit.
func (b *Bridge) auditUnknown(ctx context.Context, r *run, name, callID string, args json.RawMessage) {
	r.calls.Add(1)
	b.opt.Guard.Deny(ctx, r.turn, name, callID, args, GatePolicyFloor, CodeUnknownTool, "no such tool is registered for this turn")
}

// Disclose applies the disclosure gate to an answer envelope. A block returns
// the fixed notice, and only the rule and the category are audited.
func (b *Bridge) Disclose(ctx context.Context, t *Turn, e Envelope) (Envelope, bool) {
	audit := func(ctx context.Context, row gates.AuditRow) error {
		err := b.opt.Sessions.AppendDecision(ctx, row)
		b.note(err, "disclosure audit not written")
		return err
	}
	return b.opt.Disclosure.Apply(ctx, t, e, audit, b.opt.Salt, time.Now().UnixMilli())
}

// recorded is the part of an envelope the conversation record keeps with the
// answer, so a reopened conversation shows the sources the stream carried.
func recorded(e Envelope) sessions.Envelope {
	out := sessions.Envelope{Limitations: e.Limitations, NextAction: e.NextAction}
	for _, c := range e.Citations {
		out.Citations = append(out.Citations, sessions.Citation{Label: c.Label, Snippet: c.Snippet, URL: c.URL})
	}
	return out
}

// finalEvent is the stream's final event for an envelope.
func finalEvent(turnID string, e Envelope) wire.ChatEvent {
	ev := wire.ChatEvent{Type: wire.ChatFinal, TurnID: turnID, Text: e.Text, Limitations: e.Limitations, NextAction: e.NextAction}
	for _, c := range e.Citations {
		ev.Citations = append(ev.Citations, wire.ChatCitation{Label: c.Label, Snippet: c.Snippet, URL: c.URL})
	}
	return ev
}
