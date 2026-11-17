package discovery

import (
	"fmt"
	"html"
	"net/http"
	"strings"
	"testing"
	"time"
)

const speedrunTestBase = "https://speedrun.example.test"

// speedrunObject mimics one encoded job object of the board's list page.
func speedrunObject(id, title, company, published string) string {
	obj := fmt.Sprintf(`[0,{"id":[0,%q],"title":[0,%q],"company_key":[0,%q],"location":[0,"Austin, TX"],"employment_type":[0,null],"published_at":[0,%q],"path":[0,"/jobs/%s"]}]`,
		id, title, company, published, id)
	return html.EscapeString(obj)
}

func speedrunPage(objects ...string) string {
	return `<html><body><astro-island props="` + strings.Join(objects, "") + `"></astro-island></body></html>`
}

func speedrunEnv(t *testing.T, pages map[string]string) (*env, *feedServer) {
	e, fs, _ := newFeedEnv(t)
	e.sc.Endpoints["speedrun"] = speedrunTestBase
	fs.handle = func(w http.ResponseWriter, r *http.Request, _ string) {
		if r.URL.Path == "/jobs.rss" {
			writeHTML(w, pages["rss"])
			return
		}
		page := r.URL.Query().Get("page")
		if page == "" {
			page = "1"
		}
		body, ok := pages[page]
		if !ok {
			body = speedrunPage()
		}
		writeHTML(w, body)
	}
	return e, fs
}

func fresh(id, title string) string {
	return speedrunObject(id, title, "Example Co", ago(2*time.Hour).Format(time.RFC3339))
}

func stale(id, title string) string {
	return speedrunObject(id, title, "Example Co", ago(72*time.Hour).Format(time.RFC3339))
}

func TestSpeedrun_ParsesEncodedJobObjectsAndBuildsApplyURL(t *testing.T) {
	e, fs := speedrunEnv(t, map[string]string{"1": speedrunPage(fresh("sr-1", "Software Engineer"), stale("sr-2", "Old Engineer"))})
	rec := e.run(ScanRequest{Source: "speedrun", Portals: eng()})
	if rec.Status != RunCompleted || rec.Counts.Inserted != 1 || rec.Counts.Filtered["stale"] != 1 {
		t.Fatalf("record = %+v", rec)
	}
	row := e.inbox(InboxQuery{})[0]
	if row.URL != speedrunTestBase+"/jobs/sr-1" || row.Company != "Example Co" || row.Role != "Software Engineer" || row.Location != "Austin, TX" {
		t.Fatalf("row = %+v", row)
	}
	for _, r := range fs.requests() {
		if r.Method != http.MethodGet {
			t.Errorf("request %s %s", r.Method, r.Path)
		}
	}
}

func TestSpeedrun_BoardFiltersNotFreeText(t *testing.T) {
	e, fs := speedrunEnv(t, map[string]string{"1": speedrunPage(fresh("sr-1", "Software Engineer"))})
	e.run(ScanRequest{Source: "speedrun", Portals: eng()})
	q := fs.requests()[0].query()
	if q.Get("fn") != "engineering" || q.Get("sen") != "junior" || q.Has("q") || q.Has("emp") || q.Has("remote") {
		t.Fatalf("default query = %v", q)
	}
	e2, fs2 := speedrunEnv(t, map[string]string{"1": speedrunPage(fresh("sr-1", "Software Engineer"))})
	e2.run(ScanRequest{Source: "speedrun", Portals: eng(), Fn: "research", Seniority: "senior", Remote: true, Search: "ml"})
	q = fs2.requests()[0].query()
	if q.Get("fn") != "research" || q.Get("sen") != "senior" || q.Get("remote") != "1" || q.Get("q") != "ml" {
		t.Fatalf("query = %v", q)
	}
}

func TestSpeedrun_ExplicitURLWinsOverFilters(t *testing.T) {
	e, fs := speedrunEnv(t, map[string]string{"1": speedrunPage(fresh("sr-1", "Software Engineer"))})
	e.run(ScanRequest{Source: "speedrun", Portals: eng(), URL: speedrunTestBase + "/jobs?fn=design", Fn: "engineering"})
	reqs := fs.requests()
	if len(reqs) == 0 || reqs[0].query().Get("fn") != "design" || reqs[0].query().Has("sen") {
		t.Fatalf("requests = %+v", reqs)
	}
}

