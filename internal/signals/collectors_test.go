package signals

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"applysmith/internal/workspace"
)

const (
	feedHost = "alerts.example.test"
	hnHost   = "hn.algolia.com"
	exaHost  = "api.exa.ai"
	xHost    = "api.twitterapi.io"
)

const atomFeed = `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><entry>
<title type="html">&lt;b&gt;Example Corp&lt;/b&gt; raises $14M</title>
<link href="https://news.example.test/a"/><published>2026-10-10T08:00:00Z</published>
<content type="html">&lt;b&gt;Example Corp&lt;/b&gt; raised &amp;amp; will hire 20 engineers.</content></entry></feed>`

func (e *env) alertFeed(url string) {
	if e.sets.Portals.Sources == nil {
		e.sets.Portals.Sources = map[string]workspace.Source{}
	}
	var n yaml.Node
	_ = yaml.Unmarshal([]byte("url: "+url), &n)
	e.sets.Portals.Sources[SourceAlerts] = workspace.Source{Enabled: boolp(true), Queries: []yaml.Node{*n.Content[0]}}
}

func TestAlerts_ConditionalGetEmitsNothingOn304(t *testing.T) {
	e := newEnv(t)
	e.alertFeed("https://" + feedHost + "/alerts/feeds/1/2")
	var inm []string
	e.web.on(feedHost, "/alerts/feeds", func(w http.ResponseWriter, r *http.Request) {
		inm = append(inm, r.Header.Get("If-None-Match"))
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Last-Modified", "Sat, 10 Oct 2026 08:00:00 GMT")
		fmt.Fprint(w, atomFeed)
	})
	if got := e.tick(TickRequest{Sources: []string{SourceAlerts}}).Sources[0]; got.New != 1 {
		t.Fatalf("first = %+v", got)
	}
	second := e.tick(TickRequest{Sources: []string{SourceAlerts}}).Sources[0]
	if second.Docs != 0 || second.Status != SrcOK {
		t.Fatalf("second = %+v", second)
	}
	if len(inm) != 2 || inm[1] != `"v1"` {
		t.Fatalf("validators sent = %v", inm)
	}
	st, _ := e.svc.repo.CollectorState(e.ctx, SourceAlerts, "https://"+feedHost+"/alerts/feeds/1/2")
	if st.ETag != `"v1"` || st.LastModified == "" {
		t.Fatalf("validators were not kept: %+v", st)
	}
}

