package tracker

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"applysmith/internal/identity"
)

func TestTracker_AgentMarkInAllViews(t *testing.T) {
	f := newFixture(t)
	const url = "https://jobs.example.com/agent-applied"
	e := f.upsert(report(1, "Example Corp", "Engineer", url, 4.2)).Entry
	f.setStatus(e.ID, Applied, ActorApplyChain)
	f.upsert(report(2, "User Corp", "Engineer", "https://jobs.example.com/user-applied", 4.0))
	user := f.add(AddRow{Company: "Manual Corp", Role: "Engineer", URL: "https://jobs.example.com/manual", Status: Applied})

	page, err := f.tr.List(f.ctx, ListQuery{Status: Applied})
	if err != nil || len(page.Entries) != 2 {
		t.Fatalf("list: %+v %v", page, err)
	}
	for _, row := range page.Entries {
		want := ByUser
		if row.ID == e.ID {
			want = ByAgent
		}
		if row.AppliedBy != want || (want == ByAgent) != strings.HasSuffix(row.DisplayStatus(), "(agent)") {
			t.Errorf("list row %d: applied_by %q, shown as %q", row.ID, row.AppliedBy, row.DisplayStatus())
		}
	}
	if d := f.get(e.ID); d.Entry.AppliedBy != ByAgent || d.Entry.DisplayStatus() != "applied (agent)" {
		t.Errorf("detail: %+v", d.Entry)
	}
	if look, err := f.tr.Lookup(f.ctx, url); err != nil || !look.Found || look.Entry.DisplayStatus() != "applied (agent)" {
		t.Errorf("lookup: %+v %v", look, err)
	}
	if d := f.get(user.ID); strings.Contains(d.Entry.DisplayStatus(), "agent") {
		t.Errorf("a user row carries the agent mark: %q", d.Entry.DisplayStatus())
	}
	if !strings.Contains(f.export(FormatMarkdown, ListQuery{}), "| applied (agent) |") {
		t.Error("the markdown export drops the mark")
	}
	recs, _ := csv.NewReader(strings.NewReader(f.export(FormatCSV, ListQuery{}))).ReadAll()
	var marked int
	for _, r := range recs[1:] {
		if r[9] == "agent" {
			marked++
		}
	}
	if marked != 1 {
		t.Errorf("the CSV marks %d rows, want 1", marked)
	}
}

func TestTracker_IdentityKeyRecomputedOnRename(t *testing.T) {
	f := newFixture(t)
	in := identity.Posting{Company: "Example Corp", Role: "Engineer", URL: "https://jobs.example.com/p/1"}
	e := f.upsert(report(1, in.Company, in.Role, in.URL, 4.0)).Entry
	if want := in.IdentityKey(); e.IdentityKey != want || want == "" {
		t.Fatalf("the key is %q, want the shared definition's %q", e.IdentityKey, want)
	}
	// The stored key cannot go stale: a statement that assigns company or role
	// assigns identity_key in the same UPDATE (refreshNames, inside the
	// report's transaction). (A merge may fill an empty URL from a removed
	// twin, whose own key then becomes an alias of the survivor.)
	files, _ := filepath.Glob("*.go")
	update := regexp.MustCompile(`(?is)UPDATE\s+tracker_entries\s+SET([^` + "`" + `"]*)`)
	names := regexp.MustCompile(`(?i)\b(company|role)\s*=`)
	keyed := regexp.MustCompile(`(?i)\bidentity_key\s*=`)
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range update.FindAllSubmatch(src, -1) {
			if names.Match(m[1]) && !keyed.Match(m[1]) {
				t.Errorf("%s assigns company or role without identity_key; recompute the key there", name)
			}
		}
	}
	// A blank half never reaches the table, so a row never holds a key built
	// from nothing.
	if _, err := f.tr.Add(f.ctx, AddRow{Company: " ", Role: "Engineer", URL: "https://jobs.example.com/p/2"}); err == nil {
		t.Error("a row without a company was stored")
	}
}

