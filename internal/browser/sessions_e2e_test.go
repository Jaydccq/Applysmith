//go:build browser

package browser

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"

	"applysmith/internal/api/wire"
)

// cookieEcho records the names of the cookies each request carried, by host.
type cookieEcho struct {
	mu   sync.Mutex
	seen map[string][]string
	srv  *httptest.Server
}

func newCookieEcho(t *testing.T) *cookieEcho {
	t.Helper()
	e := &cookieEcho{seen: map[string][]string{}}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	e.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var names []string
		for _, c := range r.Cookies() {
			names = append(names, c.Name)
		}
		slices.Sort(names)
		host, _, _ := net.SplitHostPort(r.Host)
		e.mu.Lock()
		e.seen[host] = names
		e.mu.Unlock()
		_, _ = w.Write([]byte("<html><body>ok</body></html>"))
	}))
	e.srv.Listener = l
	e.srv.Start()
	t.Cleanup(e.srv.Close)
	return e
}

func (e *cookieEcho) url(host string) string {
	return fmt.Sprintf("http://%s:%d/whoami", host, e.srv.Listener.Addr().(*net.TCPAddr).Port)
}

func (e *cookieEcho) sent(host string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return strings.Join(e.seen[host], ",")
}

func browserNames(t *testing.T, jar CookieJar, host string) string {
	t.Helper()
	cs, err := jar.Cookies(ctx30(t))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range cs {
		if scoped(c.host(), host) {
			names = append(names, c.Name)
		}
	}
	slices.Sort(names)
	return strings.Join(names, ",")
}

func lax(domain, name string, hostOnly bool) wire.SessionCookie {
	exp := float64(time.Now().Add(48 * time.Hour).Unix())
	return wire.SessionCookie{Name: name, Value: "value-" + name, Domain: domain, Path: "/", HTTPOnly: name == "b", HostOnly: hostOnly, SameSite: "lax", ExpirationDate: &exp}
}

