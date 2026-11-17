package tracker

import (
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"

	"applysmith/internal/identity"
)

func TestUpsert_BlankCompanyOrRoleRefused(t *testing.T) {
	f := newFixture(t)
	for _, c := range [][2]string{{"Example Corp", ""}, {"Example Corp", "  "}, {"", "Engineer"}} {
		_, err := f.upsertReport(report(1, c[0], c[1], "https://example.com/j", 4.0))
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%q/%q: %v", c[0], c[1], err)
		}
		if _, err := f.tr.Add(f.ctx, AddRow{Company: c[0], Role: c[1]}); !errors.Is(err, ErrInvalid) {
			t.Errorf("Add %q/%q: %v", c[0], c[1], err)
		}
	}
	if n := f.count(`SELECT (SELECT count(*) FROM tracker_entries) + (SELECT count(*) FROM tracker_events)`); n != 0 {
		t.Errorf("%d rows or events written", n)
	}
}

func TestUpsert_ReportValidation(t *testing.T) {
	f := newFixture(t)
	base := report(1, "Example Corp", "Engineer", "https://example.com/j", 4.0)
	bad := map[string]func(*Report){
		"no number":        func(r *Report) { r.Number = 0 },
		"bad date":         func(r *Report) { r.Date = "10/03/2027" },
		"bad outcome":      func(r *Report) { r.Outcome = "maybe" },
		"final below 1":    func(r *Report) { r.Score = fp(0.5) },
		"above 5":          func(r *Report) { r.Score = fp(5.1) },
		"missing kind":     func(r *Report) { r.Kind = "" },
		"screening above5": func(r *Report) { r.Kind, r.Score = ScoreScreening, fp(6) },
	}
	for name, mut := range bad {
		r := base
		mut(&r)
		if _, err := f.upsertReport(r); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	r := base
	r.Kind, r.Score = ScoreScreening, fp(0.4)
	res := f.upsert(r)
	if res.Entry.Score == nil || *res.Entry.Score != 0.4 {
		t.Errorf("a screening score can be below 1.0: %+v", res.Entry.Score)
	}
}

func TestUpsert_FromReport_StatusPerRecommendation(t *testing.T) {
	tests := []struct {
		outcome   Outcome
		score     *float64
		status    Status
		wantScore bool
		wantNote  string
	}{
		{OutcomeRecommended, fp(4.2), Evaluated, true, ""},
		{OutcomeReview, fp(3.0), Evaluated, true, "review"},
		{OutcomeSkip, fp(2.1), Skipped, true, ""},
		{OutcomeGated, fp(3.0), Skipped, false, "Gated"},
		{OutcomeDrop, fp(0.8), Skipped, true, ""},
	}
	for _, tt := range tests {
		t.Run(string(tt.outcome), func(t *testing.T) {
			f := newFixture(t)
			r := report(1, "Example Corp", "Engineer", "https://example.com/j", 0)
			r.Outcome, r.Score = tt.outcome, tt.score
			if tt.outcome == OutcomeDrop {
				r.Kind = ScoreScreening
			}
			e := f.upsert(r).Entry
			if e.Status != tt.status || (e.Score != nil) != tt.wantScore || !strings.Contains(e.Notes, tt.wantNote) {
				t.Errorf("%+v", e)
			}
			if !tt.wantScore && e.ScoreKind != "" {
				t.Errorf("an unscored row has no score kind: %q", e.ScoreKind)
			}
		})
	}
}

func TestUpsert_GatedIsSkippedNotFailed(t *testing.T) {
	f := newFixture(t)
	r := report(7, "Example Corp", "Engineer", "https://example.com/j", 0)
	r.Outcome, r.Score, r.Note = OutcomeGated, nil, "Gate rules: location, clearance"
	e := f.upsert(r).Entry
	if e.Status != Skipped || e.Score != nil || !strings.Contains(e.Notes, "location, clearance") {
		t.Errorf("%+v", e)
	}
}

func TestUpsert_NewRowEventAndIdentityKey(t *testing.T) {
	f := newFixture(t)
	e := f.upsert(report(3, "Example Corp", "Engineer", "https://boards.greenhouse.io/example-co/jobs/1001?gh_src=x", 4.0)).Entry
	if e.IdentityKey != "greenhouse:example-co/1001" || e.PostingURL != "https://boards.greenhouse.io/example-co/jobs/1001" {
		t.Errorf("key %q url %q", e.IdentityKey, e.PostingURL)
	}
	evs := f.events(e.ID, EventCreated)
	if len(evs) != 1 || evs[0].Actor != ActorEvaluation || evs[0].NewStatus != Evaluated {
		t.Errorf("%+v", evs)
	}
}

func TestScore_UpdateRules_Table(t *testing.T) {
	type state struct {
		score float64
		kind  ScoreKind
		none  bool
	}
	tests := []struct {
		name    string
		have    state
		in      state
		want    state
		updated bool
	}{
		{"null score takes any scored report", state{none: true}, state{3.0, ScoreFinal, false}, state{3.0, ScoreFinal, false}, true},
		{"final supersedes screening even when lower", state{4.0, ScoreScreening, false}, state{3.6, ScoreFinal, false}, state{3.6, ScoreFinal, false}, true},
		{"screening never replaces final", state{3.0, ScoreFinal, false}, state{4.9, ScoreScreening, false}, state{3.0, ScoreFinal, false}, false},
		{"higher final replaces lower", state{3.0, ScoreFinal, false}, state{4.1, ScoreFinal, false}, state{4.1, ScoreFinal, false}, true},
		{"lower final changes nothing", state{4.2, ScoreFinal, false}, state{3.9, ScoreFinal, false}, state{4.2, ScoreFinal, false}, false},
		{"higher screening replaces lower screening", state{2.0, ScoreScreening, false}, state{2.5, ScoreScreening, false}, state{2.5, ScoreScreening, false}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			first := report(10, "Example Corp", "Engineer", "https://example.com/j", 3.0)
			if tt.have.none {
				first.Score = nil
			} else {
				first.Score, first.Kind = fp(tt.have.score), tt.have.kind
			}
			id := f.upsert(first).Entry.ID
			next := report(11, "Example Corp", "Engineer", "https://example.com/j", tt.in.score)
			next.Kind = tt.in.kind
			res := f.upsert(next)
			got := res.Entry
			if got.Score == nil || *got.Score != tt.want.score || got.ScoreKind != tt.want.kind {
				t.Fatalf("got %v %s", got.Score, got.ScoreKind)
			}
			if n := len(f.events(id, EventScoreUpdated)); (n == 1) != tt.updated {
				t.Errorf("score_updated events = %d, want updated=%v", n, tt.updated)
			}
			wantReport := int64(10)
			if tt.updated {
				wantReport = 11
			}
			if *got.ReportNumber != wantReport {
				t.Errorf("report link = %d, want %d", *got.ReportNumber, wantReport)
			}
			if tt.updated && !strings.Contains(got.Notes, "Score") {
				t.Errorf("no note appended: %q", got.Notes)
			}
		})
	}
}

