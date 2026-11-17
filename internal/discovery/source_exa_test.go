package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/tracker"
	"applysmith/internal/workspace"
)

const testExaKey = "exa-test-key-0001"

type fakeLeads struct {
	mu    sync.Mutex
	pages []string
}

func (f *fakeLeads) AddCareersPage(_ context.Context, pageURL, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pages = append(f.pages, pageURL)
	return nil
}

type fakeMirror struct {
	hosts   []string
	resolve func(mirrorURL, company, role string) MirrorResult
}

func (f fakeMirror) IsMirror(raw string) bool {
	for _, h := range f.hosts {
		if strings.Contains(raw, h) {
			return true
		}
	}
	return false
}
func (f fakeMirror) Resolve(_ context.Context, u, c, r string) MirrorResult {
	return f.resolve(u, c, r)
}
func (f fakeMirror) AggregatorHosts() []string { return f.hosts }

func exaPortals(roles ...string) workspace.Portals {
	p := portals(roles, nil)
	return p
}

// exaServer answers every search with the same hits and cost.
func exaServer(e *env, fs *feedServer, cost float64, hits func(r *http.Request, body map[string]any) []obj) {
	e.sc.Endpoints["exa"] = "https://api.exa.example.test"
	e.withKeys(map[string]string{"api-key-exa": testExaKey})
	fs.handle = func(w http.ResponseWriter, r *http.Request, raw string) {
		var body map[string]any
		_ = json.Unmarshal([]byte(raw), &body)
		var out []obj
		if hits != nil {
			out = hits(r, body)
		}
		if out == nil {
			out = []obj{}
		}
		writeJSON(w, obj{"results": out, "costDollars": obj{"total": cost}})
	}
}

func ghHit(n int) obj {
	return obj{"url": fmt.Sprintf("https://job-boards.greenhouse.io/example-co/jobs/%d", 7000+n), "title": fmt.Sprintf("Software Engineer %d", n),
		"text": "Build things.", "publishedDate": ago(5 * time.Hour).Format(time.RFC3339)}
}

func (e *env) seedTracker(n int, score float64, status string) {
	e.t.Helper()
	for i := 0; i < n; i++ {
		en := e.trackedRow(fmt.Sprintf("Seed Company %s %d", status, i), fmt.Sprintf("Engineer %d", i), fmt.Sprintf("https://job-boards.greenhouse.io/seed-%s-%d/jobs/%d", status, i, 100+i))
		e.exec(`UPDATE tracker_entries SET score = ?, status = ? WHERE id = ?`, score, status, en.ID)
	}
}

func TestExa_NoKeyEndsSkipped(t *testing.T) {
	e, fs, _ := newFeedEnv(t)
	rec := e.run(ScanRequest{Source: "exa", Portals: exaPortals("engineer")})
	if rec.Status != RunSkipped || rec.Reason != ReasonNoKey || len(fs.requests()) != 0 {
		t.Fatalf("record = %+v", rec)
	}
}

func TestExa_QueryPlan_TracksAndBounds(t *testing.T) {
	e, fs, _ := newFeedEnv(t)
	exaServer(e, fs, 0.001, nil)
	e.seedTracker(10, 4.7, "evaluated")
	e.seedTracker(10, 3.0, "responded")
	roles := []string{"engineer", "developer", "programmer", "analyst", "scientist", "architect", "designer", "tester"}
	rec := e.run(ScanRequest{Source: "exa", Portals: exaPortals(roles...)})
	if rec.Status != RunCompleted {
		t.Fatalf("record = %+v", rec)
	}
	var ats, web, similar, watch int
	for _, r := range fs.requests() {
		var b map[string]any
		_ = json.Unmarshal([]byte(r.Body), &b)
		switch {
		case r.Path == "/findSimilar":
			similar++
		case b["includeText"] != nil:
			watch++
		case b["includeDomains"] != nil:
			ats++
		default:
			web++
		}
		if b["numResults"] != float64(10) || b["contents"].(map[string]any)["text"].(map[string]any)["maxCharacters"] != float64(8000) {
			t.Fatalf("request bounds = %v", b)
		}
		if r.Header.Get("X-Api-Key") != testExaKey {
			t.Fatalf("key header = %q", r.Header.Get("X-Api-Key"))
		}
	}
	if ats != 6 || web != 6 || similar != 8 || watch != 8 {
		t.Fatalf("ats %d, web %d, similar %d, watch %d", ats, web, similar, watch)
	}
}

