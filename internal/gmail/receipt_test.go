package gmail

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"applysmith/internal/gmail/classify"
	"applysmith/internal/gmail/imaptest"
)

var press = time.Date(2027, 3, 10, 12, 0, 0, 0, time.UTC)

var rcptLookup = ReceiptLookup{JobURL: "https://boards.greenhouse.io/exampleco/jobs/123456", ATS: "greenhouse", Tenant: "exampleco", Company: "Example Corp", SubmittedAt: press}

func receiptMail(id string, at time.Time, company string) msgSpec {
	return msgSpec{ID: id, From: company + " <" + addr("no-reply", "greenhouse-mail.io") + ">", Subject: "Thank you for applying to " + company,
		Text: "We have received your application to " + company + ".", MS: at.UnixMilli()}
}

// receiptEnv answers a search only with the receipts the fake clock has
// reached.
func receiptEnv(t *testing.T, mails ...msgSpec) *lookupEnv {
	le := newLookupEnv(t)
	le.clk.set(press)
	le.imap.SearchWith(func(attr, q string) []string {
		if attr != `\All` {
			return nil
		}
		le.mu.Lock()
		defer le.mu.Unlock()
		le.qs = append(le.qs, q)
		var ids []string
		for _, m := range mails {
			if !time.UnixMilli(m.MS).After(le.clk.now()) {
				ids = append(ids, m.ID)
			}
		}
		return ids
	})
	for _, m := range mails {
		le.imap.Add(imaptest.AllMail, m)
	}
	return le
}

func (le *lookupEnv) fakeSleep() (func(context.Context, time.Duration) error, *[]time.Duration) {
	var waits []time.Duration
	return func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		waits = append(waits, d)
		le.clk.advance(d)
		return nil
	}, &waits
}

func TestGmail_Receipt_TenantReceiptFoundOnTheSecondPoll(t *testing.T) {
	le := receiptEnv(t, receiptMail("a1", press.Add(15*time.Second), "Example Corp"))
	sleep, waits := le.fakeSleep()
	r, err := le.lk.AwaitReceipt(le.ctx, rcptLookup, AwaitOptions{Sleep: sleep})
	if err != nil || r == nil || r.MessageID != "a1" || !r.ReceivedAt.Equal(press.Add(15*time.Second)) {
		t.Fatalf("receipt = %+v, %v", r, err)
	}
	if len(*waits) != 2 || (*waits)[0] != 5*time.Second || (*waits)[1] != 15*time.Second {
		t.Fatalf("waits = %v: found at the 20-second poll", *waits)
	}
	if len(le.qs) != 2 {
		t.Fatalf("inbox passes = %d", len(le.qs))
	}
}

func TestGmail_Receipt_PollSchedules(t *testing.T) {
	le := receiptEnv(t)
	sleep, waits := le.fakeSleep()
	if r, err := le.lk.AwaitReceipt(le.ctx, rcptLookup, AwaitOptions{Sleep: sleep}); r != nil || err != nil || len(*waits) != 5 {
		t.Fatalf("not found: %v %v waits=%v", r, err, *waits)
	}
	le2 := receiptEnv(t)
	sleep2, waits2 := le2.fakeSleep()
	if _, err := le2.lk.AwaitReceipt(le2.ctx, rcptLookup, AwaitOptions{Sleep: sleep2, PageConfirmed: true}); err != nil || len(*waits2) != 2 {
		t.Fatalf("confirmed waits = %v, %v", *waits2, err)
	}
	// The schedule counts from the press: a lane that spent 40 seconds skips the early polls.
	le3 := receiptEnv(t)
	le3.clk.t = press.Add(40 * time.Second)
	sleep3, waits3 := le3.fakeSleep()
	if _, err := le3.lk.AwaitReceipt(le3.ctx, rcptLookup, AwaitOptions{Sleep: sleep3}); err != nil {
		t.Fatal(err)
	}
	if len(*waits3) != 3 || (*waits3)[0] != 5*time.Second {
		t.Fatalf("late lane waits = %v", *waits3)
	}
	if len(le3.qs) != 5 {
		t.Fatalf("a late lane still polls at every remaining offset: %d passes", len(le3.qs))
	}
}

func TestGmail_Receipt_AnotherApplicantsReceipt(t *testing.T) {
	le := receiptEnv(t, receiptMail("b2", press.Add(10*time.Second), "Sift"))
	le.clk.t = press.Add(time.Minute)
	r, err := le.lk.FindReceipt(le.ctx, rcptLookup)
	if err != nil || r != nil {
		t.Fatalf("receipt = %+v, %v", r, err)
	}
}

