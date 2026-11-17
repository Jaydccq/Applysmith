package mcpserver

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestTransport_RefusesNonGETBeforeSending(t *testing.T) {
	api := newFakeAPI(t)
	api.serve(map[string]any{"/api/candidacies": candidacyPage()})
	c := api.client()
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead} {
		req, _ := http.NewRequest(method, api.srv.URL+"/api/candidacies", strings.NewReader("{}"))
		resp, err := c.http.Do(req)
		if err == nil {
			resp.Body.Close()
			t.Errorf("%s was sent", method)
		}
	}
	if n := len(api.requests()); n != 0 {
		t.Errorf("%d requests reached the API", n)
	}
}

func TestTransport_RefusesPathsOutsideTheAllowlist(t *testing.T) {
	api := newFakeAPI(t)
	c := api.client()
	for _, p := range []string{
		"/api/config/profile", "/api/answer-bank", "/api/candidacies/7", "/api/reports", "/api/reports/1/html",
		"/api/reports/..", "/api/health/x", "/internal/agent/catalog", "/", "/api/token/rotate", "/api/apply/runs",
	} {
		resp, err := c.http.Get(api.srv.URL + p)
		if err == nil {
			resp.Body.Close()
			t.Errorf("GET %s was sent", p)
		}
	}
	if n := len(api.requests()); n != 0 {
		t.Errorf("%d requests reached the API", n)
	}
}

func TestToken_RotatedTokenIsReadOnceMoreAndNeverLogged(t *testing.T) {
	api := newFakeAPI(t)
	const fresh = "fresh-token-value-0002"
	api.handle = func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+fresh {
			writeFail(w, 401, "unauthenticated", "the token is not valid")
			return
		}
		writeOK(w, candidacyPage())
	}
	var reads atomic.Int32
	c := api.client(func(o *Options) {
		o.Token = func() (string, error) {
			if reads.Add(1) == 1 {
				return testToken, nil // the value the process started with
			}
			return fresh, nil
		}
	})
	var logs bytes.Buffer
	res := call(t, connect(t, c, &logs), "list_candidacies", map[string]any{})
	structured(t, res)
	reqs := api.requests()
	if len(reqs) != 2 || reads.Load() != 2 {
		t.Fatalf("requests = %d, token reads = %d; want one retry with one re-read", len(reqs), reads.Load())
	}
	for _, s := range []string{logs.String(), textOf(t, res)} {
		if strings.Contains(s, testToken) || strings.Contains(s, fresh) {
			t.Error("a token appears in output or logs")
		}
	}
}

func TestToken_StillRefusedAfterOneRetryIsUnauthorized(t *testing.T) {
	api := newFakeAPI(t)
	api.handle = func(w http.ResponseWriter, _ *http.Request) {
		writeFail(w, 401, "unauthenticated", "the token is not valid")
	}
	te := toolError(t, call(t, connect(t, api.client(), nil), "list_candidacies", map[string]any{}))
	if te.Code != CodeUnauthorized || !strings.Contains(te.Remedy, "applysmith pair") {
		t.Errorf("error = %+v", te)
	}
	if n := len(api.requests()); n != 2 {
		t.Errorf("requests = %d, want exactly one retry", n)
	}
}

func TestErrors_MissingTokenIsUnauthorizedWithPairingRemedy(t *testing.T) {
	api := newFakeAPI(t)
	c := api.client(func(o *Options) { o.Token = func() (string, error) { return "", ErrNoToken } })
	te := toolError(t, call(t, connect(t, c, nil), "list_candidacies", map[string]any{}))
	if te.Code != CodeUnauthorized || !strings.Contains(te.Remedy, "applysmith pair") {
		t.Errorf("error = %+v", te)
	}
	if len(api.requests()) != 0 {
		t.Error("a call was sent without a token")
	}
}

func TestErrors_ServerNotRunningForEveryToolButHealth(t *testing.T) {
	api := newFakeAPI(t)
	c := api.client()
	api.srv.Close()
	cs := connect(t, c, nil)
	for name, args := range map[string]any{
		"list_candidacies": map[string]any{}, "apply_list_status": map[string]any{},
		"get_report": map[string]any{"number": 1}, "get_application_by_url": map[string]any{"url": "https://example.com/p"},
	} {
		te := toolError(t, call(t, cs, name, args))
		if te.Code != CodeServerNotRunning || !strings.Contains(te.Remedy, "applysmith serve") {
			t.Errorf("%s: error = %+v", name, te)
		}
	}
}

func TestErrors_RateLimitedCarriesRetryAfter(t *testing.T) {
	api := newFakeAPI(t)
	api.handle = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"ok":false,"requestId":"req_x","error":{"code":"rate_limited","message":"slow down","retryable":true,"details":{"retryAfterSeconds":7}}}`))
	}
	te := toolError(t, call(t, connect(t, api.client(), nil), "apply_list_status", map[string]any{}))
	if te.Code != CodeRateLimited || te.RetryAfterSeconds != 7 {
		t.Errorf("error = %+v", te)
	}
	if n := len(api.requests()); n != 1 {
		t.Errorf("a rate-limited call was retried (%d requests)", n)
	}
}

func TestErrors_SlowAPIIsATimeout(t *testing.T) {
	api := newFakeAPI(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	api.handle = func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}
	c := api.client(func(o *Options) { o.CallTimeout = 150 * time.Millisecond })
	start := time.Now()
	te := toolError(t, call(t, connect(t, c, nil), "list_candidacies", map[string]any{}))
	if te.Code != CodeTimeout {
		t.Errorf("error = %+v", te)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("the call outlived its timeout")
	}
}

// roundTrip is a transport made of one function.
type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// With four calls held in the transport, the other four wait on the client:
// once every goroutine is blocked, exactly four requests were sent.
func TestConcurrency_AtMostFourCallsAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var inflight, peak, sent atomic.Int32
		release := make(chan struct{})
		c := NewClient(Options{
			Origin: func() (string, error) { return "http://127.0.0.1:1", nil },
			Token:  func() (string, error) { return "test-token", nil },
			Base: roundTrip(func(r *http.Request) (*http.Response, error) {
				sent.Add(1)
				n := inflight.Add(1)
				for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
				}
				<-release
				inflight.Add(-1)
				body := io.NopCloser(strings.NewReader(`{"ok":true,"requestId":"req_test0001","data":{}}`))
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: body, Request: r}, nil
			}),
		})
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				var out map[string]any
				if err := c.get(t.Context(), "/api/candidacies", nil, false, &out); err != nil {
					t.Error(err)
				}
			})
		}
		synctest.Wait()
		if n := sent.Load(); n != 4 {
			t.Errorf("requests sent while four are held = %d, want 4", n)
		}
		close(release)
		wg.Wait()
		if peak.Load() != 4 || sent.Load() != 8 {
			t.Errorf("peak concurrency = %d, requests = %d; want 4 and 8", peak.Load(), sent.Load())
		}
	})
}

func TestClient_DoesNotFollowRedirects(t *testing.T) {
	api := newFakeAPI(t)
	api.handle = func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/api/config/profile", http.StatusFound)
	}
	te := toolError(t, call(t, connect(t, api.client(), nil), "list_candidacies", map[string]any{}))
	if te.Code != CodeServerError {
		t.Errorf("error = %+v", te)
	}
	if n := len(api.requests()); n != 1 {
		t.Errorf("requests = %d", n)
	}
}
