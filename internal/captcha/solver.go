// Package captcha asks the user's own 2Captcha account to clear a captcha, and
// only when the safety gate allows it. It hands the token to the caller and
// does nothing with the page: delivering the token is the chain's job.
package captcha

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"applysmith/internal/gates"
)

// Challenge describes one captcha the page step found. PageURL goes to the
// service because it needs the address of the page; it is never logged,
// stored or printed.
type Challenge struct {
	Kind    string // a gates.Challenge* value
	Point   string // a gates.Point* value
	SiteKey string
	PageURL string
	// CombinedAccountAndApplication marks a combined form, which is left to
	// the user.
	CombinedAccountAndApplication bool
}

// String names the challenge without its page address.
func (c Challenge) String() string               { return "captcha challenge " + c.Kind + " at " + c.Point }
func (c Challenge) MarshalText() ([]byte, error) { return []byte(c.String()), nil }
func (c Challenge) GoString() string             { return c.String() }

// Solution carries a token. Only Token returns it; every formatting, logging
// and encoding path prints a placeholder.
type Solution struct {
	kind  string
	token string
	retry bool
	ch    Challenge
}

// Token is the response string to put in the page's response field.
func (s Solution) Token() string { return s.token }

// Kind is the challenge kind that was solved.
func (s Solution) Kind() string { return s.kind }

func (s Solution) String() string               { return "captcha solution for " + s.kind + " (token withheld)" }
func (s Solution) GoString() string             { return s.String() }
func (s Solution) LogValue() slog.Value         { return slog.StringValue(s.String()) }
func (s Solution) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// Solver is what the chain's page steps call. The page step puts a token in
// the widget's fields, presses, and reads the page: a widget that still
// stands means TokenRefused, after the step has blanked the fields.
type Solver interface {
	Solve(ctx context.Context, c Challenge) (Solution, error)
	// TokenRefused records that the page did not accept the token and asks
	// for one more, once per challenge and only inside the run's cap.
	TokenRefused(ctx context.Context, s Solution) (Solution, error)
	// Detail returns the `captcha: ...` lines for the stage detail.
	Detail() []string
	// Creations is how many task creations count against the run's cap,
	// those of the run's earlier legs included.
	Creations() int
}

// Errors a caller can branch on. Each leaves the challenge to the user.
var (
	ErrNotAccepted = errors.New("captcha: the page did not accept the token")
	ErrTimedOut    = errors.New("captcha: the service did not answer within the time limit")
	ErrIncomplete  = errors.New("captcha: the widget's site key or page address was not read")
)

// Refusal is a gate denial. The solver made no request for it, apart from the
// one balance read that precedes the final decision.
type Refusal struct{ Decision gates.Decision }

func (r *Refusal) Error() string { return r.Decision.Reason }

// Config holds the facts of one run. Key and Skip are read at each use: the
// key so a rotated secret takes effect, the skip list because the company is
// known only part-way through a run.
type Config struct {
	// Demo is the workspace's demo marker: every solve is refused.
	Demo bool
	Run  gates.RunKind
	// Spent is the task creations an earlier leg of the same run made (a run
	// the user resumed); they count against the cap.
	Spent  int
	Switch gates.SolverSwitch
	// Key returns the 2Captcha key, or "" when none is configured.
	Key func() string
	// Skip reports the batch skip list's verdict on the run's company; nil
	// reads as unreadable.
	Skip func() gates.SkipStatus
	// Audit receives every decision that refuses or allows a solve, for the
	// gate_decisions table. Concurrent callers may call it concurrently.
	Audit func(gates.Decision)
	Log   *slog.Logger

	// The fields below exist so tests need no network and no waiting.
	BaseURL string
	HTTP    *http.Client
	Now     func() time.Time
	Sleep   func(context.Context, time.Duration) error
}

// Session is the solver for one run.
type Session struct {
	cfg Config
	api *api

	// balSem lets one caller at a time read the balance while a waiting
	// caller can still give up with its own context.
	balSem chan struct{}

	// mu guards the fields below and is never held across I/O, so Detail and
	// a caller's own deadline are never stuck behind another caller's solve.
	mu sync.Mutex
	// creations counts createTask requests sent or about to be sent, retries
	// included: the cap bounds what the service is asked to start, not what
	// succeeds.
	creations int
	balRead   bool
	balOK     bool
	balance   int64
	lines     []string
}