func TestScore_SkippedRowReplaced(t *testing.T) {
	f := newFixture(t)
	r := report(12, "Example Corp", "Engineer", "https://example.com/j", 3.1)
	r.Outcome = OutcomeSkip
	id := f.upsert(r).Entry.ID
	next := report(30, "Example Corp", "Engineer", "https://example.com/j", 4.4)
	e := f.upsert(next).Entry
	if e.Status != Evaluated || *e.Score != 4.4 || *e.ReportNumber != 30 {
		t.Fatalf("%+v", e)
	}
	if !strings.Contains(e.Notes, "12") || !strings.Contains(e.Notes, "30") {
		t.Errorf("note names neither report: %q", e.Notes)
	}
	if evs := f.events(id, EventStatusChanged); len(evs) != 1 || evs[0].Actor != ActorEvaluation {
		t.Errorf("%+v", evs)
	}
}

func TestScore_SkippedRowKeptByANewSkipReport(t *testing.T) {
	f := newFixture(t)
	r := report(12, "Example Corp", "Engineer", "https://example.com/j", 3.1)
	r.Outcome = OutcomeSkip
	f.upsert(r)
	r2 := report(30, "Example Corp", "Engineer", "https://example.com/j", 2.0)
	r2.Outcome = OutcomeSkip
	e := f.upsert(r2).Entry
	if e.Status != Skipped || *e.Score != 3.1 || *e.ReportNumber != 12 {
		t.Errorf("%+v", e)
	}
}

