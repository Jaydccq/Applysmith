package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Request is one completion call.
type Request struct {
	// Task is one of the registry task ids.
	Task   string
	System string
	Prompt string
	// MaxOutputTokens is the caller's ceiling; with thinking on it is raised to
	// the row's ceiling. Zero means the row's ceiling.
	MaxOutputTokens int
	// Model pins the call to one "provider/model": the only candidate, never
	// downgraded, still recorded under its task.
	Model string
	// Deadline is the caller's job budget; the zero value means none.
	Deadline time.Time
	// Timeout replaces the row's attempt timeout when set.
	Timeout time.Duration
	// RunID ties ledger rows to the run or session that made the call.
	RunID string
	// WebSearch asks the provider to search the web before it answers and to
	// return the pages it used, at most this many; zero is no search. Only a
	// candidate that offers CapWebSearch serves such a call.
	WebSearch int
	// OnDelta receives streamed text. Once a delta was delivered, a failure is
	// returned instead of walking on.
	OnDelta func(string)

	// pin restricts the call to one candidate (the structured-output repair).
	pin *Fallback
	// callID and attemptBase continue an earlier call: the repair is the same
	// call in the ledger, numbered after the attempts already made.
	callID      string
	attemptBase int
}

// Response is a successful completion.
type Response struct {
	Text         string
	Provider     string
	Model        string
	Usage        Usage
	FinishReason string
	CostUSD      *float64
	Billing      string
	// Attempts counts the attempts the call made, including failed ones.
	Attempts   int
	Downgraded bool
	// Fallback says a fallback candidate answered, not the route's primary.
	Fallback bool
	CallID   string
	// Sources are the pages a web search returned, in order, without repeats.
	Sources []Source
}

// Source is one page a provider's web search cited.
type Source struct {
	URL, Title, Content string
}

// completion is what a provider client returns.
type completion struct {
	text   string
	finish string
	usage  Usage
	// sources are the pages a web search cited.
	sources []Source
	// estimated says the provider reported no usage and the counts come from
	// text length.
	estimated bool
}

// estimateUsage fills the counts from text length when the provider sent none.
func (c *completion) estimateUsage(in, out string) {
	c.usage, c.estimated = estimateUsage(in, out), true
}

// attemptInput is the shaped request an attempt sends.
type attemptInput struct {
	system, prompt string
	maxTokens      int
	thinking       string
	schema         json.RawMessage
	emit           func(string)
	task           string
	webSearch      int
}

func (r *Router) prepare(ctx context.Context, req Request) (*callState, error) {
	task, ok := LookupTask(req.Task)
	if !ok {
		return nil, &Error{Kind: KindUnknownTask, Task: req.Task, Message: "not a task id"}
	}
	s, err := LoadSettings(ctx, r.store)
	if err != nil {
		return nil, &Error{Kind: KindOther, Task: task.ID, Message: err.Error()}
	}
	override := req.Model
	var pin *Fallback
	switch {
	case req.pin != nil:
		pin = req.pin
	case override != "":
		prov, model, ok := strings.Cut(override, "/")
		if !ok || prov == "" || model == "" {
			return nil, &Error{Kind: KindBadRequest, Task: task.ID, Message: "a model override is provider/model"}
		}
		if err := ValidModelID(model); err != nil {
			return nil, &Error{Kind: KindBadRequest, Task: task.ID, Message: "model override: " + err.Error()}
		}
		pin = &Fallback{prov, model}
	}
	res, err := r.resolveWith(s, task.ID)
	if err != nil {
		if pin == nil {
			return nil, err
		}
		res = fromRoute(task, Route{Provider: pin.Provider, Model: pin.Model}, SourceTable)
	}
	if req.Timeout > 0 {
		res.Timeout = req.Timeout
	}
	id := req.callID
	if id == "" {
		id = newCallID()
	}
	cs := &callState{id: id, attemptBase: req.attemptBase, task: task, res: res, settings: s, runID: req.RunID, deadline: req.Deadline, prompt: req.System + "\n\n" + req.Prompt}

	if pin != nil {
		p, ok := r.providerFor(s, pin.Provider)
		if !ok {
			return nil, &Error{Kind: KindUnconfigured, Task: task.ID, Message: fmt.Sprintf("provider %q is not configured", pin.Provider)}
		}
		src := "override"
		if req.pin != nil {
			src = "primary"
		}
		cs.cands = []candidate{{provider: p, model: pin.Model, source: src}}
		return cs, nil
	}
	listed, byMode := r.callCandidates(ctx, s, res, true)
	for i, c := range listed {
		p, ok := r.providerFor(s, c.Provider)
		if !ok {
			continue
		}
		src := "primary"
		if i > 0 {
			src = "fallback"
		}
		cand := candidate{provider: p, model: c.Model, source: src}
		if i == 0 {
			cand.byMode = byMode
		}
		cs.cands = append(cs.cands, cand)
	}
	if req.WebSearch > 0 {
		cs.searches = req.WebSearch
		cs.cands = slices.DeleteFunc(cs.cands, func(c candidate) bool { return !c.provider.Has(CapWebSearch) })
		if len(cs.cands) == 0 {
			return nil, &Error{Kind: KindNoRoute, Task: task.ID, Message: "no routed provider offers web search"}
		}
	}
	if len(cs.cands) == 0 {
		return nil, &Error{Kind: KindNoRoute, Task: task.ID, Message: "the routed provider is not configured"}
	}
	if r.overLimit(ctx, task) {
		cs.cands = r.downgradeOrder(ctx, s, cs.cands)
	}
	cs.cands = r.restOrder(cs.cands)
	return cs, nil
}

