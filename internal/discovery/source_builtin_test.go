package discovery

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

const builtInTestBase = "https://builtin.example.test"

func builtInCard(id int, title, company, location, age string) string {
	return fmt.Sprintf(`<div data-id="job-card"><a data-id="job-card-title" href="/job/%s/%d">%s</a>
<a data-id="company-title"><span>%s</span></a><div><i class="fa-regular fa-location-dot fs-xs text-pretty-blue"></i></div><div><span class="font-barlow text-gray-04">%s</span></div><span>%s</span></div>`,
		strings.ReplaceAll(strings.ToLower(title), " ", "-"), id, title, company, location, age)
}

func builtInPage(cards ...string) string {
	return `<html><body><div data-id="jobs-list">` + strings.Join(cards, "\n") + `</div></body></html>`
}

func builtInLD(title, company, url string, posted time.Time) string {
	return fmt.Sprintf(`<script type="application/ld+json">{"@context":"https://schema.org","@type":"JobPosting","title":%q,"url":%q,"datePosted":%q,
"hiringOrganization":{"@type":"Organization","name":%q},"jobLocation":{"@type":"Place","address":{"addressLocality":"Austin","addressRegion":"TX","addressCountry":"US"}},
"description":"<p>Build the product.</p>"}</script>`, title, url, posted.Format("2006-01-02"), company)
}

func builtInEnv(t *testing.T, page func(r *http.Request) string) (*env, *feedServer) {
	e, fs, _ := newFeedEnv(t)
	e.sc.Endpoints["builtin"] = builtInTestBase
	fs.handle = func(w http.ResponseWriter, r *http.Request, _ string) { writeHTML(w, page(r)) }
	return e, fs
}

func TestBuiltIn_SearchPath_CategorySubcategoryExperienceInOrder(t *testing.T) {
	e, fs := builtInEnv(t, func(*http.Request) string {
		return builtInPage(builtInCard(1, "Backend Engineer", "Example Corp", "Austin, TX", "1 day ago"))
	})
	p := portalsWith(t, eng(), "builtin", map[string]any{"path": "/jobs/dev-engineering/entry-level/junior"})
	rec := e.run(ScanRequest{Source: "builtin", Portals: p, Window: 48 * time.Hour})
	if rec.Status != RunCompleted || rec.Counts.Inserted != 1 {
		t.Fatalf("record = %+v", rec)
	}
	got := fs.requests()
	if len(got) != 1 || got[0].Path != "/jobs/dev-engineering/entry-level/junior" {
		t.Fatalf("requests = %+v", got)
	}
	if q := got[0].query(); q.Get("daysSinceUpdated") != "3" || q.Has("search") {
		t.Fatalf("query = %v", q)
	}
}

func TestBuiltIn_KeywordWithoutPathSearchesAllJobs(t *testing.T) {
	e, fs := builtInEnv(t, func(*http.Request) string {
		return builtInPage(builtInCard(1, "Backend Engineer", "Example Corp", "Austin, TX", "1 day ago"))
	})
	p := portalsWith(t, eng(), "builtin", map[string]any{"search": "software engineer"})
	rec := e.run(ScanRequest{Source: "builtin", Portals: p, Window: 7 * day})
	got := fs.requests()
	if rec.Status != RunCompleted || rec.Counts.Inserted != 1 || len(got) != 1 || got[0].Path != "/jobs" {
		t.Fatalf("record = %+v requests = %+v", rec, got)
	}
	if q := got[0].query(); q.Get("search") != "software engineer" || q.Get("daysSinceUpdated") != "7" {
		t.Fatalf("query = %v", q)
	}
}

func TestBuiltIn_ExplicitPathWithoutRecencyParamUsesLocalWindow(t *testing.T) {
	e, fs := builtInEnv(t, func(*http.Request) string {
		return builtInPage(builtInCard(1, "Backend Engineer", "Example Corp", "Austin, TX", "10 days ago"), builtInCard(2, "Data Engineer", "Example Corp", "Austin, TX", "3 hours ago"))
	})
	p := portalsWith(t, eng(), "builtin", map[string]any{"url": builtInTestBase + "/jobs/dev-engineering"})
	rec := e.run(ScanRequest{Source: "builtin", Portals: p, Window: 24 * time.Hour})
	if got := fs.requests()[0].query(); got.Has("daysSinceUpdated") {
		t.Fatalf("an explicit URL was given a recency parameter: %v", got)
	}
	if rec.Counts.Filtered["stale"] != 1 || rec.Counts.Inserted != 1 || rec.Status != RunCompleted {
		t.Fatalf("counts = %+v status %s", rec.Counts, rec.Status)
	}
}

func TestBuiltIn_NonEmptyPageWithoutRowsIsLayoutChanged(t *testing.T) {
	e, _ := builtInEnv(t, func(*http.Request) string {
		return `<html><body><main><h1>Something else entirely</h1></main></body></html>`
	})
	p := portalsWith(t, eng(), "builtin", map[string]any{"path": "/jobs/dev-engineering"})
	rec := e.run(ScanRequest{Source: "builtin", Portals: p})
	if rec.Status != RunFailed || rec.Reason != ReasonLayoutChanged {
		t.Fatalf("status %s reason %q", rec.Status, rec.Reason)
	}
}

