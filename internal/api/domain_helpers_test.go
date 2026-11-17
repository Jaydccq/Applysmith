package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"applysmith/internal/evaluate"
	"applysmith/internal/llm"
	"applysmith/internal/notify/telegram"
	"applysmith/internal/tracker"
)

// skipClock is a telegram.Clock whose Sleep advances its own time at once, so
// the gateway's one-message-a-second spacing and backoffs cost no real time.
// The short real pause keeps a poll loop from spinning.
type skipClock struct {
	mu  sync.Mutex
	off time.Duration
}

func (c *skipClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.off)
}

func (c *skipClock) Sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.off += d
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Millisecond):
		return nil
	}
}

var _ telegram.Clock = (*skipClock)(nil)

// gateReader is an evaluate.Reader that blocks until released, so a test can
// hold a job in its reading phase.
type gateReader struct {
	text    string
	release chan struct{}
	mu      sync.Mutex
	reads   []string
}

func newGateReader(text string) *gateReader {
	return &gateReader{text: text, release: make(chan struct{})}
}

func (g *gateReader) Read(ctx context.Context, rawURL string) (evaluate.PageRead, error) {
	g.mu.Lock()
	g.reads = append(g.reads, rawURL)
	g.mu.Unlock()
	select {
	case <-g.release:
	case <-ctx.Done():
		return evaluate.PageRead{}, context.Cause(ctx)
	}
	return evaluate.PageRead{Title: "Software Engineer", Company: "Example Systems", Location: "Remote - United States", Text: g.text, Method: "http", Result: "live"}, nil
}

// immediateReader serves the posting text at once.
type immediateReader struct{ text string }

func (r immediateReader) Read(context.Context, string) (evaluate.PageRead, error) {
	return evaluate.PageRead{Title: "Software Engineer", Company: "Example Systems", Location: "Remote - United States", Text: r.text, Method: "http", Result: "live"}, nil
}

// postingText is synthetic posting text of more than 200 characters.
func postingBody(marker string) string {
	return "Software Engineer\nCompany: Example Systems\nLocation: Remote - United States\n\n" +
		"Example Systems builds scheduling software for clinics. You will design and build backend services in Go, own their reliability in production, " +
		"and work with a small team on a shared roadmap. We value clear writing, careful design, and steady delivery.\n\n" +
		"Requirements: three or more years building production backend services; experience with SQL databases; comfort reading other people's code. Marker " + marker + "."
}

// newDomainHarness is a ready server whose model calls go to the fake provider
// and whose workspace is seeded.
func newDomainHarness(t *testing.T, mods ...func(*Config)) *harness {
	t.Helper()
	return startDomainHarness(t, true, mods...)
}

// newRoutingHarness is the same server with model routing as configured
// rather than simulated offline, for the Models routes.
func newRoutingHarness(t *testing.T) *harness {
	t.Helper()
	return startDomainHarness(t, false)
}

func startDomainHarness(t *testing.T, offline bool, mods ...func(*Config)) *harness {
	t.Helper()
	fake := llm.NewFake()
	fake.SetGenerator(evaluate.FakeGenerator)
	use := func(c *Config) {
		c.Domain.TelegramClock = &skipClock{}
		c.Domain.LLM = append(c.Domain.LLM, llm.WithFake(fake))
		if offline {
			c.Domain.LLM = append(c.Domain.LLM, llm.WithOfflineSimulation())
		}
		if c.Domain.Reader == nil {
			c.Domain.Reader = immediateReader{text: postingBody("read")}
		}
	}
	h := newBareHarness(t, append([]func(*Config){use}, mods...)...)
	if _, err := h.ws.Init(); err != nil {
		t.Fatal(err)
	}
	// Let startup timers finish before a test changes their inputs.
	h.domainOf().sched.Wait()
	return h
}

func withReader(r evaluate.Reader) func(*Config) { return func(c *Config) { c.Domain.Reader = r } }

