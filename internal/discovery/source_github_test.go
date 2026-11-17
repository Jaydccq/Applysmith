package discovery

import (
	"applysmith/internal/workspace"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const githubListPath = "/example-org/example-list/main/README.md"

var githubListURL = "https://raw.githubusercontent.com" + githubListPath

func githubReadme() string {
	return "# New grad roles\n\n" +
		`<table><thead><tr><th>Company</th><th>Role</th><th>Location</th><th>Application</th><th>Age</th></tr></thead><tbody>` +
		`<tr><td><strong><a href="https://simplify.jobs/c/Example">Example Co</a></strong></td><td>Software Engineer</td><td>Austin, TX</td><td><a href="https://jobs.example.test/careers/1">Apply</a> <a href="https://simplify.jobs/p/aaa">S</a></td><td>3h</td></tr>` +
		`</tbody></table>` + "\n\n" +
		"| Company | Role | Location | Application | Age |\n| --- | --- | --- | --- | --- |\n" +
		"| **[Sample Inc](https://simplify.jobs/c/Sample)** | Data Engineer | Remote | [Apply](https://jobs.example.test/careers/2) | 30h |\n" +
		"| " + arrowMarker + " | QA Engineer | Austin, TX | [Apply](https://jobs.example.test/careers/3) | 2h |\n" +
		"| Locked Co | Platform Engineer | Austin, TX | " + lockMarker + " | 1h |\n" +
		"| Profile Co [Profile](https://simplify.jobs/c/Profile) | Support Engineer | Austin, TX |  | 1h |\n" +
		"| Archive Co | Archive Engineer | Austin, TX | [Apply](https://jobs.example.test/careers/5) | 3mo |\n" +
		"| Mystery Co | Mystery Engineer | Austin, TX | [Apply](https://jobs.example.test/careers/6) | soon |\n"
}

func githubEnv(t *testing.T, readme string) (*env, *feedServer, *rewriteTransport) {
	e, fs, rt := newFeedEnv(t)
	fs.handle = func(w http.ResponseWriter, r *http.Request, _ string) {
		switch {
		case r.URL.Path == githubListPath:
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(readme))
		case strings.HasPrefix(r.URL.Path, "/careers/"):
			// Each posting has its own text: unrelated employers never share one.
			writeHTML(w, "<html><body><h1>Role "+r.URL.Path+"</h1><p>You will build services. Requires 1+ years of experience.</p></body></html>")
		default:
			http.NotFound(w, r)
		}
	}
	return e, fs, rt
}

func githubPortals(t *testing.T) workspace.Portals {
	return portalsWith(t, eng(), "github", map[string]any{"url": githubListURL})
}

func TestGitHub_HTMLAndMarkdownTablesShareSemantics(t *testing.T) {
	rows := parseListTables(githubReadme())
	var htmlRow, mdRow listRow
	for _, r := range rows {
		switch r.Role {
		case "Software Engineer":
			htmlRow = r
		case "Data Engineer":
			mdRow = r
		}
	}
	if htmlRow.Role != "Software Engineer" || htmlRow.Apply != "https://jobs.example.test/careers/1" || htmlRow.Age != "3h" || htmlRow.Location != "Austin, TX" {
		t.Fatalf("html row = %+v", htmlRow)
	}
	if mdRow.Role != "Data Engineer" || mdRow.Apply != "https://jobs.example.test/careers/2" || mdRow.Age != "30h" || mdRow.Location != "Remote" {
		t.Fatalf("markdown row = %+v", mdRow)
	}
	if len(rows) != 7 {
		t.Fatalf("%d rows, want the 1 html and 6 markdown rows", len(rows))
	}
	// A row whose company cell is the list's arrow takes the row above's
	// company; a locked row is closed, and a company-profile link is never the
	// application.
	for _, r := range rows {
		switch r.Role {
		case "QA Engineer":
			if r.Company != "Sample Inc" {
				t.Errorf("carry-over row = %+v", r)
			}
		case "Platform Engineer":
			if !r.Closed || r.Apply != "" {
				t.Errorf("locked row = %+v", r)
			}
		case "Support Engineer":
			if r.Closed || r.Apply != "" {
				t.Errorf("a company-profile link was taken as the application: %+v", r)
			}
		}
	}
}

