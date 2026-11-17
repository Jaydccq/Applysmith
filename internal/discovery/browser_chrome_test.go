//go:build browser

package discovery_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"applysmith/internal/browser"
	"applysmith/internal/discovery"
	"applysmith/internal/launch"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

// The Chrome tests run the real scan code against synthetic pages in a
// headless Chrome with a throwaway profile. Every host name maps to loopback
// (MAP *), so no page of a real site is ever requested; the sources' base
// URLs are pointed at the local server through Endpoints.

var shared struct {
	once sync.Once
	inst *browser.Instance
	site *fixtureSite
	root string
	err  error
}

func TestMain(m *testing.M) {
	code := m.Run()
	if shared.inst != nil {
		shared.inst.Stop(context.Background())
	}
	if shared.site != nil {
		shared.site.srv.Close()
	}
	if shared.root != "" {
		if chromeAlive(shared.root) {
			fmt.Fprintln(os.Stderr, "a Chrome of the test profile is still running after Stop")
			code = 1
		}
		_ = os.RemoveAll(shared.root)
	}
	os.Exit(code)
}

func chromeAlive(root string) bool {
	out, err := exec.Command("ps", "-Ao", "command=").Output()
	if err != nil {
		return false
	}
	real, _ := filepath.EvalSymlinks(root)
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "user-data-dir="+root) || (real != "" && strings.Contains(line, "user-data-dir="+real)) {
			return true
		}
	}
	return false
}

// watchdog kills the browser's process group once this process is gone, so a
// test binary that is killed or times out leaves no Chrome behind.
func watchdog(pid int) {
	if pid <= 1 {
		return
	}
	script := fmt.Sprintf("while kill -0 %d 2>/dev/null; do sleep 1; done; kill -9 -%d 2>/dev/null; kill -9 %d 2>/dev/null", os.Getpid(), pid, pid)
	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	_ = cmd.Start()
}

// fixtureSite serves the synthetic boards, told apart by Host header.
type fixtureSite struct {
	srv  *httptest.Server
	port int
	mu   sync.Mutex
	hits map[string]int
	// pages maps "host/path" to a handler.
	pages map[string]func(w http.ResponseWriter, r *http.Request)
}

func (f *fixtureSite) hit(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[key]
}

func (f *fixtureSite) set(host, path string, h func(w http.ResponseWriter, r *http.Request)) {
	f.mu.Lock()
	f.pages[host+path] = h
	f.mu.Unlock()
}

func (f *fixtureSite) html(host, path, body string) {
	f.set(host, path, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<!doctype html><html><head><title>fixture</title></head><body>"+body+"</body></html>")
	})
}

func (f *fixtureSite) base(host string) string { return fmt.Sprintf("http://%s:%d", host, f.port) }

func (f *fixtureSite) reset() {
	f.mu.Lock()
	f.pages = map[string]func(http.ResponseWriter, *http.Request){}
	f.hits = map[string]int{}
	f.mu.Unlock()
}

func newSite() *fixtureSite {
	f := &fixtureSite{}
	f.reset()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	f.port = l.Addr().(*net.TCPAddr).Port
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.Split(r.Host, ":")[0]
		f.mu.Lock()
		f.hits[r.URL.Path]++
		f.hits[host+r.URL.Path]++
		h := f.pages[host+r.URL.Path]
		f.mu.Unlock()
		if h == nil {
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}))
	f.srv.Listener = l
	f.srv.Start()
	return f
}

func chromeEnv(t *testing.T) (*browser.Instance, *fixtureSite) {
	t.Helper()
	shared.once.Do(func() {
		chrome, err := browser.DiscoverChrome(context.Background(), os.Getenv)
		if err != nil {
			shared.err = fmt.Errorf("no usable Google Chrome: %w", err)
			return
		}
		root, err := os.MkdirTemp("", "applysmith-browser-test-scans-")
		if err != nil {
			shared.err = err
			return
		}
		shared.root = root
		shared.site = newSite()
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		shared.inst, shared.err = browser.Open(ctx, browser.Options{
			Chrome:       chrome,
			ProfileDir:   filepath.Join(root, "applysmith-browser-test-profile"),
			StateDir:     filepath.Join(root, "state"),
			DownloadsDir: filepath.Join(root, "downloads"),
			Environ:      os.Environ(),
		}.WithHostRules("MAP * 127.0.0.1").WithHeadless())
		if shared.err == nil {
			watchdog(shared.inst.PID())
		}
	})
	if shared.err != nil {
		t.Skipf("browser unavailable: %v", shared.err)
	}
	shared.site.reset()
	return shared.inst, shared.site
}

type chromeRig struct {
	t    *testing.T
	inst *browser.Instance
	site *fixtureSite
	repo *discovery.Repo
	sc   *discovery.Scanner
}

func newRig(t *testing.T) *chromeRig {
	t.Helper()
	inst, site := chromeEnv(t)
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "applysmith.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	repo := discovery.NewRepo(st, nil)
	eps := discovery.Endpoints{}
	hosts := map[string]string{"linkedin": "www.linkedin.com", "linkedin-feed": "www.linkedin.com", "indeed": "www.indeed.com", "ziprecruiter": "www.ziprecruiter.example.test",
		"google-jobs": "www.google.com", "glassdoor": "www.glassdoor.com", "newgrad": "www.newgrad-jobs.com", "jobright": "jobright.ai"}
	for src, h := range hosts {
		eps[src] = site.base(h)
	}
	sc := &discovery.Scanner{Repo: repo, Client: discovery.NewClient(), Endpoints: eps,
		Browser: &discovery.BrowserEnv{Window: launch.ChromeWindow(func(context.Context) (*browser.Instance, error) { return inst, nil }, nil),
			Host: func(context.Context) discovery.HostSample {
				return discovery.HostSample{Verdict: discovery.PressureNormal, FreeMiB: 8000, CPUs: 8}
			},
			Rand: func(lo, hi time.Duration) time.Duration { return 0 }, Sleep: func(ctx context.Context, d time.Duration) error {
				time.Sleep(min(d, 20*time.Millisecond))
				return ctx.Err()
			},
			ContentWait: 6 * time.Second, Poll: 100 * time.Millisecond}}
	return &chromeRig{t: t, inst: inst, site: site, repo: repo, sc: sc}
}

func portals(t *testing.T, source string, queries ...any) workspace.Portals {
	t.Helper()
	p := workspace.Portals{Version: 1}
	p.Filters.Title.Positive = []string{"engineer"}
	var nodes []yaml.Node
	for _, q := range queries {
		var n yaml.Node
		if err := n.Encode(q); err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, n)
	}
	p.Sources = map[string]workspace.Source{source: {Queries: nodes}}
	return p
}

