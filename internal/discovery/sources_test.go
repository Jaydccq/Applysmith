package discovery

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestSources_EndpointURLs(t *testing.T) {
	var e Endpoints
	cases := []struct {
		b    Board
		page int
		want string
	}{
		{Board{ATS: Greenhouse, Slug: "example-co"}, 0, "https://boards-api.greenhouse.io/v1/boards/example-co/jobs"},
		{Board{ATS: Lever, Slug: "example-co"}, 0, "https://api.lever.co/v0/postings/example-co"},
		{Board{ATS: Ashby, Slug: "example-co"}, 0, "https://api.ashbyhq.com/posting-api/job-board/example-co?includeCompensation=true"},
		{Board{ATS: SmartRecruiters, Slug: "example-co"}, 2, "https://api.smartrecruiters.com/v1/companies/example-co/postings?limit=100&offset=200"},
		{Board{ATS: Workable, Slug: "example-co"}, 0, "https://apply.workable.com/api/v1/widget/accounts/example-co?details=true"},
	}
	for _, c := range cases {
		if got := e.ListURL(c.b, c.page); got != c.want {
			t.Errorf("%s: got %s, want %s", c.b.ATS, got, c.want)
		}
	}
}

func TestSources_ResolveSlug_Table(t *testing.T) {
	cases := []struct {
		url  string
		want string // "" means no board
	}{
		{"https://jobs.ashbyhq.com/example-co/0a1b2c3d", "ashby:example-co"},
		{"https://jobs.lever.co/Example-Co/9f8e7d6c-1111-2222-3333-444455556666/apply", "lever:example-co"},
		{"https://jobs.smartrecruiters.com/ExampleCo/743999000001-software-engineer", "smartrecruiters:exampleco"},
		{"https://careers.smartrecruiters.com/ExampleCo", "smartrecruiters:exampleco"},
		{"https://apply.workable.com/example-co/j/AB12CD34EF/", "workable:example-co"},
		{"https://example-co.workable.com/jobs/123", "workable:example-co"},
		{"https://boards.greenhouse.io/example-co/jobs/4001", "greenhouse:example-co"},
		{"https://job-boards.eu.greenhouse.io/example-co", "greenhouse:example-co"},
		{"https://boards.greenhouse.io/embed/job_app?for=example-co&token=4001", "greenhouse:example-co"},
		{"https://example-co.greenhouse.io/jobs/4001", "greenhouse:example-co"},
		{"https://api.greenhouse.io/v1/x", ""},
		{"https://support.workable.com/hc", ""},
		{"https://jobs.lever.co/favicon.ico", ""},
		{"https://apply.workable.com/j/AB12CD34", ""},
		{"https://example.com/careers", ""},
	}
	for _, c := range cases {
		b, err := ResolveBoard(c.url)
		got := ""
		if err == nil {
			got = b.Key()
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.url, got, c.want)
		}
	}
}

func TestSources_Normalize_Greenhouse(t *testing.T) {
	body, _ := json.Marshal(ghJobs("example-co", ghItem(4001, "Software Engineer", "New York, NY"), ghItem(4002, "Data Analyst", "Remote - US")))
	posts, err := parseGreenhouse(body, Board{ATS: Greenhouse, Slug: "example-co", Company: "Example Co"})
	if err != nil || len(posts) != 2 {
		t.Fatalf("posts=%d err=%v", len(posts), err)
	}
	p := posts[0]
	if p.ATS != "greenhouse" || p.Board != "example-co" || p.Company != "Example Co" || p.Title != "Software Engineer" ||
		p.Location != "New York, NY" || p.JobID != "4001" || p.URL != "https://job-boards.greenhouse.io/example-co/jobs/4001" || p.PostedAt.IsZero() {
		t.Fatalf("posting = %+v", p)
	}
	if posts[1].Remote != true {
		t.Fatalf("remote flag not set from location")
	}
}

