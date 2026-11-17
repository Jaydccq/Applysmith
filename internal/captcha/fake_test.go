package captcha

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/gates"
)

// fakeService is an httptest stand-in for the 2Captcha API. Zero values give
// a healthy account that answers on the first poll.
type fakeService struct {
	t *testing.T

	mu      sync.Mutex
	balance string // JSON value for the balance; "" means 5.00
	// createFails is how many createTask calls answer HTTP 500 before one works.
	createFails int
	// pendingPolls is how many getTaskResult calls answer "processing".
	pendingPolls int
	forever      bool // never ready
	tokens       []string
	// raw overrides the reply of a path with a literal body.
	raw map[string]string
	// errorCode makes createTask answer a service error with this code.
	errorCode string

	hits   map[string]int
	bodies []map[string]any
	nextID int64
	srv    *httptest.Server
}

func newFake(t *testing.T) *fakeService {
	f := &fakeService{t: t, hits: map[string]int{}, tokens: []string{"token-one", "token-two", "token-three"}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeService) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.bodies = append(f.bodies, body)
	f.hits[r.URL.Path]++
	write := func(status int, s string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, s)
	}
	if s, ok := f.raw[r.URL.Path]; ok {
		write(200, s)
		return
	}
	switch r.URL.Path {
	case "/getBalance":
		bal := f.balance
		if bal == "" {
			bal = "5.00"
		}
		write(200, `{"errorId":0,"balance":`+bal+`}`)
	case "/createTask":
		if f.errorCode != "" {
			write(200, `{"errorId":1,"errorCode":"`+f.errorCode+`","errorDescription":"echo `+fmt.Sprint(body["clientKey"])+`"}`)
			return
		}
		if f.createFails > 0 {
			f.createFails--
			write(500, `{"errorId":1,"errorCode":"ERROR_INTERNAL"}`)
			return
		}
		f.nextID++
		write(200, fmt.Sprintf(`{"errorId":0,"taskId":%d}`, f.nextID))
	case "/getTaskResult":
		if f.forever || f.pendingPolls > 0 {
			f.pendingPolls--
			write(200, `{"errorId":0,"status":"processing"}`)
			return
		}
		tok := f.tokens[0]
		if len(f.tokens) > 1 {
			f.tokens = f.tokens[1:]
		}
		write(200, fmt.Sprintf(`{"errorId":0,"status":"ready","solution":{"gRecaptchaResponse":%q,"token":%q}}`, tok, tok))
	default:
		write(404, `{}`)
	}
}

func (f *fakeService) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

func (f *fakeService) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.hits {
		n += c
	}
	return n
}

func (f *fakeService) setRaw(path, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.raw == nil {
		f.raw = map[string]string{}
	}
	f.raw[path] = body
}

// fakeClock advances only when a session sleeps, so polling takes no time.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	c.sleeps = append(c.sleeps, d)
	return nil
}

func (c *fakeClock) slept() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	var total time.Duration
	for _, d := range c.sleeps {
		total += d
	}
	return total
}

type rig struct {
	f       *fakeService
	clock   *fakeClock
	auditMu sync.Mutex
	audited []gates.Decision
	logs    *strings.Builder
}

// keyForTests is built at run time so no key-shaped literal sits in source.
func keyForTests() string { return "k" + strings.Repeat("a1", 10) }

func newRig(t *testing.T, mut func(*Config)) (*rig, *Session) {
	t.Helper()
	r := &rig{f: newFake(t), clock: &fakeClock{now: time.Unix(1_800_000_000, 0)}, logs: &strings.Builder{}}
	cfg := Config{
		Run: gates.RunApplySubmit, Switch: gates.SolverOn,
		Key:  keyForTests,
		Skip: func() gates.SkipStatus { return gates.SkipNotListed },
		Audit: func(d gates.Decision) {
			r.auditMu.Lock()
			defer r.auditMu.Unlock()
			r.audited = append(r.audited, d)
		},
		BaseURL: r.f.srv.URL, HTTP: r.f.srv.Client(),
		Now: r.clock.Now, Sleep: r.clock.Sleep,
	}
	if mut != nil {
		mut(&cfg)
	}
	return r, New(cfg)
}

func widget(kind string) Challenge {
	return Challenge{Kind: kind, Point: gates.PointBeforePress, SiteKey: "site-key-1", PageURL: "https://jobs.example.com/apply/1"}
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}
