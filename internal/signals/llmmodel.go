package signals

import (
	"context"
	"encoding/json"
	"errors"

	"applysmith/internal/llm"
)

// Model is the part of the model router that the pipeline calls.
type Model interface {
	Structured(ctx context.Context, req llm.Request, schema json.RawMessage, out any) (*llm.Response, error)
}

// Model tasks of the routing table.
const (
	taskClassify = "feed-classify"
	taskAssess   = "signal-assess"
)

// noRoute reports a model call that cannot run at all: no route, no key or a
// rejected key. The work stays pending for a later tick.
func noRoute(err error) bool {
	var e *llm.Error
	if !errors.As(err, &e) {
		return false
	}
	switch e.Kind {
	case llm.KindNoRoute, llm.KindUnconfigured, llm.KindAuth, llm.KindUnknownTask:
		return true
	}
	return false
}

// invalidStructure reports a reply that failed validation after its one repair.
func invalidStructure(err error) bool {
	var e *llm.Error
	return errors.As(err, &e) && e.Kind == llm.KindInvalidStructure
}
