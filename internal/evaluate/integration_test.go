// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"applysmith/internal/discovery"
	"applysmith/internal/llm"
	"applysmith/internal/notify"
	"applysmith/internal/workspace"
)

func TestScannerGate_UsesTheSameRulesAsTheEvaluationEntry(t *testing.T) {
	p := testProfile().Profile
	p.WorkAuthorization.RequiresSponsorshipNow = ptr(true)
	p.EligibilityGate.EmployerTiers = map[string]workspace.TierPolicy{"consultancy": {Policy: "block"}}
	blocked := func(c string) bool { return NormalizeCompany(c) == "blocked example" }
	gate := ScannerGate(p, blocked)

	row := discovery.InboxRow{URL: "https://jobs.example.test/p/1", Company: "Example Systems", Role: "Software Engineer", Location: "Remote - United States", Description: postingText("")}
	if got := gate(row); got != "" {
		t.Fatalf("a clean row passes, got %q", got)
	}
	row.Role, row.Location = "Engineering Director", "Berlin, Germany"
	if got := gate(row); got != "seniority,geography" {
		t.Fatalf("got %q", got)
	}
	row = discovery.InboxRow{URL: "https://boards.greenhouse.io/exampleco", Company: "Blocked Example", Role: "Engineer"}
	if got := gate(row); got != "board_index,blocked_employer" {
		t.Fatalf("got %q", got)
	}
	row = discovery.InboxRow{URL: "https://jobs.example.test/p/2", Company: "Accenture", Role: "Engineer"}
	if got := gate(row); got != "blocked_employer" {
		t.Fatalf("a blocked tier stops the posting at promotion, got %q", got)
	}
	row = discovery.InboxRow{URL: "https://jobs.example.test/p/3", Company: "Example Systems", Role: "Engineer", Description: "Active TS/SCI clearance required."}
	if got := gate(row); got != "clearance" {
		t.Fatalf("got %q", got)
	}
	if ScannerGate(workspace.Profile{}, nil)(row) != "" {
		t.Fatal("a neutral profile rejects nothing but board indexes and blocked employers")
	}
}

// queueRow sends a pending inbox row to an evaluation the way every caller
// does, with a job that only has an id.
func queueRow(repo *discovery.Repo, id int64, jobID string) error {
	_, _, err := repo.QueueForEvaluation(context.Background(), discovery.InboxRow{ID: id}, nil, func() (string, error) { return jobID, nil })
	return err
}

func TestDiscoveryAdapters_InboxAndBlockedStore(t *testing.T) {
	e := newTenv(t)
	repo := discovery.NewRepo(e.st, nil)
	ctx := context.Background()
	add, err := repo.InboxAddURL(ctx, "https://jobs.lever.co/example-lever/5d1c0a9e-0000-4000-8000-000000000001")
	if err != nil || add.Row == nil {
		t.Fatalf("add: %+v %v", add, err)
	}
	id := add.Row.ID
	inbox := DiscoveryInbox{Repo: repo}

	if err := queueRow(repo, id, "job1"); err != nil {
		t.Fatal(err)
	}
	if waits, err := inbox.Release(ctx, id, "evaluation failed: timeout", discovery.ChargeAttempt); err != nil || !waits {
		t.Fatalf("release: %v, the drain takes the row again %v", err, waits)
	}
	row, _ := repo.InboxGet(ctx, id)
	if row.Status != discovery.InboxPending || !strings.Contains(row.Notes, "timeout") {
		t.Fatalf("row = %+v", row)
	}
	if err := queueRow(repo, id, "job2"); err != nil {
		t.Fatal(err)
	}
	if err := inbox.Done(ctx, id, Result{Outcome: OutcomeReported}); err != nil {
		t.Fatal(err)
	}
	if row, _ = repo.InboxGet(ctx, id); row.Status != discovery.InboxEvaluated {
		t.Fatalf("row = %+v", row)
	}
	// A row that is no longer queued has nothing left to move, and no drain
	// takes it.
	if waits, err := inbox.Release(ctx, id, "late", discovery.ChargeAttempt); err != nil || waits {
		t.Fatalf("release of a settled row: %v, the drain takes it %v", err, waits)
	}

	if _, err := repo.BlockCompany(ctx, "Blocked Example, Inc.", "test", "user"); err != nil {
		t.Fatal(err)
	}
	isBlocked := BlockedFromDiscovery(repo)
	if !isBlocked("Blocked Example") || isBlocked("Another Example") {
		t.Fatal("the blocked store must decide")
	}
	e.blocked = nil
	e.reader = nil
	e.rebuild()
	e.ev.d.Blocked = isBlocked
	if res := e.run(req("bl", func(r *Request) { r.Company = "Blocked Example" })); res.Outcome != OutcomeGated {
		t.Fatalf("result = %+v", res)
	}
}

