package mcpserver

import (
	"context"
	"net/http"
	"strings"
	"syscall"
	"testing"

	"applysmith/internal/api/wire"
)

func TestApplyListStatus_ASubmitHeldForConfirmationReadsNeedsUser(t *testing.T) {
	api := newFakeAPI(t)
	api.serve(map[string]any{"/api/apply/list": map[string]any{
		"counts": map[string]any{"states": map[string]any{"needs-user": 1}},
		"lanes": []map[string]any{{"lane": "apply-now", "total": 1, "items": []map[string]any{{
			"company": "Example Corp", "role": "Engineer", "postingHost": "jobs.example.com", "state": "needs-user",
			"attempt": map[string]any{"outcome": map[string]any{"reason": "submit held for confirmation"}},
		}}}},
	}})
	got := structured(t, call(t, connect(t, api.client(), nil), "apply_list_status", map[string]any{}))
	item := got["items"].([]any)[0].(map[string]any)
	if item["state"] != "needs-user" || item["reason"] != "submit held for confirmation" {
		t.Errorf("item = %v", item)
	}
	if got["counts"].(map[string]any)["needs-user"] != float64(1) {
		t.Errorf("counts = %v", got["counts"])
	}
}

func TestErrors_ServerFailureNeverQuotesTheAnswerBody(t *testing.T) {
	const canary = "canary-field-value-9921"
	bodies := map[string]func(http.ResponseWriter){
		"plain text": func(w http.ResponseWriter) { w.WriteHeader(500); _, _ = w.Write([]byte("stack " + canary)) },
		"envelope detail": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"ok":false,"requestId":"req_x","error":{"code":"internal","message":"boom","retryable":false,"details":{"row":"` + canary + `"}}}`))
		},
	}
	for name, write := range bodies {
		t.Run(name, func(t *testing.T) {
			api := newFakeAPI(t)
			api.handle = func(w http.ResponseWriter, _ *http.Request) { write(w) }
			res := call(t, connect(t, api.client(), nil), "list_candidacies", map[string]any{})
			te := toolError(t, res)
			if te.Code != CodeServerError || te.Remedy == "" || !strings.Contains(te.Message, "500") {
				t.Errorf("error = %+v", te)
			}
			if strings.Contains(textOf(t, res), canary) {
				t.Errorf("the answer body reached the client: %s", textOf(t, res))
			}
		})
	}
}

func TestErrors_EveryCodeOfTheClosedSetCarriesOneSentenceRemedy(t *testing.T) {
	fail := func(code wire.ErrorCode) wire.Failure {
		return wire.Failure{Error: wire.ErrorBody{Code: code, Message: "m"}}
	}
	got := []*ToolError{
		classify(syscall.ECONNREFUSED),
		fromEnvelope(401, fail(wire.CodeUnauthenticated), ""),
		fromEnvelope(404, fail(wire.CodeNotFound), ""),
		fromEnvelope(400, fail(wire.CodeBadRequest), ""),
		fromEnvelope(429, fail(wire.CodeRateLimited), ""),
		classify(context.DeadlineExceeded),
		fromEnvelope(500, fail("internal"), ""),
	}
	want := []Code{CodeServerNotRunning, CodeUnauthorized, CodeNotFound, CodeInvalidArgument, CodeRateLimited, CodeTimeout, CodeServerError}
	for i, te := range got {
		if te.Code != want[i] {
			t.Errorf("case %d: code %s, want %s", i, te.Code, want[i])
		}
		if te.Remedy == "" || !strings.HasSuffix(te.Remedy, ".") || strings.Contains(te.Remedy, ". ") {
			t.Errorf("%s: remedy %q is not one sentence", te.Code, te.Remedy)
		}
	}
}
