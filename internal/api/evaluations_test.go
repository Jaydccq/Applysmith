package api

import (
	"applysmith/internal/api/apikit"
	"applysmith/internal/llm"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"applysmith/internal/api/wire"
	"applysmith/internal/discovery"
	"applysmith/internal/evaluate"
)

func TestEvaluationIsAcceptedAndRunsToAReport(t *testing.T) {
	h := newDomainHarness(t)
	r := h.startEvaluation("key-eval-0001", map[string]any{"text": postingBody("accepted"), "url": "https://jobs.example.com/p/accepted", "company": "Example Systems", "title": "Software Engineer"})
	id, _ := r.data()["runId"].(string)
	if r.StatusCode != http.StatusAccepted || id == "" || r.Header.Get("Location") != "/api/runs/"+id {
		t.Fatalf("%d %v %s", r.StatusCode, r.Header, r.body)
	}
	out := h.waitEvaluation(id)
	if out["outcome"] != "reported" || out["status"] != "succeeded" || out["reportNumber"] == nil {
		t.Fatalf("outcome %v", out)
	}
	run := h.get(r.Header.Get("Location"))
	if run.StatusCode != 200 || run.data()["kind"] != "evaluate" || run.data()["status"] != "succeeded" {
		t.Fatalf("run %d %s", run.StatusCode, run.body)
	}
	// The report exists, the tracker holds its row, and the board was told.
	rep := h.get("/api/reports/" + itoa(int(out["reportNumber"].(float64))))
	if rep.StatusCode != 200 || !strings.Contains(rep.data()["content"].(string), "Verdict") {
		t.Fatalf("report %d %s", rep.StatusCode, rep.body)
	}
	list := h.get("/api/candidacies")
	entries := list.data()["entries"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["reportNumber"] == nil {
		t.Fatalf("tracker %s", list.body)
	}
	for _, topic := range []string{"runs", "reports", "tracker"} {
		if h.boardRevision(topic) == 0 {
			t.Errorf("topic %s was not announced", topic)
		}
	}
}

// With no model for evaluation, a press that would start one (Home's
// Evaluate, the inbox's) is refused at once with the reason the page links to
// Settings, Models: no run is made and the inbox row keeps its attempts.
func TestEvaluationWithoutAModelIsRefusedAtOnce(t *testing.T) {
	h := newRoutingHarness(t)
	d := h.domainOf()
	r := h.startEvaluation("key-eval-nomodel", map[string]any{"text": postingBody("no model"), "url": "https://jobs.example.com/p/no-model"})
	if r.StatusCode != 503 || r.errReason() != wire.ReasonNoModelRoute {
		t.Fatalf("evaluate: %d %s", r.StatusCode, r.body)
	}
	add, err := d.repo.InboxAddURL(t.Context(), "https://jobs.example.com/p/no-model-row")
	if err != nil || add.Row == nil {
		t.Fatalf("add: %+v %v", add, err)
	}
	if r := h.post("/api/inbox/promote", map[string]any{"ids": []int64{add.Row.ID}}); r.StatusCode != 503 || r.errReason() != wire.ReasonNoModelRoute {
		t.Fatalf("promote: %d %s", r.StatusCode, r.body)
	}
	var runs int
	if err := h.srv.store.Load().Read().QueryRow(`SELECT count(*) FROM runs WHERE kind = 'evaluate'`).Scan(&runs); err != nil || runs != 0 {
		t.Fatalf("%d evaluation runs (%v)", runs, err)
	}
	if row, err := d.repo.InboxGet(t.Context(), add.Row.ID); err != nil || row.Status != discovery.InboxPending || row.Attempts != 0 {
		t.Fatalf("row %+v (%v)", row, err)
	}
}

func TestEvaluationRetryWithTheSameKeyReturnsTheOriginalRun(t *testing.T) {
	h := newDomainHarness(t)
	body := map[string]any{"text": postingBody("retry"), "url": "https://jobs.example.com/p/retry", "company": "Example Systems"}
	first := h.startEvaluation("key-eval-0002", body)
	h.waitEvaluation(first.data()["runId"].(string))
	second := h.startEvaluation("key-eval-0002", body)
	if second.StatusCode != 202 || second.data()["runId"] != first.data()["runId"] {
		t.Fatalf("retry returned another run: %s vs %s", first.body, second.body)
	}
	if h.runCount() != 1 {
		t.Fatalf("%d runs exist, want 1", h.runCount())
	}
	// After a restart the claim still holds and nothing is run again.
	h.restart()
	third := h.startEvaluation("key-eval-0002", body)
	if third.data()["runId"] != first.data()["runId"] || h.runCount() != 1 {
		t.Fatalf("retry after restart: %s (runs %d)", third.body, h.runCount())
	}
}

func TestEvaluationSameKeyWithAChangedPostingConflicts(t *testing.T) {
	h := newDomainHarness(t)
	h.startEvaluation("key-eval-0003", map[string]any{"text": postingBody("one")})
	r := h.startEvaluation("key-eval-0003", map[string]any{"text": postingBody("two")})
	if r.StatusCode != 409 || r.errCode() != "conflict" || h.runCount() != 1 {
		t.Fatalf("%d %s (runs %d)", r.StatusCode, r.body, h.runCount())
	}
}

func TestEvaluationRequestValidation(t *testing.T) {
	h := newDomainHarness(t)
	noKey := h.send("POST", "/api/evaluations", map[string]any{"text": postingBody("x")})
	if noKey.StatusCode != 400 || noKey.errCode() != "bad_request" {
		t.Errorf("no key: %d %s", noKey.StatusCode, noKey.body)
	}
	if r := h.startEvaluation(freshKey(), map[string]any{"url": "ftp://jobs.example.com/x"}); r.StatusCode != 422 || r.errCode() != "validation_failed" {
		t.Errorf("bad url: %d %s", r.StatusCode, r.body)
	}
	if h.runCount() != 0 {
		t.Errorf("%d runs created by invalid requests", h.runCount())
	}
}

func TestEvaluationOutcomeRoute_UnknownJobIsNotFound(t *testing.T) {
	h := newDomainHarness(t)
	if r := h.get("/api/evaluations/run_missing"); r.StatusCode != 404 || r.errCode() != "not_found" {
		t.Errorf("unknown job: %d %s", r.StatusCode, r.body)
	}
}

func TestEvaluationQueueFullIsUnavailableWithItsReason(t *testing.T) {
	gate := newGateReader(postingBody("queue"))
	h := newDomainHarness(t, withReader(gate), func(c *Config) { c.Domain.EvalQueueMax = 1 })
	// Every in-flight slot is taken by a job held in the reading phase.
	t.Cleanup(func() { close(gate.release) })
	var last result
	for i := range 20 {
		last = h.startEvaluation(freshKey(), map[string]any{"url": "https://jobs.example.com/p/q" + itoa(i)})
		if last.StatusCode != 202 {
			break
		}
	}
	if last.StatusCode != 503 || last.errCode() != "unavailable" || last.errReason() != wire.ReasonQueueFull {
		t.Fatalf("%d %s", last.StatusCode, last.body)
	}
	if retry, _ := last.json()["error"].(map[string]any)["retryable"].(bool); !retry {
		t.Errorf("queue-full must be retryable: %s", last.body)
	}
}

func TestEvaluationCreationHasItsOwnRateBucket(t *testing.T) {
	h := newDomainHarness(t)
	body := map[string]any{"text": postingBody("bucket")}
	first := h.startEvaluation("key-eval-bucket", body)
	if first.StatusCode != 202 {
		t.Fatal(first.body)
	}
	// 240 general requests are used up; the evaluation bucket is untouched.
	for range limitCounts[LimitGeneral] {
		h.srv.limiter.allow(string(apikit.PrincipalLocalToken), LimitGeneral)
	}
	if r := h.get("/api/candidacies"); r.StatusCode != 429 {
		t.Fatalf("the general bucket should be spent: %d", r.StatusCode)
	}
	if r := h.startEvaluation("key-eval-bucket", body); r.StatusCode != 202 {
		t.Fatalf("an evaluation request was refused with the general bucket: %d %s", r.StatusCode, r.body)
	}
	// The evaluation bucket has a limit of its own: the 120th request passes, the next does not.
	var last result
	for range limitCounts[LimitEvaluation] {
		last = h.startEvaluation("key-eval-bucket", body)
		if last.StatusCode == 429 {
			break
		}
	}
	if last.StatusCode != 429 || last.errCode() != "rate_limited" || last.Header.Get("Retry-After") == "" {
		t.Fatalf("%d %s", last.StatusCode, last.body)
	}
}

type needsBrowserReader struct{}

func (needsBrowserReader) Read(context.Context, string) (evaluate.PageRead, error) {
	return evaluate.PageRead{}, evaluate.ErrNeedsBrowser
}

// "Evaluate again" on a failed evaluation starts the request it was started
// with, the mode and the posting text included; a finished one is not retried.
func TestEvaluationRetryStartsTheFailedRequestAgain(t *testing.T) {
	h := newDomainHarness(t, withReader(needsBrowserReader{}))
	first := h.startEvaluation(freshKey(), map[string]any{"url": "https://careers.example.com/jobs/7", "mode": "deep", "company": "Example Systems"})
	id := first.data()["runId"].(string)
	h.waitEvaluation(id)
	again := h.send("POST", "/api/evaluations/"+id+"/retry", nil, "Idempotency-Key", freshKey())
	next, _ := again.data()["runId"].(string)
	if again.StatusCode != 202 || next == "" || next == id {
		t.Fatalf("retry: %d %s", again.StatusCode, again.body)
	}
	var was, now evaluate.Request
	_, _, _, in := h.runState(id)
	_, _, _, out := h.runState(next)
	if json.Unmarshal([]byte(in), &was) != nil || json.Unmarshal([]byte(out), &now) != nil || now.URL != was.URL || now.Mode != evaluate.ModeDeep || now.Company != "Example Systems" {
		t.Fatalf("the retry asked %s, the run asked %s", out, in)
	}
	ok := newDomainHarness(t)
	done := ok.evaluate("retry-ok")["runId"].(string)
	if r := ok.send("POST", "/api/evaluations/"+done+"/retry", nil, "Idempotency-Key", freshKey()); r.StatusCode != http.StatusConflict {
		t.Fatalf("a succeeded evaluation was retried: %d %s", r.StatusCode, r.body)
	}
}

func TestEvaluationOfAnInboxRowTheGateRejectsIsRefusedAndStaysPending(t *testing.T) {
	h := newDomainHarness(t)
	repo := h.domainOf().repo
	add, err := repo.InboxAddURL(t.Context(), "https://jobs.example.com/p/inbox-blocked")
	if err != nil || add.Row == nil {
		t.Fatalf("add: %+v %v", add, err)
	}
	if _, err := repo.BlockCompany(t.Context(), add.Row.Company, "test", "test"); err != nil {
		t.Fatal(err)
	}
	r := h.startEvaluation("key-eval-inbox2", map[string]any{"text": postingBody("inbox-blocked"), "inboxRowId": add.Row.ID})
	if r.StatusCode != http.StatusUnprocessableEntity || r.errReason() != "gate-rejected" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if row, _ := repo.InboxGet(t.Context(), add.Row.ID); row.Status != "pending" {
		t.Fatalf("the gate kept the row %s", row.Status)
	}
	if n := h.runCount(); n != 0 {
		t.Fatalf("%d runs were created for a refused row", n)
	}
}

func TestURLOnlyEvaluationUsesDefaultManagedReaderAndReturnsData(t *testing.T) {
	// A page that shows no posting yet is read once more before it fails.
	for _, tc := range []struct {
		name  string
		page  discovery.PageRead
		want  string
		reads int
	}{
		{"active", discovery.PageRead{Status: 200, Title: "Software Engineer", Heading: "Software Engineer", Text: postingBody("urlonly"), HasApply: true}, "reported", 1},
		{"thin", discovery.PageRead{Status: 200, Text: "Loading"}, "failed", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			h := newDomainHarness(t, func(c *Config) {
				c.Domain.Reader = nil
				c.Domain.Liveness = pageReaderFunc(func(_ context.Context, u string) (discovery.PageRead, error) {
					calls++
					p := tc.page
					p.FinalURL = u
					return p, nil
				})
			})
			started := h.startEvaluation(freshKey(), map[string]any{"url": "https://careers.example.test/jobs/12345", "company": "Example Systems"})
			if started.StatusCode != 202 {
				t.Fatalf("%d %s", started.StatusCode, started.body)
			}
			out := h.waitEvaluation(started.data()["runId"].(string))
			if out["outcome"] != tc.want || calls != tc.reads {
				t.Fatalf("%v reads=%d", out, calls)
			}
			if tc.want == "reported" {
				if out["score"] == nil || out["recommendation"] == nil || out["reportNumber"] == nil {
					t.Fatalf("missing result data: %v", out)
				}
				report := h.get("/api/reports/" + itoa(int(out["reportNumber"].(float64))))
				if !strings.Contains(report.data()["content"].(string), "browser live") {
					t.Fatalf("%s", report.body)
				}
			} else if out["reason"] != "posting-unreadable" || out["reportNumber"] != nil {
				t.Fatalf("%v", out)
			}
		})
	}
}

