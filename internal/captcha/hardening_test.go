package captcha

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"applysmith/internal/gates"
)

func TestCaptcha_TurnstileOnChallengeOnlyPageRefusedBeforeAnyCall(t *testing.T) {
	r, s := newRig(t, nil)
	ch := Challenge{Kind: gates.ChallengeTurnstileWidget, Point: gates.PointChallengeOnly, SiteKey: "site-key-1", PageURL: "https://jobs.example.com/cdn-cgi/challenge"}
	_, err := s.Solve(ctxT(t), ch)
	if code := refusalCode(t, err); code != gates.CodeTurnstileFullPage {
		t.Fatalf("code = %s", code)
	}
	if r.f.total() != 0 {
		t.Fatalf("no request may be made for a full-page interstitial: %v", r.f.hits)
	}
	if !hasLine(s.Detail(), "full-page Cloudflare interstitial") {
		t.Fatalf("detail = %v", s.Detail())
	}
	// A reCAPTCHA or hCaptcha widget on a challenge-only page is still cleared.
	for _, k := range []string{gates.ChallengeHCaptcha, gates.ChallengeRecaptchaV2} {
		ch.Kind = k
		if _, err := s.Solve(ctxT(t), ch); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
	}
}

// Every createTask request that reaches the service counts against the cap,
// whatever path made it.
func TestCaptcha_CreateTaskRequestsNeverExceedTwoPerRun(t *testing.T) {
	solveAll := func(t *testing.T, s *Session, n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			_, _ = s.Solve(ctxT(t), widget(gates.ChallengeRecaptchaV2))
		}
	}
	for _, c := range []struct {
		name string
		run  func(t *testing.T, r *rig, s *Session)
	}{
		{"one 5xx retry uses the second slot", func(t *testing.T, r *rig, s *Session) {
			r.f.createFails = 1
			solveAll(t, s, 4)
		}},
		{"a service that always answers 5xx", func(t *testing.T, r *rig, s *Session) {
			r.f.createFails = 1000
			solveAll(t, s, 5)
		}},
		{"a refused token after a retried creation", func(t *testing.T, r *rig, s *Session) {
			r.f.createFails = 1
			sol, err := s.Solve(ctxT(t), widget(gates.ChallengeHCaptcha))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.TokenRefused(ctxT(t), sol); refusalCode(t, err) != gates.CodeSolveCap {
				t.Fatalf("err = %v", err)
			}
		}},
		{"refused tokens asked for again and again", func(t *testing.T, r *rig, s *Session) {
			sol, err := s.Solve(ctxT(t), widget(gates.ChallengeHCaptcha))
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 5; i++ {
				_, _ = s.TokenRefused(ctxT(t), sol)
			}
		}},
		{"ten goroutines with a 5xx in the way", func(t *testing.T, r *rig, s *Session) {
			r.f.createFails = 1
			var wg sync.WaitGroup
			for i := 0; i < 10; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					sol, err := s.Solve(ctxT(t), widget(gates.ChallengeHCaptcha))
					if err == nil {
						_, _ = s.TokenRefused(ctxT(t), sol)
					}
				}()
			}
			wg.Wait()
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, s := newRig(t, nil)
			c.run(t, r, s)
			if n := r.f.count("/createTask"); n > gates.CaptchaMaxSolves {
				t.Fatalf("%d createTask requests reached the service, cap is %d", n, gates.CaptchaMaxSolves)
			}
		})
	}
}

func TestCaptcha_RedirectNeverCarriesTheKey(t *testing.T) {
	for _, name := range []string{"default client", "caller client that follows redirects"} {
		t.Run(name, func(t *testing.T) {
			var otherHits atomic.Int32
			var leaked atomic.Bool
			other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				otherHits.Add(1)
				b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
				if strings.Contains(string(b), keyForTests()) {
					leaked.Store(true)
				}
				_, _ = io.WriteString(w, `{"errorId":0,"balance":5}`)
			}))
			defer other.Close()
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
			}))
			defer first.Close()
			cfg := Config{Run: gates.RunApplySubmit, Switch: gates.SolverOn, Key: keyForTests,
				Skip: func() gates.SkipStatus { return gates.SkipNotListed }, BaseURL: first.URL}
			if name != "default client" {
				cfg.HTTP = &http.Client{Timeout: 5 * time.Second} // follows redirects unless wrapped
			}
			_, err := New(cfg).Solve(ctxT(t), widget(gates.ChallengeRecaptchaV2))
			if err == nil {
				t.Fatal("a redirected reply must not be accepted")
			}
			if otherHits.Load() != 0 || leaked.Load() {
				t.Fatalf("the redirect was followed: hits=%d leaked=%v", otherHits.Load(), leaked.Load())
			}
			if strings.Contains(err.Error(), keyForTests()) {
				t.Fatal("the key must not be in the error")
			}
		})
	}
}

