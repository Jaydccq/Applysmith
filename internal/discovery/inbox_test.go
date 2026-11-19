package discovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/workspace"
)

func TestInbox_StatusTransitions(t *testing.T) {
	e := newEnv(t)
	res, err := e.rp.InboxAddURL(e.ctx, "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000001?lever-source=x")
	if err != nil || res.Row == nil {
		t.Fatalf("%+v %v", res, err)
	}
	id := res.Row.ID
	steps := []struct {
		name string
		do   func() error
		want string
		bad  bool
	}{
		{"evaluated needs queued", func() error { return e.rp.InboxEvaluated(e.ctx, id) }, InboxPending, true},
		{"queue", func() error { return e.queue(id, "job-1") }, InboxQueued, false},
		{"queue twice", func() error { return e.queue(id, "job-2") }, InboxQueued, true},
		{"failure returns to pending", func() error { return e.rp.InboxEvalFailed(e.ctx, id, "provider timed out", ChargeAttempt) }, InboxPending, false},
		{"queue again", func() error { return e.queue(id, "job-3") }, InboxQueued, false},
		{"evaluated", func() error { return e.rp.InboxEvaluated(e.ctx, id) }, InboxEvaluated, false},
		{"expire", func() error { return e.rp.InboxExpire(e.ctx, id) }, InboxExpired, false},
		{"dismiss", func() error { return e.rp.InboxDismiss(e.ctx, id) }, InboxDismissed, false},
		{"dismissed is final", func() error { return e.rp.InboxExpire(e.ctx, id) }, InboxDismissed, true},
	}
	for _, s := range steps {
		err := s.do()
		if s.bad != errors.Is(err, ErrBadTransition) || (!s.bad && err != nil) {
			t.Fatalf("%s: err = %v", s.name, err)
		}
		row, _ := e.rp.InboxGet(e.ctx, id)
		if row.Status != s.want {
			t.Fatalf("%s: status %s, want %s", s.name, row.Status, s.want)
		}
	}
	row, _ := e.rp.InboxGet(e.ctx, id)
	if row.Notes != "provider timed out" || row.Attempts != 2 {
		t.Fatalf("row = %+v", row)
	}
	if err := e.rp.InboxDismiss(e.ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestInbox_AddURL_CanonicalizesAndDeduplicates(t *testing.T) {
	e := newEnv(t)
	first, err := e.rp.InboxAddURL(e.ctx, "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000001?lever-source=x")
	if err != nil || first.Row == nil || first.Row.IdentityKey != "lever:example-co/5d1c0a9e-0000-4000-8000-000000000001" ||
		first.Row.URL != "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000001" {
		t.Fatalf("first = %+v %v", first, err)
	}
	if first.Row.OriginalURL == first.Row.URL {
		t.Fatal("the original URL must be kept beside the canonical one")
	}
	again, err := e.rp.InboxAddURL(e.ctx, "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000001/apply")
	if err != nil || again.Row != nil || again.Duplicate != StoreInbox || again.MatchID != first.Row.ID {
		t.Fatalf("again = %+v %v", again, err)
	}
	if _, err := e.rp.InboxAddURL(e.ctx, "ftp://nope"); err == nil {
		t.Fatal("a non-web URL must be refused")
	}
}

func TestInbox_AddURL_ReportsMatchingTrackerRow(t *testing.T) {
	e := newEnv(t)
	en := e.trackedRow("Example Co", "Engineer", "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000002")
	res, err := e.rp.InboxAddURL(e.ctx, "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000002?utm_source=x")
	if err != nil || res.Row != nil || res.Duplicate != StoreTracker || res.MatchID != en.ID {
		t.Fatalf("res = %+v %v", res, err)
	}
	if len(e.inbox(InboxQuery{})) != 0 {
		t.Fatal("no row may be created")
	}
}

func TestInbox_PromoteTop_SkipsRejected(t *testing.T) {
	e := newEnv(t)
	for _, id := range []string{"01", "02", "03"} {
		if _, err := e.rp.InboxAddURL(e.ctx, "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-0000000000"+id); err != nil {
			t.Fatal(err)
		}
	}
	rows := e.inbox(InboxQuery{})
	gate := func(r InboxRow) string {
		if r.ID == rows[0].ID {
			return "reject_clearance"
		}
		return ""
	}
	queued, rejected, err := e.rp.PromoteTop(e.ctx, e.enq, gate, 2)
	if err != nil || len(queued) != 2 || rejected[rows[0].ID] != "reject_clearance" {
		t.Fatalf("queued %v rejected %v err %v", queued, rejected, err)
	}
}

func TestInboxReleaseQueued_ReturnsOnlyQueuedRowsToPending(t *testing.T) {
	e := newEnv(t)
	var ids []int64
	for _, u := range []string{
		"https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000011",
		"https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000012",
		"https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000013",
	} {
		res, err := e.rp.InboxAddURL(e.ctx, u)
		if err != nil || res.Row == nil {
			t.Fatalf("%s: %+v %v", u, res, err)
		}
		ids = append(ids, res.Row.ID)
	}
	for _, id := range ids[:2] {
		if err := e.queue(id, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.rp.InboxDismiss(e.ctx, ids[2]); err != nil {
		t.Fatal(err)
	}
	n, err := e.rp.InboxReleaseQueued(e.ctx, "evaluation interrupted by a restart")
	if err != nil || n != 2 {
		t.Fatalf("released %d, %v", n, err)
	}
	for i, want := range []string{InboxPending, InboxPending, InboxDismissed} {
		row, _ := e.rp.InboxGet(e.ctx, ids[i])
		if row.Status != want {
			t.Errorf("row %d is %s, want %s", ids[i], row.Status, want)
		}
	}
	if row, _ := e.rp.InboxGet(e.ctx, ids[0]); !strings.Contains(row.Notes, "interrupted") {
		t.Errorf("notes = %q", row.Notes)
	}
}

func TestInbox_HistoryAndInboxAtomic(t *testing.T) {
	const url = "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000003"
	count := func(e *env, table string) int {
		var n int
		if err := e.st.Read().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	e := newEnv(t)
	if res, err := e.rp.InboxAddURL(e.ctx, url); err != nil || res.Row == nil {
		t.Fatalf("add = %+v %v", res, err)
	}
	if count(e, "inbox") != 1 || count(e, "scan_history") != 1 {
		t.Fatalf("a committed add writes the row and its history together: inbox %d, history %d", count(e, "inbox"), count(e, "scan_history"))
	}

	// A history write that fails takes the inbox insert with it, and the
	// caller sees the failure.
	broken := newEnv(t)
	broken.exec(`CREATE TRIGGER history_refuses BEFORE INSERT ON scan_history BEGIN SELECT RAISE(ABORT, 'history unavailable'); END`)
	if _, err := broken.rp.InboxAddURL(broken.ctx, url); err == nil {
		t.Fatal("a failed history write was swallowed")
	}
	if count(broken, "inbox") != 0 || count(broken, "scan_history") != 0 {
		t.Fatalf("a half-written add: inbox %d, history %d", count(broken, "inbox"), count(broken, "scan_history"))
	}
}

// waitingRow inserts a pending row through the scan pipeline's own writer.
func (e *env) waitingRow(source, id string, score int) InboxRow {
	e.t.Helper()
	p := Posting{Source: source, URL: "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-0000000000" + id, Company: "Example Co", Title: "Engineer " + id, Score: score}
	var rowID int64
	if err := e.st.Write(e.ctx, func(tx *sql.Tx) error {
		var err error
		rowID, _, err = e.rp.insertInbox(e.ctx, tx, p, "", "saved search", "run-1")
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
	row, err := e.rp.InboxGet(e.ctx, rowID)
	if err != nil {
		e.t.Fatal(err)
	}
	return row
}

// A row the pre-filter scored below zero that waited two weeks expires: the
// drain takes the best rows first and never reaches it, and it would count as
// waiting for evaluation forever. A row at the floor, and a younger one, stay.
func TestInbox_ExpireStale_ShedsOldRowsBelowTheFloor(t *testing.T) {
	e := newEnv(t)
	old, floor := e.waitingRow("registry", "01", -1), e.waitingRow("registry", "02", 0)
	e.clk.advance(InboxStaleAge - time.Hour)
	young := e.waitingRow("registry", "03", -1)
	e.clk.advance(2 * time.Hour)
	if n, err := e.rp.ExpireStale(e.ctx); err != nil || n != 1 {
		t.Fatalf("expired %d, %v; want 1", n, err)
	}
	for _, c := range []struct {
		row  InboxRow
		want string
	}{{old, InboxExpired}, {floor, InboxPending}, {young, InboxPending}} {
		if row, _ := e.rp.InboxGet(e.ctx, c.row.ID); row.Status != c.want {
			t.Errorf("%s (score %d): %s, want %s", c.row.Role, c.row.Score, row.Status, c.want)
		}
	}
}

// The scheduled drain takes the best-ranked waiting rows first, in auto mode
// whatever the source's own mode, up to its limit; a row whose one earlier
// evaluation failed gets its retry.
func TestInbox_EvaluateWaiting_BestRankedFirstInAutoModeWithinTheLimit(t *testing.T) {
	e := newEnv(t)
	low, best, retried := e.waitingRow("registry", "01", 3), e.waitingRow("registry", "02", 9), e.waitingRow("linkedin", "03", 6)
	e.exec(`UPDATE inbox SET attempts = 1 WHERE id = ?`, retried.ID)
	if _, err := e.rp.EvaluateWaiting(e.ctx, e.enq, nil, 2, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(e.enq.reqs) != 2 || e.enq.reqs[0].InboxID != best.ID || e.enq.reqs[1].InboxID != retried.ID {
		t.Fatalf("enqueued %+v", e.enq.reqs)
	}
	for _, r := range e.enq.reqs {
		if r.Mode != "auto" {
			t.Errorf("request %+v", r)
		}
	}
	if row, _ := e.rp.InboxGet(e.ctx, low.ID); row.Status != InboxPending {
		t.Errorf("the row past the limit = %+v", row)
	}
}

// Each row the drain must leave waiting: the gate rejects it (the rule is kept
// in its reasons), its evaluation already failed twice, the day's spend cap is
// reached, or the pool refuses work (the pass ends with the refusal).
func TestInbox_EvaluateWaiting_LeavesRowsWaiting(t *testing.T) {
	for _, c := range []struct {
		name     string
		attempts int
		gate     Gate
		capped   bool
		refuse   bool
		reason   string
	}{
		{name: "gate rejects", gate: func(InboxRow) string { return "reject_clearance" }, reason: "gate:reject_clearance"},
		{name: "failed twice", attempts: 2},
		{name: "spend cap reached", capped: true},
		{name: "pool refuses", refuse: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			row := e.waitingRow("registry", "11", 9)
			e.exec(`UPDATE inbox SET attempts = ? WHERE id = ?`, c.attempts, row.ID)
			var enq Enqueuer = e.enq
			if c.refuse {
				enq = failingPool{}
			}
			_, err := e.rp.EvaluateWaiting(e.ctx, enq, c.gate, 5, func(context.Context) bool { return c.capped }, nil)
			if (err != nil) != c.refuse || len(e.enq.reqs) != 0 {
				t.Fatalf("err %v, enqueued %+v", err, e.enq.reqs)
			}
			got, _ := e.rp.InboxGet(e.ctx, row.ID)
			if got.Status != InboxPending || (c.reason != "" && !slices.Contains(got.Reasons, c.reason)) {
				t.Fatalf("row = %+v", got)
			}
		})
	}
}

// An evaluation that a fault of the app's own machine ends spends none of the
// row's attempts, and it can end while the drain's run still sends rows: the
// row is waiting again before the run is over (flow round 9, E-1). The run
// takes each row once all the same, well inside its limit, and the next run
// takes it again.
func TestInbox_EvaluateWaiting_TakesEachRowOncePerRun(t *testing.T) {
	e := newEnv(t)
	best, next := e.waitingRow("registry", "21", 9), e.waitingRow("registry", "22", 5)
	pool := &syncPool{rp: e.rp, fail: true, charge: ChargeUndecided}
	for run := 1; run <= 2; run++ {
		res, err := e.rp.EvaluateWaiting(e.ctx, pool, nil, 10, nil, nil)
		if err != nil || res.Queued != 2 || !slices.Equal(pool.ids[2*(run-1):], []int64{best.ID, next.ID}) || slices.ContainsFunc(pool.errs, func(err error) bool { return err != nil }) {
			t.Fatalf("run %d: result %+v, err %v, rows sent %v, job errors %v", run, res, err, pool.ids, pool.errs)
		}
	}
	for _, id := range []int64{best.ID, next.ID} {
		if row, _ := e.rp.InboxGet(e.ctx, id); row.Status != InboxPending || row.Attempts != 0 {
			t.Errorf("row %d after two runs = %s with %d attempts", id, row.Status, row.Attempts)
		}
	}
}

// A read that decided nothing about the posting spends no attempt for the
// row's first three of them and one for each after, so the drain takes a row
// whose page always fails so in five runs, three free and then its two
// attempts, and leaves it to the user instead of giving it a slot of every
// run. A shutdown spends nothing however often it comes (flow round 10, EF-2).
func TestInbox_EvaluateWaiting_ARowThatNeverReadsIsLeftToTheUser(t *testing.T) {
	for _, c := range []struct {
		charge Charge
		taken  int
	}{{ChargeAttempt, 2}, {ChargeUndecided, 5}, {ChargeNone, 7}} {
		e := newEnv(t)
		e.waitingRow("registry", "31", 9)
		pool := &syncPool{rp: e.rp, fail: true, charge: c.charge}
		for range 7 {
			if _, err := e.rp.EvaluateWaiting(e.ctx, pool, nil, 10, nil, nil); err != nil {
				t.Fatal(err)
			}
		}
		if len(pool.ids) != c.taken || slices.ContainsFunc(pool.errs, func(err error) bool { return err != nil }) {
			t.Errorf("charge %d: seven runs took the row %d times (%v), want %d", c.charge, len(pool.ids), pool.errs, c.taken)
		}
	}
}

// The count of a row's free failures is never shown with its reasons, and a
// gate rejection that adds its rule there keeps it, so the bound goes on from
// where it was: one free read, a rejected run, two more free reads and the
// two attempts (flow round 10).
func TestInbox_TheFreeFailureCountIsKeptButNeverShown(t *testing.T) {
	e := newEnv(t)
	row := e.waitingRow("registry", "41", 9)
	pool := &syncPool{rp: e.rp, fail: true, charge: ChargeUndecided}
	for _, reject := range []bool{false, true, false, false, false, false, false, false} {
		gate := func(InboxRow) string {
			if reject {
				return "location"
			}
			return ""
		}
		if _, err := e.rp.EvaluateWaiting(e.ctx, pool, gate, 10, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	got, err := e.rp.InboxGet(e.ctx, row.ID)
	if err != nil || len(pool.ids) != 5 || !slices.Equal(got.Reasons, []string{"gate:location"}) {
		t.Errorf("taken %d times, reasons %q (%v); want 5 and only the gate's rule", len(pool.ids), got.Reasons, err)
	}
}

// The drain takes the quick-screen reviews that wait before any new row, each
// as the evaluation in depth that answers it, inside the same limit and the
// same spend cap (F-8).
func TestInbox_EvaluateWaiting_TakesTheWaitingReviewsFirst(t *testing.T) {
	review := EvalRequest{InboxID: 41, URL: "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000041", Mode: "deep", Source: "linkedin",
		Title: "Engineer 41", Company: "Example Co", Review: "run_review"}
	for _, c := range []struct {
		name   string
		limit  int
		capped bool
		want   []string // the reviews and rows sent, in order
	}{
		{name: "the limit admits one", limit: 1, want: []string{"run_review"}},
		{name: "the limit admits both", limit: 2, want: []string{"run_review", "row"}},
		{name: "the spend cap is reached", limit: 2, capped: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			row := e.waitingRow("registry", "12", 9)
			res, err := e.rp.EvaluateWaiting(e.ctx, e.enq, nil, c.limit, func(context.Context) bool { return c.capped }, []EvalRequest{review})
			if err != nil {
				t.Fatal(err)
			}
			var sent []string
			for _, r := range e.enq.reqs {
				switch {
				case r.Review != "" && r.Mode == "deep":
					sent = append(sent, r.Review)
				case r.InboxID == row.ID:
					sent = append(sent, "row")
				}
			}
			if !slices.Equal(sent, c.want) || res.Reviews != min(len(c.want), 1) {
				t.Fatalf("sent %v (%+v), result %+v", sent, e.enq.reqs, res)
			}
		})
	}
}

// The Ask Agent and Telegram find the waiting row by the posting's URL as the
// inbox de-duplicates it, and never take a tracker row's id for an inbox row.
func TestInbox_RowForURL_FindsTheRowThatHoldsThePosting(t *testing.T) {
	e := newEnv(t)
	added, err := e.rp.InboxAddURL(e.ctx, "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000041")
	if err != nil || added.Row == nil {
		t.Fatal(added, err)
	}
	row, ok, err := e.rp.InboxRowFor(e.ctx, "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000041/apply?lever-source=x")
	if err != nil || !ok || row.ID != added.Row.ID {
		t.Fatalf("row = %+v ok %v err %v", row, ok, err)
	}
	e.trackedRow("Example Co", "Engineer", "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000042")
	if _, ok, err := e.rp.InboxRowFor(e.ctx, "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000042"); ok || err != nil {
		t.Errorf("a tracked posting: ok %v err %v", ok, err)
	}
}

// One Admitter serves a batch: a posting it admitted is the inbox's when the
// batch hands it over again, by its address or as the same opening.
func TestAdmitter_APostingItAdmittedIsADuplicateForTheRestOfTheBatch(t *testing.T) {
	e := newEnv(t)
	a, err := e.rp.NewAdmitter(e.ctx, portals(nil, nil), workspace.Profile{})
	if err != nil {
		t.Fatal(err)
	}
	p := Posting{Source: "signals", Company: "Example Co", Title: "Software Engineer", Location: "Austin, TX", URL: "https://job-boards.greenhouse.io/example/jobs/100"}
	first, err := a.Admit(e.ctx, p, "")
	if err != nil || first.Row == nil {
		t.Fatalf("first: %+v, %v", first, err)
	}
	same := p
	same.URL = "https://careers.example.com/openings/software-engineer"
	for _, again := range []Posting{p, same} {
		if got, err := a.Admit(e.ctx, again, ""); err != nil || got.Duplicate != StoreInbox || got.MatchID != first.Row.ID {
			t.Fatalf("%s again: %+v, %v", again.URL, got, err)
		}
	}
}

// Rows scanned before the seeker named a target role follow the roles saved
// later, and a change of them; every other term stays as admitted.
func TestInboxList_PendingRowsFollowTheTargetRolesSavedAfterTheScan(t *testing.T) {
	e := newEnv(t)
	var profile workspace.Profile
	e.rp.RankWith(func() (workspace.Profile, workspace.Portals, error) { return profile, portals(nil, nil), nil })
	for i, p := range []Posting{
		{Title: "Account Executive"},
		{Title: "Software Engineer"},
		{Title: "Software Engineer, New Grad", PostedAt: e.clk.now().Add(-48 * time.Hour)},
	} {
		p.Source, p.Company, p.URL = "manual", "Example Co", fmt.Sprintf("https://job-boards.greenhouse.io/example/jobs/%d", 100+i)
		if a, err := e.rp.AdmitPosting(e.ctx, portals(nil, nil), workspace.Profile{}, p, ""); err != nil || a.Row == nil {
			t.Fatalf("admit %q: %+v, %v", p.Title, a, err)
		}
	}
	listed := func() []string {
		t.Helper()
		rows, err := e.rp.InboxList(e.ctx, InboxQuery{Status: InboxPending})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range rows {
			out = append(out, fmt.Sprintf("%s %d [%s]", r.Role, r.Score, strings.Join(r.Reasons, " ")))
		}
		return out
	}
	steps := []struct {
		roles []workspace.Role
		want  []string
	}{
		{nil, []string{"Software Engineer, New Grad 3 [recency:+3]", "Account Executive 0 []", "Software Engineer 0 []"}},
		{[]workspace.Role{{Title: "Software Engineer", Seniority: "senior"}}, []string{
			"Software Engineer 4 [target-role(Software Engineer):+4]",
			"Software Engineer, New Grad 3 [target-role(Software Engineer):+4 early-career:-4 recency:+3]",
			"Account Executive 0 []",
		}},
		{[]workspace.Role{{Title: "Software Engineer", Seniority: "junior"}}, []string{
			"Software Engineer, New Grad 10 [target-role(Software Engineer):+4 early-career:+3 recency:+3]",
			"Software Engineer 4 [target-role(Software Engineer):+4]",
			"Account Executive 0 []",
		}},
		{nil, []string{"Software Engineer, New Grad 3 [recency:+3]", "Account Executive 0 []", "Software Engineer 0 []"}},
	}
	for i, s := range steps {
		profile.Targets.Roles = s.roles
		if got := listed(); !reflect.DeepEqual(got, s.want) {
			t.Fatalf("step %d, targets %v:\n got %q\nwant %q", i, s.roles, got, s.want)
		}
	}
}
