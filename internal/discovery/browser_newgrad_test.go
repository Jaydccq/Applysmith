package discovery

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"applysmith/internal/workspace"
)

func ngJob(id, title, company string) rawCard {
	return rawCard{ID: id, Title: title, Company: company, Location: "Austin, TX", Salary: "$100000-$120000/yr", Extra: "On Site", Datetime: iso(epoch.Add(-time.Hour))}
}

// ngRoute serves the list page as extracted rows and the Jobright pages.
func ngRoute(rows []rawCard, detail func(rawURL string) fakePage) func(string) fakePage {
	return func(raw string) fakePage {
		if strings.Contains(raw, "/jobs/info/") {
			if detail != nil {
				return detail(raw)
			}
			return fakePage{detail: detailRead{Desc: postingText()}}
		}
		return cardsPage(rows...)
	}
}

func newGradPortals(t *testing.T, q map[string]any) workspace.Portals {
	return portalsWith(t, eng(), "newgrad", q)
}

func TestNewGrad_ListIsTheMinisitePageOfCountryAndCategory(t *testing.T) {
	fb := newFakeBrowser(t, ngRoute([]rawCard{ngJob("j1", "Backend Engineer", "Acme"), ngJob("j2", "Data Engineer", "Beta")}, nil))
	e := browserEnv(t, fb)
	rec := e.run(ScanRequest{Source: "newgrad", Portals: newGradPortals(t, map[string]any{"country": "CA", "category": "data_analysis"}), TopN: new(int)})
	if rec.Status != RunCompleted || rec.Counts.Inserted != 2 {
		t.Fatalf("status %s counts %+v error %q", rec.Status, rec.Counts, rec.Error)
	}
	if got := fb.visited()[0]; got != "https://jobright.ai/minisites-jobs/newgrad/ca/data_analysis" {
		t.Fatalf("list page = %s", got)
	}
	rows := e.inbox(InboxQuery{})
	if len(rows) != 2 || !strings.HasSuffix(rows[0].URL, "/jobs/info/j1") && !strings.HasSuffix(rows[1].URL, "/jobs/info/j1") {
		t.Fatalf("rows = %+v", rows)
	}
	fb = newFakeBrowser(t, ngRoute([]rawCard{ngJob("j1", "Backend Engineer", "Acme")}, nil))
	browserEnv(t, fb).run(ScanRequest{Source: "newgrad", Portals: newGradPortals(t, nil), TopN: new(int)})
	if got := fb.visited()[0]; got != "https://jobright.ai/minisites-jobs/newgrad/us/swe" {
		t.Fatalf("default list page = %s", got)
	}
}

func TestNewGrad_URLSearchRefusedWithTheReplacement(t *testing.T) {
	fb := newFakeBrowser(t, ngRoute([]rawCard{ngJob("j1", "Backend Engineer", "Acme")}, nil))
	rec := browserEnv(t, fb).run(ScanRequest{Source: "newgrad", Portals: newGradPortals(t, map[string]any{"url": "https://www.newgrad-jobs.com/?Category=swe&Date=24h"}), TopN: new(int)})
	if rec.Status != RunFailed || !strings.Contains(rec.Error, "country and a category") || len(fb.visited()) != 0 {
		t.Fatalf("status %s error %q visited %v", rec.Status, rec.Error, fb.visited())
	}
}

func TestNewGrad_DetailTimeoutRetriesOnceThenDegrades(t *testing.T) {
	fb := newFakeBrowser(t, ngRoute([]rawCard{ngJob("j1", "Backend Engineer", "Acme")}, func(string) fakePage { return fakePage{block: true} }))
	e := browserEnv(t, fb)
	rec := e.run(ScanRequest{Source: "newgrad", Portals: newGradPortals(t, nil), TopN: new(int)})
	if rec.Status != RunCompleted || rec.Counts.EnrichmentDegraded != 1 || rec.Counts.Inserted != 1 {
		t.Fatalf("status %s counts %+v", rec.Status, rec.Counts)
	}
	rows := e.inbox(InboxQuery{})
	if len(rows) != 1 || !strings.Contains(strings.Join(rows[0].Flags, ","), FlagDegraded) {
		t.Fatalf("rows = %+v", rows)
	}
	gotos := fb.deadlines
	if len(gotos) == 3 {
		gotos = gotos[1:] // the first is the list page
	}
	if len(gotos) != 2 || gotos[0] > 40*time.Millisecond || gotos[1] <= gotos[0] || gotos[1] > 60*time.Millisecond {
		t.Fatalf("navigation limits = %v, want one try inside the short limit and one at the longer", gotos)
	}
	if rec.Detail["retries"] != 1 {
		t.Fatalf("detail = %v", rec.Detail)
	}
}

