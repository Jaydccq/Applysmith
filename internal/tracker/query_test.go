package tracker

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/workspace"
)

func TestList_Query_TokenRanked(t *testing.T) {
	f := newFixture(t)
	f.add(AddRow{Company: "Sample Corp", Role: "Platform Engineer", URL: "https://example.org/1"})
	want := f.add(AddRow{Company: "Example Corp", Role: "Platform Engineer", URL: "https://example.com/2"})
	f.add(AddRow{Company: "Example Corp", Role: "Pastry Chef", URL: "https://example.com/3"})
	f.add(AddRow{Company: "Unrelated Inc", Role: "Welder", URL: "https://example.net/4"})
	p, err := f.tr.List(f.ctx, ListQuery{Query: "example platform engineer"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 3 || p.Entries[0].ID != want.ID {
		t.Errorf("total %d first %d", p.Total, p.Entries[0].ID)
	}
	// One-letter words are ignored.
	p, _ = f.tr.List(f.ctx, ListQuery{Query: "a x welder"})
	if p.Total != 1 {
		t.Errorf("total %d", p.Total)
	}
}

func TestList_DefaultRecentFirstAndSorts(t *testing.T) {
	f := newFixture(t)
	a := f.upsert(report(1, "A Corp", "Engineer", "https://example.com/a", 3.0)).Entry
	f.clk.advance(time.Hour)
	b := f.upsert(report(2, "B Corp", "Engineer", "https://example.com/b", 4.5)).Entry
	f.clk.advance(time.Hour)
	c := f.add(AddRow{Company: "C Corp", Role: "Engineer", URL: "https://example.com/c", Date: "2026-01-01"})
	order := func(sort string) []int64 {
		p, err := f.tr.List(f.ctx, ListQuery{Sort: sort})
		if err != nil {
			t.Fatal(err)
		}
		var ids []int64
		for _, e := range p.Entries {
			ids = append(ids, e.ID)
		}
		return ids
	}
	eq := func(got []int64, want ...int64) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%v != %v", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%v != %v", got, want)
			}
		}
	}
	eq(order(""), c.ID, b.ID, a.ID)
	eq(order(SortScoreDesc), b.ID, a.ID, c.ID)
	eq(order(SortDateDesc), b.ID, a.ID, c.ID)
	eq(order(SortCompanyAsc), a.ID, b.ID, c.ID)
	if _, err := f.tr.List(f.ctx, ListQuery{Sort: "random"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("%v", err)
	}
}