func TestGmail_Receipt_WindowTwoMinBeforeThirtyAfter(t *testing.T) {
	old := receiptMail("c3", press.Add(-3*time.Minute), "Example Corp")
	inLead := receiptMail("d4", press.Add(-90*time.Second), "Example Corp")
	late := receiptMail("e5", press.Add(31*time.Minute), "Example Corp")
	le := receiptEnv(t, old, inLead, late)
	le.clk.t = press.Add(time.Hour)
	// The fake serves everything, so the client-side window re-check decides.
	r, err := le.lk.FindReceipt(le.ctx, rcptLookup)
	if err != nil || r == nil || r.MessageID != "d4" {
		t.Fatalf("receipt = %+v, %v", r, err)
	}
	if ReceiptLead != 2*time.Minute || ReceiptWindow != 30*time.Minute {
		t.Fatal("window constants")
	}
}

func TestGmail_Receipt_QueryBoundsBothEnds(t *testing.T) {
	le := receiptEnv(t)
	le.clk.t = press.Add(time.Hour)
	if _, err := le.lk.FindReceipt(le.ctx, rcptLookup); err != nil {
		t.Fatal(err)
	}
	q := le.qs[0]
	after := fmt.Sprintf("after:%d", press.Add(-2*time.Minute).Unix())
	before := fmt.Sprintf("before:%d", press.Add(30*time.Minute).Unix())
	if !strings.Contains(q, after) || !strings.Contains(q, before) {
		t.Fatalf("query = %q, want %s and %s", q, after, before)
	}
	if receiptMaxMessages != 20 {
		t.Fatal("at most 20 messages per pass")
	}
}

func TestGmail_Receipt_BestScoreThenNewest(t *testing.T) {
	weak := msgSpec{ID: "f6", From: "Example <" + addr("jobs", "example.com") + ">", Subject: "Application received", Text: "Thanks.", MS: press.Add(10 * time.Second).UnixMilli()}
	strong := receiptMail("a7", press.Add(20*time.Second), "Example Corp")
	strong.From = "Example <" + addr("no-reply", "greenhouse-mail.io") + ">"
	le := receiptEnv(t, weak, strong)
	le.clk.t = press.Add(time.Minute)
	r, err := le.lk.FindReceipt(le.ctx, rcptLookup)
	if err != nil || r.MessageID != "a7" {
		t.Fatalf("best = %+v, %v", r, err)
	}
	// Equal scores go to the newest.
	a := receiptMail("a", press.Add(10*time.Second), "Example Corp")
	b := receiptMail("b", press.Add(40*time.Second), "Example Corp")
	le2 := receiptEnv(t, a, b)
	le2.clk.t = press.Add(time.Minute)
	if r, _ = le2.lk.FindReceipt(le2.ctx, rcptLookup); r == nil || r.MessageID != "b" {
		t.Fatalf("tie = %+v", r)
	}
}

func TestGmail_Receipt_AuthFailureNotNotFound(t *testing.T) {
	le := receiptEnv(t)
	le.refuseLogins("Invalid credentials (Failure)")
	r, err := le.lk.FindReceipt(le.ctx, rcptLookup)
	var lerr *LookupError
	if r != nil || !errors.As(err, &lerr) || lerr.Reason != ReasonNotSignedIn {
		t.Fatalf("receipt=%+v err=%v", r, err)
	}
	sleep, _ := le.fakeSleep()
	if _, err := le.lk.AwaitReceipt(le.ctx, rcptLookup, AwaitOptions{Sleep: sleep}); !errors.As(err, &lerr) {
		t.Fatalf("await err = %v", err)
	}
}

func TestGmail_Receipt_AbortMakesNoCall(t *testing.T) {
	le := receiptEnv(t)
	ctx, cancel := context.WithCancel(le.ctx)
	cancel()
	if _, err := le.lk.AwaitReceipt(ctx, rcptLookup, AwaitOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if len(le.qs) != 0 {
		t.Fatal("an aborted wait called the inbox")
	}
	// Abort during the scheduled sleep.
	ctx2, cancel2 := context.WithCancel(le.ctx)
	sleep := func(ctx context.Context, d time.Duration) error { cancel2(); return ctx.Err() }
	if _, err := le.lk.AwaitReceipt(ctx2, rcptLookup, AwaitOptions{Sleep: sleep}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if len(le.qs) != 0 {
		t.Fatal("an abort during the sleep called the inbox")
	}
}

func TestGmail_Receipt_LookupInputs(t *testing.T) {
	le := receiptEnv(t)
	if _, err := le.lk.FindReceipt(le.ctx, ReceiptLookup{JobURL: "x"}); err == nil {
		t.Fatal("a lookup without the submit instant was accepted")
	}
}

func TestGmail_Receipt_LateReceiptAttachedBySync(t *testing.T) {
	s := newSyncEnv(t)
	s.sy.Classify = NewClassifyFunc(classify.Defaults(), nil)
	s.sy.Version = classify.Version
	s.imap.Add(imaptest.AllMail, receiptMail("e5", press.Add(20*time.Minute), "Example Corp"))
	s.search("Q1", "e5")
	s.run(TriggerScheduled)
	sigs, err := s.repo.Signals(s.ctx)
	if err != nil || len(sigs) != 1 || sigs[0].Type != classify.TypeApplied || sigs[0].MessageID != "e5" {
		t.Fatalf("signals = %+v, %v", sigs, err)
	}
}
