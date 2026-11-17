package discovery

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// syncPool runs the job to its end inside Enqueue, as a fast pool worker can
// before Enqueue returns to its caller. fail fails the job at the cost charge
// says; ids are the rows it was asked to evaluate.
type syncPool struct {
	rp     *Repo
	fail   bool
	charge Charge
	errs   []error
	ids    []int64
}

func (p *syncPool) Enqueue(ctx context.Context, req EvalRequest) (string, error) {
	p.ids = append(p.ids, req.InboxID)
	switch {
	case p.fail:
		p.errs = append(p.errs, p.rp.InboxEvalFailed(ctx, req.InboxID, "evaluation failed", p.charge))
	default:
		p.errs = append(p.errs, p.rp.InboxEvaluated(ctx, req.InboxID))
	}
	return "job-sync", nil
}

func TestInbox_Promote_AFastJobEndsAtItsTerminalState(t *testing.T) {
	for _, c := range []struct {
		name string
		fail bool
		want string
	}{{"evaluated", false, InboxEvaluated}, {"failed returns to pending", true, InboxPending}} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.serveGH("example-co", []obj{ghItem(1, "Software Engineer", "Austin, TX")}, nil)
			e.add(ghBoard("example-co"))
			zero := 0
			e.run(ScanRequest{Portals: eng(), TopN: &zero})
			rows := e.inbox(InboxQuery{})
			pool := &syncPool{rp: e.rp, fail: c.fail}
			if _, _, err := e.rp.Promote(e.ctx, pool, nil, rows[0].ID); err != nil {
				t.Fatal(err)
			}
			if len(pool.errs) != 1 || pool.errs[0] != nil {
				t.Fatalf("the job's own transition failed: %v", pool.errs)
			}
			row, _ := e.rp.InboxGet(e.ctx, rows[0].ID)
			if row.Status != c.want || row.JobID != "job-sync" {
				t.Fatalf("row = %+v, want status %s with the job recorded", row, c.want)
			}
		})
	}
}

type failingPool struct{}

func (failingPool) Enqueue(context.Context, EvalRequest) (string, error) {
	return "", errors.New("pool is full")
}

func TestInbox_Promote_AFailedEnqueueRevertsTheRow(t *testing.T) {
	e := newEnv(t)
	e.serveGH("example-co", []obj{ghItem(1, "Software Engineer", "Austin, TX")}, nil)
	e.add(ghBoard("example-co"))
	zero := 0
	e.run(ScanRequest{Portals: eng(), TopN: &zero})
	rows := e.inbox(InboxQuery{})
	if _, _, err := e.rp.Promote(e.ctx, failingPool{}, nil, rows[0].ID); err == nil || !strings.Contains(err.Error(), "pool is full") {
		t.Fatalf("err = %v", err)
	}
	row, _ := e.rp.InboxGet(e.ctx, rows[0].ID)
	if row.Status != InboxPending || row.Attempts != 0 || !strings.Contains(row.Notes, "enqueue failed") {
		t.Fatalf("row = %+v", row)
	}
	// The row can be promoted again once the pool accepts work.
	if q, _, err := e.rp.Promote(e.ctx, e.enq, nil, rows[0].ID); err != nil || len(q) != 1 {
		t.Fatalf("retry: %v %v", q, err)
	}
}

func TestInbox_EnqueueAccountsAfterCallerCancellation(t *testing.T) {
	for _, c := range []struct {
		name     string
		accepted bool
		terminal string
		want     string
	}{
		{name: "refused", want: InboxPending},
		{name: "accepted", accepted: true, want: InboxQueued},
		{name: "fast-success", accepted: true, terminal: InboxEvaluated, want: InboxEvaluated},
		{name: "fast-failure", accepted: true, terminal: InboxPending, want: InboxPending},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			add, err := e.rp.InboxAddURL(e.ctx, "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000001")
			if err != nil || add.Row == nil {
				t.Fatalf("inbox: %+v %v", add, err)
			}
			ctx, cancel := context.WithCancel(e.ctx)
			defer cancel()
			id, _, err := e.rp.QueueForEvaluation(ctx, *add.Row, nil, func() (string, error) {
				if c.terminal == InboxEvaluated {
					if err := e.rp.InboxEvaluated(e.ctx, add.Row.ID); err != nil {
						t.Fatal(err)
					}
				}
				if c.terminal == InboxPending {
					if err := e.rp.InboxEvalFailed(e.ctx, add.Row.ID, "provider failed", ChargeAttempt); err != nil {
						t.Fatal(err)
					}
				}
				cancel()
				if !c.accepted {
					return "", context.Canceled
				}
				return "accepted-job", nil
			})
			if c.accepted && (err != nil || id != "accepted-job") {
				t.Errorf("accepted job id=%s err=%v", id, err)
			}
			if !c.accepted && !errors.Is(err, context.Canceled) {
				t.Errorf("refused enqueue: %v", err)
			}
			row, err := e.rp.InboxGet(e.ctx, add.Row.ID)
			if err != nil {
				t.Fatal(err)
			}
			attempts, jobID := 0, ""
			if c.accepted {
				attempts, jobID = 1, "accepted-job"
			}
			if row.Status != c.want || row.Attempts != attempts || row.JobID != jobID {
				t.Errorf("row status=%s attempts=%d job=%s; want %s/%d/%s", row.Status, row.Attempts, row.JobID, c.want, attempts, jobID)
			}
			if !c.accepted && !strings.Contains(row.Notes, "enqueue failed") {
				t.Errorf("rollback omitted failure note: %s", row.Notes)
			}
		})
	}
}