// The offline simulation's report gives a row its score, never its status:
// its skip makes a new row Evaluated, and its recommendation leaves a skipped
// row skipped.
func TestUpsert_SimulatedReportNeverSetsTheStatus(t *testing.T) {
	f := newFixture(t)
	r := report(12, "Example Corp", "Engineer", "https://example.com/j", 2.1)
	r.Outcome, r.Simulated = OutcomeSkip, true
	if e := f.upsert(r).Entry; e.Status != Evaluated || e.Score == nil || *e.Score != 2.1 {
		t.Fatalf("new row from a simulated skip: %+v", e)
	}
	skipped := report(13, "Other Corp", "Engineer", "https://example.com/k", 2.0)
	skipped.Outcome = OutcomeSkip
	id := f.upsert(skipped).Entry.ID
	next := report(30, "Other Corp", "Engineer", "https://example.com/k", 4.4)
	next.Simulated = true
	if e := f.upsert(next).Entry; e.Status != Skipped {
		t.Fatalf("a simulated recommendation revived a skipped row: %+v", e)
	}
	if evs := f.events(id, EventStatusChanged); len(evs) != 0 {
		t.Errorf("status events: %+v", evs)
	}
}

func TestScore_ArchivedSkippedRowIsNotRevived(t *testing.T) {
	f := newFixture(t)
	r := report(12, "Example Corp", "Engineer", "https://example.com/j", 3.1)
	r.Outcome = OutcomeSkip
	id := f.upsert(r).Entry.ID
	f.exec(`UPDATE tracker_entries SET archived_at = '2027-01-01T00:00:00Z' WHERE id = ?`, id)
	e := f.upsert(report(30, "Example Corp", "Engineer", "https://example.com/j", 4.4)).Entry
	if e.Status != Skipped {
		t.Errorf("status = %s", e.Status)
	}
	if *e.Score != 4.4 {
		t.Errorf("the score rules still apply on an archived row: %v", *e.Score)
	}
	if n := len(f.events(id, EventTransitionRefused)); n != 1 {
		t.Errorf("refusal events = %d", n)
	}
}

func TestScore_ReEvaluationOfAppliedPosting(t *testing.T) {
	f := newFixture(t)
	id := f.upsert(report(10, "Example Corp", "Engineer", "https://example.com/j", 3.5)).Entry.ID
	f.setStatus(id, Applied, ActorUser)
	e := f.upsert(report(20, "Example Corp", "Engineer", "https://example.com/j", 4.3)).Entry
	if e.Status != Applied || *e.Score != 4.3 || *e.ReportNumber != 20 {
		t.Errorf("%+v", e)
	}
}

func TestScore_EvaluationCannotReopenRejected(t *testing.T) {
	f := newFixture(t)
	id := f.upsert(report(10, "Example Corp", "Engineer", "https://example.com/j", 3.5)).Entry.ID
	f.setStatus(id, Rejected, ActorUser)
	e := f.upsert(report(20, "Example Corp", "Engineer", "https://example.com/j", 3.0)).Entry
	if e.Status != Rejected || *e.ReportNumber != 10 || *e.Score != 3.5 {
		t.Errorf("a worse report must not touch the row: %+v", e)
	}
	e = f.upsert(report(21, "Example Corp", "Engineer", "https://example.com/j", 4.5)).Entry
	if e.Status != Rejected || *e.ReportNumber != 21 {
		t.Errorf("an improved score links: %+v", e)
	}
}