func TestExa_WatchQueryPinsCompanyWithShortPhrase(t *testing.T) {
	e, fs, _ := newFeedEnv(t)
	exaServer(e, fs, 0.001, func(r *http.Request, body map[string]any) []obj {
		if body["includeText"] == nil {
			return nil
		}
		return []obj{
			{"url": "https://jobs.lever.co/other-co/5d1c0a9e-0000-4000-8000-000000000009", "title": "Engineer", "text": "A role at someone else.", "publishedDate": ago(time.Hour).Format(time.RFC3339)},
			{"url": "https://jobs.lever.co/seed-co/5d1c0a9e-0000-4000-8000-000000000010", "title": "Engineer at Alpha Beta Gamma Delta Epsilon Zeta Corp", "text": "Join Alpha Beta Gamma Delta Epsilon Zeta Corp today.", "publishedDate": ago(time.Hour).Format(time.RFC3339)},
		}
	})
	en := e.trackedRow("Alpha Beta Gamma Delta Epsilon Zeta Corp", "Engineer", "https://job-boards.greenhouse.io/alpha/jobs/9")
	e.exec(`UPDATE tracker_entries SET status = 'interview' WHERE id = ?`, en.ID)
	rec := e.run(ScanRequest{Source: "exa", Portals: exaPortals()})
	var phrase []any
	for _, r := range fs.requests() {
		var b map[string]any
		_ = json.Unmarshal([]byte(r.Body), &b)
		if v, ok := b["includeText"].([]any); ok {
			phrase = v
		}
	}
	if len(phrase) != 1 || len(strings.Fields(phrase[0].(string))) > 5 || phrase[0] != "Alpha Beta Gamma Delta Epsilon" {
		t.Fatalf("includeText = %v", phrase)
	}
	if rec.Detail["dropped"] != 1 || rec.Counts.Inserted != 1 {
		t.Fatalf("record = %+v", rec)
	}
}

func TestExa_WatermarkAdvancesOnlyWhenAllQueriesSucceed(t *testing.T) {
	e, fs, _ := newFeedEnv(t)
	exaServer(e, fs, 0.001, nil)
	inner := fs.handle
	failing := true
	fs.handle = func(w http.ResponseWriter, r *http.Request, body string) {
		if failing && strings.Contains(body, "analyst") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		inner(w, r, body)
	}
	p := exaPortals("engineer", "analyst")
	rec := e.run(ScanRequest{Source: "exa", Portals: p})
	st, _ := e.rp.ExaStateGet(e.ctx)
	if rec.Status != RunCompleted || !st.Watermark.IsZero() {
		t.Fatalf("a failed query advanced the watermark: %s %v", rec.Status, st.Watermark)
	}
	failing = false
	e.clk.advance(time.Hour)
	e.run(ScanRequest{Source: "exa", Portals: p})
	st, _ = e.rp.ExaStateGet(e.ctx)
	if !st.Watermark.Equal(epoch.Add(time.Hour)) {
		t.Fatalf("watermark = %v", st.Watermark)
	}
	// The window replays: the next run starts from the watermark minus 36 hours.
	fresh := len(fs.requests())
	e.clk.advance(time.Hour)
	e.run(ScanRequest{Source: "exa", Portals: p})
	for _, r := range fs.requests()[fresh:] {
		var b map[string]any
		_ = json.Unmarshal([]byte(r.Body), &b)
		ts, _ := time.Parse(time.RFC3339, b["startPublishedDate"].(string))
		if !ts.Equal(epoch.Add(time.Hour).Add(-36 * time.Hour)) {
			t.Fatalf("start = %v", ts)
		}
	}
}

func TestExa_ZeroYieldQuerySuspendedSevenDays(t *testing.T) {
	e, fs, _ := newFeedEnv(t)
	exaServer(e, fs, 0.001, nil)
	p := exaPortals("engineer")
	for i := 0; i < 5; i++ {
		e.run(ScanRequest{Source: "exa", Portals: p})
		e.clk.advance(time.Hour)
	}
	before := len(fs.requests())
	rec := e.run(ScanRequest{Source: "exa", Portals: p})
	if len(fs.requests()) != before || len(rec.Suspended) != 2 {
		t.Fatalf("a suspended query was issued: %d new requests, suspended %v", len(fs.requests())-before, rec.Suspended)
	}
	e.clk.advance(7 * day)
	e.run(ScanRequest{Source: "exa", Portals: p})
	if len(fs.requests()) == before {
		t.Fatal("the queries were not retried after the suspension")
	}
}

