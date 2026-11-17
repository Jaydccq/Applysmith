package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/gmail"
	"applysmith/internal/kb"
	"applysmith/internal/notify"
	"applysmith/internal/scheduler"
	"applysmith/internal/scheduler/jobs"
)

func TestNotify_Board_EvaluationRunAppearsWithTheRunIDAndFollowsItsEnd(t *testing.T) {
	h := newDomainHarness(t)
	start := h.startEvaluation(freshKey(), map[string]any{"text": postingBody("board1"), "url": "https://jobs.example.com/p/board1", "company": "Example Systems", "title": "Software Engineer"})
	runID, _ := start.data()["runId"].(string)
	if start.StatusCode != 202 || runID == "" {
		t.Fatalf("start: %d %s", start.StatusCode, start.body)
	}
	h.waitEvaluation(runID)
	list := h.get("/api/runs?kind=evaluate")
	var page wire.Success[wire.BoardEntryList]
	strictInto(t, list.body, &page)
	if len(page.Data.Entries) != 1 {
		t.Fatalf("entries = %+v", page.Data.Entries)
	}
	e := page.Data.Entries[0]
	if e.ID != runID || e.Kind != "evaluate" || e.Origin != "user" || e.State != "done" || e.Outcome == "" || e.Link != "/runs/"+e.ID {
		t.Fatalf("entry = %+v", e)
	}
	// GET /api/runs/{id} returns the run with its entry.
	r := h.get("/api/runs/" + e.ID)
	var rv wire.Success[wire.RunView]
	strictInto(t, r.body, &rv)
	if rv.Data.Entry == nil || rv.Data.Entry.ID != e.ID || rv.Data.RunID != e.ID {
		t.Fatalf("run view = %+v", rv.Data)
	}
	sum := h.get("/api/runs/summary")
	var s wire.Success[wire.BoardSummary]
	strictInto(t, sum.body, &s)
	if s.Data.DoneUnacknowledged != 1 {
		t.Errorf("summary = %+v", s.Data)
	}
	ack := h.post("/api/runs/"+e.ID+"/ack", nil)
	if ack.StatusCode != 200 {
		t.Fatalf("ack: %d %s", ack.StatusCode, ack.body)
	}
	strictInto(t, h.get("/api/runs/summary").body, &s)
	if s.Data.DoneUnacknowledged != 0 {
		t.Errorf("after the ack: %+v", s.Data)
	}
	var after wire.Success[wire.BoardEntryList]
	strictInto(t, h.get("/api/runs").body, &after)
	if len(after.Data.Entries) != 0 {
		t.Errorf("the default view still shows an acknowledged finished run")
	}
	var all wire.Success[wire.BoardEntryList]
	strictInto(t, h.get("/api/runs?acknowledged=true").body, &all)
	if len(all.Data.Entries) != 1 {
		t.Errorf("acknowledged=true shows %d", len(all.Data.Entries))
	}
}

func TestNotify_Board_ListFilterAndLimitAreValidated(t *testing.T) {
	h := newDomainHarness(t)
	for _, q := range []string{"state=sleeping", "limit=0"} {
		if r := h.get("/api/runs?" + q); r.StatusCode != 422 || r.errCode() != "validation_failed" {
			t.Errorf("%s: %d %s", q, r.StatusCode, r.body)
		}
	}
	if r := h.get("/api/runs?limit=2000"); r.StatusCode != 200 {
		t.Errorf("limit 2000: %d", r.StatusCode)
	}
}

// Every page a board entry, a notice or a schedule names is a page of the
// dashboard (web/src/pages.ts), so pressing it never lands on Not found.
func TestNotify_LinksNameDashboardPages(t *testing.T) {
	src, err := os.ReadFile("../../web/src/pages.ts")
	if err != nil {
		t.Fatal(err)
	}
	pages := map[string]bool{}
	for _, m := range regexp.MustCompile(`path: "(/[^"]*)"`).FindAllSubmatch(src, -1) {
		pages[string(m[1])] = true
	}
	link := regexp.MustCompile(`(?:Link|Artifact): *"(/[^"?#]*)`)
	err = filepath.WalkDir("..", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		b, err := os.ReadFile(p)
		for _, m := range link.FindAllSubmatch(b, -1) {
			if path := string(m[1]); !pages[strings.TrimSuffix(path, "/")] && path != "/" {
				t.Errorf("%s links to %s, which is no dashboard page", p, path)
			}
		}
		return err
	})
	if err != nil || len(pages) < 10 {
		t.Fatalf("read %d pages: %v", len(pages), err)
	}
}

