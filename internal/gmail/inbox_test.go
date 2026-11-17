package gmail

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"applysmith/internal/gmail/classify"
	"applysmith/internal/gmail/lifecycle"
	"applysmith/internal/gmail/link"
	"applysmith/internal/tracker"
)

func TestAPI_Gmail_InboxPagingAndFilters(t *testing.T) {
	p := newPipeEnv(t)
	for i := range 5 {
		p.seed(sig{id: fmt.Sprintf("r%d", i), typ: classify.TypeReplyNeeded, at: p.ago(2 * hour)})
	}
	p.seed(sig{id: "rc", typ: classify.TypeApplied, at: p.ago(5 * day)},
		sig{id: "off", typ: classify.TypeOffer, at: p.ago(day)})
	all, err := p.repo.Inbox(p.ctx, InboxQuery{}, p.clk.now())
	if err != nil || len(all.Threads) != 7 {
		t.Fatalf("inbox = %d threads, %v", len(all.Threads), err)
	}
	if all.Threads[0].ID != "t-off" || all.Threads[0].View.Attention != lifecycle.AttentionUrgent {
		t.Errorf("the offer should sort first: %+v", all.Threads[0].View)
	}
	last := all.Threads[len(all.Threads)-1]
	if last.View.Lane != lifecycle.LaneReceiptOnly {
		t.Errorf("the receipt sorts last: %s", last.View.Lane)
	}
	if got, _ := p.repo.Inbox(p.ctx, InboxQuery{Lane: lifecycle.LaneDoNow, Owner: lifecycle.OwnerUser}, p.clk.now()); len(got.Threads) != 5 {
		t.Errorf("lane and owner combine with AND: %d", len(got.Threads))
	}
	if got, _ := p.repo.Inbox(p.ctx, InboxQuery{Lane: lifecycle.LaneDoNow, Owner: lifecycle.OwnerCompany}, p.clk.now()); len(got.Threads) != 0 {
		t.Errorf("owner company: %d", len(got.Threads))
	}
	if got, _ := p.repo.Inbox(p.ctx, InboxQuery{Link: "unlinked"}, p.clk.now()); len(got.Threads) != 7 {
		t.Errorf("link filter: %d", len(got.Threads))
	}
	// Pages of 3 with an opaque cursor cover every thread once.
	seen := map[string]bool{}
	cur := ""
	for pages := 0; pages < 5; pages++ {
		page, err := p.repo.Inbox(p.ctx, InboxQuery{Limit: 3, Cursor: cur}, p.clk.now())
		if err != nil {
			t.Fatal(err)
		}
		for _, th := range page.Threads {
			if seen[th.ID] {
				t.Fatalf("%s listed twice", th.ID)
			}
			seen[th.ID] = true
		}
		if cur = page.Next; cur == "" {
			break
		}
	}
	if len(seen) != 7 {
		t.Errorf("paging covered %d threads", len(seen))
	}
	// An invalid cursor is treated as none.
	if got, _ := p.repo.Inbox(p.ctx, InboxQuery{Cursor: "not-a-cursor!"}, p.clk.now()); len(got.Threads) != 7 {
		t.Errorf("invalid cursor: %d", len(got.Threads))
	}
}

func TestAPI_Gmail_InboxPageSizeBound(t *testing.T) {
	p := newPipeEnv(t)
	var batch []sig
	for i := range 230 {
		batch = append(batch, sig{id: fmt.Sprintf("m%03d", i), typ: classify.TypeApplied, at: p.ago(hour)})
	}
	p.seed(batch...)
	page, err := p.repo.Inbox(p.ctx, InboxQuery{Limit: 1000}, p.clk.now())
	if err != nil || len(page.Threads) != MaxInboxLimit || page.Next == "" {
		t.Fatalf("limit=1000 gave %d rows, next %q, %v", len(page.Threads), page.Next, err)
	}
	def, _ := p.repo.Inbox(p.ctx, InboxQuery{}, p.clk.now())
	if len(def.Threads) != DefaultInboxLimit {
		t.Errorf("default page = %d", len(def.Threads))
	}
}

func TestAPI_Gmail_ThreadDetailHasTimelineWithEvidence(t *testing.T) {
	p := newPipeEnv(t)
	p.seed(sig{id: "a", thread: "t1", typ: classify.TypeApplied, at: p.ago(2 * day)}, sig{id: "b", thread: "t1", typ: classify.TypeInterview, at: p.ago(day)})
	d, ok, err := p.repo.ThreadDetail(p.ctx, "t1", p.clk.now())
	if err != nil || !ok || len(d.Timeline) != 2 || d.View.State != lifecycle.StateInterview {
		t.Fatalf("detail = %+v %v %v", d, ok, err)
	}
	if len(d.Timeline[0].Evidence) == 0 {
		t.Error("no evidence on the timeline")
	}
	if _, ok, _ := p.repo.ThreadDetail(p.ctx, "nope", p.clk.now()); ok {
		t.Error("an unknown thread was found")
	}
}

