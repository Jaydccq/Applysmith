package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"applysmith/internal/agent/approvals"
	"applysmith/internal/agent/sessions"
	"applysmith/internal/gates"
	"applysmith/internal/lazyregexp"
)

// Gates of the chain, in the order they run. The apply-safety gates keep their
// own names.
const (
	GatePolicyFloor     gates.Gate = "policy_floor"
	GateChannelEnvelope gates.Gate = "channel_envelope"
	GateApproval        gates.Gate = "approval_requirement"
	GateConfirmation    gates.Gate = "action_confirmation"
	GateIdempotency     gates.Gate = "idempotency_claim"
)

// Reason codes of the chain's own gates.
const (
	CodeOK                = "ok"
	CodeForbiddenTool     = "forbidden_tool"
	CodeUndeclaredClass   = "undeclared_class"
	CodeIrreversibleWrite = "irreversible_write"
	CodeUnknownTool       = "unknown_tool"
	CodeChannelEnvelope   = "channel_envelope"
	CodeApprovalRequired  = "approval_required"
	CodeIntentNotStated   = "intent_not_stated"
	CodeIntentStated      = "intent_stated"
	CodeActionNotNamed    = "action_not_named"
	CodeActionNamed       = "action_named"
	CodeOutsideContent    = "outside_content_read"
	CodeOutsideAllowed    = "outside_content_allowed"
	CodeReadOnly          = "read_tier"
	CodeDraftTier         = "draft_tier"
	CodeSubmitTier        = "submit_tier"
	CodeIdempotencyClaim  = "idempotency_claimed"
	CodeGateError         = "gate_error"
	CodeInvalidArguments  = "invalid_arguments"
	CodeToolLimit         = "tool_limit"
	CodeRepeatedCall      = "repeated_call"
	CodeObjectNotNamed    = "object_not_named"
	CodeBulkRemoval       = "bulk_removal"
	CodeOperateAsked      = "operate_asked"
	CodeTaskRun           = "task_run"
)

// Decision is the chain's answer for one call.
type Decision struct {
	Verdict gates.Verdict // Allow, AllowWithTrace, Hold or Deny
	Gate    gates.Gate
	Code    string
	Reason  string
	Tier    int
}

// Allowed reports whether the call may run.
func (d Decision) Allowed() bool {
	return d.Verdict == gates.Allow || d.Verdict == gates.AllowWithTrace
}

// Grants is where standing approvals plug into the chain. A grant can lift a
// hold of tier 2 or 3 that came from missing intent or from taint, and nothing
// else; it is not consulted by the other gates. The zero chain has none.
type Grants interface {
	Covers(ctx context.Context, t *Turn, tool Tool, args json.RawMessage) (id string, ok bool)
}

// ApplyList returns the canonical URLs of today's apply list; an unreadable
// list is an error and then vouches for nothing.
type ApplyList func(ctx context.Context) ([]string, error)

// GuardOptions wires the chain to its inputs.
type GuardOptions struct {
	Sessions  *sessions.Store
	Now       func() time.Time
	Log       *slog.Logger
	Salt      string
	ApplyList ApplyList
	Grants    Grants
}

// Guard is the chain every tool call passes, whoever chose it: the policy
// floor, the channel envelope, the approval requirement, the action
// confirmation, the submit gate, then the idempotency claim. The disclosure
// gate on final answers is applied where answers are delivered.
type Guard struct {
	reg  *Registry
	opt  GuardOptions
	step []guardStep
}

type guardStep struct {
	gate gates.Gate
	run  func(context.Context, *callState) (gates.Decision, error)
}

// callState is one call walking the chain.
type callState struct {
	turn   *Turn
	tool   Tool
	callID string
	args   json.RawMessage
	submit bool
	tier   int
	conf   *Confirmation
}

// Confirmation is the user's approval of a held call: the card they approved
// and who approved it. It lifts only holds; a refusal of any gate stands, and
// so does every gate that would not have held the call.
type Confirmation struct {
	CardID string
	Actor  approvals.Actor
}