// A location name that is a machine slug (Hover's "san_francisconew_york")
// names no place: the posting's offices stand for it, else the slug's words. A
// site name with no comma (BCC's "NIH-Bethesda") gives way to the offices'
// addresses; a "City, ST" name or remote work keeps its own.
func TestSources_Normalize_GreenhouseSlugLocation(t *testing.T) {
	withOffices := ghItem(4003, "Security Engineer", "san_francisconew_york")
	withOffices["offices"] = []obj{{"name": "San Francisco / New York"}}
	site := ghItem(4006, "Full Stack Engineer", "NIH-Bethesda")
	site["offices"] = []obj{{"name": "BCC-Bethesda", "location": "Bethesda, Maryland, United States"}}
	remote := ghItem(4007, "Data Engineer", "Remote")
	remote["offices"] = []obj{{"name": "NYC", "location": "New York, New York, United States"}}
	city := ghItem(4008, "Data Engineer", "Austin, TX")
	city["offices"] = []obj{{"name": "HQ", "location": "Denver, Colorado, United States"}}
	body, _ := json.Marshal(ghJobs("example-co", withOffices, ghItem(4004, "Platform Engineer", "san_francisco"), ghItem(4005, "Data Engineer", "Remote"), site, remote, city))
	posts, err := parseGreenhouse(body, Board{ATS: Greenhouse, Slug: "example-co", Company: "Example Co"})
	if err != nil || len(posts) != 6 {
		t.Fatalf("posts=%d err=%v", len(posts), err)
	}
	for i, want := range []string{"San Francisco / New York", "san francisco", "Remote", "Bethesda, Maryland, United States", "Remote", "Austin, TX"} {
		if posts[i].Location != want {
			t.Errorf("posting %s location = %q, want %q", posts[i].JobID, posts[i].Location, want)
		}
	}
}

func TestSources_Normalize_LeverAshbyWorkable(t *testing.T) {
	lb, _ := json.Marshal([]obj{leverPost("5d1c0a9e-0000-4000-8000-000000000001", "Backend Engineer", "Austin, TX", epoch.Add(-72*time.Hour))})
	lp, err := parseLever(lb, Board{ATS: Lever, Slug: "example-lever", Company: "Example Lever"})
	if err != nil || len(lp) != 1 || lp[0].Location != "Austin, TX" || lp[0].PostedAt.IsZero() || lp[0].Description == "" || lp[0].EmploymentType != "Full-time" {
		t.Fatalf("lever: %+v %v", lp, err)
	}
	ab, _ := json.Marshal(obj{"jobs": []obj{ashbyItem("11111111-aaaa-bbbb-cccc-000000000001", "Platform Engineer", "Remote", epoch.Add(-24*time.Hour))}})
	ap, err := parseAshby(ab, Board{ATS: Ashby, Slug: "example-ashby"})
	if err != nil || len(ap) != 1 || ap[0].Compensation != "$120K - $150K" || ap[0].Company != "Example Ashby" {
		t.Fatalf("ashby: %+v %v", ap, err)
	}
	wb, _ := json.Marshal(obj{"name": "Example Workable Inc", "jobs": []obj{workableItem("AB12CD34", "QA Engineer", "Exampleton", "CA", "United States", "2026-10-09")}})
	wp, err := parseWorkable(wb, Board{ATS: Workable, Slug: "example-work", Company: "Registry Name"})
	if err != nil || len(wp) != 1 || wp[0].URL != "https://apply.workable.com/example-work/j/AB12CD34" ||
		wp[0].Company != "Example Workable Inc" || wp[0].Location != "Exampleton, CA, United States" {
		t.Fatalf("workable: %+v %v (the account name wins over the registry name)", wp, err)
	}
}

func TestSources_SmartRecruiters_NoIdentifierDropped(t *testing.T) {
	page := srPage(2, 0, 2, "ExampleCo")
	page["content"].([]obj)[1]["company"] = obj{"name": "Example Sr Co"}
	b, _ := json.Marshal(page)
	posts, _, err := parseSmartRecruiters(b, Board{ATS: SmartRecruiters, Slug: "exampleco"})
	if err != nil || len(posts) != 1 {
		t.Fatalf("posts=%d err=%v", len(posts), err)
	}
	if posts[0].URL != "https://jobs.smartrecruiters.com/ExampleCo/743900000000" {
		t.Fatalf("url = %s, identifier case must be preserved", posts[0].URL)
	}
}

func TestSources_SmartRecruiters_PagesTo500ThenTruncated(t *testing.T) {
	f := newFakeATS(t)
	for p := 0; p < 6; p++ {
		f.json("/v1/companies/exampleco/postings?offset="+itoa(p*100), srPage(640, p*100, 100, "ExampleCo"))
	}
	c := NewClient()
	c.Sleep = f.sleep
	c.MinGap = 0
	res, err := c.FetchBoard(context.Background(), f.endpoints(), Board{ATS: SmartRecruiters, Slug: "exampleco"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Postings) != 500 || !res.Truncated || res.Lost != 140 {
		t.Fatalf("read=%d truncated=%v lost=%d", len(res.Postings), res.Truncated, res.Lost)
	}
	if f.hitCount("/v1/companies/exampleco/postings?offset=500") != 0 {
		t.Fatal("a sixth page was requested")
	}
}
