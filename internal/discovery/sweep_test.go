package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func (e *env) stableBoard(slug string) {
	e.serveGH(slug, []obj{ghItem(1, "Software Engineer", "Austin, TX")}, nil)
}

// withLiveBoard adds a board that answers, because a run strikes nothing
// unless some visit in it succeeded.
func (e *env) withLiveBoard() {
	e.stableBoard("ok-co")
	e.add(ghBoard("ok-co"))
}

func TestSweep_304PreservesCountsStoresNewETag(t *testing.T) {
	e := newEnv(t)
	path := "/v1/boards/example-co/jobs"
	b, _ := json.Marshal(ghJobs("example-co", ghItem(1, "Software Engineer", "Austin, TX"), ghItem(2, "Software Engineer II", "Austin, TX")))
	e.srv.set(path, route{body: string(b), etag: `"v1"`})
	e.srv.json("/v1/boards/example-co/jobs/1", obj{"id": 1, "content": "<p>x</p>"})
	e.srv.json("/v1/boards/example-co/jobs/2", obj{"id": 2, "content": "<p>x</p>"})
	e.add(ghBoard("example-co"))
	first := e.run(ScanRequest{Portals: eng()})
	row := e.registry(ghBoard("example-co"))
	if first.Counts.Inserted != 2 || row.ETag != `"v1"` || row.LastJobCount != 2 {
		t.Fatalf("first: %+v row %+v", first.Counts, row)
	}
	e.clk.advance(time.Hour)
	second := e.run(ScanRequest{Portals: eng()})
	after := e.registry(ghBoard("example-co"))
	if second.Counts.Discovered != 0 || second.Boards != 1 {
		t.Fatalf("a 304 must not re-process postings: %+v", second.Counts)
	}
	if after.LastJobCount != 2 || after.Strikes != 0 || after.Status != StatusActive || !after.LastScanned.After(row.LastScanned) {
		t.Fatalf("row after 304 = %+v", after)
	}
	// A changed board stores the new ETag.
	e.srv.set(path, route{body: string(b), etag: `"v2"`})
	e.run(ScanRequest{Portals: eng()})
	if got := e.registry(ghBoard("example-co")).ETag; got != `"v2"` {
		t.Fatalf("etag = %s", got)
	}
}

func TestSweep_ThreeFailuresDead_TransientNeverStrikes(t *testing.T) {
	e := newEnv(t)
	e.withLiveBoard()
	e.srv.set("/v1/boards/gone-co/jobs", route{status: 404})
	e.add(ghBoard("gone-co"))
	for i, want := range []struct {
		strikes int
		status  string
	}{{1, StatusActive}, {2, StatusActive}, {3, StatusDead}} {
		rec := e.run(ScanRequest{Portals: eng()})
		row := e.registry(ghBoard("gone-co"))
		if row.Strikes != want.strikes || row.Status != want.status {
			t.Fatalf("visit %d: row %+v", i+1, row)
		}
		if rec.Counts.BoardErrors != 1 {
			t.Fatalf("board errors = %d", rec.Counts.BoardErrors)
		}
	}
	hits := e.srv.hitCount("/v1/boards/gone-co/jobs")
	e.run(ScanRequest{Portals: eng()})
	if e.srv.hitCount("/v1/boards/gone-co/jobs") != hits {
		t.Fatal("a dead board must not be requested")
	}

	// Transient errors never strike, even over many runs.
	e2 := newEnv(t)
	e2.srv.set("/v1/boards/flaky-co/jobs", route{status: 503, body: "busy"})
	e2.add(ghBoard("flaky-co"))
	for i := 0; i < 3; i++ {
		e2.run(ScanRequest{Portals: eng()})
	}
	row := e2.registry(ghBoard("flaky-co"))
	if row.Strikes != 0 || row.Status != StatusActive {
		t.Fatalf("flaky row = %+v", row)
	}
}