// The evaluation gate blocks the employers a scan drops: any name of the
// company in the blocked store, and the skip_companies of portals.yml.
func TestBlockedFromDiscovery_IsTheScansBlockedRule(t *testing.T) {
	e := newTenv(t)
	repo := discovery.NewRepo(e.st, nil)
	var portals workspace.Portals
	portals.Filters.SkipCompanies = []string{"Skipped"}
	repo.RankWith(func() (workspace.Profile, workspace.Portals, error) { return workspace.Profile{}, portals, nil })
	if _, err := repo.BlockCompany(context.Background(), "Blocked Example", "test", "user"); err != nil {
		t.Fatal(err)
	}
	isBlocked := BlockedFromDiscovery(repo)
	for company, want := range map[string]bool{
		"Blocked Example": true, "Parent Example (Blocked Example)": true, "Skipped Example Labs": true, "Another Example": false,
	} {
		if got := isBlocked(company); got != want {
			t.Errorf("blocked(%q) = %v, want %v", company, got, want)
		}
	}
}

func TestPoolEnqueuer_StartsEvaluationsForAScan(t *testing.T) {
	e := newTenv(t)
	stub := newStub()
	e.model = stub
	e.rebuild()
	p := startPool(t, e, PoolOptions{})
	id, err := PoolEnqueuer{Pool: p}.Enqueue(context.Background(), discovery.EvalRequest{InboxID: 3, URL: "https://jobs.example.test/p/pe", Mode: "auto", RunID: "scan-abc", Source: "linkedin"})
	if err != nil || id == "" {
		t.Fatalf("id %q err %v", id, err)
	}
	if p.Stats().Active+p.Stats().Queued != 1 {
		t.Fatalf("stats = %+v", p.Stats())
	}
	var mode, priority string
	_ = e.st.Read().QueryRow(`SELECT mode, priority FROM evaluation_jobs WHERE run_id = ?`, id).Scan(&mode, &priority)
	if mode != "auto" || priority != "scan" {
		t.Fatalf("mode %s priority %s", mode, priority)
	}
	// The job names the scan run that queued it, so its outcome reaches that run.
	var in string
	_ = e.st.Read().QueryRow(`SELECT input_json FROM runs WHERE id = ?`, id).Scan(&in)
	if !strings.Contains(in, `"ScanRun":"scan-abc"`) {
		t.Fatalf("input = %s", in)
	}
	stub.release <- struct{}{}
}

// The date a source gave a posting reaches the evaluation, whose "over 60
// days old" signal reads it: from a scan's or a drain's request, and from the
// row of an evaluation the user asked for.
func TestRequest_CarriesThePostingsDate(t *testing.T) {
	e := newTenv(t)
	stub := newStub()
	e.model = stub
	e.rebuild()
	p := startPool(t, e, PoolOptions{})
	posted := testNow.Add(-90 * 24 * time.Hour)
	id, err := PoolEnqueuer{Pool: p}.Enqueue(context.Background(), discovery.EvalRequest{InboxID: 4, URL: "https://jobs.example.test/p/old", Mode: "auto", PostedAt: posted})
	if err != nil {
		t.Fatal(err)
	}
	var in string
	_ = e.st.Read().QueryRow(`SELECT input_json FROM runs WHERE id = ?`, id).Scan(&in)
	var stored Request
	if err := json.Unmarshal([]byte(in), &stored); err != nil || !stored.PostedAt.Equal(posted) {
		t.Fatalf("stored request posted %v (%v), want %v", stored.PostedAt, err, posted)
	}
	stub.release <- struct{}{}

	var fromRow Request
	fromRow.FillFromInbox(discovery.InboxRow{URL: "https://jobs.example.test/p/old-row", PostedAt: posted})
	if !fromRow.PostedAt.Equal(posted) {
		t.Fatalf("a request filled from its row posted %v, want %v", fromRow.PostedAt, posted)
	}
}