func (c *Confirmation) code() string { return "confirmed:" + c.CardID }

// NewGuard builds the chain over a registry.
func NewGuard(reg *Registry, opt GuardOptions) *Guard {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Log == nil {
		opt.Log = slog.New(slog.DiscardHandler)
	}
	g := &Guard{reg: reg, opt: opt}
	g.step = []guardStep{
		{GatePolicyFloor, g.policyFloor},
		{GateChannelEnvelope, g.channelEnvelope},
		{GateApproval, g.approval},
		{GateConfirmation, g.confirmation},
		{gates.GateAgentSubmit, g.submitGate},
		{GateIdempotency, g.idempotency},
	}
	return g
}

// Decide walks a call through the chain and audits each decision. args are the
// validated arguments with the reserved step already removed. A step that
// errors denies the call with reason gate_error.
func (g *Guard) Decide(ctx context.Context, t *Turn, tool Tool, callID string, args json.RawMessage) Decision {
	return g.decide(ctx, t, tool, callID, args, nil)
}

// DecideConfirmed is Decide for the single re-run of a call whose card the
// user approved. The whole chain runs again.
func (g *Guard) DecideConfirmed(ctx context.Context, t *Turn, tool Tool, callID string, args json.RawMessage, conf Confirmation) Decision {
	return g.decide(ctx, t, tool, callID, args, &conf)
}

func (g *Guard) decide(ctx context.Context, t *Turn, tool Tool, callID string, args json.RawMessage, conf *Confirmation) Decision {
	cs := &callState{turn: t, tool: tool, callID: callID, args: args, tier: tierOf(tool, args), conf: conf}
	cs.submit = cs.tier == 4
	final := Decision{Verdict: gates.Allow, Gate: GateIdempotency, Code: CodeOK, Reason: "every gate allowed the call", Tier: cs.tier}
	traced := false
	for _, s := range g.step {
		d, err := s.run(ctx, cs)
		if err != nil {
			d = gates.Decision{Gate: s.gate, Verdict: gates.Deny, Code: CodeGateError, Reason: "a gate failed, so the call is refused"}
			g.opt.Log.Error("agent: gate error", "gate", string(s.gate), "tool", tool.Name, "error", err.Error())
		}
		if d.Gate == "" {
			d.Gate = s.gate
		}
		out := Decision{Verdict: d.Verdict, Gate: d.Gate, Code: d.Code, Reason: d.Reason, Tier: cs.tier}
		if err := g.record(ctx, cs, out); err != nil {
			g.opt.Log.Error("agent: audit write failed", "tool", tool.Name, "error", err.Error())
			return Decision{Verdict: gates.Deny, Gate: s.gate, Code: CodeGateError, Reason: "the decision could not be recorded, so the call is refused", Tier: cs.tier}
		}
		if !out.Allowed() {
			return out
		}
		// The caller wants to know why the call was allowed: the confirmation
		// step says so, and for a submit the submit gate does.
		if s.gate == GateConfirmation || (s.gate == gates.GateAgentSubmit && cs.submit) {
			final = out
		}
		traced = traced || out.Verdict == gates.AllowWithTrace
	}
	final.Verdict = gates.Allow
	if traced {
		final.Verdict = gates.AllowWithTrace
	}
	return final
}

// Deny audits and returns a refusal decided outside the chain, such as an
// unknown tool name or a call over the tool limit.
func (g *Guard) Deny(ctx context.Context, t *Turn, name, callID string, args json.RawMessage, gate gates.Gate, code, reason string) Decision {
	cs := &callState{turn: t, tool: Tool{Name: name}, callID: callID, args: args, tier: -1}
	d := Decision{Verdict: gates.Deny, Gate: gate, Code: code, Reason: reason, Tier: -1}
	if err := g.record(ctx, cs, d); err != nil {
		g.opt.Log.Error("agent: audit write failed", "tool", name, "error", err.Error())
	}
	return d
}