func TestSweep_FailureKinds(t *testing.T) {
	for name, body := range map[string]string{"unparsable": `{"jobs": "none"}`, "empty": `{"jobs": []}`} {
		e := newEnv(t)
		e.withLiveBoard()
		e.srv.set("/v1/boards/x-co/jobs", route{body: body})
		e.add(ghBoard("x-co"))
		e.run(ScanRequest{Portals: eng()})
		if e.registry(ghBoard("x-co")).Strikes != 1 {
			t.Errorf("%s payload did not strike", name)
		}
	}
}

func TestSweep_SuccessResetsStrikes(t *testing.T) {
	e := newEnv(t)
	e.withLiveBoard()
	e.srv.set("/v1/boards/x-co/jobs", route{status: 404})
	e.add(ghBoard("x-co"))
	e.run(ScanRequest{Portals: eng()})
	e.run(ScanRequest{Portals: eng()})
	e.stableBoard("x-co")
	e.run(ScanRequest{Portals: eng()})
	if row := e.registry(ghBoard("x-co")); row.Strikes != 0 || row.Status != StatusActive {
		t.Fatalf("row = %+v", row)
	}
}

func TestSweep_SkipsDeadAndBlocked_ProbeRevives(t *testing.T) {
	e := newEnv(t)
	e.stableBoard("live-co")
	e.srv.set("/v1/boards/dead-co/jobs", route{status: 404})
	e.stableBoard("blocked-co")
	e.add(ghBoard("live-co"), ghBoard("dead-co"), ghBoard("blocked-co"))
	if err := e.rp.RegistrySetStatus(e.ctx, ghBoard("blocked-co"), StatusBlocked); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		e.run(ScanRequest{Portals: eng()})
	}
	if e.registry(ghBoard("dead-co")).Status != StatusDead {
		t.Fatal("dead-co should be dead")
	}
	if e.srv.hitCount("/v1/boards/blocked-co/jobs") != 0 {
		t.Fatal("a blocked board was requested")
	}
	// Not due yet.
	res, err := e.sc.ProbeDead(e.ctx, eng())
	if err != nil || res.Probed != 0 {
		t.Fatalf("probe before 30 days: %+v %v", res, err)
	}
	e.clk.advance(31 * 24 * time.Hour)
	e.stableBoard("dead-co")
	res, err = e.sc.ProbeDead(e.ctx, eng())
	if err != nil || res.Probed != 1 || res.Revived != 1 {
		t.Fatalf("probe = %+v %v", res, err)
	}
	if row := e.registry(ghBoard("dead-co")); row.Status != StatusActive || row.Strikes != 0 {
		t.Fatalf("revived row = %+v", row)
	}
	if e.registry(ghBoard("blocked-co")).Status != StatusBlocked {
		t.Fatal("a blocked row was revived")
	}
}

func TestSweep_FailedProbeKeepsDeadAndDelaysNext(t *testing.T) {
	e := newEnv(t)
	e.withLiveBoard()
	e.srv.set("/v1/boards/dead-co/jobs", route{status: 404})
	e.add(ghBoard("dead-co"))
	for i := 0; i < 3; i++ {
		e.run(ScanRequest{Portals: eng()})
	}
	e.clk.advance(31 * 24 * time.Hour)
	res, _ := e.sc.ProbeDead(e.ctx, eng())
	if res.Probed != 1 || res.Revived != 0 {
		t.Fatalf("%+v", res)
	}
	res, _ = e.sc.ProbeDead(e.ctx, eng())
	if res.Probed != 0 {
		t.Fatal("the next probe is 30 days away")
	}
}

func TestSweep_RateLimitBreakerDefersWithoutStrikes(t *testing.T) {
	e := newEnv(t)
	var boards []Board
	for i := 0; i < 30; i++ {
		slug := fmt.Sprintf("limited-%02d", i)
		e.srv.set("/v1/boards/"+slug+"/jobs", route{status: 429})
		boards = append(boards, ghBoard(slug))
	}
	e.add(boards...)
	rec := e.run(ScanRequest{Portals: eng()})
	var deferred int
	for _, n := range rec.Deferred {
		deferred += n
	}
	if len(rec.Deferred) != 1 || deferred != 30 {
		t.Fatalf("deferred = %v", rec.Deferred)
	}
	for _, b := range boards {
		if row := e.registry(b); row.Strikes != 0 || row.Status != StatusActive {
			t.Fatalf("%s struck: %+v", b.Key(), row)
		}
	}
	if rec.Status != RunCompleted || rec.Counts.BoardErrors != 0 {
		t.Fatalf("rec = %+v", rec)
	}
}

