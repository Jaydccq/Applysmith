package discovery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Rate and retry bounds from the sweep requirement.
const (
	maxInFlight     = 16
	defaultHostConc = 4
	defaultMinGap   = 100 * time.Millisecond
	requestTimeout  = 15 * time.Second
	maxRetries      = 3
	baseBackoff     = time.Second
	maxRetryAfter   = 60 * time.Second
	maxBody         = 32 << 20
	maxRedirects    = 10
	userAgent       = "applysmith/dev (+local job-search runtime; polite public-API reader)"
)

// TransientError is a request that failed at the network level, timed out, or
// kept answering 5xx after its retries. It is never a verdict on a board.
type TransientError struct {
	URL     string
	Status  int // 0 for a network-level failure
	Network bool
	Err     error
}

func (e *TransientError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", hostOf(e.URL), e.Err)
	}
	return fmt.Sprintf("%s answered %d after retries", hostOf(e.URL), e.Status)
}
func (e *TransientError) Unwrap() error { return e.Err }

// errTooLarge is a response over maxBody. A retry would read the same bytes.
var errTooLarge = fmt.Errorf("response is larger than %d MiB", maxBody>>20)

// RateLimitedError is a host that still answered 429 after its retries. The
// host's breaker is now open for the rest of the run.
type RateLimitedError struct{ Host string }

func (e *RateLimitedError) Error() string { return e.Host + " is rate limiting; deferred" }

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Host
	}
	return raw
}

// Response is one successful HTTP exchange.
type Response struct {
	Status      int
	Body        []byte
	ETag        string
	NotModified bool
	Header      http.Header
}

// Client is a polite HTTP client: a global in-flight cap, a per-host cap, a
// minimum gap between requests to one host, retries with exponential backoff,
// Retry-After and a per-host rate-limit breaker. A Client is meant for one
// run; Session returns a fresh one.
type Client struct {
	HTTP      *http.Client
	UserAgent string
	// HostConcurrency caps requests in flight per host (the portals
	// concurrency.list setting).
	HostConcurrency int
	MinGap          time.Duration
	// Sleep and Now are injectable for tests.
	Sleep func(context.Context, time.Duration) error
	Now   func() time.Time

	global chan struct{}
	mu     sync.Mutex
	hosts  map[string]*hostState
}

type hostState struct {
	sem     chan struct{}
	next    time.Time
	tripped bool
}

// NewClient returns a client with the production bounds.
func NewClient() *Client {
	return (&Client{HTTP: &http.Client{}, UserAgent: userAgent, MinGap: defaultMinGap, HostConcurrency: defaultHostConc}).Session(0)
}

// Session returns a copy that shares the transport but not the per-run state:
// breakers, host gaps and in-flight counters start fresh.
func (c *Client) Session(hostConcurrency int) *Client {
	n := &Client{HTTP: c.HTTP, UserAgent: c.UserAgent, HostConcurrency: c.HostConcurrency, MinGap: c.MinGap, Sleep: c.Sleep, Now: c.Now,
		global: make(chan struct{}, maxInFlight), hosts: map[string]*hostState{}}
	if hostConcurrency > 0 {
		n.HostConcurrency = hostConcurrency
	}
	// Only the transport is shared; the copy refuses to leave the host, so a
	// captive portal or a hijacked redirect cannot stand in for a vendor.
	hc := http.Client{}
	if c.HTTP != nil {
		hc = *c.HTTP
	}
	hc.CheckRedirect = sameHostRedirect
	n.HTTP = &hc
	if n.UserAgent == "" {
		n.UserAgent = userAgent
	}
	if n.HostConcurrency < 1 {
		n.HostConcurrency = defaultHostConc
	}
	if n.Sleep == nil {
		n.Sleep = sleepCtx
	}
	if n.Now == nil {
		n.Now = time.Now
	}
	return n
}

// sameHostRedirect follows a redirect only within the host that was asked.
// Any other redirect ends the exchange with the redirect response itself,
// which the caller judges as a transient failure.
func sameHostRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	first := via[0].URL
	if !strings.EqualFold(req.URL.Host, first.Host) || req.URL.Scheme != first.Scheme {
		return http.ErrUseLastResponse
	}
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Client) host(h string) *hostState {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.hosts[h]
	if s == nil {
		s = &hostState{sem: make(chan struct{}, c.HostConcurrency)}
		c.hosts[h] = s
	}
	return s
}

// Tripped reports whether the host's rate-limit breaker is open.
func (c *Client) Tripped(host string) bool {
	s := c.host(host)
	c.mu.Lock()
	defer c.mu.Unlock()
	return s.tripped
}

func (c *Client) trip(h *hostState) {
	c.mu.Lock()
	h.tripped = true
	c.mu.Unlock()
}

// reserve returns how long to wait so requests to one host stay MinGap apart.
func (c *Client) reserve(h *hostState) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.Now()
	at := h.next
	if at.Before(now) {
		at = now
	}
	h.next = at.Add(c.MinGap)
	return at.Sub(now)
}