func TestUpsert_SameReportNumberIsTheSameRow(t *testing.T) {
	f := newFixture(t)
	a := f.upsert(report(10, "Example Corp", "Engineer", "https://example.com/a", 3.5)).Entry
	res := f.upsert(report(10, "Other Corp", "Chef", "https://example.org/b", 3.5))
	if res.Created || res.Entry.ID != a.ID || res.Rule != RuleReport {
		t.Errorf("%+v", res)
	}
}

func TestUpsert_UserRowTakesTheReportLink(t *testing.T) {
	f := newFixture(t)
	a := f.add(AddRow{Company: "Example Corp", Role: "Engineer", URL: "https://example.com/j"})
	r := report(5, "Example Corp", "Engineer", "https://example.com/j", 0)
	r.Outcome, r.Score = OutcomeGated, nil
	e := f.upsert(r).Entry
	if e.ID != a.ID || e.ReportNumber == nil || *e.ReportNumber != 5 {
		t.Errorf("%+v", e)
	}
	if n := len(f.events(a.ID, EventReportLinked)); n != 1 {
		t.Errorf("report_linked events = %d", n)
	}
}

func TestDuplicate_Table(t *testing.T) {
	type spec struct {
		name         string
		firstCo      string
		firstRole    string
		firstLoc     string
		firstURL     string
		nextCo       string
		nextRole     string
		nextLoc      string
		nextURL      string
		sameRow      bool
		wantRule     string
		wantAliasKey string
	}
	tests := []spec{
		{name: "years and boilerplate are ignored", firstCo: "Example Corp", firstRole: "Software Engineer (Remote)", firstURL: "https://example.com/1", nextCo: "Example Corp", nextRole: "Software Engineer - Remote 2026", nextURL: "https://example.com/2", sameRow: true, wantRule: RuleOpening},
		{name: "level markers separate roles", firstCo: "Example Corp", firstRole: "Software Engineer I", firstURL: "https://example.com/1", nextCo: "Example Corp", nextRole: "Software Engineer II", nextURL: "https://example.com/2"},
		{name: "location conflict creates a separate row", firstCo: "Example Corp", firstRole: "Software Engineer", firstLoc: "Exampleton", firstURL: "https://example.com/1", nextCo: "Example Corp", nextRole: "Software Engineer", nextLoc: "Sampleville", nextURL: "https://example.com/2"},
		{name: "two requisitions with one title", firstCo: "Example Corp", firstRole: "Software Engineer", firstURL: "https://boards.greenhouse.io/example-co/jobs/1001", nextCo: "Example Corp", nextRole: "Software Engineer", nextURL: "https://boards.greenhouse.io/example-co/jobs/2002"},
		{name: "qualifier separates roles", firstCo: "Example Corp", firstRole: "Software Engineer (iOS)", firstURL: "https://example.com/1", nextCo: "Example Corp", nextRole: "Software Engineer", nextURL: "https://example.com/2"},
		{name: "mirror attaches with an alias", firstCo: "Example Corp", firstRole: "Software Engineer", firstURL: "https://boards.greenhouse.io/example-co/jobs/1001", nextCo: "Example Corp", nextRole: "Software Engineer", nextURL: "https://mirror.example.net/p/55", sameRow: true, wantRule: RuleOpening, wantAliasKey: "https://mirror.example.net/p/55"},
		{name: "same key under another URL shape", firstCo: "Example Corp", firstRole: "Software Engineer", firstURL: "https://boards.greenhouse.io/example-co/jobs/1001", nextCo: "Renamed Corp", nextRole: "Totally Different", nextURL: "https://job-boards.greenhouse.io/embed/job_app?for=example-co&token=1001", sameRow: true, wantRule: RuleKey},
		{name: "placeholder company groups nothing", firstCo: "Unknown", firstRole: "Software Engineer", firstURL: "https://example.com/1", nextCo: "Unknown", nextRole: "Software Engineer", nextURL: "https://example.com/2"},
		{name: "no location on one side attaches", firstCo: "Example Corp", firstRole: "Software Engineer", firstURL: "https://example.com/1", nextCo: "Example Corp", nextRole: "Software Engineer", nextLoc: "Exampleton", nextURL: "https://example.com/2", sameRow: true, wantRule: RuleOpening},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			a := report(1, tt.firstCo, tt.firstRole, tt.firstURL, 4.0)
			a.Posting.Location = tt.firstLoc
			first := f.upsert(a).Entry
			b := report(2, tt.nextCo, tt.nextRole, tt.nextURL, 4.0)
			b.Posting.Location = tt.nextLoc
			res := f.upsert(b)
			if res.Created == tt.sameRow {
				t.Fatalf("created=%v, wanted same row=%v", res.Created, tt.sameRow)
			}
			if !tt.sameRow {
				if f.count(`SELECT count(*) FROM tracker_entries`) != 2 || len(f.events(first.ID, EventDuplicateAttached)) != 0 {
					t.Error("a separate row records no duplicate")
				}
				return
			}
			if res.Entry.ID != first.ID || res.Rule != tt.wantRule {
				t.Errorf("rule %q row %d", res.Rule, res.Entry.ID)
			}
			if res.Entry.PostingURL != first.PostingURL {
				t.Errorf("the row must keep its own URL, got %q", res.Entry.PostingURL)
			}
			if len(f.events(first.ID, EventDuplicateAttached)) != 1 {
				t.Error("no duplicate_attached event")
			}
			if tt.wantAliasKey != "" && f.count(`SELECT count(*) FROM tracker_aliases WHERE identity_key = ? AND entry_id = ?`, tt.wantAliasKey, first.ID) != 1 {
				t.Error("the mirror key did not become an alias")
			}
		})
	}
}

