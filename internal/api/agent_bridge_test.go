package api

import (
	"errors"
	"strings"
	"testing"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/llm"
)

// pi retries a request that failed with a 429 or 5xx unless the text holds a
// phrase it takes for a limit that lasts; the retry is what moves a turn from
// the ChatGPT plan to another wire. A bad request is never retried.
func TestModelError_AFailureAnotherWireCouldAnswerIsRetryableForPi(t *testing.T) {
	for _, tc := range []struct {
		kind llm.Kind
		code wire.ErrorCode
	}{
		{llm.KindQuota, wire.CodeRateLimited},
		{llm.KindAuth, wire.CodeUnavailable},
		{llm.KindOverloaded, wire.CodeUnavailable},
		{llm.KindBadRequest, wire.CodeBadRequest},
	} {
		err := modelError(&llm.Error{Kind: tc.kind, Task: llm.AgentTask, Message: "ChatGPT plan usage limit reached (subscription_sharing_usage_limit_exceeded)"})
		var e *apikit.Error
		if !errors.As(err, &e) || e.Code != tc.code || strings.Contains(e.Message, "subscription_sharing_usage_limit_exceeded") {
			t.Errorf("%s -> %v", tc.kind, err)
		}
	}
}
