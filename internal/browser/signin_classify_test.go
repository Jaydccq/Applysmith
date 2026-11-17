package browser

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var probeSite = SignInSite{ID: "x", Label: "X", Domain: "jobs.example.test", SignInURL: "https://www.jobs.example.test/login", AccountURL: "https://www.jobs.example.test/account", Cookies: []string{"sid"}}

func TestSignInClassifier_RulesInOrder(t *testing.T) {
	own := "https://www.jobs.example.test/account"
	cases := []struct {
		name string
		r    PageReading
		want SignInState
	}{
		{"signed out by redirect to the sign-in address", PageReading{Status: 200, URL: "https://www.jobs.example.test/login?next=%2Faccount", Title: "Sign in", Text: "Welcome back"}, StateSignedOut},
		{"signed out by a sign-in form on the account address", PageReading{Status: 200, URL: own, Title: "Account", SignInForm: true}, StateSignedOut},
		{"signed in at its own address with a proof cookie", PageReading{Status: 200, URL: own, Title: "Account", Text: "Your profile", Cookies: []string{"sid"}}, StateSignedIn},
		{"a page below the account address is still its own", PageReading{Status: 200, URL: own + "/settings", Title: "Settings", Cookies: []string{"sid"}}, StateSignedIn},
		{"no proof cookie is not signed in", PageReading{Status: 200, URL: own, Title: "Account", Cookies: []string{"other"}}, StateUnknown},
		{"a redirect to some other page is unknown", PageReading{Status: 200, URL: "https://www.jobs.example.test/welcome", Title: "Hi", Cookies: []string{"sid"}}, StateUnknown},
		{"challenge wording is a captcha", PageReading{Status: 200, URL: own, Title: "Check", Text: "Are you human? Press and hold the button"}, StateCaptcha},
		{"a challenge frame is a captcha", PageReading{Status: 200, URL: own, Title: "Account", Frames: []string{"https://geo.captcha-delivery.com/captcha/?x=1"}, Cookies: []string{"sid"}}, StateCaptcha},
		{"HTTP 403 is a captcha", PageReading{Status: 403, URL: own, Title: "Forbidden"}, StateCaptcha},
		{"Cloudflare interstitial is a captcha and never a checkpoint", PageReading{Status: 200, URL: "https://www.jobs.example.test/checkpoint/x", Title: "Just a moment...", Text: "Verify your identity"}, StateCaptcha},
		{"HTTP 429 is rate limited", PageReading{Status: 429, URL: own, Title: "Error", Text: "Are you human"}, StateRateLimited},
		{"rate limit wording is rate limited", PageReading{Status: 200, URL: own, Title: "Error", Text: "Too many requests, try later"}, StateRateLimited},
		{"a checkpoint address is a checkpoint", PageReading{Status: 200, URL: "https://www.jobs.example.test/checkpoint/challenge", Title: "Welcome"}, StateCheckpoint},
		{"a checkpoint title is a checkpoint", PageReading{Status: 200, URL: own, Title: "Security Verification | X"}, StateCheckpoint},
		{"the identity phrase in the body is a checkpoint", PageReading{Status: 200, URL: own, Title: "Account", Text: "Please verify your identity to continue"}, StateCheckpoint},
		{"job description words are not a checkpoint", PageReading{Status: 200, URL: own, Title: "Account", Text: "You will train model checkpoints and build two-factor authentication flows", Cookies: []string{"sid"}}, StateSignedIn},
		{"a closed posting is unknown before the sign-in rule", PageReading{Status: 200, URL: "https://www.jobs.example.test/jobs/1", Title: "Job", Text: "This job is no longer available", SignInForm: true}, StateUnknown},
		{"HTTP 404 is unknown before the sign-in rule", PageReading{Status: 404, URL: own, Title: "Not found", SignInForm: true}, StateUnknown},
		{"a DataDome delivery address is a captcha", PageReading{Status: 200, URL: "https://geo.captcha-delivery.com/interstitial/?x=1", Title: "Account"}, StateCaptcha},
		{"a load that failed is unknown", PageReading{LoadFailed: true}, StateUnknown},
		{"wall wording far down a signed-in page is not a wall", PageReading{Status: 200, URL: own, Title: "Feed", Text: strings.Repeat("A post. ", 60) + "Slow down! Access denied? Verify your identity.", Cookies: []string{"sid"}}, StateSignedIn},
		{"a challenge widget beside a sign-in form is signed out", PageReading{Status: 200, URL: own, Title: "Account", SignInForm: true, Frames: []string{"https://www.google.com/recaptcha/api2/anchor?k=1"}}, StateSignedOut},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifySignIn(probeSite, tc.r); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestSignInClassifier_SiteWithoutProofCookiesNeedsNoneToBeSignedIn(t *testing.T) {
	s := probeSite
	s.Cookies = nil
	r := PageReading{Status: 200, URL: "https://www.jobs.example.test/account", Title: "Account"}
	if got := ClassifySignIn(s, r); got != StateSignedIn {
		t.Fatal(got)
	}
}

func TestSignInClassifier_NextActionProjection(t *testing.T) {
	want := map[SignInState]string{
		StateSignedIn: "proceed", StateUnknown: "proceed", StateRateLimited: "retry-later",
		StateSignedOut: "needs-user", StateCheckpoint: "needs-user", StateCaptcha: "blocked-by-safety",
	}
	for s, a := range want {
		if got := NextAction(s); got != a {
			t.Errorf("%s: %s, want %s", s, got, a)
		}
	}
}

type memStates struct{ m map[string]SignInRecord }

func (s *memStates) Last(context.Context) (map[string]SignInRecord, error) { return s.m, nil }
func (s *memStates) Save(_ context.Context, d string, st SignInState, at time.Time) error {
	s.m[d] = SignInRecord{State: st, CheckedAt: at}
	return nil
}

func probeSessions(t *testing.T, states *memStates, now time.Time, probe func(SignInSite) (PageReading, error)) (*Sessions, *int) {
	t.Helper()
	calls := 0
	jar := newJar(Cookie{Name: "li_at", Value: "v", Domain: ".linkedin.com", Path: "/"})
	s := NewSessions(SessionsConfig{
		Dir: t.TempDir(), Running: func() CookieJar { return jar },
		Probe: func(_ context.Context, site SignInSite) (PageReading, error) {
			calls++
			return probe(site)
		},
		States: states,
	})
	s.now = func() time.Time { return now }
	return s, &calls
}

func TestSignInProbe_StoresTheStateAndServesItForFiveMinutes(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	states := &memStates{m: map[string]SignInRecord{}}
	s, calls := probeSessions(t, states, now, func(site SignInSite) (PageReading, error) {
		if site.ID == "linkedin" {
			return PageReading{Status: 200, URL: site.AccountURL, Title: "Feed"}, nil
		}
		return PageReading{Status: 200, URL: site.SignInURL, Title: "Sign in"}, nil
	})
	got, err := s.CheckSignIn(context.Background())
	if err != nil || len(got) != len(SignInSites) || *calls != len(SignInSites) {
		t.Fatalf("%v %d %d", err, len(got), *calls)
	}
	if states.m["linkedin.com"].State != StateSignedIn || states.m["indeed.com"].State != StateSignedOut || !states.m["indeed.com"].CheckedAt.Equal(now) {
		t.Fatalf("%+v", states.m)
	}
	sites, _ := s.Sites(context.Background())
	by := map[string]string{}
	for _, x := range sites {
		by[x.Domain] = x.SignIn
	}
	if by["linkedin.com"] != "signed_in" || by["indeed.com"] != "signed_out" {
		t.Fatalf("the site list reports the stored states: %v", by)
	}
	s.now = func() time.Time { return now.Add(4 * time.Minute) }
	again, err := s.CheckSignIn(context.Background(), "linkedin")
	if err != nil || *calls != len(SignInSites) || len(again) != 1 || !again[0].Cached {
		t.Fatalf("a result under five minutes old is served as it is: %v %d %+v", err, *calls, again)
	}
	s.now = func() time.Time { return now.Add(6 * time.Minute) }
	if _, err := s.CheckSignIn(context.Background(), "linkedin"); err != nil || *calls != len(SignInSites)+1 {
		t.Fatalf("an older one is probed again: %v %d", err, *calls)
	}
}

// The user signs in, or clears a challenge, and checks again at once: a
// signed-out or challenge result is read again, not served for five minutes.
func TestSignInProbe_ReadsAgainAStateTheUserFixesWithinFiveMinutes(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	for _, st := range []SignInState{StateSignedOut, StateCaptcha} {
		states := &memStates{m: map[string]SignInRecord{"indeed.com": {State: st, CheckedAt: now.Add(-time.Minute)}}}
		s, calls := probeSessions(t, states, now, func(site SignInSite) (PageReading, error) {
			return PageReading{Status: 200, URL: site.AccountURL, Title: "Account"}, nil
		})
		got, err := s.CheckSignIn(context.Background(), "indeed")
		if err != nil || *calls != 1 || len(got) != 1 || got[0].Cached || !states.m["indeed.com"].CheckedAt.Equal(now) {
			t.Fatalf("%s a minute ago was not read again: %v calls=%d %+v", st, err, *calls, got)
		}
	}
}

func TestSignInProbe_AFailedProbeKeepsTheLastStateAndReportsWhy(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	states := &memStates{m: map[string]SignInRecord{"indeed.com": {State: StateSignedIn, CheckedAt: now.Add(-time.Hour)}}}
	s, _ := probeSessions(t, states, now, func(SignInSite) (PageReading, error) { return PageReading{}, errors.New("no browser") })
	got, err := s.CheckSignIn(context.Background(), "indeed")
	if err != nil || len(got) != 1 || got[0].Err == nil || got[0].State != StateSignedIn {
		t.Fatalf("%v %+v", err, got)
	}
	if states.m["indeed.com"].State != StateSignedIn || !states.m["indeed.com"].CheckedAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("nothing was stored: %+v", states.m)
	}
}
