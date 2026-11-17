package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"applysmith/internal/api/wire"
)

// A step answers one poll of a scripted server; the last step repeats.
type pollStep func(w http.ResponseWriter)

func answer(data any) pollStep {
	return func(w http.ResponseWriter) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
	}
}

// setForTest sets a package variable for one test and restores it after.
func setForTest[T any](t *testing.T, p *T, v T) {
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

func pollEnv(t *testing.T) (*env, *bytes.Buffer) {
	var errw bytes.Buffer
	setForTest(t, &evaluatePoll, time.Millisecond)
	setForTest(t, &backupPollEvery, time.Millisecond)
	return &env{stdout: &bytes.Buffer{}, stderr: &errw}, &errw
}

func liveAt(srv *httptest.Server) *liveServer {
	return &liveServer{origin: srv.URL, token: "tok", client: srv.Client()}
}

// The running server answers the earlier result until the new test ends; the
// command waits for the new one and asks for the model it was given.
func TestLLMTestCommand_WaitsForTheNewResultOfTheNamedModel(t *testing.T) {
	old := &wire.LLMTestResult{Provider: "nim", Model: "a", Outcome: "ok", TestedAt: wire.Time{Time: time.Unix(1, 0)}}
	views := []wire.LLMRouting{{Preset: "nim", LastTest: old}, {Preset: "nim", LastTest: old, Testing: "nim"},
		{Preset: "nim", LastTest: &wire.LLMTestResult{Provider: "nim", Model: "b", Outcome: "failed", TestedAt: wire.Time{Time: time.Unix(2, 0)}}}}
	var sent wire.LLMTestRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/llm/providers/nim/test" {
			_ = json.NewDecoder(r.Body).Decode(&sent)
			answer(wire.LLMTestStarted{Provider: "nim"})(w)
			return
		}
		answer(views[0])(w)
		views = views[min(1, len(views)-1):]
	}))
	t.Cleanup(srv.Close)
	e, _ := pollEnv(t)
	lt, err := e.liveProviderTest(context.Background(), liveAt(srv), "", "b")
	if err != nil || lt.Model != "b" || sent.Model != "b" {
		t.Fatalf("result %+v err %v sent %+v", lt, err, sent)
	}
}