// TestSessionsAgainstARealProfile drives the sign-in sync against a throwaway
// Chrome profile: cookies for a fictional host are imported, read back and
// delivered by the browser, the undo restores the earlier ones and a cookie
// outside the request is refused.
func TestSessionsAgainstARealProfile(t *testing.T) {
	dir := t.TempDir()
	o := testOptions(t, dir)
	snapDir := filepath.Join(dir, "snapshots")
	s := InstanceSessions(o, snapDir, nil, nil)
	echo := newCookieEcho(t)

	// Listing sites never starts the browser.
	if _, err := s.Sites(ctx30(t)); err != nil || running(o.ProfileDir) != nil {
		t.Fatalf("sites started the browser: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	jar, err := s.cfg.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	inst := jar.(*Instance)
	t.Cleanup(func() { inst.Stop(context.Background()) })

	oldExpiry := float64(time.Now().Add(72 * time.Hour).Unix())
	for _, c := range []Cookie{
		{Name: "old_a", Value: "old-value", Domain: "jobs.example.test", Path: "/", Expires: oldExpiry, SameSite: "Lax"},
		{Name: "other_keep", Value: "kept", Domain: "other.example.test", Path: "/", Expires: oldExpiry, SameSite: "Lax"},
	} {
		if err := jar.SetCookie(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	scan, err := inst.Lane(LaneScan).OpenTab(ctx30(t), "sessions-e2e")
	if err != nil {
		t.Fatal(err)
	}
	visit := func(host string) {
		t.Helper()
		if _, err := inst.Lane(LaneScan).Navigate(ctx30(t), scan, echo.url(host)); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("Import with read-back", func(t *testing.T) {
		dropped := lax("jobs.example.test", "e", true)
		dropped.SameSite = "no_restriction"
		session := lax("jobs.example.test", "d", true)
		session.Session, session.ExpirationDate = true, nil
		res, err := s.Import(ctx30(t), []string{"jobs.example.test"}, []wire.SessionCookie{
			lax("jobs.example.test", "a", true), lax("jobs.example.test", "b", true), lax(".jobs.example.test", "c", false), session, dropped,
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.Imported != 4 || res.Failed != 1 || res.Replaced != 1 || res.NotInSource != 0 {
			t.Fatalf("%+v", res)
		}
		if got := browserNames(t, jar, "jobs.example.test"); got != "a,b,c,d" {
			t.Fatalf("the profile holds exactly the cookies that stayed: %s", got)
		}
		if got := browserNames(t, jar, "other.example.test"); got != "other_keep" {
			t.Fatalf("a cookie outside the request was touched: %s", got)
		}
		cs, _ := jar.Cookies(ctx30(t))
		for _, c := range cs {
			switch c.Name {
			case "a":
				if c.Domain != "jobs.example.test" || c.SameSite != "Lax" {
					t.Errorf("host-only scope lost: %s", c.Domain)
				}
			case "b":
				if !c.HTTPOnly {
					t.Error("HttpOnly lost")
				}
			case "c":
				if c.Domain != ".jobs.example.test" {
					t.Errorf("domain scope lost: %s", c.Domain)
				}
			case "d":
				if d := time.Until(time.Unix(int64(c.Expires), 0)); d < 29*24*time.Hour || d > 31*24*time.Hour {
					t.Errorf("a session cookie gets about 30 days, got %v", d)
				}
			}
		}
		visit("jobs.example.test")
		if got := echo.sent("jobs.example.test"); got != "a,b,c,d" {
			t.Fatalf("the browser sends the imported cookies: %s", got)
		}
		visit("www.jobs.example.test")
		if got := echo.sent("www.jobs.example.test"); got != "c" {
			t.Fatalf("only the domain cookie reaches a subdomain: %s", got)
		}
	})

	t.Run("Snapshot file", func(t *testing.T) {
		st, err := os.Stat(filepath.Join(snapDir, snapshotFile))
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("%v %v", st, err)
		}
		data, _ := os.ReadFile(filepath.Join(snapDir, snapshotFile))
		if !strings.Contains(string(data), "old_a") || strings.Contains(string(data), "other_keep") {
			t.Fatal("the snapshot holds the replaced cookies and only those")
		}
		if !s.UndoAvailable() {
			t.Fatal("undo is available")
		}
	})

	t.Run("Sites counts the profile's cookies", func(t *testing.T) {
		sites, err := s.Sites(ctx30(t))
		if err != nil {
			t.Fatal(err)
		}
		for _, site := range sites {
			if site.Domain == "jobs.example.test" && site.Cookies == 4 {
				return
			}
		}
		t.Fatalf("%+v", sites)
	})

	t.Run("Out-of-scope cookie", func(t *testing.T) {
		before := browserNames(t, jar, "example.test")
		_, err := s.Import(ctx30(t), []string{"jobs.example.test"}, []wire.SessionCookie{lax("jobs.example.test", "z", true), lax("other.example.test", "z", true)})
		if code, _ := CodeOf(err); code != CodeCookieOutOfScope {
			t.Fatalf("%v", err)
		}
		if after := browserNames(t, jar, "example.test"); after != before {
			t.Fatalf("nothing changes: %s -> %s", before, after)
		}
	})

	t.Run("Site not signed in at the source", func(t *testing.T) {
		res, err := s.Import(ctx30(t), []string{"other.example.test"}, nil)
		if err != nil || res.NotInSource != 1 || browserNames(t, jar, "other.example.test") != "other_keep" {
			t.Fatalf("%+v %v", res, err)
		}
	})

	t.Run("Snapshot first", func(t *testing.T) {
		blocker := filepath.Join(dir, "blocker")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		broken := &Sessions{cfg: SessionsConfig{Dir: filepath.Join(blocker, "x"), Open: s.cfg.Open}, now: time.Now}
		before := browserNames(t, jar, "jobs.example.test")
		_, err := broken.Import(ctx30(t), []string{"jobs.example.test"}, []wire.SessionCookie{lax("jobs.example.test", "late", true)})
		if code, _ := CodeOf(err); code != CodeSnapshotFailed {
			t.Fatalf("%v", err)
		}
		if after := browserNames(t, jar, "jobs.example.test"); after != before {
			t.Fatalf("the profile changed: %s -> %s", before, after)
		}
	})

	t.Run("Undo restores", func(t *testing.T) {
		res, err := s.Undo(ctx30(t))
		if err != nil || res.Restored != 1 || res.Failed != 0 {
			t.Fatalf("%+v %v", res, err)
		}
		if got := browserNames(t, jar, "jobs.example.test"); got != "old_a" {
			t.Fatalf("the cookies from before are back: %s", got)
		}
		cs, _ := jar.Cookies(ctx30(t))
		for _, c := range cs {
			if c.Name == "old_a" && (c.Value != "old-value" || c.Expires != oldExpiry) {
				t.Errorf("restored with its value and expiry: %v %v", c.Expires, oldExpiry)
			}
		}
		if _, err := os.Stat(filepath.Join(snapDir, snapshotFile)); !os.IsNotExist(err) || s.UndoAvailable() {
			t.Fatal("the snapshot is deleted")
		}
		visit("jobs.example.test")
		if got := echo.sent("jobs.example.test"); got != "old_a" {
			t.Fatalf("the browser sends the restored cookies: %s", got)
		}
	})
}

// A cookie with a partition key cannot be set back, so it is left out of the
// listing, the replacement and the restore: an import for its site does not
// touch it.
func TestE2E_Browser_ImportSkipsPartitionedCookies(t *testing.T) {
	inst, _ := ownInstance(t, nil)
	exp := cdp.TimeSinceEpoch(float64(time.Now().Add(48 * time.Hour).Unix()))
	_, err := chromedp.CallBrowser(inst.root, storage.SetCookies, storage.SetCookiesParams{Cookies: []*network.CookieParam{{
		Name: "partitioned", Value: "p", Domain: "jobs.example.test", Path: "/", Secure: true, SameSite: network.CookieSameSiteNone, Expires: exp,
		PartitionKey: &network.CookiePartitionKey{TopLevelSite: "https://example.test", HasCrossSiteAncestor: true},
	}}})
	ok(t, err)
	raw := func() int {
		res, err := chromedp.CallBrowser(inst.root, storage.GetCookies, storage.GetCookiesParams{})
		ok(t, err)
		n := 0
		for _, c := range res.Cookies {
			if c.Name == "partitioned" {
				n++
			}
		}
		return n
	}
	if raw() != 1 {
		t.Skip("this Chrome did not store the partitioned cookie")
	}
	if names := browserNames(t, inst, "jobs.example.test"); strings.Contains(names, "partitioned") {
		t.Fatalf("the listing holds a partitioned cookie: %s", names)
	}
	s := NewSessions(SessionsConfig{Dir: t.TempDir(), Open: func(context.Context) (CookieJar, error) { return inst, nil }})
	res, err := s.Import(ctx30(t), []string{"jobs.example.test"}, []wire.SessionCookie{lax("jobs.example.test", "a", true)})
	ok(t, err)
	if res.Imported != 1 || res.Replaced != 0 {
		t.Fatalf("%+v", res)
	}
	if raw() != 1 {
		t.Fatal("the import removed a partitioned cookie")
	}
	if _, err := s.Undo(ctx30(t)); err != nil {
		t.Fatal(err)
	}
	if raw() != 1 {
		t.Fatal("the undo removed a partitioned cookie")
	}
}
