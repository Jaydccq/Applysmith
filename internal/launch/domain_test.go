package launch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"applysmith/internal/api"
	"applysmith/internal/browser"
	"applysmith/internal/evaluate"
	"applysmith/internal/llm"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

const postingText = "Software Engineer\nCompany: Example Systems\nLocation: Remote - United States\n\n" +
	"Example Systems builds scheduling software for clinics. You will design and build backend services in Go, own their reliability in production, " +
	"and work with a small team on a shared roadmap. We value clear writing, careful design, and steady delivery.\n\n" +
	"Requirements: three or more years building production backend services; experience with SQL databases."

// blockingReader holds every read until release closes; entered receives one
// value per read that started.
type blockingReader struct{ release, entered chan struct{} }

func (b blockingReader) Read(ctx context.Context, _ string) (evaluate.PageRead, error) {
	if b.entered != nil {
		b.entered <- struct{}{}
	}
	select {
	case <-b.release:
	case <-ctx.Done():
		return evaluate.PageRead{}, context.Cause(ctx)
	}
	return evaluate.PageRead{Title: "Software Engineer", Company: "Example Systems", Text: postingText, Method: "http", Result: "live"}, nil
}

// domainOptions routes every model call to the fake provider.
func domainOptions(ws *workspace.Workspace, backend vault.Backend, reader evaluate.Reader) Options {
	fake := llm.NewFake()
	fake.SetGenerator(evaluate.FakeGenerator)
	o := opts(ws)
	o.Backend = backend
	o.Domain = api.DomainOptions{LLM: []llm.Option{llm.WithFake(fake), llm.WithOfflineSimulation()}, Reader: reader}
	return o
}

type apiClient struct {
	t      *testing.T
	origin string
	token  string
}

func (c apiClient) do(method, path string, body any, key string) (int, map[string]any) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.origin+path, rd)
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var env struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(raw, &env)
	return resp.StatusCode, env.Data
}

