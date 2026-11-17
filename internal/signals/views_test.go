package signals

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Undo of a rejection (flow round 12, U-1): the item waits again exactly as it
// waited before the press. An undo that finds the item no longer rejected, an
// approved item or one undone already, changes nothing.
func TestSignals_ReopenUndoesARejectionOnly(t *testing.T) {
	for _, approve := range []bool{false, true} {
		e := newEnv(t)
		_, ev, _ := e.company("Example Corp", raiseBody, epoch.Add(-24*time.Hour), adapterAlerts)
		held := Assessment{EventID: ev.ID, Rubric: RubricVersion, Status: StatusMidConfidence, Type: TypeFunding, Intent: IntentStrong, Scale: ScaleHeadcount, Strength: 8, Confidence: 0.5}
		if err := e.svc.repo.StoreAssessment(e.ctx, held, "", StatusMidConfidence, false); err != nil {
			t.Fatal(err)
		}
		open, _ := e.svc.Review(e.ctx, false)
		if _, err := e.svc.Decide(e.ctx, open[0].ID, approve); err != nil {
			t.Fatal(err)
		}
		decided, _ := e.svc.Review(e.ctx, true)
		got, err := e.svc.Reopen(e.ctx, open[0].ID)
		switch {
		case approve && (!errors.Is(err, ErrNotUndoable) || !reflect.DeepEqual(e.review(), decided)):
			t.Errorf("an approval was undone: %v, %+v", err, e.review())
		case !approve && (err != nil || !reflect.DeepEqual(got, open[0]) || !reflect.DeepEqual(e.review(), open) || e.count(`SELECT count(*) FROM signal_review WHERE decided_at IS NULL`) != 1):
			t.Errorf("the rejection was not undone: %v, %+v", err, e.review())
		}
		before := e.review()
		if _, err := e.svc.Reopen(e.ctx, open[0].ID); !errors.Is(err, ErrNotUndoable) || !reflect.DeepEqual(e.review(), before) {
			t.Errorf("approve %v: a second undo: %v", approve, err)
		}
	}
}

func (e *env) review() []ReviewItem {
	e.t.Helper()
	items, err := e.svc.Review(e.ctx, true)
	if err != nil {
		e.t.Fatal(err)
	}
	return items
}

// Undo of a dismissal (flow round 12, U-1): the company takes back the state it
// had before the press, and an undo that finds it no longer dismissed, watched
// again or undone already, changes nothing.
func TestSignals_RestoreUndoesADismissal(t *testing.T) {
	type press = func(*Service, context.Context, int64) error
	watch, dismiss := press((*Service).Watch), press((*Service).Dismiss)
	for _, c := range []struct {
		name   string
		press  []press
		undone bool
		want   string
	}{
		{"new", []press{dismiss}, true, StateNew},
		{"watching", []press{watch, dismiss}, true, StateWatching},
		{"watching, dismissed twice", []press{watch, dismiss, dismiss}, true, StateWatching},
		{"watched again since", []press{dismiss, watch}, false, StateWatching},
	} {
		e := newEnv(t)
		co := e.hot("Example Corp")
		for _, press := range c.press {
			if err := press(e.svc, e.ctx, co.ID); err != nil {
				t.Fatal(err)
			}
		}
		state := func() string {
			got, err := e.svc.repo.Company(e.ctx, co.ID)
			if err != nil {
				t.Fatal(err)
			}
			return got.State
		}
		if err := e.svc.Restore(e.ctx, co.ID); (err == nil) != c.undone || state() != c.want {
			t.Errorf("%s: undo = %v, state %s, want %s", c.name, err, state(), c.want)
		}
		if err := e.svc.Restore(e.ctx, co.ID); !errors.Is(err, ErrNotUndoable) || state() != c.want {
			t.Errorf("%s: a second undo = %v, state %s", c.name, err, state())
		}
	}
}

func TestSignalsCard_NoPostingFallbackLinksNoNetwork(t *testing.T) {
	e := newEnv(t)
	c := e.hot("Example Corp")
	reqs := len(e.web.requests(""))
	d, err := e.svc.Detail(e.ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.SearchLinks) != 2 {
		t.Fatalf("links = %v", d.SearchLinks)
	}
	for i, l := range d.SearchLinks {
		u, err := url.Parse(l)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query().Get("q")
		if !strings.Contains(q, "site:linkedin.com/in") || !strings.Contains(q, `"Example Corp"`) || strings.Contains(q, "@") {
			t.Errorf("link %d query = %q", i, q)
		}
	}
	if !strings.Contains(d.SearchLinks[0], "recruiter") || !strings.Contains(d.SearchLinks[1], "engineering") {
		t.Fatalf("links = %v", d.SearchLinks)
	}
	if len(e.web.requests("")) != reqs {
		t.Fatal("building the links made a request")
	}
	// A gate-passing posting removes the fallback.
	e.exec(`INSERT INTO signal_postings (company_id, ats, board, external_id, url, title, location, verdict, first_seen_at) VALUES (?, 'greenhouse', 'b', '1', 'https://x.example.test/j', 't', 'l', 'pass', ?)`, c.ID, fmtTS(e.clk.now()))
	if d, _ := e.svc.Detail(e.ctx, c.ID); len(d.SearchLinks) != 0 {
		t.Fatalf("links with a posting = %v", d.SearchLinks)
	}
}

