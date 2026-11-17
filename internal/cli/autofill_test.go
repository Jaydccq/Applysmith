package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api/wire"
)

type fakeFill struct {
	startErr error
	results  []wire.AutofillTabResult
	polls    int
}

func (f *fakeFill) start(context.Context) (string, error) { return "run_1", f.startErr }

func (f *fakeFill) result(context.Context, string) (wire.AutofillTabResult, error) {
	i := min(f.polls, len(f.results)-1)
	f.polls++
	return f.results[i], nil
}

func fillEnv(t *testing.T) (*env, *bytes.Buffer, *bytes.Buffer) {
	var out, errw bytes.Buffer
	setForTest(t, &fillPoll, time.Millisecond)
	return &env{stdout: &out, stderr: &errw}, &out, &errw
}

var doneFill = wire.AutofillTabResult{RunID: "run_1", Status: wire.RunSucceeded, URL: "https://jobs.example.test/apply/1",
	Filled:  []wire.AutofillControl{{Label: "Full name"}},
	Review:  []wire.AutofillControl{{Label: "Phone"}},
	Missing: []wire.AutofillControl{{Label: "Why us", Reason: "no-answer"}, {Label: "Referral code", Reason: "no-answer"}},
	Skipped: []wire.AutofillControl{}, Drafts: 1}

func TestAutofillCommand_WaitsForTheResultAndListsTheLeftovers(t *testing.T) {
	e, out, errw := fillEnv(t)
	f := &fakeFill{results: []wire.AutofillTabResult{{RunID: "run_1", Status: wire.RunRunning}, {RunID: "run_1", Status: wire.RunRunning}, doneFill}}
	if code := e.runAutofill(context.Background(), f, false); code != 0 {
		t.Fatalf("exit %d: %s", code, errw)
	}
	text := out.String()
	for _, want := range []string{"filled 1, review 1, missing 2, skipped 0", "missing: Why us (no-answer)", "missing: Referral code (no-answer)", "review: Phone",
		"drafts awaiting review 1", "Nothing was submitted"} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	if f.polls != 3 {
		t.Errorf("polled %d times", f.polls)
	}
}

func TestAutofillCommand_JSONIsTheResult(t *testing.T) {
	e, out, _ := fillEnv(t)
	if code := e.runAutofill(context.Background(), &fakeFill{results: []wire.AutofillTabResult{doneFill}}, true); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var got wire.AutofillTabResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got.Missing) != 2 {
		t.Fatalf("%v %s", err, out)
	}
}

func TestAutofillCommand_ARefusalIsAFailureWithTheServersWords(t *testing.T) {
	e, out, errw := fillEnv(t)
	f := &fakeFill{startErr: errors.New("the running server refused: this page is not an application form")}
	if code := e.runAutofill(context.Background(), f, false); code != 1 || out.Len() != 0 || !strings.Contains(errw.String(), "not an application form") {
		t.Fatalf("exit %d %q %q", code, out, errw)
	}
}

func TestAutofillCommand_AFailedFillExitsNonZero(t *testing.T) {
	e, _, errw := fillEnv(t)
	f := &fakeFill{results: []wire.AutofillTabResult{{RunID: "run_1", Status: wire.RunFailed, Reason: "the tab could not be read"}}}
	if code := e.runAutofill(context.Background(), f, false); code != 1 || !strings.Contains(errw.String(), "the tab could not be read") {
		t.Fatalf("exit %d %q", code, errw)
	}
}

func TestAutofillCommand_WithoutAServerSaysSoAndTakesNoArguments(t *testing.T) {
	te := newTestEnv(t)
	o := te.run(context.Background(), "autofill", "--workspace", te.ws)
	if o.code != 1 || !strings.Contains(o.stderr, "no server runs") {
		t.Fatalf("%+v", o)
	}
	if o := te.run(context.Background(), "autofill", "https://jobs.example.test/a"); o.code != 2 || !strings.Contains(o.stderr, "usage: applysmith autofill") {
		t.Fatalf("%+v", o)
	}
}

func TestAutofillCommand_TalksToTheServerWithTheBearerToken(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": wire.RunAccepted{RunID: "run_9"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": doneFill})
	}))
	defer srv.Close()
	e, out, errw := fillEnv(t)
	live := liveFill{e: e, srv: &liveServer{origin: srv.URL, token: "tok", client: srv.Client()}}
	if code := e.runAutofill(context.Background(), live, false); code != 0 {
		t.Fatalf("exit %d: %s", code, errw)
	}
	if strings.Join(paths, ",") != "POST /api/autofill/active-tab,GET /api/autofill/active-tab/run_9" || !strings.Contains(out.String(), "filled 1") {
		t.Fatalf("%v\n%s", paths, out)
	}
}