func (c *chromeRig) run(source string, p workspace.Portals, mut func(*discovery.ScanRequest)) discovery.Record {
	c.t.Helper()
	zero := 0
	req := discovery.ScanRequest{Source: source, Portals: p, TopN: &zero, Budget: 2 * time.Minute}
	if mut != nil {
		mut(&req)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	rec, err := c.sc.Run(ctx, req)
	if err != nil {
		c.t.Fatalf("%s: %v", source, err)
	}
	if len(rec.Errors) > 0 {
		c.t.Logf("%s notes: %v", source, rec.Errors)
	}
	return rec
}

func (c *chromeRig) rows() []discovery.InboxRow {
	c.t.Helper()
	rows, err := c.repo.InboxList(context.Background(), discovery.InboxQuery{})
	if err != nil {
		c.t.Fatal(err)
	}
	return rows
}

func rowFor(rows []discovery.InboxRow, sub string) (discovery.InboxRow, bool) {
	for _, r := range rows {
		if strings.Contains(r.URL, sub) {
			return r, true
		}
	}
	return discovery.InboxRow{}, false
}

func hasFlag(r discovery.InboxRow, f string) bool {
	for _, x := range r.Flags {
		if x == f {
			return true
		}
	}
	return false
}

const description = "Build and run backend services for the product. You will write Go, review code and ship changes with a small team that cares about reliability and clear writing. Requirements include two years of professional experience."

func ago(d time.Duration) string { return time.Now().Add(-d).UTC().Format(time.RFC3339) }

// ---- LinkedIn ----

// linkedInFixtures serves both result-page layouts: 1001 and 1002 are the
// signed-in 2026-10 cards (componentkey'd buttons with no link), 1003 the
// older linked card.
func linkedInFixtures(site *fixtureSite) {
	h := "www.linkedin.com"
	card := func(id int, title, company string, easy bool) string {
		btn := ""
		if easy {
			btn = `<p>Easy Apply</p>`
		}
		return fmt.Sprintf(`<li><div role="button" tabindex="0" componentkey="job-card-component-ref-%d"><div componentkey="job-card-component-ref-%d">
<p>%s (Verified job)<span aria-hidden="true">%s</span></p><p>%s</p><p>Austin, TX</p><p>3 school alumni work here</p><p>Posted 1 hour ago<span aria-hidden="true">1 hour ago</span></p>%s
<button class="save" onclick="fetch('/__write?save')">Save</button></div></div></li>`, id, id, title, title, company, btn)
	}
	oldCard := func(id int, title, company string) string {
		return fmt.Sprintf(`<li data-occludable-job-id="%d"><div class="job-card-container" data-job-id="%d"><a class="job-card-list__title" href="/jobs/view/%d/" aria-label="%s">%s</a>
<div class="artdeco-entity-lockup__subtitle">%s</div><div class="artdeco-entity-lockup__caption">Austin, TX</div><time datetime="%s">1 hour ago</time></div></li>`, id, id, id, title, title, company, ago(time.Hour))
	}
	site.html(h, "/jobs/search-results/", `<ul class="jobs-search__results-list">`+
		card(1001, "Backend Engineer", "Acme Corp", false)+card(1002, "Platform Engineer", "Beta Labs", true)+oldCard(1003, "Data Engineer", "Gamma Inc")+`</ul>`)
	external := func(id int) string {
		return fmt.Sprintf(`<a aria-label="Apply on company website" href="/safety/go/?url=http%%3A%%2F%%2Fcareers.example.com%%3A%d%%2Fjobs%%2F%d">Apply</a>`, site.port, id)
	}
	detail := func(id int, easy bool) string {
		apply := external(id)
		if easy {
			apply = `<button aria-label="Easy Apply to this job" onclick="fetch('/__write?easy')">Easy Apply</button>`
		}
		// A similar-job card is one link whose text says Easy Apply; it is
		// not this posting's apply control.
		return fmt.Sprintf(`<div aria-label="Company, Company."><a href="/company/x/">Company</a></div><p>Backend Engineer</p>
<div componentkey="JobDetails_AboutTheJob_%d"><h2>About the job</h2><span data-testid="expandable-text-box">%s</span></div>%s
<a href="/jobs/search-results/?currentJobId=9">Go Engineer Delta Co Remote Posted 2 days ago · Easy Apply</a><input type="text" name="q" oninput="fetch('/__typed')">`, id, description, apply)
	}
	oldDetail := func(id int) string {
		return fmt.Sprintf(`<h1 class="top-card-layout__title">Data Engineer</h1><a class="topcard__org-name-link">Company</a><span class="num-applicants__caption">42 applicants</span>
<div class="show-more-less-html__markup">%s</div>%s`, description, external(id))
	}
	site.html(h, "/jobs/view/1001", detail(1001, false))
	site.html(h, "/jobs/view/1002", detail(1002, true))
	site.html(h, "/jobs/view/1003", oldDetail(1003))
}

func TestLinkedInJobs_Chrome_ExtractsCardsAndDetailsWithoutWriting(t *testing.T) {
	c := newRig(t)
	linkedInFixtures(c.site)
	rec := c.run("linkedin", portals(t, "linkedin", map[string]any{"search": "engineer"}), func(r *discovery.ScanRequest) { r.Pages = 1 })
	if rec.Status != discovery.RunCompleted || rec.Counts.Inserted != 3 {
		t.Fatalf("status %s counts %+v error %q", rec.Status, rec.Counts, rec.Error)
	}
	rows := c.rows()
	for _, x := range rows {
		t.Logf("row %s aliases %v flags %v", x.URL, x.Aliases, x.Flags)
	}
	r, ok := rowFor(rows, "/jobs/1001")
	if !ok || !strings.Contains(r.URL, "careers.example.com") || len(r.Aliases) == 0 || r.Role != "Backend Engineer" || r.Company != "Acme Corp" || r.Location != "Austin, TX" || !strings.Contains(r.Description, "Go") {
		t.Fatalf("employer row = %+v", r)
	}
	if hasFlag(r, "mirror") || hasFlag(r, discovery.FlagEasyApply) {
		t.Fatalf("flags = %v", r.Flags)
	}
	easy, ok := rowFor(rows, "/jobs/view/1002")
	if !ok || !hasFlag(easy, discovery.FlagEasyApply) || easy.Company != "Beta Labs" {
		t.Fatalf("easy row = %+v", easy)
	}
	old, ok := rowFor(rows, "/jobs/1003")
	if !ok || old.Company != "Gamma Inc" || !hasFlag(old, "applicants=42 applicants") {
		t.Fatalf("older-layout row = %+v", old)
	}
	// Safety (BS-003): nothing was typed and no write control was pressed.
	for _, p := range []string{"/__write", "/__typed"} {
		if n := c.site.hit(p); n != 0 {
			t.Fatalf("%s was hit %d times", p, n)
		}
	}
}

func TestBrowserScan_Chrome_OnlyScanLaneTabsOpen(t *testing.T) {
	c := newRig(t)
	linkedInFixtures(c.site)
	apply := c.inst.Lane(browser.LaneApply)
	anchor, err := apply.OpenTab(context.Background(), "test-anchor")
	if err != nil {
		t.Fatal(err)
	}
	defer apply.CloseTab(context.Background(), anchor)
	before := len(apply.Tabs())
	var maxScan, applyChanged atomic.Int64
	stop, polled := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(polled)
		for {
			maxScan.Store(max(maxScan.Load(), int64(len(c.inst.Lane(browser.LaneScan).Tabs()))))
			if len(apply.Tabs()) != before {
				applyChanged.Add(1)
			}
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
	c.run("linkedin", portals(t, "linkedin", map[string]any{"search": "engineer"}), func(r *discovery.ScanRequest) { r.Pages = 1 })
	close(stop)
	<-polled
	if maxScan.Load() < 1 {
		t.Fatal("no scan-lane tab was seen open while the scan ran")
	}
	if n := applyChanged.Load(); n != 0 {
		t.Fatalf("the apply lane's tab count changed during the scan (%d polls)", n)
	}
	if after := len(apply.Tabs()); after != before {
		t.Fatalf("the apply lane has %d tabs after the scan, %d before", after, before)
	}
	if left := len(c.inst.Lane(browser.LaneScan).Tabs()); left != 0 {
		t.Fatalf("%d scan tabs stayed open", left)
	}
}

func TestBrowserScan_Chrome_ClickGuardRefusesWriteControls(t *testing.T) {
	c := newRig(t)
	// A feed post whose "comments" control is really a Like button.
	c.site.html("www.linkedin.com", "/feed/", `<div class="feed-shared-update-v2" data-urn="urn:li:activity:5">
<span class="update-components-actor__title">Alex Example</span><div class="update-components-text">We are hiring an engineer. Link in the comments!</div>
<button class="social-details-social-counts__comments" onclick="fetch('/__write?like')">Like</button></div>`)
	model := func(context.Context, string, string) (string, error) {
		return `{"is_hiring":false,"confidence":0.9}`, nil
	}
	c.sc.FeedModel = model
	rec := c.run("linkedin-feed", portals(t, "linkedin-feed"), nil)
	if rec.Status != discovery.RunCompleted {
		t.Fatalf("status %s %s", rec.Status, rec.Error)
	}
	if c.site.hit("/__write") != 0 {
		t.Fatal("a write control was pressed")
	}
}

// ---- Indeed ----

func TestIndeed_Chrome_ExtractsCardsAndFollowsTheApplyControl(t *testing.T) {
	c := newRig(t)
	h := "www.indeed.com"
	c.site.html(h, "/jobs", `<div class="job_seen_beacon"><h2 class="jobTitle"><a class="jcs-JobTitle" data-jk="abc123" href="/viewjob?jk=abc123"><span title="Backend Engineer">Backend Engineer</span></a></h2>
<span data-testid="company-name">Acme</span><div data-testid="text-location">Austin, TX</div><span data-testid="myJobsStateDate">Posted 2 days ago</span></div>
<div class="job_seen_beacon"><h2 class="jobTitle"><a class="jcs-JobTitle" data-jk="def456" href="/viewjob?jk=def456"><span title="Data Engineer">Data Engineer</span></a></h2>
<span data-testid="company-name">Beta</span><div data-testid="text-location">Remote</div><span data-testid="myJobsStateDate">Just posted</span></div>`)
	c.site.html(h, "/viewjob", fmt.Sprintf(`<h1 class="jobsearch-JobInfoHeader-title">Backend Engineer</h1><div id="jobDescriptionText">%s</div>
<button id="applyButton" onclick="window.open('http://careers.example.com:%d/jobs/'+new URLSearchParams(location.search).get('jk'))">Apply on company site</button>`, description, c.site.port))
	c.site.set("careers.example.com", "/jobs/abc123", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<h1>Backend Engineer at Acme</h1><p>"+description+"</p>")
	})
	c.site.set("careers.example.com", "/jobs/def456", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<h1>Data Engineer at Beta</h1><p>"+description+"</p>")
	})
	rec := c.run("indeed", portals(t, "indeed", map[string]any{"search": "engineer", "location": "Austin"}), nil)
	if rec.Status != discovery.RunCompleted || rec.Counts.Inserted != 2 {
		t.Fatalf("status %s counts %+v error %q", rec.Status, rec.Counts, rec.Error)
	}
	rows := c.rows()
	if r, ok := rowFor(rows, "/jobs/abc123"); !ok || len(r.Aliases) == 0 {
		t.Fatalf("the apply control was not followed to the employer: %+v", rows)
	}
	if rec.Detail["external-ats-navigations"] < 1 {
		t.Fatalf("detail = %v", rec.Detail)
	}
	if left := len(c.inst.Lane(browser.LaneScan).Tabs()); left != 0 {
		t.Fatalf("%d scan tabs stayed open: the throwaway page was not closed", left)
	}
}