func TestBuiltIn_EmptyFreshnessWindowCompletes_NoRecencyEmptyFails(t *testing.T) {
	empty := `<html><body><div data-id="jobs-list"><p>No jobs found</p></div></body></html>`
	e, _ := builtInEnv(t, func(*http.Request) string { return empty })
	p := portalsWith(t, eng(), "builtin", map[string]any{"path": "/jobs/dev-engineering"})
	rec := e.run(ScanRequest{Source: "builtin", Portals: p, Window: 24 * time.Hour})
	if rec.Status != RunCompleted || rec.Reason != ReasonEmptyWindow {
		t.Fatalf("with the window: status %s reason %q", rec.Status, rec.Reason)
	}
	p = portalsWith(t, eng(), "builtin", map[string]any{"url": builtInTestBase + "/jobs/dev-engineering"})
	rec = e.run(ScanRequest{Source: "builtin", Portals: p})
	if rec.Status != RunFailed || rec.Reason != ReasonNoCards {
		t.Fatalf("without the window: status %s reason %q", rec.Status, rec.Reason)
	}
	e2, _ := builtInEnv(t, func(*http.Request) string {
		return `<div data-id="job-card"><span>no link here</span></div>`
	})
	rec = e2.run(ScanRequest{Source: "builtin", Portals: p})
	if rec.Status != RunFailed || rec.Reason != ReasonEmptyExtract {
		t.Fatalf("cards that do not parse: status %s reason %q", rec.Status, rec.Reason)
	}
}

func TestBuiltIn_JSONLDPreferredOverCards(t *testing.T) {
	e, _ := builtInEnv(t, func(*http.Request) string {
		return builtInLD("Platform Engineer", "Structured Co", builtInTestBase+"/job/platform-engineer/9", ago(24*time.Hour)) +
			builtInPage(builtInCard(9, "Card Title Engineer", "Card Co", "Nowhere", "1 day ago"))
	})
	p := portalsWith(t, eng(), "builtin", map[string]any{"path": "/jobs/dev-engineering"})
	rec := e.run(ScanRequest{Source: "builtin", Portals: p})
	if rec.Counts.Inserted != 1 {
		t.Fatalf("counts = %+v", rec.Counts)
	}
	row := e.inbox(InboxQuery{})[0]
	if row.Role != "Platform Engineer" || row.Company != "Structured Co" || row.Location != "Austin, TX, US" || row.Source != "builtin" {
		t.Fatalf("row = %+v", row)
	}
}

func TestBuiltIn_CardsParsed(t *testing.T) {
	e, _ := builtInEnv(t, func(*http.Request) string {
		return builtInPage(builtInCard(7, "Backend Engineer", "Example Corp", "Austin, TX", "Reposted 2 days ago"))
	})
	p := portalsWith(t, eng(), "builtin", map[string]any{"path": "/jobs/dev-engineering"})
	rec := e.run(ScanRequest{Source: "builtin", Portals: p})
	if rec.Counts.Inserted != 1 || len(rec.Attempts) != 1 || rec.Attempts[0].Status != AttemptOK || strings.Contains(rec.Attempts[0].Path, "?") {
		t.Fatalf("record = %+v", rec)
	}
	row := e.inbox(InboxQuery{})[0]
	// The live card has no location attribute; the text after the pin icon is it.
	if row.URL != builtInTestBase+"/job/backend-engineer/7" || row.Company != "Example Corp" || row.Location != "Austin, TX" || row.PostedAt.IsZero() {
		t.Fatalf("row = %+v", row)
	}
}

func TestBuiltIn_DoublyEncodedTitleReadsAsText(t *testing.T) {
	e, _ := builtInEnv(t, func(*http.Request) string {
		return builtInPage(builtInCard(8, "Software &amp;quot;DevOps&amp;quot; Engineer R&amp;amp;D", "Example Corp", "Austin, TX", "1 day ago"))
	})
	p := portalsWith(t, eng(), "builtin", map[string]any{"path": "/jobs/dev-engineering"})
	e.run(ScanRequest{Source: "builtin", Portals: p})
	if row := e.inbox(InboxQuery{})[0]; row.Role != `Software "DevOps" Engineer R&D` {
		t.Fatalf("role = %q", row.Role)
	}
}

func TestBuiltIn_MultiLocationCardListsItsTooltipLocations(t *testing.T) {
	card := strings.Replace(builtInCard(8, "Backend Engineer", "Example Corp", "2 Locations", "1 day ago"), `<span class="font-barlow text-gray-04">2 Locations</span>`,
		`<span class="font-barlow" data-bs-title="&lt;div class=&#x27;text-truncate&#x27;&gt;Seattle, WA, USA&lt;/div&gt;&lt;div class=&#x27;text-truncate&#x27;&gt;Austin, TX, USA&lt;/div&gt;">2 Locations </span>`, 1)
	e, _ := builtInEnv(t, func(*http.Request) string { return builtInPage(card) })
	e.run(ScanRequest{Source: "builtin", Portals: portalsWith(t, eng(), "builtin", map[string]any{"path": "/jobs/dev-engineering"})})
	if row := e.inbox(InboxQuery{})[0]; row.Location != "Seattle, WA, USA; Austin, TX, USA" {
		t.Fatalf("location = %q", row.Location)
	}
}

func TestBuiltIn_DayGranularBadgeIsNotStaleAtTheEdge(t *testing.T) {
	e, _ := builtInEnv(t, func(*http.Request) string {
		return builtInPage(builtInCard(1, "Backend Engineer", "Example Corp", "Austin, TX", "Yesterday"))
	})
	p := portalsWith(t, eng(), "builtin", map[string]any{"url": builtInTestBase + "/jobs/dev-engineering"})
	rec := e.run(ScanRequest{Source: "builtin", Portals: p, Window: 24 * time.Hour})
	if rec.Counts.Inserted != 1 || rec.Counts.Filtered["stale"] != 0 {
		t.Fatalf("counts = %+v", rec.Counts)
	}
}