func TestExa_SpendCapStopsPlan(t *testing.T) {
	e, fs, _ := newFeedEnv(t)
	exaServer(e, fs, 0.025, nil)
	rec := e.run(ScanRequest{Source: "exa", Portals: exaPortals("engineer", "developer", "analyst", "scientist", "architect", "designer")})
	if rec.Status != RunCompleted || rec.Reason != ReasonBudget || len(fs.requests()) != 10 {
		t.Fatalf("status %s reason %q after %d requests", rec.Status, rec.Reason, len(fs.requests()))
	}
}

// The queries the spend cap kept from running are owed the window they
// missed: the next run asks them first, from where this run would have, while
// the watermark moves on for the queries that ran.
func TestExa_QueriesTheSpendCapLeftRunFirstNextTimeOverTheWindowTheyMissed(t *testing.T) {
	e, fs, _ := newFeedEnv(t)
	exaServer(e, fs, 0.025, nil)
	p := exaPortals("engineer", "developer", "analyst", "scientist", "architect", "designer")
	e.run(ScanRequest{Source: "exa", Portals: p})
	st, _ := e.rp.ExaStateGet(e.ctx)
	left := []string{"web|architect job opening", "web|designer job opening"}
	if !st.Watermark.Equal(epoch) || len(st.Owed) != 2 || !st.Owed[left[0]].Equal(epoch.Add(-72*time.Hour)) || !st.Owed[left[1]].Equal(epoch.Add(-72*time.Hour)) {
		t.Fatalf("watermark %v, owed %v", st.Watermark, st.Owed)
	}
	first := len(fs.requests())
	e.clk.advance(time.Hour)
	e.run(ScanRequest{Source: "exa", Portals: p})
	for i, r := range fs.requests()[first:] {
		var b map[string]any
		_ = json.Unmarshal([]byte(r.Body), &b)
		ts, _ := time.Parse(time.RFC3339, b["startPublishedDate"].(string))
		want := epoch.Add(-36 * time.Hour)
		if i < 2 {
			want = epoch.Add(-72 * time.Hour)
			if !strings.HasPrefix(left[i], "web|"+b["query"].(string)) || b["includeDomains"] != nil {
				t.Fatalf("request %d asked %v, want the owed %q", i, b["query"], left[i])
			}
		}
		if !ts.Equal(want) {
			t.Fatalf("request %d (%v) starts at %v, want %v", i, b["query"], ts, want)
		}
	}
	st, _ = e.rp.ExaStateGet(e.ctx)
	if len(st.Owed) != 2 || st.Owed[left[0]] != (time.Time{}) || !st.Owed["web|analyst job opening"].Equal(epoch.Add(-36*time.Hour)) || !st.Owed["web|scientist job opening"].Equal(epoch.Add(-36*time.Hour)) {
		t.Fatalf("owed after the second run = %v", st.Owed)
	}
}

func TestExa_BudgetUsesResponseCostNotEstimate(t *testing.T) {
	// A flat estimate of one cent a query would stop the plan after 25; the
	// responses report a tenth of that, so every query runs.
	e, fs, _ := newFeedEnv(t)
	exaServer(e, fs, 0.001, nil)
	e.seedTracker(8, 4.9, "evaluated")
	rec := e.run(ScanRequest{Source: "exa", Portals: exaPortals("engineer", "developer", "analyst", "scientist", "architect", "designer")})
	if rec.Reason == ReasonBudget || len(fs.requests()) != 20 {
		t.Fatalf("reason %q after %d requests", rec.Reason, len(fs.requests()))
	}
	if rec.Detail["spend-micro-usd"] != 20000 {
		t.Fatalf("spend = %d micro-dollars", rec.Detail["spend-micro-usd"])
	}
}