func TestDuplicate_AliasMatchesLaterReports(t *testing.T) {
	f := newFixture(t)
	first := f.upsert(report(1, "Example Corp", "Software Engineer", "https://boards.greenhouse.io/example-co/jobs/1001", 4.0)).Entry
	f.upsert(report(2, "Example Corp", "Software Engineer", "https://mirror.example.net/p/55", 3.0))
	res := f.upsert(report(3, "Other Name", "Other Title", "https://mirror.example.net/p/55?utm_source=x", 3.0))
	if res.Created || res.Entry.ID != first.ID || res.Rule != RuleKey {
		t.Errorf("%+v", res)
	}
}

func TestDuplicate_AliasedRowStillRefusesAnotherRequisition(t *testing.T) {
	f := newFixture(t)
	f.upsert(report(1, "Example Corp", "Software Engineer", "https://mirror.example.net/p/55", 4.0))
	f.upsert(report(2, "Example Corp", "Software Engineer", "https://boards.greenhouse.io/example-co/jobs/1001", 4.0)) // attaches; ATS alias recorded
	res := f.upsert(report(3, "Example Corp", "Software Engineer", "https://boards.greenhouse.io/example-co/jobs/2002", 4.0))
	if !res.Created {
		t.Error("a different requisition attached to a row whose alias is another requisition")
	}
}

