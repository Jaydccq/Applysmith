package api

import (
	"context"

	"applysmith/internal/answerbank/learn"
)

// reconcileLearning finishes an answer-bank approval the previous process
// stopped in the middle of. It runs once at start, before anything else uses
// the bank; the periodic maintenance (candidates, stale transition, old
// events) is a retention rule of the scheduler's daily sweep.
func (s *Server) reconcileLearning(ctx context.Context, svc *learn.Service) {
	if _, err := svc.Reconcile(ctx); err != nil && ctx.Err() == nil {
		s.log.Error("answer-bank approval reconciliation failed", "error", err.Error())
	}
}