func TestCaptcha_BalanceFloorHasNoTolerance(t *testing.T) {
	for _, c := range []struct {
		balance string
		solves  bool
	}{
		{"0", false}, {"0.49", false}, {"0.499999", false}, {"0.4999999", false}, {"0.4999999999", false}, {"0.49999999995", false},
		{"0.50", true}, {"0.5000000001", true}, {"0.51", true},
	} {
		t.Run(c.balance, func(t *testing.T) {
			r, s := newRig(t, nil)
			r.f.balance = c.balance
			_, err := s.Solve(ctxT(t), widget(gates.ChallengeRecaptchaV2))
			if c.solves {
				if err != nil || r.f.count("/createTask") != 1 {
					t.Fatalf("err=%v hits=%v", err, r.f.hits)
				}
				return
			}
			if code := refusalCode(t, err); code != gates.CodeBalanceLow {
				t.Fatalf("code = %s", code)
			}
			if r.f.count("/createTask") != 0 {
				t.Fatal("no task may be made under the floor")
			}
			if !hasLine(s.Detail(), "captcha: not attempted — the balance is under the USD 0.50 floor") {
				t.Fatalf("detail = %v", s.Detail())
			}
		})
	}
}

func TestCaptcha_HugeBalanceRefusedWithItsOwnReason(t *testing.T) {
	for _, b := range []string{"1000000.5", "1e7", "1e300"} {
		t.Run(b, func(t *testing.T) {
			r, s := newRig(t, nil)
			r.f.balance = b
			_, err := s.Solve(ctxT(t), widget(gates.ChallengeRecaptchaV2))
			if code := refusalCode(t, err); code != gates.CodeBalanceImplausible {
				t.Fatalf("code = %s", code)
			}
			if r.f.count("/createTask") != 0 {
				t.Fatal("no task may be made on an implausible balance")
			}
		})
	}
	// A figure the number type cannot hold fails closed as unreadable.
	r, s := newRig(t, nil)
	r.f.balance = "1e400"
	_, err := s.Solve(ctxT(t), widget(gates.ChallengeRecaptchaV2))
	if code := refusalCode(t, err); code != gates.CodeBalanceUnknown || r.f.count("/createTask") != 0 {
		t.Fatalf("code = %s hits=%v", code, r.f.hits)
	}
}

// stalledRig serves the fake but holds a path's reply until released or the
// request is cancelled. reached receives once a request is held there.
func stalledRig(t *testing.T, path string) (r *rig, s *Session, free func(), reached <-chan struct{}) {
	t.Helper()
	release := make(chan struct{})
	held := make(chan struct{}, 1)
	var once sync.Once
	free = func() { once.Do(func() { close(release) }) }
	t.Cleanup(free)
	r, _ = newRig(t, nil)
	stall := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == path {
			select {
			case held <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-req.Context().Done():
				return
			}
		}
		r.f.handle(w, req)
	}))
	t.Cleanup(stall.Close)
	s = New(Config{Run: gates.RunApplySubmit, Switch: gates.SolverOn, Key: keyForTests,
		Skip: func() gates.SkipStatus { return gates.SkipNotListed }, BaseURL: stall.URL, HTTP: stall.Client(),
		Now: r.clock.Now, Sleep: r.clock.Sleep})
	return r, s, free, held
}

// stalled waits until a request is held by a stalledRig.
func stalled(t *testing.T, reached <-chan struct{}) {
	t.Helper()
	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		t.Fatal("no request reached the stalled path")
	}
}

// within runs f on another goroutine, so f reports with t.Errorf, never t.Fatal.
func within(t *testing.T, d time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return within %v", what, d)
	}
}

func TestCaptcha_DetailAndOtherCallersDoNotWaitBehindASolve(t *testing.T) {
	_, s, release, reached := stalledRig(t, "/getBalance")
	first := make(chan error, 1)
	go func() {
		_, err := s.Solve(ctxT(t), widget(gates.ChallengeRecaptchaV2))
		first <- err
	}()
	stalled(t, reached) // the first solve is now stalled inside the balance read
	within(t, 2*time.Second, "Detail", func() { _ = s.Detail() })
	within(t, 2*time.Second, "a refused solve", func() {
		_, err := s.Solve(ctxT(t), Challenge{Kind: gates.ChallengeArkose, Point: gates.PointBeforePress, SiteKey: "k", PageURL: "https://jobs.example.com/a"})
		var r *Refusal
		if !errors.As(err, &r) || r.Decision.Code != gates.CodeUnsupportedChallenge {
			t.Errorf("err = %v", err)
		}
	})
	// A waiting caller honours its own deadline.
	within(t, 2*time.Second, "a waiting caller's own deadline", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		if _, err := s.Solve(ctx, widget(gates.ChallengeRecaptchaV2)); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v", err)
		}
	})
	release()
	select {
	case err := <-first:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first solve never finished")
	}
}

func TestCaptcha_DetailAnswersWhilePollingStalls(t *testing.T) {
	_, s, release, reached := stalledRig(t, "/getTaskResult")
	done := make(chan struct{})
	go func() {
		_, _ = s.Solve(ctxT(t), widget(gates.ChallengeRecaptchaV2))
		close(done)
	}()
	stalled(t, reached)
	within(t, 2*time.Second, "Detail during polling", func() { _ = s.Detail() })
	release()
	<-done
}

func TestCaptcha_SolveCapHoldsUnderTenConcurrentCallers(t *testing.T) {
	r, s := newRig(t, nil)
	var wg sync.WaitGroup
	var ok atomic.Int32
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Solve(ctxT(t), widget(gates.ChallengeHCaptcha)); err == nil {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != gates.CaptchaMaxSolves || r.f.count("/createTask") != gates.CaptchaMaxSolves || r.f.count("/getBalance") != 1 {
		t.Fatalf("ok=%d hits=%v", ok.Load(), r.f.hits)
	}
}