func TestATSReader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/boards/exampleco/jobs/4012345"):
			_, _ = w.Write([]byte(`{"title":"Backend Engineer","company_name":"Example Co","location":{"name":"Remote"},"content":"&lt;p&gt;Build &amp;amp; run services.&lt;/p&gt;"}`))
		case strings.HasPrefix(r.URL.Path, "/v1/boards/exampleco/jobs/4012346"):
			_, _ = w.Write([]byte(`{"title":"Security Engineer","company_name":"Example Co","location":{"name":"san_francisconew_york"},"offices":[{"name":"San Francisco / New York"}],"content":"x"}`))
		case strings.HasPrefix(r.URL.Path, "/v0/postings/exampleco/0a1b2c3d-1111-2222-3333-444455556666"):
			_, _ = w.Write([]byte(`{"text":"Platform Engineer","descriptionPlain":"Run the platform.","additionalPlain":"Benefits.","lists":[{"text":"What we require","content":"<li>Five years of Go</li><li>On-call &amp; operations</li>"}],"categories":{"location":"Austin, TX"}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/postings/exampleco/0a1b2c3d-1111-2222-3333-444455556667"):
			_, _ = w.Write([]byte(`{"text":"Backend Engineer","descriptionPlain":"x","categories":{"location":"Remote"},"country":"US","workplaceType":"remote"}`))
		case strings.HasPrefix(r.URL.Path, "/v0/postings/exampleco/0a1b2c3d-1111-2222-3333-444455556668"):
			_, _ = w.Write([]byte(`{"text":"Backend Engineer","descriptionPlain":"x","categories":{"location":"London, UK"},"country":"GB"}`))
		case strings.HasPrefix(r.URL.Path, "/posting-api/job-board/exampleco"):
			_, _ = w.Write([]byte(`{"jobs":[{"id":"0a1b2c3d-1111-2222-3333-444455556666","title":"Data Engineer","location":"Remote","descriptionPlain":"Move data.","compensation":{"compensationTierSummary":"$100,000 - $120,000"}},` +
				`{"id":"0a1b2c3d-1111-2222-3333-444455556667","title":"Backend Engineer","location":"Remote","address":{"postalAddress":{"addressCountry":"United States"}},"descriptionPlain":"x"},` +
				`{"id":"0a1b2c3d-1111-2222-3333-444455556668","title":"Backend Engineer","location":"London, UK","address":{"postalAddress":{"addressCountry":"United Kingdom"}},"descriptionPlain":"x"}]}`))
		case strings.HasPrefix(r.URL.Path, "/v1/boards/exampleco/jobs/4290000"):
			w.WriteHeader(http.StatusTooManyRequests)
		case strings.HasPrefix(r.URL.Path, "/v1/boards/exampleco/jobs/9990000"):
			w.WriteHeader(999)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	r := ATSReader{Hosts: map[string]string{"greenhouse": srv.URL, "lever": srv.URL, "ashby": srv.URL}}
	ctx := context.Background()

	gh, err := r.Read(ctx, "https://boards.greenhouse.io/exampleco/jobs/4012345")
	if err != nil || gh.Title != "Backend Engineer" || gh.Company != "Example Co" || gh.Method != "http" || gh.Result != "live" || !strings.Contains(gh.Text, "<p>Build") {
		t.Fatalf("greenhouse: %+v %v", gh, err)
	}
	if clean := CleanText(gh.Text); !strings.Contains(clean, "Build & run services.") || strings.Contains(clean, "<p>") {
		t.Fatalf("clean = %q", clean)
	}
	// A location name that is a machine slug ("san_francisconew_york") names
	// no place; the posting's offices do.
	for id, want := range map[string]string{"4012345": "Remote", "4012346": "San Francisco / New York"} {
		got, err := r.Read(ctx, "https://boards.greenhouse.io/exampleco/jobs/"+id)
		if err != nil || got.Location != want {
			t.Fatalf("greenhouse %s location = %q, %v; want %q", id, got.Location, err, want)
		}
	}
	lv, err := r.Read(ctx, "https://jobs.lever.co/exampleco/0a1b2c3d-1111-2222-3333-444455556666")
	if err != nil || lv.Title != "Platform Engineer" || lv.Location != "Austin, TX" || !strings.Contains(lv.Text, "Benefits.") {
		t.Fatalf("lever: %+v %v", lv, err)
	}
	// The requirement lists are part of the posting, one line per item.
	if want := "Run the platform.\nWhat we require\n- Five years of Go\n- On-call & operations\nBenefits."; lv.Text != want {
		t.Fatalf("lever text = %q, want %q", lv.Text, want)
	}
	// Lever's location chip ("Remote") names no country; the posting's own
	// country code completes it, and a location that names one keeps it.
	for id, want := range map[string]string{"0a1b2c3d-1111-2222-3333-444455556667": "Remote, United States", "0a1b2c3d-1111-2222-3333-444455556668": "London, UK"} {
		got, err := r.Read(ctx, "https://jobs.lever.co/exampleco/"+id)
		if err != nil || got.Location != want {
			t.Fatalf("lever %s location = %q, %v; want %q", id, got.Location, err, want)
		}
	}
	ab, err := r.Read(ctx, "https://jobs.ashbyhq.com/exampleco/0a1b2c3d-1111-2222-3333-444455556666")
	if err != nil || ab.Title != "Data Engineer" || ab.Compensation != "$100,000 - $120,000" || ab.Location != "Remote" {
		t.Fatalf("ashby: %+v %v", ab, err)
	}
	// Acorns (Ashby, live): a "Remote" location names no country; the
	// posting's address does, as Lever's country code does.
	for id, want := range map[string]string{"0a1b2c3d-1111-2222-3333-444455556667": "Remote, United States", "0a1b2c3d-1111-2222-3333-444455556668": "London, UK"} {
		got, err := r.Read(ctx, "https://jobs.ashbyhq.com/exampleco/"+id)
		if err != nil || got.Location != want {
			t.Fatalf("ashby %s location = %q, %v; want %q", id, got.Location, err, want)
		}
	}
	if _, err := r.Read(ctx, "https://boards.greenhouse.io/exampleco/jobs/9999999"); err == nil {
		t.Fatal("a missing posting is an error")
	}
	// An API that throttles the read, with 429 or LinkedIn's 999, says nothing
	// about the posting, as a page's throttle does (flow round 10, EF-3).
	for _, id := range []string{"4290000", "9990000"} {
		if _, err := r.Read(ctx, "https://boards.greenhouse.io/exampleco/jobs/"+id); !throttled(err) {
			t.Fatalf("a throttled read: %v", err)
		}
	}
	if _, err := r.Read(ctx, "https://careers.example.test/job/1"); err != ErrNeedsBrowser {
		t.Fatalf("err = %v", err)
	}
}

// Ashby answers with the whole job board, descriptions included; Harvey's
// passed 5 MB and was cut at the old 4 MiB bound, so each of its postings
// failed as unreadable with "unexpected EOF". A board within the bound is
// read, and one beyond it says it is too large.
func TestATSReader_ALargeAshbyBoardIsRead(t *testing.T) {
	const id = "0a1b2c3d-1111-2222-3333-444455556666"
	var b strings.Builder
	b.WriteString(`{"jobs":[`)
	filler := strings.Repeat("Build and run the services our customers rely on. ", 200)
	for i := range 600 {
		fmt.Fprintf(&b, `{"id":"other-%d","title":"Role %d","descriptionPlain":%q},`, i, i, filler)
	}
	fmt.Fprintf(&b, `{"id":%q,"title":"Legal Engineer","location":"New York","descriptionPlain":"Build legal AI."}]}`, id)
	body := b.String()
	if len(body) < 5<<20 {
		t.Fatalf("the board is %d bytes, want more than 5 MB", len(body))
	}
	boards := map[string]string{"exampleco": body, "hugeco": `{"jobs":[` + strings.Repeat(" ", 33<<20) + `]}`}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, boards[strings.TrimPrefix(r.URL.Path, "/posting-api/job-board/")])
	}))
	defer srv.Close()
	r := ATSReader{Hosts: map[string]string{"ashby": srv.URL}}
	got, err := r.Read(context.Background(), "https://jobs.ashbyhq.com/exampleco/"+id)
	if err != nil || got.Title != "Legal Engineer" || got.Text != "Build legal AI." {
		t.Fatalf("a 5 MB board: %+v %v", got, err)
	}
	if _, err := r.Read(context.Background(), "https://jobs.ashbyhq.com/hugeco/"+id); err == nil || !strings.Contains(err.Error(), "more than 32 MB") {
		t.Fatalf("a board past the bound: %v", err)
	}
}

// Ashby answers a posting only with its whole board, so a batch of
// evaluations on one board shares one fetch: concurrent reads wait for the one
// in flight, later reads within the cache's lifetime reuse it, and a read past
// that lifetime fetches the board again.
func TestATSReader_EvaluationsOfOneAshbyBoardFetchItOnce(t *testing.T) {
	const n = 6
	var b strings.Builder
	b.WriteString(`{"jobs":[`)
	for i := range n {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":"0a1b2c3d-1111-2222-3333-00000000000%d","title":"Role %d","descriptionPlain":"Build things."}`, i, i)
	}
	b.WriteString(`]}`)
	var fetches atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		<-release
		_, _ = io.WriteString(w, b.String())
	}))
	defer srv.Close()
	now := testNow
	boards := &BoardCache{now: func() time.Time { return now }}
	r := ATSReader{Hosts: map[string]string{"ashby": srv.URL}, Boards: boards}
	read := func(i int) error {
		got, err := r.Read(context.Background(), fmt.Sprintf("https://jobs.ashbyhq.com/exampleco/0a1b2c3d-1111-2222-3333-00000000000%d", i))
		if err == nil && got.Title != fmt.Sprintf("Role %d", i) {
			err = fmt.Errorf("read %+v", got)
		}
		return err
	}
	errs := make(chan error, n)
	for i := range n {
		go func() { errs <- read(i) }()
	}
	waitFor(t, "the other reads to join the fetch", func() bool { return boards.flight.Joined() == n-1 })
	close(release)
	for range n {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if err := read(2); err != nil {
		t.Fatal(err)
	}
	if got := fetches.Load(); got != 1 {
		t.Fatalf("%d evaluations of one board made %d fetches, want 1", n+1, got)
	}
	now = now.Add(boardCacheLife + time.Second)
	if err := read(3); err != nil || fetches.Load() != 2 {
		t.Fatalf("a read past the cache's life: %v, %d fetches", err, fetches.Load())
	}
}

