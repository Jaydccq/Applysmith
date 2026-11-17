package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"applysmith/internal/answerbank/learn"
	"applysmith/internal/llm"
	"applysmith/internal/workspace"
)

const eventsPath = "/api/answer-bank/events"

// learnRun is the run every domain harness has issued; a manual fill is ground
// truth only inside a run.
const learnRun = "run_0123456789abcdef0123aa01"

// learnEventTime is just before the harness clock.
const learnEventTime = "2026-01-02T03:00:00.000Z"

// evtID is the lowercase UUID the route accepts as an event id, derived from a
// label so a test can name its events.
func evtID(label string) string {
	sum := sha256.Sum256([]byte(label))
	h := hex.EncodeToString(sum[:16])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// learnPosting numbers the reports of the fixtures' events: each event is seen
// on a posting of its own unless the test names one.
var learnPosting int

func learnEvent(id, kind string, extra map[string]any) map[string]any {
	learnPosting++
	ev := map[string]any{"eventId": evtID(id), "kind": kind, "host": "jobs.example.com", "question": "How did you hear about Example Corp?", "controlKind": "text",
		"at": learnEventTime, "reportNumber": learnPosting}
	if kind == "manual-fill" {
		ev["trustedInput"], ev["runId"] = true, learnRun
	}
	for k, v := range extra {
		ev[k] = v
	}
	return ev
}

// ensureLearnRun issues learnRun when the workspace has not got it yet.
func (h *harness) ensureLearnRun() {
	h.t.Helper()
	err := h.srv.store.Load().Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT OR IGNORE INTO runs (id, kind, status, created_at, updated_at) VALUES (?, 'autofill', 'running', ?, ?)`, learnRun, learnEventTime, learnEventTime)
		return err
	})
	if err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) postEvents(events ...map[string]any) result {
	h.t.Helper()
	h.ensureLearnRun()
	return h.post(eventsPath, map[string]any{"events": events})
}

func (h *harness) rowCount(table string) int {
	h.t.Helper()
	var n int
	if err := h.srv.store.Load().Read().QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

// dbHolds reports whether any column of any table holds the needle: the proof
// that a value never reached the database.
func (h *harness) dbHolds(needle string) bool {
	h.t.Helper()
	db := h.srv.store.Load().Read()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		h.t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			h.t.Fatal(err)
		}
		tables = append(tables, n)
	}
	rows.Close()
	for _, tb := range tables {
		cr, err := db.Query("SELECT * FROM " + tb)
		if err != nil {
			h.t.Fatal(err)
		}
		cols, _ := cr.Columns()
		for cr.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := cr.Scan(ptrs...); err != nil {
				h.t.Fatal(err)
			}
			for _, v := range vals {
				text := fmt.Sprint(v)
				if b, ok := v.([]byte); ok {
					text = string(b)
				}
				if strings.Contains(text, needle) {
					cr.Close()
					return true
				}
			}
		}
		cr.Close()
	}
	return false
}

func TestAnswerBank_Events_RouteBatchAcceptedOnce(t *testing.T) {
	h := newDomainHarness(t)
	var evs []map[string]any
	for i := 0; i < 10; i++ {
		evs = append(evs, learnEvent(fmt.Sprintf("evt-%d", i), "uncertainty", map[string]any{"reason": "no-entry", "question": fmt.Sprintf("Question %d", i)}))
	}
	first := h.postEvents(evs...)
	if first.StatusCode != 200 || first.data()["accepted"] != float64(10) || len(first.data()["duplicates"].([]any)) != 0 || len(first.data()["rejected"].([]any)) != 0 {
		t.Fatalf("%d %s", first.StatusCode, first.body)
	}
	second := h.postEvents(evs...)
	if second.data()["accepted"] != float64(10) || len(second.data()["duplicates"].([]any)) != 10 {
		t.Fatalf("the second post reports all ten as already accepted: %s", second.body)
	}
	if n := h.rowCount("learning_uploads"); n != 10 {
		t.Fatalf("%d events exist, want 10", n)
	}
}

func TestAnswerBank_Events_RouteOversizedBatch(t *testing.T) {
	h := newDomainHarness(t)
	var evs []map[string]any
	for i := 0; i < learn.MaxBatch+1; i++ {
		evs = append(evs, learnEvent(fmt.Sprintf("big-%d", i), "uncertainty", map[string]any{"reason": "no-entry"}))
	}
	r := h.postEvents(evs...)
	if r.StatusCode != 413 || r.errCode() != "payload_too_large" || h.rowCount("learning_uploads") != 0 || h.rowCount("answer_events") != 0 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestAnswerBank_Events_RouteSensitiveValueDropped(t *testing.T) {
	h := newDomainHarness(t)
	const secret = "HostileSecretValue"
	hostile := []map[string]any{
		learnEvent("s-dob", "manual-fill", map[string]any{"question": "Date of birth", "after": secret + "-dob", "before": secret + "-dobprior", "entryId": "date-of-birth"}),
		learnEvent("s-pw", "manual-fill", map[string]any{"question": "Create a password", "inputType": "password", "after": secret + "-pw"}),
		learnEvent("s-otp", "manual-fill", map[string]any{"question": "Verification code", "autocomplete": "one-time-code", "after": secret + "-otp"}),
		learnEvent("s-card", "manual-fill", map[string]any{"question": "Card number", "autocomplete": "cc-number", "after": secret + "-card"}),
		learnEvent("s-ssn", "manual-fill", map[string]any{"question": "Social Security Number", "after": secret + "-ssn"}),
		learnEvent("s-gender", "manual-fill", map[string]any{"question": "Gender", "controlKind": "select", "options": []string{"Female", "Male", "Decline to self-identify"}, "optionLabel": secret + "-gender", "after": secret + "-gender"}),
		learnEvent("s-name", "manual-fill", map[string]any{"question": "Full name", "after": secret + "-name"}),
		learnEvent("s-email", "manual-fill", map[string]any{"question": "Email address", "inputType": "email", "after": secret + "-email"}),
		learnEvent("s-corr", "correction", map[string]any{"question": "Gender", "controlKind": "select", "origin": "user-edit", "entryId": "salutation", "before": "Decline to self-identify", "after": secret + "-corr", "optionLabel": secret + "-corr"}),
	}
	r := h.postEvents(hostile...)
	if r.StatusCode != 200 || r.data()["accepted"] != float64(len(hostile)) {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if h.dbHolds(secret) {
		t.Fatal("a value of a withheld control reached the database")
	}
	list := h.get("/api/answer-bank/manual-fills")
	fills := list.data()["fills"].([]any)
	if len(fills) != 8 {
		t.Fatalf("%d fills: %s", len(fills), list.body)
	}
	for _, f := range fills {
		m := f.(map[string]any)
		if _, has := m["value"]; has || m["withheld"] == "" {
			t.Errorf("%v", m)
		}
		if _, has := m["optionLabel"]; has {
			t.Errorf("%v", m)
		}
		if _, has := m["priorValue"]; has {
			t.Errorf("%v", m)
		}
	}
	// The correction on the self-identification control was stored as a count for
	// its entry, without the value.
	var counted int
	if err := h.srv.store.Load().Read().QueryRow(`SELECT counted FROM answer_events WHERE entry_id = 'salutation' AND value IS NULL AND option_label IS NULL`).Scan(&counted); err != nil || counted != 1 {
		t.Fatalf("%d %v", counted, err)
	}
}

func TestAnswerBank_ManualFills_RouteListAndDelete(t *testing.T) {
	h := newDomainHarness(t)
	var evs []map[string]any
	for i := 0; i < 3; i++ {
		evs = append(evs, learnEvent(fmt.Sprintf("f-%d", i), "manual-fill", map[string]any{"question": "Preferred editor", "after": "Vim"}))
	}
	h.postEvents(evs...)
	// Generation runs after the response; ask for it explicitly so the test does not race it.
	h.domainOf().learn.Settle()
	gen := h.post("/api/answer-bank/candidates/generate", map[string]any{})
	if gen.StatusCode != 200 {
		t.Fatalf("%d %s", gen.StatusCode, gen.body)
	}
	cands := h.get("/api/answer-bank/candidates?state=pending").data()["candidates"].([]any)
	if len(cands) != 1 || cands[0].(map[string]any)["support"] != float64(3) {
		t.Fatalf("%v", cands)
	}
	list := h.get("/api/answer-bank/manual-fills")
	d := list.data()
	fills := d["fills"].([]any)
	if d["total"] != float64(3) || len(fills) != 3 {
		t.Fatalf("%s", list.body)
	}
	id := fills[0].(map[string]any)["id"].(float64)
	del := h.send("DELETE", fmt.Sprintf("/api/answer-bank/manual-fills/%d", int(id)), nil)
	if del.StatusCode != 200 || del.data()["id"] != id {
		t.Fatalf("%d %s", del.StatusCode, del.body)
	}
	// The candidate's support dropped by one.
	cands = h.get("/api/answer-bank/candidates?state=pending").data()["candidates"].([]any)
	if cands[0].(map[string]any)["support"] != float64(2) {
		t.Fatalf("%v", cands)
	}
	if r := h.send("DELETE", fmt.Sprintf("/api/answer-bank/manual-fills/%d", int(id)), nil); r.StatusCode != 404 || r.errCode() != "not_found" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func uncertaintyEvents(prefix, question string, n int) []map[string]any {
	var evs []map[string]any
	for i := 0; i < n; i++ {
		evs = append(evs, learnEvent(fmt.Sprintf("%s-%d", prefix, i), "uncertainty", map[string]any{"reason": "no-entry", "question": question}))
	}
	return evs
}

func TestAnswerBank_Candidates_RouteApproveWritesTheBank(t *testing.T) {
	h := seededBankHarness(t)
	h.postEvents(uncertaintyEvents("u", "How did you hear about Example Corp?", 3)...)
	h.domainOf().learn.Settle()
	var c map[string]any
	for _, x := range h.get("/api/answer-bank/candidates").data()["candidates"].([]any) {
		c = x.(map[string]any)
	}
	if c == nil || c["state"] != "pending" || c["type"] != "new-entry" || c["key"] != "new-entry:how did you hear about example corp" {
		t.Fatalf("%v", c)
	}
	// Nothing approved it.
	before, _ := os.ReadFile(h.ws.Path(workspace.FileAnswerBank))
	if strings.Contains(string(before), c["entryId"].(string)) {
		t.Fatal("the bank changed before an approval")
	}
	at := func(act string) string {
		return fmt.Sprintf("/api/answer-bank/candidates/%d/%s", int(c["id"].(float64)), act)
	}
	r := h.post(at("approve"), map[string]any{"answer": "Through a friend"})
	if r.StatusCode != 200 || r.data()["entryId"] != c["entryId"] || r.data()["candidate"].(map[string]any)["state"] != "approved" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	after, _ := os.ReadFile(h.ws.Path(workspace.FileAnswerBank))
	if !strings.Contains(string(after), c["entryId"].(string)) || !strings.Contains(string(after), "created_by: agent") || !strings.Contains(string(after), "needs_review: true") ||
		!strings.Contains(string(after), "Through a friend") {
		t.Fatalf("approval writes an agent-created entry for review with the user's answer:\n%s", after[len(after)-400:])
	}
	// Its undo takes the entry out and the suggestion waits again; a second is refused.
	if back := h.post(at("unapprove"), nil); back.StatusCode != 200 || back.data()["candidate"].(map[string]any)["state"] != "pending" {
		t.Fatalf("unapprove: %d %s", back.StatusCode, back.body)
	}
	if now, _ := os.ReadFile(h.ws.Path(workspace.FileAnswerBank)); strings.Contains(string(now), c["entryId"].(string)) {
		t.Fatal("the take-back left the entry")
	}
	if again := h.post(at("unapprove"), nil); again.StatusCode != 409 || again.errReason() != "not-approved" {
		t.Fatalf("second unapprove: %d %s", again.StatusCode, again.body)
	}
	if r := h.post(at("approve"), map[string]any{}); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	// Approving again is refused with its reason, and so is rejecting.
	for act, body := range map[string]any{"approve": map[string]any{}, "reject": nil} {
		again := h.post(at(act), body)
		if again.StatusCode != 409 || again.errCode() != "conflict" || again.errReason() != "not-pending" {
			t.Fatalf("%s: %d %s", act, again.StatusCode, again.body)
		}
	}
	if r := h.post("/api/answer-bank/candidates/9999/approve", map[string]any{}); r.StatusCode != 404 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestAnswerBank_Candidates_RouteRejectAndRestore(t *testing.T) {
	h := seededBankHarness(t)
	h.postEvents(uncertaintyEvents("r", "Which gadget do you prefer", 3)...)
	h.domainOf().learn.Settle()
	c := h.get("/api/answer-bank/candidates").data()["candidates"].([]any)[0].(map[string]any)
	rej := h.post(fmt.Sprintf("/api/answer-bank/candidates/%d/reject", int(c["id"].(float64))), nil)
	if rej.StatusCode != 200 || rej.data()["candidate"].(map[string]any)["state"] != "rejected" {
		t.Fatalf("%d %s", rej.StatusCode, rej.body)
	}
	// Its undo makes it wait again (flow round 12, U-1); a second is refused with its reason.
	back := h.post(fmt.Sprintf("/api/answer-bank/candidates/%d/restore", int(c["id"].(float64))), nil)
	if back.StatusCode != 200 || back.data()["candidate"].(map[string]any)["state"] != "pending" {
		t.Fatalf("restore: %d %s", back.StatusCode, back.body)
	}
	if again := h.post(fmt.Sprintf("/api/answer-bank/candidates/%d/restore", int(c["id"].(float64))), nil); again.StatusCode != 409 || again.errReason() != "not-rejected" {
		t.Fatalf("second restore: %d %s", again.StatusCode, again.body)
	}
}

func TestAnswerBank_Usage_RouteListsEveryEntryAndReadsDoNotMutate(t *testing.T) {
	h := seededBankHarness(t)
	bankBefore, _ := os.ReadFile(h.ws.Path(workspace.FileAnswerBank))
	l, _ := h.domainOf().bank.File()
	r := h.get("/api/answer-bank/usage")
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	rows := r.data()["rows"].([]any)
	if len(rows) != len(l.Bank.Entries) {
		t.Fatalf("%d rows for %d entries", len(rows), len(l.Bank.Entries))
	}
	first := rows[0].(map[string]any)
	if first["matches"] != float64(0) || first["lastActivityAt"] != "" || first["exampleQuestions"] == nil {
		t.Fatalf("%v", first)
	}
	bankAfter, _ := os.ReadFile(h.ws.Path(workspace.FileAnswerBank))
	if !bytes.Equal(bankBefore, bankAfter) {
		t.Fatal("reading usage modified answer-bank.yml")
	}
}

// issueRun inserts a run row and returns its id: a run the server issued.
func (h *harness) issueRun() string {
	h.t.Helper()
	id := "run_" + evtID(h.t.Name())[:8] + "0123456789abcdef"
	err := h.srv.store.Load().Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO runs (id, kind, status, created_at, updated_at) VALUES (?, 'autofill', 'running', ?, ?)`, id, learnEventTime, learnEventTime)
		return err
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return id
}

