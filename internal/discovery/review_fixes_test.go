package discovery

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestScan_InboxKeyTakenMidScanIsADuplicate(t *testing.T) {
	e := newEnv(t)
	e.serveGH("example-co", []obj{ghItem(1, "Software Engineer", "Austin, TX")}, nil)
	e.add(ghBoard("example-co"))
	done := false
	e.srv.hook = func(path string) {
		if path == "/v1/boards/example-co/jobs" && !done {
			done = true
			if _, err := e.rp.InboxAddURL(e.ctx, "https://job-boards.greenhouse.io/example-co/jobs/1"); err != nil {
				t.Errorf("add: %v", err)
			}
		}
	}
	rec := e.run(ScanRequest{Portals: eng()})
	if rec.Status != RunCompleted || rec.Counts.Duplicates[StoreInbox] != 1 || rec.Counts.Inserted != 0 {
		t.Fatalf("status=%s err=%s counts=%+v", rec.Status, rec.Error, rec.Counts)
	}
	if err := rec.Counts.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestFetch_RedirectToAnotherHostIsTransientNeverAStrike(t *testing.T) {
	e := newEnv(t)
	var portalHits atomic.Int32
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		portalHits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>Sign in to the hotel network</html>"))
	}))
	defer portal.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, portal.URL+"/login", http.StatusFound)
	}))
	defer redir.Close()
	e.sc.Endpoints = Endpoints{Greenhouse: redir.URL}
	e.add(ghBoard("a-co"), ghBoard("b-co"))
	for i := 0; i < 4; i++ {
		rec := e.run(ScanRequest{Portals: eng()})
		if rec.Counts.Inserted != 0 {
			t.Fatalf("run %d inserted rows", i)
		}
	}
	for _, slug := range []string{"a-co", "b-co"} {
		if row := e.registry(ghBoard(slug)); row.Strikes != 0 || row.Status != StatusActive {
			t.Fatalf("%s = %+v", slug, row)
		}
	}
	if portalHits.Load() != 0 {
		t.Fatal("a redirect to another host was followed")
	}
}

func TestFetch_NonJSONBodyIsTransientNotAStrike(t *testing.T) {
	e := newEnv(t)
	e.srv.set("/v1/boards/x-co/jobs", route{body: "<html>nope</html>"})
	e.add(ghBoard("x-co"))
	e.stableBoard("y-co")
	e.add(ghBoard("y-co"))
	for i := 0; i < 3; i++ {
		e.run(ScanRequest{Portals: eng()})
	}
	if row := e.registry(ghBoard("x-co")); row.Strikes != 0 || row.Status != StatusActive {
		t.Fatalf("row = %+v", row)
	}
	// JSON of the wrong shape is still an unparsable payload.
	e.srv.set("/v1/boards/x-co/jobs", route{body: `{"jobs": "none"}`})
	e.run(ScanRequest{Portals: eng()})
	if e.registry(ghBoard("x-co")).Strikes != 1 {
		t.Fatal("a JSON payload of the wrong shape must strike")
	}
}

func TestFetch_OversizedBodyIsTransientNotAStrike(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"jobs":[`))
		chunk := []byte(strings.Repeat(" ", 1<<20))
		for i := 0; i < maxBody>>20+1; i++ {
			_, _ = w.Write(chunk)
		}
		_, _ = w.Write([]byte(`]}`))
	}))
	defer srv.Close()
	c := NewClient()
	c.MinGap = 0
	_, err := c.FetchBoard(context.Background(), Endpoints{Greenhouse: srv.URL}, ghBoard("big-co"), "")
	var te *TransientError
	if !errors.As(err, &te) {
		t.Fatalf("err = %v, want a transient error", err)
	}
}

// A board on Lever's EU host is served by the EU API only; the global API
// answers 404 for it. A board both APIs miss is gone.
func TestFetch_ALeverBoardTheGlobalAPIMissesIsReadFromTheEUAPI(t *testing.T) {
	global := httptest.NewServer(http.NotFoundHandler())
	defer global.Close()
	eu := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/postings/eu-co" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[{"id":"0a1b2c3d-1111-2222-3333-444455556666","text":"Platform Engineer","hostedUrl":"https://jobs.eu.lever.co/eu-co/0a1b2c3d-1111-2222-3333-444455556666","categories":{"location":"Berlin"}}]`))
	}))
	defer eu.Close()
	c := NewClient()
	c.MinGap = 0
	ep := Endpoints{Lever: global.URL, LeverEU: eu.URL}
	res, err := c.FetchBoard(context.Background(), ep, Board{ATS: Lever, Slug: "eu-co"}, "")
	if err != nil || len(res.Postings) != 1 || res.Postings[0].URL != "https://jobs.eu.lever.co/eu-co/0a1b2c3d-1111-2222-3333-444455556666" {
		t.Fatalf("postings = %+v, err = %v", res.Postings, err)
	}
	var gone *BoardFailure
	if _, err := c.FetchBoard(context.Background(), ep, Board{ATS: Lever, Slug: "no-co"}, ""); !errors.As(err, &gone) || gone.Status != http.StatusNotFound {
		t.Fatalf("err = %v, want a board failure with 404", err)
	}
}

func TestSweep_NoStrikesFlushedWhenNoVisitSucceeded(t *testing.T) {
	e := newEnv(t)
	e.srv.set("/v1/boards/gone-co/jobs", route{status: 404})
	e.add(ghBoard("gone-co"))
	for i := 0; i < 4; i++ {
		e.run(ScanRequest{Portals: eng()})
	}
	if row := e.registry(ghBoard("gone-co")); row.Strikes != 0 || row.Status != StatusActive {
		t.Fatalf("row = %+v", row)
	}
}