func TestTracker_ReEvaluationCorrectsNamesAndKeepsHistory(t *testing.T) {
	f := newFixture(t)
	const url = "https://nvidia.wd5.myworkdayjobs.com/NVIDIAExternalCareerSite/job/US-CA-Santa-Clara/Software-Engineer_JR2000001"
	first := f.upsert(report(7, "NVIDIA", "CAREERS AT NVIDIA", url, 3.0)).Entry
	if _, err := f.tr.SetStatus(f.ctx, StatusChange{ID: first.ID, To: Applied, Actor: ActorUser}); err != nil {
		t.Fatal(err)
	}
	res := f.upsert(report(9, "NVIDIA", "Senior Software Engineer, Platform", url, 3.4))
	e := res.Entry
	if res.Created || e.ID != first.ID || e.Role != "Senior Software Engineer, Platform" || e.Company != "NVIDIA" {
		t.Fatalf("the row kept its poor read: %+v", e)
	}
	if e.IdentityKey != first.IdentityKey || e.Status != Applied || e.ReportNumber == nil || *e.ReportNumber != 9 || !strings.Contains(e.Notes, "Renamed from NVIDIA / CAREERS AT NVIDIA") {
		t.Errorf("key, status, report or note wrong: %+v", e)
	}
	if n := f.count(`SELECT count(*) FROM tracker_entries`); n != 1 {
		t.Errorf("%d rows, want 1", n)
	}
	// An older report, a placeholder and a later report that does not become the
	// row's own never rename it.
	f.upsert(report(8, "Nvidia Corp", "Other Name", url, 2.0))
	f.upsert(report(10, "Unknown company", "Unknown role", url, 2.0))
	f.upsert(report(11, "NVIDIA", "Like No Place You've Ever Worked", url, 2.0)) // a later, lower-scored reading
	if got := f.get(first.ID).Entry; got.Role != e.Role || got.Company != "NVIDIA" {
		t.Errorf("renamed by a report that must not: %+v", got)
	}
}

func TestTracker_PlaceholderRowIsNamedByTheReEvaluation(t *testing.T) {
	f := newFixture(t)
	first := f.upsert(report(8, "Unknown company", "Unknown role", "", 2.0)).Entry
	r := report(9, "Example Corp", "Backend Engineer", "", 3.0)
	r.EntryID = first.ID
	e := f.upsert(r).Entry
	if e.ID != first.ID || e.Company != "Example Corp" || e.Role != "Backend Engineer" {
		t.Fatalf("got %+v", e)
	}
	want := identity.Posting{Company: "Example Corp", Role: "Backend Engineer"}.IdentityKey()
	if want == "" || (e.IdentityKey != first.IdentityKey && e.IdentityKey != want) {
		t.Errorf("key %q", e.IdentityKey)
	}
}

// A rename whose new names give the row the key another row holds makes them
// one application: the rows merge and both histories stay.
func TestTracker_RenameOntoAnotherRowsKeyMergesTheRows(t *testing.T) {
	f := newFixture(t)
	other := f.upsert(report(3, "Example Corp", "Backend Engineer", "", 3.5)).Entry
	draft := f.upsert(report(5, "Example Corp", "Platform Reliability Specialist", "", 3.0)).Entry
	if draft.ID == other.ID || f.count(`SELECT count(*) FROM tracker_entries`) != 2 {
		t.Fatalf("setup: the rows are one already")
	}
	r := report(9, "Example Corp", "Backend Engineer", "", 3.2)
	r.EntryID = draft.ID
	e := f.upsert(r).Entry
	if n := f.count(`SELECT count(*) FROM tracker_entries`); n != 1 {
		t.Fatalf("%d rows, want the two merged", n)
	}
	if e.Company != "Example Corp" || e.Role != "Backend Engineer" || e.IdentityKey != other.IdentityKey && e.IdentityKey != draft.IdentityKey {
		t.Fatalf("got %+v", e)
	}
	if n := f.count(`SELECT count(*) FROM tracker_events WHERE kind = 'merged'`); n != 1 {
		t.Errorf("%d merge events, want 1", n)
	}
	// Both rows' histories are on the survivor.
	if n := f.count(`SELECT count(*) FROM tracker_events WHERE entry_id = ? AND kind = 'created'`, e.ID); n != 2 {
		t.Errorf("%d created events on the survivor, want both rows' histories (2)", n)
	}
	for _, k := range []string{other.IdentityKey, draft.IdentityKey} {
		if k != e.IdentityKey && f.count(`SELECT count(*) FROM tracker_aliases WHERE identity_key = ? AND entry_id = ?`, k, e.ID) != 1 {
			t.Errorf("key %q of the folded row is not an alias of the survivor", k)
		}
	}
}
