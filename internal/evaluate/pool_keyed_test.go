// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"applysmith/internal/store"
)

func keyedClaim(key string) Claim { return Claim{Principal: "dashboard", Key: key} }

func TestPool_KeyedSubmitReplaysTheOriginalRun(t *testing.T) {
	e := newTenv(t)
	p := startPool(t, e, PoolOptions{})
	first, err := p.SubmitKeyed(context.Background(), keyedClaim("key-0000-0001"), req("keyed1"))
	if err != nil || first.Replayed || first.Attached {
		t.Fatalf("first = %+v, %v", first, err)
	}
	if res, err := p.Wait(context.Background(), first.RunID); err != nil || res.Outcome != OutcomeReported {
		t.Fatalf("result = %+v, %v", res, err)
	}
	again, err := p.SubmitKeyed(context.Background(), keyedClaim("key-0000-0001"), req("keyed1"))
	if err != nil || !again.Replayed || again.RunID != first.RunID {
		t.Fatalf("retry = %+v, %v", again, err)
	}
	if n := e.count("runs"); n != 1 {
		t.Fatalf("%d runs exist, want 1", n)
	}
	if n := e.count("report_index"); n != 1 {
		t.Fatalf("%d reports exist, want 1", n)
	}
}

func TestPool_KeyedSubmitWithChangedInputConflicts(t *testing.T) {
	e := newTenv(t)
	p := startPool(t, e, PoolOptions{})
	if _, err := p.SubmitKeyed(context.Background(), keyedClaim("key-0000-0001"), req("keyed2")); err != nil {
		t.Fatal(err)
	}
	_, err := p.SubmitKeyed(context.Background(), keyedClaim("key-0000-0001"), req("keyed3"))
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("changed input: %v", err)
	}
	if n := e.count("runs"); n != 1 {
		t.Fatalf("%d runs exist, want 1", n)
	}
}

func TestPool_KeyedSubmitIsScopedToThePrincipal(t *testing.T) {
	e := newTenv(t)
	p := startPool(t, e, PoolOptions{})
	a, err := p.SubmitKeyed(context.Background(), Claim{Principal: "dashboard", Key: "key-0000-0001"}, req("keyed4", func(r *Request) { r.Force = true }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Wait(context.Background(), a.RunID); err != nil {
		t.Fatal(err)
	}
	b, err := p.SubmitKeyed(context.Background(), Claim{Principal: "local-token", Key: "key-0000-0001"}, req("keyed4", func(r *Request) { r.Force = true }))
	if err != nil || b.Replayed || b.RunID == a.RunID {
		t.Fatalf("another principal shares the key: %+v, %v", b, err)
	}
}

func TestPool_KeyedSubmitJoinsAnActiveJobAndRemembersTheKey(t *testing.T) {
	e := newTenv(t)
	stub := newStub()
	e.model = stub
	e.rebuild()
	p := startPool(t, e, PoolOptions{})
	first := submit(t, p, "keyed5")
	waitFor(t, "the job to start", func() bool { return p.Stats().Active == 1 })
	joined, err := p.SubmitKeyed(context.Background(), keyedClaim("key-0000-0001"), req("keyed5"))
	if err != nil || !joined.Attached || joined.RunID != first.RunID {
		t.Fatalf("joined = %+v, %v", joined, err)
	}
	again, err := p.SubmitKeyed(context.Background(), keyedClaim("key-0000-0001"), req("keyed5"))
	if err != nil || !again.Replayed || again.RunID != first.RunID {
		t.Fatalf("retry of a joined request = %+v, %v", again, err)
	}
	stub.release <- struct{}{}
	if _, err := p.Wait(context.Background(), first.RunID); err != nil {
		t.Fatal(err)
	}
	if n := e.count("runs"); n != 1 {
		t.Fatalf("%d runs exist, want 1", n)
	}
}

func TestPool_KeyedSubmitHonoursTheQueueBound(t *testing.T) {
	e := newTenv(t)
	stub := newStub()
	e.model = stub
	e.rebuild()
	p := startPool(t, e, PoolOptions{QueueMax: 1, Concurrency: func(context.Context) int { return 1 }})
	submit(t, p, "keyed6")
	waitFor(t, "the first job to start", func() bool { return p.Stats().Active == 1 })
	submit(t, p, "keyed7")
	_, err := p.SubmitKeyed(context.Background(), keyedClaim("key-0000-0001"), req("keyed8"))
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("full queue: %v", err)
	}
	if n := e.count("runs"); n != 2 {
		t.Fatalf("%d runs exist, want 2; a refused request must create none", n)
	}
	stub.release <- struct{}{}
	stub.release <- struct{}{}
}

func TestPool_KeyedSubmitThatFailsLeavesNoClaimSoTheRetryExecutes(t *testing.T) {
	e := newTenv(t)
	p := startPool(t, e, PoolOptions{})
	ctx := context.Background()
	trigger := func(sqlText string) {
		t.Helper()
		if err := e.st.Write(ctx, func(tx *sql.Tx) error { _, err := tx.Exec(sqlText); return err }); err != nil {
			t.Fatal(err)
		}
	}
	trigger(`CREATE TRIGGER fail_job BEFORE INSERT ON evaluation_jobs BEGIN SELECT RAISE(ABORT, 'injected'); END`)
	if _, err := p.SubmitKeyed(ctx, keyedClaim("key-0000-0009"), req("keyed9")); err == nil {
		t.Fatal("the injected failure did not surface")
	}
	if n := e.count("runs"); n != 0 {
		t.Fatalf("%d runs survive a failed submit", n)
	}
	trigger(`DROP TRIGGER fail_job`)
	h, err := p.SubmitKeyed(ctx, keyedClaim("key-0000-0009"), req("keyed9"))
	if err != nil || h.Replayed {
		t.Fatalf("retry = %+v, %v", h, err)
	}
	if res, err := p.Wait(ctx, h.RunID); err != nil || res.Outcome != OutcomeReported {
		t.Fatalf("the retried run must execute: %+v, %v", res, err)
	}
}

func TestPool_KeyedSubmitWithACancelledRequestStillExecutesOnRetry(t *testing.T) {
	e := newTenv(t)
	p := startPool(t, e, PoolOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h, err := p.SubmitKeyed(ctx, keyedClaim("key-0000-0010"), req("keyed10"))
	if err != nil {
		// The claim never committed; a retry starts clean.
		if n := e.count("runs"); n != 0 {
			t.Fatalf("%d runs survive a refused submit", n)
		}
		h, err = p.SubmitKeyed(context.Background(), keyedClaim("key-0000-0010"), req("keyed10"))
		if err != nil {
			t.Fatal(err)
		}
	}
	if res, err := p.Wait(context.Background(), h.RunID); err != nil || res.Outcome != OutcomeReported {
		t.Fatalf("result %+v, %v", res, err)
	}
	again, err := p.SubmitKeyed(context.Background(), keyedClaim("key-0000-0010"), req("keyed10"))
	if err != nil || !again.Replayed || again.RunID != h.RunID {
		t.Fatalf("retry = %+v, %v", again, err)
	}
}