// A page read that finds the posting closed expires the posting's inbox row
// instead of sending it back to Waiting, as the liveness audit would.
func TestEvaluationOfAClosedPostingExpiresItsInboxRow(t *testing.T) {
	const posting = "https://careers.example.test/jobs/4242"
	h := newDomainHarness(t, func(c *Config) {
		c.Domain.Reader = nil
		c.Domain.Liveness = pageReaderFunc(func(_ context.Context, u string) (discovery.PageRead, error) {
			return discovery.PageRead{Status: 410, FinalURL: u, Text: postingBody("closed")}, nil
		})
	})
	repo := h.domainOf().repo
	add, err := repo.InboxAddURL(t.Context(), posting)
	if err != nil || add.Row == nil {
		t.Fatalf("add: %+v %v", add, err)
	}
	r := h.startEvaluation(freshKey(), map[string]any{"url": posting, "inboxRowId": add.Row.ID})
	if r.StatusCode != http.StatusAccepted {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if out := h.waitEvaluation(r.data()["runId"].(string)); out["outcome"] != "failed" || out["reason"] != "posting-unreadable" {
		t.Fatalf("outcome %v", out)
	}
	if err := h.domainOf().pool.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if row, err := repo.InboxGet(t.Context(), add.Row.ID); err != nil || row.Status != discovery.InboxExpired {
		t.Fatalf("row %s (%v)", row.Status, err)
	}
}

func TestEvaluationProductionKnowledgeReachesModel(t *testing.T) {
	var saw atomic.Bool
	fake := llm.NewFake()
	fake.SetGenerator(func(task, prompt string) (string, bool) {
		if strings.Contains(prompt, "Harbor survey exporter evidence fixture") {
			saw.Store(true)
		}
		return evaluate.FakeGenerator(task, prompt)
	})
	h := newDomainHarness(t, func(c *Config) { c.Domain.LLM = append(c.Domain.LLM, llm.WithFake(fake)) })
	writeKnowledge(t, h, "knowledge/projects/harbor.md", "---\nid: harbor\n---\n\nHarbor survey exporter evidence fixture. Built backend services in Go and PostgreSQL.")
	r := h.startEvaluation("knowledge-evaluation-01", map[string]any{"text": postingBody("knowledge") + " Backend services in Go and PostgreSQL.", "company": "Example Systems", "title": "Software Engineer"})
	if r.StatusCode != 202 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	out := h.waitEvaluation(r.data()["runId"].(string))
	if out["status"] != "succeeded" || !saw.Load() {
		t.Fatalf("model evidence absent: %v saw=%v", out, saw.Load())
	}
}