// The "Look for new suggestions" press answers at once and the suggestions are
// sorted after it: the list carries each review, the ones not worth an entry
// too, and a reusable one's draft from the user's own facts with its sources.
// The draft runs unsupervised, so a stored dangerous mode drafts as flex: a
// question none of the user's facts covers gets no draft.
func TestAnswerBank_Candidates_RouteGenerateThenReviewInTheBackground(t *testing.T) {
	const languagesQ, managerQ, gamesQ = "Which programming languages do you use for survey exporters?", "Who was your manager?", "Which board games do you play?"
	reusable := llm.Step{Text: `{"verdict": "reusable", "reason": "One answer fits every application."}`}
	fake := llm.NewFake(
		llm.Fixture{Task: "suggestion-review", Contains: languagesQ, Steps: []llm.Step{reusable}},
		llm.Fixture{Task: "suggestion-review", Contains: gamesQ, Steps: []llm.Step{reusable}},
		llm.Fixture{Task: "suggestion-review", Contains: managerQ, Steps: []llm.Step{{Text: `{"verdict": "per-application", "reason": "It depends on the past job the row describes."}`}}},
		llm.Fixture{Task: "narrative", Contains: languagesQ, Steps: []llm.Step{{Text: "I write Go at work: I built the reef survey exporter in Go."}}},
		llm.Fixture{Task: "narrative", Contains: gamesQ, Steps: []llm.Step{{Text: "I play chess and go with a weekly club near my home."}}})
	h := newDomainHarness(t, func(c *Config) { c.Domain.LLM = append(c.Domain.LLM, llm.WithFake(fake)) })
	writeKnowledge(t, h, "knowledge/projects/reef.md", reefSnippet)
	h.srv.kb.Refresh(t.Context())
	if r := h.send("PATCH", "/api/answer-bank/settings", map[string]any{"groundingMode": "dangerous"}); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	for _, q := range []string{languagesQ, managerQ, gamesQ} {
		for i := 0; i < 3; i++ {
			h.domainOf().learn.Record(context.Background(), learn.Event{Kind: learn.KindAbstained, At: h.clock.now().Add(-time.Duration(i+1) * time.Minute), Host: "jobs.example.com",
				Question: q, ControlKind: "text", Outcome: "abstained", Reason: "no-entry", ReportNumber: int64(i + 1)})
		}
	}
	h.domainOf().learn.Flush()
	if gen := h.post("/api/answer-bank/candidates/generate", map[string]any{}); gen.StatusCode != 200 || gen.data()["created"] != float64(3) {
		t.Fatalf("%d %s", gen.StatusCode, gen.body)
	}
	h.domainOf().learn.Settle()
	got := map[string]map[string]any{}
	for _, x := range h.get("/api/answer-bank/candidates?state=pending").data()["candidates"].([]any) {
		c := x.(map[string]any)
		got[c["seedQuestion"].(string)] = c["review"].(map[string]any)
	}
	drafted, row, games := got[languagesQ], got[managerQ], got[gamesQ]
	if drafted == nil || drafted["verdict"] != "reusable" || drafted["draft"] != "I write Go at work: I built the reef survey exporter in Go." ||
		!strings.Contains(fmt.Sprint(drafted["cited"]), "reef") {
		t.Fatalf("drafted: %v", drafted)
	}
	if games == nil || games["verdict"] != "reusable" || games["draft"] != "" {
		t.Fatalf("ungrounded: %v", games)
	}
	if row == nil || row["verdict"] != "per-application" || row["reason"] != "It depends on the past job the row describes." || row["draft"] != "" {
		t.Fatalf("per-application: %v", row)
	}
}
