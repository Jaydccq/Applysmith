// Package agentapi serves the Ask Agent's Memory tab and switches in the local
// API: the facts, rules and review queue the agent keeps, and the session
// settings. internal/api registers the handlers.
package agentapi

import (
	"context"
	"errors"
	"time"

	"applysmith/internal/agent/memory"
	chatsessions "applysmith/internal/agent/sessions"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/store"
)

// Deps is what the Memory and settings handlers use from the server.
type Deps struct {
	Memory   func() (*memory.Store, error)
	Sessions func() (*chatsessions.Store, error) // the switches and the token budget
	Now      func() time.Time
}

// Handlers serves the Memory and settings routes.
type Handlers struct{ Deps }

func New(d Deps) *Handlers { return &Handlers{Deps: d} }

// memoryError maps the store's refusals onto the API's answers.
func memoryError(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return apikit.NewErr(wire.CodeNotFound, "no such fact, rule or review item")
	case errors.Is(err, memory.ErrRulesFull), errors.Is(err, memory.ErrFull):
		return apikit.NewErr(wire.CodeConflict, "%s", err.Error())
	case errors.Is(err, memory.ErrInvalid), errors.Is(err, memory.ErrRefused), errors.Is(err, memory.ErrRuleGrantsPermission), errors.Is(err, memory.ErrTombstoned):
		return apikit.NewErr(wire.CodeValidationFailed, "%s", err.Error())
	}
	return err
}

func toWireFact(f memory.Fact) wire.MemoryFact {
	return wire.MemoryFact{ID: f.ID, Kind: f.Kind, Subject: f.Subject, SubjectRef: f.SubjectRef, Body: f.Body, Source: f.Source, Trust: f.Trust, PII: f.PII,
		CreatedAt: wire.Time{Time: f.CreatedAt}}
}

func toWireRule(r memory.Rule) wire.MemoryRule {
	return wire.MemoryRule{ID: r.ID, Body: r.Body, State: r.State, Source: r.Source, CreatedAt: wire.Time{Time: r.CreatedAt}, UpdatedAt: wire.Time{Time: r.UpdatedAt}}
}

func toWireReview(r memory.ReviewItem) wire.MemoryReviewItem {
	return wire.MemoryReviewItem{ID: r.ID, Kind: r.Kind, Subject: r.Subject, SubjectRef: r.SubjectRef, Body: r.Body, Reason: r.Reason, Source: r.Source, CreatedAt: wire.Time{Time: r.CreatedAt}}
}

func wireFacts(in []memory.Fact) []wire.MemoryFact {
	out := make([]wire.MemoryFact, len(in))
	for i, f := range in {
		out[i] = toWireFact(f)
	}
	return out
}

func wireRules(in []memory.Rule) []wire.MemoryRule {
	out := make([]wire.MemoryRule, len(in))
	for i, r := range in {
		out[i] = toWireRule(r)
	}
	return out
}

func wireReviews(in []memory.ReviewItem) []wire.MemoryReviewItem {
	out := make([]wire.MemoryReviewItem, len(in))
	for i, r := range in {
		out[i] = toWireReview(r)
	}
	return out
}

func (s *Handlers) ListMemoryFacts(c *apikit.Call) (apikit.Reply, error) {
	m, err := s.Memory()
	if err != nil {
		return apikit.Reply{}, err
	}
	trust := apikit.QueryOf(c).Str("trust")
	if trust != "" && trust != memory.Trusted && trust != memory.NeedsReview {
		return apikit.Reply{}, apikit.InvalidField("trust", "trust must be trusted or needs_review")
	}
	facts, err := m.Facts(c.R.Context(), trust)
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: wire.MemoryFactList{Facts: wireFacts(facts), MaxFacts: apikit.Safe(memory.MaxActiveFacts), MaxFactBody: apikit.Safe(memory.MaxFactChars)}}, nil
}

