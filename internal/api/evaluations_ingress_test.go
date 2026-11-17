package api

import (
	"context"
	"testing"

	"applysmith/internal/agent"
	"applysmith/internal/discovery"
)

// An evaluation the Ask Agent or Telegram starts for a posting that waits in
// the inbox names that row, so the row leaves Waiting with the evaluation.
func TestEvaluate_AgentAndTelegramTakeTheWaitingRowAlong(t *testing.T) {
	h := newDomainHarness(t)
	ctx := context.Background()
	repo := h.domainOf().repo
	for name, start := range map[string]func(string) (string, error){
		"agent": func(u string) (string, error) {
			return agentActions{s: h.srv}.StartEvaluation(ctx, agent.EvaluationRequest{URL: u})
		},
		"telegram": func(u string) (string, error) {
			st, err := telegramBackend{s: h.srv}.StartEvaluation(ctx, u)
			return st.RunID, err
		},
	} {
		u := "https://jobs.example.com/p/waiting-" + name
		add, err := repo.InboxAddURL(ctx, u)
		if err != nil || add.Row == nil {
			t.Fatalf("%s add: %+v %v", name, add, err)
		}
		run, err := start(u)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		h.waitEvaluation(run)
		if err := h.domainOf().pool.Drain(ctx); err != nil {
			t.Fatal(err)
		}
		if row, err := repo.InboxGet(ctx, add.Row.ID); err != nil || row.Status != discovery.InboxEvaluated || row.JobID != run {
			t.Errorf("%s: row %s job %s (%v)", name, row.Status, row.JobID, err)
		}
	}
}
