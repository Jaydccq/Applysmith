package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"applysmith/internal/gmail/classify"
	"applysmith/internal/gmail/link"
	"applysmith/internal/interview"
	"applysmith/internal/ledger"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
)

func TestOutcome_EventToStatusMap(t *testing.T) {
	for typ, want := range map[string]tracker.Status{
		classify.TypeApplied: tracker.Applied, classify.TypeReplyNeeded: tracker.Responded, classify.TypeActionNeeded: tracker.Responded,
		classify.TypeAssessment: tracker.Interview, classify.TypeInterview: tracker.Interview, classify.TypeRejection: tracker.Rejected,
		classify.TypeOffer: tracker.Offer, classify.TypeReminder: "", classify.TypeReview: "", classify.TypeLowPriority: "",
	} {
		if got := targetOf(typ); got != want {
			t.Errorf("%s maps to %q, want %q", typ, got, want)
		}
	}
}

func TestOutcome_ReceiptPromotesEvaluatedAndEventNamesMessageAndActor(t *testing.T) {
	p := newPipeEnv(t)
	id := p.add("Example Corp", "Backend Engineer", tracker.Evaluated)
	p.seed(sig{id: "m1", typ: classify.TypeApplied, role: "Backend Engineer"})
	p.link()
	if got := p.status(id); got != tracker.Applied {
		t.Fatalf("status = %s", got)
	}
	d, _ := p.tr.Get(p.ctx, id)
	var found bool
	for _, ev := range d.Events {
		if ev.Kind != tracker.EventStatusChanged || ev.Actor != tracker.ActorGmail {
			continue
		}
		var detail map[string]any
		if err := json.Unmarshal(ev.Detail, &detail); err != nil {
			t.Fatal(err)
		}
		found = found || (detail["source_message_id"] == "m1" && detail["signal_type"] == classify.TypeApplied)
	}
	if !found {
		t.Fatalf("no gmail status event naming the message: %+v", d.Events)
	}
}

