package discovery

import (
	"testing"
	"time"

	"applysmith/internal/workspace"
)

func no() *bool { b := false; return &b }

func TestTracked_PerCompanyTitleFilterReplacesGlobal(t *testing.T) {
	e := newEnv(t)
	e.serveGH("override-co", []obj{ghItem(1, "Barista", "Austin, TX"), ghItem(2, "Software Engineer", "Austin, TX")}, nil)
	p := portals([]string{"engineer"}, nil)
	p.Companies = []workspace.Company{{Name: "Override Co", ATS: "greenhouse", Slug: "override-co",
		Filters: workspace.CompanyFilters{Title: &workspace.TitleFilter{Positive: []string{"barista"}}}}}
	rec := e.run(ScanRequest{Source: "tracked", Portals: p})
	rows := e.inbox(InboxQuery{})
	if rec.Status != RunCompleted || len(rows) != 1 || rows[0].Role != "Barista" {
		t.Fatalf("rec %s %s rows %+v", rec.Status, rec.Error, rows)
	}
	if rec.Counts.Filtered[FilterTitle] != 1 {
		t.Fatalf("counts = %+v", rec.Counts)
	}
}

func TestTracked_ReadsBoardNotInRegistryAndAddsIt(t *testing.T) {
	e := newEnv(t)
	e.srv.json("/posting-api/job-board/example-corp", obj{"jobs": []obj{ashbyItem("11111111-aaaa-bbbb-cccc-000000000001", "Software Engineer", "Austin, TX", epoch.Add(-24*time.Hour))}})
	p := eng()
	p.Companies = []workspace.Company{{Name: "Example Corp", ATS: "ashby", Slug: "Example-Corp"}}
	rec := e.run(ScanRequest{Source: "tracked", Portals: p})
	if rec.Counts.Inserted != 1 {
		t.Fatalf("counts = %+v", rec.Counts)
	}
	row := e.registry(Board{ATS: Ashby, Slug: "example-corp"})
	if row.Source != "tracked" || row.Company != "Example Corp" {
		t.Fatalf("row = %+v", row)
	}
	if got := e.inbox(InboxQuery{})[0].Company; got != "Example Corp" {
		t.Fatalf("company = %q, the entry's name labels boards that carry none", got)
	}
}

func TestTracked_DisabledEntryNeverRead(t *testing.T) {
	e := newEnv(t)
	e.serveGH("off-co", []obj{ghItem(1, "Software Engineer", "Austin, TX")}, nil)
	p := eng()
	p.Companies = []workspace.Company{{Name: "Off Co", ATS: "greenhouse", Slug: "off-co", Enabled: no()}}
	if rec := e.run(ScanRequest{Source: "tracked", Portals: p}); rec.Status != RunSkipped {
		t.Fatalf("status = %s", rec.Status)
	}
	e.add(ghBoard("other-co"))
	e.srv.set("/v1/boards/other-co/jobs", route{status: 404})
	e.run(ScanRequest{Portals: p})
	if e.srv.hitCount("/v1/boards/off-co/jobs") != 0 {
		t.Fatal("a disabled entry was read")
	}
}

func TestTracked_FailuresReportedNeverStruck(t *testing.T) {
	e := newEnv(t)
	e.srv.set("/v1/boards/gone-co/jobs", route{status: 404})
	p := eng()
	p.Companies = []workspace.Company{{Name: "Gone Co", ATS: "greenhouse", Slug: "gone-co"}}
	var rec Record
	for i := 0; i < 4; i++ {
		rec = e.run(ScanRequest{Source: "tracked", Portals: p})
	}
	row := e.registry(ghBoard("gone-co"))
	if row.Strikes != 0 || row.Status != StatusActive || rec.Counts.BoardErrors != 1 || len(rec.Errors) != 1 {
		t.Fatalf("row %+v rec %+v", row, rec)
	}
}

func TestTracked_CareersURLResolvesToBoard(t *testing.T) {
	e := newEnv(t)
	e.srv.json("/v0/postings/example-lever", []obj{leverPost("5d1c0a9e-0000-4000-8000-000000000001", "Software Engineer", "Austin, TX", epoch)})
	p := eng()
	p.Companies = []workspace.Company{
		{Name: "Example Lever", CareersURL: "https://jobs.lever.co/example-lever"},
		{Name: "Unsupported Co", CareersURL: "https://careers.example.com/jobs"},
	}
	rec := e.run(ScanRequest{Source: "tracked", Portals: p})
	if rec.Counts.Inserted != 1 || rec.Counts.BoardErrors != 1 {
		t.Fatalf("counts = %+v errors %v", rec.Counts, rec.Errors)
	}
}

func TestScan_SkippedRuns(t *testing.T) {
	e := newEnv(t)
	// An empty registry is skipped with the code each front end words: the
	// command line names its command, the dashboard links its download.
	rec := e.run(ScanRequest{Portals: eng()})
	if rec.Status != RunSkipped || rec.Reason != ReasonRegistryEmpty {
		t.Fatalf("empty registry: %s %q", rec.Status, rec.Reason)
	}
	e.add(ghBoard("example-co"))
	p := eng()
	p.Registry.Enabled = no()
	if rec := e.run(ScanRequest{Portals: p}); rec.Status != RunSkipped {
		t.Fatalf("registry disabled: %s", rec.Status)
	}
	if rec, err := e.sc.Run(e.ctx, ScanRequest{Source: "linkedin"}); err != nil || rec.Status != RunFailed {
		t.Fatalf("a browser source with no scan window fails its run: %v %s", err, rec.Status)
	}
	if _, err := e.sc.Run(e.ctx, ScanRequest{Source: "bogus"}); err == nil {
		t.Fatal("unknown source")
	}
}
