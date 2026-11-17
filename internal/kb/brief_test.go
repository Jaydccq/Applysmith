package kb

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubFetcher serves canned pages and counts requests per URL.
type stubFetcher struct {
	mu     sync.Mutex
	pages  map[string]*Page
	errs   map[string]error
	counts map[string]int
	gate   chan struct{}
}

func (s *stubFetcher) Fetch(ctx context.Context, u string) (*Page, error) {
	s.mu.Lock()
	if s.counts == nil {
		s.counts = map[string]int{}
	}
	s.counts[u]++
	p, err := s.pages[u], s.errs[u]
	gate := s.gate
	s.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if err != nil {
		return nil, err
	}
	if p == nil {
		return &Page{URL: u, Status: 404, ContentType: "text/html"}, nil
	}
	return p, nil
}

func (s *stubFetcher) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.counts {
		n += c
	}
	return n
}

func (s *stubFetcher) count(u string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[u]
}

func ldPage(orgURL string, sameAs ...string) *Page {
	same := ""
	if len(sameAs) > 0 {
		same = `,"sameAs":["` + strings.Join(sameAs, `","`) + `"]`
	}
	url := ""
	if orgURL != "" {
		url = `"url":"` + orgURL + `",`
	}
	body := `<html><head><script type="application/ld+json">{"@type":"JobPosting","hiringOrganization":{` + url + `"name":"Example Corp"` + same + `}}</script></head><body>Job</body></html>`
	return &Page{Status: 200, ContentType: "text/html", Body: []byte(body)}
}

func sitePage(sentence string) *Page {
	body := "<html><head><style>.x{color:red}</style><script>var a=1;</script></head><body><h1>Example Test</h1>" +
		strings.Repeat("<p>"+sentence+"</p>", 3) + "<p>We build reef sensors for coastal research teams around the world and ship them quickly.</p>" +
		"<p>Our engineers calibrate every unit in a saltwater tank before it leaves the workshop, and our support team answers questions from field crews in every time zone.</p></body></html>"
	return &Page{Status: 200, ContentType: "text/html; charset=utf-8", Body: []byte(body)}
}

const (
	appURL = "https://boards.example.org/apply/123"
	jobURL = "https://boards.example.org/jobs/123"
)

func briefFixture(t *testing.T, sf *stubFetcher) *fixture {
	return newFixture(t, func(o *Options) { o.Fetcher = sf })
}

func TestBrief_DeclaredSiteOnly_NeverGuessed(t *testing.T) {
	sf := &stubFetcher{pages: map[string]*Page{
		appURL:                      ldPage("https://www.example.test"),
		"https://www.example.test/": sitePage("Example Test makes reef sensors."),
	}}
	f := briefFixture(t, sf)
	b, err := f.svc.EnsureBrief(f.ctx, BriefRequest{Company: "Example Corp", ApplicationURL: appURL, JobURL: jobURL})
	if err != nil {
		t.Fatal(err)
	}
	if b.Domain != "example.test" || b.Path != "knowledge/companies/example.test.md" || b.Manual {
		t.Fatalf("brief = %+v", b)
	}
	data := f.read(b.Path)
	for _, want := range []string{"source_url: https://www.example.test/", "fetched_at:", "expires_at:", "reef sensors"} {
		if !strings.Contains(data, want) {
			t.Errorf("brief file lacks %q:\n%s", want, data)
		}
	}
	if strings.Contains(data, "color:red") || strings.Contains(data, "var a=1") {
		t.Fatalf("scripts or styles survived:\n%s", data)
	}
}

func TestBrief_NoGuessing(t *testing.T) {
	sf := &stubFetcher{pages: map[string]*Page{
		appURL: {Status: 200, ContentType: "text/html", Body: []byte("<html>nothing structured</html>")},
		jobURL: {Status: 200, ContentType: "text/html", Body: []byte("<html>nothing structured</html>")},
	}}
	f := briefFixture(t, sf)
	_, err := f.svc.EnsureBrief(f.ctx, BriefRequest{Company: "Example Corp", ApplicationURL: appURL, JobURL: jobURL})
	if !errors.Is(err, ErrNoSite) {
		t.Fatalf("err = %v", err)
	}
	for u := range sf.counts {
		if strings.Contains(u, "example") && !strings.HasPrefix(u, "https://boards.example.org/") {
			t.Fatalf("a site was requested that no page declared: %s", u)
		}
	}
	if sf.total() != 2 {
		t.Fatalf("requests = %v", sf.counts)
	}
	if list, _ := f.svc.List(f.ctx); len(list) != 0 {
		t.Fatalf("a brief exists: %v", list)
	}
}