func TestIndeed_Chrome_ChallengePageBlocksTheRun(t *testing.T) {
	c := newRig(t)
	c.site.html("www.indeed.com", "/jobs", `<h1>Just a moment...</h1><p>Please verify you are human by completing the action below.</p><div id="widget"><input type="checkbox" onclick="fetch('/__widget')"></div>`)
	rec := c.run("indeed", portals(t, "indeed", map[string]any{"search": "engineer"}), nil)
	if rec.Status != discovery.RunBlocked || c.site.hit("/__widget") != 0 {
		t.Fatalf("status %s widget hits %d", rec.Status, c.site.hit("/__widget"))
	}
}

func TestIndeed_Chrome_CloudflareCheckboxIsPressedThenTheResultsAreRead(t *testing.T) {
	// The live Indeed shape: the interstitial nests the widget frame in
	// a closed shadow root, the frame is another site (its own process here as
	// in production) and its checkbox sits in a second closed shadow root,
	// away from the frame's corner. Only a trusted press passes.
	c := newRig(t)
	h := "www.indeed.com"
	// cloudflare.com is HSTS-preloaded, so the fixture frame is on another
	// site and is known by the widget's frame id, as the live one also is.
	frame := fmt.Sprintf("http://challenges.cf-fixture.test:%d/cdn-cgi/challenge-platform/turnstile", c.site.port)
	c.site.set(h, "/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := r.Cookie("cf_clearance"); err == nil {
			fmt.Fprint(w, `<!doctype html><html><head><title>engineer jobs</title></head><body><div class="job_seen_beacon"><h2 class="jobTitle"><a class="jcs-JobTitle" data-jk="abc123" href="/viewjob?jk=abc123"><span title="Backend Engineer">Backend Engineer</span></a></h2>
<span data-testid="company-name">Acme</span><div data-testid="text-location">Austin, TX</div><span data-testid="myJobsStateDate">Posted 2 days ago</span></div></body></html>`)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `<!doctype html><html><head><title>Just a moment...</title></head><body><h1>Additional Verification Required</h1><div id="host"></div><script>
const root = document.getElementById('host').attachShadow({mode: 'closed'});
setTimeout(() => { const f = document.createElement('iframe'); f.id = 'cf-chl-widget-x1'; f.src = %q; f.style.cssText = 'width:300px;height:80px;border:0'; root.appendChild(f); }, 300);
addEventListener('message', e => { if (e.data === 'pass') { document.cookie = 'cf_clearance=ok; path=/'; location.reload(); } });
</script></body></html>`, frame)
	})
	c.site.html("challenges.cf-fixture.test", "/cdn-cgi/challenge-platform/turnstile", `<div id="w"></div><script>
const r = document.getElementById('w').attachShadow({mode: 'closed'});
r.innerHTML = '<div style="padding:24px 120px"><label><input type="checkbox" style="width:24px;height:24px;margin:0"> Verify you are human</label></div>';
r.querySelector('input').addEventListener('click', e => { if (e.isTrusted) parent.postMessage('pass', '*'); else fetch('/__untrusted'); });
</script>`)
	c.site.html(h, "/viewjob", `<h1 class="jobsearch-JobInfoHeader-title">Backend Engineer</h1><div id="jobDescriptionText">`+description+`</div>`)
	rec := c.run("indeed", portals(t, "indeed", map[string]any{"search": "engineer"}), nil)
	if rec.Status != discovery.RunCompleted || rec.Counts.Inserted != 1 || rec.Detail["challenges-cleared"] != 1 || c.site.hit("/__untrusted") != 0 {
		t.Fatalf("status %s counts %+v detail %v error %q untrusted %d", rec.Status, rec.Counts, rec.Detail, rec.Error, c.site.hit("/__untrusted"))
	}
}

func TestBrowserScan_Chrome_EmptyWindowAndRetiredRoute(t *testing.T) {
	c := newRig(t)
	c.site.html("www.indeed.com", "/jobs", `<p>No jobs match your search.</p>`)
	rec := c.run("indeed", portals(t, "indeed", map[string]any{"search": "engineer"}), nil)
	if rec.Status != discovery.RunCompleted || rec.Reason != discovery.ReasonEmptyWindow {
		t.Fatalf("with the window: %s %q", rec.Status, rec.Reason)
	}
	rec = c.run("indeed", portals(t, "indeed", map[string]any{"url": c.site.base("www.indeed.com") + "/jobs?q=engineer"}), nil)
	if rec.Status != discovery.RunFailed || rec.Reason != discovery.ReasonNoCards {
		t.Fatalf("without it: %s %q", rec.Status, rec.Reason)
	}
}

// ---- ZipRecruiter, Google Jobs, Glassdoor ----