// jobrightNext is the shape of a live Jobright posting page: the facts sit on
// props.pageProps.dataSource.jobResult, and the work-authorisation labels are
// booleans (isH1bSponsor, isCitizenOnly, isClearanceRequired).
func jobrightNext(apply string, flags map[string]any) json.RawMessage {
	job := map[string]any{"applyLink": apply, "jobSeniority": "Entry Level", "salaryDesc": "$100K/yr", "publishTime": "2026-10-17 03:05:51",
		"jobSummary": postingText(), "skills": []any{map[string]any{"skill": "Go"}, map[string]any{"skill": "SQL"}}}
	for k, v := range flags {
		job[k] = v
	}
	m := map[string]any{"props": map[string]any{"pageProps": map[string]any{"dataSource": map[string]any{"jobResult": job, "companyResult": map[string]any{"isH1bSponsor": false}}}}}
	b, _ := json.Marshal(m)
	return b
}

// The facts are the posting's own (jobResult), whatever else on the page uses
// the same key and whatever order the document's objects are walked in.
func TestJobrightFacts_ThePostingsOwnFieldsWinOverAnotherObjectsOfTheSameName(t *testing.T) {
	next := json.RawMessage(`{"props":{"pageProps":{"dataSource":{
		"companyResult":{"description":"A company blurb.","salary":"n/a","originalUrl":"https://company.example.com/"},
		"jobResult":{"description":"The job text.","salary":"$100K/yr","originalUrl":"https://careers.example.com/jobs/1"},
		"similarJobs":{"first":{"description":"Another job.","salary":"$1/yr","originalUrl":"https://other.example.com/jobs/9"}}}}}}`)
	for i := 0; i < 50; i++ {
		if f := jrFactsOf(next); f.Summary != "The job text." || f.Salary != "$100K/yr" || f.Apply != "https://careers.example.com/jobs/1" {
			t.Fatalf("read %d: facts = %+v", i, f)
		}
	}
}

func TestNewGrad_EmployerPageConfirmsSponsorshipWording(t *testing.T) {
	visited := false
	fb := newFakeBrowser(t, ngRoute([]rawCard{ngJob("j1", "Backend Engineer", "Acme")}, func(string) fakePage {
		return fakePage{detail: detailRead{Next: jobrightNext("https://careers.example.com/jobs/1", map[string]any{"isH1bSponsor": true, "isClearanceRequired": true})}}
	}))
	base := fb.route
	fb.route = func(raw string) fakePage {
		if strings.Contains(raw, "careers.example.com") {
			visited = true
		}
		return base(raw)
	}
	e := browserEnv(t, fb)
	e.run(ScanRequest{Source: "newgrad", Portals: newGradPortals(t, nil), TopN: new(int)})
	rows := e.inbox(InboxQuery{})
	if visited || len(rows) != 1 || rows[0].URL != "https://careers.example.com/jobs/1" {
		t.Fatalf("employer page read: %v, rows %+v", visited, rows)
	}
	for _, want := range []string{"Skills: Go, SQL", "Seniority: Entry Level", "Salary: $100K/yr", "Sponsorship: H1B Sponsor Likely", "Security clearance required"} {
		if !strings.Contains(rows[0].Description, want) {
			t.Errorf("description lacks %q", want)
		}
	}
	if !rows[0].ClearanceRequired || !rows[0].PostedAt.Equal(time.Date(2026, 10, 17, 3, 5, 51, 0, time.UTC)) {
		t.Errorf("the page's clearance flag and exact publication time are kept: clearance %t posted %s", rows[0].ClearanceRequired, rows[0].PostedAt)
	}
}

func jobrightPortals(t *testing.T) workspace.Portals { return eng() }

func recommendRoute(cards []rawCard, detail fakePage) func(string) fakePage {
	return func(raw string) fakePage {
		if strings.Contains(raw, "/jobs/info/") {
			return detail
		}
		return cardsPage(cards...)
	}
}

func TestJobright_SignedOutFeedEndsNeedsSignIn(t *testing.T) {
	for _, pg := range []fakePage{
		{nav: Nav{URL: "https://jobright.ai/"}},
		{nav: Nav{URL: "https://jobright.ai/jobs/recommend"}, probe: pageProbe{Text: "Join now to get personalised matches. Sign up free."}},
	} {
		fb := newFakeBrowser(t, func(string) fakePage { return pg })
		rec := browserEnv(t, fb).run(ScanRequest{Source: "jobright", Portals: jobrightPortals(t)})
		if rec.Status != RunNeedsSignIn || rec.Counts.Inserted != 0 {
			t.Fatalf("status %s", rec.Status)
		}
	}
}

