// Clean-room: docs/provenance.md#CR-AGENT-TAINT

package pageread

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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

// fakeResolver answers each name from a script and counts the lookups.
type fakeResolver struct {
	mu      sync.Mutex
	answers map[string][][]netip.Addr // successive answers per name; the last repeats
	calls   map[string]int
}

func (r *fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.calls == nil {
		r.calls = map[string]int{}
	}
	seq, ok := r.answers[host]
	if !ok {
		return nil, errors.New("no such host")
	}
	i := min(r.calls[host], len(seq)-1)
	r.calls[host]++
	return seq[i], nil
}

func (r *fakeResolver) count(host string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[host]
}

func addrs(s ...string) []netip.Addr {
	var out []netip.Addr
	for _, x := range s {
		out = append(out, netip.MustParseAddr(x))
	}
	return out
}

const publicIP = "93.184.216.34"

// rig serves pages from a local server while the fetcher believes it is
// talking to publicIP, and records every address the fetcher dialed.
type rig struct {
	srv    *httptest.Server
	res    *fakeResolver
	f      *Fetcher
	mu     sync.Mutex
	dialed []string
}

func newRig(t *testing.T, h http.HandlerFunc, answers map[string][][]netip.Addr) *rig {
	t.Helper()
	r := &rig{srv: httptest.NewServer(h), res: &fakeResolver{answers: answers}}
	t.Cleanup(r.srv.Close)
	r.f = New(Options{Resolver: r.res, Timeout: 5 * time.Second, Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		r.mu.Lock()
		r.dialed = append(r.dialed, addr)
		r.mu.Unlock()
		return (&net.Dialer{}).DialContext(ctx, network, r.srv.Listener.Addr().String())
	}})
	return r
}

func (r *rig) dials() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.dialed...)
}

func wantRefusal(t *testing.T, err error, rule string) {
	t.Helper()
	var ref *Refusal
	if !errors.As(err, &ref) || !strings.HasPrefix(ref.Rule, rule) {
		t.Fatalf("err = %v, want refusal %q", err, rule)
	}
}

func html200(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, body)
	}
}

func TestRead_PublicPageIsFetchedAndReducedToText(t *testing.T) {
	r := newRig(t, html200(`<html><head><title>Reef &amp; Co</title><style>p{}</style></head><body><h1>Survey Lead</h1><p>Dive <b>safely</b>.</p><script>var x = "<p>hidden</p>";</script><!-- note --><ul><li>One</li><li>Two</li></ul></body></html>`),
		map[string][][]netip.Addr{"jobs.example.test": {addrs(publicIP)}})
	p, err := r.f.Read(context.Background(), "http://jobs.example.test/p/1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Reef & Co" || p.Status != 200 || p.ContentType != "text/html" {
		t.Fatalf("page = %+v", p)
	}
	for _, want := range []string{"Survey Lead", "Dive safely.", "One\nTwo"} {
		if !strings.Contains(p.Text, want) {
			t.Errorf("text %q lacks %q", p.Text, want)
		}
	}
	for _, bad := range []string{"hidden", "note", "p{}", "<"} {
		if strings.Contains(p.Text, bad) {
			t.Errorf("text %q holds %q", p.Text, bad)
		}
	}
	if got := r.dials(); len(got) != 1 || got[0] != net.JoinHostPort(publicIP, "80") {
		t.Fatalf("dialed %v; the connection must go to the checked address", got)
	}
}

func TestRead_LoopbackListenerIsRefusedBeforeAnyConnection(t *testing.T) {
	r := newRig(t, html200("x"), nil)
	for _, u := range []string{r.srv.URL + "/api/runs", "http://127.0.0.1:47319/api/runs", "http://localhost/x", "http://[::1]/x"} {
		_, err := r.f.Read(context.Background(), u)
		wantRefusal(t, err, "")
		if n := len(r.dials()); n != 0 {
			t.Fatalf("%s: %d connections were made", u, n)
		}
	}
}

func TestRead_MetadataAddressAndSpelledOutAddressAreRefused(t *testing.T) {
	r := newRig(t, html200("x"), nil)
	for u, rule := range map[string]string{
		"http://169.254.169.254/latest/meta-data/": "metadata",
		"http://2130706433/":                       "loopback",
		"http://0x7f000001/":                       "loopback",
		"http://0177.0.0.1/":                       "loopback",
		"http://[::ffff:7f00:1]/":                  "loopback",
		"http://metadata.google.internal/":         "metadata",
		"http://printer.local/":                    "local-name",
		"http://intranet/":                         "single-label",
		"http://100.64.1.1/":                       "carrier-grade-nat",
	} {
		_, err := r.f.Read(context.Background(), u)
		wantRefusal(t, err, rule)
	}
	if n := len(r.dials()); n != 0 {
		t.Fatalf("%d connections were made", n)
	}
}

func TestRead_UserInfoAndOtherSchemesAreRefused(t *testing.T) {
	r := newRig(t, html200("x"), map[string][][]netip.Addr{"jobs.example.test": {addrs(publicIP)}})
	for u, rule := range map[string]string{
		"http://good.example.test@jobs.example.test/p": RuleUserInfo,
		"ftp://jobs.example.test/p":                    RuleScheme,
		"file:///etc/passwd":                           RuleScheme,
		"javascript:alert(1)":                          RuleScheme,
	} {
		_, err := r.f.Read(context.Background(), u)
		wantRefusal(t, err, rule)
	}
}

func TestRead_NameThatResolvesPrivatelyIsRefused(t *testing.T) {
	r := newRig(t, html200("x"), map[string][][]netip.Addr{
		"jobs.example.test":  {addrs("10.0.0.5")},
		"mixed.example.test": {addrs(publicIP, "192.168.1.9")},
	})
	for _, host := range []string{"jobs.example.test", "mixed.example.test"} {
		_, err := r.f.Read(context.Background(), "http://"+host+"/")
		wantRefusal(t, err, RuleResolves+":")
	}
	if n := len(r.dials()); n != 0 {
		t.Fatalf("%d connections were made", n)
	}
}

func TestRead_RedirectToAPrivateHostIsRefused(t *testing.T) {
	r := newRig(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://192.168.1.10/admin")
		w.WriteHeader(http.StatusFound)
	}, map[string][][]netip.Addr{"jobs.example.test": {addrs(publicIP)}})
	_, err := r.f.Read(context.Background(), "http://jobs.example.test/start")
	wantRefusal(t, err, "private-address")
	var ref *Refusal
	errors.As(err, &ref)
	if ref.Host != "192.168.1.10" {
		t.Fatalf("host = %q", ref.Host)
	}
	if got := r.dials(); len(got) != 1 {
		t.Fatalf("dialed %v; only the first hop may connect", got)
	}
}

