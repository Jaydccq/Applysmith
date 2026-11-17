package signals

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"
)

// Bounds from the collector contract.
const (
	requestTimeout = 30 * time.Second
	maxRetries     = 2
	baseBackoff    = time.Second
	maxResponse    = 8 << 20
	defaultUA      = "applysmith/dev (+local job-search runtime)"
)

// Gaps between requests to one host, by the class of the call.
const (
	gapEdgar = 100 * time.Millisecond // 10 requests per second
	gapHN    = time.Second
	gapRSS   = time.Second
	gapWeb   = 2 * time.Second
	gapX     = time.Second
)

// HTTPError is a response the caller has to judge: the status and a short,
// key-free excerpt of the body.
type HTTPError struct {
	Status int
	Host   string
	Detail string
}

func (e *HTTPError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("%s answered %d: %s", e.Host, e.Status, e.Detail)
	}
	return fmt.Sprintf("%s answered %d", e.Host, e.Status)
}

// Response is one finished exchange.
type Response struct {
	Status int
	Body   []byte
	Header http.Header
}

// Fetcher makes the outbound requests of the collectors: one limiter per host,
// a 30 second timeout, and at most two retries with exponential backoff on 429
// and 5xx.
type Fetcher struct {
	HTTP  *http.Client
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) error

	mu   sync.Mutex
	next map[string]time.Time
}

// NewFetcher returns a fetcher over client with the wall clock.
func NewFetcher(client *http.Client) *Fetcher {
	if client == nil {
		client = &http.Client{}
	}
	return &Fetcher{HTTP: client, Now: time.Now, Sleep: sleepCtx}
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

// Request describes one call. Gap is the minimum spacing of requests to the
// host; Header carries the call's own headers (a user agent, a key).
type Request struct {
	Method string
	URL    string
	Body   []byte
	Header http.Header
	Gap    time.Duration
}

// reserve returns how long to wait so requests to host stay gap apart.
func (f *Fetcher) reserve(host string, gap time.Duration) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.next == nil {
		f.next = map[string]time.Time{}
	}
	now := f.Now()
	at := f.next[host]
	if at.Before(now) {
		at = now
	}
	f.next[host] = at.Add(gap)
	return at.Sub(now)
}

// Do makes the request. A 2xx, 304 or other non-retryable status is returned
// for the caller to judge; 429 and 5xx are retried and then returned as an
// *HTTPError.
func (f *Fetcher) Do(ctx context.Context, rq Request) (Response, error) {
	u, err := url.Parse(rq.URL)
	if err != nil {
		return Response{}, err
	}
	method := rq.Method
	if method == "" {
		method = http.MethodGet
	}
	var last error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			if err := f.Sleep(ctx, baseBackoff<<(attempt-1)); err != nil {
				return Response{}, err
			}
		}
		if err := f.Sleep(ctx, f.reserve(u.Host, rq.Gap)); err != nil {
			return Response{}, err
		}
		res, retry, err := f.once(ctx, method, rq, u.Host)
		if err == nil {
			return res, nil
		}
		last = err
		if !retry || ctx.Err() != nil {
			break
		}
	}
	return Response{}, last
}

func (f *Fetcher) once(ctx context.Context, method string, rq Request, host string) (Response, bool, error) {
	cctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var body io.Reader
	if rq.Body != nil {
		body = bytes.NewReader(rq.Body)
	}
	req, err := http.NewRequestWithContext(cctx, method, rq.URL, body)
	if err != nil {
		return Response{}, false, err
	}
	req.Header.Set("User-Agent", defaultUA)
	for k, v := range rq.Header {
		req.Header[k] = v
	}
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return Response{}, true, fmt.Errorf("%s: %w", host, scrubURLError(err))
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return Response{}, true, fmt.Errorf("%s: reading the response: %w", host, err)
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		if ra, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && ra > 0 && ra <= 5 {
			_ = f.Sleep(ctx, time.Duration(ra)*time.Second)
		}
		return Response{}, true, &HTTPError{Status: resp.StatusCode, Host: host, Detail: excerpt(b)}
	}
	return Response{Status: resp.StatusCode, Body: b, Header: resp.Header}, false, nil
}

// scrubURLError drops the request URL from a transport error: a URL can carry
// a query that names a key.
func scrubURLError(err error) error {
	if ue, ok := err.(*url.Error); ok {
		return ue.Err
	}
	return err
}

// excerpt is the start of an error body as text, cut at a character boundary.
func excerpt(b []byte) string {
	s := string(b)
	if n := 160; len(s) > n {
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		s = s[:n]
	}
	return cleanSpace(stripTags(s))
}