// A Gmail that cannot be signed in to is one item, whether the sync's
// schedule or the Gmail stack saw it, and a schedule's own item reads once in
// the day: the demo's day said "Gmail sync every 10 minutes needs you needs
// something from you." beside the Gmail item.
func TestNotify_Inbox_AGmailSignInFailureIsOneItem(t *testing.T) {
	h := newDomainHarness(t)
	d := h.domainOf()
	ctx := context.Background()
	d.notify.scheduledNeedsYou(ctx, scheduler.NeedsYouItem{RunID: "run_g", ScheduleID: 8, Schedule: "Gmail sync every 10 minutes", Kind: "gmail.sync", Reason: jobs.ReasonNeedsGmailSignIn, Message: "Gmail refused the sign-in (Invalid credentials (Failure))"}, h.srv.log.Warn)
	h.srv.gmailHooks(d.notify).OnRefused(ctx, "Gmail refused the sign-in (Invalid credentials (Failure))")
	d.notify.scheduledNeedsYou(ctx, scheduler.NeedsYouItem{RunID: "run_s", ScheduleID: 9, Schedule: "Nightly scan", Kind: "discover.exa", Reason: "needs-you", Message: "the scan stopped"}, h.srv.log.Warn)
	var inbox wire.Success[wire.NeedsYouInbox]
	strictInto(t, h.get("/api/needs-you").body, &inbox)
	var ids []string
	for _, it := range inbox.Data.Items {
		ids = append(ids, it.Entry.ID)
	}
	if slices.Sort(ids); len(ids) != 2 || ids[0] != "gmail-sign-in" {
		t.Fatalf("inbox ids = %v, want the one Gmail item and the scan's", ids)
	}
	v := decode[wire.DayView](t, h.get("/api/day"))
	for _, l := range v.Left {
		if strings.Contains(l.Text, "needs you needs") {
			t.Errorf("day line %q", l.Text)
		}
	}
}

func TestNotify_Inbox_ScheduledNeedsYouAndGmailSignInAreItems(t *testing.T) {
	h := newDomainHarness(t)
	d := h.domainOf()
	ctx := context.Background()
	d.notify.scheduledNeedsYou(ctx, scheduler.NeedsYouItem{RunID: "run_x", ScheduleID: 3, Schedule: "Nightly Gmail", Kind: "gmail.sync", Reason: scheduler.ReasonNeedsSignIn, Message: "Gmail asked to sign in again"}, h.srv.log.Warn)
	gm := h.srv.gmailHooks(d.notify)
	gm.OnRefused(ctx, "Gmail refused the sign-in (Invalid credentials (Failure))")
	var inbox wire.Success[wire.NeedsYouInbox]
	strictInto(t, h.get("/api/needs-you").body, &inbox)
	if len(inbox.Data.Items) != 2 || inbox.Data.Unseen != 2 {
		t.Fatalf("inbox = %+v", inbox.Data)
	}
	var scheduled string
	for _, it := range inbox.Data.Items {
		if it.Entry.State != "needs-you" || it.Entry.Reason != "sign-in" || it.Action != wire.ActionOpenRun {
			t.Errorf("item = %+v", it)
		}
		if it.Entry.ID != "gmail-sign-in" {
			scheduled = it.Entry.ID
		} else if it.Entry.Detail != gmail.RefusalDetail {
			t.Errorf("the Gmail item says %q", it.Entry.Detail)
		}
	}
	// Connecting again resolves the Gmail item; opening the other marks it seen.
	gm.OnRestored(ctx)
	if r := h.post("/api/runs/"+scheduled+"/seen", nil); r.StatusCode != 200 {
		t.Fatalf("seen: %d %s", r.StatusCode, r.body)
	}
	strictInto(t, h.get("/api/needs-you").body, &inbox)
	if len(inbox.Data.Items) != 1 || inbox.Data.Unseen != 0 {
		t.Errorf("inbox after connecting again = %+v", inbox.Data)
	}
	// The run page of an entry without a run record still reads.
	if r := h.get("/api/runs/gmail-sign-in"); r.StatusCode != 200 {
		t.Errorf("entry page: %d %s", r.StatusCode, r.body)
	}
	if r := h.get("/api/runs/nope"); r.StatusCode != 404 {
		t.Errorf("unknown run: %d", r.StatusCode)
	}
}