// An evaluation that reached its posting through a tracking link or a job
// board's listing hands that address's key over as an alias. A new row keeps
// it, and a row recorded at that address is the posting's row even when the
// employer's page names the role differently: it moves to the posting's URL
// and key and keeps its old key, so the application goes to the employer's
// page. A key another row holds stays with that row.
func TestUpsert_AReportReachedThroughAnAddressKeepsItAsAnAlias(t *testing.T) {
	const listing, posting = "https://www.linkedin.com/jobs/view/4105", "https://careers.example.test/job/4105"
	listingKey, _ := identity.URLKey(listing)
	postingKey, _ := identity.URLKey(posting)
	reached := func(n int64, role string) Report {
		r := report(n, "Northwind Example", role, posting, 4.0)
		r.Aliases = []string{listingKey}
		return r
	}
	const aliasOf = `SELECT count(*) FROM tracker_aliases WHERE identity_key = ? AND entry_id = ?`

	f := newFixture(t)
	created := f.upsert(reached(1, "Software Engineer")).Entry
	if f.count(aliasOf, listingKey, created.ID) != 1 {
		t.Error("the new row does not keep the listing's key")
	}

	f = newFixture(t)
	row := f.upsert(report(1, "Northwind Example", "Software Engineer", listing, 3.0)).Entry
	res := f.upsert(reached(2, "Backend Platform Engineer"))
	if res.Created || res.Entry.ID != row.ID || res.Rule != RuleKey || res.Entry.PostingURL != posting || res.Entry.IdentityKey != postingKey {
		t.Fatalf("the listing's row: %+v", res)
	}
	if f.count(aliasOf, listingKey, row.ID) != 1 {
		t.Error("the moved row lost the listing's key")
	}

	f = newFixture(t)
	listed := f.upsert(report(1, "Northwind Example", "Software Engineer", listing, 3.0)).Entry
	other := f.upsert(report(2, "Northwind Example", "Software Engineer II", "https://mirror.example.net/p/4105", 3.0)).Entry
	f.upsert(report(3, "Northwind Example", "Software Engineer II", posting, 3.0)) // the posting's key becomes an alias of other
	res, err := f.upsertReport(reached(4, "Software Engineer"))
	if err != nil || res.Entry.ID != listed.ID || res.Entry.PostingURL != listing {
		t.Fatalf("a key another row holds: %+v, %v", res, err)
	}
	if f.count(aliasOf, postingKey, other.ID) != 1 {
		t.Error("the posting's key left the row that held it")
	}

	// A row that already held the posting's key as an alias holds it once.
	f = newFixture(t)
	row = f.upsert(report(1, "Northwind Example", "Software Engineer", listing, 3.0)).Entry
	f.upsert(report(2, "Northwind Example", "Software Engineer", posting, 3.0))
	if res = f.upsert(reached(3, "Software Engineer")); res.Entry.IdentityKey != postingKey || f.count(aliasOf, postingKey, row.ID) != 0 {
		t.Errorf("the posting's key is the row's key and its alias: %+v", res)
	}
}

func TestDuplicate_ArchivedRowsStillMatch(t *testing.T) {
	f := newFixture(t)
	first := f.upsert(report(1, "Example Corp", "Software Engineer", "https://example.com/jobs/9", 4.0)).Entry
	f.setStatus(first.ID, Rejected, ActorUser)
	f.exec(`UPDATE tracker_entries SET archived_at = '2027-01-01T00:00:00Z'`)
	res := f.upsert(report(2, "Example Corp", "Software Engineer", "https://example.com/jobs/9?utm_source=x", 3.0))
	if res.Created || res.Entry.ID != first.ID {
		t.Fatalf("%+v", res)
	}
	evs := f.events(first.ID, EventDuplicateAttached)
	if len(evs) != 1 || !strings.Contains(string(evs[0].Detail), `"archived":true`) {
		t.Errorf("%+v", evs)
	}
}

func TestDuplicate_HighestStatusWinsAmongTwoMatches(t *testing.T) {
	cands := []candidate{
		{id: 1, company: "Example Corp", role: "Software Engineer", key: "https://mirror-a.example.net/1", status: Evaluated},
		{id: 2, company: "Example Corp", role: "Software Engineer", key: "https://mirror-b.example.net/2", status: Applied},
		{id: 3, company: "Example Corp", role: "Software Engineer", key: "https://mirror-c.example.net/3", status: Applied},
	}
	in := matchInput{posting: identity.Posting{Key: "https://mirror-d.example.net/4", Company: "Example Corp", Role: "Software Engineer"}}
	c, rule, ok := findMatch(cands, in)
	if !ok || rule != RuleOpening || c.id != 2 {
		t.Fatalf("got %d %q %v; want the applied row with the lowest id", c.id, rule, ok)
	}
}