// send makes a JSON request with the server's own token.
func (h *harness) send(method, path string, body any, headers ...string) result {
	h.t.Helper()
	c := call{method: method, path: path, useToken: true, headers: map[string]string{}}
	if body != nil {
		c.contentType, c.body = "application/json", jsonBody(body)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		c.headers[headers[i]] = headers[i+1]
	}
	return h.do(c)
}

func (h *harness) get(path string) result { return h.send("GET", path, nil) }

func (h *harness) post(path string, body any) result { return h.send("POST", path, body) }

var keySeq = struct {
	sync.Mutex
	n int
}{}

// freshKey is an Idempotency-Key no other test request used.
func freshKey() string {
	keySeq.Lock()
	defer keySeq.Unlock()
	keySeq.n++
	return fmt.Sprintf("test-key-%06d", keySeq.n)
}

// startEvaluation posts an evaluation under the key.
func (h *harness) startEvaluation(key string, body map[string]any) result {
	h.t.Helper()
	return h.send("POST", "/api/evaluations", body, "Idempotency-Key", key)
}

// waitEvaluation polls the outcome until the job has one.
func (h *harness) waitEvaluation(runID string) map[string]any {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	pause := newPollPause()
	for {
		r := h.get("/api/evaluations/" + runID)
		if d := r.data(); r.StatusCode == 200 && d["outcome"] != nil && d["outcome"] != "" {
			return d
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("evaluation %s did not finish: %s", runID, r.body)
		}
		pause()
	}
}

// evaluate runs one evaluation of a synthetic posting to its outcome.
func (h *harness) evaluate(marker string) map[string]any {
	h.t.Helper()
	r := h.startEvaluation(freshKey(), map[string]any{"text": postingBody(marker), "url": "https://jobs.example.com/p/" + marker, "company": "Example Systems", "title": "Software Engineer"})
	if r.StatusCode != 202 {
		h.t.Fatalf("start: %d %s", r.StatusCode, r.body)
	}
	return h.waitEvaluation(r.data()["runId"].(string))
}

// boardRevision reads the current revision of a topic from the board snapshot.
func (h *harness) boardRevision(topic string) int {
	h.t.Helper()
	s := h.openStream("/api/board/events", h.token(), "")
	defer s.close()
	m := s.next()
	var snap struct {
		Revisions map[string]int `json:"revisions"`
	}
	if m.name != "snapshot" {
		h.t.Fatalf("first event %+v", m)
	}
	if err := json.Unmarshal([]byte(m.data), &snap); err != nil {
		h.t.Fatal(err)
	}
	return snap.Revisions[topic]
}

// topicRev reads a topic's revision in memory, under the board's lock.
func (h *harness) topicRev(topic string) int64 {
	h.srv.board.mu.Lock()
	defer h.srv.board.mu.Unlock()
	return h.srv.board.revs[topic]
}

// domainOf returns the running product capabilities.
func (h *harness) domainOf() *domain {
	h.t.Helper()
	d := h.srv.domain.Load()
	if d == nil {
		h.t.Fatal("the domain is not built")
	}
	return d
}

func (h *harness) addCandidacy(company, role, urlText string) map[string]any {
	h.t.Helper()
	body := map[string]any{"company": company, "role": role}
	if urlText != "" {
		body["url"] = urlText
	}
	r := h.post("/api/candidacies", body)
	if r.StatusCode != 201 {
		h.t.Fatalf("add %s: %d %s", company, r.StatusCode, r.body)
	}
	return r.data()
}

func idOf(m map[string]any) string { return strconv.Itoa(int(m["id"].(float64))) }

func writeFile(path, text string) error { return os.WriteFile(path, []byte(text), 0o600) }

// upsertReport records a report in the tracker in a transaction of its own,
// as the evaluation does in its.
func (h *harness) upsertReport(ctx context.Context, r tracker.Report) (res tracker.UpsertResult, err error) {
	err = h.srv.store.Load().Write(ctx, func(tx *sql.Tx) (err error) {
		res, err = h.domainOf().tracker.UpsertReportIn(ctx, tx, r)
		return err
	})
	return res, err
}