// inboxFixture seeds four threads whose employer, role and subject are known,
// so the expected sets of the filters are read off the fixture.
func inboxFixture(p *pipeEnv) {
	p.seed(
		sig{id: "reply", thread: "t-reply", typ: classify.TypeReplyNeeded, employer: "Northwind Example", role: "Staff Engineer", subject: "Can we schedule a call?", at: p.ago(hour * 3)},
		sig{id: "oa", thread: "t-oa", typ: classify.TypeAssessment, employer: "Contoso Example", role: "Backend Engineer", subject: "Complete the coding exercise", at: p.ago(hour * 5)},
		sig{id: "rej", thread: "t-rej", typ: classify.TypeRejection, employer: "Fabrikam Example", role: "Platform Engineer", subject: "Your application", at: p.ago(day)},
		sig{id: "app", thread: "t-app", typ: classify.TypeApplied, employer: "Northwind Example", role: "Data Engineer", subject: "We received your application", at: p.ago(2 * day)},
	)
}

func ids(threads []Thread) []string {
	var out []string
	for _, t := range threads {
		out = append(out, t.ID)
	}
	slices.Sort(out)
	return out
}

func doNow(t *testing.T, p *pipeEnv) []string {
	t.Helper()
	page, err := p.repo.Inbox(p.ctx, InboxQuery{Lane: lifecycle.LaneDoNow}, p.clk.now())
	if err != nil {
		t.Fatal(err)
	}
	return ids(page.Threads)
}

func TestAPI_Gmail_DoneEndsTheActionUntilNewMail(t *testing.T) {
	p := newPipeEnv(t)
	inboxFixture(p)
	if got := doNow(t, p); !slices.Equal(got, []string{"t-oa", "t-reply"}) {
		t.Fatalf("do now before = %v", got)
	}
	if err := p.repo.MarkDone(p.ctx, "t-oa"); err != nil {
		t.Fatal(err)
	}
	if got := doNow(t, p); !slices.Equal(got, []string{"t-reply"}) {
		t.Errorf("a done thread is still in do now: %v", got)
	}
	// It stays readable in the closed lane, with its kind.
	d, ok, err := p.repo.ThreadDetail(p.ctx, "t-oa", p.clk.now())
	if err != nil || !ok || !d.Done || d.View.Lane != lifecycle.LaneClosed || d.View.Owner != lifecycle.OwnerNone || d.Kind() != classify.TypeAssessment || len(d.Timeline) != 1 {
		t.Fatalf("done thread = %+v %v %v", d.Thread.View, ok, err)
	}
	if page, _ := p.repo.Inbox(p.ctx, InboxQuery{Lane: lifecycle.LaneClosed, Kind: classify.TypeAssessment}, p.clk.now()); !slices.Equal(ids(page.Threads), []string{"t-oa"}) {
		t.Errorf("closed lane = %v", ids(page.Threads))
	}
	// Marking again changes nothing; undo restores the lane.
	if err := p.repo.MarkDone(p.ctx, "t-oa"); err != nil {
		t.Errorf("marking a done thread again: %v", err)
	}
	if err := p.repo.ReopenDone(p.ctx, "t-oa"); err != nil {
		t.Fatal(err)
	}
	if got := doNow(t, p); !slices.Equal(got, []string{"t-oa", "t-reply"}) {
		t.Errorf("undo: do now = %v", got)
	}
	// A newer message re-opens a done thread: the decision covers the mail seen.
	// Promotional mail (low tier) asks nothing of the user and does not.
	if err := p.repo.MarkDone(p.ctx, "t-oa"); err != nil {
		t.Fatal(err)
	}
	p.seed(sig{id: "promo", thread: "t-oa", typ: classify.TypeLowPriority, tier: classify.TierLow, employer: "Contoso Example", subject: "New roles you may like", at: p.clk.now()})
	if got := doNow(t, p); !slices.Equal(got, []string{"t-reply"}) {
		t.Errorf("a low-tier message re-opened a done thread: %v", got)
	}
	p.seed(sig{id: "oa2", thread: "t-oa", typ: classify.TypeAssessment, employer: "Contoso Example", role: "Backend Engineer", subject: "Reminder: coding exercise", at: p.clk.now()})
	if got := doNow(t, p); !slices.Equal(got, []string{"t-oa", "t-reply"}) {
		t.Errorf("new mail did not re-open the thread: %v", got)
	}
	if d, _, _ := p.repo.ThreadDetail(p.ctx, "t-oa", p.clk.now()); d.Done {
		t.Error("a re-opened thread still reads as done")
	}
	// Only a thread that asks something of the user can be marked done.
	if err := p.repo.MarkDone(p.ctx, "t-rej"); !errors.Is(err, ErrNotActionable) {
		t.Errorf("a closed thread: %v", err)
	}
	if err := p.repo.MarkDone(p.ctx, "t-none"); !errors.Is(err, ErrNoThread) {
		t.Errorf("an unknown thread: %v", err)
	}
}