func TestNotify_Desktop_AnnounceReplaceAndWithdrawOnTheStream(t *testing.T) {
	h := newDomainHarness(t)
	d := h.domainOf()
	ctx := context.Background()
	s := h.openStream("/api/notifications/events", h.token(), "")
	if _, err := d.notify.board.Open(ctx, notify.OpenArgs{ID: "ent_1", Kind: notify.KindApply, Origin: notify.OriginUser, Title: "Apply to Example Systems"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.notify.board.Move(ctx, "ent_1", notify.NeedsYou, notify.MoveArgs{Reason: "one-time-code", Detail: "Enter 123456"}); err != nil {
		t.Fatal(err)
	}
	m := s.next()
	var a wire.NotificationAnnounce
	if m.name != "announce" || json.Unmarshal([]byte(m.data), &a) != nil || a.ID != "ent_1" || !a.Urgent || a.Link != "/runs/ent_1" {
		t.Fatalf("event = %+v", m)
	}
	if strings.Contains(m.data, "123456") || !strings.Contains(a.Body, "verification code is needed") {
		t.Errorf("announcement leaks or is vague: %+v", a)
	}
	// Escalation replaces by identifier.
	if _, err := d.notify.board.Move(ctx, "ent_1", notify.Blocked, notify.MoveArgs{Reason: "lockout"}); err != nil {
		t.Fatal(err)
	}
	m = s.next()
	var b wire.NotificationAnnounce
	if m.name != "announce" || json.Unmarshal([]byte(m.data), &b) != nil || b.ID != "ent_1" || !strings.Contains(b.Body, "locked") {
		t.Fatalf("second event = %+v", m)
	}
	// The user acknowledges it from the announcement.
	if r := h.post("/api/notifications/ent_1/ack", nil); r.StatusCode != 200 {
		t.Fatalf("ack: %d %s", r.StatusCode, r.body)
	}
	m = s.next()
	var w wire.NotificationWithdraw
	if m.name != "withdraw" || json.Unmarshal([]byte(m.data), &w) != nil || w.ID != "ent_1" {
		t.Fatalf("third event = %+v", m)
	}
	if r := h.post("/api/notifications/n-99/ack", nil); r.StatusCode != 404 {
		t.Errorf("ack of an unknown announcement: %d", r.StatusCode)
	}
}

func TestNotify_Settings_DefaultsPartialPatchAndUnknownKeyRejected(t *testing.T) {
	h := newDomainHarness(t)
	var got wire.Success[wire.NotificationSettings]
	strictInto(t, h.get("/api/notifications/settings").body, &got)
	if got.Data != (wire.NotificationSettings{Desktop: true, Telegram: true}) {
		t.Fatalf("defaults = %+v", got.Data)
	}
	before := h.boardRevision("notifications")
	r := h.send("PATCH", "/api/notifications/settings", map[string]any{"failedScans": true})
	strictInto(t, r.body, &got)
	if r.StatusCode != 200 || !got.Data.FailedScans || !got.Data.Desktop || !got.Data.Telegram {
		t.Fatalf("patch: %d %+v", r.StatusCode, got.Data)
	}
	if h.boardRevision("notifications") <= before {
		t.Error("the notifications topic did not advance")
	}
	for name, body := range map[string]map[string]any{
		"wrong type":  {"telegram": "yes"},
		"unknown key": {"sms": true},
	} {
		bad := h.send("PATCH", "/api/notifications/settings", body)
		if bad.StatusCode != 422 || bad.errCode() != "validation_failed" {
			t.Errorf("%s: %d %s", name, bad.StatusCode, bad.body)
		}
	}
	strictInto(t, h.get("/api/notifications/settings").body, &got)
	if !got.Data.FailedScans || !got.Data.Telegram {
		t.Errorf("a rejected patch changed the settings: %+v", got.Data)
	}
}

func TestNotify_Test_SendsAtOnceAndSaysWhatToDoPerChannel(t *testing.T) {
	h := newDomainHarness(t)
	s := h.openStream("/api/notifications/events", h.token(), "")
	var got wire.Success[wire.NotificationTest]
	r := h.post("/api/notifications/test", nil)
	strictInto(t, r.body, &got)
	ch := got.Data.Channels
	if r.StatusCode != 200 || len(ch) != 2 || ch[0].Channel != "desktop" || ch[0].State != "sent" || ch[1].Channel != "telegram" || ch[1].State != "unavailable" ||
		!strings.Contains(ch[1].Detail, "No bot token") {
		t.Fatalf("test: %d %s", r.StatusCode, r.body)
	}
	if m := s.next(); m.name != "announce" || !strings.Contains(m.data, "Test notification") {
		t.Errorf("event = %+v", m)
	}
	h.send("PATCH", "/api/notifications/settings", map[string]any{"desktop": false})
	strictInto(t, h.post("/api/notifications/test", nil).body, &got)
	if got.Data.Channels[0].State != "off" {
		t.Errorf("desktop switched off: %+v", got.Data.Channels)
	}
}

func TestNotify_Inbox_NoKeyScanRaisesOneItemPerSourcePerDay(t *testing.T) {
	h := newDomainHarness(t)
	d := h.domainOf()
	ctx := context.Background()
	for _, run := range []string{"run_a", "run_b"} {
		d.notify.scheduledNeedsYou(ctx, scheduler.NeedsYouItem{RunID: run, Schedule: "Daily scan · exa", Kind: "discover.exa", Reason: jobs.ReasonNeedsKey, Message: "the exa scan has no key"}, h.srv.log.Warn)
	}
	d.notify.scheduledNeedsYou(ctx, scheduler.NeedsYouItem{RunID: "run_c", Schedule: "Daily scan · theirstack", Kind: "discover.theirstack", Reason: jobs.ReasonNeedsKey, Message: "the theirstack scan has no key"}, h.srv.log.Warn)
	var inbox wire.Success[wire.NeedsYouInbox]
	strictInto(t, h.get("/api/needs-you").body, &inbox)
	if len(inbox.Data.Items) != 2 {
		t.Fatalf("%d items, want one per source", len(inbox.Data.Items))
	}
	// The day in the id is the server clock's.
	for _, it := range inbox.Data.Items {
		if day := h.clock.now().UTC().Format("2006-01-02"); !strings.HasSuffix(it.Entry.ID, "-"+day) {
			t.Errorf("item %s is not of %s", it.Entry.ID, day)
		}
	}
}

// A schedule's failures that wait for the user are one item per reason until a
// later run of that schedule succeeds; another schedule's item stays, and a
// failure after the success raises the item again.
func TestNotify_Inbox_ScheduledNeedsYouIsOneItemPerScheduleAndReasonUntilItRecovers(t *testing.T) {
	h := newDomainHarness(t)
	d := h.domainOf()
	ctx := context.Background()
	fail := func(run string, schedule int64, reason string) {
		d.notify.scheduledNeedsYou(ctx, scheduler.NeedsYouItem{RunID: run, ScheduleID: schedule, Schedule: fmt.Sprint("Scan ", schedule), Kind: "discover.linkedin",
			Reason: reason, Message: fmt.Sprintf("schedule %d: %s", schedule, reason)}, h.srv.log.Warn)
	}
	waiting := func() []string {
		var inbox wire.Success[wire.NeedsYouInbox]
		strictInto(t, h.get("/api/needs-you").body, &inbox)
		var got []string
		for _, it := range inbox.Data.Items {
			got = append(got, it.Entry.Detail)
		}
		slices.Sort(got)
		return got
	}
	fail("run_1", 1, scheduler.ReasonNeedsSignIn)
	fail("run_2", 1, scheduler.ReasonNeedsSignIn)
	fail("run_3", 1, "challenge")
	fail("run_4", 2, scheduler.ReasonNeedsSignIn)
	if got, want := waiting(), []string{"schedule 1: challenge", "schedule 1: needs-sign-in", "schedule 2: needs-sign-in"}; !slices.Equal(got, want) {
		t.Fatalf("inbox %q, want %q", got, want)
	}
	d.notify.scheduledRecovered(ctx, 1, h.srv.log.Warn)
	if got, want := waiting(), []string{"schedule 2: needs-sign-in"}; !slices.Equal(got, want) {
		t.Fatalf("after schedule 1 succeeded: %q, want %q", got, want)
	}
	fail("run_5", 1, scheduler.ReasonNeedsSignIn)
	if got, want := waiting(), []string{"schedule 1: needs-sign-in", "schedule 2: needs-sign-in"}; !slices.Equal(got, want) {
		t.Fatalf("a failure after the success: %q, want %q", got, want)
	}
}

// The tray's quit check reads the summary with a three-second deadline. The
// summary first lets the board follow the run records, and that writes; a busy
// writer must not make the read outlast the caller (the observed quit dialog
// "activity could not be checked": a 503 after 5.6 s on a locked screen).
func TestNotify_Summary_DoesNotWaitForABusyWriter(t *testing.T) {
	h := newDomainHarness(t)
	now := h.clock.now().UTC().Format("2006-01-02T15:04:05.000Z")
	st := h.srv.store.Load()
	execAll(t, st, `INSERT INTO runs (id, kind, status, stage, created_at, updated_at) VALUES ('run_busy1', 'scan', 'running', 'sweep', '`+now+`', '`+now+`')`)
	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = st.Write(context.Background(), func(*sql.Tx) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	defer func() { close(release); <-done }()
	start := time.Now()
	r := h.get("/api/runs/summary")
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("the summary waited %v for the writer", took)
	}
	var sum wire.Success[wire.BoardSummary]
	strictInto(t, r.body, &sum)
	if r.StatusCode != 200 {
		t.Fatalf("summary: %d %s", r.StatusCode, r.body)
	}
}

// Needs you's counts are what Home read whole lists for: the pending email
// status changes, the unpublished wiki drafts, the held hiring signals, and
// the suggested answers and hand edits the Answer Bank page offers.
func TestNeedsYouCountsAreTheCountsOfTheListsTheyStandFor(t *testing.T) {
	h := newDomainHarness(t)
	seedSignal(t, h)
	var drafts []int64
	for _, id := range []string{"pager", "harbor", "reef"} {
		d, err := h.domainOf().kb.CreateDraft(t.Context(), kb.NewDraft{Kind: kb.DraftStory, SnippetID: id, Markdown: "---\nid: " + id + "\n---\nText.\n"})
		if err != nil {
			t.Fatal(err)
		}
		drafts = append(drafts, d.ID)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	err := h.srv.store.Load().Write(t.Context(), func(tx *sql.Tx) error {
		// A published draft waits for nothing.
		if _, err := tx.Exec(`UPDATE kb_drafts SET status = 'published' WHERE id = ?`, drafts[2]); err != nil {
			return err
		}
		for i, state := range []string{"pending", "pending", "dismissed"} {
			if _, err := tx.Exec(`INSERT INTO gmail_confirmations (message_id, thread_id, candidacy_id, status, state, created_at) VALUES (?, 't', 1, 'offer', ?, ?)`, fmt.Sprint("m", i), state, now); err != nil {
				return err
			}
		}
		for i, verdict := range []string{"", "reusable", "trap"} {
			if _, err := tx.Exec(`INSERT INTO answer_candidates (key, type, source, seed_question, questions_json, hosts_json, support_count, first_seen_at, last_seen_at, created_at, updated_at, review_verdict)
				VALUES (?, 'new-entry', 'model-fill', ?, '[]', '[]', 3, ?, ?, ?, ?, ?)`, fmt.Sprint("k", i), fmt.Sprintf("Which harbor berth solver number %d did you design?", i), now, now, now, now, verdict); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var got wire.Success[wire.NeedsYouCounts]
	if r := h.get("/api/needs-you/counts"); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	} else {
		strictInto(t, r.body, &got)
	}
	var cands wire.Success[wire.AnswerCandidateList]
	strictInto(t, h.get("/api/answer-bank/candidates").body, &cands)
	suggested := 0
	for _, c := range cands.Data.Candidates {
		if c.State == "pending" && (c.Review.Verdict == "" || c.Review.Verdict == "reusable") {
			suggested++
		}
	}
	want := wire.NeedsYouCounts{EmailConfirmations: 2, WikiDrafts: 2, HeldSignals: 1, SuggestedAnswers: wire.SafeInt(suggested), AnswerEdits: wire.SafeInt(len(cands.Data.Edits))}
	if got.Data != want || suggested != 2 {
		t.Errorf("counts = %+v, want %+v", got.Data, want)
	}
	if len(h.get("/api/kb/drafts").data()["drafts"].([]any)) != 2 || len(h.get("/api/signals/review").data()["items"].([]any)) != 1 {
		t.Error("the lists disagree with the counts the test expects")
	}
}
