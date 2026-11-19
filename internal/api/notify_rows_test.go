package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/discovery"
	"applysmith/internal/evaluate"
	"applysmith/internal/gmail/classify"
	"applysmith/internal/interview"
	"applysmith/internal/notify"
	"applysmith/internal/tracker"
)

func TestNotify_Inbox_ItemActionOpensRunOrFocusesKeptTab(t *testing.T) {
	h := newDomainHarness(t)
	d := h.domainOf()
	ctx := context.Background()
	if _, err := d.notify.board.Raise(ctx, notify.RaiseArgs{ID: "ent_plain", Kind: notify.KindApply, Origin: notify.OriginUser, Title: "Apply to Acme Labs",
		State: notify.NeedsYou, Reason: "answer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.notify.board.Open(ctx, notify.OpenArgs{ID: "ent_kept", Kind: notify.KindApply, Origin: notify.OriginAgent, Title: "Apply to Example Systems"}); err != nil {
		t.Fatal(err)
	}
	h.clock.advance(time.Minute)
	if _, err := d.notify.board.Move(ctx, "ent_kept", notify.NeedsYou, notify.MoveArgs{Reason: "challenge", FocusTab: "tab-7"}); err != nil {
		t.Fatal(err)
	}
	var inbox wire.Success[wire.NeedsYouInbox]
	strictInto(t, h.get("/api/needs-you").body, &inbox)
	byID := map[string]wire.NeedsYouItem{}
	for _, it := range inbox.Data.Items {
		byID[it.Entry.ID] = it
	}
	if it := byID["ent_plain"]; it.Action != wire.ActionOpenRun || it.Entry.Link != "/runs/ent_plain" || it.Entry.CanFocusTab || it.Entry.FocusTab != "" {
		t.Errorf("an item without a kept tab: %+v", it)
	}
	if it := byID["ent_kept"]; it.Action != wire.ActionFocusTab || !it.Entry.CanFocusTab || it.Entry.FocusTab != "tab-7" || it.Entry.Link != "/runs/ent_kept" {
		t.Errorf("an item with a kept tab: %+v", it)
	}
	// The jump is the kept tab's own action; a tab that is gone leaves the run page.
	if r := h.post("/api/apply/runs/ent_plain/show-tab", nil); r.StatusCode != 404 || r.errReason() != wire.ReasonNoTab {
		t.Errorf("show-tab for a run without a kept tab: %d %s", r.StatusCode, r.body)
	}
}

// With no app attached an urgent entry lands in Notices, and a notice is
// marked read by its id.
func TestNotify_Desktop_UnattachedAnUrgentEntryIsANoticeReadByID(t *testing.T) {
	h := newDomainHarness(t)
	d := h.domainOf()
	ctx := context.Background()
	if _, err := d.notify.board.Raise(ctx, notify.RaiseArgs{ID: "ent_n0", Kind: notify.KindApply, Origin: notify.OriginUser, Title: "Apply to Beta Corp",
		State: notify.NeedsYou, Reason: "sign-in"}); err != nil {
		t.Fatal(err)
	}
	if err := d.notify.policy.Process(ctx); err != nil {
		t.Fatal(err)
	}
	var got wire.Success[wire.NoticeList]
	strictInto(t, h.get("/api/notices").body, &got)
	if n := got.Data; n.Unread != 1 || len(n.Notices) != 1 || n.Notices[0].Title != "Apply to Beta Corp" || n.Notices[0].Link != "/runs/ent_n0" {
		t.Fatalf("notices = %+v", n)
	}
	var read wire.Success[wire.NoticeList]
	strictInto(t, h.post("/api/notices/read", wire.NoticesRead{IDs: []wire.SafeInt{got.Data.Notices[0].ID}}).body, &read)
	if read.Data.Unread != 0 || !read.Data.Notices[0].Read {
		t.Fatalf("read = %+v", read.Data)
	}
}

func TestTelegram_Scan_FinishReachesTelegramThroughPolicy(t *testing.T) {
	bot := newBotAPI(t)
	srv := atsServer(t)
	page := newGateReader(postingBody("read"))
	h := startDomainHarness(t, false, withReader(page), func(c *Config) {
		c.Domain.TelegramAPI = bot.srv.URL
		c.Domain.ScanEndpoints = discovery.Endpoints{discovery.Greenhouse: srv.URL}
	})
	d := h.domainOf()
	if _, err := d.repo.RegistryAdd(t.Context(), "test", discovery.Board{ATS: discovery.Greenhouse, Slug: "example-co", Company: "Example Co"}); err != nil {
		t.Fatal(err)
	}
	if r := h.send("PUT", "/api/telegram/token", map[string]any{"token": bot.token}); r.StatusCode != 200 {
		t.Fatalf("token: %d %s", r.StatusCode, r.body)
	}
	var code wire.Success[wire.TelegramPairingCode]
	strictInto(t, h.send("POST", "/api/telegram/pairing-code", nil).body, &code)
	bot.say(7, 8, code.Data.Code)
	bot.waitText(t, "Paired")
	bot.say(7, 8, "/scan")
	bot.waitText(t, "Which scan")
	bot.tap(7, 8, "scan:registry", "Which scan should I start?")
	waitLong(t, "the scan to end", func() bool {
		var n int
		_ = h.srv.store.Load().Read().QueryRow(`SELECT count(*) FROM runs WHERE kind = 'scan' AND status = 'succeeded'`).Scan(&n)
		return n == 1
	})
	// A real evaluation is still running when its scan ends, so this one reads
	// only after the scan's end. The finish is a board entry, queued once the
	// evaluation has ended too, and its delay counts from then: the clock moves
	// past the delay only once the finish is queued. It reaches the chat through
	// the announcement policy, with no poll of the gateway's own.
	close(page.release)
	scanFinish := `kind = 'done' AND link LIKE '/runs/%' AND title LIKE 'Scan%'`
	waitLong(t, "the scan's finish to be queued", func() bool {
		h.srv.syncBoard()
		return countOutbox(t, h, scanFinish) == 1
	})
	h.clock.advance(notify.DefaultDoneDelay + time.Second)
	if err := d.notify.policy.Process(h.srv.bg); err != nil {
		t.Fatal(err)
	}
	text := bot.waitText(t, "queued for evaluation")
	registry, _ := discovery.LookupSource("registry")
	if !strings.Contains(text, registry.Title+": ") || !strings.Contains(strings.ToLower(text), "scan") {
		t.Errorf("the finish message does not name the scan and its counts: %q", text)
	}
	if countOutbox(t, h, scanFinish) != 1 {
		t.Errorf("the scan's finish was not announced once through the policy")
	}
}

// narratorRig drives the writers whose records the narrator reads: run and
// evaluation-job rows in the shapes the evaluator stores (copied from a live
// workspace), scan records through the discovery repository, tracker rows
// through the tracker, and board entries through the board.
type narratorRig struct {
	t   *testing.T
	h   *harness
	d   *domain
	ctx context.Context
}

func newNarratorRig(t *testing.T, mods ...func(*Config)) narratorRig {
	g := newFakeGmail(t)
	fake := func(c *Config) { c.Domain.Gmail.Dial = g.Dial }
	h := newDomainHarness(t, append([]func(*Config){unavailableWindow, fake}, mods...)...)
	return narratorRig{t: t, h: h, d: h.domainOf(), ctx: context.Background()}
}

func (r narratorRig) stamp() string { return r.h.clock.now().UTC().Format("2006-01-02T15:04:05.000Z") }

func (r narratorRig) exec(q string, args ...any) {
	r.t.Helper()
	if err := r.h.srv.store.Load().Write(r.ctx, func(tx *sql.Tx) error { _, err := tx.Exec(q, args...); return err }); err != nil {
		r.t.Fatal(err)
	}
}

// evaluation runs an evaluation to its end: queued by the scan record scan
// (blank for none), it ends with the run status and the job's outcome and
// detail, as report 33.
func (r narratorRig) evaluation(id, scan, status, outcome, detail string, failure ...string) {
	r.t.Helper()
	r.evaluationOf(id, `"ScanRun":"`+scan+`"`, status, outcome, detail, failure...)
}

// evaluationOf is evaluation with the given members of the request input.
func (r narratorRig) evaluationOf(id, input, status, outcome, detail string, failure ...string) {
	r.t.Helper()
	r.exec(`INSERT INTO runs (id, kind, status, input_json, created_at, updated_at) VALUES (?, 'evaluate', 'running', ?, ?, ?)`, id,
		`{"URL":"https://jobs.example.test/`+id+`",`+input+`,"Source":"jobright","Mode":"screen","InboxRowID":85}`, r.stamp(), r.stamp())
	r.exec(`INSERT INTO evaluation_jobs (run_id, identity_key, mode, priority, source, posting_url, inbox_row_id, created_at) VALUES (?, ?, 'screen', 'scan', 'jobright', ?, 85, ?)`,
		id, "k-"+id, "https://jobs.example.test/"+id, r.stamp())
	r.h.srv.syncBoard()
	r.exec(`UPDATE runs SET status = ?, failure_reason = ?, updated_at = ?, finished_at = ? WHERE id = ?`, status, strings.Join(failure, ""), r.stamp(), r.stamp(), id)
	r.exec(`UPDATE evaluation_jobs SET outcome = ?, report_number = 33, detail_json = ?, finished_at = ? WHERE run_id = ?`, outcome, detail, r.stamp(), id)
	r.h.srv.syncBoard()
}

// scan starts a scan's run and record; end finishes both.
func (r narratorRig) scan(runID string) discovery.Record {
	r.t.Helper()
	rec, err := r.d.repo.ScanStart(r.ctx, "jobright", map[string]any{"run_id": runID})
	if err != nil {
		r.t.Fatal(err)
	}
	r.exec(`INSERT INTO runs (id, kind, status, input_json, created_at, updated_at) VALUES (?, 'scan', 'running', '{"source":"jobright"}', ?, ?)`, runID, r.stamp(), r.stamp())
	return rec
}

func (r narratorRig) endScan(runID string, rec discovery.Record, status, failure string) {
	r.t.Helper()
	if _, err := r.d.repo.ScanFinish(r.ctx, rec); err != nil {
		r.t.Fatal(err)
	}
	r.exec(`UPDATE runs SET status = ?, failure_reason = ?, updated_at = ?, finished_at = ? WHERE id = ?`, status, failure, r.stamp(), r.stamp(), runID)
	r.h.srv.syncBoard()
}

// apply finishes an application run to company through the apply chain's own
// finish.
func (r narratorRig) apply(id, company string, o apply.Outcome, row int64) {
	r.t.Helper()
	if _, err := r.d.notify.board.Open(r.ctx, notify.OpenArgs{ID: id, Kind: notify.KindApply, Origin: notify.OriginUser, Title: "Apply to " + company}); err != nil {
		r.t.Fatal(err)
	}
	run := apply.Run{ID: id, ATS: apply.ATSGreenhouse, Input: apply.Input{URL: "https://boards.greenhouse.io/acme/jobs/1", Company: company}, Outcome: o, TrackerRow: row}
	r.d.apply.finish(&run)
}

var confirmed, heldFill = apply.Outcome{Kind: apply.OutcomeSubmitted, Confirmed: true, ConfirmSource: apply.ConfirmPage}, apply.Outcome{Kind: apply.OutcomeFilled, Board: apply.HoldConfirmation}

// applied adds a tracker row and sets it applied as actor does.
func (r narratorRig) applied(company string, actor tracker.Actor, by tracker.AppliedBy) int64 {
	r.t.Helper()
	e, err := r.d.tracker.Add(r.ctx, tracker.AddRow{Company: company, Role: "Engineer", URL: "https://jobs.example.test/" + strings.ToLower(company)})
	if err == nil {
		_, err = r.d.tracker.SetStatus(r.ctx, tracker.StatusChange{ID: e.ID, To: tracker.Applied, Actor: actor, AppliedBy: by})
	}
	if err != nil {
		r.t.Fatal(err)
	}
	return e.ID
}

// A run's end says what it came to (F-2): an evaluation its score, lane and
// report, a scan stopped by a challenge page why without quoting the page, a
// submission where it went and what confirmed it, and a held fill that it waits
// for the user's Submit, never that it was submitted.
func TestNarrator_ARunEndSaysWhatItCameTo(t *testing.T) {
	r := newNarratorRig(t)
	type runEnd struct {
		name     string
		end      func(id string)
		has, not []string
	}
	cases := []runEnd{
		{"an evaluation in full", func(id string) {
			r.evaluation(id, "", "succeeded", "reported", `{"decision":"reported","recommendation":"strong","score":4.6,"screen":{"decision":"advance","proposed":"advance","score":4}}`)
		}, []string{"4.6", "Apply now", "report 33"}, nil},
		// The record's error text can quote the page; it is never repeated.
		{"a scan a challenge page stopped", func(id string) {
			rec := r.scan(id)
			rec.Status, rec.Reason, rec.Error = discovery.RunBlocked, "challenge", "Checking your browser before you access Example Jobs"
			r.endScan(id, rec, "failed", "challenge")
		}, []string{"challenge page"}, []string{"Checking your browser"}},
		{"a submission the page confirmed", func(id string) {
			r.apply(id, "Acme", confirmed, 12)
		}, []string{"Submitted to Acme", "Greenhouse", "confirmed by the page", "tracker row 12"}, nil},
		{"a fill held for the user's Submit", func(id string) { r.apply(id, "Acme", heldFill, 0) },
			[]string{"Submit waits for your confirmation"}, []string{"Submitted", "the tab is closed"}},
	}
	for i, c := range cases {
		id := fmt.Sprintf("run_%d", i)
		c.end(id)
		e, err := r.d.notify.board.Get(r.ctx, id)
		if err != nil || e.State == notify.Working {
			t.Errorf("%s: the run did not end: %+v, %v", c.name, e, err)
			continue
		}
		for _, s := range c.has {
			if !strings.Contains(strings.ToLower(e.Detail), strings.ToLower(s)) {
				t.Errorf("%s: %q does not say %q", c.name, e.Detail, s)
			}
		}
		for _, s := range c.not {
			if strings.Contains(e.Detail, s) {
				t.Errorf("%s: %q says %q", c.name, e.Detail, s)
			}
		}
	}
}

// The inbox drain is told once with what the evaluations it sent came to,
// counted, the best of them named, and a review the day's spend limit holds back
// saying so (F-2, F-8). A posting the read found closed is counted as closed,
// not as a failure (flow round 10, EF-4).
func TestNarrator_TheInboxDrainsAnnouncementSaysWhatItsEvaluationsCameTo(t *testing.T) {
	r := newNarratorRig(t)
	made := r.h.post("/api/schedules", map[string]any{"name": "inbox evaluation", "kind": "inbox.evaluate", "preset": map[string]any{"kind": "daily", "time": "23:30", "timezone": "UTC"}})
	if made.StatusCode != 201 {
		t.Fatalf("schedule: %d %s", made.StatusCode, made.body)
	}
	r.exec(`INSERT INTO runs (id, kind, status, schedule_id, created_at, updated_at) VALUES ('run_drain', 'inbox.evaluate', 'running', ?, ?, ?)`,
		int64(made.data()["id"].(float64)), r.stamp(), r.stamp())
	r.h.srv.syncBoard()
	for _, m := range []struct{ id, role, company, status, outcome, detail string }{
		{"run_d1", "Platform Engineer", "Example Co", "succeeded", "reported", `{"decision":"reported","recommendation":"consider","score":3.7,"screen":{"decision":"advance","proposed":"advance","score":3.8}}`},
		{"run_d2", "Staff Engineer", "Northwind Example", "succeeded", "reported", `{"decision":"reported","recommendation":"strong","score":4.4,"screen":{"decision":"advance","proposed":"advance","score":4.1}}`},
		{"run_d3", "Data Engineer", "Example Co", "succeeded", "screened", `{"decision":"drop","prompts":[],"recommendation":"skip","score":1.5,"screen":{"decision":"drop","proposed":"drop","score":1.5}}`},
		{"run_d4", "Site Engineer", "Northwind Example", "review_required", "screened", `{"decision":"review","explanation":"` + evaluate.CapHeldReview + `","prompts":[],"recommendation":"consider","review_reason":"seniority_unclear","score":3.9,"screen":{"decision":"review","proposed":"review","score":3.9}}`},
		{"run_d5", "Backend Engineer", "Example Co", "succeeded", "reported", `{"decision":"reported","recommendation":"consider","score":3.6,"screen":{"decision":"advance","proposed":"advance","score":3.6}}`},
	} {
		r.evaluationOf(m.id, `"Drain":"run_drain","Title":"`+m.role+`","Company":"`+m.company+`"`, m.status, m.outcome, m.detail)
	}
	r.evaluationOf("run_d6", `"Drain":"run_drain","Title":"Data Analyst","Company":"Example Co"`, "failed", "failed",
		`{"closed":true,"decision":"","error":"evaluation failed: posting-unreadable: the posting could not be read: http-404","explanation":"the posting is closed (its page no longer exists); it was removed from your inbox","recommendation":"","score":null}`,
		evaluate.ReasonUnreadable)
	r.exec(`UPDATE runs SET status = 'succeeded', updated_at = ?, finished_at = ? WHERE id = 'run_drain'`, r.stamp(), r.stamp())
	r.h.srv.syncBoard()
	r.h.clock.advance(notify.DefaultDoneDelay + time.Second)
	if err := r.d.notify.policy.Process(r.ctx); err != nil {
		t.Fatal(err)
	}
	var body string
	if err := r.h.srv.store.Load().Read().QueryRow(`SELECT body FROM notify_outbox WHERE entry_id = 'run_drain'`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"6 evaluated", "1 Apply now", "2 Selective", "1 wait for the day's spend limit", "1 skipped", "1 closed",
		"Best: Staff Engineer at Northwind Example 4.4, Platform Engineer at Example Co 3.7."} {
		if !strings.Contains(body, s) {
			t.Errorf("the drain's announcement %q does not say %q", body, s)
		}
	}
	if strings.Contains(body, "failed") {
		t.Errorf("the drain's announcement %q counts a failure", body)
	}
}

// The day counts what was done for the user today and what is left for them,
// read from the records their writers store; work from before today is not
// today's, and each count opens a list that holds exactly the rows it counts
// (F-2).
func TestNarrator_TheDayCountsWhatWasDoneAndWhatIsLeft(t *testing.T) {
	r := newNarratorRig(t)
	// Before today: an application that ended, and a row the user applied to.
	r.apply("run_old", "Initech", confirmed, 0)
	if _, err := r.d.tracker.Add(r.ctx, tracker.AddRow{Company: "Hooli", Role: "Engineer", Status: tracker.Applied}); err != nil {
		t.Fatal(err)
	}
	inbox := func(n string) {
		if _, err := r.d.repo.InboxAddURL(r.ctx, "https://jobs.example.test/inbox-"+n); err != nil {
			t.Fatal(err)
		}
	}
	inbox("old")
	r.h.clock.advance(9 * 24 * time.Hour)
	inbox("new")
	rec := r.scan("run_scan")
	rec.Status, rec.Counts = discovery.RunCompleted, discovery.Counts{Discovered: 7, Inserted: 4}
	r.endScan("run_scan", rec, "succeeded", "")
	r.evaluation("run_eval", "", "succeeded", "reported", `{"decision":"reported","recommendation":"strong","score":4.6,"screen":{"decision":"advance","proposed":"advance","score":4}}`)
	r.apply("run_sub", "Acme", confirmed, r.applied("Acme", tracker.ActorApplyChain, tracker.ByAgent))
	// One posting tried twice and stopped twice: one line, opening the newer run.
	r.apply("run_stop1", "Vandelay", apply.Outcome{Kind: apply.OutcomeStopped, Reason: "preflight blocked"}, 0)
	r.h.clock.advance(time.Minute)
	r.apply("run_stop2", "Vandelay", apply.Outcome{Kind: apply.OutcomeStopped, Reason: "preflight blocked"}, 0)
	// Gmail moves one row twice: one row moved.
	globex := r.applied("Globex", tracker.ActorGmail, "")
	if _, err := r.d.tracker.SetStatus(r.ctx, tracker.StatusChange{ID: globex, To: tracker.Responded, Actor: tracker.ActorGmail}); err != nil {
		t.Fatal(err)
	}
	// Applied today, turned down and archived: off the tracker's lists, so not counted.
	gone := r.applied("Initrode", tracker.ActorUser, tracker.ByUser)
	if _, err := r.d.tracker.SetStatus(r.ctx, tracker.StatusChange{ID: gone, To: tracker.Rejected, Actor: tracker.ActorUser}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.d.tracker.ArchiveRow(r.ctx, gone, tracker.ActorUser); err != nil {
		t.Fatal(err)
	}
	if _, err := r.d.notify.board.Raise(r.ctx, notify.RaiseArgs{ID: "run_wait", Kind: notify.KindApply, Origin: notify.OriginUser, Title: "Apply to Contoso",
		State: notify.NeedsYou, Reason: "answer"}); err != nil {
		t.Fatal(err)
	}
	seedScored(t, r.h, 40, "Umbrella", 4.4)
	r.h.connectGmail()
	seedGmail(t, r.h, apiSignal{id: "m1", typ: classify.TypeReplyNeeded, at: r.h.clock.now().Add(-time.Hour)})

	v := decode[wire.DayView](t, r.h.get("/api/day"))
	// Left for the user: the waiting run, the follow-up and the email.
	if c := v.Counts; c.Ready != 1 || c.AppliedByAgent != 1 || c.AppliedByUser != 1 || c.Waiting != 3 {
		t.Errorf("counts = %+v", c)
	}
	// Home links each count to the rows it counts: the list holds exactly them.
	list := decode[wire.CandidacyPage](t, r.h.get("/api/candidacies?reached=applied&since="+v.Date))
	var listed []string
	for _, e := range list.Entries {
		listed = append(listed, e.Company)
	}
	if slices.Sort(listed); !slices.Equal(listed, []string{"Acme", "Globex"}) || int(list.Total) != int(v.Counts.AppliedByUser+v.Counts.AppliedByAgent) {
		t.Errorf("applied today lists %v (total %d), counts %+v", listed, list.Total, v.Counts)
	}
	if moved := decode[wire.CandidacyPage](t, r.h.get("/api/candidacies?movedBy=gmail&since="+v.Date)); moved.Total != 1 || moved.Entries[0].Company != "Globex" {
		t.Errorf("moved by Gmail today lists %+v", moved.Entries)
	}
	if found := decode[wire.InboxPage](t, r.h.get("/api/inbox?since="+v.Date)); found.Total != v.Counts.Found || found.Total != 1 || found.Counts["pending"] != 1 {
		t.Errorf("found today lists %d (counts %v), counted %d", found.Total, found.Counts, v.Counts.Found)
	}
}

// "Ran N evaluations" counts every evaluation that ended since local midnight,
// a failure and an acknowledged one included, and opens the board's evaluation
// runs that ended since that day: the list holds exactly the runs the line
// counts, the one that ended a minute before midnight and the one still
// running in neither.
func TestNarrator_TheDaysEvaluationsAreTheRunsItOpens(t *testing.T) {
	r := newNarratorRig(t)
	n := r.h.clock.now().In(time.Local)
	midnight := time.Date(n.Year(), n.Month(), n.Day()+1, 0, 0, 0, 0, time.Local)
	r.h.clock.advance(midnight.Add(-time.Minute).Sub(r.h.clock.now()))
	reported := `{"decision":"reported","recommendation":"strong","score":4.6,"screen":{"decision":"advance","proposed":"advance","score":4}}`
	r.evaluation("run_late", "", "succeeded", "reported", reported)
	r.h.clock.advance(2 * time.Minute)
	r.evaluation("run_ok", "", "succeeded", "reported", reported)
	r.evaluation("run_bad", "", "failed", "failed", `{"explanation":"too short"}`, evaluate.ReasonUnreadable)
	if _, err := r.d.notify.board.Ack(r.ctx, "run_ok"); err != nil {
		t.Fatal(err)
	}
	r.exec(`INSERT INTO runs (id, kind, status, input_json, created_at, updated_at) VALUES ('run_live', 'evaluate', 'running', '{}', ?, ?)`, r.stamp(), r.stamp())
	r.exec(`INSERT INTO evaluation_jobs (run_id, identity_key, mode, priority, source, posting_url, created_at) VALUES ('run_live', 'k-live', 'screen', 'scan', 'jobright', 'https://jobs.example.test/live', ?)`, r.stamp())
	r.h.srv.syncBoard()

	v := decode[wire.DayView](t, r.h.get("/api/day"))
	i := slices.IndexFunc(v.Did, func(l wire.DayLine) bool { return strings.HasPrefix(l.Text, "Ran ") })
	if i < 0 {
		t.Fatalf("no evaluation line: %+v", v.Did)
	}
	var counted int
	if _, err := fmt.Sscanf(v.Did[i].Text, "Ran %d", &counted); err != nil {
		t.Fatal(err)
	}
	link, _ := url.Parse(v.Did[i].Link)
	var listed []string
	for _, e := range decode[wire.BoardEntryList](t, r.h.get("/api/runs?"+link.RawQuery)).Entries {
		listed = append(listed, e.ID)
	}
	if slices.Sort(listed); link.Path != "/runs" || counted != len(listed) || !slices.Equal(listed, []string{"run_bad", "run_ok"}) {
		t.Errorf("the line says %q and opens %s, which lists %v", v.Did[i].Text, v.Did[i].Link, listed)
	}
}

// atLocal moves the clock to the next local hour:minute and returns it.
func atLocal(h *harness, hour, minute int) time.Time {
	now := h.clock.now().In(time.Local)
	to := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, time.Local)
	for !to.After(now) {
		to = to.AddDate(0, 0, 1)
	}
	h.clock.advance(to.Sub(now))
	return to
}

// interviewRow adds an application at the interview stage with a stage at
// each time, and returns its id and its interview drawer's address.
func (r narratorRig) interviewRow(company string, at ...time.Time) (int64, string) {
	r.t.Helper()
	e, err := r.d.tracker.Add(r.ctx, tracker.AddRow{Company: company, Role: "Engineer", Status: tracker.Interview})
	if err != nil {
		r.t.Fatal(err)
	}
	for i, when := range at {
		if _, err := r.d.docs.Interview().RecordStage(r.ctx, e.ID, interview.StageInput{ID: fmt.Sprintf("stage-%d", i+1), Type: interview.TypeSystemDesign,
			At: when.UTC().Format(time.RFC3339)}); err != nil {
			r.t.Fatal(err)
		}
	}
	return e.ID, fmt.Sprintf("/tracker?interview=%d", e.ID)
}

// A reminder is due from 19:00 on the calendar day before a stage until that
// day starts, and in its last two hours while it has no prep pack; never once
// it began. The day before is the zone's calendar day, also when the clocks
// change (New York springs forward on the March stage's day, so 24 hours
// after 23:30 the evening before is already the day after the stage).
func TestNarrator_InterviewReminderTimes(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip(err)
	}
	at := func(month time.Month, d, h, m int) time.Time { return time.Date(2026, month, d, h, m, 0, 0, ny) }
	friday := at(10, 23, 14, 0)
	for _, c := range []struct {
		stage, now time.Time
		packed     bool
		want       string
	}{
		{friday, at(10, 22, 18, 59), false, ""},
		{friday, at(10, 22, 19, 0), true, "eve"},
		{friday, at(10, 23, 12, 0), false, "soon"},
		{friday, at(10, 23, 12, 0), true, ""},
		{friday, at(10, 23, 14, 0), false, ""},
		{at(3, 8, 10, 0), at(3, 7, 23, 30), true, "eve"},
	} {
		if got := reminderDue(c.stage, c.now, ny, c.packed); got != c.want {
			t.Errorf("a stage at %s, packed %v, at %s: %q, want %q", c.stage.Format(time.DateTime), c.packed, c.now.Format(time.DateTime), got, c.want)
		}
	}
}

