package api

import (
	"strings"
	"testing"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/browser"
	"applysmith/internal/identity"
	"applysmith/internal/notify"
	"applysmith/internal/tracker"
)

// A list leaves out the rows on a job site the app browser is signed out of
// and says so once, as it starts, in the inbox: a LinkedIn row run minutes
// into a list stopped at LinkedIn's signed-out page. A sign-in check that
// finds the site signed in resolves the item.
func TestApplyStart_AListLeavesOutRowsOnASiteTheBrowserIsSignedOutOf(t *testing.T) {
	b := &fakeBrowser{signedOut: map[string]bool{"linkedin.com": true}}
	h := newDomainHarness(t, func(c *Config) { unavailableWindow(c); c.SessionBrowser = b })
	score := 4.4
	var ids []wire.SafeInt
	for i, p := range []identity.Posting{postingOf("Example Labs"), {Company: "Sample Works", Role: "Engineer", URL: "https://www.linkedin.com/jobs/view/4000000001"}} {
		res, err := h.upsertReport(t.Context(), tracker.Report{Number: int64(i + 1), Date: "2026-01-02", Outcome: tracker.OutcomeRecommended, Kind: tracker.ScoreFinal, Score: &score, Posting: p})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, apikit.Safe64(res.Entry.ID))
	}
	r := h.post("/api/apply/batches", wire.ApplyBatchRequest{Lanes: []string{wire.LaneApplyNow, wire.LaneSelective}})
	if r.StatusCode != 202 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	got := decode[wire.ApplyBatchAccepted](t, r)
	if got.Rows != 1 || len(got.Skipped) != 1 || got.Skipped[0].Company != "Sample Works" || !strings.Contains(got.Skipped[0].Reason, "signed out of LinkedIn") {
		t.Fatalf("batch = %+v", got)
	}
	board := h.domainOf().notify.board
	e, err := board.Get(t.Context(), siteSignInID+"linkedin.com")
	if err != nil || e.State != notify.NeedsYou || e.Reason != "sign-in" || !strings.Contains(e.Detail, "1 row") ||
		!strings.Contains(e.Detail, "No Applysmith extension is paired in your Chrome") {
		t.Fatalf("item = %+v, %v", e, err)
	}
	waitLong(t, "the list to end", func() bool { return !h.domainOf().apply.busy.Load() })
	// A list left with no row says why its rows were left out.
	r = h.post("/api/apply/batches", wire.ApplyBatchRequest{Candidacies: ids[1:]})
	if r.StatusCode != 422 || r.errReason() != "nothing-to-run" || !strings.Contains(string(r.body), "Sample Works: the app browser is signed out of LinkedIn") {
		t.Fatalf("a list of the signed-out row only: %d %s", r.StatusCode, r.body)
	}
	b.mu.Lock()
	b.checked = []browser.SignInCheck{{Site: browser.SignInSites[0], State: browser.StateSignedIn}}
	b.mu.Unlock()
	if r := h.post("/api/browser/sessions/check", nil); r.StatusCode != 200 {
		t.Fatalf("check: %d %s", r.StatusCode, r.body)
	}
	if e, err := board.Get(t.Context(), siteSignInID+"linkedin.com"); err != nil || e.Waiting() {
		t.Fatalf("after the check: %+v, %v", e, err)
	}
}
