// Clean-room: docs/provenance.md#CR-SCAN-SKELETON

package discovery

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestScan_Registry_AllFiveATS(t *testing.T) {
	e := newEnv(t)
	e.serveGH("example-gh", []obj{ghItem(4001, "Backend Engineer", "Austin, TX")}, nil)
	e.srv.json("/v0/postings/example-lever", []obj{leverPost("5d1c0a9e-0000-4000-8000-000000000001", "Platform Engineer", "Austin, TX", epoch.Add(-24*time.Hour))})
	e.srv.json("/posting-api/job-board/example-ashby", obj{"jobs": []obj{ashbyItem("11111111-aaaa-bbbb-cccc-000000000001", "Data Engineer", "Austin, TX", epoch.Add(-24*time.Hour))}})
	e.srv.json("/v1/companies/examplesr/postings?offset=0", srPage(1, 0, 1, "ExampleSr"))
	e.srv.json("/api/v1/widget/accounts/example-work", obj{"name": "Example Work", "jobs": []obj{workableItem("AB12CD34", "QA Engineer", "Austin", "TX", "United States", "2026-10-09")}})
	e.add(ghBoard("example-gh"), Board{ATS: Lever, Slug: "example-lever", Company: "Example Lever"}, Board{ATS: Ashby, Slug: "example-ashby", Company: "Example Ashby"},
		Board{ATS: SmartRecruiters, Slug: "examplesr"}, Board{ATS: Workable, Slug: "example-work"})
	rec := e.run(ScanRequest{Portals: eng()})
	if rec.Status != RunCompleted {
		t.Fatalf("status %s: %s %s", rec.Status, rec.Reason, rec.Error)
	}
	c := rec.Counts
	if c.Discovered != 5 || c.Inserted != 5 || rec.Boards != 5 {
		t.Fatalf("counts = %+v boards=%d", c, rec.Boards)
	}
	byATS := map[string]InboxRow{}
	for _, r := range e.inbox(InboxQuery{}) {
		byATS[strings.SplitN(r.IdentityKey, ":", 2)[0]] = r
	}
	for _, ats := range ATSNames {
		if _, ok := byATS[ats]; !ok {
			t.Errorf("no inbox row with an %s identity key: %v", ats, byATS)
		}
	}
	gh := byATS[Greenhouse]
	if gh.Company != "Example Gh" || gh.IdentityKey != "greenhouse:example-gh/4001" || gh.Source != "registry" || !contains(gh.Description, "Build things") {
		t.Fatalf("greenhouse row = %+v", gh)
	}
	if c.Enriched != 1 {
		t.Fatalf("enriched = %d", c.Enriched)
	}
}

func TestRun_Counts_ReconcileExactly(t *testing.T) {
	e := newEnv(t)
	jobs := ghIDs(1, 70, func(i int) string { return fmt.Sprintf("Barista %d", i) }, "Austin, TX")
	jobs = append(jobs, ghIDs(71, 100, engineer, "Austin, TX")...)
	e.serveGH("example-co", jobs, nil)
	for i := 71; i <= 80; i++ {
		e.trackedRow("Example Co", engineer(i), fmt.Sprintf("https://job-boards.greenhouse.io/example-co/jobs/%d", i))
	}
	e.add(ghBoard("example-co"))
	rec := e.run(ScanRequest{Portals: eng()})
	c := rec.Counts
	if c.Discovered != 100 || c.Filtered[FilterTitle] != 70 || c.Duplicates[StoreTracker] != 10 || c.Inserted != 20 {
		t.Fatalf("counts = %+v", c)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if rec.Status != RunCompleted {
		t.Fatalf("status %s %s", rec.Status, rec.Error)
	}
	saved, err := e.rp.ScanGet(e.ctx, rec.ID)
	if err != nil || saved.Counts.Inserted != 20 || saved.Finished.IsZero() || saved.DurationMS < 0 {
		t.Fatalf("saved = %+v %v", saved, err)
	}
	if err := (Counts{Discovered: 5, Inserted: 4}).Validate(); err == nil {
		t.Fatal("an unbalanced record must not validate")
	}
}

func TestDedup_CountsByMatchingStore(t *testing.T) {
	e := newEnv(t)
	e.serveGH("example-co", []obj{ghItem(1, "Software Engineer A", "Austin, TX"), ghItem(2, "Software Engineer B", "Austin, TX"), ghItem(3, "Software Engineer C", "Austin, TX")}, nil)
	e.add(ghBoard("example-co"))
	e.trackedRow("Example Co", "Software Engineer A", "https://job-boards.greenhouse.io/example-co/jobs/1")
	en := e.trackedRow("Example Co", "Software Engineer B", "https://job-boards.greenhouse.io/example-co/jobs/2")
	e.archive(en.ID)
	rec := e.run(ScanRequest{Portals: eng()})
	if rec.Counts.Duplicates[StoreTracker] != 1 || rec.Counts.Duplicates[StoreArchive] != 1 || rec.Counts.Inserted != 1 {
		t.Fatalf("counts = %+v", rec.Counts)
	}
}