func TestZipRecruiter_Chrome_ExtractsRedirectCardsAsMirrors(t *testing.T) {
	c := newRig(t)
	h := "www.ziprecruiter.example.test"
	c.site.html(h, "/jobs-search", `<article id="job-card-z1" data-job-id="z1"><a data-testid="job-card-link" href="/c/Acme/Job/Backend-Engineer/-in-Austin,TX?jid=z1"><h2 data-testid="job-card-title">Backend Engineer</h2></a>
<a data-testid="job-card-company">Acme</a><div data-testid="job-card-location">Austin, TX</div><span data-testid="job-card-posted">2 hours ago</span></article>`)
	c.site.html(h, "/c/Acme/Job/Backend-Engineer/-in-Austin,TX", `<h1>Backend Engineer</h1><div data-testid="job-description">`+description+`</div>`)
	rec := c.run("ziprecruiter", portals(t, "ziprecruiter", map[string]any{"search": "engineer"}), nil)
	rows := c.rows()
	if rec.Counts.Inserted != 1 || len(rows) != 1 || !hasFlag(rows[0], "mirror") || !strings.Contains(rows[0].URL, "/c/Acme/Job/Backend-Engineer") || rows[0].Description == "" {
		t.Fatalf("counts %+v rows %+v", rec.Counts, rows)
	}
	// The live results page: each card twice (the desktop list and a hidden
	// mobile one), no link to the posting, which the results page itself shows
	// in its pane when the address names it with lk=<card id>.
	c.site.reset()
	card := func(id, title, company string) string {
		return fmt.Sprintf(`<article id="job-card-%s"><a data-testid="job-card-company" href="/co/%s/Jobs">%s</a><h2 aria-label="%s"><div data-testid="serp-job-card-title">%s</div></h2><a data-testid="job-card-location" href="/jobs-search?location=Austin">Austin, TX</a></article>`, id, company, company, title, title)
	}
	list := card("z2", "Platform Engineer", "Beta") + card("z3", "Data Engineer", "Gamma")
	c.site.set(h, "/jobs-search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pane := ""
		if lk := r.URL.Query().Get("lk"); lk != "" {
			title := map[string]string{"z2": "Platform Engineer", "z3": "Data Engineer"}[lk]
			pane = fmt.Sprintf(`<div data-testid="job-details-scroll-container"><div data-testid="serp-job-details-title">%s</div><div>Austin, TX • On-site</div>
<div data-testid="employer-details-header"><span>Employer %s</span></div><h2>Job description</h2><div class="text-primary whitespace-pre-line">%s</div></div>`, title, lk, description)
		}
		fmt.Fprint(w, `<h1>2 Engineer jobs in Austin, TX</h1><div class="hidden md:block">`+list+`</div><div class="block md:hidden" style="display:none">`+list+`</div>`+pane)
	})
	rec = c.run("ziprecruiter", portals(t, "ziprecruiter", map[string]any{"search": "engineer"}), nil)
	z2, ok2 := rowFor(c.rows(), "lk=z2")
	z3, ok3 := rowFor(c.rows(), "lk=z3")
	if rec.Counts.Discovered != 2 || !ok2 || !ok3 || z2.Role != "Platform Engineer" || z3.Company != "Gamma" || z2.Description == "" {
		t.Fatalf("counts %+v rows %+v", rec.Counts, c.rows())
	}
	// The current layout: one list item per posting, the card an article
	// whose test id carries the posting id, the title an h2 around a button,
	// the company a /co/ link and the pay plain text. A search with no place
	// reads the whole country, not the place ZipRecruiter guesses from the IP.
	c.site.reset()
	place := ""
	card2 := func(id, title, company, pay string) string {
		return fmt.Sprintf(`<li data-testid="job-card-item-%s"><article data-testid="job-card-%s"><button data-testid="job-card-overlay" aria-label="View %s"></button><div><p><span><a href="/co/%s/Jobs/-in-Phoenix,AZ?uuid=x"><span>%s</span></a></span></p></div>
<div><h2 data-testid="serp-job-card-title" aria-label="%s"><button data-testid="job-card-activation"><span>%s</span></button></h2><div><span><span><a href="/jobs-search?location=Phoenix%%2CAZ&amp;search=engineer"><span>Phoenix, AZ</span></a></span> · On-site</span></div><p>%s</p></div><div><div data-testid="new-label-badge"><span>Posted today</span></div></div></article></li>`, id, id, title, company, company, title, title, pay)
	}
	c.site.set(h, "/jobs-search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		place = r.URL.Query().Get("location")
		pane := ""
		if lk := r.URL.Query().Get("lk"); lk != "" {
			pane = `<div data-testid="right-pane"><h2 data-testid="serp-job-details-title">Backend Engineer</h2><div data-testid="employer-details-header"><span>Delta</span></div><div class="text-primary whitespace-pre-line">` + description + `</div></div>`
		}
		fmt.Fprint(w, `<div data-testid="result-count">2 Engineer jobs in United States</div><ul>`+card2("aB_1-x", "Backend Engineer", "Delta", "$87K - $157K/yr")+card2("cD_2", "Data Engineer", "Echo", "")+`</ul>`+pane)
	})
	rec = c.run("ziprecruiter", portals(t, "ziprecruiter", map[string]any{"search": "engineer"}), nil)
	d, ok := rowFor(c.rows(), "lk=aB_1-x")
	if rec.Counts.Discovered != 2 || !ok || d.Role != "Backend Engineer" || d.Company != "Delta" || d.Location != "Phoenix, AZ" || place != "United States" {
		t.Fatalf("counts %+v location %q rows %+v", rec.Counts, place, c.rows())
	}
}

func TestGoogleJobs_Chrome_ResolvesEmployerURLFromThePanel(t *testing.T) {
	c := newRig(t)
	h := "www.google.com"
	share := func(id string) string { return fmt.Sprintf("%s/search?q=engineer&htidocid=%s", c.site.base(h), id) }
	c.site.html(h, "/search", "")
	c.site.set(h, "/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if id := r.URL.Query().Get("htidocid"); id != "" {
			fmt.Fprintf(w, `<h1>Backend Engineer</h1><div class="HBvzbc">%s</div><a href="http://careers.example.com:%d/jobs/%s">Apply on Acme</a>`, description, c.site.port, id)
			return
		}
		fmt.Fprintf(w, `<ul><li data-share-url="%s"><div class="BjJfJf">Backend Engineer</div><div class="vNEEBe">Acme</div><div class="Qk80Jf">Austin, TX</div><span class="LL4CDc">2 hours ago</span></li>
<li data-share-url="%s"><div class="BjJfJf">Data Engineer</div><div class="vNEEBe">Beta</div><div class="Qk80Jf">Remote</div><span class="LL4CDc">5 hours ago</span></li></ul>`, share("D1"), share("D2"))
	})
	rec := c.run("google-jobs", portals(t, "google-jobs", map[string]any{"search": "engineer"}), nil)
	rows := c.rows()
	if rec.Counts.Inserted != 2 || len(rows) != 2 {
		t.Fatalf("counts %+v error %q", rec.Counts, rec.Error)
	}
	if _, ok := rowFor(rows, "/jobs/D1"); !ok {
		t.Fatalf("rows = %+v", rows)
	}
	// The live list (udm=8): a div per posting, the source after the place,
	// an unclassed age; the detail viewer is a labelled group under a
	// feedback dialog's h1.
	c.site.reset()
	c.site.set(h, "/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if id := r.URL.Query().Get("htidocid"); id != "" {
			fmt.Fprintf(w, `<h1>Choose what you are giving feedback on</h1><div role="group" aria-label="Job details for Night Nurse" data-is-preview="true" style="display:none"><div class="knLYHc">Omega</div><div role="heading" aria-level="1">Night Nurse</div></div>
<div role="group" aria-label="Job details for Platform Engineer" data-is-preview="false"><div class="knLYHc">Delta</div><div role="heading" aria-level="1">Platform Engineer</div>
<h2>Job description</h2><span>%s</span><a href="http://careers.example.com:%d/jobs/%s">Apply on Delta Careers</a></div>`, description, c.site.port, id)
			return
		}
		fmt.Fprintf(w, `<div class="EimVGf" data-share-url="%s"><div role="button"><div class="tNxQIb PUpOsf">Platform Engineer</div><div class="wHYlTd MKCbgd a3jPc">Delta</div><div class="wHYlTd FqK3wc MKCbgd">Raleigh, NC &#8226; via Delta Careers</div></div><span><span>3 hours ago</span><span>Full-time</span></span></div>`, share("D3"))
	})
	rec = c.run("google-jobs", portals(t, "google-jobs", map[string]any{"search": "engineer"}), nil)
	row, ok := rowFor(c.rows(), "/jobs/D3")
	if !ok || row.Role != "Platform Engineer" || row.Company != "Delta" || row.Location != "Raleigh, NC" || row.PostedAt.IsZero() || !strings.Contains(row.Description, "backend services") || strings.Contains(row.Description, "Omega") {
		t.Fatalf("counts %+v error %q rows %+v", rec.Counts, rec.Error, c.rows())
	}
}