// The timer sends a stage's reminder once, again for a stage moved to another
// time, and the two-hour one for a stage with no prep pack; each opens the
// stage's interview drawer. They are normal notices: quiet hours hold them.
func TestNarrator_InterviewRemindersAreSentOnce(t *testing.T) {
	r := newNarratorRig(t)
	nine := atLocal(r.h, 9, 0)
	stage := time.Date(nine.Year(), nine.Month(), nine.Day()+2, 14, 0, 0, 0, time.Local)
	eve := time.Date(stage.Year(), stage.Month(), stage.Day()-1, interviewEveHour, 0, 0, 0, time.Local)
	setQuietWindow(t, r.h, eve.UTC().Add(-time.Hour).Format("15:04"), eve.UTC().Add(time.Hour).Format("15:04"))
	id, link := r.interviewRow("Fabrikam", stage)
	sent := func(to time.Time) []string {
		t.Helper()
		r.h.clock.advance(to.Sub(r.h.clock.now()))
		if err := (interviewReminderTimer{s: r.h.srv, d: r.d}).Tick(r.ctx, to); err != nil {
			t.Fatal(err)
		}
		if err := r.d.notify.policy.Process(r.ctx); err != nil {
			t.Fatal(err)
		}
		var out []string
		rows, err := r.h.srv.store.Load().Read().Query(`SELECT state, title, link FROM notify_outbox WHERE kind = 'interview-reminder' ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var state, title, l string
			if err := rows.Scan(&state, &title, &l); err != nil {
				t.Fatal(err)
			}
			if l != link {
				t.Errorf("%q opens %q, want %q", title, l, link)
			}
			out = append(out, state+" "+title)
		}
		return out
	}
	if got := sent(eve.Add(-time.Minute)); len(got) != 0 {
		t.Fatalf("before the evening: %v", got)
	}
	if got := sent(eve.Add(time.Minute)); len(got) != 1 || !strings.HasPrefix(got[0], "held ") || !strings.Contains(got[0], "14:00") {
		t.Fatalf("the evening before, in quiet hours: %v", got)
	}
	if got := sent(eve.Add(2 * time.Minute)); len(got) != 1 {
		t.Fatalf("a second tick: %v", got)
	}
	w, err := r.d.docs.Interview().Workspace(r.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	st := w.Stages[0]
	moved := stage.Add(time.Hour)
	if _, err := r.d.docs.Interview().RecordStage(r.ctx, id, interview.StageInput{ID: "stage-1", Type: st.Type, At: moved.UTC().Format(time.RFC3339),
		ExpectedRevision: &st.Revision}); err != nil {
		t.Fatal(err)
	}
	if got := sent(eve.Add(3 * time.Minute)); len(got) != 2 || !strings.Contains(got[1], "15:00") {
		t.Fatalf("a stage moved to 15:00: %v", got)
	}
	if got := sent(moved.Add(-interviewLastCall - time.Minute)); len(got) != 2 {
		t.Fatalf("before the last two hours: %v", got)
	}
	if got := sent(moved.Add(-interviewLastCall + time.Minute)); len(got) != 3 || strings.HasPrefix(got[2], "held ") {
		t.Fatalf("the last two hours, out of quiet hours: %v", got)
	}
}

// A scheduled Gmail sync that read no mail, or found a sync already reading
// it, is acknowledged as it ends: every ten minutes such a run would otherwise
// add an unseen finished entry. One that read mail, and one that failed, stay.
func TestNotify_Board_AnIdleGmailSyncIsAcknowledgedAsItEnds(t *testing.T) {
	r := newNarratorRig(t)
	r.exec(`INSERT INTO schedules (id, name, job_kind, dtstart, created_at, updated_at) VALUES (901, 'mail check', 'gmail.sync', ?, ?, ?)`, r.stamp(), r.stamp(), r.stamp())
	runs := []struct {
		id, status, summary string
		acked               bool
	}{
		{"run_none", "succeeded", "0 listed, 0 stored, 0 for review, 0 linked", true},
		{"run_busy", "succeeded", "a sync was already running", true},
		{"run_mail", "succeeded", "1 listed, 1 stored, 0 for review, 0 linked", false},
		{"run_bad", "failed", "0 listed, 0 stored, 0 for review, 0 linked", false},
	}
	for i, c := range runs {
		at := fmt.Sprintf("2026-11-01T00:%02d:00Z", i)
		r.exec(`INSERT INTO runs (id, kind, status, schedule_id, scheduled_for, created_at, updated_at, finished_at) VALUES (?, 'gmail.sync', ?, 901, ?, ?, ?, ?)`,
			c.id, c.status, at, r.stamp(), r.stamp(), r.stamp())
		r.exec(`INSERT INTO schedule_history (schedule_id, scheduled_for, outcome, run_id, summary, created_at) VALUES (901, ?, 'fired', ?, ?, ?)`, at, c.id, c.summary, r.stamp())
	}
	r.h.srv.syncBoard()
	for _, c := range runs {
		e, err := r.d.notify.board.Get(r.ctx, c.id)
		if err != nil {
			t.Fatal(err)
		}
		if e.State != notify.Done || e.Acked() != c.acked {
			t.Errorf("%s (%q): %s, acknowledged %v, want done, acknowledged %v", c.id, c.summary, e.State, e.Acked(), c.acked)
		}
	}
}