// overLimit reports whether a task's calls move to the cheapest model: the day
// is over the daily cap, or the task over its own budget, and the task is one
// the spend limits move. The agent gateway reads the same rule.
func (r *Router) overLimit(ctx context.Context, task Task) bool {
	return (r.capReached(ctx) || r.BudgetReached(ctx, task.ID)) && !exemptFromDowngrade[task.ID] && task.Class != ClassEmbedding
}

// providerFor finds a configured provider; offline simulation answers with the
// fake provider.
func (r *Router) providerFor(s Settings, id string) (ProviderConfig, bool) {
	if r.offline || s.Offline {
		return ProviderConfig{ID: offlineProvider, Kind: KindFake}, true
	}
	return s.Provider(id)
}

// NativeSearch reports whether the task's routed model is served by a provider
// that searches the web itself. Offline simulation and an unrouted task say no.
func (r *Router) NativeSearch(ctx context.Context, taskID string) bool {
	s, err := LoadSettings(ctx, r.store)
	if err != nil {
		return false
	}
	res, err := r.resolveWith(s, taskID)
	if err != nil {
		return false
	}
	p, ok := r.providerFor(s, s.head(res).Provider)
	return ok && p.Has(CapWebSearch)
}

// shape applies the request rules: with thinking on, the caller's ceiling is
// raised to the row's (never below 2,048) because reasoning and answer share it.
func shape(res Resolved, req Request) (maxTokens int, thinking string) {
	thinking = res.Thinking
	maxTokens = req.MaxOutputTokens
	if thinking == "" || thinking == ThinkOff {
		if maxTokens == 0 {
			maxTokens = res.MaxOutputTokens
		}
		return maxTokens, ThinkOff
	}
	return max(maxTokens, res.MaxOutputTokens, minThinkingMax), thinking
}

// Complete performs a completion call: it resolves the route, walks the
// candidates and records every attempt in the ledger.
func (r *Router) Complete(ctx context.Context, req Request) (*Response, error) {
	cs, err := r.prepare(ctx, req)
	if err != nil {
		return nil, err
	}
	return r.complete(ctx, cs, req, nil)
}

func (r *Router) complete(ctx context.Context, cs *callState, req Request, schema json.RawMessage) (*Response, error) {
	maxTokens, thinking := shape(cs.res, req)
	cs.streams = true
	var out completion
	err := r.walk(ctx, cs, func(actx context.Context, a *attempt) error {
		in := attemptInput{system: req.System, prompt: req.Prompt, maxTokens: maxTokens, thinking: thinking, task: cs.task.ID,
			webSearch: req.WebSearch,
			emit: func(s string) {
				if s != "" && req.OnDelta != nil {
					a.delivered = true
					req.OnDelta(s)
				}
			}}
		if schema != nil {
			if a.cand.provider.Has(CapStructured) {
				in.schema = schema
			} else {
				in.prompt = promptWithSchema(req.Prompt, schema)
			}
		}
		c, err := r.dispatch(actx, cs, a, in)
		a.usage, a.finish, a.text, a.estimated = c.usage, c.finish, c.text, c.estimated
		if err != nil {
			return err
		}
		switch {
		case strings.TrimSpace(c.text) == "":
			return &Error{Kind: KindEmptyCompletion, Message: "the reply held no text (finish: " + c.finish + ")"}
		case c.finish == "length":
			return &Error{Kind: KindTruncated, Message: "the reply hit the output ceiling", Partial: c.text}
		}
		out = c
		return nil
	})
	if err != nil {
		return nil, err
	}
	won := cs.won
	cost := r.costing(ctx, cs.settings, won.cand.provider, won.cand.model, out.usage).withSearch(cs.searches)
	return &Response{Text: out.text, Provider: won.cand.provider.ID, Model: won.cand.model, Usage: out.usage,
		FinishReason: out.finish, CostUSD: cost.Cost, Billing: cost.Billing, Attempts: cs.attempts,
		Downgraded: won.cand.downgraded, Fallback: won.cand.source == "fallback", CallID: cs.id, Sources: out.sources}, nil
}

func (r *Router) dispatch(ctx context.Context, cs *callState, a *attempt, in attemptInput) (completion, error) {
	switch a.cand.provider.Kind {
	case KindOpenAI:
		return r.openAIComplete(ctx, cs, a, in)
	case KindAnthropic:
		return r.anthropicComplete(ctx, a, in)
	case KindCLI:
		return r.cliComplete(ctx, a, in)
	case KindChatGPT:
		return r.chatgptComplete(ctx, a, in)
	case KindFake:
		return r.fake.complete(ctx, a, in)
	}
	return completion{}, &Error{Kind: KindOther, Message: "unknown provider kind " + a.cand.provider.Kind}
}