func (s *Handlers) PatchMemoryFact(c *apikit.Call) (apikit.Reply, error) {
	var req wire.MemoryFactPatch
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	m, err := s.Memory()
	if err != nil {
		return apikit.Reply{}, err
	}
	res, err := m.Amend(c.R.Context(), c.R.PathValue("id"), req.Body)
	if err != nil {
		return apikit.Reply{}, memoryError(err)
	}
	out := wire.MemoryFactEdited{ReviewID: res.ReviewID}
	if res.Outcome != memory.Reviewed {
		f := toWireFact(res.Fact)
		out.Fact = &f
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Handlers) DeleteMemoryFact(c *apikit.Call) (apikit.Reply, error) {
	m, err := s.Memory()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("id")
	if err := m.Forget(c.R.Context(), id); err != nil {
		return apikit.Reply{}, memoryError(err)
	}
	return apikit.Reply{Data: wire.MemoryDeleted{ID: id}}, nil
}

func (s *Handlers) ApproveMemoryFact(c *apikit.Call) (apikit.Reply, error) {
	m, err := s.Memory()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("id")
	if err := m.ApproveFact(c.R.Context(), id); err != nil {
		return apikit.Reply{}, memoryError(err)
	}
	facts, err := m.Facts(c.R.Context(), "")
	if err != nil {
		return apikit.Reply{}, err
	}
	for _, f := range facts {
		if f.ID == id {
			return apikit.Reply{Data: toWireFact(f)}, nil
		}
	}
	return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "no such fact, rule or review item")
}

func (s *Handlers) ListMemoryRules(c *apikit.Call) (apikit.Reply, error) {
	m, err := s.Memory()
	if err != nil {
		return apikit.Reply{}, err
	}
	rules, err := m.Rules(c.R.Context(), "")
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: wire.MemoryRuleList{Rules: wireRules(rules), MaxActive: apikit.Safe(memory.MaxActiveRules), MaxRuleBody: apikit.Safe(memory.MaxRuleChars)}}, nil
}

// ruleReply reads a rule back after a change, so the answer shows its state.
func (s *Handlers) ruleReply(c *apikit.Call, m *memory.Store, id string) (apikit.Reply, error) {
	rules, err := m.Rules(c.R.Context(), "")
	if err != nil {
		return apikit.Reply{}, err
	}
	for _, r := range rules {
		if r.ID == id {
			return apikit.Reply{Data: toWireRule(r)}, nil
		}
	}
	return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "no such fact, rule or review item")
}

func (s *Handlers) PatchMemoryRule(c *apikit.Call) (apikit.Reply, error) {
	var req wire.MemoryRulePatch
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	m, err := s.Memory()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("id")
	if err := m.EditRule(c.R.Context(), id, req.Body); err != nil {
		return apikit.Reply{}, memoryError(err)
	}
	return s.ruleReply(c, m, id)
}

func (s *Handlers) DeleteMemoryRule(c *apikit.Call) (apikit.Reply, error) {
	m, err := s.Memory()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("id")
	if err := m.DeleteRule(c.R.Context(), id); err != nil {
		return apikit.Reply{}, memoryError(err)
	}
	return apikit.Reply{Data: wire.MemoryDeleted{ID: id}}, nil
}

func (s *Handlers) ApproveMemoryRule(c *apikit.Call) (apikit.Reply, error) {
	return s.changeRule(c, (*memory.Store).ApproveRule)
}

func (s *Handlers) RetireMemoryRule(c *apikit.Call) (apikit.Reply, error) {
	return s.changeRule(c, (*memory.Store).RetireRule)
}

func (s *Handlers) changeRule(c *apikit.Call, change func(*memory.Store, context.Context, string) error) (apikit.Reply, error) {
	m, err := s.Memory()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("id")
	if err := change(m, c.R.Context(), id); err != nil {
		return apikit.Reply{}, memoryError(err)
	}
	return s.ruleReply(c, m, id)
}