func TestExa_HitClassification_PostingCareersPageDropped(t *testing.T) {
	e, fs, _ := newFeedEnv(t)
	leads := &fakeLeads{}
	e.sc.Leads = leads
	exaServer(e, fs, 0.001, func(r *http.Request, body map[string]any) []obj {
		return []obj{ghHit(1),
			{"url": "https://jobs.ashbyhq.com/example-corp", "title": "Careers", "text": "x"},
			{"url": "https://www.example.org/job/12345", "title": "A job on a company site", "text": "x"},
			{"url": "http://127.0.0.1:47319/jobs/1", "title": "local", "text": "x"}}
	})
	rec := e.run(ScanRequest{Source: "exa", Portals: exaPortals("engineer")})
	if rec.Counts.Inserted != 1 || len(leads.pages) != 1 || leads.pages[0] != "https://jobs.ashbyhq.com/example-corp" {
		t.Fatalf("inserted %d, careers pages %v", rec.Counts.Inserted, leads.pages)
	}
	// 1 non-board job page and 1 unsafe URL per query, two queries.
	if rec.Detail["dropped"] != 4 {
		t.Fatalf("dropped = %d", rec.Detail["dropped"])
	}
	row := e.inbox(InboxQuery{})[0]
	if row.IdentityKey != "greenhouse:example-co/7001" || row.Company != "Example Co" {
		t.Fatalf("row = %+v", row)
	}
}

func TestExa_SeenURLCountsAsDuplicate(t *testing.T) {
	e, fs, _ := newFeedEnv(t)
	exaServer(e, fs, 0.001, func(r *http.Request, body map[string]any) []obj { return []obj{ghHit(1)} })
	p := exaPortals("engineer")
	e.run(ScanRequest{Source: "exa", Portals: p})
	rec := e.run(ScanRequest{Source: "exa", Portals: p})
	if rec.Counts.Inserted != 0 || rec.Counts.Duplicates["exa-seen"] != 2 {
		t.Fatalf("counts = %+v", rec.Counts)
	}
	if err := rec.Counts.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestTracker_ScoredPostingsRead(t *testing.T) {
	e := newEnv(t)
	row := func(company string, score float64, ageDays int) tracker.Entry {
		en := e.trackedRow(company, "Engineer", "https://jobs.example.com/"+strings.ReplaceAll(strings.ToLower(company), " ", "-"))
		e.exec(`UPDATE tracker_entries SET score = ?, updated_at = ? WHERE id = ?`, score, iso(e.clk.now().Add(-time.Duration(ageDays)*24*time.Hour)), en.ID)
		return en
	}
	best := row("Best Corp", 4.9, 1)
	good := row("Good Corp", 4.5, 29)
	row("Below Corp", 4.4, 1)
	row("Old Corp", 4.8, 31)
	gone := row("Archived Corp", 4.8, 1)
	e.archive(gone.ID)
	nourl := row("Nourl Corp", 4.8, 1)
	e.exec(`UPDATE tracker_entries SET posting_url = '' WHERE id = ?`, nourl.ID)

	seeds, err := e.rp.ExaSeedsGet(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(seeds.Similar) != 2 || seeds.Similar[0] != best.PostingURL || seeds.Similar[1] != good.PostingURL {
		t.Fatalf("seeds = %v, want the 4.5-and-up postings of the last 30 days, best first", seeds.Similar)
	}

	for i := 0; i < 12; i++ {
		row(fmt.Sprintf("Many %d Corp", i), 4.6, 2)
	}
	if seeds, _ = e.rp.ExaSeedsGet(e.ctx); len(seeds.Similar) != 8 {
		t.Fatalf("seeds = %d, want at most 8", len(seeds.Similar))
	}
}

func TestExa_PostingTitleLeavesOutThePageTitlesCompanyTail(t *testing.T) {
	for _, c := range []struct{ url, title, want string }{
		{"https://jobs.ashbyhq.com/expensify/096ff7eb-2a55-4867-b0d1-b9c4743dc576", "Full Stack Engineer  @ expensify", "Full Stack Engineer"},
		{"https://boards.greenhouse.io/chime/jobs/8868948002", "Software Engineer, Jade - AI Financial Partner at Chime", "Software Engineer, Jade - AI Financial Partner"},
		{"https://boards.greenhouse.io/chime/jobs/8868948003", "Platform Engineer at Scale", "Platform Engineer at Scale"},
	} {
		if p := exaPosting(c.url, exaHit{Title: c.title}); p.Title != c.want {
			t.Errorf("%q: title %q, want %q", c.title, p.Title, c.want)
		}
	}
}