func TestInbox_CancelledBeforeEnqueueChangesNothing(t *testing.T) {
	e := newEnv(t)
	add, err := e.rp.InboxAddURL(e.ctx, "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000002")
	if err != nil || add.Row == nil {
		t.Fatalf("inbox: %+v %v", add, err)
	}
	ctx, cancel := context.WithCancel(e.ctx)
	cancel()
	called := false
	_, _, err = e.rp.QueueForEvaluation(ctx, *add.Row, nil, func() (string, error) { called = true; return "job", nil })
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("enqueue called=%v err=%v", called, err)
	}
	row, err := e.rp.InboxGet(e.ctx, add.Row.ID)
	if err != nil || row.Status != InboxPending || row.Attempts != 0 || row.JobID != "" {
		t.Fatalf("row=%+v err=%v", row, err)
	}
}

func TestInbox_AnOlderAdmissionCannotOverwriteANewerRetry(t *testing.T) {
	for _, accepted := range []bool{true, false} {
		t.Run(map[bool]string{true: "accepted", false: "refused"}[accepted], func(t *testing.T) {
			e := newEnv(t)
			add, err := e.rp.InboxAddURL(e.ctx, "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000003")
			if err != nil || add.Row == nil {
				t.Fatalf("inbox: %+v %v", add, err)
			}
			ctx, cancel := context.WithCancel(e.ctx)
			defer cancel()
			id, _, err := e.rp.QueueForEvaluation(ctx, *add.Row, nil, func() (string, error) {
				// A queued row returns to pending before admission accounting;
				// its next accepted attempt owns the new job id and state.
				if err := e.rp.InboxEvalFailed(e.ctx, add.Row.ID, "first provider failed", ChargeAttempt); err != nil {
					t.Fatal(err)
				}
				pending, err := e.rp.InboxGet(e.ctx, add.Row.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := e.rp.QueueForEvaluation(e.ctx, pending, nil, func() (string, error) { return "newer-job", nil }); err != nil {
					t.Fatal(err)
				}
				cancel()
				if !accepted {
					return "", context.Canceled
				}
				return "older-job", nil
			})
			if accepted && (err != nil || id != "older-job") {
				t.Fatalf("first admission: id=%s err=%v", id, err)
			}
			if !accepted && !errors.Is(err, context.Canceled) {
				t.Fatalf("refused admission: %v", err)
			}
			row, err := e.rp.InboxGet(e.ctx, add.Row.ID)
			if err != nil || row.Status != InboxQueued || row.Attempts != 2 || row.JobID != "newer-job" {
				t.Fatalf("newer admission overwritten: status=%s attempts=%d job=%s err=%v", row.Status, row.Attempts, row.JobID, err)
			}
		})
	}
}

func TestInbox_AdmissionAccountingPreservesUserTerminalState(t *testing.T) {
	for _, state := range []string{InboxDismissed, InboxExpired} {
		for _, accepted := range []bool{true, false} {
			t.Run(state+map[bool]string{true: "/accepted", false: "/refused"}[accepted], func(t *testing.T) {
				e := newEnv(t)
				add, err := e.rp.InboxAddURL(e.ctx, "https://jobs.lever.co/example-co/5d1c0a9e-0000-4000-8000-000000000004")
				if err != nil || add.Row == nil {
					t.Fatalf("inbox: %+v %v", add, err)
				}
				ctx, cancel := context.WithCancel(e.ctx)
				defer cancel()
				_, _, err = e.rp.QueueForEvaluation(ctx, *add.Row, nil, func() (string, error) {
					var err error
					if state == InboxDismissed {
						err = e.rp.InboxDismiss(e.ctx, add.Row.ID)
					} else {
						err = e.rp.InboxExpire(e.ctx, add.Row.ID)
					}
					if err != nil {
						t.Fatal(err)
					}
					cancel()
					if !accepted {
						return "", context.Canceled
					}
					return "accepted-job", nil
				})
				if accepted && err != nil || !accepted && !errors.Is(err, context.Canceled) {
					t.Fatalf("admission: %v", err)
				}
				row, err := e.rp.InboxGet(e.ctx, add.Row.ID)
				if err != nil || row.Status != state {
					t.Fatalf("user state changed: row=%+v err=%v", row, err)
				}
			})
		}
	}
}