// countingTransport fails every request at the network level.
type countingTransport struct {
	mu    sync.Mutex
	paths map[string]bool
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.paths[r.URL.Path] = true
	c.mu.Unlock()
	return nil, errors.New("dial tcp: connection refused")
}

func TestSweep_HostOfflineAbortsEarlyNoStrikes(t *testing.T) {
	e := newEnv(t)
	tr := &countingTransport{paths: map[string]bool{}}
	e.sc.Client.HTTP = &http.Client{Transport: tr}
	var boards []Board
	for i := 0; i < 200; i++ {
		boards = append(boards, ghBoard(fmt.Sprintf("offline-%03d", i)))
	}
	e.add(boards...)
	rec := e.run(ScanRequest{Portals: eng()})
	if rec.Status != RunFailed || rec.Reason != "host-offline" {
		t.Fatalf("status %s reason %s", rec.Status, rec.Reason)
	}
	if n := len(tr.paths); n >= 100 || n < offlineAfter {
		t.Fatalf("%d of 200 boards were attempted, want fewer than half", n)
	}
	for _, b := range boards {
		if row := e.registry(b); row.Strikes != 0 || row.Status != StatusActive {
			t.Fatalf("%s changed: %+v", b.Key(), row)
		}
	}
	if len(e.inbox(InboxQuery{})) != 0 {
		t.Fatal("an offline run wrote inbox rows")
	}
}

func TestSweep_OfflineDoesNotTripAfterASuccess(t *testing.T) {
	e := newEnv(t)
	e.stableBoard("aa-live")
	e.add(ghBoard("aa-live"))
	for i := 0; i < 30; i++ {
		e.srv.set(fmt.Sprintf("/v1/boards/zz-%02d/jobs", i), route{status: 404})
		e.add(ghBoard(fmt.Sprintf("zz-%02d", i)))
	}
	rec := e.run(ScanRequest{Portals: eng()})
	if rec.Status != RunCompleted {
		t.Fatalf("status %s %s", rec.Status, rec.Reason)
	}
	if e.registry(ghBoard("zz-00")).Strikes != 1 {
		t.Fatal("failures after a success must strike")
	}
}

func TestSweep_BudgetStopsAndLeavesBoardsDue(t *testing.T) {
	e := newEnv(t)
	const total = 40
	for i := 0; i < total; i++ {
		slug := fmt.Sprintf("b-%02d", i)
		e.stableBoard(slug)
		e.add(ghBoard(slug))
	}
	// The pause between two requests lasts until the budget ends the run.
	e.sc.Client.Sleep = func(ctx context.Context, d time.Duration) error {
		<-ctx.Done()
		return ctx.Err()
	}
	rec := e.run(ScanRequest{Portals: eng(), Budget: 300 * time.Millisecond})
	if rec.Status != RunCompleted || rec.Reason != "budget-reached" || rec.Boards >= total {
		t.Fatalf("rec = %s %s boards=%d", rec.Status, rec.Reason, rec.Boards)
	}
	due, _ := e.rp.RegistryActive(e.ctx)
	if len(due) != total || !due[0].LastScanned.IsZero() {
		t.Fatalf("boards not reached must stay due: %d active, first %+v", len(due), due[0])
	}
}