// The lists' emoji markers never reach a company or role name; letters of any
// script do.
func TestGitHub_MarkersAreStrippedFromNamesLettersAreKept(t *testing.T) {
	readme := "| Company | Role | Apply | Age |\n| --- | --- | --- | --- |\n" +
		"| \U0001F525 **[Palantir](https://simplify.jobs/c/P)** | SWE \U0001F393 \U0001F1FA\U0001F1F8 | [A](https://jobs.example.test/7) | 1h |\n" +
		"| \u2B50\uFE0F Soci\u00e9t\u00e9 | Ing\u00e9nieur \U0001F44D\U0001F3FD | [A](https://jobs.example.test/8) | 1h |\n" +
		"| &#x1F525; \u682a\u5f0f\u4f1a\u793e | QA | [A](https://jobs.example.test/9) | 1h |\n"
	var got [][2]string
	for _, r := range parseListTables(readme) {
		got = append(got, [2]string{r.Company, r.Role})
	}
	want := [][2]string{{"Palantir", "SWE"}, {"Soci\u00e9t\u00e9", "Ing\u00e9nieur"}, {"\u682a\u5f0f\u4f1a\u793e", "QA"}}
	if !slices.Equal(got, want) {
		t.Fatalf("names = %q, want %q", got, want)
	}
}

func TestGitHub_Freshness_AbsoluteDatesYearBoundaryFutureUnreadable(t *testing.T) {
	now := time.Date(2027, 1, 2, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		cell string
		want time.Duration
		ok   bool
	}{
		{"Dec 30", 3 * day, true},
		{"Jul 24, 2026", now.Sub(time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC)) - 12*time.Hour, true},
		{"2026-12-31", 2 * day, true},
		{"12/30", 3 * day, true},
		{"23h", 23 * time.Hour, true},
		{"3mo", 90 * day, true},
		{lockMarker + " 5h", 5 * time.Hour, true},
		{"2027-01-03", 0, true},
		{"2027-02-01", 0, false},
		{"Feb 30", 0, false},
		{"soon", 0, false},
	}
	for _, c := range cases {
		got, ok := AgeOf(c.cell, now)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("AgeOf(%q) = %s, %v; want %s, %v", c.cell, got, ok, c.want, c.ok)
		}
	}
	// A year-less date that would fall after today reads as last year's.
	if got, ok := AgeOf("Jan 5", now); !ok || got < 360*day {
		t.Errorf("AgeOf(Jan 5) = %s, %v", got, ok)
	}
}

