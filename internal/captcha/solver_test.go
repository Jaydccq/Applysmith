package captcha

import (
	"errors"
	"strings"
	"testing"

	"applysmith/internal/gates"
)

func refusalCode(t *testing.T, err error) string {
	t.Helper()
	var r *Refusal
	if !errors.As(err, &r) {
		t.Fatalf("err = %v, want a *Refusal", err)
	}
	return r.Decision.Code
}

func hasLine(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func TestCaptcha_SolveReturnsTokenFromService(t *testing.T) {
	r, s := newRig(t, nil)
	sol, err := s.Solve(ctxT(t), widget(gates.ChallengeHCaptcha))
	if err != nil {
		t.Fatal(err)
	}
	if sol.Token() != "token-one" || sol.Kind() != gates.ChallengeHCaptcha {
		t.Fatalf("solution = %q kind %q", sol.Token(), sol.Kind())
	}
	if r.f.count("/getBalance") != 1 || r.f.count("/createTask") != 1 {
		t.Fatalf("hits = %v", r.f.hits)
	}
	if !hasLine(s.Detail(), "captcha: hcaptcha solve 1 of 2 at before_press") || !hasLine(s.Detail(), "token received") {
		t.Fatalf("detail = %v", s.Detail())
	}
}

func TestCaptcha_RequestShapePerKind(t *testing.T) {
	for _, c := range []struct{ kind, task, token string }{
		{gates.ChallengeRecaptchaV2, "RecaptchaV2TaskProxyless", "gRecaptchaResponse"},
		{gates.ChallengeRecaptchaV2Enterprise, "RecaptchaV2EnterpriseTaskProxyless", "gRecaptchaResponse"},
		{gates.ChallengeHCaptcha, "HCaptchaTaskProxyless", "gRecaptchaResponse"},
		{gates.ChallengeTurnstileWidget, "TurnstileTaskProxyless", "token"},
	} {
		t.Run(c.kind, func(t *testing.T) {
			r, s := newRig(t, nil)
			r.f.setRaw("/getTaskResult", `{"errorId":0,"status":"ready","solution":{"`+c.token+`":"from-`+c.token+`"}}`)
			sol, err := s.Solve(ctxT(t), widget(c.kind))
			if err != nil || sol.Token() != "from-"+c.token {
				t.Fatalf("sol=%q err=%v", sol.Token(), err)
			}
			var task map[string]any
			for _, b := range r.f.bodies {
				if v, ok := b["task"].(map[string]any); ok {
					task = v
				}
			}
			if task["type"] != c.task || task["websiteKey"] != "site-key-1" || task["websiteURL"] != "https://jobs.example.com/apply/1" {
				t.Fatalf("task = %v", task)
			}
			if r.f.bodies[0]["clientKey"] != keyForTests() {
				t.Fatal("the key must reach the service")
			}
		})
	}
}

func TestCaptcha_ThirdSolveRefusedAndBalanceReadOnce(t *testing.T) {
	r, s := newRig(t, nil)
	for i := 0; i < 2; i++ {
		if _, err := s.Solve(ctxT(t), widget(gates.ChallengeRecaptchaV2)); err != nil {
			t.Fatalf("solve %d: %v", i+1, err)
		}
	}
	before := r.f.total()
	_, err := s.Solve(ctxT(t), widget(gates.ChallengeRecaptchaV2))
	if code := refusalCode(t, err); code != gates.CodeSolveCap {
		t.Fatalf("code = %s", code)
	}
	if r.f.total() != before {
		t.Fatal("a refused third solve must make no request")
	}
	if r.f.count("/getBalance") != 1 || r.f.count("/createTask") != 2 {
		t.Fatalf("hits = %v", r.f.hits)
	}
}

// A single-URL run is an apply_submit run like a batch one: the session has no
// other run scope, so the skip list refuses it the same way.
func TestCaptcha_SkipListCompanyRefusedEvenInSingleRun(t *testing.T) {
	for name, c := range map[string]struct {
		skip func() gates.SkipStatus
		want string
	}{
		"listed":     {func() gates.SkipStatus { return gates.SkipListed }, gates.CodeSkipListCompany},
		"unreadable": {func() gates.SkipStatus { return gates.SkipUnreadable }, gates.CodeSkipListUnreadable},
		"not wired":  {nil, gates.CodeSkipListUnreadable},
	} {
		t.Run(name, func(t *testing.T) {
			r, s := newRig(t, func(cfg *Config) { cfg.Skip = c.skip })
			_, err := s.Solve(ctxT(t), widget(gates.ChallengeRecaptchaV2))
			if code := refusalCode(t, err); code != c.want {
				t.Fatalf("code = %s, want %s", code, c.want)
			}
			if r.f.total() != 0 {
				t.Fatal("the skip list must refuse before any request")
			}
		})
	}
}

func TestCaptcha_SkipListReadPerCall(t *testing.T) {
	listed := false
	r, s := newRig(t, func(c *Config) {
		c.Skip = func() gates.SkipStatus {
			if listed {
				return gates.SkipListed
			}
			return gates.SkipNotListed
		}
	})
	if _, err := s.Solve(ctxT(t), widget(gates.ChallengeRecaptchaV2)); err != nil {
		t.Fatal(err)
	}
	listed = true
	_, err := s.Solve(ctxT(t), widget(gates.ChallengeRecaptchaV2))
	if code := refusalCode(t, err); code != gates.CodeSkipListCompany {
		t.Fatalf("code = %s", code)
	}
	if r.f.count("/createTask") != 1 {
		t.Fatalf("hits = %v", r.f.hits)
	}
}

func TestCaptcha_OnlySubmittingRunsGetASolver(t *testing.T) {
	for _, run := range []gates.RunKind{gates.RunDryRun, "scan", ""} {
		t.Run(string(run), func(t *testing.T) {
			r, s := newRig(t, func(c *Config) { c.Run = run })
			_, err := s.Solve(ctxT(t), widget(gates.ChallengeRecaptchaV2))
			if code := refusalCode(t, err); code != gates.CodeRunNotSubmitting {
				t.Fatalf("code = %s", code)
			}
			if r.f.total() != 0 {
				t.Fatal("a non-submitting run must make no request")
			}
		})
	}
}

func TestCaptcha_UnsupportedChallengesRefusedBeforeAnyCall(t *testing.T) {
	for _, k := range []string{gates.ChallengeRecaptchaV3, gates.ChallengeInterstitial, gates.ChallengeImagePuzzle, gates.ChallengeArkose, gates.ChallengeDataDome, "something_else"} {
		t.Run(k, func(t *testing.T) {
			r, s := newRig(t, nil)
			_, err := s.Solve(ctxT(t), widget(k))
			if code := refusalCode(t, err); code != gates.CodeUnsupportedChallenge {
				t.Fatalf("code = %s", code)
			}
			if r.f.total() != 0 {
				t.Fatal("an unsupported challenge must make no request")
			}
			if !hasLine(s.Detail(), "captcha: not attempted") {
				t.Fatalf("detail = %v", s.Detail())
			}
		})
	}
}

func TestCaptcha_MissingKeyOrSwitchOffMeansOff(t *testing.T) {
	for name, c := range map[string]struct {
		mut  func(*Config)
		want string
	}{
		"no key":         {func(c *Config) { c.Key = func() string { return "" } }, gates.CodeKeyMissing},
		"blank key":      {func(c *Config) { c.Key = func() string { return "  \n" } }, gates.CodeKeyMissing},
		"key not wired":  {func(c *Config) { c.Key = nil }, gates.CodeKeyMissing},
		"switch off":     {func(c *Config) { c.Switch = gates.SolverOff }, gates.CodeSolverOff},
		"switch unknown": {func(c *Config) { c.Switch = "" }, gates.CodeSolverOff},
	} {
		t.Run(name, func(t *testing.T) {
			r, s := newRig(t, c.mut)
			_, err := s.Solve(ctxT(t), widget(gates.ChallengeRecaptchaV2))
			if code := refusalCode(t, err); code != c.want {
				t.Fatalf("code = %s, want %s", code, c.want)
			}
			if r.f.total() != 0 {
				t.Fatal("an off solver must make no request")
			}
		})
	}
}

func TestCaptcha_IncompleteChallengeMakesNoRequest(t *testing.T) {
	for name, mut := range map[string]func(*Challenge){
		"no site key": func(c *Challenge) { c.SiteKey = "" },
		"no page":     func(c *Challenge) { c.PageURL = "" },
		"bad scheme":  func(c *Challenge) { c.PageURL = "javascript:alert(1)" },
	} {
		t.Run(name, func(t *testing.T) {
			r, s := newRig(t, nil)
			c := widget(gates.ChallengeRecaptchaV2)
			mut(&c)
			if _, err := s.Solve(ctxT(t), c); !errors.Is(err, ErrIncomplete) {
				t.Fatalf("err = %v", err)
			}
			if r.f.total() != 0 {
				t.Fatal("no request expected")
			}
		})
	}
}

func TestCaptcha_RefusedTokenRetriedOnceThenRecorded(t *testing.T) {
	r, s := newRig(t, nil)
	first, err := s.Solve(ctxT(t), widget(gates.ChallengeHCaptcha))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.TokenRefused(ctxT(t), first)
	if err != nil {
		t.Fatal(err)
	}
	if second.Token() == first.Token() {
		t.Fatal("the retry must ask for a fresh token")
	}
	if _, err := s.TokenRefused(ctxT(t), second); !errors.Is(err, ErrNotAccepted) {
		t.Fatalf("err = %v", err)
	}
	if r.f.count("/createTask") != 2 {
		t.Fatalf("hits = %v", r.f.hits)
	}
	n := 0
	for _, l := range s.Detail() {
		if l == "captcha: hcaptcha token was not accepted by the page" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("detail = %v", s.Detail())
	}
}

func TestCaptcha_RetryStaysInsideTheCap(t *testing.T) {
	r, s := newRig(t, nil)
	if _, err := s.Solve(ctxT(t), widget(gates.ChallengeRecaptchaV2)); err != nil {
		t.Fatal(err)
	}
	last, err := s.Solve(ctxT(t), widget(gates.ChallengeRecaptchaV2))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.TokenRefused(ctxT(t), last)
	if code := refusalCode(t, err); code != gates.CodeSolveCap {
		t.Fatalf("code = %s", code)
	}
	if r.f.count("/createTask") != 2 {
		t.Fatalf("hits = %v", r.f.hits)
	}
	if _, err := s.TokenRefused(ctxT(t), Solution{}); !errors.Is(err, ErrNotAccepted) {
		t.Fatalf("zero solution: %v", err)
	}
}

func TestCaptcha_PollingStopsAtTheDeadline(t *testing.T) {
	r, s := newRig(t, nil)
	r.f.forever = true
	_, err := s.Solve(ctxT(t), widget(gates.ChallengeTurnstileWidget))
	if !errors.Is(err, ErrTimedOut) {
		t.Fatalf("err = %v", err)
	}
	// First wait at 5 s, then a poll every 5 s from 5 s to 115 s.
	if got := r.f.count("/getTaskResult"); got != 23 {
		t.Fatalf("polls = %d", got)
	}
	if got := r.clock.slept().Seconds(); got != 120 {
		t.Fatalf("slept %v s", got)
	}
	if !hasLine(s.Detail(), "no answer from the service after 120 seconds") {
		t.Fatalf("detail = %v", s.Detail())
	}
}

func TestCaptcha_CreateRetriedOnceAfterServerError(t *testing.T) {
	r, s := newRig(t, nil)
	r.f.createFails = 1
	if _, err := s.Solve(ctxT(t), widget(gates.ChallengeRecaptchaV2)); err != nil {
		t.Fatal(err)
	}
	if r.f.count("/createTask") != 2 || r.clock.sleeps[0].Seconds() != 3 {
		t.Fatalf("hits=%v sleeps=%v", r.f.hits, r.clock.sleeps)
	}
	// The retry that succeeded used the second slot.
	if _, err := s.Solve(ctxT(t), widget(gates.ChallengeRecaptchaV2)); refusalCode(t, err) != gates.CodeSolveCap || r.f.count("/createTask") != 2 {
		t.Fatalf("err=%v hits=%v", err, r.f.hits)
	}

	r2, s2 := newRig(t, nil)
	r2.f.createFails = 2
	_, err := s2.Solve(ctxT(t), widget(gates.ChallengeRecaptchaV2))
	var se *ServiceError
	if !errors.As(err, &se) || se.Status != 500 || r2.f.count("/createTask") != 2 {
		t.Fatalf("err=%v hits=%v", err, r2.f.hits)
	}
	if !hasLine(s2.Detail(), "captcha: service error ERROR_INTERNAL (HTTP 500)") {
		t.Fatalf("detail = %v", s2.Detail())
	}
	// Each creation request used a slot, so both are gone.
	if _, err := s2.Solve(ctxT(t), widget(gates.ChallengeRecaptchaV2)); refusalCode(t, err) != gates.CodeSolveCap {
		t.Fatalf("err = %v", err)
	}
	if r2.f.count("/createTask") != 2 {
		t.Fatalf("hits = %v", r2.f.hits)
	}
}

func TestCaptcha_BalanceReadFailureIsRememberedForTheRun(t *testing.T) {
	r, s := newRig(t, nil)
	r.f.setRaw("/getBalance", `{"errorId":1,"errorCode":"ERROR_KEY_DOES_NOT_EXIST"}`)
	for i := 0; i < 2; i++ {
		_, err := s.Solve(ctxT(t), widget(gates.ChallengeRecaptchaV2))
		if code := refusalCode(t, err); code != gates.CodeBalanceUnknown {
			t.Fatalf("code = %s", code)
		}
	}
	if r.f.count("/getBalance") != 1 || r.f.count("/createTask") != 0 {
		t.Fatalf("hits = %v", r.f.hits)
	}
}

func TestCaptcha_MalformedResponsesFailClosed(t *testing.T) {
	const garbage = `{"errorId":0,"taskId":"not-a-number","status":"mystery","balance":"lots"}`
	for _, c := range []struct {
		name, path, body string
		refusal          bool // a balance problem refuses; the rest fail the solve
	}{
		{"balance not json", "/getBalance", "<html>maintenance</html>", true},
		{"balance missing", "/getBalance", `{"errorId":0}`, true},
		{"balance a string", "/getBalance", `{"errorId":0,"balance":"5.00"}`, true},
		{"balance negative", "/getBalance", `{"errorId":0,"balance":-1}`, true},
		{"balance without errorId", "/getBalance", `{"balance":5}`, true},
		{"task id missing", "/createTask", `{"errorId":0}`, false},
		{"task id zero", "/createTask", `{"errorId":0,"taskId":0}`, false},
		{"task not json", "/createTask", `garbage`, false},
		{"create garbage", "/createTask", garbage, false},
		{"status unknown", "/getTaskResult", `{"errorId":0,"status":"mystery"}`, false},
		{"ready without solution", "/getTaskResult", `{"errorId":0,"status":"ready"}`, false},
		{"ready with blank token", "/getTaskResult", `{"errorId":0,"status":"ready","solution":{"gRecaptchaResponse":"  "}}`, false},
		{"ready with spaced token", "/getTaskResult", `{"errorId":0,"status":"ready","solution":{"gRecaptchaResponse":"a b"}}`, false},
		{"solution wrong type", "/getTaskResult", `{"errorId":0,"status":"ready","solution":{"gRecaptchaResponse":42}}`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, s := newRig(t, nil)
			r.f.setRaw(c.path, c.body)
			sol, err := s.Solve(ctxT(t), widget(gates.ChallengeRecaptchaV2))
			if err == nil || sol.Token() != "" {
				t.Fatalf("a malformed reply must not yield a token: %q %v", sol.Token(), err)
			}
			if c.refusal {
				if code := refusalCode(t, err); code != gates.CodeBalanceUnknown {
					t.Fatalf("code = %s", code)
				}
				return
			}
			var se *ServiceError
			if !errors.As(err, &se) || se.Code != codeMalformed {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestCaptcha_UnreachableServiceFailsWithoutLeakingTheAddress(t *testing.T) {
	r, s := newRig(t, nil)
	r.f.srv.Close()
	_, err := s.Solve(ctxT(t), widget(gates.ChallengeRecaptchaV2))
	if err == nil {
		t.Fatal("expected a failure")
	}
	if strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(strings.Join(s.Detail(), "\n"), "127.0.0.1") {
		t.Fatalf("the transport error text leaked: %v", err)
	}
}

func TestCaptcha_EveryDecisionIsAudited(t *testing.T) {
	r, s := newRig(t, nil)
	if _, err := s.Solve(ctxT(t), widget(gates.ChallengeRecaptchaV2)); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Solve(ctxT(t), widget(gates.ChallengeArkose))
	if len(r.audited) != 2 || r.audited[0].Verdict != gates.Allow || r.audited[1].Code != gates.CodeUnsupportedChallenge {
		t.Fatalf("audited = %+v", r.audited)
	}
	for _, d := range r.audited {
		if !d.Valid() {
			t.Fatalf("invalid decision %+v", d)
		}
	}
}