func TestBrief_RejectedHosts(t *testing.T) {
	for _, bad := range []string{
		"https://boards.greenhouse.io/examplecorp", "https://jobs.lever.co/examplecorp", "https://www.linkedin.com/company/example",
		"https://twitter.com/example", "https://www.crunchbase.com/organization/example", "http://localhost:8080", "https://127.0.0.1/",
		"https://10.1.2.3/", "http://[::1]/", "https://yourcompany.com", "https://corp.internal/", "https://acme.myworkdayjobs.com/en/careers", "ftp://example.test/",
	} {
		if _, ok := acceptSite(bad); ok {
			t.Errorf("accepted %s", bad)
		}
	}
	if u, ok := acceptSite("example.test"); !ok || u.Host != "example.test" || u.Scheme != "https" {
		t.Errorf("bare domain = %v %v", u, ok)
	}
	// url is read before sameAs, and a directory in sameAs is skipped.
	sf := &stubFetcher{pages: map[string]*Page{
		appURL: ldPage("", "https://www.linkedin.com/company/example", "https://www.example.test"),
	}}
	f := briefFixture(t, sf)
	site, err := f.svc.DeclaredSite(f.ctx, BriefRequest{Company: "Example Corp", ApplicationURL: appURL, JobURL: jobURL})
	if err != nil || site.Host != "www.example.test" {
		t.Fatalf("site = %v, %v", site, err)
	}
	if _, err := f.svc.DeclaredSite(f.ctx, BriefRequest{Company: "Careers", ApplicationURL: appURL}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("generic name = %v", err)
	}
}

func TestBrief_ApplicationPageWinsOverJobPage(t *testing.T) {
	sf := &stubFetcher{pages: map[string]*Page{appURL: ldPage("https://one.example.test"), jobURL: ldPage("https://two.example.test")}}
	f := briefFixture(t, sf)
	site, err := f.svc.DeclaredSite(f.ctx, BriefRequest{Company: "Example Corp", ApplicationURL: appURL, JobURL: jobURL})
	if err != nil || site.Host != "one.example.test" {
		t.Fatalf("site = %v, %v", site, err)
	}
	sf2 := &stubFetcher{pages: map[string]*Page{appURL: ldPage(""), jobURL: ldPage("https://two.example.test")}}
	f2 := briefFixture(t, sf2)
	site, err = f2.svc.DeclaredSite(f2.ctx, BriefRequest{Company: "Example Corp", ApplicationURL: appURL, JobURL: jobURL})
	if err != nil || site.Host != "two.example.test" {
		t.Fatalf("fallback to the job page = %v, %v", site, err)
	}
}

func TestBrief_ConcurrentCallersShareOneFetch(t *testing.T) {
	gate := make(chan struct{})
	sf := &stubFetcher{pages: map[string]*Page{appURL: ldPage("https://www.example.test")}, gate: gate}
	f := briefFixture(t, sf)
	req := BriefRequest{Company: "Example Corp", ApplicationURL: appURL}
	var wg sync.WaitGroup
	errs := make([]error, 3)
	cancelCtx, cancel := context.WithCancel(f.ctx)
	first := make(chan struct{})
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := f.ctx
			if i == 0 {
				c = cancelCtx
				defer close(first)
			}
			_, errs[i] = f.svc.DeclaredSite(c, req)
		}()
	}
	// Wait until one fetch runs and the two other callers joined it.
	for deadline := time.Now().Add(10 * time.Second); sf.count(appURL) != 1 || f.svc.flights.Joined() != 2; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("fetches %d, joined %d", sf.count(appURL), f.svc.flights.Joined())
		}
	}
	cancel() // one caller aborts
	<-first
	close(gate)
	wg.Wait()
	if !errors.Is(errs[0], context.Canceled) || errs[1] != nil || errs[2] != nil {
		t.Fatalf("errors = %v", errs)
	}
	if sf.count(appURL) != 1 {
		t.Fatalf("fetches = %d", sf.count(appURL))
	}
}

func TestBrief_Expiry(t *testing.T) {
	sf := &stubFetcher{pages: map[string]*Page{appURL: ldPage("https://www.example.test"), "https://www.example.test/": sitePage("Example Test makes reef sensors.")}}
	f := briefFixture(t, sf)
	req := BriefRequest{Company: "Example Corp", ApplicationURL: appURL}
	if _, err := f.svc.EnsureBrief(f.ctx, req); err != nil {
		t.Fatal(err)
	}
	if r := f.search("reef sensors coastal research"); len(r.Facts) == 0 || r.Facts[0].SourceKind != KindBrief {
		t.Fatalf("fresh brief not evidence: %+v", r.Facts)
	}
	f.clk.advance(91 * 24 * time.Hour)
	if r := f.search("reef sensors coastal research"); len(r.Facts) != 0 {
		t.Fatalf("an expired brief was returned: %v", ids(r))
	}
	before := sf.count("https://www.example.test/")
	if _, err := f.svc.EnsureBrief(f.ctx, req); err != nil {
		t.Fatal(err)
	}
	if sf.count("https://www.example.test/") != before+1 {
		t.Fatal("no refetch on next need")
	}
	if r := f.search("reef sensors coastal research"); len(r.Facts) == 0 {
		t.Fatal("the refreshed brief is not evidence")
	}
}