// The live viewer offers one "Apply on X" link per site, each a google.com/goto
// redirect whose destination only the click reveals, and an aggregator often
// comes first. The employer's own site, named after the company, is followed.
func TestGoogleJobs_Chrome_PrefersTheEmployersOwnApplyLink(t *testing.T) {
	c := newRig(t)
	h := "www.google.com"
	share := fmt.Sprintf("%s/search?q=engineer&htidocid=E1", c.site.base(h))
	c.site.set(h, "/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Query().Get("htidocid") != "" {
			fmt.Fprintf(w, `<div role="group" aria-label="Job details for Platform Engineer" data-is-preview="false"><div class="knLYHc">Delta Systems</div><div role="heading" aria-level="1">Platform Engineer</div>
<h2>Job description</h2><span>%s</span>
<a href="/goto?url=opaque1" target="_blank" title="Apply on LinkedIn">Apply on LinkedIn</a>
<a href="/goto?url=opaque2" target="_blank" title="Apply on BeBee">Apply on BeBee</a>
<a href="/goto?url=opaque3" target="_blank" title="Apply on Delta Careers">Apply on Delta Careers</a></div>`, description)
			return
		}
		fmt.Fprintf(w, `<div class="EimVGf" data-share-url="%s"><div role="button"><div class="tNxQIb PUpOsf">Platform Engineer</div><div class="wHYlTd MKCbgd a3jPc">Delta Systems</div><div class="wHYlTd FqK3wc MKCbgd">Raleigh, NC</div></div><span><span>3 hours ago</span></span></div>`, share)
	})
	c.site.set(h, "/goto", func(w http.ResponseWriter, r *http.Request) {
		dest := map[string]string{
			"opaque1": fmt.Sprintf("http://www.linkedin.com:%d/jobs/view/4000000001", c.site.port),
			"opaque2": fmt.Sprintf("http://bebee.example.com:%d/job/1", c.site.port),
			"opaque3": fmt.Sprintf("http://careers.example.com:%d/jobs/E1", c.site.port),
		}[r.URL.Query().Get("url")]
		http.Redirect(w, r, dest, http.StatusFound)
	})
	for host, path := range map[string]string{"www.linkedin.com": "/jobs/view/4000000001", "bebee.example.com": "/job/1", "careers.example.com": "/jobs/E1"} {
		c.site.html(host, path, `<h1>Platform Engineer</h1><p>`+description+`</p>`)
	}
	rec := c.run("google-jobs", portals(t, "google-jobs", map[string]any{"search": "engineer"}), nil)
	if row, ok := rowFor(c.rows(), "careers.example.com"); !ok || !strings.Contains(row.URL, "/jobs/E1") {
		t.Fatalf("counts %+v error %q rows %+v", rec.Counts, rec.Error, c.rows())
	}
}

func TestGlassdoor_Chrome_ExtractsCardsAndDetectsASignInWall(t *testing.T) {
	c := newRig(t)
	h := "www.glassdoor.com"
	// A live card names the employer in a hashed-class span and carries two
	// links: the title (the posting) and a tracking redirect that changes on
	// every visit.
	// The results page is the site's own address for the search; the old
	// jobs.htm redirects there and drops the freshness window.
	c.site.html(h, "/Job/engineer-jobs-SRCH_KO0,8.htm", `<ul><li data-test="jobListing" data-jobid="77"><span class="EmployerProfile_compactEmployerName__9MGcV">Acme</span>
<a data-test="job-title" href="/job-listing/backend-engineer-acme-JV_IC1.htm?jl=77">Backend Engineer</a><div data-test="emp-location">Austin, TX</div><span data-test="job-age">1d</span>
<a data-test="job-link" href="/partner/jobListing.htm?pos=101&cb=1792219813713&jobListingId=77"></a></li></ul>`)
	c.site.html(h, "/job-listing/backend-engineer-acme-JV_IC1.htm", `<h1>Backend Engineer</h1><div class="JobDetails_jobDescription__x">`+description+`</div>`)
	rec := c.run("glassdoor", portals(t, "glassdoor", map[string]any{"search": "engineer"}), nil)
	if rec.Counts.Inserted != 1 {
		t.Fatalf("counts %+v error %q", rec.Counts, rec.Error)
	}
	if row, ok := rowFor(c.rows(), "/job-listing/backend-engineer-acme-JV_IC1.htm"); !ok || row.Company != "Acme" || row.Location != "Austin, TX" {
		t.Fatalf("the row names the posting and its employer, not the tracking link: %+v", c.rows())
	}
	// A saved search's location becomes the site's own location id: the real
	// lookup script runs in Chrome against the typeahead the site answers.
	var located atomic.Value
	c.site.html(h, "/Job/index.htm", `<h1>Jobs</h1>`)
	c.site.set(h, "/autocomplete/location", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("term") != "Austin, TX" || r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `[{"locationId":1139761,"locationType":"C","locationName":"Austin, TX","longName":"Austin, TX (US)"}]`)
	})
	c.site.set(h, "/Job/austin-tx-engineer-jobs-SRCH_IL.0,9_IC1139761_KO10,18.htm", func(w http.ResponseWriter, r *http.Request) {
		located.Store(r.URL.Path)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// The card prints no age; the page's own data carries it, keyed by the
		// card's job id (the shape of the site's Next.js flight chunks).
		flight, _ := json.Marshal(`5:{"jobListings":[{"jobview":{"header":{"ageInDays":2,"easyApply":true},"job":{"listingId":78}}}]}` + "\n")
		fmt.Fprint(w, `<ul><li data-test="jobListing" data-jobid="78">
<a data-test="job-title" href="/job-listing/data-engineer-beta-JV_IC2.htm?jl=78">Data Engineer</a><div data-test="emp-location">Austin, TX</div></li></ul>
<script>self.__next_f.push([1,`+string(flight)+`])</script>`)
	})
	// A card with no employer takes it from the posting page's header.
	c.site.html(h, "/job-listing/data-engineer-beta-JV_IC2.htm", `<div class="EmployerProfile_employerNameHeading__bXBYr"><h4>Beta</h4></div><h1>Data Engineer</h1><div class="JobDetails_jobDescription__x">`+description+`</div>`)
	rec = c.run("glassdoor", portals(t, "glassdoor", map[string]any{"search": "engineer", "location": "Austin, TX"}), nil)
	query, _ := located.Load().(string)
	if rec.Counts.Inserted != 1 || query == "" {
		t.Fatalf("counts %+v error %q results page %q", rec.Counts, rec.Error, query)
	}
	if row, ok := rowFor(c.rows(), "/job-listing/data-engineer-beta-JV_IC2.htm"); !ok || row.PostedAt.IsZero() || time.Since(row.PostedAt) > 3*24*time.Hour || row.Company != "Beta" {
		t.Fatalf("the card's age comes from the page data: %+v", c.rows())
	}
	c.site.reset()
	c.site.html(h, "/Job/index.htm", `<h1>Jobs</h1>`)
	c.site.set(h, "/autocomplete/location", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"locationId":1,"locationType":"C","longName":"Denver, CO (US)"}]`)
	})
	c.site.set(h, "/Job/denver-engineer-jobs-SRCH_IL.0,6_IC1_KO7,15.htm", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/profile/login_input.htm", http.StatusFound)
	})
	c.site.html(h, "/profile/login_input.htm", `<h1>Welcome back</h1><input type="password" name="p">`)
	rec = c.run("glassdoor", portals(t, "glassdoor", map[string]any{"search": "engineer", "location": "Denver"}), nil)
	if rec.Status != discovery.RunNeedsSignIn {
		t.Fatalf("status %s", rec.Status)
	}
}

// ---- new-grad aggregator and Jobright ----

// The fixture is the server-rendered minisite page, trimmed to three synthetic
// jobs; the real page script reads it in Chrome, so a change to the data's
// shape fails here and not in a live scan.
func TestNewGrad_Chrome_ReadsTheMinisiteDataAndEnrichesFromJobright(t *testing.T) {
	c := newRig(t)
	page, err := os.ReadFile("testdata/newgrad_minisite.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	for i := 1; i <= 3; i++ {
		html = strings.Replace(html, fmt.Sprintf(`"__POSTED_%d__"`, i), fmt.Sprint(time.Now().Add(-time.Duration(i)*time.Hour).UnixMilli()), 1)
	}
	c.site.html("jobright.ai", "/minisites-jobs/newgrad/us/swe", html)
	c.site.html("jobright.ai", "/jobs/info/6a0000000000000000000001", fmt.Sprintf(`<h1>Software Engineer, Platform</h1><div data-testid="job-description">%s</div>