func TestSweep_OldestScannedFirst(t *testing.T) {
	e := newEnv(t)
	e.add(ghBoard("old-co"), ghBoard("new-co"), ghBoard("never-co"))
	if err := e.rp.RegistryRecord(e.ctx, ghBoard("old-co"), VisitNotModified, 0, ""); err != nil {
		t.Fatal(err)
	}
	e.clk.advance(time.Hour)
	if err := e.rp.RegistryRecord(e.ctx, ghBoard("new-co"), VisitNotModified, 0, ""); err != nil {
		t.Fatal(err)
	}
	rows, _ := e.rp.RegistryActive(e.ctx)
	got := rows[0].Slug + " " + rows[1].Slug + " " + rows[2].Slug
	if got != "never-co old-co new-co" {
		t.Fatalf("order = %s", got)
	}
}

func TestSweep_RunBudgetCancelsInFlightBoardWork(t *testing.T) {
	for _, phase := range []string{"list", "detail"} {
		t.Run(phase, func(t *testing.T) {
			e := newEnv(t)
			e.add(ghBoard("budget-co"))
			fire := e.budgetOnCue()
			ended := make(chan bool, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				list := r.URL.Path == "/v1/boards/budget-co/jobs"
				if phase == "list" && list || phase == "detail" && !list {
					fire()
					select {
					case <-r.Context().Done():
						ended <- true
						return
					case <-time.After(3 * time.Second):
						ended <- false
					}
				}
				if list {
					_ = json.NewEncoder(w).Encode(ghJobs("budget-co", ghItem(1, "Software Engineer", "Austin, TX")))
				} else {
					_ = json.NewEncoder(w).Encode(obj{"id": 1, "content": "Build reliable services."})
				}
			}))
			defer srv.Close()
			e.sc.Endpoints[Greenhouse] = srv.URL
			zero := 0
			rec := e.run(ScanRequest{Portals: eng(), TopN: &zero})
			select {
			case cancelled := <-ended:
				if !cancelled {
					t.Error("scan budget did not cancel in-flight " + phase)
				}
			case <-time.After(time.Second):
				t.Fatal("owned request did not end")
			}
			if rec.Status != RunCompleted || rec.Reason != ReasonBudget || rec.Counts.Queued != 0 || rec.Retryable {
				t.Fatalf("budget result: %+v", rec)
			}
			if row := e.registry(ghBoard("budget-co")); !row.LastScanned.IsZero() || row.Strikes != 0 {
				t.Fatalf("unfinished board no longer due: %+v", row)
			}
			e.sc.Endpoints = e.srv.endpoints()
			e.stableBoard("budget-co")
			if retry := e.run(ScanRequest{Portals: eng()}); retry.Status != RunCompleted || retry.Counts.Inserted != 1 || retry.Counts.Queued != 1 {
				t.Fatalf("retry: %+v", retry)
			}
		})
	}
}

func TestScan_RunBudgetReachesEvaluationAdmission(t *testing.T) {
	e := newEnv(t)
	e.stableBoard("budget-co")
	e.add(ghBoard("budget-co"))
	cancelled := false
	// This test waits out a real one-second budget, the timer the product
	// starts; the other budget tests end theirs on cue.
	e.sc.Enqueuer = leadAdmissionFunc(func(ctx context.Context, _ EvalRequest) (string, error) {
		select {
		case <-ctx.Done():
			cancelled = true
			return "", ctx.Err()
		case <-time.After(3 * time.Second):
			return "late-job", nil
		}
	})
	rec := e.run(ScanRequest{Portals: eng(), Budget: time.Second})
	if !cancelled || rec.Status != RunCompleted || rec.Reason != ReasonBudget || rec.Counts.Queued != 0 {
		t.Fatalf("budget did not reach admission: cancelled=%t rec=%+v", cancelled, rec)
	}
	rows := e.inbox(InboxQuery{})
	if len(rows) != 1 || rows[0].Status != InboxPending || rows[0].JobID != "" {
		t.Fatalf("refused admission stranded work: %+v", rows)
	}
}