func (s *Handlers) ListMemoryReview(c *apikit.Call) (apikit.Reply, error) {
	m, err := s.Memory()
	if err != nil {
		return apikit.Reply{}, err
	}
	items, err := m.Review(c.R.Context())
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: wire.MemoryReviewList{Items: wireReviews(items)}}, nil
}

func (s *Handlers) DismissMemoryReview(c *apikit.Call) (apikit.Reply, error) {
	m, err := s.Memory()
	if err != nil {
		return apikit.Reply{}, err
	}
	id := c.R.PathValue("id")
	if err := m.DismissReview(c.R.Context(), id); err != nil {
		return apikit.Reply{}, memoryError(err)
	}
	return apikit.Reply{Data: wire.MemoryDeleted{ID: id}}, nil
}

// ExportMemory returns everything the memory holds: the facts of both trust
// levels, every rule and the review queue.
func (s *Handlers) ExportMemory(c *apikit.Call) (apikit.Reply, error) {
	m, err := s.Memory()
	if err != nil {
		return apikit.Reply{}, err
	}
	ctx := c.R.Context()
	facts, err := m.Facts(ctx, "")
	if err != nil {
		return apikit.Reply{}, err
	}
	rules, err := m.Rules(ctx, "")
	if err != nil {
		return apikit.Reply{}, err
	}
	items, err := m.Review(ctx)
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: wire.MemoryExport{ExportedAt: wire.Time{Time: s.Now()}, Facts: wireFacts(facts), Rules: wireRules(rules), Review: wireReviews(items)}}, nil
}

func agentSettings(set chatsessions.Settings) wire.AgentSettings {
	return wire.AgentSettings{Compaction: set.Compaction, Consolidation: set.Consolidation, SelfImprove: set.SelfImprove, TokenBudget: apikit.Safe(set.TokenBudget),
		TokenBudgetMin: apikit.Safe(chatsessions.MinTokenBudget), TokenBudgetMax: apikit.Safe(chatsessions.MaxTokenBudget), TokenBudgetDefault: apikit.Safe(chatsessions.DefaultTokenBudget)}
}

func (s *Handlers) GetSettings(c *apikit.Call) (apikit.Reply, error) {
	sess, err := s.Sessions()
	if err != nil {
		return apikit.Reply{}, err
	}
	set, err := sess.Settings(c.R.Context())
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: agentSettings(set)}, nil
}

func (s *Handlers) PatchSettings(c *apikit.Call) (apikit.Reply, error) {
	var req wire.AgentSettingsPatch
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	sess, err := s.Sessions()
	if err != nil {
		return apikit.Reply{}, err
	}
	ctx := c.R.Context()
	if b := req.TokenBudget; b != nil && (*b < chatsessions.MinTokenBudget || *b > chatsessions.MaxTokenBudget) {
		return apikit.Reply{}, apikit.InvalidField("tokenBudget", "tokenBudget must be between %d and %d", chatsessions.MinTokenBudget, chatsessions.MaxTokenBudget)
	}
	if req.Compaction != nil {
		if err := sess.SetSwitch(ctx, chatsessions.SettingCompaction, *req.Compaction); err != nil {
			return apikit.Reply{}, err
		}
	}
	if req.Consolidation != nil {
		if err := sess.SetSwitch(ctx, chatsessions.SettingConsolidation, *req.Consolidation); err != nil {
			return apikit.Reply{}, err
		}
	}
	if req.SelfImprove != nil {
		if err := sess.SetSwitch(ctx, chatsessions.SettingSelfImprove, *req.SelfImprove); err != nil {
			return apikit.Reply{}, err
		}
	}
	if req.TokenBudget != nil {
		if err := sess.SetTokenBudget(ctx, int(*req.TokenBudget)); err != nil {
			return apikit.Reply{}, err
		}
	}
	set, err := sess.Settings(ctx)
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: agentSettings(set)}, nil
}