func TestAlerts_FeedURLMustBeHTTPSAndAFeed(t *testing.T) {
	for _, bad := range []string{"http://alerts.example.test/f", "ftp://x", "https://www.google.com/alerts", "not a url"} {
		if err := ValidateFeedURL(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if err := ValidateFeedURL("https://www.google.com/alerts/feeds/12/34"); err != nil {
		t.Errorf("a feed address was rejected: %v", err)
	}
	e := newEnv(t)
	e.alertFeed("https://" + feedHost + "/page")
	e.web.body(feedHost, "/page", "text/html", "<html><body>not a feed</body></html>")
	got := e.tick(TickRequest{Sources: []string{SourceAlerts}}).Sources[0]
	if got.Status != SrcFailed || !strings.Contains(got.Reason, "not a feed") {
		t.Fatalf("source = %+v", got)
	}
}

func hnSearchJSON(hits ...string) string { return `{"hits":[` + strings.Join(hits, ",") + `]}` }

func hnFixture(id, title, url string, at int64) string {
	b, _ := json.Marshal(map[string]any{"objectID": id, "title": title, "url": url, "created_at_i": at, "story_text": ""})
	return string(b)
}

func TestHN_StoryCursorAndHiringThreadTopLevelOnly(t *testing.T) {
	t.Run("story cursor", hnStoryCursor)
	t.Run("hiring thread", hnHiringReplyIsNotAPostAndCapIs300)
}

func hnStoryCursor(t *testing.T) {
	e := newEnv(t)
	e.enable(SourceHN, "series a startup")
	now := e.clk.now().Unix()
	e.web.body(hnHost, "/api/v1/search_by_date", "application/json", hnSearchJSON(
		hnFixture("1", "New A", "https://a.example.test/x", now-10), hnFixture("2", "New B", "", now-20), hnFixture("3", "New C", "https://c.example.test", now-30),
		hnFixture("4", "Old D", "https://d.example.test", now-1000), hnFixture("5", "Old E", "https://e.example.test", now-2000)))
	e.exec(`INSERT INTO signal_collector_state (source_id, scope, cursor, updated_at) VALUES (?, ?, ?, ?)`, SourceHN, "series a startup", now-100, fmtTS(e.clk.now()))
	sum := e.tick(TickRequest{Sources: []string{SourceHN}})
	if got := sum.Sources[0]; got.Docs != 3 {
		t.Fatalf("source = %+v", got)
	}
	st, _ := e.svc.repo.CollectorState(e.ctx, SourceHN, "series a startup")
	if st.Cursor != now-10 {
		t.Fatalf("cursor = %d, want %d", st.Cursor, now-10)
	}
	docs, _ := e.svc.repo.PendingDocuments(e.ctx, 0, 10)
	urls := map[string]bool{}
	for _, d := range docs {
		urls[d.URL] = true
	}
	if !urls["https://news.ycombinator.com/item?id=2"] || !urls["https://a.example.test/x"] {
		t.Fatalf("urls = %v", urls)
	}
}

func hnHiringReplyIsNotAPostAndCapIs300(t *testing.T) {
	e := newEnv(t)
	now := e.clk.now().Unix()
	var thread []string
	for i := 0; i < 45; i++ {
		title := "Ask HN: Who wants to be hired?"
		if i == 39 {
			title = "Ask HN: Who is hiring? (October 2026)"
		}
		thread = append(thread, hnFixture(fmt.Sprint(900+i), title, "", now-int64(i)*100))
	}
	e.web.body(hnHost, "/api/v1/search_by_date", "application/json", hnSearchJSON(thread...))
	var kids []map[string]any
	for i := 0; i < 400; i++ {
		kid := map[string]any{"id": 10000 + i, "created_at_i": now - int64(i), "text": fmt.Sprintf("Company%03d (Series B) | Onsite | Exampleton<p>We build things.", i),
			"children": []map[string]any{{"id": 50000 + i, "created_at_i": now, "text": "a reply"}, {"id": 60000 + i, "created_at_i": now, "text": "another reply"}}}
		kids = append(kids, kid)
	}
	b, _ := json.Marshal(map[string]any{"id": 939, "created_at_i": now - 3900, "children": kids})
	e.web.body(hnHost, "/api/v1/items/939", "application/json", string(b))
	sum := e.tick(TickRequest{Sources: []string{SourceHNHiring}})
	got := sum.Sources[0]
	if got.Status != SrcOK || got.Docs != 300 {
		t.Fatalf("source = %+v", got)
	}
	docs, _ := e.svc.repo.PendingDocuments(e.ctx, 0, 1000)
	for _, d := range docs {
		if id := atoi(d.URL[strings.LastIndex(d.URL, "=")+1:]); id >= 50000 {
			t.Fatalf("a reply became a document: %s", d.URL)
		}
	}
	// The oldest 300 come first, and the cursor stops at the newest of them.
	first := docs[0]
	if first.Title != "Company399 (Series B) | Onsite | Exampleton" || !strings.Contains(first.Body, "\n\nWe build things.") {
		t.Fatalf("doc = %q / %q", first.Title, first.Body)
	}
	if st, _ := e.svc.repo.CollectorState(e.ctx, SourceHNHiring, "939"); st.Cursor != now-100 {
		t.Fatalf("cursor = %d, want %d", st.Cursor, now-100)
	}
	// The second tick collects the comments beyond the cap, and a third finds nothing new.
	if again := e.tick(TickRequest{Sources: []string{SourceHNHiring}}).Sources[0]; again.Docs != 100 {
		t.Fatalf("second pass = %+v", again)
	}
	if st, _ := e.svc.repo.CollectorState(e.ctx, SourceHNHiring, "939"); st.Cursor != now {
		t.Fatalf("cursor = %d, want %d", st.Cursor, now)
	}
	if n := e.count(`SELECT COUNT(*) FROM signal_documents WHERE source_id = ?`, SourceHNHiring); n != 400 {
		t.Fatalf("%d of the 400 comments were collected", n)
	}
	if third := e.tick(TickRequest{Sources: []string{SourceHNHiring}}).Sources[0]; third.Docs != 0 {
		t.Fatalf("third pass = %+v", third)
	}
}

// Comments that share the second at which the cap falls are collected
// together, since the cursor cannot tell them apart.
func TestHNHiring_CapDoesNotSplitCommentsOfOneSecond(t *testing.T) {
	e := newEnv(t)
	now := e.clk.now().Unix()
	e.web.body(hnHost, "/api/v1/search_by_date", "application/json", hnSearchJSON(hnFixture("939", "Ask HN: Who is hiring? (October 2026)", "", now-3900)))
	var kids []map[string]any
	for i := 0; i < 305; i++ {
		at := now - 1000 + int64(i)
		if i >= 298 {
			at = now - 1000 + 298
		}
		kids = append(kids, map[string]any{"id": 10000 + i, "created_at_i": at, "text": fmt.Sprintf("Company%03d | Onsite", i)})
	}
	b, _ := json.Marshal(map[string]any{"id": 939, "created_at_i": now - 3900, "children": kids})
	e.web.body(hnHost, "/api/v1/items/939", "application/json", string(b))
	if got := e.tick(TickRequest{Sources: []string{SourceHNHiring}}).Sources[0]; got.Docs != 305 {
		t.Fatalf("source = %+v", got)
	}
}

func TestWebSearch_NoProviderKeySkipped_NoEngineScraping(t *testing.T) {
	e := newEnv(t)
	e.enable(SourceWeb, "series a hiring")
	got := e.tick(TickRequest{Sources: []string{SourceWeb}}).Sources[0]
	if got.Status != SrcSkipped || got.Reason != "no key" {
		t.Fatalf("source = %+v", got)
	}
	if n := len(e.web.requests("")); n != 0 {
		t.Fatalf("%d requests were sent without a provider key", n)
	}
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, _ := os.ReadFile(f)
		for _, banned := range []string{"bing.com/search", "duckduckgo.com/html", "google.com/search?"} {
			if strings.Contains(string(src), banned) && f != "views.go" {
				t.Errorf("%s names a consumer search engine page %q", f, banned)
			}
		}
	}
}

// A scoped search sends its hosts to the provider, so an excluded page takes no
// result slot; an unscoped one sends none.
func TestWebSearch_ScopeIsTheProvidersOwnFilter(t *testing.T) {
	e := newEnv(t)
	e.keys[KeyExa] = "k-" + strings.Repeat("x", 12)
	var bodies []map[string]any
	e.web.on(exaHost, "/search", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		fmt.Fprint(w, `{"results":[]}`)
	})
	p := e.svc.SearchProvider()
	if _, err := p.Search(e.ctx, "q", 10, Scope{Include: []string{"ats.example.test"}, Exclude: []string{"board.example.test"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Search(e.ctx, "q", 10, Scope{}); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || fmt.Sprint(bodies[0]["includeDomains"]) != "[ats.example.test]" || fmt.Sprint(bodies[0]["excludeDomains"]) != "[board.example.test]" {
		t.Fatalf("scoped body %v", bodies)
	}
	if _, ok := bodies[1]["includeDomains"]; ok {
		t.Fatalf("unscoped body %v", bodies[1])
	}
	if _, ok := bodies[1]["excludeDomains"]; ok {
		t.Fatalf("unscoped body %v", bodies[1])
	}
}

func TestWebSearch_QueryCapDefersTheRest(t *testing.T) {
	e := newEnv(t)
	var qs []string
	for i := 0; i < 14; i++ {
		qs = append(qs, fmt.Sprintf("query %d", i))
	}
	e.enable(SourceWeb, qs...)
	e.keys[KeyExa] = "k-" + strings.Repeat("x", 12)
	var asked []string
	e.web.on(exaHost, "/search", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query      string `json:"query"`
			NumResults int    `json:"numResults"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		asked = append(asked, body.Query)
		if body.NumResults != 10 || r.Header.Get("X-Api-Key") == "" {
			t.Errorf("numResults %d, key %q", body.NumResults, r.Header.Get("X-Api-Key"))
		}
		fmt.Fprintf(w, `{"results":[{"url":"https://p.example.test/%d","title":"Example raises","text":"%s","publishedDate":"2026-10-10T00:00:00Z"}]}`, len(asked), strings.Repeat("a", 9000))
	})
	got := e.tick(TickRequest{Sources: []string{SourceWeb}}).Sources[0]
	if len(asked) != 10 || len(got.Deferred) != 4 || got.Deferred[0] != "query 10" {
		t.Fatalf("asked %d, deferred %v", len(asked), got.Deferred)
	}
	docs, _ := e.svc.repo.PendingDocuments(e.ctx, 0, 100)
	if len(docs) != 10 || len([]rune(docs[0].Body)) != 8000 {
		t.Fatalf("docs %d, body %d", len(docs), len([]rune(docs[0].Body)))
	}
	// At least two seconds between provider requests.
	reqs := e.web.requests(exaHost)
	for i := 1; i < len(reqs); i++ {
		if reqs[i].At.Sub(reqs[i-1].At) < 2*time.Second {
			t.Fatalf("requests %d and %d are %v apart", i-1, i, reqs[i].At.Sub(reqs[i-1].At))
		}
	}
	// The next tick starts with the deferred queries and goes round.
	asked = nil
	got = e.tick(TickRequest{Sources: []string{SourceWeb}}).Sources[0]
	if len(asked) != 10 || asked[0] != "query 10" || asked[3] != "query 13" || asked[4] != "query 0" || asked[9] != "query 5" {
		t.Fatalf("the second tick asked %v", asked)
	}
	if len(got.Deferred) != 4 || got.Deferred[0] != "query 6" || got.Deferred[3] != "query 9" {
		t.Fatalf("the second tick deferred %v", got.Deferred)
	}
}

func xTweet(id string, likes, reposts, views, followers int, verified, reply bool, at time.Time) map[string]any {
	return map[string]any{"id": id, "text": "Example Corp is hiring engineers " + id, "createdAt": at.Format(time.RubyDate), "likeCount": likes, "retweetCount": reposts,
		"viewCount": views, "isReply": reply, "author": map[string]any{"userName": "alex_example", "followers": followers, "isBlueVerified": verified}}
}

func TestXCollector_EngagementThresholdsHalvedForVerified(t *testing.T) {
	e := newEnv(t)
	e.enable(SourceX, "hiring engineers")
	e.keys[KeyX] = "x-" + strings.Repeat("k", 12)
	at := e.clk.now().Add(-time.Hour)
	top := []map[string]any{
		xTweet("1", 12, 4, 900, 300, false, false, at),   // dropped: reposts
		xTweet("2", 12, 4, 900, 300, true, false, at),    // kept: verified, threshold 2.5
		xTweet("3", 50, 20, 5000, 900, false, false, at), // kept
		xTweet("4", 50, 20, 5000, 900, false, true, at),  // reply
		xTweet("5", 50, 20, 5000, 99, false, false, at),  // dropped: followers
	}
	e.web.on(xHost, "/twitter/tweet/advanced_search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		list := top
		if q.Get("queryType") == "Latest" {
			list = append([]map[string]any{xTweet("3", 50, 20, 5000, 900, false, false, at)}, xTweet("6", 10, 5, 500, 100, false, false, at))
		}
		if r.Header.Get("X-Api-Key") == "" {
			t.Error("no key header")
		}
		b, _ := json.Marshal(map[string]any{"tweets": list, "has_next_page": false})
		w.Write(b)
	})
	got := e.tick(TickRequest{Sources: []string{SourceX}}).Sources[0]
	if got.Status != SrcOK || got.Docs != 3 {
		t.Fatalf("source = %+v", got)
	}
	docs, _ := e.svc.repo.PendingDocuments(e.ctx, 0, 10)
	ids := map[string]bool{}
	for _, d := range docs {
		ids[d.URL[strings.LastIndex(d.URL, "/")+1:]] = true
	}
	if !ids["2"] || !ids["3"] || !ids["6"] || ids["1"] || ids["4"] || ids["5"] {
		t.Fatalf("kept = %v", ids)
	}
	var passes []string
	for _, r := range e.web.requests(xHost) {
		passes = append(passes, r.Query)
	}
	if len(passes) != 2 || !strings.Contains(passes[0], "queryType=Top") || !strings.Contains(passes[0], "min_faves") || !strings.Contains(passes[1], "queryType=Latest") {
		t.Fatalf("passes = %v", passes)
	}
}

func TestXCollector_BillingFailureIsNonRetryable(t *testing.T) {
	t.Run("no key", xCollectorNoKeyIsSkipped)
	t.Run("billing", xCollectorPaymentErrorIsNotRetried)
}

func xCollectorNoKeyIsSkipped(t *testing.T) {
	e := newEnv(t)
	e.enable(SourceX, "hiring engineers")
	if got := e.tick(TickRequest{Sources: []string{SourceX}}).Sources[0]; got.Status != SrcSkipped || got.Reason != "no key" {
		t.Fatalf("no key: %+v", got)
	}
}

func xCollectorPaymentErrorIsNotRetried(t *testing.T) {
	e := newEnv(t)
	e.enable(SourceX, "hiring engineers")
	e.keys[KeyX] = "x-" + strings.Repeat("k", 12)
	e.web.status(xHost, "/twitter", http.StatusPaymentRequired)
	got := e.tick(TickRequest{Sources: []string{SourceX}}).Sources[0]
	if got.Status != SrcFailed || !strings.Contains(got.Reason, "payment") {
		t.Fatalf("source = %+v", got)
	}
	if n := len(e.web.requests(xHost)); n != 1 {
		t.Fatalf("a billing error was retried: %d requests", n)
	}
	_, retry := ClassifyStatus(SourceX, got.Reason)
	if retry {
		t.Fatalf("the failure reads as retryable: %q", got.Reason)
	}
}

func TestXCollector_CursorAdvancesOnlyAfterBothPasses(t *testing.T) {
	e := newEnv(t)
	e.enable(SourceX, "hiring engineers")
	e.keys[KeyX] = "x-" + strings.Repeat("k", 12)
	at := e.clk.now().Add(-time.Hour).Truncate(time.Second)
	e.web.on(xHost, "/twitter/tweet/advanced_search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("queryType") == "Latest" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		b, _ := json.Marshal(map[string]any{"tweets": []map[string]any{xTweet("3", 50, 20, 5000, 900, false, false, at)}})
		w.Write(b)
	})
	got := e.tick(TickRequest{Sources: []string{SourceX}}).Sources[0]
	if got.Docs != 1 {
		t.Fatalf("source = %+v", got)
	}
	if st, _ := e.svc.repo.CollectorState(e.ctx, SourceX, "hiring engineers"); st.Cursor != 0 {
		t.Fatalf("the cursor moved although the Latest pass failed: %d", st.Cursor)
	}
}