func TestScan_IndependentAdmissionFailureSurvivesBudget(t *testing.T) {
	e := newEnv(t)
	e.stableBoard("budget-co")
	e.add(ghBoard("budget-co"))
	fire := e.budgetOnCue()
	e.sc.Enqueuer = leadAdmissionFunc(func(ctx context.Context, _ EvalRequest) (string, error) {
		fire()
		<-ctx.Done()
		return "", errors.Join(ctx.Err(), errors.New("owned independent admission refusal"))
	})
	rec := e.run(ScanRequest{Portals: eng()})
	if rec.Status != RunFailed || !strings.Contains(rec.Error, "owned independent admission refusal") {
		t.Fatalf("independent failure masked by budget: %+v", rec)
	}
}

func TestScan_AcceptedAdmissionWithFailedLinkIsCounted(t *testing.T) {
	for _, cancelCaller := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelCaller), func(t *testing.T) {
			e := newEnv(t)
			e.stableBoard("budget-co")
			e.add(ghBoard("budget-co"))
			ctx, cancel := context.WithCancelCause(e.ctx)
			defer cancel(nil)
			e.sc.Enqueuer = leadAdmissionFunc(func(context.Context, EvalRequest) (string, error) {
				e.exec(`CREATE TRIGGER owned_job_link_refusal BEFORE UPDATE OF job_id ON inbox BEGIN SELECT RAISE(ABORT, 'owned job-link refusal'); END`)
				if cancelCaller {
					cancel(ErrCancelled)
				}
				return "accepted-job", nil
			})
			rec, err := e.sc.Run(ctx, ScanRequest{Source: "registry", Portals: eng()})
			if err != nil || rec.Status != RunFailed || rec.Counts.Queued != 1 || !strings.Contains(rec.Error, "owned job-link refusal") {
				t.Fatalf("accepted work lost: rec=%+v err=%v", rec, err)
			}
		})
	}
}

func TestScan_FinishFailureIsNotSuccess(t *testing.T) {
	e := newEnv(t)
	e.stableBoard("budget-co")
	e.add(ghBoard("budget-co"))
	e.exec(`CREATE TRIGGER owned_scan_finish_refusal BEFORE UPDATE OF status ON scan_runs WHEN NEW.status != 'running' BEGIN SELECT RAISE(ABORT, 'owned scan-finish refusal'); END`)
	zero := 0
	rec, err := e.sc.Run(e.ctx, ScanRequest{Source: "registry", Portals: eng(), TopN: &zero})
	if err == nil || rec.Status != RunFailed || !strings.Contains(err.Error(), "owned scan-finish refusal") {
		t.Fatalf("unrecorded scan reported success: rec=%+v err=%v", rec, err)
	}
	e.exec(`DROP TRIGGER owned_scan_finish_refusal`)
	if err := e.rp.ScanCloseOrphans(e.ctx); err != nil {
		t.Fatal(err)
	}
	if retry := e.run(ScanRequest{Portals: eng(), TopN: &zero}); retry.Status != RunCompleted || retry.Counts.Inserted != 0 {
		t.Fatalf("restart recovery: %+v", retry)
	}
}