func tokenOf(t *testing.T, ws *workspace.Workspace, backend vault.Backend) string {
	t.Helper()
	id, err := ws.ID()
	if err != nil {
		t.Fatal(err)
	}
	v, err := vault.New(id, backend, vault.WithGetenv(func(string) string { return "" }))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := v.Get(vault.APIToken)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// The headless command and the desktop app start the server through Start, so
// the capabilities the dashboard needs must come up there: the model router,
// the tracker and the evaluation pool.
func TestStartServesTheProductCapabilities(t *testing.T) {
	ws := workspace.Open(filepath.Join(t.TempDir(), "ws"))
	backend := vault.NewMemory()
	ctx := context.Background()
	srv, err := Start(ctx, domainOptions(ws, backend, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(ctx)
	if _, err := ws.Init(); err != nil {
		t.Fatal(err)
	}
	c := apiClient{t: t, origin: srv.Origin(), token: tokenOf(t, ws, backend)}
	for _, path := range []string{"/api/candidacies", "/api/tracker/summary", "/api/reports", "/api/inbox", "/api/llm/routing", "/api/apply/list", "/api/setup/state"} {
		if status, _ := c.do("GET", path, nil, ""); status != 200 {
			t.Errorf("GET %s: %d", path, status)
		}
	}
	status, data := c.do("POST", "/api/evaluations", map[string]any{"text": postingText, "url": "https://jobs.example.com/p/launch"}, "launch-key-0001")
	if status != 202 {
		t.Fatalf("start evaluation: %d", status)
	}
	id := data["runId"].(string)
	// The run's own pace is not under test: only a run that never ends fails,
	// a minute before go test's own timeout would.
	deadline, ok := t.Deadline()
	if !ok {
		deadline = time.Now().Add(10 * time.Minute)
	}
	for {
		_, out := c.do("GET", "/api/evaluations/"+id, nil, "")
		if out["outcome"] == "reported" {
			break
		}
		if time.Now().After(deadline.Add(-time.Minute)) {
			t.Fatalf("the evaluation did not finish: %v", out)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, list := c.do("GET", "/api/candidacies", nil, ""); len(list["entries"].([]any)) != 1 {
		t.Errorf("the tracker holds %v", list)
	}
}

// Shutdown interrupts a running job before the database closes; the next
// start finds it ended and starts nothing in its place.
func TestShutdownInterruptsJobsAndTheNextStartFindsThemEnded(t *testing.T) {
	ws := workspace.Open(filepath.Join(t.TempDir(), "ws"))
	backend := vault.NewMemory()
	ctx := context.Background()
	reader := blockingReader{release: make(chan struct{}), entered: make(chan struct{}, 4)}
	srv, err := Start(ctx, domainOptions(ws, backend, reader))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Init(); err != nil {
		t.Fatal(err)
	}
	c := apiClient{t: t, origin: srv.Origin(), token: tokenOf(t, ws, backend)}
	status, data := c.do("POST", "/api/evaluations", map[string]any{"url": "https://jobs.example.com/p/stop"}, "launch-key-0002")
	if status != 202 {
		t.Fatalf("start evaluation: %d", status)
	}
	id := data["runId"].(string)
	select {
	case <-reader.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the job never started")
	}
	if _, board := c.do("GET", "/api/runs?state=working", nil, ""); len(board["entries"].([]any)) != 1 {
		t.Fatalf("the running job is not on the board: %v", board)
	}
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	srv2, err := Start(ctx, domainOptions(ws, backend, reader))
	if err != nil {
		t.Fatal(err)
	}
	defer srv2.Shutdown(ctx)
	c.origin = srv2.Origin()
	_, out := c.do("GET", "/api/evaluations/"+id, nil, "")
	if out["status"] != "failed" || out["reason"] != "interrupted" {
		t.Fatalf("after a restart: %v", out)
	}
	if _, board := c.do("GET", "/api/runs?state=working", nil, ""); len(board["entries"].([]any)) != 0 {
		t.Errorf("a restart re-dispatched work: %v", board)
	}
}

// This exercises the default URL evaluation assembly with a real rendered
// document, using only a local fixture, fake models and a throwaway browser.
func TestURLOnlyEvaluationRenderedBrowser(t *testing.T) {
	if os.Getenv("APPLYSMITH_TEST_EVALUATION_BROWSER") != "1" {
		t.Skip("set APPLYSMITH_TEST_EVALUATION_BROWSER=1 and build with -tags browser")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Second)
	defer cancel()
	root := t.TempDir()
	chrome, err := browser.DiscoverChrome(ctx, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	headless, ok := any(browser.Options{Chrome: chrome, ProfileDir: filepath.Join(root, "applysmith-browser-test-profile"), StateDir: filepath.Join(root, "browser-state"), DownloadsDir: filepath.Join(root, "downloads"), Environ: os.Environ()}).(interface{ WithHeadless() browser.Options })
	if !ok {
		t.Fatal("run this test with -tags browser")
	}
	options := headless.WithHeadless()
	hosts := any(options).(interface{ WithHostRules(string) browser.Options })
	options = hosts.WithHostRules("MAP jobs.evaluation.example 127.0.0.1")
	inst, err := browser.Open(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Stop(context.Background())
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/closed":
			w.WriteHeader(404)
			fmt.Fprint(w, "<main>This job is no longer available.</main>")
		case "/login":
			fmt.Fprint(w, `<main>Sign in to see this job <input type="password"></main>`)
		case "/challenge":
			fmt.Fprint(w, `<main>Verify you are human. Checking your browser before proceeding.</main>`)
		default:
			content, _ := json.Marshal("<h1>Software Engineer</h1><p>" + postingText + "</p><button>Apply now</button>")
			fmt.Fprintf(w, `<main id="job"></main><script>setTimeout(()=>document.getElementById('job').innerHTML=%s,80)</script>`, content)
		}
	}))
	defer fixture.Close()
	u, _ := url.Parse(fixture.URL)
	ws := workspace.Open(filepath.Join(root, "ws"))
	backend := vault.NewMemory()
	o := domainOptions(ws, backend, nil)
	o.Domain.Liveness = &browserLiveness{open: (&chromeOpener{opts: options}).instance}
	srv, err := Start(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(context.Background())
	if _, err := ws.Init(); err != nil {
		t.Fatal(err)
	}
	c := apiClient{t: t, origin: srv.Origin(), token: tokenOf(t, ws, backend)}
	for _, name := range []string{"open", "closed", "login", "challenge"} {
		t.Run(name, func(t *testing.T) {
			status, data := c.do("POST", "/api/evaluations", map[string]any{"url": "http://jobs.evaluation.example:" + u.Port() + "/" + name}, "browser-url-only-"+name)
			if status != 202 {
				t.Fatalf("start %d %v", status, data)
			}
			id := data["runId"].(string)
			deadline := time.Now().Add(25 * time.Second)
			for {
				code, out := c.do("GET", "/api/evaluations/"+id, nil, "")
				if code != 200 {
					t.Fatalf("poll returned HTTP %d: %v", code, out)
				}
				if out["status"] == "succeeded" || out["status"] == "failed" {
					if name == "open" {
						if out["status"] != "succeeded" || out["score"] == nil || out["reportNumber"] == nil {
							t.Fatalf("rendered posting: %v", out)
						}
					} else if out["status"] != "failed" || out["reportNumber"] != nil {
						t.Fatalf("unreadable posting evaluated: %v", out)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("evaluation timeout: %v", out)
				}
				time.Sleep(500 * time.Millisecond)
			}
		})
	}
}