func TestBrief_RefetchMissKeepsOldFile(t *testing.T) {
	sf := &stubFetcher{pages: map[string]*Page{appURL: ldPage("https://www.example.test"), "https://www.example.test/": sitePage("Example Test makes reef sensors.")}}
	f := briefFixture(t, sf)
	req := BriefRequest{Company: "Example Corp", ApplicationURL: appURL}
	b, _ := f.svc.EnsureBrief(f.ctx, req)
	old := f.read(b.Path)
	f.clk.advance(91 * 24 * time.Hour)
	sf.mu.Lock()
	delete(sf.pages, "https://www.example.test/")
	sf.mu.Unlock()
	if _, err := f.svc.EnsureBrief(f.ctx, req); !errors.Is(err, ErrNoBrief) {
		t.Fatalf("err = %v", err)
	}
	if f.read(b.Path) != old {
		t.Fatal("the expired file changed")
	}
	if r := f.search("reef sensors coastal research"); len(r.Facts) != 0 {
		t.Fatal("a missed refetch made the expired brief evidence")
	}
	// A 24 hour miss is recorded: no further requests inside the window (the
	// declaring page itself is remembered for 30 minutes).
	n := sf.total()
	f.svc.EnsureBrief(f.ctx, req)
	if sf.total() != n {
		t.Fatalf("requests grew by %d", sf.total()-n)
	}
	f.clk.advance(25 * time.Hour)
	n = sf.total()
	f.svc.EnsureBrief(f.ctx, req)
	if sf.total() <= n+1 {
		t.Fatal("the miss memo outlived 24 hours")
	}
}

type summarizerFunc func()

func (f summarizerFunc) Summarize(context.Context, string) (string, error) {
	f()
	return "condensed", nil
}

