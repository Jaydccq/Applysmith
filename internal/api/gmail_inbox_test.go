package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/gmail/classify"
	"applysmith/internal/llm"
)

type apiSignal struct {
	id, thread, typ, tier, dir, employer, role string
	at                                         time.Time
	due                                        time.Time
}

// seedGmail stores signals the way the sync's store stage does and marks a
// finished sync.
func seedGmail(t *testing.T, h *harness, sigs ...apiSignal) {
	t.Helper()
	st := h.srv.store.Load()
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO gmail_runs (id, trigger_kind, status, mode, started_at, classifier_version, query_version, stages_json, counts_json)
			VALUES (1, 'cli', 'succeeded', 'bootstrap', '2027-01-01T00:00:00.000Z', ?, 'q', '[]', '{}')`, classify.Version); err != nil {
			return err
		}
		for _, s := range sigs {
			if s.thread == "" {
				s.thread = "t-" + s.id
			}
			if s.tier == "" {
				s.tier = classify.TierActionable
			}
			if s.dir == "" {
				s.dir = classify.DirIncoming
			}
			if s.employer == "" {
				s.employer = "Example Corp"
			}
			at := s.at.UTC().Format("2006-01-02T15:04:05.000Z")
			var due any
			if !s.due.IsZero() {
				due = s.due.UnixMilli()
			}
			if _, err := tx.Exec(`INSERT INTO gmail_evidence (message_id, thread_id, received_ms, from_header, to_header, cc_header, subject, snippet, labels, first_run_id, first_seen_at)
				VALUES (?, ?, ?, 'Recruiter <jobs@example.org>', '', '', ?, 'a short snippet', '[]', 1, ?)`, s.id, s.thread, s.at.UnixMilli(), "Update from "+s.employer, at); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO gmail_decisions (message_id, classifier_version, outcome, signal_type, tier, direction, employer, role, confidence, due_ms, rule_id, run_id, decided_at)
				VALUES (?, ?, 'signal', ?, ?, ?, ?, ?, 0.9, ?, 'test.rule', 1, ?)`, s.id, classify.Version, s.typ, s.tier, s.dir, s.employer, s.role, due, at); err != nil {
				return err
			}
		}
		_, err := tx.Exec(`INSERT INTO gmail_state (key, value) VALUES ('cursor', '1'), ('cursor_updated_at', '2027-01-01T00:00:00.000Z') ON CONFLICT(key) DO NOTHING`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func statusOf(h *harness, row map[string]any) any {
	return h.get("/api/candidacies/" + idOf(row)).data()["entry"].(map[string]any)["status"]
}

func fakeWith(f ...llm.Fixture) func(*Config) {
	return func(c *Config) { c.Domain.LLM = append(c.Domain.LLM, llm.WithFake(llm.NewFake(f...))) }
}

func TestAPI_Gmail_InboxPagingAndFilters(t *testing.T) {
	h, _ := gmailHarness(t)
	now := h.clock.now()
	var sigs []apiSignal
	for i := range 5 {
		sigs = append(sigs, apiSignal{id: fmt.Sprintf("r%d", i), typ: classify.TypeReplyNeeded, at: now.Add(-time.Duration(i+1) * time.Hour)})
	}
	sigs = append(sigs, apiSignal{id: "rc", typ: classify.TypeApplied, at: now.Add(-5 * 24 * time.Hour)},
		apiSignal{id: "of", typ: classify.TypeOffer, at: now.Add(-24 * time.Hour)})
	seedGmail(t, h, sigs...)
	r := h.get("/api/gmail/inbox")
	var page wire.Success[wire.GmailInboxPage]
	strictInto(t, r.body, &page)
	if r.StatusCode != 200 || len(page.Data.Threads) != 7 || page.Data.Threads[0].ThreadID != "t-of" || page.Data.Threads[0].Attention != "urgent" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	seen := map[string]bool{}
	cursor := ""
	for range 5 {
		var page wire.Success[wire.GmailInboxPage]
		strictInto(t, h.get("/api/gmail/inbox?limit=3&cursor="+cursor).body, &page)
		for _, th := range page.Data.Threads {
			if seen[th.ThreadID] {
				t.Fatalf("%s twice", th.ThreadID)
			}
			seen[th.ThreadID] = true
		}
		if cursor = page.Data.Next; cursor == "" {
			break
		}
	}
	if len(seen) != 7 {
		t.Errorf("paged %d threads", len(seen))
	}
	for _, bad := range []string{"lane=nope", "limit=0"} {
		if r := h.get("/api/gmail/inbox?" + bad); r.StatusCode != http.StatusUnprocessableEntity || r.errCode() != "validation_failed" {
			t.Errorf("%s: %d %s", bad, r.StatusCode, r.body)
		}
	}
	if r := h.get("/api/gmail/inbox/t-of"); r.StatusCode != 200 || !strings.Contains(string(r.body), `"timeline"`) {
		t.Errorf("detail: %d %s", r.StatusCode, r.body)
	}
	if r := h.get("/api/gmail/inbox/none"); r.StatusCode != 404 {
		t.Errorf("unknown thread: %d %s", r.StatusCode, r.body)
	}
}

// The thread decisions answer their statuses; what each does to the lanes is
// internal/gmail's inbox_test.go.
func TestAPI_Gmail_ThreadDecisions(t *testing.T) {
	h, _ := gmailHarness(t)
	now := h.clock.now()
	seedGmail(t, h, apiSignal{id: "r0", typ: classify.TypeReplyNeeded, employer: "Northwind Example", at: now.Add(-time.Hour)},
		apiSignal{id: "r1", typ: classify.TypeReplyNeeded, employer: "Other Co", at: now.Add(-2 * time.Hour)},
		apiSignal{id: "ap", typ: classify.TypeApplied, employer: "Other Co", at: now.Add(-3 * time.Hour)})
	var done wire.Success[wire.GmailThreadDone]
	r := h.post("/api/gmail/inbox/t-r0/done", nil)
	strictInto(t, r.body, &done)
	if r.StatusCode != 200 || done.Data.ThreadID != "t-r0" {
		t.Fatalf("done: %d %s", r.StatusCode, r.body)
	}
	for path, want := range map[string]int{"/api/gmail/inbox/t-ap/done": 409, "/api/gmail/inbox/none/done": 404, "/api/gmail/inbox/none/dismiss": 404} {
		if r := h.post(path, nil); r.StatusCode != want {
			t.Errorf("%s: %d %s", path, r.StatusCode, r.body)
		}
	}
	if r := h.post("/api/gmail/inbox/t-r0/reopen", nil); r.StatusCode != 200 {
		t.Fatalf("reopen: %d %s", r.StatusCode, r.body)
	}
	var gone wire.Success[wire.GmailThreadDismissed]
	r = h.post("/api/gmail/inbox/t-r1/dismiss", nil)
	strictInto(t, r.body, &gone)
	if r.StatusCode != 200 || !slices.Equal(gone.Data.SignalIDs, []string{"r1"}) {
		t.Fatalf("dismiss: %d %s", r.StatusCode, r.body)
	}
	if r := h.post("/api/gmail/links/r1/restore", nil); r.StatusCode != 200 {
		t.Fatalf("undo dismiss: %d %s", r.StatusCode, r.body)
	}
}

func TestAPI_Gmail_ThreadSummary(t *testing.T) {
	good := "Example Corp asked you to reply about the Backend Engineer role."
	h, _ := gmailHarness(t, fakeWith(llm.Fixture{Task: "gmail-summary", Contains: "MAIL", Steps: []llm.Step{{Text: good}}}))
	seedGmail(t, h, apiSignal{id: "a", thread: "t1", typ: classify.TypeReplyNeeded, role: "Backend Engineer", at: h.clock.now().Add(-time.Hour)})
	var first wire.Success[wire.GmailSummary]
	r := h.get("/api/gmail/threads/t1/summary")
	strictInto(t, r.body, &first)
	if r.StatusCode != 200 || first.Data.Method != "model" || first.Data.Cached || first.Data.Text != good {
		t.Fatalf("first = %d %s", r.StatusCode, r.body)
	}
	if r := h.get("/api/gmail/threads/missing/summary"); r.StatusCode != 404 {
		t.Errorf("unknown thread: %d %s", r.StatusCode, r.body)
	}
}

func TestAPI_Gmail_LinkReviewConfirmAndReject(t *testing.T) {
	h, _ := gmailHarness(t)
	a := h.addCandidacy("Example Corp", "Backend Engineer", "")
	h.addCandidacy("Example Corp", "Data Scientist", "")
	if r := h.patchRow("/api/candidacies/"+idOf(a), map[string]any{"status": "applied"}); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	now := h.clock.now()
	seedGmail(t, h,
		apiSignal{id: "m1", thread: "t1", typ: classify.TypeReplyNeeded, at: now.Add(-48 * time.Hour)},
		apiSignal{id: "m2", thread: "t2", typ: classify.TypeReplyNeeded, tier: classify.TierReview, at: now.Add(-24 * time.Hour), employer: "Unrelated Ltd"})
	if h.domainOf().gmail.Syncer.Linker == nil {
		t.Fatal("the sync has no link stage")
	}
	if _, err := h.domainOf().gmail.Pipeline.Link(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	var list wire.Success[wire.GmailLinkList]
	strictInto(t, h.get("/api/gmail/links?decision=review").body, &list)
	if len(list.Data.Items) != 1 || list.Data.Items[0].SignalID != "m1" || list.Data.Items[0].Reasons[0] != "ambiguous_runner_up" {
		t.Fatalf("review = %+v", list.Data.Items)
	}
	for _, bad := range []string{"", "?decision=x"} {
		if r := h.get("/api/gmail/links" + bad); r.StatusCode != 422 {
			t.Errorf("links%s: %d", bad, r.StatusCode)
		}
	}
	// Confirm applies the outcome with actor gmail.
	var conf wire.Success[wire.GmailLinkConfirmed]
	r := h.post("/api/gmail/links/m1/confirm", wire.GmailLinkConfirm{CandidacyID: 1})
	strictInto(t, r.body, &conf)
	if r.StatusCode != 200 || conf.Data.Outcome == nil || conf.Data.Outcome.Result != "changed" || conf.Data.Outcome.ToStatus != "responded" {
		t.Fatalf("confirm = %d %s", r.StatusCode, r.body)
	}
	if got := statusOf(h, a); got != "responded" {
		t.Errorf("candidacy = %v", got)
	}
	if r := h.post("/api/gmail/links/m2/confirm", wire.GmailLinkConfirm{}); r.StatusCode != 422 {
		t.Errorf("no candidacy: %d", r.StatusCode)
	}
	// Reject hides the signal from the inbox but keeps it in the audit data.
	if r := h.post("/api/gmail/links/m2/reject", nil); r.StatusCode != 200 {
		t.Fatalf("reject: %d %s", r.StatusCode, r.body)
	}
	// Restore undoes the reject: the signal is in the inbox again.
	var back wire.Success[wire.GmailLinkRestored]
	r = h.post("/api/gmail/links/m2/restore", nil)
	strictInto(t, r.body, &back)
	if r.StatusCode != 200 || back.Data.SignalID != "m2" {
		t.Fatalf("restore: %d %s", r.StatusCode, r.body)
	}
}

func TestAPI_Gmail_OfferConfirmationRoutes(t *testing.T) {
	h, _ := gmailHarness(t)
	c := h.addCandidacy("Example Corp", "Backend Engineer", "")
	h.patchRow("/api/candidacies/"+idOf(c), map[string]any{"status": "interview"})
	seedGmail(t, h, apiSignal{id: "o1", thread: "t1", typ: classify.TypeOffer, role: "Backend Engineer", at: h.clock.now().Add(-time.Hour)})
	if _, err := h.domainOf().gmail.Pipeline.Link(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	var list wire.Success[wire.GmailConfirmationList]
	strictInto(t, h.get("/api/gmail/confirmations").body, &list)
	if len(list.Data.Confirmations) != 1 || list.Data.Confirmations[0].Status != "offer" {
		t.Fatalf("confirmations = %+v", list.Data)
	}
	// The request names the tracker row it would move, not only its number.
	if got := list.Data.Confirmations[0]; got.Company != "Example Corp" || got.Role != "Backend Engineer" {
		t.Errorf("confirmation names %q / %q, want the linked row's company and role", got.Company, got.Role)
	}
	if statusOf(h, c) != "interview" {
		t.Fatal("the offer signal moved the status")
	}
	id := fmt.Sprint(list.Data.Confirmations[0].ID)
	if r := h.post("/api/gmail/confirmations/"+id+"/confirm", nil); r.StatusCode != 200 {
		t.Fatalf("confirm: %d %s", r.StatusCode, r.body)
	}
	if statusOf(h, c) != "offer" {
		t.Error("the confirmed offer did not move the row")
	}
	if r := h.post("/api/gmail/confirmations/"+id+"/dismiss", nil); r.StatusCode != 404 {
		t.Errorf("a decided request: %d", r.StatusCode)
	}
}

// GET /api/gmail/moves lists the statuses mail moved on its own in the last
// two weeks, with the row's company and the status it has now. The thread's
// timeline offers the same move back by the same rule.
func TestAPI_Gmail_MovesListWhatMailMoved(t *testing.T) {
	h, _ := gmailHarness(t)
	c := h.addCandidacy("Example Corp", "Backend Engineer", "")
	h.patchRow("/api/candidacies/"+idOf(c), map[string]any{"status": "applied"})
	seedGmail(t, h, apiSignal{id: "r1", thread: "t1", typ: classify.TypeRejection, role: "Backend Engineer", at: h.clock.now().Add(-time.Hour)},
		apiSignal{id: "u1", thread: "t2", typ: classify.TypeReplyNeeded, employer: "Unrelated Ltd", at: h.clock.now().Add(-time.Hour)})
	if _, err := h.domainOf().gmail.Pipeline.Link(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	moves := func() []wire.GmailMove {
		var list wire.Success[wire.GmailMoveList]
		strictInto(t, h.get("/api/gmail/moves").body, &list)
		return list.Data.Moves
	}
	timeline := func() wire.GmailTimelineEntry {
		var detail wire.Success[wire.GmailThreadDetail]
		strictInto(t, h.get("/api/gmail/inbox/t1").body, &detail)
		return detail.Data.Timeline[0]
	}
	if m, e := moves(), timeline(); len(m) != 1 || !m[0].PutBack || !e.PutBack || e.RowStatus != "rejected" {
		t.Fatalf("the row's latest change, moves = %+v, timeline = %+v", m, e)
	}
	h.patchRow("/api/candidacies/"+idOf(c), map[string]any{"status": "applied"})
	if e := timeline(); e.PutBack || e.RowStatus != "applied" {
		t.Fatalf("after the user's own change, timeline = %+v", e)
	}
	m := moves()
	if len(m) != 1 || m[0].ThreadID != "t1" || m[0].Company != "Example Corp" || m[0].Status != "applied" || m[0].PutBack ||
		m[0].Outcome.FromStatus != "applied" || m[0].Outcome.ToStatus != "rejected" || fmt.Sprint(*m[0].Outcome.CandidacyID) != idOf(c) {
		t.Fatalf("moves = %+v", m)
	}
	// Back at the status the mail gave by the user's own change, the move is
	// no longer the row's latest, so it is not offered back.
	h.patchRow("/api/candidacies/"+idOf(c), map[string]any{"status": "rejected"})
	if m, e := moves(), timeline(); len(m) != 1 || m[0].Status != "rejected" || m[0].PutBack || e.PutBack {
		t.Fatalf("after the user's own change to the same status, moves = %+v, timeline = %+v", m, e)
	}
	h.clock.advance(15 * 24 * time.Hour)
	if m := moves(); len(m) != 0 {
		t.Errorf("a move older than two weeks is listed: %+v", m)
	}
}

var digestKeys atomic.Int64

// makeDigest makes a brief now. The route answers 202 with a run; the brief
// is the newest one once the run ends.
func (h *harness) makeDigest() wire.GmailDigest {
	h.t.Helper()
	r := h.send("POST", "/api/gmail/digests/run", nil, "Idempotency-Key", fmt.Sprintf("key-digest-%d", digestKeys.Add(1)))
	id, _ := r.data()["runId"].(string)
	if r.StatusCode != 202 || id == "" {
		h.t.Fatalf("digest run: %d %s", r.StatusCode, r.body)
	}
	if run := h.waitRun(id); run["status"] != "succeeded" {
		h.t.Fatalf("digest run: %v", run)
	}
	var list wire.Success[wire.GmailDigestList]
	strictInto(h.t, h.get("/api/gmail/digests?limit=1").body, &list)
	if len(list.Data.Digests) != 1 {
		h.t.Fatalf("digests: %+v", list.Data)
	}
	return list.Data.Digests[0]
}

func TestAPI_Gmail_DigestRoutes(t *testing.T) {
	h, _ := unconnectedGmailHarness(t)
	// The scheduler's digest timer only ticks while Gmail is connected and could
	// otherwise produce a slot digest on the fake clock before the switch is off,
	// which moves the window past the seeded signal and leaves the manual run empty.
	off := false
	if r := h.send("PATCH", "/api/gmail/settings", wire.GmailSettingsPatch{ScheduledDigests: &off}); r.StatusCode != 200 {
		t.Fatal(r.StatusCode, string(r.body))
	}
	h.connectGmail()
	seedGmail(t, h, apiSignal{id: "a", thread: "t1", typ: classify.TypeReplyNeeded, at: h.clock.now().Add(-time.Hour)})
	d1 := h.makeDigest()
	if d1.Trigger != "manual" || len(d1.Items) != 1 || d1.Items[0].Key != "t1" || !strings.Contains(d1.Text, "Gmail digest") {
		t.Fatalf("run = %+v", d1)
	}
	if r := h.get("/api/gmail/digests/" + fmt.Sprint(d1.ID)); r.StatusCode != 200 {
		t.Fatalf("one: %d", r.StatusCode)
	}
	if r := h.get("/api/gmail/digests/999"); r.StatusCode != 404 {
		t.Errorf("unknown digest: %d", r.StatusCode)
	}
	path := "/api/gmail/digests/" + fmt.Sprint(d1.ID) + "/feedback"
	if r := h.post(path, wire.GmailDigestFeedback{ThreadKey: "t1", Verdict: "down"}); r.StatusCode != 200 {
		t.Fatalf("rate: %d %s", r.StatusCode, r.body)
	}
	if r := h.post(path, wire.GmailDigestFeedback{ThreadKey: "t1", Verdict: "meh"}); r.StatusCode != 422 {
		t.Errorf("bad verdict: %d", r.StatusCode)
	}
	var v wire.Success[wire.GmailDigestVerdicts]
	strictInto(t, h.get("/api/gmail/digests/feedback").body, &v)
	if v.Data.Verdicts["t1"] != "down" {
		t.Errorf("verdicts = %v", v.Data.Verdicts)
	}
}

// A reply draft is written on request for any thread and kept; it is the
// dashboard session's own call; with no model the page is told so.
func TestAPI_Gmail_ThreadDraftOnRequest(t *testing.T) {
	first := "Thank you for reaching out; I can talk on Thursday and will confirm by email."
	h, _ := gmailHarness(t, fakeWith(llm.Fixture{Task: "gmail-digest", Contains: "MAIL", Steps: []llm.Step{{Text: first}}}))
	now := h.clock.now()
	seedGmail(t, h, apiSignal{id: "a", thread: "t1", typ: classify.TypeReplyNeeded, role: "Backend Engineer", at: now.Add(-time.Hour)})
	draft := func(method string) (wire.GmailThreadDraft, int) {
		t.Helper()
		var env wire.Success[wire.GmailThreadDraft]
		r := h.do(call{method: method, path: "/api/gmail/threads/t1/draft", cookie: h.session(), headers: map[string]string{"Origin": h.srv.Origin()}})
		strictInto(t, r.body, &env)
		return env.Data, r.StatusCode
	}
	if d, code := draft("GET"); code != 200 || d.Draft != nil {
		t.Fatalf("a draft before any was asked for: %d %+v", code, d)
	}
	if r := h.post("/api/gmail/threads/t1/draft", nil); r.StatusCode != 403 || r.errReason() != wire.ReasonSessionOnly {
		t.Fatalf("the bearer token drafted: %d %s", r.StatusCode, r.body)
	}
	if d, code := draft("POST"); code != 200 || d.Draft == nil || d.Draft.Text != first || d.Draft.Kind != "reply" {
		t.Fatalf("draft = %d %+v", code, d)
	}
	if d, _ := draft("GET"); d.Draft == nil || d.Draft.Text != first {
		t.Errorf("the draft was not kept: %+v", d)
	}
	if r := h.do(call{method: "POST", path: "/api/gmail/threads/missing/draft", cookie: h.session(), headers: map[string]string{"Origin": h.srv.Origin()}}); r.StatusCode != 404 {
		t.Errorf("unknown thread: %d %s", r.StatusCode, r.body)
	}
	// No model routed: the same reason as the follow-up drafter.
	d, _ := gmailHarness(t)
	seedGmail(t, d, apiSignal{id: "a", thread: "t1", typ: classify.TypeReplyNeeded, at: d.clock.now().Add(-time.Hour)})
	r := d.do(call{method: "POST", path: "/api/gmail/threads/t1/draft", cookie: d.session(), headers: map[string]string{"Origin": d.srv.Origin()}})
	if msg, _ := r.json()["error"].(map[string]any)["message"].(string); r.errReason() != wire.ReasonNoModelRoute || msg == "" || strings.Contains(msg, "applysmith") {
		t.Errorf("no model: %d %s", r.StatusCode, r.body)
	}
}

// The tracker list carries each row's most pressing open thread from the
// inbox's own reading, and nothing for a row without one.
func TestAPI_Gmail_TrackerRowsCarryMailState(t *testing.T) {
	h, _ := gmailHarness(t)
	a := h.addCandidacy("Example Corp", "Backend Engineer", "")
	b := h.addCandidacy("Zeta Works", "Designer", "")
	for _, row := range []map[string]any{a, b} {
		if r := h.patchRow("/api/candidacies/"+idOf(row), map[string]any{"status": "applied"}); r.StatusCode != 200 {
			t.Fatalf("%d %s", r.StatusCode, r.body)
		}
	}
	now := h.clock.now()
	seedGmail(t, h, apiSignal{id: "m1", thread: "t1", typ: classify.TypeReplyNeeded, role: "Backend Engineer", at: now.Add(-50 * time.Hour)})
	if _, err := h.domainOf().gmail.Pipeline.Link(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	var list wire.Success[wire.CandidacyPage]
	strictInto(t, h.get("/api/candidacies").body, &list)
	got := map[string]*wire.CandidacyMail{}
	for _, e := range list.Data.Entries {
		got[e.Company] = e.Mail
	}
	m := got["Example Corp"]
	if m == nil || m.ThreadID != "t1" || m.Lane != "do_now" || m.Owner != "user" || m.NextAction == "" || m.AgeDays != 2 || m.Open != 1 || got["Zeta Works"] != nil {
		t.Errorf("mail state = %+v, %+v", m, got["Zeta Works"])
	}
	// A remote client cannot read /api/gmail/*, so the list hands it no mail.
	rq := httptest.NewRequest("GET", "/api/candidacies", nil)
	rq = rq.WithContext(context.WithValue(rq.Context(), listenerKey{}, listenTunnel))
	rep, err := h.srv.track.ListCandidacies(&apikit.Call{R: rq})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rep.Data.(wire.CandidacyPage).Entries {
		if e.Mail != nil {
			t.Errorf("a remote list carries mail for %s: %+v", e.Company, e.Mail)
		}
	}
}

// The mail filter narrows the list before paging by whose move the row's most
// pressing open thread is, so the total counts the narrowed rows; a remote
// client is refused it.
func TestAPI_Gmail_TrackerMailFilterNarrowsBeforePaging(t *testing.T) {
	h, _ := gmailHarness(t)
	now := h.clock.now()
	var sigs []apiSignal
	for i, c := range []struct{ company, typ, dir string }{
		{"Alpha", classify.TypeReplyNeeded, ""}, {"Bravo", classify.TypeReplyNeeded, ""}, {"Charlie", classify.TypeReplyNeeded, ""}, {"Delta", "", ""}} {
		row := h.addCandidacy(c.company, "Engineer", "")
		if r := h.patchRow("/api/candidacies/"+idOf(row), map[string]any{"status": "applied"}); r.StatusCode != 200 {
			t.Fatalf("%d %s", r.StatusCode, r.body)
		}
		if c.typ != "" {
			sigs = append(sigs, apiSignal{id: fmt.Sprint("in", i), thread: fmt.Sprint("t", i), typ: c.typ, employer: c.company, role: "Engineer", at: now.Add(-5 * time.Hour)})
		}
	}
	// Charlie's thread is answered: the next move is theirs.
	sigs = append(sigs, apiSignal{id: "out2", thread: "t2", typ: classify.TypeReplyNeeded, dir: classify.DirOutgoing, employer: "Charlie", role: "Engineer", at: now.Add(-time.Hour)})
	seedGmail(t, h, sigs...)
	if _, err := h.domainOf().gmail.Pipeline.Link(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	companies := func(query string) (names []string, total int) {
		var list wire.Success[wire.CandidacyPage]
		strictInto(t, h.get("/api/candidacies?"+query).body, &list)
		for _, e := range list.Data.Entries {
			names = append(names, e.Company)
		}
		slices.Sort(names)
		return names, int(list.Data.Total)
	}
	if got, total := companies("mail=needs_me"); !slices.Equal(got, []string{"Alpha", "Bravo"}) || total != 2 {
		t.Errorf("needs_me = %v of %d", got, total)
	}
	if got, total := companies("mail=waiting"); !slices.Equal(got, []string{"Charlie"}) || total != 1 {
		t.Errorf("waiting = %v of %d", got, total)
	}
	if r := h.get("/api/candidacies?mail=bogus"); r.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("an unknown mail filter: %d", r.StatusCode)
	}
	rq := httptest.NewRequest("GET", "/api/candidacies?mail=needs_me", nil)
	rq = rq.WithContext(context.WithValue(rq.Context(), listenerKey{}, listenTunnel))
	if _, err := h.srv.track.ListCandidacies(&apikit.Call{R: rq}); err == nil {
		t.Error("a remote client was given the mail filter")
	}
}