<script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"dataSource":{"jobResult":{"applyLink":"http://careers.example.com:%d/jobs/j1","jobSeniority":"Entry Level","salaryDesc":"$100K/yr","isH1bSponsor":true,"skills":[{"skill":"Go"}]}}}}}</script>`, description, c.site.port))
	rec := c.run("newgrad", portals(t, "newgrad"), nil)
	enriched := 0
	for _, r := range c.rows() {
		if strings.Contains(r.URL, "/jobs/j1") && strings.Contains(r.Description, "Skills: Go") && strings.Contains(r.Description, "Sponsorship: H1B Sponsor Likely") {
			enriched++
		}
	}
	if rec.Counts.Discovered != 3 || rec.Counts.Inserted != 3 || enriched != 1 {
		t.Fatalf("counts %+v enriched %d error %q", rec.Counts, enriched, rec.Error)
	}
}

func TestJobright_Chrome_ReadsTheFeedClosesTheOverlayAndTagsRows(t *testing.T) {
	c := newRig(t)
	h := "jobright.ai"
	// A live card is two sibling links to one posting: the body and the match score.
	c.site.html(h, "/jobs/recommend", `<div class="job-card-flag-classname"><div class="index_job-card-main"><a href="/jobs/info/r1"><span class="index_publish-time">3 hours ago</span><h2 class="index_job-title">Backend Engineer</h2><div class="index_company-name">Acme</div><span class="index_primary-location">Austin, TX</span></a></div><a href="/jobs/info/r1"><div class="index_match">81%</div></a></div>
<div role="dialog" id="upgrade"><p>Upgrade to see more</p><button aria-label="Close" onclick="document.getElementById('upgrade').remove()">x</button><button onclick="fetch('/__write?upgrade')">Upgrade now</button></div>`)
	c.site.html(h, "/jobs/info/r1", `<h1>Backend Engineer</h1><div data-testid="job-description">`+description+`</div>`)
	rec := c.run("jobright", portals(t, "jobright"), nil)
	rows := c.rows()
	if rec.Counts.Discovered != 1 || rec.Counts.Inserted != 1 || len(rows) != 1 || rows[0].Source != "jobright-recommend" || rows[0].Location != "Austin, TX" || c.site.hit("/__write") != 0 {
		t.Fatalf("counts %+v rows %+v error %q", rec.Counts, rows, rec.Error)
	}
	// Signed out: the feed redirects to the home page.
	c.site.reset()
	c.site.set(h, "/jobs/recommend", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/", http.StatusFound) })
	c.site.html(h, "/", `<h1>Find a job</h1><a href="/login">Sign up</a>`)
	rec = c.run("jobright", portals(t, "jobright"), nil)
	if rec.Status != discovery.RunNeedsSignIn {
		t.Fatalf("signed out: %s", rec.Status)
	}
	// Signed out, live: the feed address serves the landing page, whose
	// marquee links to postings, and then rewrites the address to "/".
	c.site.reset()
	c.site.html(h, "/jobs/recommend", `<header><button>SIGN IN</button><button>JOIN NOW</button></header><main><div class="index_agent-landing-first-page__mX9CO"><div class="index_agent-landing-first-page-marquee-wrapper__vTEGe"><div class="index_agent-landing-first-page-marquee-content__Uay6r">
<a class="index_agent-landing-first-page-marquee-card__fC036" target="_blank" href="/jobs/info/m1"><img alt="company-logo"><div><span>Acme · 3 minutes ago</span><span>HR Coordinator</span></div></a>
<a class="index_agent-landing-first-page-marquee-card__fC036" target="_blank" href="/jobs/info/m2"><img alt="company-logo"><div><span>Initech · 1 hour ago</span><span>Talent Sourcer</span></div></a></div></div></div></main>
<script>setTimeout(()=>history.replaceState(null,'','/'),300)</script>`)
	rec = c.run("jobright", portals(t, "jobright"), nil)
	if _, filed := rowFor(c.rows(), "/jobs/info/m"); rec.Status != discovery.RunNeedsSignIn || filed || c.site.hit("/jobs/info/m1") != 0 {
		t.Fatalf("signed-out landing page: %s, a marquee row filed %v, error %q", rec.Status, filed, rec.Error)
	}
}

// ---- LinkedIn feed ----

func TestLinkedInFeed_Chrome_ReadsPostsAndExpandsOneThread(t *testing.T) {
	c := newRig(t)
	c.site.html("www.linkedin.com", "/feed/", fmt.Sprintf(`<div class="feed-shared-update-v2" data-urn="urn:li:activity:1">
<span class="update-components-actor__title">Alex Example</span><div class="update-components-text">We are hiring a backend engineer in Austin. Link in the comments!</div>
<button class="social-details-social-counts__comments" onclick="document.getElementById('c1').style.display='block'">2 comments</button>
<div id="c1" style="display:none"><div class="comments-comment-item"><span class="comments-post-meta__name-text">Alex Example</span><a href="http://careers.example.com:%d/jobs/9">Apply here</a></div></div></div>
<div class="feed-shared-update-v2" data-urn="urn:li:activity:2"><span class="update-components-actor__title">Sam Sample</span><div class="update-components-text">Lovely sunrise today</div></div>`, c.site.port))
	var prompts []string
	var mu sync.Mutex
	c.sc.FeedModel = func(_ context.Context, _, prompt string) (string, error) {
		mu.Lock()
		prompts = append(prompts, prompt)
		mu.Unlock()
		return `{"is_hiring":true,"poster_type":"company","company":"Acme","roles":["Backend Engineer"],"locations":["Austin, TX"],"contact_method":"link","sponsorship":"unknown","confidence":0.9,"apply_links":[]}`, nil
	}
	rec := c.run("linkedin-feed", portals(t, "linkedin-feed"), nil)
	if rec.Status != discovery.RunCompleted {
		t.Fatalf("status %s %s", rec.Status, rec.Error)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 1 || !strings.Contains(prompts[0], "careers.example.com") || !strings.Contains(prompts[0], "/jobs/9") {
		t.Fatalf("prompts = %v: the post without a hiring word was classified, or the author's comment link was not read", prompts)
	}
	if rec.Detail["threads-opened"] != 1 || rec.Counts.Inserted != 1 {
		t.Fatalf("detail %v counts %+v", rec.Detail, rec.Counts)
	}
	if rows := c.rows(); len(rows) != 1 || !strings.Contains(rows[0].URL, "/jobs/9") || rows[0].Source != "linkedin-feed" {
		t.Fatalf("rows = %+v", rows)
	}
	// The server-driven feed: a post is a componentkey "expanded<key>FeedType_..."
	// with hashed classes, no urn and no permalink; the author's name,
	// connection degree and headline are paragraphs beside the profile link,
	// the body is the expandable text box. A suggestions card has no body.
	c.site.reset()
	c.site.html("www.linkedin.com", "/feed/", fmt.Sprintf(`<div componentkey="expandedAAA111FeedType_MAIN_FEED_RELEVANCE"><span>Feed post</span><span>Recommended for you</span><a href="/in/ceo/"><p>Famous Founder</p><p>Founder and CEO</p></a><p>People also follow</p></div>
