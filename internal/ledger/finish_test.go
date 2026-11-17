package ledger

import (
	"context"
	"errors"
	"testing"

	"applysmith/internal/gates"
)

func TestFinish_LandedRefusedAndUndecidedPresses(t *testing.T) {
	cases := []struct {
		name string
		v    Verdict
		want gates.LedgerState
	}{
		{"landed and confirmed", Verdict{Landed: true, PressDispatched: true, Confirmed: true, ConfirmSource: "page"}, gates.StateSubmitted},
		{"landed unconfirmed", Verdict{Landed: true, PressDispatched: true}, gates.StateSubmitted},
		{"refused after the press", Verdict{PressDispatched: true, RefusalProven: true}, gates.StateFailed},
		{"never pressed", Verdict{}, gates.StateFailed},
		{"pressed with no verdict", Verdict{PressDispatched: true}, gates.StateMayHaveSubmitted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l, _, db := clocked(t)
			r := req(postURL)
			r.Source, r.RunID = SourceApplyChain, "run-1"
			cl, err := l.Claim(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			if len(cl.Decisions) != 2 {
				t.Fatalf("decisions %v", cl.Decisions)
			}
			got, err := l.Finish(context.Background(), cl.ID, c.v)
			if err != nil || got != c.want {
				t.Fatalf("state %q err %v", got, err)
			}
			if n := count(t, db, `SELECT count(*) FROM submissions WHERE state = ? AND source = 'apply-chain' AND run_id = 'run-1'`, string(c.want)); n != 1 {
				t.Fatalf("rows in state %s: %d", c.want, n)
			}
		})
	}
}

func TestFinish_RepeatMayOnlyRaiseConfirmedAndUnknownClaimIsRefused(t *testing.T) {
	l, _, db := clocked(t)
	cl, err := l.Claim(context.Background(), req(postURL))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Finish(context.Background(), cl.ID, Verdict{Landed: true, PressDispatched: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Finish(context.Background(), cl.ID, Verdict{Landed: true, PressDispatched: true, Confirmed: true, ConfirmSource: "receipt", ReceiptID: "m1"}); err != nil {
		t.Fatal(err)
	}
	// A later report of failure cannot take the submission back.
	if st, err := l.Finish(context.Background(), cl.ID, Verdict{}); err != nil || st != gates.StateSubmitted {
		t.Fatalf("state %q err %v", st, err)
	}
	if n := count(t, db, `SELECT count(*) FROM submissions WHERE state = 'submitted' AND confirmed = 1 AND confirm_source = 'receipt' AND receipt_message_id = 'm1'`); n != 1 {
		t.Fatalf("row not confirmed")
	}
	if n := count(t, db, `SELECT count(*) FROM submissions`); n != 1 {
		t.Fatalf("three reports of one press left %d rows", n)
	}
	if _, err := l.Finish(context.Background(), "claim_nope", Verdict{}); !errors.Is(err, ErrUnknownClaim) {
		t.Fatalf("err %v", err)
	}
}

func TestGuard_ReadsEvidenceWithoutClaimingAndResubmitLiftsUnconfirmed(t *testing.T) {
	l, _, db := clocked(t)
	ctx := context.Background()
	if _, err := l.Guard(ctx, req(postURL)); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT count(*) FROM submissions`); n != 0 {
		t.Fatalf("guard wrote %d rows", n)
	}
	cl, _ := l.Claim(ctx, req(postURL))
	if _, err := l.Finish(ctx, cl.ID, Verdict{Landed: true, PressDispatched: true}); err != nil {
		t.Fatal(err)
	}
	d, err := l.Guard(ctx, req(postURL))
	var b *BlockedError
	if !errors.As(err, &b) || d.Last().Code != gates.CodeUnconfirmedSubmission {
		t.Fatalf("decision %+v err %v", d, err)
	}
	r := req(postURL)
	r.Resubmit = true
	if d, err := l.Guard(ctx, r); err != nil || d.Last().Code != gates.CodeResubmitLifted {
		t.Fatalf("decision %+v err %v", d, err)
	}
	if _, ok := DecisionOf(err); !ok {
		t.Fatal("DecisionOf finds nothing")
	}
}

func TestWaiting_UnconfirmedOrPageConfirmedChainPressesUntilSettled(t *testing.T) {
	ctx := context.Background()
	l, _, db := clocked(t)
	press := func(url string, v Verdict) Waiting {
		t.Helper()
		r := req(url)
		r.Source, r.RunID, r.Role = SourceApplyChain, "run-"+url[len(url)-1:], "Engineer "+url[len(url)-1:]
		cl, err := l.Claim(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := l.Finish(ctx, cl.ID, v); err != nil {
			t.Fatal(err)
		}
		w, ok, err := l.WaitingFor(ctx, []string{claimKey(url)})
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return Waiting{}
		}
		return w
	}
	pressed := Verdict{PressDispatched: true}
	unconfirmed := press("https://jobs.example.com/acme/roles/1", pressed)
	page := press("https://jobs.example.com/acme/roles/2", Verdict{Landed: true, PressDispatched: true, Confirmed: true, ConfirmSource: "page"})
	receipt := press("https://jobs.example.com/acme/roles/3", Verdict{Landed: true, PressDispatched: true, Confirmed: true, ConfirmSource: "receipt", ReceiptID: "m0"})
	if unconfirmed.ID == 0 || unconfirmed.Confirmed || page.ID == 0 || !page.Confirmed || receipt.ID != 0 {
		t.Fatalf("waiting: %+v %+v %+v", unconfirmed, page, receipt)
	}
	if obs, err := l.RecordObserved(ctx, Observation{EntryID: "e-4", URL: "https://jobs.example.com/acme/roles/4", Host: "jobs.example.com", Company: "Acme Labs", Role: "Engineer 4", ObservedAt: t0}); err != nil || obs.ID == 0 {
		t.Fatalf("observed: %+v %v", obs, err)
	}
	if _, ok, _ := l.WaitingFor(ctx, []string{claimKey("https://jobs.example.com/acme/roles/4")}); ok {
		t.Fatal("the person's own submission is not the chain's to settle")
	}

	if err := l.SettleWaiting(ctx, unconfirmed.ID, ConfirmUser, ""); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT count(*) FROM submissions WHERE id = ? AND state = 'submitted' AND confirmed = 1 AND confirm_source = 'user'`, unconfirmed.ID); n != 1 {
		t.Fatal("the user's statement did not settle the may-have-submitted press")
	}
	if err := l.SettleWaiting(ctx, page.ID, ConfirmReceipt, "m2"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT count(*) FROM submissions WHERE id = ? AND confirm_source = 'page' AND receipt_message_id = 'm2'`, page.ID); n != 1 {
		t.Fatal("a receipt keeps the page's confirmation and records itself")
	}
	if left, err := l.Waiting(ctx); err != nil || len(left) != 0 {
		t.Fatalf("still waiting: %+v %v", left, err)
	}
}