// leverRegions answers as Lever's two APIs do for a board on the EU host: the
// EU API holds the posting and the global one answers 404.
type leverRegions struct{ asked []string }

func (l *leverRegions) RoundTrip(r *http.Request) (*http.Response, error) {
	l.asked = append(l.asked, r.URL.Host)
	status, body := http.StatusNotFound, `{"ok":false,"error":"Document not found"}`
	if r.URL.Host == "api.eu.lever.co" {
		status, body = http.StatusOK, `{"text":"Software Tools Engineer","descriptionPlain":"Build tools.","categories":{"location":"Austin, Texas"},"country":"US"}`
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
}

// A posting on Lever's EU host is read from Lever's EU API (Cirrus Logic's
// board answered 404 from the global one, so the run lost the posting's place).
func TestATSReader_LeverEUBoardReadsTheEUAPI(t *testing.T) {
	rt := &leverRegions{}
	r := ATSReader{Client: &http.Client{Transport: rt}}
	got, err := r.Read(context.Background(), "https://jobs.eu.lever.co/cirrus/2e9a0d0f-5de3-4738-8bf4-3e851f3cb6aa/apply")
	if err != nil || got.Title != "Software Tools Engineer" || got.Location != "Austin, Texas, United States" {
		t.Fatalf("eu board: %+v %v (asked %v)", got, err, rt.asked)
	}
	rt.asked = nil
	if _, err := r.Read(context.Background(), "https://jobs.lever.co/cirrus/2e9a0d0f-5de3-4738-8bf4-3e851f3cb6aa"); err == nil || len(rt.asked) != 1 || rt.asked[0] != "api.lever.co" {
		t.Fatalf("a global board is read from the global API: %v (asked %v)", err, rt.asked)
	}
}

// leverCase answers as Lever's API does: a site name in any other case is 404.
type leverCase struct{ asked []string }

func (l *leverCase) RoundTrip(r *http.Request) (*http.Response, error) {
	l.asked = append(l.asked, r.URL.Path)
	status, body := http.StatusNotFound, `{"ok":false,"error":"Document not found"}`
	if r.URL.Path == "/v0/postings/Hivemapper/69b4f681-d6d5-4ebb-b059-fee83971ea80" {
		status, body = http.StatusOK, `{"text":"Senior Software Engineer","descriptionPlain":"","description":"<div>Map the world.</div><ul><li>Go &amp; Rust</li></ul>","categories":{"location":"San Francisco, CA"},"country":"US"}`
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
}

// A Lever posting is read by its site name as the address writes it
// (Hivemapper's board answered 404 by the lowercased name of the identity key),
// and a description stated only as HTML is read as its text.
func TestATSReader_LeverSiteNameCaseAndAnHTMLOnlyDescription(t *testing.T) {
	rt := &leverCase{}
	r := ATSReader{Client: &http.Client{Transport: rt}}
	got, err := r.Read(context.Background(), "https://jobs.lever.co/Hivemapper/69b4f681-d6d5-4ebb-b059-fee83971ea80")
	if err != nil || got.Title != "Senior Software Engineer" || !strings.Contains(got.Text, "Map the world.") || !strings.Contains(got.Text, "- Go & Rust") {
		t.Fatalf("Hivemapper: %+v %v (asked %v)", got, err, rt.asked)
	}
}

// SmartRecruiters and Workable postings are read from the public API each
// posting page uses, by the lowercased board and id of their address. The
// bodies are the vendors' responses as recorded, cut to the fields read: a
// SmartRecruiters posting taken down answers 200, inactive and with no place,
// so its page decides, and a Workable posting lists each of its places, a
// hidden one too.
func TestATSReader_SmartRecruitersAndWorkablePostings(t *testing.T) {
	bodies := map[string]string{
		"/v1/companies/northwindexample/postings/744000100000001": `{"id":"744000100000001","name":"Platform Engineer","active":true,"visibility":"PUBLIC",` +
			`"company":{"identifier":"NorthwindExample","name":"Northwind Example"},` +
			`"location":{"city":"Toronto","region":"ON","country":"ca","remote":false,"fullLocation":"Toronto, ON, Canada"},` +
			`"jobAd":{"sections":{"companyDescription":{"title":"Company Description","text":"<p>We build tools.</p>"},` +
			`"jobDescription":{"title":"Job Description","text":"<p>Run the platform.</p>"},` +
			`"qualifications":{"title":"Qualifications","text":"<ul><li>Five years of Go</li></ul>"},` +
			`"additionalInformation":{"title":"Additional Information","text":""}}}}`,
		"/v1/companies/northwindexample/postings/744000100000002": `{"id":"744000100000002","name":"Data Engineer","active":false,"visibility":"INTERNAL",` +
			`"company":{"identifier":"NorthwindExample","name":"Northwind Example"},"location":{"city":"Toronto","region":"ON","country":"ca","fullLocation":", "},` +
			`"jobAd":{"sections":{"jobDescription":{"title":"Job Description","text":"<p>Move data.</p>"}}}}`,
		"/api/v2/accounts/northwind-example/jobs/1a2b3c4d5e": `{"id":5000001,"shortcode":"1A2B3C4D5E","title":"Platform Engineer","remote":false,"workplace":"hybrid",` +
			`"location":{"city":"Toronto","country":"Canada","countryCode":"CA","region":"Ontario"},` +
			`"locations":[{"city":"Toronto","country":"Canada","countryCode":"CA","hidden":false,"region":"Ontario"},` +
			`{"city":"","country":"Canada","countryCode":"CA","hidden":true,"region":"Quebec"}],` +
			`"state":"published","description":"<p>Run the platform.</p>","requirements":"<ul><li>Five years of Go</li></ul>","benefits":""}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	r := ATSReader{Hosts: map[string]string{"smartrecruiters": srv.URL, "workable": srv.URL}}
	for _, c := range []struct {
		name, url string
		want      PageRead
		gone      bool
	}{
		{"a SmartRecruiters job page", "https://jobs.smartrecruiters.com/NorthwindExample/744000100000001-platform-engineer", PageRead{
			Title: "Platform Engineer", Company: "Northwind Example", Location: "Toronto, ON, Canada", Method: "http", Result: "live",
			Text: "Company Description\n<p>We build tools.</p>\nJob Description\n<p>Run the platform.</p>\nQualifications\n<ul><li>Five years of Go</li></ul>",
		}, false},
		{"a SmartRecruiters posting taken down", "https://jobs.smartrecruiters.com/NorthwindExample/744000100000002", PageRead{}, true},
		{"a Workable application page", "https://apply.workable.com/northwind-example/j/1A2B3C4D5E/apply/", PageRead{
			Title: "Platform Engineer", Location: "Toronto, Ontario, Canada; Quebec, Canada", Method: "http", Result: "live",
			Text: "<p>Run the platform.</p>\nRequirements\n<ul><li>Five years of Go</li></ul>",
		}, false},
	} {
		got, err := r.Read(context.Background(), c.url)
		if c.gone {
			if !errors.Is(err, ErrNeedsBrowser) {
				t.Errorf("%s: %+v %v", c.name, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: %+v %v\nwant %+v", c.name, got, err, c.want)
		}
	}
}

// A crash between marking a row queued and creating its evaluation run leaves
// a queued row no job names; the next start gives it back.
func TestRecoverInterrupted_ReleasesAQueuedRowThatHasNoJob(t *testing.T) {
	e := newTenv(t)
	repo := discovery.NewRepo(e.st, func() time.Time { return testNow })
	ctx := context.Background()
	add, err := repo.InboxAddURL(ctx, "https://boards.greenhouse.io/example/jobs/123")
	if err != nil || add.Row == nil {
		t.Fatalf("add: %+v %v", add, err)
	}
	if err := queueRow(repo, add.Row.ID, ""); err != nil {
		t.Fatal(err)
	}
	e.ev.d.Inbox = DiscoveryInbox{Repo: repo}
	p := NewPool(e.ev, PoolOptions{})
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	row, _ := repo.InboxGet(ctx, add.Row.ID)
	if row.Status != discovery.InboxPending || !strings.Contains(row.Notes, "interrupted") {
		t.Fatalf("row = %q (%s)", row.Status, row.Notes)
	}
}

func TestEvaluate_ReportsItsOutcomeToTheScanRunThatQueuedIt(t *testing.T) {
	e := newTenv(t)
	repo := discovery.NewRepo(e.st, func() time.Time { return testNow })
	ctx := context.Background()
	rec, err := repo.ScanStart(ctx, "registry", nil)
	if err != nil {
		t.Fatal(err)
	}
	rec.Status = discovery.RunCompleted
	if _, err := repo.ScanFinish(ctx, rec); err != nil {
		t.Fatal(err)
	}
	e.ev.d.ScanOutcome = ScanOutcomes(repo)
	for _, marker := range []string{"so1", "so2"} {
		if res := e.run(req(marker, func(r *Request) { r.ScanRun = rec.ID })); res.Outcome != OutcomeReported {
			t.Fatalf("result = %+v", res)
		}
	}
	e.run(req("so3", func(r *Request) {
		r.ScanRun = rec.ID
		r.Company = "Blocked Example"
		e.blocked[NormalizeCompany("Blocked Example")] = true
	}))
	got, err := repo.ScanGet(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Counts.Outcomes[discovery.OutcomeReported] != 2 || got.Counts.Outcomes[discovery.OutcomeGated] != 1 {
		t.Fatalf("outcomes = %v", got.Counts.Outcomes)
	}
}

// A row that joined another request's evaluation ends with it: evaluated when
// it is reported, back to waiting when it is cancelled or the server shuts
// down. A shutdown leaves nothing decided about the posting, so the joined
// row's attempt is not spent either (flow round 9, E-1).
func TestPoolEnqueuer_JoinedManualJobSettlesInboxAndScan(t *testing.T) {
	for _, end := range []string{"reported", "cancelled", "interrupted"} {
		t.Run(end, func(t *testing.T) {
			e := newTenv(t)
			stub := newStub()
			e.model = stub
			e.rebuild()
			repo := discovery.NewRepo(e.st, func() time.Time { return testNow })
			e.ev.d.Inbox, e.ev.d.ScanOutcome = DiscoveryInbox{Repo: repo}, ScanOutcomes(repo)
			p := startPool(t, e, PoolOptions{})
			ctx := t.Context()
			r := req("joined-manual")
			a, err := p.Submit(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, "manual evaluation entering the model", func() bool { return stub.calls() == 1 })
			add, err := repo.InboxAddURL(ctx, r.URL)
			if err != nil || add.Row == nil {
				t.Fatalf("inbox: %+v %v", add, err)
			}
			rec, err := repo.ScanStart(ctx, "registry", nil)
			if err != nil {
				t.Fatal(err)
			}
			id, rule, err := repo.QueueForEvaluation(ctx, *add.Row, nil, func() (string, error) {
				return (PoolEnqueuer{Pool: p}).Enqueue(ctx, discovery.EvalRequest{InboxID: add.Row.ID, URL: r.URL, RunID: rec.ID, Mode: "deep"})
			})
			if err != nil || rule != "" || id != a.RunID {
				t.Fatalf("joined id=%s original=%s rule=%s err=%v", id, a.RunID, rule, err)
			}
			switch end {
			case "cancelled":
				p.Cancel(a.RunID)
			case "interrupted":
				p.Close()
			default:
				stub.release <- struct{}{}
			}
			res, err := p.Wait(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			row, err := repo.InboxGet(ctx, add.Row.ID)
			if err != nil {
				t.Fatal(err)
			}
			want, outcome := discovery.InboxEvaluated, discovery.OutcomeReported
			if end != "reported" {
				want, outcome = discovery.InboxPending, discovery.OutcomeFailed
			}
			if row.Status != want || (end == "interrupted" && row.Attempts != 0) {
				t.Errorf("joined row status=%s attempts=%d want=%s job=%s result=%+v", row.Status, row.Attempts, want, row.JobID, res)
			}
			got, err := repo.ScanGet(ctx, rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Counts.Outcomes[outcome] != 1 {
				t.Errorf("joined scan outcomes=%v want %s=1", got.Counts.Outcomes, outcome)
			}
			if stub.calls() != 1 || e.count("evaluation_jobs") != 1 {
				t.Errorf("joining duplicated work: calls=%d jobs=%d", stub.calls(), e.count("evaluation_jobs"))
			}
		})
	}
}

func TestPoolEnqueuer_OneJobSettlesEachJoinedScanOnce(t *testing.T) {
	e := newTenv(t)
	stub := newStub()
	e.model = stub
	e.rebuild()
	repo := discovery.NewRepo(e.st, func() time.Time { return testNow })
	e.ev.d.ScanOutcome = ScanOutcomes(repo)
	p := startPool(t, e, PoolOptions{})
	ctx := t.Context()
	r := req("joined-scans")
	a := submit(t, p, "joined-scans")
	waitFor(t, "shared model call", func() bool { return stub.calls() == 1 })
	var runs []discovery.Record
	for _, source := range []string{"registry", "tracked"} {
		rec, err := repo.ScanStart(ctx, source, nil)
		if err != nil {
			t.Fatal(err)
		}
		runs = append(runs, rec)
		for i := 0; i < 2; i++ {
			id, err := (PoolEnqueuer{Pool: p}).Enqueue(ctx, discovery.EvalRequest{URL: r.URL, RunID: rec.ID, Mode: "deep"})
			if err != nil || id != a.RunID {
				t.Fatalf("id=%s err=%v", id, err)
			}
		}
	}
	stub.release <- struct{}{}
	res, err := p.Wait(ctx, a.RunID)
	if err != nil || res.Outcome != OutcomeReported {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	for _, rec := range runs {
		got, err := repo.ScanGet(ctx, rec.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Counts.Outcomes[discovery.OutcomeReported] != 1 {
			t.Errorf("%s outcomes=%v want reported1", got.Source, got.Counts.Outcomes)
		}
	}
	if stub.calls() != 1 || e.count("report_index") != 1 {
		t.Fatalf("calls=%d reports=%d", stub.calls(), e.count("report_index"))
	}
}

// reviewWorld is a quick screen's review that the day's spend cap held back,
// written by the evaluator over a real inbox row and mirrored onto the board.
type reviewWorld struct {
	e     *tenv
	repo  *discovery.Repo
	board *notify.Board
	req   Request
	id    string
}

func newReviewWorld(t *testing.T, fixtures ...llm.Fixture) *reviewWorld {
	t.Helper()
	ctx := t.Context()
	e := newTenv(t, append([]llm.Fixture{screenFixture("Marker rv.", screenReply(3.9, "review", "legitimacy_unclear"))}, fixtures...)...)
	repo := discovery.NewRepo(e.st, func() time.Time { return testNow })
	add, err := repo.InboxAddURL(ctx, "https://jobs.example.test/p/rv")
	if err != nil || add.Row == nil {
		t.Fatalf("add: %+v %v", add, err)
	}
	if err := queueRow(repo, add.Row.ID, ""); err != nil {
		t.Fatal(err)
	}
	e.ev.d.Inbox = DiscoveryInbox{Repo: repo}
	w := &reviewWorld{e: e, repo: repo, board: notify.NewBoard(e.st, notify.BoardOptions{Now: func() time.Time { return testNow }}),
		req: req("rv", mode(ModeAuto), scan, func(r *Request) { r.InboxRowID, r.Source = add.Row.ID, "jobright" })}
	e.capped = true
	res := e.run(w.req)
	e.capped = false
	if !res.Review {
		t.Fatalf("the screen = %+v", res)
	}
	w.id = res.RunID
	w.sync(t)
	return w
}

func (w *reviewWorld) sync(t *testing.T) {
	t.Helper()
	if _, err := w.board.SyncRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// later is another evaluation of the review's posting.
func (w *reviewWorld) later(m Mode) Request {
	r := w.req
	r.Mode = m
	return r
}

// job creates the evaluation of r under the posting key and leaves it queued.
func (w *reviewWorld) job(t *testing.T, r Request, key string) string {
	t.Helper()
	r, err := NormalizeRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.e.ev.createJob(t.Context(), r, cmp.Or(key, requestKey(r)), newIdempotencyKey())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// drain sends the waiting review to the pool the way the inbox drain does and
// mirrors the evaluation it started onto the board.
func (w *reviewWorld) drain(t *testing.T, p *Pool) string {
	t.Helper()
	open, err := OpenReviews(t.Context(), w.e.st, 1)
	if err != nil || len(open) != 1 {
		t.Fatalf("open reviews = %+v, %v", open, err)
	}
	id, err := PoolEnqueuer{Pool: p, Drain: "run_drain"}.Enqueue(t.Context(), open[0])
	if err != nil {
		t.Fatal(err)
	}
	w.sync(t)
	return id
}

// The inbox drain takes a quick screen's review that waits on the board as an
// evaluation in depth of the review's own posting, once: never one the user
// skipped or whose posting the user dismissed, while an evaluation of its
// posting runs, nor once the posting was evaluated in depth, by the drain or
// otherwise. A restart that cut the drain's evaluation short leaves the review
// to be taken again (F-8).
func TestOpenReviews_TheDrainTakesAWaitingReviewOnce(t *testing.T) {
	failing := deepFixture("Marker rv.", "not json")
	for _, c := range []struct {
		name     string
		fixtures []llm.Fixture
		then     func(t *testing.T, w *reviewWorld)
		taken    bool
	}{
		{name: "a review waiting for the user", taken: true},
		{name: "a review the user skipped", then: func(t *testing.T, w *reviewWorld) {
			if _, err := w.board.Ack(t.Context(), w.id); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "its posting dismissed from the inbox", then: func(t *testing.T, w *reviewWorld) {
			if err := w.repo.InboxDismiss(t.Context(), w.req.InboxRowID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "an evaluation of its posting queued", then: func(t *testing.T, w *reviewWorld) { w.job(t, w.later(ModeScreen), "") }},
		{name: "a later evaluation of its posting wrote a final report", then: func(t *testing.T, w *reviewWorld) {
			if res := w.e.run(w.later(ModeAuto)); res.Outcome != OutcomeReported {
				t.Fatalf("the later evaluation = %+v", res)
			}
		}},
		{name: "an evaluation in depth of its posting failed", fixtures: []llm.Fixture{failing}, then: func(t *testing.T, w *reviewWorld) {
			if res := w.e.run(w.later(ModeDeep)); res.Outcome != OutcomeFailed {
				t.Fatalf("the evaluation in depth = %+v", res)
			}
		}},
		{name: "the drain's evaluation of it failed", fixtures: []llm.Fixture{failing}, then: func(t *testing.T, w *reviewWorld) {
			p := startPool(t, w.e, PoolOptions{})
			id := w.drain(t, p)
			if res, err := p.Wait(t.Context(), id); err != nil || res.Outcome != OutcomeFailed {
				t.Fatalf("the drain's evaluation = %+v, %v", res, err)
			}
			var key, review, mode, drain string
			_ = w.e.st.Read().QueryRow(`SELECT j.identity_key, json_extract(r.input_json, '$.Review'), j.mode, json_extract(r.input_json, '$.Drain')
				FROM evaluation_jobs j JOIN runs r ON r.id = j.run_id WHERE j.run_id = ?`, id).Scan(&key, &review, &mode, &drain)
			if key != requestKey(w.req) || review != w.id || mode != string(ModeDeep) || drain != "run_drain" {
				t.Fatalf("the drain's evaluation is %s of %s for %s, sent by %q", mode, key, review, drain)
			}
		}},
		{name: "the drain's evaluation of it ended under another posting key", fixtures: []llm.Fixture{failing}, then: func(t *testing.T, w *reviewWorld) {
			r := w.later(ModeDeep)
			r.Review = w.id
			id := w.job(t, r, "drifted-key")
			w.sync(t)
			if res := w.e.ev.Run(t.Context(), id, r, "drifted-key"); res.Outcome != OutcomeFailed {
				t.Fatalf("the drain's evaluation = %+v", res)
			}
		}},
		{name: "a restart cut the drain's evaluation of it short", taken: true, then: func(t *testing.T, w *reviewWorld) {
			stub := newStub()
			w.e.model = stub
			w.e.rebuild()
			p := NewPool(w.e.ev, PoolOptions{})
			if err := p.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			w.drain(t, p)
			waitFor(t, "the evaluation in depth", func() bool { return stub.entered() == 1 })
			p.Close()
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newReviewWorld(t, c.fixtures...)
			if c.then != nil {
				c.then(t, w)
			}
			open, err := OpenReviews(t.Context(), w.e.st, 5)
			if err != nil {
				t.Fatal(err)
			}
			if !c.taken {
				if len(open) != 0 {
					t.Fatalf("taken: %+v", open)
				}
				return
			}
			want := discovery.EvalRequest{InboxID: w.req.InboxRowID, URL: w.req.URL, Mode: string(ModeDeep), Source: "jobright", Text: w.req.Text,
				Title: w.req.Title, Company: w.req.Company, Review: w.id}
			if len(open) != 1 || open[0] != want {
				t.Fatalf("open reviews = %+v, want %+v", open, want)
			}
		})
	}
}