// Request is one HTTP exchange. The zero value of every optional field is the
// sweep's default: GET, JSON, 15 seconds, 3 retries with exponential backoff.
type Request struct {
	Method string
	URL    string
	Body   []byte
	// Header carries extra request headers (a key, a content type).
	Header http.Header
	// Accept replaces the JSON default for page reads.
	Accept string
	ETag   string
	// Timeout bounds one attempt.
	Timeout time.Duration
	// Retries is the number of retries after the first attempt; NoRetry
	// disables them.
	Retries int
	NoRetry bool
	// Linear, when set, spaces retries Linear, 2*Linear, ... apart instead of
	// doubling from one second.
	Linear time.Duration
}

// Get fetches a URL, with a conditional request when etag is set. A 4xx or
// other non-retryable status is returned as a Response for the caller to
// judge; a network failure, timeout, 5xx or 429 that outlives its retries is
// a *TransientError or *RateLimitedError.
func (c *Client) Get(ctx context.Context, rawURL, etag string) (Response, error) {
	return c.Do(ctx, Request{URL: rawURL, ETag: etag})
}

// Do makes the request under the client's rate and retry bounds.
func (c *Client) Do(ctx context.Context, rq Request) (Response, error) {
	u, err := url.Parse(rq.URL)
	if err != nil {
		return Response{}, err
	}
	retries := rq.Retries
	if retries == 0 {
		retries = maxRetries
	}
	if rq.NoRetry {
		retries = 0
	}
	h := c.host(u.Host)
	if c.Tripped(u.Host) {
		return Response{}, &RateLimitedError{Host: u.Host}
	}
	var last error
	for attempt := 0; ; attempt++ {
		resp, wait, err := c.once(ctx, h, rq)
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		var te *TransientError
		var rl *RateLimitedError
		switch {
		case errors.As(err, &rl):
			last = &RateLimitedError{Host: u.Host}
		case errors.As(err, &te):
			if errors.Is(err, errTooLarge) {
				return Response{}, err
			}
			last = err
		default:
			return Response{}, err
		}
		if attempt >= retries {
			if errors.As(last, &rl) {
				c.trip(h)
			}
			return Response{}, last
		}
		if wait <= 0 {
			wait = baseBackoff << attempt
			if rq.Linear > 0 {
				wait = rq.Linear * time.Duration(attempt+1)
			}
		}
		if err := c.Sleep(ctx, min(wait, maxRetryAfter)); err != nil {
			return Response{}, err
		}
	}
}

// once makes one attempt. wait is the server's Retry-After when it sent one.
func (c *Client) once(ctx context.Context, h *hostState, rq Request) (Response, time.Duration, error) {
	rawURL := rq.URL
	select {
	case c.global <- struct{}{}:
		defer func() { <-c.global }()
	case <-ctx.Done():
		return Response{}, 0, ctx.Err()
	}
	select {
	case h.sem <- struct{}{}:
		defer func() { <-h.sem }()
	case <-ctx.Done():
		return Response{}, 0, ctx.Err()
	}
	if err := c.Sleep(ctx, c.reserve(h)); err != nil {
		return Response{}, 0, err
	}
	timeout := rq.Timeout
	if timeout <= 0 {
		timeout = requestTimeout
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	method := rq.Method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if rq.Body != nil {
		body = bytes.NewReader(rq.Body)
	}
	req, err := http.NewRequestWithContext(rctx, method, rawURL, body)
	if err != nil {
		return Response{}, 0, err
	}
	for k, vs := range rq.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("User-Agent", c.UserAgent)
	accept := rq.Accept
	if accept == "" {
		accept = "application/json"
	}
	req.Header.Set("Accept", accept)
	if rq.ETag != "" {
		req.Header.Set("If-None-Match", rq.ETag)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return Response{}, 0, &TransientError{URL: rawURL, Network: true, Err: scrubURL(err)}
	}
	defer res.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err != nil {
		return Response{}, 0, &TransientError{URL: rawURL, Network: true, Err: scrubURL(err)}
	}
	if len(payload) > maxBody {
		return Response{}, 0, &TransientError{URL: rawURL, Err: errTooLarge}
	}
	switch {
	case res.StatusCode == http.StatusTooManyRequests:
		return Response{}, retryAfter(res.Header.Get("Retry-After"), c.Now()), &RateLimitedError{Host: res.Request.URL.Host}
	case res.StatusCode >= 500:
		return Response{}, retryAfter(res.Header.Get("Retry-After"), c.Now()), &TransientError{URL: rawURL, Status: res.StatusCode}
	}
	return Response{Status: res.StatusCode, Body: payload, ETag: res.Header.Get("ETag"), NotModified: res.StatusCode == http.StatusNotModified,
		Header: res.Header}, 0, nil
}

// scrubURL drops the request URL from a transport error, because a URL may
// carry a key in its query and errors reach run records.
func scrubURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func retryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return t.Sub(now)
	}
	return 0
}
