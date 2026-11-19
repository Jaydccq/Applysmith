package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/agent"
	chatsessions "applysmith/internal/agent/sessions"
	"applysmith/internal/answerbank/learn"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/discovery"
	"applysmith/internal/gates"
	"applysmith/internal/gmail"
	"applysmith/internal/gmail/classify"
	"applysmith/internal/identity"
	"applysmith/internal/ledger"
	"applysmith/internal/notify"
	"applysmith/internal/tracker"
	"applysmith/internal/workspace"
)

func TestApplySkipList_ReadsTheDefaultThenSavesWithRevisionChecking(t *testing.T) {
	h := newDomainHarness(t)
	if err := os.WriteFile(h.ws.Path(workspace.FileProfile), []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := decode[wire.ApplySkipList](t, h.get("/api/apply/skip-list"))
	if !got.Defaulted || got.Differs || len(got.Entries) != 7 || got.Revision == "" {
		t.Fatalf("default list = %+v", got)
	}
	put := h.put("/api/apply/skip-list", wire.ApplySkipListWrite{Entries: []string{"Example Labs", " example labs ", "Sample Works"}, Revision: got.Revision})
	if put.StatusCode != 200 {
		t.Fatalf("save: %d %s", put.StatusCode, put.body)
	}
	saved := decode[wire.ApplySkipList](t, put)
	if saved.Defaulted || !saved.Differs || strings.Join(saved.Entries, ",") != "Example Labs,Sample Works" || saved.Revision == got.Revision {
		t.Fatalf("saved list = %+v", saved)
	}
	// A write against the revision that was read before is a conflict, not an overwrite.
	stale := h.put("/api/apply/skip-list", wire.ApplySkipListWrite{Entries: []string{"Other"}, Revision: got.Revision})
	if stale.StatusCode != 409 {
		t.Fatalf("stale write: %d %s", stale.StatusCode, stale.body)
	}
	// Emptying the list leaves no company to the user: a default removed stays removed.
	empty := decode[wire.ApplySkipList](t, h.put("/api/apply/skip-list", wire.ApplySkipListWrite{Entries: []string{}, Revision: saved.Revision}))
	if len(empty.Entries) != 0 || empty.Defaulted {
		t.Fatalf("emptied list = %+v", empty)
	}
	if s := decode[wire.ApplySkipStatus](t, h.get("/api/apply/skip-status?company=Microsoft")); s.Listed {
		t.Fatalf("the status route still lists a removed default: %+v", s)
	}
	if r := h.put("/api/apply/skip-list", wire.ApplySkipListWrite{Entries: []string{""}, Revision: empty.Revision}); r.StatusCode != 422 {
		t.Errorf("an empty entry: %d %s", r.StatusCode, r.body)
	}
}

func TestAPI_SkipCompanies_PutKeepsCommentsBacksUpInStateDir(t *testing.T) {
	h := newDomainHarness(t)
	const doc = "version: 1\n# kept: who I do not want a batch to apply to\ncandidate:\n  full_name: Alex Example # my name\n"
	profile := h.ws.Path(workspace.FileProfile)
	if err := os.WriteFile(profile, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	got := decode[wire.ApplySkipList](t, h.get("/api/apply/skip-list"))
	if r := h.put("/api/apply/skip-list", wire.ApplySkipListWrite{Entries: []string{"Example Labs"}, Revision: got.Revision}); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	text, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	for _, kept := range []string{"# kept: who I do not want a batch to apply to", "# my name", "Example Labs"} {
		if !strings.Contains(string(text), kept) {
			t.Errorf("the saved profile lost %q:\n%s", kept, text)
		}
	}
	backups, err := os.ReadDir(h.ws.BackupsDir())
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups %v %v", backups, err)
	}
	old, err := os.ReadFile(filepath.Join(h.ws.BackupsDir(), backups[0].Name()))
	if err != nil || string(old) != doc {
		t.Fatalf("the backup is not the previous copy: %q %v", old, err)
	}
	if _, err := os.Stat(profile + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("a .bak was left beside the profile: %v", err)
	}
}

func TestApplyRunView_StagesWithDurationsRetryAndTheBatchRecord(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	a := h.domainOf().apply
	// Two rows of one list: the first stops at preflight (the test browser is down)
	// and the second is not attempted.
	id, err := a.StartApply(context.Background(), agent.ApplyRequest{Tool: gates.ToolApplyFromList, Caller: gates.CallerAskAgent, Rows: []agent.ApplyRow{
		{URL: "https://jobs.example.test/a/1?token=abc", Company: "Example Corp", Role: "Engineer"}, {URL: "https://jobs.example.test/b/1", Company: "Sample Labs", Role: "Analyst"}}})
	if err != nil {
		t.Fatal(err)
	}
	waitLong(t, "the list to end", func() bool { return !a.busy.Load() })
	run := decode[wire.ApplyRunView](t, h.get("/api/apply/runs/"+id))
	if run.Mode != wire.ApplyModeDryRun || run.Company != "Example Corp" || run.Host != "jobs.example.test" || run.Outcome.Kind != "stopped" ||
		run.Outcome.Stage != "preflight" || run.Outcome.Owner != wire.OwnerEnvironment || run.BatchID == "" || len(run.Stages) != 10 || run.KeptTab {
		t.Fatalf("run view = %+v", run)
	}
	if run.Stages[0].Stage != "preflight" || run.Stages[0].Status != "failed" || run.Stages[0].Detail == "" || run.Stages[3].Status != "skipped" {
		t.Fatalf("stages = %+v", run.Stages)
	}
	if r := h.get("/api/apply/runs/run_missing"); r.StatusCode != 404 {
		t.Fatalf("unknown run: %d", r.StatusCode)
	}

	batch := decode[wire.ApplyBatchView](t, h.get("/api/apply/batches/"+run.BatchID))
	if batch.State != wire.BatchClosed || batch.Mode != wire.ApplyModeDryRun || len(batch.Rows) != 2 {
		t.Fatalf("batch = %+v", batch)
	}
	if batch.Rows[0].State != "ended" || batch.Rows[0].RunID != id || batch.Rows[0].Outcome == nil || batch.Rows[0].Outcome.Stage != "preflight" {
		t.Fatalf("row 0 = %+v", batch.Rows[0])
	}
	if batch.Rows[1].State != "ended" || batch.Rows[1].RunID != "" || !strings.Contains(batch.Rows[1].Note, "not attempted") {
		t.Fatalf("row 1 = %+v", batch.Rows[1])
	}
	if len(batch.Owners) != 1 || batch.Owners[0].Owner != wire.OwnerEnvironment || strings.Join(batch.Owners[0].Companies, ",") != "Example Corp" {
		t.Fatalf("owners = %+v", batch.Owners)
	}

	// A retry is a new run of the same posting; the address with its query comes from the journal.
	if r := h.post("/api/apply/runs/"+id+"/retry", wire.ApplyRetryRequest{Submit: true}); r.StatusCode != 422 || r.errField() != "confirmed" {
		t.Fatalf("an unconfirmed submitting retry: %d %s", r.StatusCode, r.body)
	}
	r := h.post("/api/apply/runs/"+id+"/retry", wire.ApplyRetryRequest{})
	if r.StatusCode != 202 {
		t.Fatalf("retry: %d %s", r.StatusCode, r.body)
	}
	again := decode[wire.RunAccepted](t, r).RunID
	waitLong(t, "the retry to end", func() bool {
		v := decode[wire.ApplyRunView](t, h.get("/api/apply/runs/"+again))
		return v.FinishedAt != nil
	})
	var url string
	if err := h.srv.store.Load().Read().QueryRow(`SELECT url FROM apply_batch_rows WHERE run_id = ?`, id).Scan(&url); err != nil || url != "https://jobs.example.test/a/1?token=abc" {
		t.Fatalf("journal url %q, %v", url, err)
	}
}

// A submitting batch that resumes after a restart has no user present to
// confirm its submit: its rows are filled only, and a filled row waits on the
// board for a fresh confirmation from the dashboard.
func TestApplyStack_ASubmittingBatchResumedAfterARestartFillsOnlyAndWaitsForConfirmation(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	a := h.domainOf().apply
	rows := []apply.ListRow{{URL: "https://a.example.test/jobs/1", Company: "Alpha Labs"}, {URL: "https://b.example.test/jobs/1", Company: "Bravo Labs"}}
	ctx := context.Background()
	meta := apply.BatchMeta{Caller: gates.CallerAskAgent, Tool: gates.ToolApplyFromList, Submit: true, Explicit: true, Deadline: time.Now().Add(time.Hour)}
	if err := a.journal.Create(ctx, "list_submit", meta, rows); err != nil {
		t.Fatal(err)
	}
	a.resume()
	waitLong(t, "the resumed list to end", func() bool { return !a.busy.Load() && len(applyRunRows(t, h)) > 0 })
	var submit int
	var id string
	if err := h.srv.store.Load().Read().QueryRow(`SELECT submit, run_id FROM apply_runs WHERE batch_id = 'list_submit' ORDER BY started_at LIMIT 1`).Scan(&submit, &id); err != nil || submit != 0 {
		t.Fatalf("the resumed row ran with submit=%d (%v); a batch resumed after a restart only fills", submit, err)
	}
	// No user started the resumed row, so a Done on it creates no account (F-8).
	if q, err := apply.LoadRequest(ctx, h.srv.store.Load(), id); err != nil || q.Input().UserStarted {
		t.Fatalf("the resumed row's request %+v (%v) rebuilds a run the user started", q, err)
	}
	filled := heldAfterRestart(apply.Outcome{Kind: apply.OutcomeFilled}, true)
	if state, mv := boardEnd(filled, false); state != notify.NeedsYou || mv.Reason != "confirmation" || !strings.Contains(mv.Detail, "restart") {
		t.Fatalf("a filled row of a resumed submitting batch ends %s %+v", state, mv)
	}
	if o := heldAfterRestart(apply.Outcome{Kind: apply.OutcomeFilled}, false); o.Board != "" {
		t.Fatalf("a fill-only batch's row waits for a confirmation it never asked for: %+v", o)
	}
	if o := heldAfterRestart(apply.Outcome{Kind: apply.OutcomeStopped, Board: "sign-in"}, true); o.Board != "sign-in" {
		t.Fatalf("a stopped row lost its own reason: %+v", o)
	}
}

func TestAPI_ApplyList_ItemCarriesLatestAttemptByIdentityKey(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	score := 4.4
	_, err := h.upsertReport(t.Context(), tracker.Report{Number: 1, Date: "2026-01-02", Outcome: tracker.OutcomeRecommended, Kind: tracker.ScoreFinal,
		Score: &score, Posting: postingOf("Example Labs")})
	if err != nil {
		t.Fatal(err)
	}
	list := func() wire.ApplyList { return decode[wire.ApplyList](t, h.get("/api/apply/list?lane=apply-now")) }
	if it := list().Lanes[0].Items[0]; it.State != wire.ApplyStateIdle || it.Attempt != nil || it.KeptTab {
		t.Fatalf("a row never attempted: %+v", it)
	}
	// The attempt is found by the posting's identity key, so the same job under a
	// tracking parameter still carries it.
	r := h.post("/api/apply/runs", wire.ApplyRunRequest{URL: "https://jobs.example.com/example-labs/1?utm_source=x"})
	if r.StatusCode != 202 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	waitForApplyRun(t, h)
	waitLong(t, "the run to be the item's attempt", func() bool {
		ls := list().Lanes
		return len(ls) > 0 && len(ls[0].Items) > 0 && ls[0].Items[0].Attempt != nil
	})
	l := list()
	it := l.Lanes[0].Items[0]
	if it.State != wire.ApplyStateFailed || it.Attempt.Outcome.Stage != "preflight" || it.Attempt.Outcome.Owner != wire.OwnerEnvironment || it.RunID == "" || it.Attempt.Mode != wire.ApplyModeDryRun {
		t.Fatalf("item = %+v / %+v", it, it.Attempt)
	}
	if l.Counts.States[wire.ApplyStateFailed] != 1 || l.Counts.Lanes[wire.LaneApplyNow] != 1 {
		t.Fatalf("counts = %+v", l.Counts)
	}
	if it.Version < 1 {
		t.Fatalf("an item carries its row's version, the one a Skip quotes: %d", it.Version)
	}
	// Marked applied by the user, the row is theirs: the stopped run stays its
	// attempt but no longer makes it failed.
	if _, err := h.domainOf().tracker.SetStatus(t.Context(), tracker.StatusChange{ID: int64(it.ID), To: tracker.Applied, Actor: tracker.ActorUser}); err != nil {
		t.Fatal(err)
	}
	marked := decode[wire.ApplyList](t, h.get("/api/apply/list?lane=marked-applied"))
	if it := marked.Lanes[0].Items[0]; it.State != wire.ApplyStateIdle || it.Attempt == nil || marked.Counts.States[wire.ApplyStateFailed] != 0 {
		t.Fatalf("a marked-applied row with a stopped run: %+v %+v", it, marked.Counts)
	}
}

func seedScored(t *testing.T, h *harness, n int64, company string, score float64) {
	t.Helper()
	_, err := h.upsertReport(t.Context(), tracker.Report{Number: n, Date: "2026-01-02", Outcome: tracker.OutcomeRecommended, Kind: tracker.ScoreFinal,
		Score: &score, Posting: postingOf(company)})
	if err != nil {
		t.Fatal(err)
	}
}

// Newest orders a lane by report date over its whole length, and page.<lane>
// pages that lane alone while the others stay on page 1.
func TestAPI_ApplyList_NewestFirstAndAPagePerLane(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	for n, r := range []struct {
		company, date string
		score         float64
	}{{"Old Best", "2026-01-02", 4.9}, {"Fresh Good", "2026-01-05", 4.1}, {"Older Maybe", "2026-01-03", 3.6}, {"Newer Maybe", "2026-01-04", 3.8}} {
		score := r.score
		if _, err := h.upsertReport(t.Context(), tracker.Report{Number: int64(n + 1), Date: r.date, Outcome: tracker.OutcomeRecommended, Kind: tracker.ScoreFinal,
			Score: &score, Posting: postingOf(r.company)}); err != nil {
			t.Fatal(err)
		}
	}
	got := decode[wire.ApplyList](t, h.get("/api/apply/list?lane=apply-now,selective&pageSize=1&sort=newest&page.selective=2"))
	var firsts []string
	for _, l := range got.Lanes {
		for _, it := range l.Items {
			firsts = append(firsts, l.Lane+": "+it.Company)
		}
	}
	if want := []string{"apply-now: Fresh Good", "selective: Older Maybe"}; !slices.Equal(firsts, want) {
		t.Fatalf("rows = %v, want %v", firsts, want)
	}
	if r := h.get("/api/apply/list?sort=oldest"); r.StatusCode != 422 {
		t.Fatalf("unknown sort: %d %s", r.StatusCode, r.body)
	}
}

// Every lane rule of the apply-list status route, one row each, seeded through
// the writers that hold each fact in production.
func TestAPI_ApplyList_EveryLaneRule(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	d := h.domainOf()
	ctx := t.Context()
	n := int64(0)
	scored := func(company string, score float64, kind tracker.ScoreKind) tracker.Entry {
		t.Helper()
		n++
		res, err := h.upsertReport(ctx, tracker.Report{Number: n, Date: "2026-01-02", Outcome: tracker.OutcomeRecommended, Kind: kind, Score: &score, Posting: postingOf(company)})
		if err != nil {
			t.Fatal(err)
		}
		return res.Entry
	}
	final := func(company string, score float64) tracker.Entry { return scored(company, score, tracker.ScoreFinal) }
	exec := func(q string, args ...any) {
		t.Helper()
		if err := h.srv.store.Load().Write(ctx, func(tx *sql.Tx) error { _, err := tx.Exec(q, args...); return err }); err != nil {
			t.Fatal(err)
		}
	}
	live := func(e tracker.Entry, verdict string) {
		t.Helper()
		if err := d.repo.LivenessSave(ctx, identity.Posting{URL: e.PostingURL}.IdentityKey(), e.PostingURL, discovery.Liveness{Verdict: verdict}, discovery.LivenessMethod); err != nil {
			t.Fatal(err)
		}
	}
	stopped := func(e tracker.Entry, reason string) {
		t.Helper()
		key, stp := apply.IdentityKey(e.PostingURL)
		if stp != nil {
			t.Fatal(stp)
		}
		exec(`INSERT INTO apply_runs (run_id, started_at, finished_at, caller, posting_url, identity_key, outcome_kind, outcome_stage, outcome_class, outcome_reason, company, role)
			VALUES (?, '2026-01-02T03:04:05.000Z', '2026-01-02T03:05:05.000Z', 'cli', ?, ?, 'stopped', 'account', 'posting', ?, ?, ?)`, "run_"+e.Company, e.PostingURL, key, reason, e.Company, e.Role)
	}
	observed := func(e tracker.Entry, rawURL string) {
		t.Helper()
		if _, err := d.ledger.RecordObserved(ctx, ledger.Observation{EntryID: "e-" + e.Company, URL: rawURL, Host: "jobs.example.com", Company: e.Company, Role: e.Role,
			ObservedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	press := func(e tracker.Entry, v ledger.Verdict) int64 {
		t.Helper()
		cl, err := d.ledger.Claim(ctx, ledger.ClaimRequest{URL: e.PostingURL, Host: "jobs.example.com", Company: e.Company, Role: e.Role, Source: ledger.SourceApplyChain, RunID: "run_" + e.Company})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.ledger.Finish(ctx, cl.ID, v); err != nil {
			t.Fatal(err)
		}
		w, _, err := d.ledger.WaitingFor(ctx, []string{e.IdentityKey})
		if err != nil {
			t.Fatal(err)
		}
		return w.ID
	}
	confirmedByPage := func(e tracker.Entry) int64 {
		t.Helper()
		id := press(e, ledger.Verdict{Landed: true, PressDispatched: true, Confirmed: true, ConfirmSource: apply.ConfirmPage})
		if _, err := d.tracker.SetStatus(ctx, tracker.StatusChange{ID: e.ID, To: tracker.Applied, Actor: tracker.ActorApplyChain, AppliedBy: tracker.ByAgent}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	markedApplied := func(company string) tracker.Entry {
		t.Helper()
		e := final(company, 4.4)
		if _, err := d.tracker.SetStatus(ctx, tracker.StatusChange{ID: e.ID, To: tracker.Applied, Actor: tracker.ActorUser}); err != nil {
			t.Fatal(err)
		}
		return e
	}
	stamp := "2026-01-02T03:04:05.000Z"
	cases := []struct {
		name string
		seed func() tracker.Entry
		lane string // "" for no lane
		// leftOut is true when a list started now would leave the row out.
		leftOut, badge bool
	}{
		{"a final 4.0 is in Apply now", func() tracker.Entry { return final("Edge Corp", 4.0) }, wire.LaneApplyNow, false, false},
		{"a final 3.9 is in Selective", func() tracker.Entry { return final("Mid Corp", 3.9) }, wire.LaneSelective, false, false},
		{"a 3.4 is in no lane", func() tracker.Entry { return final("Low Corp", 3.4) }, "", false, false},
		{"a screening 4.2 never reaches Apply now", func() tracker.Entry { return scored("Screened High", 4.2, tracker.ScoreScreening) }, "", false, false},
		{"a skip-listed company keeps its badge in Selective", func() tracker.Entry { return final("Microsoft", 4.4) }, wire.LaneSelective, true, true},
		{"a liveness mismatch needs review", func() tracker.Entry { e := final("Moved Corp", 4.3); live(e, discovery.LiveMismatch); return e }, wire.LaneSelective, false, false},
		{"a doubtful posting needs review", func() tracker.Entry {
			e := final("Doubtful Corp", 4.5)
			exec(`INSERT INTO report_artifacts (report_number, kind, path, status, content_hash, created_at, updated_at) VALUES (?, 'final', 'reports/d.md', 'ready', 'h', ?, ?)`, n, stamp, stamp)
			exec(`INSERT INTO report_index (report_number, kind, company, role, posting_url, identity_key, score, score_kind, recommendation, legitimacy, verification, markdown_path, html_path, job_id, created_at)
				VALUES (?, 'final', ?, ?, ?, ?, 4.5, 'final', 'apply', 'doubtful', 'cli pass', 'reports/d.md', 'reports/d.html', 'j-d', ?)`, n, e.Company, e.Role, e.PostingURL, e.IdentityKey, stamp)
			return e
		}, wire.LaneSelective, false, false},
		{"an expired posting is in no lane", func() tracker.Entry { e := final("Expired Corp", 4.4); live(e, discovery.LiveExpired); return e }, "", false, false},
		{"a closed-posting fact is in no lane", func() tracker.Entry { e := final("Closed Corp", 4.4); stopped(e, "posting closed: HTTP 404"); return e }, "", false, false},
		{"another posting fact keeps the row with its reason", func() tracker.Entry { e := final("Email Corp", 4.4); stopped(e, "applies by email only"); return e }, wire.LaneApplyNow, true, false},
		{"a submission of the posting is in no lane", func() tracker.Entry { e := final("Observed Corp", 4.4); observed(e, e.PostingURL); return e }, "", false, false},
		{"the same company and role sent elsewhere is in no lane", func() tracker.Entry {
			e := final("Twin Corp", 4.4)
			observed(e, "https://jobs.example.com/twin-corp/2")
			return e
		}, "", false, false},
		{"an unconfirmed press of the chain awaits", func() tracker.Entry {
			e := final("Pressed Corp", 4.4)
			press(e, ledger.Verdict{PressDispatched: true})
			return e
		}, wire.LaneAwaiting, false, false},
		{"a press the page confirmed awaits its receipt", func() tracker.Entry { e := final("Paged Corp", 4.4); confirmedByPage(e); return e }, wire.LaneAwaiting, false, false},
		{"a press whose receipt arrived is in no lane", func() tracker.Entry {
			e := final("Received Corp", 4.4)
			if err := d.ledger.SettleWaiting(ctx, confirmedByPage(e), ledger.ConfirmReceipt, "m-received"); err != nil {
				t.Fatal(err)
			}
			return e
		}, "", false, false},
		{"the user's own application is marked applied", func() tracker.Entry { return markedApplied("Marked Corp") }, wire.LaneMarkedApplied, false, false},
		{"the user's application with a receipt is in no lane", func() tracker.Entry {
			e := markedApplied("Receipted Corp")
			exec(`INSERT INTO gmail_outcomes (message_id, candidacy_id, signal_type, result, from_status, to_status, reason, at) VALUES ('m-own', ?, ?, ?, 'applied', 'applied', '', ?)`,
				e.ID, classify.TypeApplied, gmail.OutcomeNoChange, stamp)
			return e
		}, "", false, false},
	}
	ids := make([]int64, len(cases))
	for i, c := range cases {
		ids[i] = c.seed().ID
	}
	type placed struct {
		lane string
		item wire.ApplyItem
	}
	got := map[int64]placed{}
	idsOf := func(l wire.ApplyLane) []int64 {
		var out []int64
		for _, it := range l.Items {
			out = append(out, int64(it.ID))
		}
		return out
	}
	for _, l := range decode[wire.ApplyList](t, h.get("/api/apply/list?pageSize=100")).Lanes {
		for _, it := range l.Items {
			if _, twice := got[int64(it.ID)]; twice {
				t.Errorf("%s is in two lanes", it.Company)
			}
			got[int64(it.ID)] = placed{l.Lane, it}
		}
		// A lane asked for alone is placed as it is among all four.
		alone := decode[wire.ApplyList](t, h.get("/api/apply/list?pageSize=100&lane="+l.Lane))
		if len(alone.Lanes) != 1 || alone.Lanes[0].Total != l.Total || !slices.Equal(idsOf(alone.Lanes[0]), idsOf(l)) {
			t.Errorf("lane %s asked for alone: %+v, among all four: %v", l.Lane, alone.Lanes, idsOf(l))
		}
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := got[ids[i]]
			if p.lane != c.lane || (p.item.LeftOut != "") != c.leftOut || (p.item.SkipEntry != "") != c.badge {
				t.Fatalf("lane %q left out %q badge %q, want lane %q left out %v badge %v", p.lane, p.item.LeftOut, p.item.SkipEntry, c.lane, c.leftOut, c.badge)
			}
		})
	}
}

// A row's source check is its posting's last check: a stored liveness verdict
// when there is one, else its report's verification line. Only a check that
// saw the posting live, or no check at all, says nothing.
func TestAPI_ApplyList_SourceCheckReadsTheLastCheck(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	d := h.domainOf()
	ctx := t.Context()
	stamp := "2026-01-02T03:04:05.000Z"
	cases := []struct {
		company, verification, liveness, want string
	}{
		{"Live Corp", "browser live 2026-01-02", "", ""},
		{"Pasted Corp", "pasted unverified 2026-01-02", "", wire.SourceCheckUnknown},
		{"Moved Corp", "browser live 2026-01-02", discovery.LiveMismatch, wire.SourceCheckFailed},
	}
	for i, c := range cases {
		n := int64(i + 1)
		score := 4.1
		res, err := h.upsertReport(ctx, tracker.Report{Number: n, Date: "2026-01-02", Outcome: tracker.OutcomeRecommended, Kind: tracker.ScoreFinal, Score: &score, Posting: postingOf(c.company)})
		if err != nil {
			t.Fatal(err)
		}
		e := res.Entry
		err = h.srv.store.Load().Write(ctx, func(tx *sql.Tx) error {
			if c.verification == "" {
				return nil
			}
			if _, err := tx.Exec(`INSERT INTO report_artifacts (report_number, kind, path, status, content_hash, created_at, updated_at) VALUES (?, 'final', ?, 'ready', 'h', ?, ?)`, n, fmt.Sprintf("reports/%d.md", n), stamp, stamp); err != nil {
				return err
			}
			_, err := tx.Exec(`INSERT INTO report_index (report_number, kind, company, role, posting_url, identity_key, score, score_kind, recommendation, legitimacy, verification, markdown_path, html_path, job_id, created_at)
				VALUES (?, 'final', ?, ?, ?, ?, 4.1, 'final', 'apply', 'credible', ?, ?, 'r.html', 'j', ?)`, n, e.Company, e.Role, e.PostingURL, e.IdentityKey, c.verification, fmt.Sprintf("reports/%d.md", n), stamp)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if c.liveness != "" {
			if err := d.repo.LivenessSave(ctx, identity.Posting{URL: e.PostingURL}.IdentityKey(), e.PostingURL, discovery.Liveness{Verdict: c.liveness}, discovery.LivenessMethod); err != nil {
				t.Fatal(err)
			}
		}
	}
	got := map[string]string{}
	for _, l := range decode[wire.ApplyList](t, h.get("/api/apply/list?pageSize=100")).Lanes {
		for _, it := range l.Items {
			got[it.Company] = it.SourceCheck
		}
	}
	for _, c := range cases {
		if v, ok := got[c.company]; !ok || v != c.want {
			t.Errorf("%s: source check %q (listed %v), want %q", c.company, v, ok, c.want)
		}
	}
}

// "Confirm submitted" is the user's statement that the chain's waiting
// submission went through (F-5): the row is applied as the agent's and the
// ledger records the user, not the page, as what confirmed it.
func TestAPI_ConfirmSubmitted_TheUsersWordSettlesTheChainsWaitingSubmission(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	d := h.domainOf()
	ctx := t.Context()
	seedScored(t, h, 1, "Acme Labs", 4.4)
	seedScored(t, h, 2, "Other Labs", 4.4)
	rowOf := func(company string) tracker.Entry {
		t.Helper()
		lk, err := d.tracker.Lookup(ctx, postingOf(company).URL)
		if err != nil || !lk.Found {
			t.Fatalf("%s: %+v %v", company, lk, err)
		}
		return *lk.Entry
	}
	acme := rowOf("Acme Labs")
	cl, err := d.ledger.Claim(ctx, ledger.ClaimRequest{URL: acme.PostingURL, Host: "jobs.example.com", Company: acme.Company, Role: acme.Role, Source: ledger.SourceApplyChain, RunID: "run_a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ledger.Finish(ctx, cl.ID, ledger.Verdict{PressDispatched: true}); err != nil {
		t.Fatal(err)
	}
	cookie := h.session()
	confirm := func(e tracker.Entry) result {
		return h.chat(cookie, "POST", "/api/candidacies/"+strconv.FormatInt(e.ID, 10)+"/confirm-submitted", nil)
	}
	r := confirm(acme)
	if got := decode[wire.Candidacy](t, r); r.StatusCode != 200 || got.Status != string(tracker.Applied) || got.AppliedBy != string(tracker.ByAgent) {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	var state, source string
	if err := h.srv.store.Load().Read().QueryRow(`SELECT state, confirm_source FROM submissions WHERE claim_id = ?`, cl.ID).Scan(&state, &source); err != nil || state != "submitted" || source != ledger.ConfirmUser {
		t.Fatalf("ledger %s %q %v", state, source, err)
	}
	if r := confirm(acme); r.StatusCode != 409 {
		t.Fatalf("a settled submission confirmed again: %d %s", r.StatusCode, r.body)
	}
	other := rowOf("Other Labs")
	if r := confirm(other); r.StatusCode != 409 || rowOf("Other Labs").Status != tracker.Evaluated {
		t.Fatalf("a row the chain never pressed: %d %s", r.StatusCode, r.body)
	}
}

func TestApplyList_LaneReadSameQueryAsPageInProcess(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	seedScored(t, h, 1, "Sample Works", 4.1)
	seedScored(t, h, 2, "Example Labs", 4.9)
	seedScored(t, h, 3, "Third Party", 4.5)
	// The page shows the lane best score first; a batch of two takes the same two rows.
	page := decode[wire.ApplyList](t, h.get("/api/apply/list?lane=apply-now")).Lanes[0].Items
	if len(page) != 3 || page[0].Company != "Example Labs" {
		t.Fatalf("page = %+v", page)
	}
	r := h.post("/api/apply/batches", wire.ApplyBatchRequest{Lanes: []string{wire.LaneApplyNow}, Limit: 2})
	if r.StatusCode != 202 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	batch := decode[wire.ApplyBatchAccepted](t, r)
	if batch.Rows != 2 || batch.BatchID == "" {
		t.Fatalf("batch = %+v", batch)
	}
	waitLong(t, "the list to end", func() bool { return !h.domainOf().apply.busy.Load() })
	v := decode[wire.ApplyBatchView](t, h.get("/api/apply/batches/"+batch.BatchID))
	if len(v.Rows) != 2 || v.Rows[0].Company != "Example Labs" || v.Rows[1].Company != "Third Party" {
		t.Fatalf("rows = %+v", v.Rows)
	}
}

// A stopped run's questions are answered from its record: only the ones an
// entry can hold are offered, a choice takes one of the form's options, and the
// answer is the user's own entry, scoped to the site unless they chose every
// site. The run then names the entry that answers each question.
func TestApplyRunQuestions_AnswerSavesTheUsersOwnEntry(t *testing.T) {
	h := newDomainHarness(t)
	lo := func(label, kind string, options ...string) apply.Leftover {
		return apply.Leftover{Label: label, Reason: apply.ReasonNeedsUser, Draft: &apply.EntryDraft{Kind: kind, Options: options}}
	}
	office := lo("Which office would you join? *", "radio-group", "Remote", "On-site")
	pages, _ := json.Marshal([]apply.PageReport{
		{URL: "https://jobs.example.test/apply/1", Leftovers: []apply.Leftover{office, lo("Account password", "text"), lo("What is your gender?", "select", "Male", "Female"),
			lo("I certify the above is true", "checkbox"), lo("Electronic signature", "text")}},
		{URL: "https://jobs.example.test/apply/2", Leftovers: []apply.Leftover{office, lo("Favourite tool", "text")}},
	})
	err := h.srv.store.Load().Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO apply_runs (run_id, started_at, finished_at, caller, posting_url, identity_key, outcome_kind, pages_json) VALUES
			('run_q', '2027-10-12T15:04:05.000Z', '2027-10-12T15:09:05.000Z', 'cli', 'https://jobs.example.test/apply', 'k', 'stopped', ?1),
			('run_w', '2027-10-12T15:04:05.000Z', NULL, 'cli', 'https://jobs.example.test/apply', 'k', 'stopped', ?1)`, string(pages))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	qs := decode[wire.ApplyRunView](t, h.get("/api/apply/runs/run_q")).Questions
	if len(qs) != 2 || qs[0].Question != "Which office would you join?" || len(qs[0].Options) != 2 || qs[1].Question != "Favourite tool" || qs[0].AnsweredBy != "" {
		t.Fatalf("questions %+v", qs)
	}
	answer := func(id, q, a string, everywhere bool) result {
		return h.post("/api/apply/runs/"+id+"/answers", wire.ApplyAnswerRequest{Question: q, Answer: a, Everywhere: everywhere})
	}
	for _, c := range []struct {
		id, q, a string
		status   int
	}{
		{"run_q", "Which office would you join?", "Hybrid", 422}, {"run_q", "Account password", "x", 404}, {"run_w", "Favourite tool", "Go", 404},
	} {
		if r := answer(c.id, c.q, c.a, false); r.StatusCode != c.status {
			t.Errorf("%s %q: %d %s", c.id, c.q, r.StatusCode, r.body)
		}
	}
	saved := decode[wire.ApplyAnswerSaved](t, answer("run_q", "Which office would you join?", " remote ", false))
	tool := decode[wire.ApplyAnswerSaved](t, answer("run_q", "Favourite tool", "Go", true))
	if r := answer("run_q", "Which office would you join?", "On-site", false); r.StatusCode != 409 || r.errReason() != "already-answered" {
		t.Fatalf("a second answer: %d %s", r.StatusCode, r.body)
	}
	file, err := h.domainOf().bank.File()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range file.Bank.Entries {
		if e.ID == saved.EntryID && (e.EffectiveCreatedBy() != "user" || e.NeedsReview || e.Preferred[0] != "Remote" || e.CapturedScope[0].Host != "jobs.example.test") ||
			e.ID == tool.EntryID && (e.Answer != "Go" || len(e.CapturedScope) != 0 || e.NeedsReview) {
			t.Errorf("entry %+v", e)
		}
	}
	qs = decode[wire.ApplyRunView](t, h.get("/api/apply/runs/run_q")).Questions
	if qs[0].AnsweredBy != saved.EntryID || qs[0].Answer != "Remote" || qs[1].AnsweredBy != tool.EntryID {
		t.Fatalf("answered questions %+v", qs)
	}
}

// answeredRun records a stopped run of the user with the given questions left,
// its start request, and its board entry waiting for answers with tab kept.
func answeredRun(t *testing.T, h *harness, id, tab string, questions ...string) {
	t.Helper()
	waitingFor(t, h, id, "answer", tab, questions...)
}

// waitingFor is answeredRun for a run whose board entry waits for reason.
func waitingFor(t *testing.T, h *harness, id, reason, tab string, questions ...string) {
	t.Helper()
	var left []apply.Leftover
	for _, q := range questions {
		left = append(left, apply.Leftover{Label: q, Reason: apply.ReasonNeedsUser, Draft: &apply.EntryDraft{Kind: "text"}})
	}
	waitingWith(t, h, id, reason, tab, left...)
}

// waitingWith is waitingFor with the leftovers the run recorded.
func waitingWith(t *testing.T, h *harness, id, reason, tab string, left ...apply.Leftover) {
	t.Helper()
	pages, _ := json.Marshal([]apply.PageReport{{URL: "https://jobs.example.test/apply/1", Leftovers: left}})
	st := h.srv.store.Load()
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		// Each run is its own posting: the board folds the waits of one posting.
		_, err := tx.Exec(`INSERT INTO apply_runs (run_id, started_at, finished_at, caller, posting_url, identity_key, outcome_kind, pages_json) VALUES
			(?1, '2027-10-12T15:04:05.000Z', '2027-10-12T15:09:05.000Z', 'dashboard', 'https://jobs.example.test/apply', ?1, 'stopped', ?2)`, id, string(pages))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	q := apply.StartRequest{RunID: id, Caller: gates.CallerDashboard, Tool: gates.ToolAutofillApplication, Explicit: true, URL: "https://jobs.example.test/apply"}
	if err := apply.SaveRequest(context.Background(), st, q, time.Now()); err != nil {
		t.Fatal(err)
	}
	openWaiting(t, h, id, notify.OriginUser, reason, tab)
}

// A first answer moves the other waiting runs it completes too (F-3, flow round
// 3): one with its tab kept goes on there, one whose tab is gone says it can be
// retried, and one still waiting on another question or on a step in the page
// stays as it was. The run the answer was saved to has a question still open.
func TestApplyRunQuestions_AFirstAnswerMovesTheOtherRunsItCompletes(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	const asked = "a bot check"
	runs := []struct {
		id, reason, tab string
		questions       []string
		state           notify.State
		detail          string
	}{
		{"run_from", "answer", "tab-1", []string{"Favourite tool", "Notice period"}, notify.NeedsYou, asked},
		{"run_kept", "answer", "tab-2", []string{"Favourite tool?"}, notify.Done, ""},
		{"run_gone", "answer", "", []string{"favourite tool"}, notify.NeedsYou, "Every question has an answer now; retry the run to fill the form with them."},
		{"run_challenge", "challenge", "tab-4", []string{"Favourite tool"}, notify.NeedsYou, asked},
		// Answered before this answer was saved, so this answer did not complete it.
		{"run_before", "answer", "tab-5", []string{"Years with Go"}, notify.NeedsYou, asked},
	}
	if _, err := h.domainOf().learn.AddEntry(t.Context(), learn.AddEntryRequest{Question: "Years with Go", Answer: "6", Host: "jobs.example.test", Everywhere: true}); err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		waitingFor(t, h, r.id, r.reason, r.tab, r.questions...)
	}
	saved := decode[wire.ApplyAnswerSaved](t, h.post("/api/apply/runs/run_from/answers", wire.ApplyAnswerRequest{Question: "Favourite tool", Answer: "Go", Everywhere: true}))
	if saved.Continued {
		t.Fatalf("the run with a question still open went on: %+v", saved)
	}
	for _, r := range runs {
		e := waitEntry(t, h, r.id, func(e notify.Entry) bool { return e.State != notify.Working })
		if e.State != r.state || r.detail != "" && e.Detail != r.detail {
			t.Errorf("%s: %s %q, want %s %q", r.id, e.State, e.Detail, r.state, r.detail)
		}
	}
}

// A follow-up the engine named after the question before it ("...? If yes,
// please provide details") is asked after that question (flow round 5). Once
// the user answers that question No the follow-up is answered "N/A" through
// it: no list asks it, the Ask Agent is not offered it, and a run that waited
// on nothing else goes on. Under a Yes it is still the user's to answer.
func TestApplyRunQuestions_AFollowUpIsAskedAfterTheQuestionItFollows(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	const felony, noncompete, details = "Have you ever been convicted of a felony?", "Do you have a non-compete agreement?", " If yes, please provide details"
	pair := func(gate string) []apply.Leftover {
		return []apply.Leftover{
			{Label: gate + " *", Reason: apply.ReasonNeedsUser, Draft: &apply.EntryDraft{Kind: "radio-group", Options: []string{"Yes", "No"}}},
			{Label: gate + details + " *", Reason: apply.ReasonNeedsUser, Draft: &apply.EntryDraft{Kind: "text"}},
		}
	}
	waitingWith(t, h, "run_no", "answer", "tab-1", pair(felony)...)
	waitingWith(t, h, "run_retry", "answer", "", pair(felony)...)
	waitingWith(t, h, "run_yes", "answer", "tab-2", pair(noncompete)...)
	asked := func() []string {
		var out []string
		for _, a := range decode[wire.AnswerBankUnanswered](t, h.get("/api/answer-bank/unanswered")).Asked {
			out = append(out, a.Question)
		}
		return out
	}
	answer := func(id, q, a string) wire.ApplyAnswerSaved {
		return decode[wire.ApplyAnswerSaved](t, h.post("/api/apply/runs/"+id+"/answers", wire.ApplyAnswerRequest{Question: q, Answer: a}))
	}
	no := answer("run_no", felony, "No")
	if !no.Continued {
		t.Errorf("the run that waited on the follow-up alone did not go on: %+v", no)
	}
	if yes := answer("run_yes", noncompete, "Yes"); yes.Continued {
		t.Errorf("the run whose follow-up a Yes leaves open went on: %+v", yes)
	}
	if got := asked(); slices.Contains(got, felony+details) || !slices.Contains(got, noncompete+details) {
		t.Errorf("asked after the answers: %q", got)
	}
	qs := decode[wire.ApplyRunView](t, h.get("/api/apply/runs/run_retry")).Questions
	if len(qs) != 2 || qs[1].Question != felony+details || qs[1].AnsweredBy != no.EntryID || qs[1].Answer != "N/A" {
		t.Errorf("the run waiting for its retry: %+v", qs)
	}
	if e := entryOf(t, h, "run_retry"); e.Detail != "Every question has an answer now; retry the run to fill the form with them." {
		t.Errorf("its entry: %+v", e)
	}
	offered := func(id string) []string {
		got, err := h.srv.apply.StoppedRuns().Questions(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, q := range got {
			out = append(out, q.Question)
		}
		return out
	}
	if got := offered("run_retry"); !slices.Equal(got, []string{felony}) {
		t.Errorf("offered to the agent for the settled run: %q", got)
	}
	if got := offered("run_yes"); !slices.Equal(got, []string{noncompete, noncompete + details}) {
		t.Errorf("offered to the agent for the open run: %q", got)
	}
}

// The Ask Agent's answer applies where the questions form's starts (F-4): on
// every site, unless the question names the company, so the next run on
// another site, in another kind of control, finds it (flow round 3).
func TestApplyRunQuestions_TheAgentsAnswerAppliesWhereTheFormsWould(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	cases := []struct {
		question, answer string
		options          []string
		elsewhere        string
	}{
		{"Favourite tool", "Go", []string{"Rust", "Go"}, "Go"},
		{"Have you worked here before?", "No", nil, ""},
	}
	answeredRun(t, h, "run_a", "", cases[0].question, cases[1].question)
	for _, c := range cases {
		if _, err := h.srv.apply.StoppedRuns().SaveAnswer(t.Context(), "run_a", c.question, c.answer, nil); err != nil {
			t.Fatal(err)
		}
		route, err := h.domainOf().learn.Routes()
		if err != nil {
			t.Fatal(err)
		}
		if got := route.Route("careers.example.org", match.Posting{}, c.question, c.options).Outright(); got.Answer != c.elsewhere {
			t.Errorf("%q on another site: %+v", c.question, got)
		}
	}
}

// A required field the questions do not ask (a value the site refused, a
// password, a signature) is named to the agent for the user to complete in the
// kept tab, so the agent does not tell the user its questions were all there
// was (Grafana's LinkedIn box refused the persona's link, live).
func TestApplyRunQuestions_TheAgentIsToldTheFieldsOnlyTheTabTakes(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	lo := func(label, kind string) apply.Leftover {
		return apply.Leftover{Label: label, Reason: apply.ReasonNeedsUser, Draft: &apply.EntryDraft{Kind: kind}}
	}
	refused := apply.Leftover{Label: "LinkedIn Profile *", Reason: apply.ReasonNeedsUser, Detail: "bank-declined:no-entry"}
	pages, _ := json.Marshal([]apply.PageReport{
		{URL: "https://jobs.example.test/apply/1", Leftovers: []apply.Leftover{lo("Favourite tool", "text"), refused, lo("Account password", "text")}},
		{URL: "https://jobs.example.test/apply/2", Leftovers: []apply.Leftover{refused, lo("Electronic signature", "text")}},
	})
	err := h.srv.store.Load().Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO apply_runs (run_id, started_at, finished_at, caller, posting_url, identity_key, outcome_kind, pages_json) VALUES
			('run_t', '2027-10-12T15:04:05.000Z', '2027-10-12T15:09:05.000Z', 'cli', 'https://jobs.example.test/apply', 'k', 'stopped', ?1),
			('run_s', '2027-10-12T15:04:05.000Z', '2027-10-12T15:09:05.000Z', 'cli', 'https://jobs.example.test/apply', 'k', 'submitted', ?1)`, string(pages))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	runs := h.srv.apply.StoppedRuns()
	qs, err := runs.Questions(t.Context(), "run_t")
	if err != nil || len(qs) != 1 || qs[0].Question != "Favourite tool" {
		t.Fatalf("questions %+v, %v", qs, err)
	}
	tab, err := runs.InTab(t.Context(), "run_t")
	if err != nil || !slices.Equal(tab, []string{"LinkedIn Profile", "Account password", "Electronic signature"}) {
		t.Fatalf("in the tab %q, %v", tab, err)
	}
	if tab, err := runs.InTab(t.Context(), "run_s"); err != nil || len(tab) != 0 {
		t.Fatalf("a submitted run: %q, %v", tab, err)
	}
}

// "Let the agent finish" works in the tab the run kept: a run without one is
// refused before any conversation starts (AC-4).
func TestApplyRunTakeover_ARunWithoutItsKeptTabIsRefused(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	answeredRun(t, h, "run_gone", "", "Notice period")
	cookie := h.session()
	for path, status := range map[string]int{"/api/apply/runs/run_gone/takeover": 409, "/api/apply/runs/run_missing/takeover": 404} {
		if r := h.chat(cookie, "POST", path, nil); r.StatusCode != status {
			t.Errorf("%s: %d %s", path, r.StatusCode, r.body)
		}
	}
	if _, n, err := h.domainOf().sessions.List(t.Context(), chatsessions.ListQuery{}); err != nil || n != 0 {
		t.Fatalf("%d conversations, %v", n, err)
	}
}

// On a run that kept its tab, "Let the agent finish" starts the button's turn in
// a dashboard conversation: the fixed message names the run, and the turn is
// given the run tools and the apply tools that work its kept tab, and no other tool that writes or acts (AC-4).
func TestApplyRunTakeover_AKeptRunStartsTheButtonsTurn(t *testing.T) {
	var mu sync.Mutex
	var offered []string
	h, l := chatHarness(t, func(p *scriptedPi) {
		piStart(p)
		_, cat := p.bridge("GET", "/internal/agent/catalog", nil)
		tools, _ := cat["tools"].([]any)
		mu.Lock()
		for _, tl := range tools {
			offered = append(offered, tl.(map[string]any)["name"].(string))
		}
		mu.Unlock()
		p.emit(assistantLine([]map[string]any{{"type": "text", "text": "I read the questions."}}, "stop"))
		p.emit(map[string]any{"type": "agent_settled"})
	})
	answeredRun(t, h, "run_kept", "tab-1", "Notice period")
	r := h.chat(h.session(), "POST", "/api/apply/runs/run_kept/takeover", nil)
	if r.StatusCode != 202 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	started := decode[wire.TakeoverStarted](t, r)
	readStream(t, h, started.SessionID, "", terminalItem, l.release)
	h.waitIdle(started.SessionID)
	sess, msgs, _, err := h.domainOf().sessions.Detail(t.Context(), started.SessionID)
	if err != nil || sess.Channel != chatsessions.Dashboard || len(msgs) == 0 || msgs[0].Body != agent.FinishMessage("run_kept", "", "", "jobs.example.test") {
		t.Fatalf("conversation %+v, messages %+v, %v", sess, msgs, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(offered, agent.ToolRunQuestions) || !slices.Contains(offered, agent.ToolAnswerRunQuestion) {
		t.Fatalf("the run tools were not offered: %v", offered)
	}
	for _, name := range offered {
		applyTool := slices.Contains([]string{agent.ToolApplyFill, agent.ToolApplyAnswer, agent.ToolApplyPress, agent.ToolApplyContinue, agent.ToolApplyLeave}, name)
		if c := agent.ToolClass(name); (c == string(agent.ClassWriteLocal) || c == string(agent.ClassExternalAction)) && name != agent.ToolAnswerRunQuestion && !applyTool {
			t.Errorf("the button's turn was offered %s (%s)", name, c)
		}
	}
}