// Gates and codes of the events a call records outside the chain.
const (
	GateResultRedaction gates.Gate = "result_redaction"
	GatePageRead        gates.Gate = "page_read"
	CodeRedacted                   = "redacted"
	// CodeToolRefused is a held call its tool would refuse, answered without
	// a card.
	CodeToolRefused = "tool_refused"
)

// Note is an audited event of a call that is not a chain decision: the span
// redaction of a result, or the refusal of a destination by a tool. Target is
// a URL whose host and path the row keeps; the caller reduces it first.
type Note struct {
	Gate    gates.Gate
	Verdict gates.Verdict
	Code    string
	Reason  string
	Target  string
}

// Note writes one audit row for an event of a call. It is written whatever the
// turn has recorded before: each call has its own.
func (g *Guard) Note(ctx context.Context, t *Turn, toolName, callID string, args json.RawMessage, n Note) {
	gd := gates.Decision{Gate: n.Gate, Verdict: n.Verdict, Code: n.Code, Reason: n.Reason}
	row := gates.NewAuditRow(g.opt.Now().UnixMilli(), gd, gates.AuditContext{
		Tool: toolName, TurnID: t.ID, ConversationID: t.SessionID, Caller: t.Channel.caller(),
		ArgFingerprint: argHash(args), Salt: g.opt.Salt, UserMessage: t.UserMessage, TargetURL: n.Target,
	})
	row.Verdict = n.Verdict
	if err := g.opt.Sessions.AppendDecision(context.WithoutCancel(ctx), row); err != nil {
		g.opt.Log.Error("agent: audit write failed", "tool", toolName, "call", callID, "error", err.Error())
	}
}

// record writes one decision, once per distinct combination of gate, tool,
// argument fingerprint and verdict within the turn.
func (g *Guard) record(ctx context.Context, cs *callState, d Decision) error {
	tier := ""
	if d.Tier >= 0 {
		tier = fmt.Sprint(d.Tier)
	}
	gd := gates.Decision{Gate: d.Gate, Verdict: d.Verdict, Code: d.Code, Reason: d.Reason}
	t := cs.turn
	row := gates.NewAuditRow(g.opt.Now().UnixMilli(), gd, gates.AuditContext{
		Tool: cs.tool.Name, TurnID: t.ID, ConversationID: t.SessionID, RiskTier: tier, Caller: t.Channel.caller(),
		ArgFingerprint: argHash(cs.args), Salt: g.opt.Salt, UserMessage: t.UserMessage, TargetURL: auditTarget(cs.tool.Name, cs.args),
	})
	row.Verdict = d.Verdict
	if !t.admit(row) {
		return nil
	}
	return g.opt.Sessions.AppendDecision(ctx, row)
}

func argHash(args json.RawMessage) string {
	sum := sha256.Sum256([]byte(canonicalArgs(args)))
	return hex.EncodeToString(sum[:])[:16]
}

// tierOf gives a call its risk tier: 0 read, 1 draft, 2 write-local, 3
// external action, 4 a call with submit true.
func tierOf(tool Tool, args json.RawMessage) int {
	if a, ok := readActions[tool.Name]; ok && argString(args, "action") == a {
		return 0
	}
	if a, ok := draftActions[tool.Name]; ok && argString(args, "action") == a {
		return 1
	}
	if f, ok := draftFlags[tool.Name]; ok && argTrue(args, f) {
		return 1
	}
	switch tool.Class {
	case ClassRead:
		return 0
	case ClassDraft:
		return 1
	case ClassWriteLocal:
		return 2
	}
	if submitTrue(args) {
		return 4
	}
	return 3
}

// outsideAllowed are the tier 3 actions that run after outside content was read
// when the user's message names them: a fill that stops at Submit. The owner
// chose this because a card per posting stalled the fills handed to the agent
// (docs/architecture/ask-agent.md); a submit still follows the submit gate, and
// the other actions are still held.
var outsideAllowed = map[string]bool{"autofill_application": true, "apply_from_list": true}

// readActions are the actions of a write-local tool that only read; a call
// with one is tier 0.
var readActions = map[string]string{"batch_skip_list": "list"}