func TestRead_EveryRedirectHopIsCheckedAgainstTheResolver(t *testing.T) {
	var hits atomic.Int32 // the handler's goroutine counts
	r := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		hits.Add(1)
		switch req.URL.Path {
		case "/a":
			http.Redirect(w, req, "http://other.example.test/b", http.StatusMovedPermanently)
		default:
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "landed")
		}
	}, map[string][][]netip.Addr{"jobs.example.test": {addrs(publicIP)}, "other.example.test": {addrs("10.1.2.3")}})
	_, err := r.f.Read(context.Background(), "http://jobs.example.test/a")
	wantRefusal(t, err, RuleResolves+":private-address")
	if n := hits.Load(); n != 1 {
		t.Fatalf("server hits = %d", n)
	}
}

func TestRead_RelativeRedirectIsFollowedAndSchemeChangeIsRefused(t *testing.T) {
	r := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/old":
			http.Redirect(w, req, "/new", http.StatusFound)
		case "/ftp":
			w.Header().Set("Location", "ftp://jobs.example.test/x")
			w.WriteHeader(http.StatusFound)
		default:
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "new page")
		}
	}, map[string][][]netip.Addr{"jobs.example.test": {addrs(publicIP)}})
	p, err := r.f.Read(context.Background(), "http://jobs.example.test/old")
	if err != nil || p.Text != "new page" {
		t.Fatalf("page = %+v, %v", p, err)
	}
	_, err = r.f.Read(context.Background(), "http://jobs.example.test/ftp")
	wantRefusal(t, err, RuleRedirectTo)
}

func TestRead_AtMostFiveRedirectsAreFollowed(t *testing.T) {
	r := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, req.URL.Path+"x", http.StatusFound)
	}, map[string][][]netip.Addr{"jobs.example.test": {addrs(publicIP)}})
	_, err := r.f.Read(context.Background(), "http://jobs.example.test/")
	wantRefusal(t, err, RuleRedirects)
	if n := len(r.dials()); n != MaxRedirects+1 {
		t.Fatalf("connections = %d, want %d", n, MaxRedirects+1)
	}
}