<div componentkey="expandedBBB222FeedType_MAIN_FEED_RELEVANCE"><span>Feed post</span><div><a href="/in/riley/"><div></div></a><div><a href="/in/riley/"><p>Riley Recruiter</p></a><p>• 3rd+</p><p>Technical Recruiter at Acme</p><p>5m</p></div></div>
<div data-testid="expandable-text-box">We are hiring a platform engineer in Denver. Apply: <a href="http://careers.example.com:%d/jobs/12">careers.example.com/jobs/12</a> <a href="/search/results/all/?keywords=%%23hiring">#hiring</a></div></div>
<div componentkey="expandedCCC333FeedType_MAIN_FEED_RELEVANCE"><span>Feed post</span><a href="/in/sam/"><p>Sam Sample</p></a><p>Gardener</p><p>1h</p><div data-testid="expandable-text-box">Lovely sunrise today</div></div>`, c.site.port))
	prompts = nil
	mu.Unlock()
	rec = c.run("linkedin-feed", portals(t, "linkedin-feed"), nil)
	mu.Lock()
	if rec.Status != discovery.RunCompleted || len(prompts) != 1 || !strings.Contains(prompts[0], "Riley Recruiter") || !strings.Contains(prompts[0], "Technical Recruiter at Acme") || !strings.Contains(prompts[0], "/jobs/12") {
		t.Fatalf("status %s %s, prompts %v", rec.Status, rec.Error, prompts)
	}
	// The model's answer repeats the first post's company and role, so the
	// lead is a repost; the read is what this half checks.

	// The server-driven post's thread: the action bar's "Comment" button
	// (its text is the count) loads the comments, each a
	// CommentComponentReference_<urn> whose profile link carries the name in
	// an aria-hidden span (the post author's own has an "Author" badge and no
	// such span, so the profile address decides) and whose body is an
	// expandable text box. Only the author's own comment links are read; Like
	// and Reply are never pressed.
	c.site.reset()
	c.site.html("www.linkedin.com", "/feed/", fmt.Sprintf(`<div componentkey="expandedDDD444FeedType_MAIN_FEED_RELEVANCE"><span>Feed post</span><div><a href="/in/riley/"><p><span>Riley Recruiter</span></p></a><p>Technical Recruiter at Acme</p><p>2h</p></div>