// draftActions are the actions of a write-local tool that only propose: a
// created scheduled prompt runs nothing until the user confirms it in the
// dashboard, so a call with one is tier 1, like propose_schedule_change.
var draftActions = map[string]string{"manage_scheduled_prompt": "create"}

// draftFlags are the arguments that make a call of a read tool a draft: with
// generate true gmail_digest asks a model for a fresh digest and stores it,
// sending it nowhere, so that call is tier 1.
var draftFlags = map[string]string{"gmail_digest": "generate"}

func argTrue(args json.RawMessage, key string) bool {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(args, &m)
	return string(m[key]) == "true"
}

func argString(args json.RawMessage, key string) string {
	var a map[string]any
	_ = json.Unmarshal(args, &a)
	s, _ := a[key].(string)
	return strings.TrimSpace(s)
}

func submitTrue(args json.RawMessage) bool {
	var a struct {
		Submit bool `json:"submit"`
	}
	_ = json.Unmarshal(args, &a)
	return a.Submit
}

// auditTarget is the URL a call's audit rows keep. A page the model reads is
// recorded by its host alone, because its path and query can carry anything.
func auditTarget(tool string, args json.RawMessage) string {
	u := postingURL(args)
	if tool != ToolReadPage {
		return u
	}
	return hostOnly(u)
}

func postingURL(args json.RawMessage) string {
	var a struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(args, &a)
	return a.URL
}

var irreversibleName = lazyregexp.New(`(^|_)(delete|drop|wipe|purge|erase|destroy|format|reset)_(database|db|workspace|everything|all_data)($|_)`)

func (g *Guard) policyFloor(_ context.Context, c *callState) (gates.Decision, error) {
	low := strings.ToLower(c.tool.Name)
	switch {
	case !c.tool.Class.valid():
		return deny(GatePolicyFloor, CodeUndeclaredClass, "the tool has no declared permission class"), nil
	case !gates.SubmitRegistration(c.tool.Name, c.tool.DeclaresSubmit()).Allowed() || slices.Contains(forbiddenNames, low):
		return deny(GatePolicyFloor, CodeForbiddenTool, "the tool name is forbidden"), nil
	case irreversibleName.MatchString(low):
		return deny(GatePolicyFloor, CodeIrreversibleWrite, "an irreversible local write is never allowed"), nil
	}
	return allow(GatePolicyFloor, CodeOK, "the tool is permitted by the policy floor"), nil
}

func (g *Guard) channelEnvelope(_ context.Context, c *callState) (gates.Decision, error) {
	if c.turn.Channel == ChannelTelegram && isApplyTool(c.tool.Name) {
		d := gates.SubmitAuthorization(gates.SubmitAuthInput{Caller: gates.CallerTelegram, Tool: c.tool.Name, Submit: c.submit})
		return gates.Decision{Gate: GateChannelEnvelope, Verdict: d.Verdict, Code: d.Code, Reason: d.Reason}, nil
	}
	if !c.turn.Channel.allows(c.tool.Class) {
		return deny(GateChannelEnvelope, CodeChannelEnvelope, fmt.Sprintf("a %s turn may not use %s tools", c.turn.Channel, c.tool.Class)), nil
	}
	if !c.turn.permits(c.tool) {
		return deny(GateChannelEnvelope, CodeChannelEnvelope, fmt.Sprintf("this %s turn is not offered %s", c.turn.Channel, c.tool.Name)), nil
	}
	return allow(GateChannelEnvelope, CodeOK, fmt.Sprintf("the %s envelope contains %s tools", c.turn.Channel, c.tool.Class)), nil
}

func (g *Guard) approval(ctx context.Context, c *callState) (gates.Decision, error) {
	if c.tool.RequiresApproval {
		if c.conf != nil {
			return allowTrace(GateApproval, c.conf.code(), "the user approved this call on its confirmation card"), nil
		}
		return hold(GateApproval, CodeApprovalRequired, "this tool needs a recorded approval of the user, and none covers the call"), nil
	}
	return allow(GateApproval, CodeOK, "the tool needs no approval"), nil
}