func TestList_FiltersAndPaging(t *testing.T) {
	f := newFixture(t)
	for i := 1; i <= 30; i++ {
		f.upsert(report(int64(i), "Example Corp "+string(rune('A'+i%26)), "Engineer "+string(rune('a'+i%26))+string(rune('a'+i/26)), "https://example.com/"+string(rune('a'+i%26))+string(rune('a'+i/26)), float64(i%5)+1))
	}
	p, err := f.tr.List(f.ctx, ListQuery{})
	if err != nil || p.Page != 1 || p.PageSize != 25 || p.Total != 30 || len(p.Entries) != 25 {
		t.Fatalf("%+v %v", p, err)
	}
	p, _ = f.tr.List(f.ctx, ListQuery{Page: 2})
	if len(p.Entries) != 5 {
		t.Errorf("page 2 holds %d", len(p.Entries))
	}
	p, _ = f.tr.List(f.ctx, ListQuery{MinScore: fp(5.0), PageSize: 100})
	if p.Total != 6 {
		t.Errorf("min score total %d", p.Total)
	}
	p, _ = f.tr.List(f.ctx, ListQuery{Company: "corp b"})
	if p.Total == 0 {
		t.Error("company filter found nothing")
	}
	if _, err := f.tr.List(f.ctx, ListQuery{PageSize: 500}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "100") {
		t.Errorf("%v", err)
	}
	if _, err := f.tr.List(f.ctx, ListQuery{Page: -1}); !errors.Is(err, ErrInvalid) {
		t.Errorf("%v", err)
	}
	if _, err := f.tr.List(f.ctx, ListQuery{Query: strings.Repeat("x", 201)}); !errors.Is(err, ErrInvalid) {
		t.Errorf("%v", err)
	}
	if _, err := f.tr.List(f.ctx, ListQuery{Status: "maybe"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("%v", err)
	}
}

func TestList_ArchivedExcludedByDefault(t *testing.T) {
	f := newFixture(t)
	a := f.add(AddRow{Company: "A Corp", Role: "Engineer", URL: "https://example.com/a"})
	f.add(AddRow{Company: "B Corp", Role: "Engineer", URL: "https://example.com/b"})
	f.exec(`UPDATE tracker_entries SET archived_at = '2027-01-01T00:00:00Z' WHERE id = ?`, a.ID)
	if p, _ := f.tr.List(f.ctx, ListQuery{}); p.Total != 1 || p.Archived != 1 {
		t.Errorf("total %d, archived %d", p.Total, p.Archived)
	}
	if p, _ := f.tr.List(f.ctx, ListQuery{Archived: true}); p.Total != 2 {
		t.Errorf("total %d", p.Total)
	}
	sum, _ := f.tr.Summary(f.ctx)
	if sum[Evaluated] != 1 {
		t.Errorf("the summary counted an archived row: %v", sum)
	}
	// What the default list leaves out is said: the archived rows the same
	// filter matches, and the counts of a list that includes them.
	if p, _ := f.tr.List(f.ctx, ListQuery{Company: "a corp"}); p.Total != 0 || p.Archived != 1 {
		t.Errorf("a filter matching only an archived row: total %d, archived %d", p.Total, p.Archived)
	}
	if all, _ := f.tr.SummaryOf(f.ctx, true); all[Evaluated] != 2 {
		t.Errorf("the counts with archived rows: %v", all)
	}
}

func TestLookup_FoundAndNotFound(t *testing.T) {
	f := newFixture(t)
	e := f.upsert(report(1, "Example Corp", "Engineer", "https://boards.greenhouse.io/example-co/jobs/1001", 4.0)).Entry
	f.setStatus(e.ID, Applied, ActorUser)
	before := f.count(`SELECT (SELECT count(*) FROM tracker_entries) + (SELECT count(*) FROM tracker_events)`)
	res, err := f.tr.Lookup(f.ctx, "https://job-boards.greenhouse.io/embed/job_app?for=example-co&token=1001&utm_source=x")
	if err != nil || !res.Found || res.Entry.ID != e.ID || res.IdentityKey != "greenhouse:example-co/1001" {
		t.Fatalf("%+v %v", res, err)
	}
	if len(res.History) != 2 || res.History[0].Status != Evaluated || res.History[1].Status != Applied || res.History[1].Actor != ActorUser {
		t.Errorf("%+v", res.History)
	}
	miss, err := f.tr.Lookup(f.ctx, "https://example.com/other/job")
	if err != nil || miss.Found {
		t.Errorf("%+v %v", miss, err)
	}
	if after := f.count(`SELECT (SELECT count(*) FROM tracker_entries) + (SELECT count(*) FROM tracker_events)`); after != before {
		t.Error("lookup wrote")
	}
}

func TestLookup_IdentityKeyOnly_AliasAfterReportMove(t *testing.T) {
	f := newFixture(t)
	e := f.upsert(report(1, "Example Corp", "Engineer", "https://example.com/jobs/1", 4.0)).Entry
	f.upsert(report(2, "Example Corp", "Engineer", "https://mirror.example.net/p/9", 4.5))
	if res, _ := f.tr.Lookup(f.ctx, "https://mirror.example.net/p/9"); !res.Found || res.Entry.ID != e.ID {
		t.Errorf("an alias must answer: %+v", res)
	}
	// Same company and role, other posting: never guessed by name.
	if res, err := f.tr.Lookup(f.ctx, "https://example.com/jobs/777"); err != nil || res.Found {
		t.Errorf("lookup guessed by name: %+v %v", res, err)
	}
}

func TestLookup_Validation(t *testing.T) {
	f := newFixture(t)
	for _, bad := range []string{"", "not a url", "ftp://example.com/x", "/relative", "https://example.com/" + strings.Repeat("a", 2100)} {
		if _, err := f.tr.Lookup(f.ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%.30q: %v", bad, err)
		}
	}
}

func TestArchive_Default(t *testing.T) {
	f := newFixture(t)
	rej := f.add(AddRow{Company: "A Corp", Role: "Engineer", URL: "https://example.com/a"})
	app := f.add(AddRow{Company: "B Corp", Role: "Engineer", URL: "https://example.com/b"})
	dis := f.add(AddRow{Company: "C Corp", Role: "Engineer", URL: "https://example.com/c"})
	fresh := f.add(AddRow{Company: "D Corp", Role: "Engineer", URL: "https://example.com/d"})
	f.setStatus(rej.ID, Rejected, ActorUser)
	f.setStatus(app.ID, Applied, ActorUser)
	f.setStatus(dis.ID, Discarded, ActorUser)
	f.clk.advance(45 * 24 * time.Hour)
	f.setStatus(fresh.ID, Rejected, ActorUser)

	dry, err := f.tr.Archive(f.ctx, ActorUser, ArchiveOptions{DryRun: true})
	if err != nil || len(dry) != 2 {
		t.Fatalf("%v %v", dry, err)
	}
	if f.count(`SELECT count(*) FROM tracker_entries WHERE archived_at IS NOT NULL`) != 0 {
		t.Fatal("a dry run archived")
	}
	got, err := f.tr.Archive(f.ctx, ActorUser, ArchiveOptions{})
	if err != nil || len(got) != 2 || got[0].ID != rej.ID || got[1].ID != dis.ID {
		t.Fatalf("%v %v", got, err)
	}
	if f.get(app.ID).Entry.ArchivedAt != "" || f.get(fresh.ID).Entry.ArchivedAt != "" {
		t.Error("an applied or recent row was archived")
	}
	// Events and keys survive.
	d := f.get(rej.ID)
	if d.Entry.ArchivedAt == "" || len(f.events(rej.ID, EventArchived)) != 1 || len(d.Events) < 3 || d.Entry.IdentityKey == "" {
		t.Errorf("%+v", d)
	}
	if again, err := f.tr.Archive(f.ctx, ActorUser, ArchiveOptions{}); err != nil || len(again) != 0 {
		t.Errorf("archived twice: %v %v", again, err)
	}
}

func TestArchive_OlderThanAndSkipped(t *testing.T) {
	f := newFixture(t)
	r := report(1, "A Corp", "Engineer", "https://example.com/a", 2.0)
	r.Outcome = OutcomeSkip
	sk := f.upsert(r).Entry
	rej := f.add(AddRow{Company: "B Corp", Role: "Engineer", URL: "https://example.com/b"})
	f.setStatus(rej.ID, Rejected, ActorUser)
	f.clk.advance(10 * 24 * time.Hour)
	if got, _ := f.tr.Archive(f.ctx, ActorUser, ArchiveOptions{OlderThan: 7 * 24 * time.Hour}); len(got) != 1 || got[0].ID != rej.ID {
		t.Errorf("--older-than: %v", got)
	}
	f.clk.advance(55 * 24 * time.Hour)
	if got, err := f.tr.Archive(f.ctx, ActorUser, ArchiveOptions{}); err != nil || len(got) != 0 {
		t.Errorf("skipped rows are not archived by default: %v %v", got, err)
	}
	got, _ := f.tr.Archive(f.ctx, ActorUser, ArchiveOptions{IncludeSkipped: true})
	if len(got) != 1 || got[0].ID != sk.ID {
		t.Errorf("--include-skipped: %v", got)
	}
	if _, err := f.tr.Archive(f.ctx, ActorUser, ArchiveOptions{OlderThan: -time.Hour}); !errors.Is(err, ErrInvalid) {
		t.Errorf("%v", err)
	}
}

func TestArchive_KeepsIdsEventsAndKeys_UnarchiveRestores(t *testing.T) {
	f := newFixture(t)
	e := f.add(AddRow{Company: "A Corp", Role: "Engineer", URL: "https://example.com/a"})
	f.setStatus(e.ID, Rejected, ActorUser)
	f.clk.advance(40 * 24 * time.Hour)
	f.tr.Archive(f.ctx, ActorUser, ArchiveOptions{})
	// A scanned duplicate of the archived row still finds it.
	res := f.upsert(report(9, "A Corp", "Engineer", "https://example.com/a?utm_source=x", 3.0))
	if res.Created || res.Entry.ID != e.ID {
		t.Fatalf("%+v", res)
	}
	got, err := f.tr.Unarchive(f.ctx, e.ID, ActorUser)
	if err != nil || got.ArchivedAt != "" || len(f.events(e.ID, EventUnarchived)) != 1 {
		t.Errorf("%+v %v", got, err)
	}
}

func TestArchiveRow_OnlyAClosedOrSkippedRowAndUnarchiveRestores(t *testing.T) {
	f := newFixture(t)
	live := f.add(AddRow{Company: "Live Corp", Role: "Engineer", URL: "https://example.com/live", Status: Applied})
	if _, err := f.tr.ArchiveRow(f.ctx, live.ID, ActorUser); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a row in flight was archived: %v", err)
	}
	closed := f.add(AddRow{Company: "Closed Corp", Role: "Engineer", URL: "https://example.com/closed"})
	f.setStatus(closed.ID, Skipped, ActorUser)
	got, err := f.tr.ArchiveRow(f.ctx, closed.ID, ActorUser)
	if err != nil || got.ArchivedAt == "" {
		t.Fatalf("%+v %v", got, err)
	}
	// Pressed twice it stays one archive event.
	if _, err := f.tr.ArchiveRow(f.ctx, closed.ID, ActorUser); err != nil || len(f.events(closed.ID, EventArchived)) != 1 {
		t.Errorf("second archive: %v, %d events", err, len(f.events(closed.ID, EventArchived)))
	}
	page, err := f.tr.List(f.ctx, ListQuery{})
	if err != nil || page.Total != 1 || page.Entries[0].ID != live.ID {
		t.Errorf("the default list holds %+v %v", page, err)
	}
	if back, err := f.tr.Unarchive(f.ctx, closed.ID, ActorUser); err != nil || back.ArchivedAt != "" {
		t.Errorf("%+v %v", back, err)
	}
}