func TestDuplicate_RulePrecedence(t *testing.T) {
	cands := []candidate{
		{id: 1, company: "Example Corp", role: "Software Engineer", key: "k:1", status: Applied},
		{id: 2, company: "Other Corp", role: "Chef", key: "k:2", status: Evaluated, report: 77},
		{id: 3, company: "Third Corp", role: "Baker", key: "k:3", status: Evaluated, aliases: []string{"k:alias"}},
	}
	p := identity.Posting{Key: "k:alias", Company: "Example Corp", Role: "Software Engineer"}
	tests := []struct {
		name string
		in   matchInput
		want int64
		rule string
	}{
		{"report number first", matchInput{report: 77, entryID: 1, posting: p}, 2, RuleReport},
		{"row id second", matchInput{entryID: 1, posting: p}, 1, RuleID},
		{"identity key including aliases third", matchInput{posting: p}, 3, RuleKey},
		{"opening last", matchInput{posting: identity.Posting{Key: "k:new", Company: "Example Corp", Role: "Software Engineer"}}, 1, RuleOpening},
	}
	for _, tt := range tests {
		c, rule, ok := findMatch(cands, tt.in)
		if !ok || c.id != tt.want || rule != tt.rule {
			t.Errorf("%s: got %d %q %v", tt.name, c.id, rule, ok)
		}
	}
}

func TestUpsert_ConcurrentSamePostingOneRow(t *testing.T) {
	f := newFixture(t)
	var wg sync.WaitGroup
	ids := make([]int64, 8)
	errs := make([]error, 8)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := f.upsertReport(report(int64(100+i), "Example Corp", "Software Engineer", "https://boards.greenhouse.io/example-co/jobs/1001", 4.0))
			ids[i], errs[i] = res.Entry.ID, err
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
		if ids[i] != ids[0] {
			t.Errorf("writer %d saw row %d, writer 0 saw %d", i, ids[i], ids[0])
		}
	}
	if n := f.count(`SELECT count(*) FROM tracker_entries`); n != 1 {
		t.Errorf("%d rows", n)
	}
}

func TestSchema_UniqueIndexesGuardReportAndKey(t *testing.T) {
	f := newFixture(t)
	f.upsert(report(1, "Example Corp", "Engineer", "https://example.com/1", 4.0))
	ins := `INSERT INTO tracker_entries (date, company, role, status, identity_key, report_number, created_at, updated_at) VALUES ('2027-01-01', 'A', 'B', 'evaluated', ?, ?, 'x', 'x')`
	for name, args := range map[string][]any{
		"same identity key": {"https://example.com/1", nil},
		"same report":       {"https://example.com/other", 1},
	} {
		err := f.st.Write(f.ctx, func(tx *sql.Tx) error { _, err := tx.Exec(ins, args...); return err })
		if err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// Rows without a report number never collide with each other.
	for _, key := range []string{"k:1", "k:2"} {
		if err := f.st.Write(f.ctx, func(tx *sql.Tx) error { _, err := tx.Exec(ins, key, nil); return err }); err != nil {
			t.Errorf("null report numbers must not collide: %v", err)
		}
	}
}

// The role of a posting read from a page's document title loses the job site's
// name and the company's segment ("Software Engineer | Northwind Example
// Private Limited | LinkedIn" is a LinkedIn tab). A role that holds a pipe or a
// dash of its own is whole, and a title is never emptied.
func TestRoleName_PageTitleLosesTheJobSiteAndTheCompany(t *testing.T) {
	for _, c := range []struct{ name, title, company, want string }{
		{"role, company, site", "Software Engineer | Northwind Example Private Limited | LinkedIn", "", "Software Engineer"},
		{"the company leads", "Northwind Example | Platform Engineer | LinkedIn", "Northwind Example", "Platform Engineer"},
		{"a dash is no division", "Full Stack Engineer 4- People Tech - GenAI | Northwind Example | LinkedIn", "Northwind Example", "Full Stack Engineer 4- People Tech - GenAI"},
		{"a pipe of the role's own, no site, no company", "Backend Engineer | Payments", "Northwind Example", "Backend Engineer | Payments"},
		{"nothing but a site", "LinkedIn", "", "LinkedIn"},
	} {
		if got := RoleName(c.title, c.company); got != c.want {
			t.Errorf("%s: RoleName(%q, %q) = %q, want %q", c.name, c.title, c.company, got, c.want)
		}
	}
}