// confirmation assigns the tier and decides from the user's message, the tool
// and its arguments, the tier and the turn's source trust. It never reads text
// the model wrote.
func (g *Guard) confirmation(ctx context.Context, c *callState) (gates.Decision, error) {
	t := c.turn
	switch c.tier {
	case 0:
		return allow(GateConfirmation, CodeReadOnly, "a read changes nothing"), nil
	case 1:
		return allowTrace(GateConfirmation, CodeDraftTier, "a draft has no effect until the user uses it"), nil
	case 4:
		return allow(GateConfirmation, CodeSubmitTier, "a call with submit follows the submit gate"), nil
	}
	if c.conf != nil {
		return allowTrace(GateConfirmation, c.conf.code(), "the user approved this call on its confirmation card"), nil
	}
	fromUser := t.Origin != OriginModel
	if c.tier == 2 {
		switch {
		case fromUser:
		case c.tool.Name == ToolDelegateTask:
			if !t.names(writeVerbs) && !t.names(actionVerbs) && !t.names(finishVerbs) && !t.names(delegateVerbs) && !t.names(searchVerbs) {
				return g.holdOrGrant(ctx, c, CodeIntentNotStated, "the user's message does not hand this outcome to the agent"), nil
			}
		case c.tool.Name == ToolApplyLeave && t.Task != "" && slices.Contains(t.TaskRuns, argString(c.args, "run_id")):
			return allowTrace(GateConfirmation, CodeTaskRun, "the run belongs to the task the user delegated, and handing it back leaves its page as it is"), nil
		case !t.names(writeVerbs):
			return g.holdOrGrant(ctx, c, CodeIntentNotStated, "the user's message does not ask for this change"), nil
		default:
			if d, held := objectHold(c); held {
				return d, nil
			}
		}
		return allowTrace(GateConfirmation, CodeIntentStated, "the user's message states the intent to change local data"), nil
	}
	if operates[c.tool.Name] {
		return g.operate(ctx, c, fromUser), nil
	}
	if !fromUser && !t.names(actionVerbs) && !(c.tool.Name == "scan_portal" && t.names(searchVerbs)) {
		return g.holdOrGrant(ctx, c, CodeActionNotNamed, "the user's message does not name this action"), nil
	}
	if t.Untrusted() {
		if outsideAllowed[c.tool.Name] {
			return allowTrace(GateConfirmation, CodeOutsideAllowed, "outside content was read, and a fill stops before Submit"), nil
		}
		return g.holdOrGrant(ctx, c, CodeOutsideContent, "outside content was read, which could have chosen this action"), nil
	}
	return allowTrace(GateConfirmation, CodeActionNamed, "the user's message names the action and no outside content was read"), nil
}

// operates are the computer-use actions: the desktop actions, the answer to a
// stopped run's question and the apply tools that work its kept tab.
var operates = map[string]bool{ToolDesktopClick: true, ToolDesktopType: true, ToolDesktopKeys: true, ToolDesktopScroll: true, ToolAnswerRunQuestion: true,
	ToolApplyFill: true, ToolApplyAnswer: true, ToolApplyPress: true, ToolApplyContinue: true}

// applyRunActs are the apply tools that change a run's page or carry it on; a
// finish verb beside the user's naming of the run asks for them.
var applyRunActs = map[string]bool{ToolApplyFill: true, ToolApplyAnswer: true, ToolApplyPress: true, ToolApplyContinue: true}

