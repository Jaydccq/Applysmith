package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
)

// AgentTask is the routing task of the Ask Agent's model loop, and the row of
// a heavy turn.
const AgentTask = "agent-kernel"

// AgentLightTask is the row of a turn the intent classifier called light.
// Routing it turns intent routing on; IntentTask is the classifier's own row.
const (
	AgentLightTask = "agent-light"
	IntentTask     = "agent-intent"
)

// agentRow reports whether a task is one of the two rows that serve agent turns.
func agentRow(task string) bool { return task == AgentTask || task == AgentLightTask }

// LedgerKindPi is the provider kind of ledger rows written for agent turns,
// whose model calls are made by the pi runtime rather than by this package.
const LedgerKindPi = "pi"

// PiLoginPrefix starts the provider id of an agent-kernel row that the pi
// runtime serves with a login of its own (a subscription), as in
// pi:openai-codex. No other task may use such a provider, and the system's
// vault holds no key for it.
const PiLoginPrefix = "pi:"

var piLoginID = lazyregexp.New(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// PiLogin reads a provider id of the form pi:<pi provider id>.
func PiLogin(providerID string) (piProvider string, ok bool) {
	id, found := strings.CutPrefix(providerID, PiLoginPrefix)
	if !found || !piLoginID.MatchString(id) {
		return "", false
	}
	return id, true
}

// AgentRoute is the row resolved for one turn: what the pi
// runtime is launched with. Its model calls come to the gateway
// (ServeAgentCall), which holds the keys; only a pi login is called by pi.
type AgentRoute struct {
	Resolved
	Config ProviderConfig
	// PiLogin is pi's own provider id when the row names a pi login; the route
	// then has no key and is billed as a subscription.
	PiLogin string
	// Heads is the first candidate of the row on each wire, resting wires last.
	// A row with more than one wire is routed by pi (pi_extension.mjs): the
	// gateway moves a call only among the candidates of its own wire.
	Heads []AgentHead
}

// AgentHead is the model pi is declared with for one wire: the row's first
// candidate of that wire.
type AgentHead struct {
	Wire   string
	Config ProviderConfig
	Model  string
}

// imageModels are the models known to take images beside text, by their id
// without a provider prefix (OpenRouter's anthropic/claude-sonnet-5-5 is
// claude-sonnet-5-5). A model absent here is given text only: one wrongly
// listed fails every request that carries an image, while one wrongly left
// out only loses the screenshot. docs/architecture/llm.md records the source.
var imageModels = map[string]bool{
	"claude-haiku-5-5": true, "claude-sonnet-5-5": true, "claude-opus-5-5": true,
	"gpt-6-luna": true, "gpt-6.1-sol": true,
	"gemini-3.5-flash-lite": true, "gemini-3.8-flash": true,
	"MiniMax-M3.1-Flash": true, "glm-5.3-flash": true, "grok-4.7": true,
}

// TakesImages reports whether a model takes images beside text (AC-3).
func TakesImages(model string) bool {
	return imageModels[model[strings.LastIndex(model, "/")+1:]]
}

// ErrAgentOffline is AgentRoute's answer while the offline simulation is on:
// it stands in for the router's own calls, and the agent runtime, which calls
// a provider itself, has nothing to run on.
var ErrAgentOffline = errors.New("the offline simulation cannot serve the agent runtime")

// AgentRoute resolves an agent row (AgentTask or AgentLightTask) and checks
// that its primary is one the gateway can call: an API kind with a stored key.
func (r *Router) AgentRoute(ctx context.Context, task string) (AgentRoute, error) {
	if !agentRow(task) {
		return AgentRoute{}, &Error{Kind: KindUnknownTask, Task: task, Message: "not an agent task"}
	}
	s, err := LoadSettings(ctx, r.store)
	if err != nil {
		return AgentRoute{}, err
	}
	if r.offline || s.Offline {
		return AgentRoute{}, ErrAgentOffline
	}
	res, err := r.resolveWith(s, task)
	if err != nil {
		return AgentRoute{}, err
	}
	if login, ok := PiLogin(res.Provider); ok {
		return AgentRoute{Resolved: res, Config: ProviderConfig{ID: res.Provider, Kind: LedgerKindPi}, PiLogin: login}, nil
	}
	heads := r.agentHeads(ctx, s, res)
	if res.Provider == GroupProvider {
		// pi is launched with the group's first listed member; the gateway
		// serves each call from the members of that member's wire kind.
		head := s.head(res)
		res.Provider, res.Model = head.Provider, head.Model
	}
	prov, ok := s.Provider(res.Provider)
	if !ok || !agentKind(prov.Kind) {
		return AgentRoute{}, &Error{Kind: KindUnconfigured, Task: task,
			Message: fmt.Sprintf("the %s route names provider %s, which the agent runtime cannot call; route it to an API provider with `applysmith llm configure`", task, res.Provider)}
	}
	// The plan has no stored key: the token is read for each call, and a
	// sign-in that is missing fails that call, which pi's routing moves on.
	if prov.Kind != KindChatGPT {
		if _, err := r.apiKey(prov.ID); err != nil {
			return AgentRoute{}, err
		}
	}
	return AgentRoute{Resolved: res, Config: prov, Heads: heads}, nil
}

// agentKind reports whether the gateway can call a provider kind.
func agentKind(kind string) bool {
	return kind == KindOpenAI || kind == KindAnthropic || kind == KindChatGPT
}

// agentHeads lists the row's first candidate on each wire, in the order the
// rests leave the row: a wire whose first candidate rests comes after the
// others.
func (r *Router) agentHeads(ctx context.Context, s Settings, res Resolved) []AgentHead {
	listed, _ := r.callCandidates(ctx, s, res, false)
	var cands []candidate
	for _, f := range listed {
		if p, ok := s.Provider(f.Provider); ok && agentKind(p.Kind) {
			cands = append(cands, candidate{provider: p, model: f.Model})
		}
	}
	var heads []AgentHead
	seen := map[string]bool{}
	for _, c := range r.restOrder(cands) {
		if w := AgentWire(c.provider); !seen[w] {
			seen[w] = true
			heads = append(heads, AgentHead{Wire: w, Config: c.provider, Model: c.model})
		}
	}
	return heads
}

// DailySpend returns the local day's metered spend and the daily cap, reusing a
// read for 15 seconds. A cap of zero means the cap is off.
func (r *Router) DailySpend(ctx context.Context) (spend, capUSD float64) {
	d := r.spend(ctx, false)
	return d.total, d.cap
}

// AgentUsage is one model response of an agent turn on a pi login, as the pi
// runtime reported it.
type AgentUsage struct {
	StartedAt    time.Time
	SessionID    string
	Outcome      string // ok, truncated, aborted or error
	Usage        Usage
	Latency      time.Duration
	TTFT         *time.Duration
	FinishReason string
	ErrorText    string
	Estimated    bool
}

// RecordAgentUsage writes the ledger row of one response pi made on its own
// login, a subscription that costs zero; the gateway writes the rows of every
// other agent call itself. A failed write is logged and never reaches the turn.
func (r *Router) RecordAgentUsage(ctx context.Context, route AgentRoute, u AgentUsage) {
	zero := 0.0
	row := LedgerRow{
		CallID: newCallID(), StartedAt: u.StartedAt, Task: AgentTask, ProviderID: route.Config.ID, ProviderKind: LedgerKindPi,
		Model: route.Model, Attempt: 1, CandidateSource: "primary", Outcome: u.Outcome, Usage: u.Usage, CostUSD: &zero,
		Billing: BillingSubscription, Latency: u.Latency, TTFT: u.TTFT, FinishReason: u.FinishReason, RunID: u.SessionID, ErrorText: u.ErrorText,
	}
	if u.Estimated {
		row.ErrorText = trimJoin(row.ErrorText, "(usage estimated from text length)")
	}
	r.writeLedger(ctx, row, nil, false)
}

func trimJoin(a, b string) string {
	if a == "" {
		return b
	}
	return a + " " + b
}