func TestGitHub_DropBreakdownReachesRunRecord(t *testing.T) {
	e, _, _ := githubEnv(t, githubReadme())
	rec := e.run(ScanRequest{Source: "github", Portals: githubPortals(t)})
	c := rec.Counts
	if c.Filtered["closed"] != 1 || c.Filtered["incomplete"] != 1 || c.Filtered["stale"] != 2 || c.Undated != 1 {
		t.Fatalf("counts = %+v", c)
	}
	// 3h, 2h kept; 30h and the archive row are stale.
	if c.Inserted != 2 || rec.Status != RunCompleted {
		t.Fatalf("record = %+v", rec)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	saved, _ := e.rp.ScanGet(e.ctx, rec.ID)
	if saved.Counts.Filtered["stale"] != 2 {
		t.Fatalf("saved = %+v", saved.Counts)
	}
}

func TestGitHub_FailedApplicationPageKeepsListRow(t *testing.T) {
	e, fs, _ := githubEnv(t, githubReadme())
	inner := fs.handle
	fs.handle = func(w http.ResponseWriter, r *http.Request, body string) {
		if strings.HasPrefix(r.URL.Path, "/careers/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		inner(w, r, body)
	}
	rec := e.run(ScanRequest{Source: "github", Portals: githubPortals(t)})
	if rec.Counts.Inserted != 2 || rec.Counts.EnrichmentDegraded != 2 || rec.Status != RunCompleted {
		t.Fatalf("record = %+v", rec)
	}
}

func TestGitHub_ReadsOnlyREADMEAndApplicationPages(t *testing.T) {
	e, fs, _ := githubEnv(t, githubReadme())
	e.run(ScanRequest{Source: "github", Portals: githubPortals(t)})
	for _, r := range fs.requests() {
		okList := r.Path == githubListPath
		okPage := strings.HasPrefix(r.Path, "/careers/") && r.Host == "jobs.example.test"
		if r.Method != http.MethodGet || (!okList && !okPage) {
			t.Errorf("unexpected request %s %s%s", r.Method, r.Host, r.Path)
		}
	}
}

func TestGitHub_UnreadableListIsLayoutChanged(t *testing.T) {
	e, _, _ := githubEnv(t, "# Nothing tabular here\n\nJust prose.")
	rec := e.run(ScanRequest{Source: "github", Portals: githubPortals(t)})
	if rec.Status != RunFailed || rec.Reason != ReasonLayoutChanged {
		t.Fatalf("status %s reason %q", rec.Status, rec.Reason)
	}
}

func TestGitHub_FilteredRowsNeverFetchApplicationPage(t *testing.T) {
	readme := "| Company | Role | Location | Application | Age |\n| --- | --- | --- | --- | --- |\n" +
		"| Example Co | Barista Lead | Austin, TX | [Apply](https://jobs.example.test/careers/1) | 1h |\n" +
		"| Blocked Co | Software Engineer | Austin, TX | [Apply](https://jobs.example.test/careers/2) | 1h |\n" +
		"| Sample Inc | Software Engineer | Austin, TX | [Apply](https://jobs.example.test/careers/3) | 1h |\n"
	e, fs, _ := githubEnv(t, readme)
	if _, err := e.rp.BlockCompany(e.ctx, "Blocked Co", "test", "user"); err != nil {
		t.Fatal(err)
	}
	p := portalsWith(t, portals([]string{"engineer"}, []string{"barista"}), "github", map[string]any{"url": githubListURL})
	rec := e.run(ScanRequest{Source: "github", Portals: p})
	if rec.Status != RunCompleted || rec.Counts.Filtered[FilterTitle] != 1 || rec.Counts.Filtered[FilterBlockedCompany] != 1 || rec.Counts.Inserted != 1 {
		t.Fatalf("record = %+v", rec)
	}
	for _, r := range fs.requests() {
		if r.Path == "/careers/1" || r.Path == "/careers/2" {
			t.Errorf("the page of a filtered row was read: %s", r.Path)
		}
	}
}

func TestGitHub_LocationCellsBecomeACleanList(t *testing.T) {
	readme := "| Company | Role | Location | Application | Age |\n| --- | --- | --- | --- | --- |\n" +
		// The shape of the SimplifyJobs and Vansh lists, saved from the live READMEs.
		"| Alpha | Software Engineer | <details><summary>**4 locations**</summary>Boston, MA</br>Seattle, WA</br>Scottsdale, AZ</br>Atlanta, GA</details> | [Apply](https://jobs.example.test/careers/1) | 1h |\n" +
		"| Beta | Software Engineer | <details><summary><strong>2 locations</strong></summary>Austin, TX</br>Austin, TX</details> | [Apply](https://jobs.example.test/careers/2) | 1h |\n" +
		"| Gamma | Software Engineer | Palo Alto, CA<br>Palo Alto, California, United States | [Apply](https://jobs.example.test/careers/3) | 1h |\n" +
		"| Delta | Software Engineer | Remote in USA | [Apply](https://jobs.example.test/careers/4) | 1h |\n"
	got := map[string]string{}
	for _, r := range parseListTables(readme) {
		got[r.Company] = r.Location
	}
	want := map[string]string{
		"Alpha": "Boston, MA; Seattle, WA; Scottsdale, AZ; Atlanta, GA",
		"Beta":  "Austin, TX",
		"Gamma": "Palo Alto, California, United States",
		"Delta": "Remote in USA",
	}
	for c, w := range want {
		if got[c] != w {
			t.Errorf("%s location = %q, want %q", c, got[c], w)
		}
	}
}

// zapplyEnv serves a list whose links are zapply.jobs redirects, the way the
// live host answers: a slash redirect, then a redirect to the employer's page
// for a live posting, or to its own /jobs page for one that is gone. The
// vanshb03 list's go.vansh.nyc links redirect once, to the employer's page.
func zapplyEnv(t *testing.T, readme string) (*env, *feedServer) {
	e, fs, _ := githubEnv(t, readme)
	inner := fs.handle
	fs.handle = func(w http.ResponseWriter, r *http.Request, body string) {
		if r.Header.Get("X-Original-Host") == "go.vansh.nyc" && r.URL.Path == "/15K" {
			http.Redirect(w, r, "https://jobs.example.test/careers/3", http.StatusFound)
			return
		}
		if r.Header.Get("X-Original-Host") != "zapply.jobs" {
			inner(w, r, body)
			return
		}
		switch r.URL.Path {
		case "/l/d/live-1", "/l/d/live-2", "/l/d/gone":
			http.Redirect(w, r, r.URL.Path+"/?"+r.URL.RawQuery, http.StatusMovedPermanently)
		case "/l/d/live-1/":
			http.Redirect(w, r, "https://jobs.example.test/careers/1", http.StatusFound)
		case "/l/d/live-2/":
			http.Redirect(w, r, "https://jobs.example.test/careers/2", http.StatusFound)
		case "/l/d/gone/":
			http.Redirect(w, r, "https://zapply.jobs/jobs", http.StatusFound)
		case "/l/d/broken", "/l/d/broken/":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			writeHTML(w, "<html><title>Latest Entry-Level Jobs | Zapply</title><body>Find your dream job</body></html>")
		}
	}
	return e, fs
}

func TestGitHub_RedirectLinksResolveToEmployerPostings(t *testing.T) {
	readme := "| Company | Role | Location | Application | Age |\n| --- | --- | --- | --- | --- |\n" +
		"| Example Co | Software Engineer | Austin, TX | [Apply](https://zapply.jobs/l/d/live-1?s=gh) | 1h |\n" +
		// The same job, listed by another list with the employer's own URL.
		"| Example Co | Software Engineer | Austin, TX | [Apply](https://jobs.example.test/careers/1) | 2h |\n" +
		"| Sample Inc | Data Engineer | Remote | [Apply](https://zapply.jobs/l/d/live-2?s=gh) | 1h |\n" +
		"| Gone Co | Platform Engineer | Austin, TX | [Apply](https://zapply.jobs/l/d/gone?s=gh) | 1h |\n" +
		"| Broken Co | Support Engineer | Austin, TX | [Apply](https://zapply.jobs/l/d/broken?s=gh) | 1h |\n" +
		"| Vansh Co | Software Engineer | Austin, TX | [Apply](https://go.vansh.nyc/15K?s=A) | 1h |\n"
	e, fs := zapplyEnv(t, readme)
	rec := e.run(ScanRequest{Source: "github", Portals: githubPortals(t)})
	c := rec.Counts
	if rec.Status != RunCompleted || c.Inserted != 3 || c.Duplicates[StoreRun] != 1 || c.Filtered[FilterDeadLink] != 1 || c.Filtered[FilterUnresolvedLink] != 1 {
		t.Fatalf("record = %+v %+v", rec.Status, c)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, r := range e.inbox(InboxQuery{}) {
		if strings.Contains(r.URL, "zapply") || strings.Contains(r.OriginalURL, "zapply") || strings.Contains(r.URL, "vansh") || !strings.HasPrefix(r.URL, "https://jobs.example.test/careers/") {
			t.Errorf("stored %s (original %s), want the employer URL", r.URL, r.OriginalURL)
		}
		if !contains(r.Description, "build services") {
			t.Errorf("%s description = %q", r.URL, r.Description)
		}
	}
	// Each link is followed once, and the redirector's own pages are never read as postings.
	if n := fs.count("/l/d/live-1"); n != 1 {
		t.Errorf("live-1 followed %d times", n)
	}
	for _, r := range fs.requests() {
		if r.Host == "zapply.jobs" && !strings.HasPrefix(r.Path, "/l/d/") && r.Path != "/jobs" && r.Path != "/jobs/" {
			t.Errorf("unexpected zapply request %s", r.Path)
		}
	}
}

func TestGitHub_TemplatePagesAreNotDescriptions(t *testing.T) {
	readme := "| Company | Role | Location | Application | Age |\n| --- | --- | --- | --- | --- |\n" +
		"| One Co | Software Engineer | Austin, TX | [Apply](https://jobs.example.test/careers/1) | 1h |\n" +
		"| Two Co | Software Engineer | Austin, TX | [Apply](https://jobs.example.test/careers/2) | 1h |\n" +
		"| Three Co | Software Engineer | Austin, TX | [Apply](https://jobs.example.test/careers/3) | 1h |\n" +
		"| Four Co | Software Engineer | Austin, TX | [Apply](https://jobs.example.test/careers/4) | 1h |\n" +
		"| Four Co | Software Engineer II | Austin, TX | [Apply](https://jobs.example.test/careers/5) | 1h |\n"
	e, fs, _ := githubEnv(t, readme)
	inner := fs.handle
	fs.handle = func(w http.ResponseWriter, r *http.Request, body string) {
		switch r.URL.Path {
		case "/careers/1", "/careers/2": // one shell for two employers
			writeHTML(w, "<html><title>Careers</title><body>Welcome to our careers site. Search and apply.</body></html>")
		case "/careers/3": // the notice a vendor shows during an outage
			writeHTML(w, "<html><title>Workday is currently unavailable.</title><body>We will be back soon.</body></html>")
		case "/careers/4", "/careers/5": // one employer repeating its own text is real
			writeHTML(w, "<html><title>Engineer</title><body>Join Four Co and build services for 1+ years.</body></html>")
		default:
			inner(w, r, body)
		}
	}
	rec := e.run(ScanRequest{Source: "github", Portals: githubPortals(t)})
	if rec.Counts.Inserted != 5 || rec.Counts.Enriched != 2 || rec.Counts.EnrichmentDegraded != 3 {
		t.Fatalf("counts = %+v", rec.Counts)
	}
	for _, r := range e.inbox(InboxQuery{}) {
		real := strings.HasSuffix(r.URL, "/4") || strings.HasSuffix(r.URL, "/5")
		if real != (r.Description != "") {
			t.Errorf("%s description = %q", r.URL, r.Description)
		}
	}
}

// An Oracle Recruiting posting page is a script shell whose only text is the
// site's name; the posting is read from the site's public requisition
// endpoint, and a requisition the endpoint no longer lists is no description.
func TestGitHub_OracleRequisitionIsReadFromItsEndpoint(t *testing.T) {
	readme := "| Company | Role | Location | Application | Age |\n| --- | --- | --- | --- | --- |\n" +
		"| One Co | Software Engineer | Austin, TX | [Apply](https://abcd.fa.us2.oraclecloud.com/hcmUI/CandidateExperience/en/sites/CX_1/job/42?utm_source=list) | 1h |\n" +
		"| Two Co | Data Engineer | Austin, TX | [Apply](https://wxyz.fa.ocs.oraclecloud.com/hcmUI/CandidateExperience/en/job/77/?utm_medium=jobboard) | 1h |\n"
	e, fs, _ := githubEnv(t, readme)
	var finders []string
	var mu sync.Mutex
	inner := fs.handle
	fs.handle = func(w http.ResponseWriter, r *http.Request, body string) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/hcmUI/"):
			writeHTML(w, "<html><title>Candidate Experience page</title><body><script>app()</script>Candidate Experience page</body></html>")
		case r.URL.Path == "/hcmRestApi/resources/latest/recruitingCEJobRequisitionDetails":
			q := r.URL.Query()
			mu.Lock()
			finders = append(finders, r.Header.Get("X-Original-Host")+" "+q.Get("finder"))
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(q.Get("finder"), `"77"`) {
				_, _ = w.Write([]byte(`{"items":[],"count":0}`))
				return
			}
			_, _ = w.Write([]byte(`{"items":[{"Title":"Software Engineer","ExternalDescriptionStr":"<p>You will build services.</p>","ExternalResponsibilitiesStr":"<ul><li>Ship code</li></ul>","ExternalQualificationsStr":"<p>Requires 1+ years of experience.</p>"}]}`))
		default:
			inner(w, r, body)
		}
	}
	rec := e.run(ScanRequest{Source: "github", Portals: githubPortals(t)})
	if rec.Counts.Inserted != 2 || rec.Counts.Enriched != 1 || rec.Counts.EnrichmentDegraded != 1 {
		t.Fatalf("counts = %+v", rec.Counts)
	}
	for _, r := range e.inbox(InboxQuery{}) {
		switch r.Company {
		case "One Co":
			for _, want := range []string{"build services", "Ship code", "1+ years"} {
				mustContain(t, "description", r.Description, want)
			}
		default:
			if r.Description != "" {
				t.Errorf("a requisition the endpoint does not list kept %q", r.Description)
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(finders) != 2 || !slices.Contains(finders, `abcd.fa.us2.oraclecloud.com ById;Id="42",siteNumber=CX_1`) || !slices.Contains(finders, `wxyz.fa.ocs.oraclecloud.com ById;Id="77"`) {
		t.Fatalf("requisition reads = %q", finders)
	}
}