var _ Solver = (*Session)(nil)

// New builds the solver for one run.
func New(cfg Config) *Session {
	if cfg.Key == nil {
		cfg.Key = func() string { return "" }
	}
	if cfg.Skip == nil {
		cfg.Skip = func() gates.SkipStatus { return gates.SkipUnreadable }
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	// A redirect would re-send the key to whatever host it names, so no
	// client, whatever the caller passes, follows one.
	hc := http.Client{}
	if cfg.HTTP != nil {
		hc = *cfg.HTTP
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	cfg.HTTP = &hc
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepCtx
	}
	return &Session{cfg: cfg, creations: max(cfg.Spent, 0), balSem: make(chan struct{}, 1), api: &api{base: strings.TrimRight(cfg.BaseURL, "/"), http: cfg.HTTP, key: cfg.Key}}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Solve asks for a token.
func (s *Session) Solve(ctx context.Context, c Challenge) (Solution, error) {
	return s.solve(ctx, c, false)
}

// TokenRefused records the refusal and, on the first one for a challenge,
// asks again.
func (s *Session) TokenRefused(ctx context.Context, sol Solution) (Solution, error) {
	if sol.kind == "" {
		return Solution{}, ErrNotAccepted
	}
	s.note(fmt.Sprintf("captcha: %s token was not accepted by the page", sol.kind))
	if sol.retry {
		return Solution{}, ErrNotAccepted
	}
	return s.solve(ctx, sol.ch, true)
}

// Creations is how many task creations count against the run's cap.
func (s *Session) Creations() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.creations
}

// Detail returns a copy of the `captcha: ...` lines so far.
func (s *Session) Detail() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lines...)
}

func (s *Session) note(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, line)
	if s.cfg.Log != nil {
		s.cfg.Log.Info("captcha", "detail", line)
	}
}

func (s *Session) refuse(d gates.Decision) error {
	s.note(d.Reason)
	if s.cfg.Audit != nil {
		s.cfg.Audit(d)
	}
	return &Refusal{Decision: d}
}

// facts reads the parts of the gate input that need no counter. The key and
// the skip list are read here, outside the lock, because either may be slow.
func (s *Session) facts(c Challenge) gates.CaptchaInput {
	return gates.CaptchaInput{
		Demo:                          s.cfg.Demo,
		Run:                           s.cfg.Run,
		Switch:                        s.cfg.Switch,
		KeyConfigured:                 strings.TrimSpace(s.cfg.Key()) != "",
		Challenge:                     c.Kind,
		Point:                         c.Point,
		CombinedAccountAndApplication: c.CombinedAccountAndApplication,
		Skip:                          s.cfg.Skip(),
	}
}

// preCheck asks the gate with a balance that passes, so every other condition
// can refuse before any request is made.
func (s *Session) preCheck(c Challenge) gates.Decision {
	in := s.facts(c)
	in.BalanceKnown, in.BalanceMicroUSD = true, gates.CaptchaBalanceFloorMicroUSD
	s.mu.Lock()
	in.SolvesThisRun = s.creations
	s.mu.Unlock()
	return gates.CaptchaSolve(in)
}

// reserve asks the gate with the real balance and, when it allows, takes a
// slot in the same critical section, so concurrent callers cannot share one.
func (s *Session) reserve(c Challenge) gates.Decision {
	in := s.facts(c)
	s.mu.Lock()
	defer s.mu.Unlock()
	in.SolvesThisRun, in.BalanceKnown, in.BalanceMicroUSD = s.creations, s.balOK, s.balance
	d := gates.CaptchaSolve(in)
	if d.Allowed() {
		s.creations++
	}
	return d
}

// reserveRetry takes a slot for a second task-creation attempt, if one is left.
func (s *Session) reserveRetry() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.creations >= gates.CaptchaMaxSolves {
		return false
	}
	s.creations++
	return true
}