func TestOutcome_ReceiptSettlesTheApplyChainsWaitingSubmission(t *testing.T) {
	type press struct {
		verdict ledger.Verdict
		applied bool // the chain already marked the row applied
	}
	cases := []struct {
		name       string
		press      press
		receivedAt time.Duration // after the press
		status     tracker.Status
		by         tracker.AppliedBy
		settled    bool
	}{
		{"an unconfirmed press keeps the agent mark", press{verdict: ledger.Verdict{PressDispatched: true}}, time.Hour, tracker.Applied, tracker.ByAgent, true},
		{"a page-confirmed press stops waiting", press{verdict: ledger.Verdict{Landed: true, PressDispatched: true, Confirmed: true, ConfirmSource: "page"}, applied: true}, time.Hour, tracker.Applied, tracker.ByAgent, true},
		{"mail from before the press is another application's", press{verdict: ledger.Verdict{PressDispatched: true}}, -day, tracker.Applied, tracker.ByUser, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newPipeEnv(t)
			const posting = "https://jobs.example.org/example-corp/backend-1"
			e, err := p.tr.Add(p.ctx, tracker.AddRow{Company: "Example Corp", Role: "Backend Engineer", URL: posting})
			if err != nil {
				t.Fatal(err)
			}
			led := ledger.New(p.st, p.clk.now)
			cl, err := led.Claim(p.ctx, ledger.ClaimRequest{URL: posting, Host: "jobs.example.org", Company: "Example Corp", Role: "Backend Engineer",
				Source: ledger.SourceApplyChain, RunID: "run-1"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := led.Finish(p.ctx, cl.ID, c.press.verdict); err != nil {
				t.Fatal(err)
			}
			if c.press.applied {
				if _, err := p.tr.SetStatus(p.ctx, tracker.StatusChange{ID: e.ID, To: tracker.Applied, Actor: tracker.ActorApplyChain, AppliedBy: tracker.ByAgent}); err != nil {
					t.Fatal(err)
				}
			}
			p.seed(sig{id: "rc", typ: classify.TypeApplied, role: "Backend Engineer", at: p.clk.now().Add(c.receivedAt)})
			p.link()
			d, _ := p.tr.Get(p.ctx, e.ID)
			if d.Entry.Status != c.status || d.Entry.AppliedBy != c.by {
				t.Fatalf("row %s by %q, want %s by %q", d.Entry.Status, d.Entry.AppliedBy, c.status, c.by)
			}
			_, waiting, err := led.WaitingFor(p.ctx, []string{d.Entry.IdentityKey})
			if err != nil || waiting == c.settled {
				t.Fatalf("still waiting = %v, want %v (%v)", waiting, !c.settled, err)
			}
		})
	}
}

func TestOutcome_RejectionBeforeReceipt(t *testing.T) {
	p := newPipeEnv(t)
	id := p.add("Example Corp", "Backend Engineer", tracker.Evaluated)
	p.seed(sig{id: "m1", typ: classify.TypeRejection, role: "Backend Engineer"})
	p.link()
	d, _ := p.tr.Get(p.ctx, id)
	if d.Entry.Status != tracker.Rejected {
		t.Fatalf("status = %s", d.Entry.Status)
	}
	inferred := false
	for _, ev := range d.Events {
		inferred = inferred || strings.Contains(string(ev.Detail), "inferred_applied")
	}
	if !inferred {
		t.Fatalf("the tracker did not record that the row was never marked applied: %+v", d.Events)
	}
	// The thread's timeline says what the message did to the row, for the
	// page to name the move and offer to take it back.
	td, _, err := p.repo.ThreadDetail(p.ctx, "t-m1", p.clk.now())
	if o := td.Timeline[0].Outcome; err != nil || o == nil || o.Result != OutcomeChanged || o.From != string(tracker.Evaluated) || o.To != string(tracker.Rejected) || o.CandidacyID != id {
		t.Fatalf("timeline outcome = %+v, %v", o, err)
	}
}

func TestOutcome_ReplayInReceivedOrderFromRealStatus(t *testing.T) {
	p := newPipeEnv(t)
	id := p.add("Example Corp", "Backend Engineer", tracker.Evaluated)
	// Stored out of order; the receipt is the oldest.
	p.seed(
		sig{id: "iv", typ: classify.TypeInterview, role: "Backend Engineer", at: p.ago(2 * day), thread: "t1"},
		sig{id: "rc", typ: classify.TypeApplied, role: "Backend Engineer", at: p.ago(5 * day), thread: "t1"},
		sig{id: "rj", typ: classify.TypeRejection, role: "Backend Engineer", at: p.ago(1 * day), thread: "t1"},
	)
	p.link()
	if got := p.status(id); got != tracker.Rejected {
		t.Fatalf("status = %s", got)
	}
	for _, m := range []string{"rc", "iv", "rj"} {
		o, ok, err := p.repo.outcomeOf(p.ctx, m)
		if err != nil || !ok || o.Result != OutcomeChanged {
			t.Errorf("%s: %+v %v %v", m, o, ok, err)
		}
	}
}

func TestOutcome_LateReceiptIsNoChange(t *testing.T) {
	p := newPipeEnv(t)
	id := p.add("Example Corp", "Backend Engineer", tracker.Interview)
	p.seed(sig{id: "late", typ: classify.TypeApplied, role: "Backend Engineer"})
	p.link()
	if got := p.status(id); got != tracker.Interview {
		t.Fatalf("status = %s", got)
	}
	o, _, _ := p.repo.outcomeOf(p.ctx, "late")
	if o.Result != OutcomeNoChange {
		t.Fatalf("outcome = %+v", o)
	}
	items, err := p.repo.LinkReview(p.ctx, link.Review)
	if err != nil || len(items) != 0 {
		t.Fatalf("a no-change signal reached the review queue: %+v %v", items, err)
	}
}

func TestOutcome_ArchivedRowGoesToReviewWithTrackerReason(t *testing.T) {
	p := newPipeEnv(t)
	id := p.add("Example Corp", "Backend Engineer", tracker.Rejected)
	p.clk.advance(40 * day)
	if rows, err := p.tr.Archive(p.ctx, tracker.ActorUser, tracker.ArchiveOptions{}); err != nil || len(rows) != 1 {
		t.Fatalf("archive = %v %v", rows, err)
	}
	p.seed(sig{id: "m1", typ: classify.TypeInterview, role: "Backend Engineer", at: p.clk.now()})
	p.link()
	if got := p.status(id); got != tracker.Rejected {
		t.Fatalf("the archived row changed: %s", got)
	}
	items, err := p.repo.LinkReview(p.ctx, link.Review)
	if err != nil || len(items) != 1 || !strings.Contains(items[0].OutcomeReason, "reopen") || items[0].OutcomeTo != "interview" || items[0].OutcomeFrom != "rejected" {
		t.Fatalf("review queue = %+v %v", items, err)
	}
}

func TestOutcome_ConfirmingALinkAsksTheTrackerAgainForARefusedSignal(t *testing.T) {
	p := newPipeEnv(t)
	old := p.add("Example Corp", "Backend Engineer", tracker.Rejected)
	p.clk.advance(40 * day)
	if rows, err := p.tr.Archive(p.ctx, tracker.ActorUser, tracker.ArchiveOptions{}); err != nil || len(rows) != 1 {
		t.Fatalf("archive = %v %v", rows, err)
	}
	p.seed(sig{id: "m1", typ: classify.TypeInterview, role: "Backend Engineer"}, sig{id: "m2", typ: classify.TypeInterview, role: "Backend Engineer"})
	p.link()
	if items, err := p.repo.LinkReview(p.ctx, link.Review); err != nil || len(items) != 2 {
		t.Fatalf("review queue = %+v %v", items, err)
	}
	// The user says the first mail is about another application.
	again := p.add("Example Corp", "Platform Engineer", tracker.Applied)
	res, err := p.stack.Pipeline.ConfirmLink(p.ctx, "m1", again)
	if err != nil || res.Outcome == nil || res.Outcome.Result != OutcomeChanged || res.Outcome.CandidacyID != again {
		t.Fatalf("confirm = %+v (outcome %+v) %v", res, res.Outcome, err)
	}
	if got := p.status(again); got != tracker.Interview {
		t.Fatalf("the confirmed row is %s", got)
	}
	// The second mail is confirmed for the archived row: the tracker refuses
	// again and the item stays, with the reason.
	res, err = p.stack.Pipeline.ConfirmLink(p.ctx, "m2", old)
	if err != nil || res.Outcome == nil || res.Outcome.Result != OutcomeReview {
		t.Fatalf("confirm on the archived row = %+v (outcome %+v) %v", res, res.Outcome, err)
	}
	items, err := p.repo.LinkReview(p.ctx, link.Review)
	if err != nil || len(items) != 1 || items[0].SignalID != "m2" || !strings.Contains(items[0].OutcomeReason, "reopen") {
		t.Fatalf("review queue after the confirmations = %+v %v", items, err)
	}
}

func TestOutcome_ReplayTwiceWritesNothing(t *testing.T) {
	p := newPipeEnv(t)
	id := p.add("Example Corp", "Backend Engineer", tracker.Evaluated)
	p.seed(sig{id: "m1", typ: classify.TypeApplied, role: "Backend Engineer"}, sig{id: "m2", typ: classify.TypeInterview, role: "Backend Engineer", at: p.clk.now().Add(time.Minute), thread: "t-m1"})
	p.link()
	before, _ := p.tr.Get(p.ctx, id)
	st, err := p.stack.Pipeline.ApplyOutcomes(p.ctx)
	if err != nil || st.Changed+st.NoChange+st.Review != 0 || st.AlreadyProcessed != 2 {
		t.Fatalf("second pass = %+v %v", st, err)
	}
	after, _ := p.tr.Get(p.ctx, id)
	if len(after.Events) != len(before.Events) {
		t.Fatalf("events grew from %d to %d", len(before.Events), len(after.Events))
	}
}

func TestOutcome_OnlyAutoLinksWrite(t *testing.T) {
	p := newPipeEnv(t)
	a := p.add("Example Corp", "Backend Engineer", tracker.Evaluated)
	b := p.add("Example Corp", "Data Scientist", tracker.Evaluated)
	// No role: two roles at one company, so the link is review.
	p.seed(sig{id: "m1", typ: classify.TypeRejection, role: classify.UnknownRole})
	p.link()
	if p.status(a) != tracker.Evaluated || p.status(b) != tracker.Evaluated {
		t.Fatal("a review link wrote a status")
	}
	items, _ := p.repo.LinkReview(p.ctx, link.Review)
	if len(items) != 1 || items[0].SignalID != "m1" {
		t.Fatalf("review queue = %+v", items)
	}
}

func TestOutcome_OfferWaitsForConfirmation(t *testing.T) {
	p := newPipeEnv(t)
	id := p.add("Example Corp", "Backend Engineer", tracker.Interview)
	p.seed(sig{id: "o1", typ: classify.TypeOffer, role: "Backend Engineer"})
	p.link()
	if got := p.status(id); got != tracker.Interview {
		t.Fatalf("an offer signal moved the status to %s", got)
	}
	reqs, err := p.repo.Confirmations(p.ctx, "pending")
	if err != nil || len(reqs) != 1 || reqs[0].Status != "offer" || reqs[0].CandidacyID != id {
		t.Fatalf("confirmations = %+v %v", reqs, err)
	}
	if err := p.stack.Pipeline.DecideConfirmation(p.ctx, reqs[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if got := p.status(id); got != tracker.Offer {
		t.Fatalf("after confirming: %s", got)
	}
	if err := p.stack.Pipeline.DecideConfirmation(p.ctx, reqs[0].ID, true); !errors.Is(err, ErrNoConfirmation) {
		t.Fatalf("a second decision = %v", err)
	}
	// Undo of the confirmation, in one call: the row is back to interview and
	// the request waits again, and only once.
	if back, err := p.stack.Pipeline.RestoreConfirmation(p.ctx, reqs[0].ID); err != nil || back != tracker.Interview || p.status(id) != tracker.Interview {
		t.Fatalf("restore a confirmed offer: %s %v, row %s", back, err, p.status(id))
	}
	// The confirmation's note goes with it.
	if d, err := p.tr.Get(p.ctx, id); err != nil || strings.Contains(d.Entry.Notes, "Offer confirmed") {
		t.Fatalf("notes after the undo = %q %v", d.Entry.Notes, err)
	}
	if again, _ := p.repo.Confirmations(p.ctx, "pending"); len(again) != 1 {
		t.Fatalf("pending after restore = %+v", again)
	}
	if _, err := p.stack.Pipeline.RestoreConfirmation(p.ctx, reqs[0].ID); !errors.Is(err, ErrNoConfirmation) {
		t.Fatalf("restoring a pending request = %v", err)
	}
	// Once the row moved on after a confirmation, the undo changes nothing.
	if err := p.stack.Pipeline.DecideConfirmation(p.ctx, reqs[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := p.tr.SetStatus(p.ctx, tracker.StatusChange{ID: id, To: tracker.Rejected, Actor: tracker.ActorUser}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.stack.Pipeline.RestoreConfirmation(p.ctx, reqs[0].ID); !errors.Is(err, store.ErrConflict) || p.status(id) != tracker.Rejected {
		t.Fatalf("undo after the row moved on = %v, row %s", err, p.status(id))
	}
	if again, _ := p.repo.Confirmations(p.ctx, "pending"); len(again) != 0 {
		t.Fatalf("a refused undo put the request back: %+v", again)
	}
}

func TestOutcome_NeverCreatesRows(t *testing.T) {
	p := newPipeEnv(t)
	p.seed(sig{id: "m1", typ: classify.TypeApplied, employer: "Nowhere Inc", role: "Pilot"})
	p.link()
	page, err := p.tr.List(p.ctx, tracker.ListQuery{Archived: true})
	if err != nil || page.Total != 0 {
		t.Fatalf("rows = %+v %v", page, err)
	}
	items, _ := p.repo.LinkReview(p.ctx, link.Unlinked)
	if len(items) != 1 {
		t.Fatalf("unlinked queue = %+v", items)
	}
}

func TestLink_ReviewReasonsNeverAuto(t *testing.T) {
	for _, reason := range []string{classify.ReviewSenderUnverified, classify.ReviewSignupWindowUnknown} {
		t.Run(reason, func(t *testing.T) {
			p := newPipeEnv(t)
			id := p.add("Example Corp", "Backend Engineer", tracker.Evaluated)
			p.seed(sig{id: "m1", typ: classify.TypeApplied, role: "Backend Engineer", reasons: reason})
			p.link()
			if p.status(id) != tracker.Evaluated {
				t.Fatal("a signal with review reason " + reason + " wrote a status")
			}
			items, _ := p.repo.LinkReview(p.ctx, link.Review)
			if len(items) != 1 || items[0].CandidacyID != 0 {
				t.Fatalf("queue = %+v", items)
			}
		})
	}
}

func TestLink_ThreadContinuityAndConfirmationIsThreadWideAndFinal(t *testing.T) {
	p := newPipeEnv(t)
	a := p.add("Example Corp", "Backend Engineer", tracker.Applied)
	b := p.add("Example Corp", "Data Scientist", tracker.Applied)
	p.seed(sig{id: "m1", thread: "t1", typ: classify.TypeReplyNeeded, role: classify.UnknownRole, at: p.ago(2 * day)})
	p.link()
	items, _ := p.repo.LinkReview(p.ctx, link.Review)
	if len(items) != 1 {
		t.Fatalf("queue = %+v", items)
	}
	res, err := p.stack.Pipeline.ConfirmLink(p.ctx, "m1", a)
	if err != nil || res.Outcome == nil || res.Outcome.Result != OutcomeChanged {
		t.Fatalf("confirm = %+v %v", res, err)
	}
	if p.status(a) != tracker.Responded || p.status(b) != tracker.Applied {
		t.Fatalf("statuses %s %s", p.status(a), p.status(b))
	}
	// A new message of the thread inherits the confirmed link, even though the
	// company has two candidacies.
	p.seed(sig{id: "m2", thread: "t1", typ: classify.TypeInterview, role: classify.UnknownRole, at: p.ago(1 * day)})
	p.link()
	if p.status(a) != tracker.Interview || p.status(b) != tracker.Applied {
		t.Fatalf("after the second message: %s %s", p.status(a), p.status(b))
	}
	// Final: confirming another candidacy for the thread is refused.
	if _, err := p.stack.Pipeline.ConfirmLink(p.ctx, "m2", b); !errors.Is(err, ErrNoLinkable) {
		t.Fatalf("a second, different confirmation = %v", err)
	}
	// Re-resolution leaves the confirmed link alone.
	if _, err := p.stack.Pipeline.Resolve(p.ctx); err != nil {
		t.Fatal(err)
	}
	links, _ := p.repo.linkRows(p.ctx)
	if l := links["m1"]; !l.confirmed || l.candidacyID != a {
		t.Fatalf("m1 link = %+v", l)
	}
}

func TestLink_RejectHidesTheSignalAndKeepsItInAudit(t *testing.T) {
	p := newPipeEnv(t)
	p.seed(sig{id: "m1", typ: classify.TypeReplyNeeded, role: "Backend Engineer", tier: classify.TierReview, reasons: classify.ReviewCompanyUnknown})
	if err := p.repo.RejectLink(p.ctx, "m1"); err != nil {
		t.Fatal(err)
	}
	if err := p.repo.RejectLink(p.ctx, "missing"); !errors.Is(err, ErrNoSignal) {
		t.Fatalf("unknown signal = %v", err)
	}
	threads, _ := p.repo.Threads(p.ctx, p.clk.now())
	if len(threads) != 0 {
		t.Fatalf("a rejected signal is still in the inbox: %+v", threads)
	}
	sigs, _ := p.repo.Signals(p.ctx)
	if len(sigs) != 1 || sigs[0].Tier != classify.TierSuppressed {
		t.Fatalf("signals = %+v", sigs)
	}
	items, _ := p.repo.LinkReview(p.ctx, link.Review)
	if len(items) != 0 {
		t.Fatalf("queue = %+v", items)
	}
}

func TestLink_RestoreBringsBackWhatRejectHidAndOnlyThat(t *testing.T) {
	p := newPipeEnv(t)
	p.seed(sig{id: "m1", thread: "t1", typ: classify.TypeRejection, role: "Backend Engineer"},
		sig{id: "m2", thread: "t2", typ: classify.TypeRejection, role: "Data Scientist"})
	p.link()
	queue := func() []LinkItem {
		items, err := p.repo.LinkReview(p.ctx, link.Unlinked)
		if err != nil {
			t.Fatal(err)
		}
		return items
	}
	threads := func() int {
		got, err := p.repo.Threads(p.ctx, p.clk.now())
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}
	before, inbox := queue(), threads()
	if len(before) != 2 || inbox != 2 {
		t.Fatalf("setup: queue %+v, %d threads", before, inbox)
	}
	for _, id := range []string{"m1", "m2"} {
		if err := p.repo.RejectLink(p.ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.repo.RestoreLink(p.ctx, "m1"); err != nil {
		t.Fatal(err)
	}
	if got := queue(); len(got) != 1 || !reflect.DeepEqual(got[0], before[0]) || threads() != 1 {
		t.Fatalf("after restoring m1: queue %+v, %d threads; want only m1, as before", got, threads())
	}
	// Restoring a signal that is not rejected changes nothing; an unknown one is refused.
	if err := p.repo.RestoreLink(p.ctx, "m1"); err != nil || len(queue()) != 1 {
		t.Fatalf("restoring a signal that is not rejected: %v, queue %+v", err, queue())
	}
	if err := p.repo.RestoreLink(p.ctx, "missing"); !errors.Is(err, ErrNoSignal) {
		t.Fatalf("unknown signal = %v", err)
	}
}

func TestLink_NewTrackerRowResolvesOldUnlinkedSignal(t *testing.T) {
	p := newPipeEnv(t)
	p.seed(sig{id: "m1", typ: classify.TypeRejection, role: "Backend Engineer"})
	p.link()
	items, _ := p.repo.LinkReview(p.ctx, link.Unlinked)
	if len(items) != 1 {
		t.Fatalf("unlinked = %+v", items)
	}
	id := p.add("Example Corp", "Backend Engineer", tracker.Applied)
	p.link()
	if got := p.status(id); got != tracker.Rejected {
		t.Fatalf("status = %s", got)
	}
}

func TestLink_CandidacyLoadFailureFailsTheStage(t *testing.T) {
	p := newPipeEnv(t)
	p.add("Example Corp", "Backend Engineer", tracker.Applied)
	p.seed(sig{id: "m1", typ: classify.TypeRejection, role: "Backend Engineer"})
	ctx, cancel := context.WithCancel(p.ctx)
	cancel()
	if _, err := p.stack.Pipeline.Link(ctx, p.run); err == nil {
		t.Fatal("a pass that could not load the candidacies succeeded")
	}
	links, _ := p.repo.linkRows(p.ctx)
	if len(links) != 0 {
		t.Fatalf("an empty candidacy list was written down as an answer: %+v", links)
	}
}

func TestSignalEvents_OncePerMessageNoBodyAndNoOutgoing(t *testing.T) {
	p := newPipeEnv(t)
	p.seed(sig{id: "m1", typ: classify.TypeInterview, tier: classify.TierReview, reasons: classify.ReviewCompanyUnknown},
		sig{id: "mine", typ: classify.TypeReplyNeeded, dir: classify.DirOutgoing})
	evs, err := p.repo.SignalEvents(p.ctx, 0, 100)
	if err != nil || len(evs) != 1 || evs[0].MessageID != "m1" || evs[0].Tier != classify.TierReview || evs[0].Direction != classify.DirIncoming {
		t.Fatalf("events = %+v %v", evs, err)
	}
	// Reclassified with another tier and type after a resync: still one event.
	p.seed(sig{id: "m1", typ: classify.TypeRejection, tier: classify.TierActionable})
	_, _, err = p.repo.storeBatch(p.ctx, p.run, classify.Version, []scoredMessage{{msg: &Message{ID: "m1", ThreadID: "t-m1", InternalMS: 1}, verdict: Verdict{Kind: VerdictSignal, Type: classify.TypeOffer, Tier: classify.TierActionable, Direction: classify.DirIncoming}}}, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	evs, _ = p.repo.SignalEvents(p.ctx, 0, 100)
	if len(evs) != 1 || evs[0].SignalType != classify.TypeInterview {
		t.Fatalf("events after reclassification = %+v", evs)
	}
	rows, _ := p.st.Read().Query(`SELECT name FROM pragma_table_info('gmail_signal_events')`)
	defer rows.Close()
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		if strings.Contains(name, "body") || strings.Contains(name, "subject") || strings.Contains(name, "text") {
			t.Errorf("the event record has a column %q", name)
		}
	}
}

func TestSignalEvents_AFreshMessageAfterIgnoredGetsOneEventAndRetentionPrunes(t *testing.T) {
	p := newPipeEnv(t)
	p.seed(sig{id: "m1", typ: classify.TypeApplied})
	p.clk.advance(31 * day)
	p.seed(sig{id: "m2", typ: classify.TypeApplied, at: p.clk.now()})
	n, err := p.repo.PruneSignalEvents(p.ctx)
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	evs, _ := p.repo.SignalEvents(p.ctx, 0, 100)
	if len(evs) != 1 || evs[0].MessageID != "m2" {
		t.Fatalf("events = %+v", evs)
	}
	after, _ := p.repo.SignalEvents(p.ctx, evs[0].Seq, 100)
	if len(after) != 0 {
		t.Fatalf("after the last seq: %+v", after)
	}
}

func TestGmail_Purge_EmptiesTheOutcomeAndDigestTables(t *testing.T) {
	p := newPipeEnv(t)
	p.clk.set(at(10, 8, 0))
	id := p.add("Example Corp", "Backend Engineer", tracker.Interview)
	p.seed(sig{id: "m1", thread: "t1", typ: classify.TypeOffer, role: "Backend Engineer", at: at(10, 7, 0)})
	p.link()
	_, _ = p.stack.EvaluateAlerts(p.ctx)
	d, err := p.stack.Digests.Run(p.ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = p.repo.Rate(p.ctx, d.ID, "t1", VerdictUp)
	_ = p.repo.RejectLink(p.ctx, "m1")
	_, _ = p.stack.SummaryFor(p.ctx, "t1")
	_ = id
	if err := Purge(p.ctx, p.repo, p.vault, true); err != nil {
		t.Fatal(err)
	}
	for _, table := range gmailTables {
		var n int
		if err := p.st.Read().QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s holds %d rows after a purge (%v)", table, n, err)
		}
	}
}

type stageCatcher struct {
	got []interview.StageInput
}

func (c *stageCatcher) RecordStage(_ context.Context, _ int64, in interview.StageInput) (interview.Stage, error) {
	c.got = append(c.got, in)
	return interview.Stage{ID: in.ID}, nil
}

func TestInterview_EmailProposedStage(t *testing.T) {
	p := newPipeEnv(t)
	catcher := &stageCatcher{}
	p.stack.Pipeline.Stages = catcher
	p.add("Example Corp", "Backend Engineer", tracker.Applied)
	due := p.clk.now().Add(7 * day)
	p.seed(sig{id: "Mail9f3A", typ: classify.TypeInterview, role: "Backend Engineer", subject: "Technical interview with Example Corp", due: due},
		sig{id: "m2", typ: classify.TypeApplied, role: "Backend Engineer", at: p.ago(3 * day)})
	p.link()
	if len(catcher.got) != 1 {
		t.Fatalf("proposed stages = %+v", catcher.got)
	}
	in := catcher.got[0]
	if in.ID != "mail-mail9f3a" || in.Type != interview.TypeTechnical || in.UserConfirmed || in.At != due.UTC().Format(time.RFC3339) {
		t.Errorf("proposed stage = %+v", in)
	}
	p.stack.Pipeline.Stages = catcher
	if _, err := p.stack.Pipeline.ApplyOutcomes(p.ctx); err != nil || len(catcher.got) != 1 {
		t.Errorf("a second pass proposed the stage again: %v %d", err, len(catcher.got))
	}
}

func TestLink_PriorAutoLinksSeedThreads(t *testing.T) {
	p := newPipeEnv(t)
	a := p.add("Example Corp", "Backend Engineer", tracker.Applied)
	b := p.add("Example Corp", "Data Scientist", tracker.Applied)
	p.seed(sig{id: "m1", thread: "t1", typ: classify.TypeReplyNeeded, role: "Backend Engineer", at: p.ago(5 * day)})
	p.link()
	// Two candidacies make a role-less message ambiguous by itself; the thread's
	// earlier auto link, kept from a previous pass, settles it.
	p.seed(sig{id: "m2", thread: "t1", typ: classify.TypeReplyNeeded, role: classify.UnknownRole, at: p.ago(4 * day)})
	p.seed(sig{id: "alone", thread: "t-alone", typ: classify.TypeReplyNeeded, role: classify.UnknownRole, at: p.ago(4 * day)})
	// Within one pass a link grows the thread's set for the messages after it.
	p.seed(sig{id: "n1", thread: "t2", typ: classify.TypeReplyNeeded, role: "Data Scientist", at: p.ago(3 * day)})
	p.seed(sig{id: "n2", thread: "t2", typ: classify.TypeReplyNeeded, role: classify.UnknownRole, at: p.ago(2 * day)})
	// Tombstoned and ignored mail never link.
	p.seed(sig{id: "gone", thread: "t3", typ: classify.TypeReplyNeeded, role: "Backend Engineer", at: p.ago(day)})
	p.seed(sig{id: "ignored", thread: "t4", typ: classify.TypeReplyNeeded, role: "Backend Engineer", tier: classify.TierLow, at: p.ago(day)})
	p.exec(`INSERT INTO gmail_tombstones (message_id, tombstoned_at) VALUES ('gone', '2027-01-01T00:00:00Z')`)
	p.link()
	links, err := p.repo.linkRows(p.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]int64{"m1": a, "m2": a, "n1": b, "n2": b} {
		if l := links[id]; l.decision != link.Auto || l.candidacyID != want {
			t.Errorf("%s = %+v, want auto to %d", id, l, want)
		}
	}
	if l := links["alone"]; l.decision == link.Auto {
		t.Errorf("a role-less message of an unlinked thread linked on its own: %+v", l)
	}
	for _, id := range []string{"gone", "ignored"} {
		if _, ok := links[id]; ok {
			t.Errorf("%s was linked", id)
		}
	}
}

func TestOutcome_MissingCandidacyToReview(t *testing.T) {
	p := newPipeEnv(t)
	id := p.add("Example Corp", "Backend Engineer", tracker.Applied)
	p.add("Other Corp", "Data Scientist", tracker.Applied)
	p.seed(sig{id: "m1", typ: classify.TypeRejection, role: "Backend Engineer"})
	// The row disappears between the link and the status change.
	var st OutcomeStats
	if err := p.stack.Pipeline.replay(p.ctx, id+100, []pending{{sig: SignalRow{MessageID: "m1", Type: classify.TypeRejection}, cand: id + 100}}, &st); err != nil {
		t.Fatal(err)
	}
	o, ok, err := p.repo.outcomeOf(p.ctx, "m1")
	if err != nil || !ok || o.Result != OutcomeReview || o.Reason == "" || st.Review != 1 {
		t.Fatalf("outcome %+v %v %v stats %+v: a vanished row is a review item", o, ok, err, st)
	}
}