// operate decides a computer-use action. Its own gate bounds what it does (the
// desktop_action gate, the answer's provenance), so outside content read in the
// turn, a look at a window among it, does not hold it (AC-3, AC-4). The user
// must still have asked: with the button, with an operate verb in their own
// words, or, for an answer, by writing it. A desktop action they did not ask
// for is refused rather than held, because a card approved later would find
// its look stale.
func (g *Guard) operate(ctx context.Context, c *callState, fromUser bool) gates.Decision {
	t := c.turn
	answer := c.tool.Name == ToolAnswerRunQuestion
	switch {
	case (answer || applyRunActs[c.tool.Name]) && t.Task != "" && slices.Contains(t.TaskRuns, argString(c.args, "run_id")):
		return allowTrace(GateConfirmation, CodeTaskRun, "the run belongs to the task the user delegated, and the tool's own gate bounds what it does")
	case fromUser || t.names(operateVerbs) || answer && inOwnWords(t.typedText(), argString(c.args, "answer")) || applyRunActs[c.tool.Name] && t.names(finishVerbs):
		return allowTrace(GateConfirmation, CodeOperateAsked, "the user asked for this, and the tool's own gate bounds what it does")
	case answer:
		return g.holdOrGrant(ctx, c, CodeActionNotNamed, "the user's message does not ask to answer the run's questions")
	}
	return deny(GateConfirmation, CodeActionNotNamed, "the user's message does not ask the agent to operate a window; ask them what to do")
}

// writeObjects are the write tools whose object the user's own words must name
// beside a write verb, so a verb aimed elsewhere ("unblock all companies and
// clear my tracker") never reaches a protective setting or a deletion.
var writeObjects = map[string][]string{
	"batch_skip_list": {"skip list", "skiplist", "skip lists",
		"\u8df3\u8fc7\u540d\u5355", "\u8df3\u8fc7\u5217\u8868", "\u8df3\u8fc7\u6e05\u5355"}, // skip list (three wordings)
	"forget_fact": {"forget", "memory", "memories", "fact", "facts", "remembered",
		"\u5fd8", "\u8bb0\u5fc6", "\u8bb0\u4f4f"}, // forget, memory, remember
}

// bulkWords make a skip-list removal one of many.
var bulkWords = []string{"all", "every", "everything", "everyone", "entire", "whole", "clear", "empty", "reset",
	"\u5168\u90e8", "\u6240\u6709", "\u6e05\u7a7a", "\u90fd"} // all, every, clear out, all of them

// objectHold holds a write whose object the user's own words do not name. A
// skip-list removal also needs the company named, and a bulk removal (a word
// of bulkWords, or a second removal in the turn) is confirmed card by card.
func objectHold(c *callState) (gates.Decision, bool) {
	objects, ok := writeObjects[c.tool.Name]
	if !ok {
		return gates.Decision{}, false
	}
	own := c.turn.ownText()
	if !namesAny(own, objects) {
		return hold(GateConfirmation, CodeObjectNotNamed, "the user's message does not name what this call changes"), true
	}
	if c.tool.Name != "batch_skip_list" || argString(c.args, "action") != "remove" {
		return gates.Decision{}, false
	}
	switch {
	case namesAny(own, bulkWords):
		return hold(GateConfirmation, CodeBulkRemoval, "a removal from the skip list in bulk is confirmed one company at a time"), true
	case !namesAny(own, []string{argString(c.args, "company")}):
		return hold(GateConfirmation, CodeObjectNotNamed, "the user's message does not name this company"), true
	case c.turn.countRemoval() > 0:
		return hold(GateConfirmation, CodeBulkRemoval, "a second removal from the skip list in one turn is confirmed on its own card"), true
	}
	return gates.Decision{}, false
}

// namesAny reports whether text names a phrase: English as whole words outside
// URLs, ignoring case; Chinese anywhere.
func namesAny(text string, phrases []string) bool {
	norm := " " + strings.Join(wordsOf(urlText.ReplaceAllString(text, " ")), " ") + " "
	for _, p := range phrases {
		if hasHan(p) && strings.Contains(text, p) {
			return true
		}
		if w := wordsOf(p); len(w) > 0 && !hasHan(p) && strings.Contains(norm, " "+strings.Join(w, " ")+" ") {
			return true
		}
	}
	return false
}