func TestJobright_RecommendFeedRowsTaggedAndDeduped(t *testing.T) {
	var cards []rawCard
	for i := 1; i <= 3; i++ {
		cards = append(cards, rawCard{ID: fmt.Sprintf("r%d", i), Title: fmt.Sprintf("Backend Engineer %d", i), Company: fmt.Sprintf("Company %d", i), Age: "3 hours ago"})
	}
	fb := newFakeBrowser(t, recommendRoute(cards, fakePage{detail: detailRead{Desc: postingText()}}))
	e := browserEnv(t, fb)
	rec := e.run(ScanRequest{Source: "jobright", Portals: jobrightPortals(t), TopN: new(int)})
	rows := e.inbox(InboxQuery{})
	if rec.Counts.Inserted != 3 || len(rows) != 3 || rows[0].Source != "jobright-recommend" {
		t.Fatalf("counts %+v rows %+v", rec.Counts, rows)
	}
	again := e.run(ScanRequest{Source: "jobright", Portals: jobrightPortals(t), TopN: new(int)})
	if again.Counts.Inserted != 0 || sumMap(again.Counts.Duplicates) != 3 {
		t.Fatalf("second run: %+v", again.Counts)
	}
}

func TestJobright_OverlayClosedOnlyByExplicitCloseControl(t *testing.T) {
	open := true
	card := rawCard{ID: "r1", Title: "Backend Engineer", Company: "Acme", Age: "3 hours ago"}
	mk := func(close string) *fakeBrowser {
		open = true
		return newFakeBrowser(t, func(raw string) fakePage {
			if strings.Contains(raw, "/jobs/info/") {
				return fakePage{detail: detailRead{Desc: postingText()}}
			}
			p := cardsPage(card)
			p.eval = map[string]any{"overlay": func() any { return overlayState{Open: open, Close: close} }, "click": func() any { open = false; return clickResult{Clicked: true} }}
			return p
		})
	}
	fb := mk("[data-aj-close]")
	rec := browserEnv(t, fb).run(ScanRequest{Source: "jobright", Portals: jobrightPortals(t), TopN: new(int)})
	clicks := 0
	for _, s := range fb.scripts {
		if strings.Contains(s, "aj:click") {
			clicks++
			if !strings.Contains(s, "data-aj-close") || !strings.Contains(s, `"overlay-close"`) && !strings.Contains(s, "e.click()") {
				t.Fatalf("an unexpected click: %s", s)
			}
		}
	}
	if rec.Status != RunCompleted || clicks != 1 || rec.Counts.Inserted != 1 {
		t.Fatalf("status %s clicks %d counts %+v", rec.Status, clicks, rec.Counts)
	}
	fb = mk("")
	rec = browserEnv(t, fb).run(ScanRequest{Source: "jobright", Portals: jobrightPortals(t), TopN: new(int)})
	if rec.Status != RunBlocked || rec.Reason != "overlay" || fb.count("click") != 0 {
		t.Fatalf("no close control: status %s reason %q clicks %d", rec.Status, rec.Reason, fb.count("click"))
	}
}

func TestJobright_NoSponsorshipLabelFilteredWhenProfileNeedsSponsorship(t *testing.T) {
	card := rawCard{ID: "r1", Title: "Backend Engineer", Company: "Acme", Age: "3 hours ago"}
	route := recommendRoute([]rawCard{card}, fakePage{detail: detailRead{Next: jobrightNext("https://careers.example.com/jobs/1", map[string]any{"isCitizenOnly": true, "isH1bSponsor": true})}})
	yes := true
	req := ScanRequest{Source: "jobright", Portals: jobrightPortals(t), TopN: new(int)}
	req.Profile.WorkAuthorization.RequiresSponsorshipNow = &yes
	rec := browserEnv(t, newFakeBrowser(t, route)).run(req)
	if rec.Counts.Filtered[FilterSponsorship] != 1 || rec.Counts.Inserted != 0 {
		t.Fatalf("counts = %+v", rec.Counts)
	}
	if err := rec.Counts.Validate(); err != nil {
		t.Fatal(err)
	}
	req.Profile.WorkAuthorization.RequiresSponsorshipNow = nil
	rec = browserEnv(t, newFakeBrowser(t, route)).run(req)
	if rec.Counts.Inserted != 1 {
		t.Fatalf("a profile that needs no sponsorship keeps the row: %+v", rec.Counts)
	}
}
