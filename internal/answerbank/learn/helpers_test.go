package learn

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

// clock is a settable test clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// counterRand returns a reproducible byte stream for minted ids.
type counterRand struct{ n byte }

func (r *counterRand) Read(p []byte) (int, error) {
	for i := range p {
		r.n++
		p[i] = r.n
	}
	return len(p), nil
}

type env struct {
	t     *testing.T
	s     *Service
	ws    *workspace.Workspace
	st    *store.Store
	bank  *answerbank.Service
	clk   *clock
	logs  *bytes.Buffer
	logMu sync.Mutex
}

type syncBuffer struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (b syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

var baseTime = time.Date(2031, 3, 4, 12, 0, 0, 0, time.UTC)

func newEnv(t *testing.T) *env {
	t.Helper()
	ws := workspace.Open(t.TempDir())
	if err := os.MkdirAll(ws.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(ws.StateDir(), store.DBFile), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{t: t, ws: ws, st: st, clk: &clock{t: baseTime}, logs: &bytes.Buffer{}}
	e.bank = answerbank.New(ws, st)
	log := slog.New(slog.NewTextHandler(syncBuffer{&e.logMu, e.logs}, nil))
	e.s = New(st, e.bank, Options{Now: e.clk.now, Log: log, Rand: &counterRand{}})
	t.Cleanup(e.s.Close)
	e.writeBank()
	e.exec(`INSERT INTO runs (id, kind, status, created_at, updated_at) VALUES (?, 'autofill', 'running', ?, ?)`, testRun, stamp(baseTime), stamp(baseTime))
	return e
}

// testRun is the run every env issues, and every fillUpload belongs to: a
// manual fill outside a run is never ground truth.
const testRun = "run_00000000000000000000aa01"

func (e *env) logged() string {
	e.logMu.Lock()
	defer e.logMu.Unlock()
	return e.logs.String()
}

// writeBank replaces the bank file with the given entry YAML.
func (e *env) writeBank(entries ...string) {
	e.t.Helper()
	text := "version: 1\nemployers: []\nentries:\n" + strings.Join(entries, "")
	if len(entries) == 0 {
		text = "version: 1\nemployers: []\nentries: []\n"
	}
	text += "overrides: {}\n"
	if err := os.WriteFile(e.ws.Path(workspace.FileAnswerBank), []byte(text), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) bankText() string {
	e.t.Helper()
	b, err := os.ReadFile(e.ws.Path(workspace.FileAnswerBank))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

// fixedEntry is bank YAML for a fixed entry answered by its examples.
func fixedEntry(id, answer string, examples []string, extra ...string) string {
	s := fmt.Sprintf("  - id: %s\n    description: %s\n    kind: fixed\n    answer: %q\n", id, "About "+id, answer)
	if len(examples) > 0 {
		s += "    examples:\n"
		for _, ex := range examples {
			s += fmt.Sprintf("      - %q\n", ex)
		}
	}
	for _, x := range extra {
		s += "    " + x + "\n"
	}
	return s
}

func (e *env) ingest(ups ...Upload) IngestResult {
	e.t.Helper()
	res, err := e.s.Ingest(context.Background(), ups)
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

var uploadSeq int

// eid is the lowercase UUID the route accepts as an event id, derived from a
// label so a test can name its events.
func eid(label string) string {
	sum := sha256.Sum256([]byte(label))
	h := hex.EncodeToString(sum[:16])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// issueRun inserts a run row and returns its id, a run the server issued.
func (e *env) issueRun() string {
	e.t.Helper()
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		e.t.Fatal(err)
	}
	id := "run_" + hex.EncodeToString(b[:])
	ts := stamp(baseTime)
	e.exec(`INSERT INTO runs (id, kind, status, created_at, updated_at) VALUES (?, 'autofill', 'running', ?, ?)`, id, ts, ts)
	return id
}

// postingSeq numbers the reports of the fixtures' events: each event is seen on
// a posting of its own unless the test names one.
var postingSeq int64

func nextPosting() int64 {
	postingSeq++
	return postingSeq
}

// fillUpload is a manual-fill upload with a fresh event id, on a posting of its
// own.
func fillUpload(host, question, value string) Upload {
	uploadSeq++
	return Upload{EventID: eid(fmt.Sprintf("fill-%d", uploadSeq)), Kind: UploadManualFill, Host: host, Question: question, ControlKind: "text",
		After: value, At: baseTime.Add(-time.Hour), TrustedInput: true, RunID: testRun, ReportNumber: nextPosting()}
}

func uncertainty(host, question, reason string) Upload {
	uploadSeq++
	return Upload{EventID: eid(fmt.Sprintf("unc-%d", uploadSeq)), Kind: UploadUncertainty, Host: host, Question: question, ControlKind: "text",
		Reason: reason, At: baseTime.Add(-time.Hour), ReportNumber: nextPosting()}
}

func (e *env) generate() GenerateResult {
	e.t.Helper()
	res, err := e.s.Generate(context.Background(), GenerateOptions{})
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func (e *env) candidates(state string) []Candidate {
	e.t.Helper()
	cs, err := e.s.Candidates(context.Background(), state)
	if err != nil {
		e.t.Fatal(err)
	}
	return cs
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.st.Read().QueryRow("SELECT count(*) FROM "+q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(q, args...)
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

// record stores events, each without a run or report on a posting of its own.
func (e *env) record(evs ...Event) {
	for i := range evs {
		if evs[i].RunID == "" && evs[i].ReportNumber == 0 {
			evs[i].ReportNumber = nextPosting()
		}
	}
	e.s.Record(context.Background(), evs...)
	e.s.Flush()
}

// abstain records n no-entry abstains of one question.
func (e *env) abstains(n int, question string) {
	e.t.Helper()
	for i := 0; i < n; i++ {
		e.record(Event{Kind: KindAbstained, At: e.clk.now().Add(-time.Duration(i+1) * time.Minute), Host: "jobs.example.com", Question: question,
			ControlKind: "text", Outcome: "abstained", Reason: "no-entry"})
	}
}

// fills posts n identical manual fills of one question.
func (e *env) fills(n int, question, value string) {
	e.t.Helper()
	var ups []Upload
	for i := 0; i < n; i++ {
		u := fillUpload("jobs.example.com", question, value)
		u.At = e.clk.now().Add(-time.Hour)
		ups = append(ups, u)
	}
	if res := e.ingest(ups...); len(res.Rejected) > 0 {
		e.t.Fatalf("rejected: %+v", res.Rejected)
	}
}