// A refresh whose context ends before the state is recorded leaves the old
// file, so the brief is not mistaken for a user's edit and refreshes later.
func TestBrief_RefreshCancelledBeforeRecordKeepsTheOldFile(t *testing.T) {
	sf := &stubFetcher{pages: map[string]*Page{appURL: ldPage("https://www.example.test"), "https://www.example.test/": sitePage("Example Test makes reef sensors.")}}
	cancel := func() {}
	f := newFixture(t, func(o *Options) { o.Fetcher = sf; o.Summarizer = summarizerFunc(func() { cancel() }) })
	req := BriefRequest{Company: "Example Corp", ApplicationURL: appURL}
	b, err := f.svc.EnsureBrief(f.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	old := f.read(b.Path)
	f.clk.advance(91 * 24 * time.Hour)
	ctx, c := context.WithCancel(f.ctx)
	cancel = c
	if _, err := f.svc.EnsureBrief(ctx, req); err == nil {
		t.Fatal("a refresh under a cancelled context reported success")
	}
	if f.read(b.Path) != old {
		t.Fatal("the file was replaced without its state")
	}
	cancel = func() {}
	before := sf.count("https://www.example.test/")
	if b, err := f.svc.EnsureBrief(f.ctx, req); err != nil || b.Manual || sf.count("https://www.example.test/") != before+1 {
		t.Fatalf("the next need did not refresh: %+v, %v", b, err)
	}
}

func TestBrief_ManualBriefNeverExpires(t *testing.T) {
	sf := &stubFetcher{pages: map[string]*Page{appURL: ldPage("https://www.example.test"), "https://www.example.test/": sitePage("Fetched text that must not replace the manual brief.")}}
	f := briefFixture(t, sf)
	f.write("knowledge/companies/example.test.md", "---\ntitle: Example Test\n---\n\nHand written note: Example Test makes reef sensors.\n")
	f.clk.advance(400 * 24 * time.Hour)
	if r := f.search("reef sensors Example Test"); len(r.Facts) == 0 || r.Facts[0].SourceKind != KindBrief {
		t.Fatalf("manual brief not evidence: %+v", r.Facts)
	}
	b, err := f.svc.EnsureBrief(f.ctx, BriefRequest{Company: "Example Corp", ApplicationURL: appURL})
	if err != nil || !b.Manual {
		t.Fatalf("brief = %+v, %v", b, err)
	}
	if sf.count("https://www.example.test/") != 0 {
		t.Fatal("a manual brief was refetched")
	}
	if !strings.Contains(f.read("knowledge/companies/example.test.md"), "Hand written note") {
		t.Fatal("the manual brief was overwritten")
	}
}

func TestBrief_UserEditMakesItManual(t *testing.T) {
	sf := &stubFetcher{pages: map[string]*Page{appURL: ldPage("https://www.example.test"), "https://www.example.test/": sitePage("Example Test makes reef sensors.")}}
	f := briefFixture(t, sf)
	req := BriefRequest{Company: "Example Corp", ApplicationURL: appURL}
	f.svc.EnsureBrief(f.ctx, req)
	d, err := f.svc.Show(f.ctx, "example.test")
	if err != nil {
		t.Fatal(err)
	}
	edited, err := f.svc.Edit(f.ctx, "example.test", strings.Replace(d.Markdown, "reef sensors", "reef sensors and buoys", 1), d.FileHash)
	if err != nil || !strings.Contains(edited.Markdown, "manual: true") {
		t.Fatalf("edit = %+v, %v", edited, err)
	}
	f.clk.advance(200 * 24 * time.Hour)
	before := sf.count("https://www.example.test/")
	b, err := f.svc.EnsureBrief(f.ctx, req)
	if err != nil || !b.Manual || sf.count("https://www.example.test/") != before {
		t.Fatalf("an edited brief was refetched: %+v, %v", b, err)
	}
}

func TestBrief_SecretInPageIsRefused(t *testing.T) {
	leak := "sk" + "-" + "q7Zr4TbX9mLw2KdVn8Ys3HcFa5JgP6Ue"
	page := sitePage("Example Test makes reef sensors. Our public key " + leak + " is listed here.")
	sf := &stubFetcher{pages: map[string]*Page{appURL: ldPage("https://www.example.test"), "https://www.example.test/": page}}
	f := briefFixture(t, sf)
	_, err := f.svc.EnsureBrief(f.ctx, BriefRequest{Company: "Example Corp", ApplicationURL: appURL})
	if !errors.Is(err, ErrSecret) || f.exists("knowledge/companies/example.test.md") {
		t.Fatalf("err = %v", err)
	}
}

func TestBrief_Fetch_PrivateAddressAndRedirects(t *testing.T) {
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { hits.Add(1); fmt.Fprint(w, "ok") })
	mux.HandleFunc("/hop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://internal.example.test/", http.StatusFound)
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/loop", http.StatusFound) })
	mux.HandleFunc("/ftp", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "ftp://example.test/", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	addr := srv.Listener.Addr().(*net.TCPAddr)
	loop := netip.MustParseAddr(addr.IP.String())

	// The test server is on loopback; the lookup maps a public-looking name to
	// it and a second name to a private address, and only the address guard's
	// verdict is replaced so that the redirect check is exercised.
	f := &GuardedFetcher{
		lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
			if host == "internal.example.test" {
				return []netip.Addr{netip.MustParseAddr("10.0.0.5")}, nil
			}
			return []netip.Addr{loop}, nil
		},
		allow: func(a netip.Addr) bool { return a == loop },
	}
	base := fmt.Sprintf("http://public.example.test:%d", addr.Port)
	if p, err := f.Fetch(context.Background(), base+"/"); err != nil || string(p.Body) != "ok" {
		t.Fatalf("plain fetch = %v", err)
	}
	if _, err := f.Fetch(context.Background(), base+"/hop"); !errors.Is(err, ErrRefused) {
		t.Fatalf("redirect into a private address = %v", err)
	}
	if _, err := f.Fetch(context.Background(), base+"/loop"); !errors.Is(err, ErrRefused) {
		t.Fatalf("redirect loop = %v", err)
	}
	if _, err := f.Fetch(context.Background(), base+"/ftp"); !errors.Is(err, ErrRefused) {
		t.Fatalf("redirect to ftp = %v", err)
	}
	if _, err := f.Fetch(context.Background(), "file:///etc/passwd"); !errors.Is(err, ErrRefused) {
		t.Fatalf("file scheme = %v", err)
	}
}

func TestBrief_PublicAddrPolicy(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "93.184.216.34": true, "127.0.0.1": false, "10.0.0.1": false, "192.168.1.1": false, "172.16.5.5": false,
		"169.254.169.254": false, "::1": false, "fe80::1": false, "fd00:ec2::254": false, "100.64.0.1": false, "0.0.0.0": false,
		"::ffff:127.0.0.1": false,
	} {
		if got := publicAddr(netip.MustParseAddr(addr)); got != want {
			t.Errorf("publicAddr(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestBrief_Fetch_BodyCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(strings.Repeat("a", 3<<20)))
	}))
	defer srv.Close()
	f := &GuardedFetcher{allow: func(netip.Addr) bool { return true }}
	p, err := f.Fetch(context.Background(), srv.URL)
	if err != nil || len(p.Body) != fetchMaxBody {
		t.Fatalf("body = %d, %v", len(p.Body), err)
	}
}
