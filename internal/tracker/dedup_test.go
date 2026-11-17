package tracker

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"applysmith/internal/store"
)

// twin inserts a second row for a posting the duplicate rules would normally
// have attached, as old data or a race could leave behind.
func (f *fixture) twin(company, role, key, location string) int64 {
	f.t.Helper()
	now := f.clk.now().UTC().Format(time.RFC3339)
	var id int64
	err := f.st.Write(f.ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO tracker_entries (date, company, role, status, location, identity_key, created_at, updated_at) VALUES ('2027-03-10', ?, ?, 'evaluated', ?, ?, ?, ?)`,
			company, role, location, key, now, now)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		_, err = tx.Exec(`INSERT INTO tracker_events (entry_id, at, actor, kind, new_status) VALUES (?, ?, 'user', 'created', 'evaluated')`, id, now)
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func TestDedup_DryRunDefaultByteIdentical(t *testing.T) {
	f := newFixture(t)
	f.twin("Example Corp", "Software Engineer", "k:1", "")
	f.twin("Example Corp", "Software Engineer", "k:2", "")
	dump := func() string {
		return f.dump(`SELECT id || '|' || company || '|' || status || '|' || notes || '|' || version || '|' || identity_key FROM tracker_entries ORDER BY id`,
			`SELECT id || '|' || entry_id || '|' || kind || '|' || at FROM tracker_events ORDER BY id`,
			`SELECT identity_key || '|' || entry_id FROM tracker_aliases ORDER BY identity_key`)
	}
	before := dump()
	groups, err := f.tr.DedupPlan(f.ctx)
	if err != nil || len(groups) != 1 || groups[0].Merged {
		t.Fatalf("%+v %v", groups, err)
	}
	if dump() != before {
		t.Error("the plan changed data")
	}
}

// dump prints the rows each query selects, one text column per row.
func (f *fixture) dump(queries ...string) string {
	var b strings.Builder
	for _, q := range queries {
		rows, err := f.st.Read().Query(q)
		if err != nil {
			f.t.Fatal(err)
		}
		for rows.Next() {
			var line string
			rows.Scan(&line)
			b.WriteString(line + "\n")
		}
		rows.Close()
	}
	return b.String()
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestDedup_Command_StrictSubjectEquality(t *testing.T) {
	tests := []struct {
		name     string
		a, b     string
		grouped  bool
		locA, lB string
	}{
		{"different requisitions are never merged", "Software Development Engineer", "Software Development Engineer - Database 2026", false, "", ""},
		{"equal titles group", "Software Engineer", "software engineer", true, "", ""},
		{"same word set groups", "Backend Engineer", "Engineer, Backend", true, "", ""},
		{"seniority counts", "Software Engineer", "Senior Software Engineer", false, "", ""},
		{"employment type counts", "Software Engineer", "Software Engineer Intern", false, "", ""},
		{"two letter words count", "AI Engineer", "Engineer", false, "", ""},
		{"differing requisition numbers", "AI Analyst - 11444", "AI Analyst - 12125", false, "", ""},
		{"level only roles need an exact title", "Senior II", "II Senior", false, "", ""},
		{"location conflict is two openings", "Software Engineer", "Software Engineer", false, "Exampleton", "Sampleville"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.twin("Example Corp", tt.a, "k:1", tt.locA)
			f.twin("Example Corp", tt.b, "k:2", tt.lB)
			groups, err := f.tr.DedupPlan(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if (len(groups) == 1) != tt.grouped {
				t.Errorf("groups = %+v", groups)
			}
		})
	}
}

func TestDedup_PlaceholderCompanyGroupsNothing(t *testing.T) {
	f := newFixture(t)
	f.twin("Unknown", "Software Engineer", "k:1", "")
	f.twin("Unknown", "Software Engineer", "k:2", "")
	if g, err := f.tr.DedupPlan(f.ctx); err != nil || len(g) != 0 {
		t.Errorf("%+v %v", g, err)
	}
}

func TestDedup_UnequalATSKeysNeverGroup(t *testing.T) {
	f := newFixture(t)
	f.twin("Example Corp", "Software Engineer", "greenhouse:example-co/1001", "")
	f.twin("Example Corp", "Software Engineer", "greenhouse:example-co/2002", "")
	if g, err := f.tr.DedupPlan(f.ctx); err != nil || len(g) != 0 {
		t.Errorf("%+v %v", g, err)
	}
}

func TestDedup_GroupsByAliasKey(t *testing.T) {
	f := newFixture(t)
	a := f.twin("Example Corp", "Alpha Role", "k:1", "")
	b := f.twin("Other Corp", "Beta Role", "k:2", "")
	f.exec(`INSERT INTO tracker_aliases (identity_key, entry_id) VALUES ('k:2', ?)`, a)
	g, _ := f.tr.DedupPlan(f.ctx)
	if len(g) != 1 || g[0].Rule != ruleDedupKey || g[0].Members[0] != a || g[0].Members[1] != b {
		t.Errorf("%+v", g)
	}
}

func TestDedup_SurvivorStatusIsMostRecentEvent(t *testing.T) {
	f := newFixture(t)
	a := f.twin("Example Corp", "Software Engineer", "k:1", "")
	b := f.twin("Example Corp", "Software Engineer", "k:2", "")
	f.setStatus(a, Applied, ActorUser) // earlier day
	f.setStatus(a, Interview, ActorUser)
	f.clk.advance(96 * time.Hour)
	f.setStatus(b, Responded, ActorGmail) // the later event is on row b
	// Row a's interview ranks above row b's reply: status order alone would
	// pick row a.
	groups, err := f.tr.DedupApply(f.ctx, ActorUser)
	if err != nil || len(groups) != 1 || !groups[0].Merged {
		t.Fatalf("%+v %v", groups, err)
	}
	if groups[0].Survivor != b || groups[0].Status != Responded {
		t.Errorf("%+v", groups[0])
	}
	got := f.get(b).Entry
	if got.Status != Responded {
		t.Errorf("%+v", got)
	}
	if f.count(`SELECT count(*) FROM tracker_entries`) != 1 {
		t.Error("the other row remains")
	}
}

func TestDedup_SameDayTieBreaksByStatusOrder(t *testing.T) {
	f := newFixture(t)
	a := f.twin("Example Corp", "Software Engineer", "k:1", "")
	b := f.twin("Example Corp", "Software Engineer", "k:2", "")
	f.setStatus(b, Responded, ActorUser)
	f.clk.advance(time.Hour)
	f.setStatus(a, Applied, ActorUser) // later the same day, lower in order
	g, err := f.tr.DedupPlan(f.ctx)
	if err != nil || len(g) != 1 {
		t.Fatalf("%+v %v", g, err)
	}
	if g[0].Survivor != b || g[0].Status != Responded {
		t.Errorf("%+v", g[0])
	}
}

func TestDedup_SurvivorPrefersBestScoreThenLowestID(t *testing.T) {
	f := newFixture(t)
	a := f.twin("Example Corp", "Software Engineer", "k:1", "")
	b := f.twin("Example Corp", "Software Engineer", "k:2", "")
	c := f.twin("Example Corp", "Software Engineer", "k:3", "")
	f.exec(`UPDATE tracker_entries SET score = 3.0, score_kind = 'final' WHERE id = ?`, b)
	f.exec(`UPDATE tracker_entries SET score = 4.9, score_kind = 'screening' WHERE id = ?`, c)
	g, _ := f.tr.DedupPlan(f.ctx)
	if g[0].Survivor != b {
		t.Errorf("a final score outranks a screening one: %+v", g[0])
	}
	f.exec(`UPDATE tracker_entries SET score = NULL, score_kind = NULL`)
	if g, _ = f.tr.DedupPlan(f.ctx); g[0].Survivor != a {
		t.Errorf("lowest id on a tie: %+v", g[0])
	}
}

func TestDedup_Apply_MergesEverything(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.LinkedTables = []LinkedTable{{Table: "mail_links", Column: "entry_id"}} })
	f.exec(`CREATE TABLE mail_links (message_id TEXT PRIMARY KEY, entry_id INTEGER NOT NULL REFERENCES tracker_entries (id)) STRICT`)
	a := f.twin("Example Corp", "Software Engineer", "k:4", "")
	b := f.twin("Example Corp", "Software Engineer", "k:9", "")
	f.exec(`UPDATE tracker_entries SET score = 3.2, score_kind = 'final', report_number = 11, notes = '2027-01-01 user: first', date = '2027-02-01' WHERE id = ?`, a)
	f.exec(`UPDATE tracker_entries SET score = 4.4, score_kind = 'final', report_number = 12, notes = '2027-01-02 user: second', resume_pdf = 1,
		cover_letter = 0, applied_at = '2027-02-05', applied_by = 'agent', location = 'Exampleton', date = '2027-01-15' WHERE id = ?`, b)
	f.exec(`UPDATE tracker_entries SET cover_letter = 1 WHERE id = ?`, a)
	f.exec(`INSERT INTO tracker_aliases (identity_key, entry_id) VALUES ('mirror:b', ?)`, b)
	f.exec(`INSERT INTO mail_links (message_id, entry_id) VALUES ('m-1', ?)`, b)
	f.exec(`INSERT INTO tracker_events (entry_id, at, actor, kind, new_status) VALUES (?, '2027-03-11T00:00:00Z', 'gmail', 'status_changed', 'evaluated')`, b)

	groups, err := f.tr.DedupApply(f.ctx, ActorUser)
	if err != nil || len(groups) != 1 || !groups[0].Merged {
		t.Fatalf("%+v %v", groups, err)
	}
	s := groups[0].Survivor
	if s != b {
		t.Fatalf("survivor %d: the most recent status event is on row b", s)
	}
	got := f.get(s)
	e := got.Entry
	if !e.ResumePDF || !e.CoverLetter || e.AppliedAt != "2027-02-05" || e.AppliedBy != ByAgent || e.Date != "2027-01-15" ||
		*e.Score != 4.4 || *e.ReportNumber != 12 || e.Location != "Exampleton" {
		t.Errorf("%+v", e)
	}
	if !strings.Contains(e.Notes, "second") || !strings.Contains(e.Notes, "#"+itoa(a)+" 2027-01-01 user: first") {
		t.Errorf("notes: %q", e.Notes)
	}
	if got.Aliases == nil || len(got.Aliases) != 2 {
		t.Errorf("aliases %v", got.Aliases)
	}
	if f.count(`SELECT count(*) FROM tracker_entries`) != 1 || f.count(`SELECT count(*) FROM mail_links WHERE entry_id = ?`, s) != 1 {
		t.Error("rows or links")
	}
	merged := f.events(s, EventMerged)
	if len(merged) != 1 || !strings.Contains(string(merged[0].Detail), `"removed":[`+itoa(a)+`]`) {
		t.Errorf("%+v", merged)
	}
	if f.count(`SELECT count(*) FROM tracker_events WHERE entry_id = ?`, s) != 4 {
		t.Errorf("events were not re-pointed: %d", f.count(`SELECT count(*) FROM tracker_events WHERE entry_id = ?`, s))
	}
}

func TestDedup_Apply_NoLinkLost(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.LinkedTables = []LinkedTable{{Table: "mail_links", Column: "entry_id"}} })
	f.exec(`CREATE TABLE mail_links (message_id TEXT PRIMARY KEY, entry_id INTEGER NOT NULL REFERENCES tracker_entries (id)) STRICT`)
	a := f.twin("Example Corp", "Software Engineer", "k:1", "")
	b := f.twin("Example Corp", "Software Engineer", "k:2", "")
	f.exec(`INSERT INTO mail_links (message_id, entry_id) VALUES ('only-link', ?)`, b)
	f.exec(`UPDATE tracker_entries SET score = 5.0, score_kind = 'final' WHERE id = ?`, a)
	groups, err := f.tr.DedupApply(f.ctx, ActorUser)
	if err != nil {
		t.Fatal(err)
	}
	if groups[0].Survivor != a {
		t.Fatalf("%+v", groups[0])
	}
	var owner int64
	if err := f.st.Read().QueryRow(`SELECT entry_id FROM mail_links WHERE message_id = 'only-link'`).Scan(&owner); err != nil || owner != a {
		t.Errorf("link points at %d: %v", owner, err)
	}
}

func TestDedup_Apply_KeepsAliasesSoLaterPostingsAttach(t *testing.T) {
	f := newFixture(t)
	f.twin("Example Corp", "Software Engineer", "https://example.com/jobs/4", "")
	f.twin("Example Corp", "Software Engineer", "https://example.com/jobs/9", "")
	if _, err := f.tr.DedupApply(f.ctx, ActorUser); err != nil {
		t.Fatal(err)
	}
	res := f.upsert(report(50, "Another Name", "Another Title", "https://example.com/jobs/9", 4.0))
	if res.Created || res.Entry.ID != 1 || res.Rule != RuleKey {
		t.Errorf("%+v", res)
	}
}

func TestDedup_ConflictingAppliedHistoriesNeedUser(t *testing.T) {
	f := newFixture(t)
	a := f.twin("Example Corp", "Software Engineer", "k:1", "")
	b := f.twin("Example Corp", "Software Engineer", "k:2", "")
	f.exec(`UPDATE tracker_entries SET status = 'applied', applied_at = '2027-01-01', applied_by = 'user' WHERE id = ?`, a)
	f.exec(`UPDATE tracker_entries SET status = 'applied', applied_at = '2027-02-02', applied_by = 'user' WHERE id = ?`, b)
	groups, err := f.tr.DedupApply(f.ctx, ActorUser)
	if err != nil || len(groups) != 1 || !groups[0].NeedsUser || groups[0].Merged || !strings.Contains(groups[0].Reason, "2027-01-01") {
		t.Fatalf("%+v %v", groups, err)
	}
	if f.count(`SELECT count(*) FROM tracker_entries`) != 2 {
		t.Error("a conflicting group was merged")
	}
	// The same applied date on both is one history and merges.
	f.exec(`UPDATE tracker_entries SET applied_at = '2027-01-01'`)
	groups, _ = f.tr.DedupApply(f.ctx, ActorUser)
	if !groups[0].Merged || f.count(`SELECT count(*) FROM tracker_entries`) != 1 {
		t.Errorf("%+v", groups)
	}
}

func TestDedup_OnlyTheUserApplies(t *testing.T) {
	f := newFixture(t)
	if _, err := f.tr.DedupApply(f.ctx, ActorAgent); !errors.Is(err, ErrForbidden) {
		t.Errorf("%v", err)
	}
}

func TestDedup_Apply_RollsBackWholeOperation(t *testing.T) {
	f := newFixture(t)
	a := f.twin("Example Corp", "Software Engineer", "k:1", "")
	f.twin("Example Corp", "Software Engineer", "k:2", "")
	c := f.twin("Other Corp", "Data Analyst", "k:3", "")
	f.twin("Other Corp", "Data Analyst", "k:4", "")
	f.exec(`CREATE TRIGGER no_merge_event BEFORE INSERT ON tracker_events WHEN NEW.kind = 'merged' AND NEW.entry_id = ` + itoa(c) + ` BEGIN SELECT RAISE(ABORT, 'no'); END`)
	if _, err := f.tr.DedupApply(f.ctx, ActorUser); err == nil {
		t.Fatal("expected a failure")
	}
	if n := f.count(`SELECT count(*) FROM tracker_entries`); n != 4 {
		t.Errorf("%d rows after a failed batch (group of row %d was merged)", n, a)
	}
}

func TestDedup_SubmittedRowNeverMergedIntoAnUnsubmittedSurvivor(t *testing.T) {
	f := newFixture(t)
	a := f.twin("Example Corp", "Software Engineer", "k:1", "")
	f.setStatus(a, Applied, ActorUser)
	f.clk.advance(48 * time.Hour)
	b := f.twin("Example Corp", "Software Engineer", "k:2", "") // created later, still evaluated

	plan, err := f.tr.DedupPlan(f.ctx)
	if err != nil || len(plan) != 1 {
		t.Fatalf("%+v %v", plan, err)
	}
	if !plan[0].NeedsUser || !strings.Contains(plan[0].Reason, "submitted") {
		t.Errorf("a group that would drop a submitted row is for the user: %+v", plan[0])
	}
	groups, err := f.tr.DedupApply(f.ctx, ActorUser)
	if err != nil || groups[0].Merged {
		t.Fatalf("%+v %v", groups, err)
	}
	if f.count(`SELECT count(*) FROM tracker_entries WHERE id IN (?, ?)`, a, b) != 2 {
		t.Error("a row was deleted")
	}
	if got := f.get(a).Entry; got.Status != Applied || got.AppliedBy != ByUser {
		t.Errorf("the applied row changed: %+v", got)
	}
}

func TestDedup_SubmittedAndUnsubmittedMergeWhenTheSubmittedStatusIsLatest(t *testing.T) {
	f := newFixture(t)
	a := f.twin("Example Corp", "Software Engineer", "k:1", "")
	b := f.twin("Example Corp", "Software Engineer", "k:2", "")
	f.clk.advance(48 * time.Hour)
	f.setStatus(b, Applied, ActorUser)
	groups, err := f.tr.DedupApply(f.ctx, ActorUser)
	if err != nil || len(groups) != 1 || !groups[0].Merged || groups[0].Survivor != b || groups[0].Status != Applied {
		t.Fatalf("%+v %v", groups, err)
	}
	if f.count(`SELECT count(*) FROM tracker_entries WHERE id = ?`, a) != 0 {
		t.Error("the evaluated twin remains")
	}
}

func TestDedup_Apply_ReferenceFromAnUnregisteredTableFailsLoudly(t *testing.T) {
	f := newFixture(t)
	f.exec(`CREATE TABLE side_links (id INTEGER PRIMARY KEY, entry_id INTEGER NOT NULL REFERENCES tracker_entries (id))`)
	a := f.twin("Example Corp", "Software Engineer", "k:1", "")
	b := f.twin("Example Corp", "Software Engineer", "k:2", "")
	f.exec(`INSERT INTO side_links (entry_id) VALUES (?)`, b)
	if _, err := f.tr.DedupApply(f.ctx, ActorUser); err == nil {
		t.Fatal("a merge that would orphan a link must fail")
	}
	if f.count(`SELECT count(*) FROM tracker_entries WHERE id IN (?, ?)`, a, b) != 2 {
		t.Error("the failed merge left a partial result")
	}
}

func TestDuplicate_AbbreviatedCompanyNamesStayDistinct(t *testing.T) {
	f := newFixture(t)
	first := f.upsert(report(1, "Dr. Pepper", "Software Engineer", "", 4.0)).Entry
	res := f.upsert(report(2, "Dr. Martens", "Software Engineer", "", 4.0))
	if !res.Created || res.Entry.ID == first.ID {
		t.Errorf("a Dr. Martens report attached to the Dr. Pepper row: %+v", res)
	}
}

// Merge folds exactly the group the user saw; a changed or needs-user group is refused.
func TestDedupMerge_OneGroupAsShown(t *testing.T) {
	f := newFixture(t)
	a1, a2 := f.twin("Alpha Corp", "Data Engineer", "k:a1", ""), f.twin("Alpha Corp", "Data Engineer", "k:a2", "")
	b1, b2 := f.twin("Beta Corp", "Data Engineer", "k:b1", ""), f.twin("Beta Corp", "Data Engineer", "k:b2", "")
	f.setStatus(b1, Applied, ActorUser)
	f.exec(`UPDATE tracker_entries SET applied_at = '2027-01-01' WHERE id = ?`, b1)
	f.setStatus(b2, Applied, ActorUser)
	f.exec(`UPDATE tracker_entries SET applied_at = '2027-02-01' WHERE id = ?`, b2)
	if _, _, err := f.tr.DedupMerge(f.ctx, ActorUser, []int64{a1}); !errors.Is(err, store.ErrConflict) {
		t.Errorf("part of a group: %v", err)
	}
	kept, _, err := f.tr.DedupMerge(f.ctx, ActorUser, []int64{a2, a1})
	if err != nil || kept.ID != a1 && kept.ID != a2 {
		t.Fatalf("%+v %v", kept, err)
	}
	if n := f.count(`SELECT count(*) FROM tracker_entries`); n != 3 {
		t.Errorf("%d rows left, want 3: only the named group merges", n)
	}
	if _, _, err := f.tr.DedupMerge(f.ctx, ActorUser, []int64{a1, a2}); !errors.Is(err, store.ErrConflict) {
		t.Errorf("a group merged already: %v", err)
	}
	if _, _, err := f.tr.DedupMerge(f.ctx, ActorUser, []int64{b1, b2}); !errors.Is(err, ErrInvalid) || f.count(`SELECT count(*) FROM tracker_entries`) != 3 {
		t.Errorf("a group left to the user: %v", err)
	}
}

// Undo of a merge puts the database back as it was before it, every column of
// both rows, their events, aliases and links, apart from the kept row's
// version; a kept row changed since, or a merge past the window, stays merged.
func TestDedupMerge_Undo(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.LinkedTables = []LinkedTable{{Table: "side_links", Column: "entry_id"}} })
	f.exec(`CREATE TABLE side_links (id INTEGER PRIMARY KEY, entry_id INTEGER NOT NULL REFERENCES tracker_entries (id))`)
	f.twin("Other Corp", "Analyst", "k:other", "")
	twins := func(role string) []int64 {
		a, b := f.twin("Example Corp", role, "k:a"+role, "Berlin"), f.twin("Example Corp", role, "k:b"+role, "")
		f.exec(`UPDATE tracker_entries SET notes = 'note of ' || id, report_number = id, score = 4.2, score_kind = 'final' WHERE id = ?`, b)
		f.exec(`INSERT INTO tracker_aliases (identity_key, entry_id) VALUES (?, ?)`, "alias:"+itoa(b), b)
		f.exec(`INSERT INTO side_links (entry_id) VALUES (?)`, b)
		return []int64{a, b}
	}
	merge := func(ids []int64) (int64, string) {
		kept, token, err := f.tr.DedupMerge(f.ctx, ActorUser, ids)
		if err != nil || f.count(`SELECT count(*) FROM tracker_entries WHERE id IN (?, ?)`, ids[0], ids[1]) != 1 {
			t.Fatalf("%+v %v", kept, err)
		}
		return kept.ID, token
	}
	state := func(events int) string {
		return f.dump(`SELECT id || '|' || date || '|' || company || '|' || ifnull(score, '') || '|' || ifnull(report_number, '') || '|' || notes || '|' || location || '|' || identity_key || '|' || created_at FROM tracker_entries ORDER BY id`,
			`SELECT id || '|' || entry_id FROM tracker_events WHERE id <= `+itoa(int64(events))+` ORDER BY id`,
			`SELECT identity_key || '|' || entry_id FROM tracker_aliases ORDER BY identity_key`,
			`SELECT id || '|' || entry_id FROM side_links ORDER BY id`)
	}

	ids := twins("Software Engineer")
	events := f.count(`SELECT max(id) FROM tracker_events`)
	before := state(events)
	kept, token := merge(ids)
	if _, err := f.tr.UndoMerge(f.ctx, ActorUser, token); err != nil {
		t.Fatal(err)
	}
	if got := state(events); got != before {
		t.Errorf("after undo:\n%s\nbefore the merge:\n%s", got, before)
	}
	if ev := f.events(kept, EventMerged); len(ev) != 2 || !strings.Contains(string(ev[1].Detail), "undoes") {
		t.Errorf("the undo is not in the kept row's history: %+v", ev)
	}
	if _, err := f.tr.UndoMerge(f.ctx, ActorUser, token); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a second undo: %v", err)
	}

	kept, token = merge(twins("Data Engineer"))
	f.exec(`UPDATE tracker_entries SET version = version + 1 WHERE id = ?`, kept)
	if _, err := f.tr.UndoMerge(f.ctx, ActorUser, token); !errors.Is(err, store.ErrConflict) || f.count(`SELECT count(*) FROM tracker_entries`) != 4 {
		t.Errorf("an undo over a later change: %v", err)
	}
	_, token = merge(twins("Product Manager"))
	f.clk.advance(MergeUndoWindow + time.Second)
	if _, err := f.tr.UndoMerge(f.ctx, ActorUser, token); !errors.Is(err, store.ErrNotFound) || f.count(`SELECT count(*) FROM tracker_entries`) != 5 {
		t.Errorf("an undo past the window: %v", err)
	}
}

// A group the user called not duplicates is left by --apply until its rows
// change; taking the decision back lists it again.
func TestDismissDuplicates(t *testing.T) {
	f := newFixture(t)
	a, b := f.twin("Example Corp", "Software Engineer", "k:1", ""), f.twin("Example Corp", "Software Engineer", "k:2", "")
	if err := f.tr.DismissDuplicates(f.ctx, ActorUser, []int64{a}, false); !errors.Is(err, store.ErrConflict) {
		t.Errorf("part of a group: %v", err)
	}
	if err := f.tr.DismissDuplicates(f.ctx, ActorUser, []int64{b, a}, false); err != nil {
		t.Fatal(err)
	}
	if groups, err := f.tr.DedupApply(f.ctx, ActorUser); err != nil || len(groups) != 1 || !groups[0].Dismissed || groups[0].Merged {
		t.Errorf("apply merged a dismissed group: %+v %v", groups, err)
	}
	if err := f.tr.DismissDuplicates(f.ctx, ActorUser, []int64{a, b}, true); err != nil {
		t.Fatal(err)
	}
	if groups, err := f.tr.DedupPlan(f.ctx); err != nil || len(groups) != 1 || groups[0].Dismissed {
		t.Errorf("the decision was not taken back: %+v %v", groups, err)
	}
	if err := f.tr.DismissDuplicates(f.ctx, ActorUser, []int64{a, b}, false); err != nil {
		t.Fatal(err)
	}
	c := f.twin("Example Corp", "Software Engineer", "k:3", "")
	if groups, err := f.tr.DedupPlan(f.ctx); err != nil || len(groups) != 1 || groups[0].Dismissed || len(groups[0].Members) != 3 {
		t.Errorf("a group that gained row %d stayed dismissed: %+v %v", c, groups, err)
	}
}
