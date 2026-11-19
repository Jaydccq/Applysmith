package browser

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A list's sign-in check reads the cookie that proves the site's sign-in:
// present and unexpired is signed in; absent, expired or empty is signed out.
// A site that names no such cookie, and a browser that cannot open, say
// nothing, so no row is left out on a guess.
func TestSessions_SignedOutReadsTheProofCookie(t *testing.T) {
	later, earlier := float64(time.Now().Add(time.Hour).Unix()), float64(time.Now().Add(-time.Hour).Unix())
	for name, c := range map[string]struct {
		cookies []Cookie
		out     bool
	}{
		"present":        {[]Cookie{{Name: "li_at", Value: "v", Domain: ".www.linkedin.com", Path: "/", Expires: later}}, false},
		"session cookie": {[]Cookie{{Name: "li_at", Value: "v", Domain: ".linkedin.com", Path: "/"}}, false},
		"absent":         {[]Cookie{{Name: "JSESSIONID", Value: "v", Domain: ".linkedin.com", Path: "/"}}, true},
		"expired":        {[]Cookie{{Name: "li_at", Value: "v", Domain: ".linkedin.com", Path: "/", Expires: earlier}}, true},
		"empty":          {[]Cookie{{Name: "li_at", Domain: ".linkedin.com", Path: "/"}}, true},
		"another site":   {[]Cookie{{Name: "li_at", Value: "v", Domain: ".example.com", Path: "/"}}, true},
	} {
		jar := newJar(c.cookies...)
		s := NewSessions(SessionsConfig{Dir: t.TempDir(), Open: func(context.Context) (CookieJar, error) { return jar, nil }})
		if site, out := s.SignedOut(t.Context(), "www.linkedin.com"); out != c.out || out && site.Label != "LinkedIn" {
			t.Errorf("%s: signed out %v (%s)", name, out, site.Label)
		}
	}
	s := NewSessions(SessionsConfig{Dir: t.TempDir(), Open: func(context.Context) (CookieJar, error) { return newJar(), nil }})
	if _, out := s.SignedOut(t.Context(), "www.indeed.com"); out {
		t.Error("a site that names no proof cookie")
	}
	down := NewSessions(SessionsConfig{Dir: t.TempDir(), Open: func(context.Context) (CookieJar, error) { return nil, errors.New("no browser") }})
	if _, out := down.SignedOut(t.Context(), "www.linkedin.com"); out {
		t.Error("a browser that cannot open")
	}
}