func TestAPI_Gmail_DismissLeavesUntilNewMailAndUndoRestoresOnlyItsMarks(t *testing.T) {
	p := newPipeEnv(t)
	inboxFixture(p)
	got, err := p.repo.DismissThread(p.ctx, "t-reply")
	if err != nil || !slices.Equal(got, []string{"reply"}) {
		t.Fatalf("dismiss = %v, %v", got, err)
	}
	if now := doNow(t, p); !slices.Equal(now, []string{"t-oa"}) {
		t.Errorf("a dismissed thread is still in do now: %v", now)
	}
	p.seed(sig{id: "reply2", thread: "t-reply", typ: classify.TypeReplyNeeded, employer: "Northwind Example", subject: "Following up", at: p.clk.now()})
	if now := doNow(t, p); !slices.Equal(now, []string{"t-oa", "t-reply"}) {
		t.Errorf("new mail did not bring the dismissed thread back: %v", now)
	}
	// Undo with the returned ids brings the first message back.
	if err := p.repo.RestoreLink(p.ctx, "reply"); err != nil {
		t.Fatal(err)
	}
	d, _, _ := p.repo.ThreadDetail(p.ctx, "t-reply", p.clk.now())
	if len(d.Timeline) != 2 {
		t.Errorf("timeline after undo = %d entries", len(d.Timeline))
	}
}

// One dismissal writes one time on every mark, so its undo, which takes off the
// marks of the newest time, takes off all of them even as the clock moves.
func TestAPI_Gmail_RestoreThreadTakesOffEveryMarkOfOneDismissal(t *testing.T) {
	p := newPipeEnv(t)
	p.seed(
		sig{id: "first", thread: "t-two", typ: classify.TypeReplyNeeded, employer: "Northwind Example", subject: "Can we schedule a call?", at: p.ago(hour * 3)},
		sig{id: "second", thread: "t-two", typ: classify.TypeReplyNeeded, employer: "Northwind Example", subject: "Following up", at: p.ago(hour)},
	)
	at := p.clk.now()
	repo := NewRepo(p.st, func() time.Time { at = at.Add(time.Second); return at })
	got, err := repo.DismissThread(p.ctx, "t-two")
	if err != nil || len(got) != 2 {
		t.Fatalf("dismiss = %v, %v", got, err)
	}
	if n, err := repo.RestoreThread(p.ctx, "t-two"); err != nil || n != 2 {
		t.Fatalf("restore took off %d of 2 marks: %v", n, err)
	}
}

// A row's mail state is read through the link and the lifecycle lane: the
// most pressing open thread decides it, a thread that only names the same
// company does not count, and a done or closed thread is not open.
func TestAPI_Gmail_MailByCandidacyReadsThroughTheLinkAndLane(t *testing.T) {
	p := newPipeEnv(t)
	a := p.add("Example Corp", "Backend Engineer", tracker.Applied)
	b := p.add("Rejecting Inc", "Analyst", tracker.Applied)
	p.add("Example Corp", "Data Scientist", tracker.Applied)
	p.seed(
		sig{id: "reply", thread: "t-reply", typ: classify.TypeReplyNeeded, role: "Backend Engineer", at: p.ago(3 * day)},
		sig{id: "prep", thread: "t-prep", typ: classify.TypeInterview, role: "Backend Engineer", at: p.ago(day), due: p.clk.now().Add(5 * day)},
		sig{id: "same", thread: "t-same", typ: classify.TypeReplyNeeded, role: classify.UnknownRole, at: p.ago(day)},
		sig{id: "rej", thread: "t-rej", typ: classify.TypeRejection, employer: "Rejecting Inc", role: "Analyst", at: p.ago(day)},
	)
	p.link()
	got, err := p.repo.MailByCandidacy(p.ctx, p.clk.now())
	if err != nil {
		t.Fatal(err)
	}
	m := got[a]
	if m.Thread.ID != "t-reply" || m.Thread.View.Lane != lifecycle.LaneDoNow || m.Thread.View.StalenessDays != 3 || m.Open != 2 {
		t.Errorf("the row's mail = %s %s %d days, %d open", m.Thread.ID, m.Thread.View.Lane, m.Thread.View.StalenessDays, m.Open)
	}
	if items, _ := p.repo.LinkReview(p.ctx, link.Review); len(items) != 1 {
		t.Fatalf("the same-company thread should wait in the link queue: %d", len(items))
	}
	if _, ok := got[b]; ok || len(got) != 1 {
		t.Errorf("a closed thread or an unlinked one gave a row mail state: %v", got)
	}
	if err := p.repo.MarkDone(p.ctx, "t-reply"); err != nil {
		t.Fatal(err)
	}
	got, _ = p.repo.MailByCandidacy(p.ctx, p.clk.now())
	if m := got[a]; m.Thread.ID != "t-prep" || m.Open != 1 {
		t.Errorf("after done: %s, %d open", m.Thread.ID, m.Open)
	}
}
