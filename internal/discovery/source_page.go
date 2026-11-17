package discovery

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
)

// maxAttempts bounds the attempt trail of a run; the oldest are dropped.
const maxAttempts = 200

// Attempt statuses, in the precedence they are chosen in.
const (
	AttemptChallenge   = "challenge"
	AttemptRateLimited = "rate-limited"
	AttemptTimeout     = "timeout"
	AttemptError       = "error"
	AttemptEmpty       = "empty"
	AttemptOK          = "ok"
)

// Attempt is one list page read: where, how it ended and how long it took.
// The path carries no query, which may hold a key or a search.
type Attempt struct {
	Status    string `json:"status"`
	HTTP      int    `json:"http,omitempty"`
	Host      string `json:"host"`
	Path      string `json:"path"`
	LatencyMS int64  `json:"latency_ms"`
	N         int    `json:"attempt"`
}

var challengeRE = lazyregexp.New(`(?i)just a moment|cf-chl|checking your browser|verify you are (a )?human|captcha|attention required`)

// attemptStatus picks the status of a page read by the precedence: challenge,
// rate-limited, timeout, error, empty, ok. A page with zero items and a
// challenge is a challenge, never empty. The client answers a 429 with a
// RateLimitedError and a 5xx with a TransientError, never as a response.
func attemptStatus(res Response, err error, items int) string {
	var rl *RateLimitedError
	var te *TransientError
	switch {
	case errors.As(err, &rl):
		return AttemptRateLimited
	case err == nil && (res.Status == http.StatusForbidden || res.Status == http.StatusOK) && challengeRE.Match(head(res.Body)) && items == 0:
		return AttemptChallenge
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &te) && os.IsTimeout(te.Err)):
		return AttemptTimeout
	case err != nil || res.Status >= 400:
		return AttemptError
	case items == 0:
		return AttemptEmpty
	}
	return AttemptOK
}

func head(b []byte) []byte {
	if len(b) > 8192 {
		return b[:8192]
	}
	return b
}

// pageRead is one list-page request with its attempt record. The body is not
// judged here: the caller names how many items it found and settles the
// attempt.
type pageRead struct {
	r     *run
	url   string
	start time.Time
	Res   Response
	Err   error
}

// readPage fetches a list page as HTML.
func (r *run) readPage(ctx context.Context, rawURL string) *pageRead {
	p := &pageRead{r: r, url: rawURL, start: r.repo.now()}
	p.Res, p.Err = r.client.Do(ctx, Request{URL: rawURL, NoRetry: true, Accept: "text/html,application/xhtml+xml,text/plain;q=0.8,*/*;q=0.5"})
	return p
}

// settle appends the attempt record and returns the status. A challenge or a
// rate limit ends the run `blocked`, with no retry.
func (p *pageRead) settle(items int) (string, *endRun) {
	st := attemptStatus(p.Res, p.Err, items)
	u, _ := url.Parse(p.url)
	a := Attempt{Status: st, HTTP: p.Res.Status, LatencyMS: p.r.repo.now().Sub(p.start).Milliseconds(), N: 1}
	if u != nil {
		a.Host, a.Path = u.Host, u.Path
	}
	p.r.mu.Lock()
	p.r.rec.Attempts = append(p.r.rec.Attempts, a)
	if over := len(p.r.rec.Attempts) - maxAttempts; over > 0 {
		p.r.rec.Attempts = append([]Attempt(nil), p.r.rec.Attempts[over:]...)
	}
	p.r.mu.Unlock()
	if st == AttemptChallenge || st == AttemptRateLimited {
		return st, &endRun{status: RunBlocked, reason: st, err: errors.New(hostOf(p.url) + " answered with a " + strings.ReplaceAll(st, "-", " ") + "; the run stops without retrying")}
	}
	return st, nil
}