func TestSpeedrun_StopsWhenPageAddsNoFreshRow(t *testing.T) {
	e, fs := speedrunEnv(t, map[string]string{"1": speedrunPage(stale("sr-1", "Old Engineer")), "2": speedrunPage(fresh("sr-2", "Software Engineer"))})
	e.run(ScanRequest{Source: "speedrun", Portals: eng()})
	if n := len(fs.requests()); n != 1 {
		t.Fatalf("%d list requests, want 1 when the first page has no fresh row", n)
	}
	e2, fs2 := speedrunEnv(t, map[string]string{"1": speedrunPage(fresh("sr-1", "Software Engineer")), "2": speedrunPage(fresh("sr-2", "Data Engineer")), "3": speedrunPage(fresh("sr-3", "QA Engineer"))})
	rec := e2.run(ScanRequest{Source: "speedrun", Portals: eng()})
	if n := len(fs2.requests()); n != 2 || rec.Counts.Inserted != 2 {
		t.Fatalf("%d list requests, inserted %d; want 2 pages", n, rec.Counts.Inserted)
	}
}

// A list page that fails after an earlier one gave rows ends the paging and
// keeps what was read; the record carries the failure.
func TestSpeedrun_ALaterPagesFailureKeepsTheRowsAlreadyRead(t *testing.T) {
	e, fs := speedrunEnv(t, map[string]string{"1": speedrunPage(fresh("sr-1", "Software Engineer"))})
	inner := fs.handle
	fs.handle = func(w http.ResponseWriter, r *http.Request, body string) {
		if r.URL.Query().Get("page") == "2" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		inner(w, r, body)
	}
	rec := e.run(ScanRequest{Source: "speedrun", Portals: eng()})
	if rec.Status != RunCompleted || rec.Counts.Inserted != 1 || rec.Counts.BoardErrors != 1 || len(rec.Errors) != 1 || !strings.Contains(rec.Errors[0], "page 2") {
		t.Fatalf("record = %+v", rec)
	}
}

func TestSpeedrun_RSSTitleSplitsRoleAtCompany(t *testing.T) {
	rss := `<?xml version="1.0"?><rss><channel>
<item><title>Backend Engineer at Example Co</title><link>https://speedrun.example.test/jobs/feed-1</link><description>Austin, TX &#183; Full time</description><pubDate>` +
		ago(3*time.Hour).Format(time.RFC1123Z) + `</pubDate></item>
<item><title>Old Engineer at Example Co</title><link>https://speedrun.example.test/jobs/feed-2</link><description>Austin, TX</description><pubDate>` +
		ago(90*time.Hour).Format(time.RFC1123Z) + `</pubDate></item></channel></rss>`
	e, _ := speedrunEnv(t, map[string]string{"1": speedrunPage(fresh("sr-1", "Software Engineer")), "rss": rss})
	p := portalsWith(t, eng(), "speedrun", map[string]any{"rss": true})
	rec := e.run(ScanRequest{Source: "speedrun", Portals: p})
	if rec.Counts.Inserted != 2 || rec.Counts.Filtered["stale"] != 1 {
		t.Fatalf("counts = %+v", rec.Counts)
	}
	var found bool
	for _, row := range e.inbox(InboxQuery{}) {
		if row.Role == "Backend Engineer" && row.Company == "Example Co" && row.Location == "Austin, TX" {
			found = true
		}
	}
	if !found {
		t.Fatal("the feed row was not split into role and company")
	}
	if role, company := splitRoleAtCompany("Staff Engineer at Lab at Example Co"); role != "Staff Engineer at Lab" || company != "Example Co" {
		t.Fatalf("split = %q, %q", role, company)
	}
}

func TestSpeedrun_ListWithoutObjectsIsLayoutChanged(t *testing.T) {
	e, _ := speedrunEnv(t, map[string]string{"1": "<html><body>the board changed</body></html>"})
	rec := e.run(ScanRequest{Source: "speedrun", Portals: eng()})
	if rec.Status != RunFailed || rec.Reason != ReasonLayoutChanged {
		t.Fatalf("status %s reason %q", rec.Status, rec.Reason)
	}
}

func TestSpeedrun_ChallengeEndsRunBlocked(t *testing.T) {
	e, fs := speedrunEnv(t, nil)
	fs.handle = func(w http.ResponseWriter, r *http.Request, _ string) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<title>Just a moment...</title>"))
	}
	rec := e.run(ScanRequest{Source: "speedrun", Portals: eng()})
	if rec.Status != RunBlocked || len(rec.Attempts) != 1 || rec.Attempts[0].Status != AttemptChallenge || len(fs.requests()) != 1 {
		t.Fatalf("record = %+v, %d requests", rec, len(fs.requests()))
	}
}