func TestRead_DNSRebindingCannotChangeTheConnectedAddress(t *testing.T) {
	// The name answers a public address first and a private one on any later
	// lookup. The read resolves once per hop and connects to what it checked.
	r := newRig(t, html200("<p>public content</p>"),
		map[string][][]netip.Addr{"rebind.example.test": {addrs(publicIP), addrs("127.0.0.1")}})
	p, err := r.f.Read(context.Background(), "http://rebind.example.test/")
	if err != nil || p.Text != "public content" {
		t.Fatalf("page = %+v, %v", p, err)
	}
	if r.res.count("rebind.example.test") != 1 {
		t.Fatalf("lookups = %d, want 1", r.res.count("rebind.example.test"))
	}
	if got := r.dials(); len(got) != 1 || !strings.HasPrefix(got[0], publicIP+":") {
		t.Fatalf("dialed %v", got)
	}
}

func TestRead_DNSRebindingAnswerThatContainsAPrivateAddressIsRefused(t *testing.T) {
	r := newRig(t, html200("x"), map[string][][]netip.Addr{"rebind.example.test": {addrs("127.0.0.1", publicIP)}})
	_, err := r.f.Read(context.Background(), "http://rebind.example.test/")
	wantRefusal(t, err, RuleResolves+":loopback")
}

func TestRead_ContentTypeAllowlist(t *testing.T) {
	r := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/pdf":
			w.Header().Set("Content-Type", "application/pdf")
		case "/none":
			w.Header()["Content-Type"] = nil
		case "/json":
			w.Header().Set("Content-Type", "application/json")
		}
		fmt.Fprint(w, `{"a":1}`)
	}, map[string][][]netip.Addr{"jobs.example.test": {addrs(publicIP)}})
	for _, path := range []string{"/pdf", "/none"} {
		_, err := r.f.Read(context.Background(), "http://jobs.example.test"+path)
		wantRefusal(t, err, RuleContentType)
	}
	if p, err := r.f.Read(context.Background(), "http://jobs.example.test/json"); err != nil || p.Text != `{"a":1}` {
		t.Fatalf("json page = %+v, %v", p, err)
	}
}

func TestRead_ResponseSizeAndTimeAreBounded(t *testing.T) {
	release := make(chan struct{})
	r := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/slow" {
			select {
			case <-release:
			case <-req.Context().Done():
			}
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("a", MaxBodyBytes*2)))
	}, map[string][][]netip.Addr{"jobs.example.test": {addrs(publicIP)}})
	t.Cleanup(func() { close(release) })
	p, err := r.f.Read(context.Background(), "http://jobs.example.test/big")
	if err != nil || !p.Truncated || len([]rune(p.Text)) != MaxTextRunes {
		t.Fatalf("page: truncated %v, %d runes, err %v", p.Truncated, len([]rune(p.Text)), err)
	}
	fast := New(Options{Resolver: r.res, Timeout: 100 * time.Millisecond, Dial: r.f.dial})
	if _, err := fast.Read(context.Background(), "http://jobs.example.test/slow"); err == nil {
		t.Fatal("a stalled server must hit the time cap")
	}
}

func TestRead_HTTPSIsFetchedWithTheNameVerifiedAndTheAddressPinned(t *testing.T) {
	srv := httptest.NewTLSServer(html200("<p>secure</p>"))
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	var dialed string
	f := New(Options{
		Resolver: &fakeResolver{answers: map[string][][]netip.Addr{"example.com": {addrs(publicIP)}}},
		TLS:      &tls.Config{RootCAs: pool},
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialed = addr
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		},
	})
	p, err := f.Read(context.Background(), "https://example.com/")
	if err != nil || p.Text != "secure" || dialed != net.JoinHostPort(publicIP, "443") {
		t.Fatalf("page %+v, dialed %q, err %v", p, dialed, err)
	}
}

func TestRead_ProxySettingsFromTheEnvironmentAreIgnored(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("http_proxy", "http://127.0.0.1:1")
	r := newRig(t, html200("direct"), map[string][][]netip.Addr{"jobs.example.test": {addrs(publicIP)}})
	if p, err := r.f.Read(context.Background(), "http://jobs.example.test/"); err != nil || p.Text != "direct" {
		t.Fatalf("page %+v, err %v", p, err)
	}
}

func TestExtractHTML_QuotedAngleBracketsAndUnclosedElements(t *testing.T) {
	_, text := extractHTML(`<a title="x > y">link</a> text<br>next line <div>block</div> <ünï>odd</ünï> a &lt; b`)
	for _, want := range []string{"link text", "next line", "block", "a < b"} {
		if !strings.Contains(text, want) {
			t.Errorf("text %q lacks %q", text, want)
		}
	}
	if _, text := extractHTML("before <script>never closed"); strings.TrimSpace(text) != "before" {
		t.Errorf("text = %q", text)
	}
}