func TestSignalsAPI_EvidenceChainAndListFilters(t *testing.T) {
	e := newEnv(t)
	e.profile()
	c := e.hot("Example Corp")
	other, ev, doc := e.company("Beta Corp", "Beta Corp posted a job.", epoch.Add(-time.Hour), adapterHN)
	e.assessment(ev, Assessment{Type: TypeProduct, Intent: IntentModerate, Strength: 5, Confidence: 0.8, Quotes: []Quote{{DocumentID: doc, Quote: "posted a job"}}})
	e.svc.Rescore(e.ctx)
	d, err := e.svc.Detail(e.ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Events) != 1 || len(d.Events[0].Documents) != 1 || d.Events[0].Assessment == nil || len(d.Events[0].Assessment.Quotes) != 1 || d.Tier.Tier != TierHot || len(d.Decomposition) != 1 {
		t.Fatalf("detail = %+v", d)
	}
	if d.Events[0].Documents[0].URL == "" || d.Events[0].Documents[0].EventTime().IsZero() {
		t.Fatal("the chain lacks links or dates")
	}
	all, _ := e.svc.List(e.ctx, ListFilter{})
	if len(all) != 2 || all[0].Company != "Example Corp" {
		t.Fatalf("list = %+v", all)
	}
	for f, want := range map[string]ListFilter{"Example Corp": {Tier: TierHot}, "Beta Corp": {Source: "signals-hn"}} {
		got, _ := e.svc.List(e.ctx, want)
		if len(got) != 1 || got[0].Company != f {
			t.Errorf("filter %+v = %+v", want, got)
		}
	}
	if got, _ := e.svc.List(e.ctx, ListFilter{State: StateDismissed}); len(got) != 0 {
		t.Fatalf("dismissed = %v", got)
	}
	if got, _ := e.svc.List(e.ctx, ListFilter{From: epoch.Add(-time.Minute)}); len(got) != 0 {
		t.Fatalf("date range = %v", got)
	}
	e.svc.Dismiss(e.ctx, other.ID)
	if got, _ := e.svc.List(e.ctx, ListFilter{State: StateDismissed}); len(got) != 1 {
		t.Fatalf("dismissed = %v", got)
	}
}

func TestSignals_Retention_BodiesReducedToMetadataAfter180Days(t *testing.T) {
	e := newEnv(t)
	c, ev, _ := e.company("Example Corp", raiseBody, epoch.Add(-24*time.Hour), adapterAlerts)
	e.assessment(ev, Assessment{})
	e.clk.advance(181 * 24 * time.Hour)
	n, err := e.svc.repo.PruneBodies(e.ctx)
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	var body string
	var dropped int
	e.st.Read().QueryRowContext(e.ctx, `SELECT body, body_dropped FROM signal_documents`).Scan(&body, &dropped)
	if body != "" || dropped != 1 {
		t.Fatalf("body %q dropped %d", body, dropped)
	}
	if e.count(`SELECT count(*) FROM signal_assessments`) != 1 || e.count(`SELECT count(*) FROM signal_events`) != 1 {
		t.Fatal("an assessment or event was removed")
	}
	_ = c
}

// Deferral lines are written by every tick; old ones go, every other audit
// line stays, the state lines a restore reads above all.
func TestSignals_Retention_OldDeferralLinesAreDeleted(t *testing.T) {
	e := newEnv(t)
	c := e.hot("Example Corp")
	for _, kind := range []string{"deferred", "state", "tier-change"} {
		if err := e.svc.repo.Audit(e.ctx, kind, c.ID, "old"); err != nil {
			t.Fatal(err)
		}
	}
	e.clk.advance(181 * 24 * time.Hour)
	if err := e.svc.repo.Audit(e.ctx, "deferred", c.ID, "recent"); err != nil {
		t.Fatal(err)
	}
	n, err := e.svc.repo.PruneDeferrals(e.ctx)
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	if left := e.count(`SELECT count(*) FROM signal_audit WHERE kind = 'deferred'`); left != 1 {
		t.Fatalf("%d deferral lines left, want the recent one", left)
	}
	if kept := e.count(`SELECT count(*) FROM signal_audit WHERE detail = 'old'`); kept != 2 {
		t.Fatalf("%d of the two other old lines were kept", kept)
	}
}