func TestArchive_RefusedAutomatedAttemptDoesNotKeepAClosedRowOpen(t *testing.T) {
	f := newFixture(t)
	rej := f.add(AddRow{Company: "A Corp", Role: "Engineer", URL: "https://example.com/a"})
	f.setStatus(rej.ID, Rejected, ActorUser)
	f.clk.advance(40 * 24 * time.Hour)
	// The mail integration tries to reopen a closed row and is refused.
	_, err := f.tr.SetStatus(f.ctx, StatusChange{ID: rej.ID, To: Applied, Actor: ActorGmail})
	if !errors.Is(err, ErrTransitionRefused) {
		t.Fatalf("want a refusal, got %v", err)
	}
	got, err := f.tr.Archive(f.ctx, ActorUser, ArchiveOptions{})
	if err != nil || len(got) != 1 || got[0].ID != rej.ID {
		t.Errorf("the refusal reset the row's age: %v %v", got, err)
	}
}

func TestList_PageAndTotalComeFromOneSnapshot(t *testing.T) {
	f := newFixture(t)
	for i := range 3 {
		f.add(AddRow{Company: fmt.Sprintf("Seed %d Corp", i), Role: "Engineer", URL: fmt.Sprintf("https://example.com/seed/%d", i)})
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		// A bounded writer: readers hold snapshots, so an unbounded one grows
		// the WAL without limit on a slow machine.
		for i := 0; i < 2000; i++ {
			select {
			case <-stop:
				return
			default:
			}
			time.Sleep(200 * time.Microsecond)
			now := f.clk.now().UTC().Format(time.RFC3339)
			_ = f.st.Write(f.ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO tracker_entries (date, company, role, status, identity_key, created_at, updated_at) VALUES ('2027-03-10', 'Load Corp', 'Engineer', 'evaluated', ?, ?, ?)`,
					fmt.Sprintf("load:%d", i), now, now)
				return err
			})
		}
	}()
	defer func() { close(stop); <-done }()
	for range 200 {
		p, err := f.tr.List(f.ctx, ListQuery{PageSize: 100})
		if err != nil {
			t.Fatal(err)
		}
		if want := min(100, p.Total); len(p.Entries) != want {
			t.Fatalf("page holds %d rows beside a total of %d: the two reads saw different data", len(p.Entries), p.Total)
		}
	}
}

// The scheduled sweep archives under the system actor, which exists for that
// alone: it cannot change a status or any other part of a row.
func TestArchive_SystemActorSweepsAndDoesNothingElse(t *testing.T) {
	f := newFixture(t)
	rej := f.add(AddRow{Company: "A Corp", Role: "Engineer", URL: "https://example.com/a"})
	app := f.add(AddRow{Company: "B Corp", Role: "Engineer", URL: "https://example.com/b"})
	f.setStatus(rej.ID, Rejected, ActorUser)
	f.setStatus(app.ID, Applied, ActorUser)
	f.clk.advance(45 * 24 * time.Hour)

	got, err := f.tr.Archive(f.ctx, ActorSystem, ArchiveOptions{})
	if err != nil || len(got) != 1 || got[0].ID != rej.ID {
		t.Fatalf("%v %v", got, err)
	}
	evs := f.events(rej.ID, EventArchived)
	if len(evs) != 1 || evs[0].Actor != ActorSystem {
		t.Fatalf("archived events %+v", evs)
	}
	if f.get(app.ID).Entry.ArchivedAt != "" {
		t.Error("an applied row was archived")
	}
	if _, err := f.tr.Unarchive(f.ctx, rej.ID, ActorSystem); err != nil {
		t.Errorf("unarchive as system: %v", err)
	}

	if _, err := f.tr.SetStatus(f.ctx, StatusChange{ID: app.ID, To: Interview, Actor: ActorSystem}); err == nil {
		t.Error("the system actor changed a status")
	}
	if d := DecideNew(ActorSystem, Evaluated); d.Verdict != Refuse {
		t.Errorf("the system actor may create a row: %+v", d)
	}
	if _, err := f.tr.RecordFollowup(f.ctx, app.ID, ActorSystem, 0, workspace.Followup{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("the system actor recorded a follow-up: %v", err)
	}
	for _, a := range Actors() {
		if a == ActorSystem {
			t.Error("the system actor is listed among the status actors")
		}
	}
}

// filterFixture holds seven rows; the dates below are set against the fixture clock's today.
// Row ids follow the order below.
func filterFixture(t *testing.T) *fixture {
	f := newFixture(t)
	for _, r := range []AddRow{
		{Company: "Acme", Role: "Zeta Engineer", Status: Applied, AppliedAt: "2027-03-09", Source: "linkedin", URL: "https://example.com/1"},
		{Company: "Bolt", Role: "Alpha Engineer", Status: Responded, AppliedAt: "2027-03-03", Source: "LinkedIn Easy Apply", URL: "https://example.com/2"},
		{Company: "Cargo", Role: "Mid Engineer", Status: Interview, AppliedAt: "2027-02-20", Source: "greenhouse", URL: "https://example.com/3"},
		{Company: "Delta", Role: "alpha designer", Status: Rejected, AppliedAt: "2027-01-01", Source: "manual", URL: "https://example.com/4"},
		{Company: "Echo", Role: "Beta", URL: "https://example.com/5"},
		{Company: "Fox", Role: "beta", Status: Applied, AppliedAt: "2027-03-10", Source: "manual", URL: "https://example.com/6"},
		{Company: "Gamma", Role: "Delta Dev", Status: Applied, AppliedAt: "2027-03-02", Source: "manual", URL: "https://example.com/7"},
	} {
		f.add(r)
	}
	f.exec(`UPDATE tracker_entries SET updated_at = '2027-03-10T12:00:00Z' WHERE id = 1`)
	return f
}

func listIDs(t *testing.T, f *fixture, q ListQuery) []int64 {
	t.Helper()
	p, err := f.tr.List(f.ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	ids := []int64{}
	for _, e := range p.Entries {
		ids = append(ids, e.ID)
	}
	return ids
}

func TestList_MultiStatusAppliedWindowAndSourceFilters(t *testing.T) {
	f := filterFixture(t)
	inFlight := []Status{Applied, Responded, Interview}
	cases := []struct {
		name string
		q    ListQuery
		want []int64
	}{
		{"several statuses", ListQuery{Statuses: inFlight}, []int64{1, 2, 3, 6, 7}},
		{"window of 7 days includes the 7th day back and today", ListQuery{AppliedWithinDays: 7}, []int64{1, 2, 6}},
		{"no window keeps the row with no application date", ListQuery{}, []int64{1, 2, 3, 4, 5, 6, 7}},
		{"source is a case-insensitive substring", ListQuery{Source: "linkedin"}, []int64{1, 2}},
		{"filters combine", ListQuery{Statuses: []Status{Applied}, AppliedWithinDays: 7, Source: "LINKEDIN"}, []int64{1}},
	}
	for _, c := range cases {
		c.q.Sort = SortCompanyAsc
		if got := listIDs(t, f, c.q); !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	// The export applies the same filter.
	var buf strings.Builder
	if err := f.tr.Export(f.ctx, &buf, FormatCSV, ListQuery{Statuses: inFlight, AppliedWithinDays: 7}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(buf.String(), "\n"); got != 4 { // header and rows 1, 2, 6
		t.Errorf("export holds %d lines:\n%s", got, buf.String())
	}
}

func TestList_FilterInputsAreValidated(t *testing.T) {
	f := filterFixture(t)
	for name, q := range map[string]ListQuery{
		"unknown status in the list": {Statuses: []Status{Applied, "maybe"}},
		"status in another case":     {Status: "Applied"},
		"negative window":            {AppliedWithinDays: -1},
		"window past the cap":        {AppliedWithinDays: 4000},
		"long source":                {Source: strings.Repeat("x", 201)},
		"unknown sort":               {Sort: "role_desc"},
		"reached an unknown status":  {Reached: []Status{"maybe"}, ReachedSince: "2026-10-01"},
		"reached since no date":      {Reached: []Status{Applied}, ReachedSince: "last week"},
		"since without reached":      {ReachedSince: "2026-10-01"},
		"moved by an unknown actor":  {MovedBy: "someone", ReachedSince: "2026-10-01"},
		"moved by with no since":     {MovedBy: ActorGmail},
	} {
		if _, err := f.tr.List(f.ctx, q); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	got, err := ParseStatuses("applied, interview")
	if err != nil || !slices.Equal(got, []Status{Applied, Interview}) {
		t.Errorf("ParseStatuses = %v, %v", got, err)
	}
	if none, err := ParseStatuses(" "); err != nil || none != nil {
		t.Errorf("empty list = %v, %v", none, err)
	}
	if _, err := ParseStatuses("applied,nope"); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown name: %v", err)
	}
}