// holdOrGrant holds the call unless a standing approval covers it exactly.
func (g *Guard) holdOrGrant(ctx context.Context, c *callState, code, reason string) gates.Decision {
	if g.opt.Grants != nil {
		if id, ok := g.opt.Grants.Covers(ctx, c.turn, c.tool, c.args); ok {
			return allowTrace(GateConfirmation, "grant:"+id, "a standing approval covers this call")
		}
	}
	return hold(GateConfirmation, code, reason)
}

// submitGate applies to the two apply tools with submit true: the explicit
// request and the attended caller first, then the posting and the turn's trust.
func (g *Guard) submitGate(ctx context.Context, c *callState) (gates.Decision, error) {
	if !c.submit {
		return allow(gates.GateAgentSubmit, CodeOK, "the call does not submit"), nil
	}
	t := c.turn
	explicit := t.explicitApply() && !t.forbidsSubmit()
	d := gates.SubmitAuthorization(gates.SubmitAuthInput{
		Caller: t.Channel.caller(), Tool: c.tool.Name, Submit: true, ExplicitRequest: explicit,
		Unattended: t.Channel == ChannelScheduled || t.Channel == ChannelDigest,
	})
	if !d.Allowed() {
		return d, nil
	}
	var list []string
	if g.opt.ApplyList != nil && c.tool.Name == gates.ToolAutofillApplication {
		var err error
		if list, err = g.opt.ApplyList(ctx); err != nil {
			list = nil // an unreadable list vouches for nothing
		}
	}
	d = gates.AgentSubmit(gates.AgentSubmitInput{
		Caller: t.Channel.caller(), Tool: c.tool.Name, PostingURL: postingURL(c.args), UserMessage: t.ownText(),
		OutsideContentRead: t.Untrusted(), ListURLs: list,
	})
	if d.Verdict != gates.Hold || c.conf == nil {
		return d, nil
	}
	// The card is the one door for a held submit, and only a dashboard session
	// opens it: a remote device, Telegram and a local token do not.
	caller := gates.CallerAskAgent
	if c.conf.Actor == approvals.ActorTelegram {
		caller = gates.CallerTelegram
	}
	lift := gates.HeldSubmitConfirmation(gates.ConfirmationInput{
		Caller: caller, FromRemoteDevice: c.conf.Actor == approvals.ActorRemoteDevice, NewUserMessage: c.conf.Actor == approvals.ActorDashboard,
	})
	if !lift.Allowed() {
		return d, nil
	}
	return gates.Decision{Gate: gates.GateAgentSubmit, Verdict: gates.AllowWithTrace, Code: c.conf.code(), Reason: "the user confirmed the held submit on its dashboard card"}, nil
}

func (g *Guard) idempotency(ctx context.Context, c *callState) (gates.Decision, error) {
	if !c.tool.Idempotent {
		return allow(GateIdempotency, CodeOK, "the tool takes no idempotency key"), nil
	}
	fresh, err := g.opt.Sessions.Claim(ctx, c.tool.Name, c.callID)
	if err != nil {
		return gates.Decision{}, err
	}
	if !fresh {
		return deny(GateIdempotency, CodeIdempotencyClaim, "this tool call id was already claimed; a new attempt needs a new call"), nil
	}
	return allow(GateIdempotency, CodeOK, "the call id was claimed"), nil
}

func isApplyTool(name string) bool {
	return name == gates.ToolAutofillApplication || name == gates.ToolApplyFromList
}

func allow(g gates.Gate, code, reason string) gates.Decision {
	return gates.Decision{Gate: g, Verdict: gates.Allow, Code: code, Reason: reason}
}

func allowTrace(g gates.Gate, code, reason string) gates.Decision {
	return gates.Decision{Gate: g, Verdict: gates.AllowWithTrace, Code: code, Reason: reason}
}

func hold(g gates.Gate, code, reason string) gates.Decision {
	return gates.Decision{Gate: g, Verdict: gates.Hold, Code: code, Reason: reason}
}

func deny(g gates.Gate, code, reason string) gates.Decision {
	return gates.Decision{Gate: g, Verdict: gates.Deny, Code: code, Reason: reason}
}