// ensureBalance reads the balance once per run. A caller that waits for the
// read honours its own context.
func (s *Session) ensureBalance(ctx context.Context) error {
	if s.balanceRead() {
		return nil
	}
	select {
	case s.balSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.balSem }()
	if s.balanceRead() {
		return nil
	}
	bal, err := s.api.balanceMicroUSD(ctx)
	if isContextErr(err) {
		return err
	}
	s.mu.Lock()
	s.balRead, s.balOK, s.balance = true, err == nil, bal
	s.mu.Unlock()
	if err != nil {
		s.note(err.Error())
	}
	return nil
}

func (s *Session) balanceRead() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.balRead
}

// solve asks the gate before the balance read, so a refusal makes no request,
// and again with the real balance before the task is made. It holds no lock
// while it talks to the service.
func (s *Session) solve(ctx context.Context, c Challenge, retry bool) (Solution, error) {
	if d := s.preCheck(c); !d.Allowed() {
		return Solution{}, s.refuse(d)
	}
	if c.SiteKey == "" || !httpURL(c.PageURL) {
		s.note("captcha: not attempted — the widget's site key or page address was not read")
		return Solution{}, ErrIncomplete
	}
	if err := s.ensureBalance(ctx); err != nil {
		return Solution{}, err
	}
	d := s.reserve(c)
	if !d.Allowed() {
		return Solution{}, s.refuse(d)
	}
	if s.cfg.Audit != nil {
		s.cfg.Audit(d)
	}
	s.note(d.Reason)

	start := s.cfg.Now()
	id, err := s.create(ctx, c)
	if err != nil {
		return Solution{}, s.fail(err)
	}
	token, err := s.poll(ctx, c.Kind, id, start)
	if err != nil {
		return Solution{}, s.fail(err)
	}
	s.note(fmt.Sprintf("captcha: %s token received", c.Kind))
	return Solution{kind: c.Kind, token: token, retry: retry, ch: c}, nil
}

func (s *Session) fail(err error) error {
	switch {
	case errors.Is(err, ErrTimedOut):
		s.note(fmt.Sprintf("captcha: no answer from the service after %d seconds", gates.CaptchaGiveUpAfter/gates.Second))
	case isContextErr(err):
		s.note("captcha: stopped before the service answered")
	default:
		s.note(err.Error())
	}
	return err
}

func (s *Session) create(ctx context.Context, c Challenge) (int64, error) {
	for retries := 0; ; retries++ {
		id, err := s.api.createTask(ctx, c)
		if err == nil {
			return id, nil
		}
		var se *ServiceError
		if !errors.As(err, &se) {
			return 0, err
		}
		again, wait := gates.CaptchaCreateRetry(se.Status, retries)
		if !again {
			return 0, err
		}
		if !s.reserveRetry() {
			s.note("captcha: no solve left in this run for a second task creation attempt")
			return 0, err
		}
		if err := s.cfg.Sleep(ctx, time.Duration(wait)*time.Millisecond); err != nil {
			return 0, err
		}
	}
}

// poll follows the gate's schedule: a first wait, a poll every few seconds and
// a give-up time measured from task creation.
func (s *Session) poll(ctx context.Context, kind string, id int64, start time.Time) (string, error) {
	pctx, cancel := context.WithTimeout(ctx, time.Duration(gates.CaptchaGiveUpAfter)*time.Millisecond)
	defer cancel()
	for {
		elapsed := s.cfg.Now().Sub(start).Milliseconds()
		var sleep int64
		switch gates.CaptchaPollStep(elapsed) {
		case gates.PollGiveUp:
			return "", ErrTimedOut
		case gates.PollWait:
			sleep = gates.CaptchaFirstWait - elapsed
		case gates.PollNow:
			token, err := s.api.taskResult(pctx, kind, id)
			if err != nil && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
				return "", ErrTimedOut
			}
			if err != nil || token != "" {
				return token, err
			}
			sleep = gates.CaptchaPollEvery
		}
		if err := s.cfg.Sleep(pctx, time.Duration(sleep)*time.Millisecond); err != nil {
			if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
				return "", ErrTimedOut
			}
			return "", err
		}
	}
}

func httpURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