<div data-testid="expandable-text-box">We are hiring a data engineer in Austin. Link in the comments!</div>
<button aria-label="Reaction button state: no reaction" onclick="fetch('/__wrote/like')"><span>Like</span></button>
<button aria-label="Comment" onclick="document.getElementById('t4').style.display='block'"><span>2</span></button>
<div id="t4" style="display:none" componentkey="commentsSectionContainerDDD444FeedType_MAIN_FEED_RELEVANCE">
<div componentkey="CommentComponentReference_urn:li:comment:(ugcPost:1,2)"><a href="/in/riley/?miniProfile=x"><p><span>Riley Recruiter</span><span>Author</span></p><p><span>Technical Recruiter at Acme</span></p></a>
<p><span data-testid="expandable-text-box">Apply here: <a href="http://careers.example.com:%d/jobs/44">careers.example.com/jobs/44</a></span></p><button aria-label="Reply" onclick="fetch('/__wrote/reply')"></button></div>
<div componentkey="CommentComponentReference_urn:li:comment:(ugcPost:1,3)"><a href="/in/spam/"><p><span><span>Spam Bot</span><span aria-hidden="true">Spam Bot<span> &#8226; 3rd+</span></span></span></p></a>
<p><span data-testid="expandable-text-box">Try <a href="http://spam.example.com:%d/x">this</a></span></p></div></div></div>`, c.site.port, c.site.port))
	prompts = nil
	mu.Unlock()
	rec = c.run("linkedin-feed", portals(t, "linkedin-feed"), nil)
	mu.Lock()
	if rec.Status != discovery.RunCompleted || rec.Detail["threads-opened"] != 1 || len(prompts) != 1 || !strings.Contains(prompts[0], "/jobs/44") || strings.Contains(prompts[0], "spam.example.com") {
		t.Fatalf("status %s %s, detail %v, prompts %v", rec.Status, rec.Error, rec.Detail, prompts)
	}
	if n := c.site.hit("/__wrote/like") + c.site.hit("/__wrote/reply"); n != 0 {
		t.Fatalf("a write control was pressed %d times", n)
	}
}

// The "watch scans" setting moves a scan to the apply window, except for a
// guarded board, and gives way to an apply run that holds the window.
func TestBrowserScan_Chrome_WatchSettingChoosesTheLane(t *testing.T) {
	inst, _ := chromeEnv(t)
	watch := false
	win := launch.ChromeWindow(func(context.Context) (*browser.Instance, error) { return inst, nil }, func(context.Context) bool { return watch })
	ctx := context.Background()
	laneOf := func(host string) (applyTabs, scanTabs int, closeTab func()) {
		tab, err := win.OpenTab(ctx, "scan-test", host)
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		return len(inst.Lane(browser.LaneApply).Tabs()), len(inst.Lane(browser.LaneScan).Tabs()), tab.Close
	}
	base := len(inst.Lane(browser.LaneApply).Tabs())

	a, s, closeTab := laneOf("jobright.ai")
	if a != base || s != 1 {
		t.Fatalf("setting off: apply tabs %d (was %d), scan tabs %d; want the scan lane", a, base, s)
	}
	closeTab()

	watch = true
	a, s, closeTab = laneOf("jobright.ai")
	if a != base+1 || s != 0 {
		t.Fatalf("setting on: apply tabs %d (was %d), scan tabs %d; want the apply window", a, base, s)
	}
	// A second watched tab shares the lease, and the lease ends with the last tab.
	_, _, closeSecond := laneOf("jobright.ai")
	closeTab()
	if _, err := inst.Acquire(browser.LaneApply, "other"); err == nil {
		t.Fatal("the watch lease was released while a watched tab was open")
	}
	closeSecond()
	lease, err := inst.Acquire(browser.LaneApply, "other")
	if err != nil {
		t.Fatalf("the watch lease outlived its last tab: %v", err)
	}

	// A run holds the apply window: the scan stays hidden instead of failing.
	a, s, closeTab = laneOf("jobright.ai")
	if s != 1 {
		t.Fatalf("apply lane busy: scan tabs %d; want the scan lane", s)
	}
	closeTab()
	lease.Release()

	// A guarded board is hidden whatever the setting says.
	a, s, closeTab = laneOf("www.indeed.com")
	if a != base || s != 1 {
		t.Fatalf("guarded board with the setting on: apply tabs %d, scan tabs %d", a, s)
	}
	closeTab()
}

func TestBrowserScan_Chrome_HTTPFailureNeverCompletesAsEmptyWindow(t *testing.T) {
	c := newRig(t)
	c.site.set("www.indeed.com", "/jobs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, "<title>Service unavailable</title><p>Please try later.</p>")
	})
	rec := c.run("indeed", portals(t, "indeed", map[string]any{"search": "engineer"}), nil)
	if rec.Status != discovery.RunFailed || rec.Reason == discovery.ReasonEmptyWindow || rec.Detail["page-failed"] != 1 {
		t.Fatalf("HTTP failure treated as empty window: status=%s reason=%s detail=%v attempts=%+v", rec.Status, rec.Reason, rec.Detail, rec.Attempts)
	}
	if c.site.hit("/jobs") != 2 || len(rec.Attempts) != 1 || rec.Attempts[0].HTTP != 503 || rec.Attempts[0].N != 2 {
		t.Fatalf("expected two attempts at HTTP503: hits=%d attempts=%+v", c.site.hit("/jobs"), rec.Attempts)
	}
}

// ---- Liveness reader ----

// The page read an evaluation is made from reads the posting where the page
// keeps it, in the shapes the owner's postings had (flow round 4): an iCIMS
// posting inside its content frame, a challenge page whose document has no
// body while it reloads, a tracking link's interstitial that a script sends
// on to the posting, a career site whose text opens with its search form and
// whose JobPosting data holds the description, a job board's listing that
// renders its description after its apply control and shows another
// posting's card, and a consent notice. Each posting is open.
func TestLiveness_Chrome_ReadsThePostingWhereThePageKeepsIt(t *testing.T) {
	inst, site := chromeEnv(t)
	reader := launch.ChromeLiveness(func(context.Context) (*browser.Instance, error) { return inst, nil })
	serve := func(host, path string, status int, doc string) {
		site.set(host, path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(status)
			fmt.Fprint(w, "<!doctype html><html>"+doc+"</html>")
		})
	}
	posting := "<h1>Software Engineer</h1><p>" + description + "</p>"
	careers := site.base("careers.northwind.example")
	restored, _ := json.Marshal(posting + `<a href="/jobs/4102/apply">Apply now</a>`)
	ld, _ := json.Marshal(map[string]any{"@context": "https://schema.org", "@type": "JobPosting", "title": "Software Engineer",
		"description": "<p>" + description + "</p><ul><li>Go</li><li>SQL</li></ul>"})
	cases := []struct {
		name, url string
		setup     func()
		check     func(discovery.PageRead) string
	}{
		{"an iCIMS posting in its content frame", site.base("careers-northwind.icims-portal.example") + "/jobs/4101/software-engineer/job?mode=job", func() {
			site.set("careers-northwind.icims-portal.example", "/jobs/4101/software-engineer/job", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				if r.URL.Query().Get("in_iframe") == "1" {
					fmt.Fprint(w, `<!doctype html><html><body><div class="iCIMS_JobContent">`+posting+`<a class="iCIMS_ApplyOnlineButton" href="/jobs/4101/software-engineer/login">Apply for this job online</a></div></body></html>`)
					return
				}
				fmt.Fprint(w, `<!doctype html><html><head><title>iCIMS Careers Portal</title></head><body><iframe id="icims_content_iframe" src="/jobs/4101/software-engineer/job?mode=job&in_iframe=1" style="width:100%;height:900px"></iframe></body></html>`)
			})
		}, func(pg discovery.PageRead) string {
			if !strings.Contains(pg.Text, "Build and run backend services") || !pg.HasApply {
				return "the frame's posting was not read"
			}
			return ""
		}},
		{"a challenge page whose document has no body while it reloads", site.base("jobs.northwind.example") + "/jobs/4102", func() {
			serve("jobs.northwind.example", "/jobs/4102", 200, `<head><title>Just a moment...</title><script>document.addEventListener('DOMContentLoaded',()=>{document.body.remove();
setTimeout(()=>{const b=document.createElement('body');b.innerHTML=`+string(restored)+`;document.documentElement.appendChild(b);document.title='Software Engineer'},1500)})</script></head><body><p>Checking your browser.</p></body>`)
		}, func(pg discovery.PageRead) string {
			if !strings.Contains(pg.Text, "Build and run backend services") {
				return "the reloaded document was not read"
			}
			return ""
		}},
		{"a tracking link's interstitial that a script sends on", site.base("click.northwind-track.example") + "/t/Zx81", func() {
			serve("click.northwind-track.example", "/t/Zx81", 403, `<head><title>One moment</title></head><body><p>Taking you to the job.</p><script>setTimeout(()=>location.replace('`+careers+`/job/4103'),300)</script></body>`)
			serve("careers.northwind.example", "/job/4103", 200, `<head><title>Software Engineer</title></head><body><main>`+posting+`<a href="/apply/4103">Apply</a></main></body>`)
		}, func(pg discovery.PageRead) string {
			if pg.FinalURL != careers+"/job/4103" || pg.Status != 200 {
				return fmt.Sprintf("read ended at %s with status %d", pg.FinalURL, pg.Status)
			}
			return ""
		}},
		{"a career site whose text opens with its search form", careers + "/job/-/-/4104?p_uid=9f2", func() {
			serve("careers.northwind.example", "/job/-/-/4104", 200, `<head><title>Software Engineer at Northwind Example</title><link rel="canonical" href="`+careers+`/job/united-states/software-engineer/4104">
<script type="application/ld+json">`+string(ld)+`</script></head><body><main><form>`+strings.Repeat("Search jobs by keyword, location and team. ", 60)+`</form>`+posting+`<a href="/apply/4104">Apply</a></main></body>`)
		}, func(pg discovery.PageRead) string {
			if !strings.HasPrefix(pg.Description, "Build and run backend services") || !strings.Contains(pg.Description, "\nGo\nSQL") || pg.Canonical != careers+"/job/united-states/software-engineer/4104" {
				return fmt.Sprintf("description %q canonical %q", pg.Description, pg.Canonical)
			}
			return ""
		}},
		{"a job board's listing with another posting's card", site.base("www.linkedin.com") + "/jobs/view/4105", func() {
			desc, _ := json.Marshal(description)
			serve("www.linkedin.com", "/jobs/view/4105", 200, `<head><title>Software Engineer | Northwind Example | LinkedIn</title></head><body><main><h1 class="top-card-layout__title">Software Engineer</h1>
<a class="topcard__org-name-link">Northwind Example</a><button class="apply-button">Apply</button><div class="show-more-less-html__markup"></div>
<h2>People also viewed</h2><ul><li><a href="/jobs/view/data-analyst-at-northwind-example-4106">Data Analyst</a><span>No longer accepting applications</span></li></ul></main>
<script>setTimeout(()=>{document.querySelector('.show-more-less-html__markup').textContent=`+string(desc)+`},600)</script></body>`)
		}, func(pg discovery.PageRead) string {
			if pg.Description != description {
				return fmt.Sprintf("description %q", pg.Description)
			}
			return ""
		}},
		{"a consent notice", site.base("jobs.northwind.example") + "/positions/4107", func() {
			serve("jobs.northwind.example", "/positions/4107", 200, `<head><title>Software Engineer</title></head><body><div id="onetrust-banner-sdk" class="otFlat banner" role="alert"><p>Your consent for this page has expired. Choose your preferences again.</p><button>Accept</button></div>`+
				posting+`<a href="/positions/4107/apply">Apply now</a></body>`)
		}, func(discovery.PageRead) string { return "" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.setup()
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			pg, err := reader.Read(ctx, c.url)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if why := c.check(pg); why != "" {
				t.Errorf("%s: %+v", why, pg)
			}
			if v := discovery.ClassifyLiveness(c.url, pg, discovery.Expect{}); v.Verdict != discovery.LiveActive {
				t.Errorf("verdict %+v for %+v", v, pg)
			}
		})
	}
}