func TestScan_UnsafeAndMalformedPostingURLsAreDroppedAndCounted(t *testing.T) {
	e := newEnv(t)
	urls := []string{
		"javascript:alert(1)", "file:///etc/hosts", "ftp://example.com/x", "http://127.0.0.1:47319/api/runs",
		"http://localhost/x", "https://10.0.0.5/jobs/1", "https://192.168.1.9/jobs/1", "http://[::1]/x",
		"https://169.254.169.254/latest", "https://printer.local/jobs/1", "not a url",
	}
	var jobs []obj
	for i, u := range urls {
		it := ghItem(100+i, "Software Engineer", "Austin, TX")
		it["absolute_url"] = u
		jobs = append(jobs, it)
	}
	good := ghItem(1, "Software Engineer Good", "Austin, TX")
	good["absolute_url"] = "HTTP://Job-Boards.Greenhouse.io/example-co/jobs/1/?utm_source=feed&gh_src=abc#apply"
	jobs = append(jobs, good)
	e.serveGH("example-co", jobs, nil)
	e.add(ghBoard("example-co"))
	rec := e.run(ScanRequest{Portals: eng()})
	if rec.Status != RunCompleted || rec.Counts.Inserted != 1 || rec.Counts.Filtered[FilterInvalidURL] != len(urls) {
		t.Fatalf("status=%s counts=%+v", rec.Status, rec.Counts)
	}
	if err := rec.Counts.Validate(); err != nil {
		t.Fatal(err)
	}
	rows := e.inbox(InboxQuery{})
	if len(rows) != 1 || rows[0].URL != "https://job-boards.greenhouse.io/example-co/jobs/1" || rows[0].OriginalURL != good["absolute_url"] {
		t.Fatalf("rows = %+v", rows)
	}
	h, err := e.rp.HistoryGet(e.ctx, "https://job-boards.greenhouse.io/example-co/jobs/1")
	if err != nil || h.Status != HistoryInbox {
		t.Fatalf("history = %+v %v", h, err)
	}
}

func TestInboxAddURL_RefusesLoopbackAndPrivateHosts(t *testing.T) {
	e := newEnv(t)
	for _, u := range []string{"http://127.0.0.1:47319/api", "http://localhost/x", "https://10.1.2.3/jobs/1", "javascript:alert(1)"} {
		if _, err := e.rp.InboxAddURL(e.ctx, u); err == nil {
			t.Errorf("%s was added", u)
		}
	}
}

func TestIdentity_GreenhousePostingOnTheEmployersSiteGetsTheNativeKey(t *testing.T) {
	e := newEnv(t)
	it := ghItem(77, "Software Engineer", "Austin, TX")
	it["absolute_url"] = "https://careers.example.com/open-roles?gh_jid=77"
	e.serveGH("example-co", []obj{it}, nil)
	e.add(ghBoard("example-co"))
	rec := e.run(ScanRequest{Portals: eng()})
	rows := e.inbox(InboxQuery{})
	if rec.Counts.Inserted != 1 || len(rows) != 1 || rows[0].IdentityKey != "greenhouse:example-co/77" {
		t.Fatalf("rows = %+v counts = %+v", rows, rec.Counts)
	}
	again, err := e.rp.InboxAddURL(e.ctx, "https://job-boards.greenhouse.io/example-co/jobs/77")
	if err != nil || again.Row != nil || again.Duplicate != StoreInbox || again.MatchID != rows[0].ID {
		t.Fatalf("add by the Greenhouse URL = %+v %v", again, err)
	}
}

func TestPosting_NativeKeyForEveryATS(t *testing.T) {
	for _, tc := range []struct{ ats, board, id, want string }{
		{Greenhouse, "Example-Co", "77", "greenhouse:example-co/77"},
		{Lever, "example-co", "0B1C2D3E-aaaa-bbbb-cccc-1234567890ab", "lever:example-co/0b1c2d3e-aaaa-bbbb-cccc-1234567890ab"},
		{Ashby, "example-co", "0b1c2d3e-aaaa-bbbb-cccc-1234567890ab", "ashby:example-co/0b1c2d3e-aaaa-bbbb-cccc-1234567890ab"},
		{SmartRecruiters, "ExampleCo", "743999001", "smartrecruiters:exampleco/743999001"},
		{Workable, "example-co", "AB12CD34EF", "workable:example-co/ab12cd34ef"},
	} {
		p := Posting{ATS: tc.ats, Board: tc.board, JobID: tc.id, URL: "https://careers.example.com/x?id=1"}
		if got := p.key(); got != tc.want {
			t.Errorf("%s: key = %s, want %s", tc.ats, got, tc.want)
		}
	}
	if got := (Posting{ATS: Greenhouse, URL: "https://careers.example.com/x"}).key(); got != "https://careers.example.com/x" {
		t.Errorf("a posting with no board keeps its URL key, got %s", got)
	}
}

func TestFetchSmartRecruiters_AllEntriesDroppedIsAnEmptyListFailure(t *testing.T) {
	e := newEnv(t)
	page := srPage(2, 0, 2, "ExampleSr")
	for _, c := range page["content"].([]obj) {
		c["name"] = ""
	}
	e.srv.json("/v1/companies/examplesr/postings?offset=0", page)
	_, err := e.sc.Client.FetchBoard(e.ctx, e.sc.Endpoints, Board{ATS: SmartRecruiters, Slug: "examplesr"}, "")
	var fail *BoardFailure
	if !errors.As(err, &fail) {
		t.Fatalf("err = %v, want a board failure", err)
	}
}