func TestScan_CancellationDuringAdmissionKeepsCallerCause(t *testing.T) {
	for _, phase := range []string{"user", "shutdown", "deadline", "accepted-user", "accepted-budget"} {
		t.Run(phase, func(t *testing.T) {
			e := newEnv(t)
			e.stableBoard("budget-co")
			e.add(ghBoard("budget-co"))
			ctx, cancel := context.WithCancelCause(e.ctx)
			defer cancel(nil)
			budget := 3 * time.Second
			fire := func() {}
			wantStatus, wantReason, queued := RunInterrupted, "interrupted", 0
			switch phase {
			case "user", "accepted-user":
				wantStatus, wantReason = RunCancelled, "cancelled"
			case "deadline":
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, time.Second)
				defer stop()
			case "accepted-budget":
				fire = e.budgetOnCue()
				wantStatus, wantReason = RunCompleted, ReasonBudget
			}
			if strings.HasPrefix(phase, "accepted-") {
				queued = 1
			}
			e.sc.Enqueuer = leadAdmissionFunc(func(admit context.Context, _ EvalRequest) (string, error) {
				switch phase {
				case "user", "accepted-user":
					cancel(ErrCancelled)
				case "shutdown":
					cancel(nil)
				case "accepted-budget":
					fire()
				}
				<-admit.Done()
				if queued == 1 {
					return "accepted-job", nil
				}
				return "", admit.Err()
			})
			rec, err := e.sc.Run(ctx, ScanRequest{Source: "registry", Portals: eng(), Budget: budget})
			if err != nil || rec.Status != wantStatus || rec.Reason != wantReason || rec.Counts.Queued != queued {
				t.Fatalf("caller cause/admission: rec=%+v err=%v", rec, err)
			}
			rows := e.inbox(InboxQuery{})
			if len(rows) != 1 {
				t.Fatalf("inbox: %+v", rows)
			}
			if queued == 1 {
				if rows[0].JobID != "accepted-job" || rows[0].Status != InboxQueued {
					t.Fatalf("accepted job lost: %+v", rows)
				}
			} else if rows[0].JobID != "" || rows[0].Status != InboxPending {
				t.Fatalf("refused job stranded: %+v", rows)
			}
		})
	}
}

func TestSweep_LimitCapsRegistryInsertsAndStopsReading(t *testing.T) {
	e := newEnv(t)
	const total = 60
	for i := 0; i < total; i++ {
		slug := fmt.Sprintf("cap-%02d-inc", i)
		e.stableBoard(slug)
		e.add(ghBoard(slug))
	}
	rec := e.run(ScanRequest{Portals: eng(), Limit: 3})
	if rec.Status != RunCompleted || rec.Reason != ReasonLimit || rec.Counts.Inserted != 3 {
		t.Fatalf("rec = %s %s inserted=%d", rec.Status, rec.Reason, rec.Counts.Inserted)
	}
	if rec.Boards >= total {
		t.Fatalf("the sweep read all %d boards after the cap was reached", rec.Boards)
	}
	if got := len(e.inbox(InboxQuery{})); got != 3 {
		t.Fatalf("%d inbox rows, want 3", got)
	}
	for _, row := range e.inbox(InboxQuery{}) {
		if !strings.Contains(row.Company, " ") || strings.Contains(row.Company, "-") {
			t.Fatalf("company %q is not a display name", row.Company)
		}
	}
}

func TestSweep_LimitKeepsTheRowTheStoredScoreRanksFirst(t *testing.T) {
	e := newEnv(t)
	e.serveGH("cap-rank-inc", []obj{ghItem(1, "Frontend Engineer", "Austin, TX"), ghItem(2, "Backend Engineer", "Austin, TX")}, nil)
	e.add(ghBoard("cap-rank-inc"))
	req := ScanRequest{Portals: eng(), Limit: 1}
	req.Profile.Targets.Roles = append(req.Profile.Targets.Roles, roleOf("Backend Engineer"))
	rec := e.run(req)
	rows := e.inbox(InboxQuery{})
	if rec.Counts.Inserted != 1 || len(rows) != 1 || rows[0].Role != "Backend Engineer" {
		t.Fatalf("inserted=%d rows=%+v, want the target-role posting", rec.Counts.Inserted, rows)
	}
}

func TestScan_EmptyTitleFilterCapsARegistrySweep(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct {
		source string
		limit  int
		want   int
	}{{"registry", 0, openFilterCap}, {"registry", 7, 7}, {"tracked", 0, 0}} {
		r, err := e.sc.newRun(e.ctx, ScanRequest{Source: c.source, Portals: portals(nil, nil), Limit: c.limit})
		if err != nil || r.insertCap != c.want {
			t.Fatalf("%s limit %d: cap %d err %v, want %d", c.source, c.limit, r.insertCap, err, c.want)
		}
	}
	r, _ := e.sc.newRun(e.ctx, ScanRequest{Source: "registry", Portals: eng()})
	if r.insertCap != 0 {
		t.Fatalf("a filter with title terms must not cap: %d", r.insertCap)
	}
}
