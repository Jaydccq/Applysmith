//go:build browser

package browser

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestSignInProbeAgainstARealProfile reads a mock site's account page in a
// scan tab: signed out while the profile holds no cookie, signed in after the
// site's cookie is set, a challenge page as a captcha. The page holds a form
// that would record a POST if the probe typed or clicked.
func TestSignInProbeAgainstARealProfile(t *testing.T) {
	var posts atomic.Int32
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
		}
		switch r.URL.Path {
		case "/account":
			if _, err := r.Cookie("sid"); err != nil {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			_, _ = w.Write([]byte(`<html><head><title>Your account</title></head><body>Welcome back</body></html>`))
		case "/login":
			_, _ = w.Write([]byte(`<html><head><title>Sign in</title></head><body><form method="post"><input type="password" name="p"><button>Sign in</button></form></body></html>`))
		case "/challenge":
			_, _ = w.Write([]byte(`<html><head><title>Just a moment...</title></head><body>Checking if the site connection is secure</body></html>`))
		}
	}))
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	base := fmt.Sprintf("http://jobs.example.test:%d", l.Addr().(*net.TCPAddr).Port)
	site := SignInSite{ID: "mock", Label: "Mock", Domain: "jobs.example.test", SignInURL: base + "/login", AccountURL: base + "/account", Cookies: []string{"sid"}}

	dir := t.TempDir()
	o := testOptions(t, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	inst, err := Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { inst.Stop(context.Background()) })

	read := func(s SignInSite) PageReading {
		t.Helper()
		r, err := inst.readSignInPage(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if got := ClassifySignIn(site, read(site)); got != StateSignedOut {
		t.Fatalf("no cookie: %s", got)
	}
	exp := float64(time.Now().Add(time.Hour).Unix())
	if err := inst.SetCookie(ctx, Cookie{Name: "sid", Value: "v", Domain: "jobs.example.test", Path: "/", Expires: exp, SameSite: "Lax"}); err != nil {
		t.Fatal(err)
	}
	r := read(site)
	r.Cookies = []string{"sid"}
	if got := ClassifySignIn(site, r); got != StateSignedIn {
		t.Fatalf("cookie set: %s (%+v)", got, r)
	}
	ch := site
	ch.AccountURL = base + "/challenge"
	if got := ClassifySignIn(ch, read(ch)); got != StateCaptcha {
		t.Fatalf("challenge: %s", got)
	}
	if n := posts.Load(); n != 0 {
		t.Fatalf("the probe sent %d POST requests", n)
	}
	if tabs := inst.Lane(LaneScan).Tabs(); len(tabs) != 0 {
		t.Fatalf("the probe left %d scan tabs open", len(tabs))
	}
}
