package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testToken = "test-token-value-0001"

// fakeAPI is a local API double. It records every request it receives.
type fakeAPI struct {
	srv *httptest.Server
	mu  sync.Mutex
	got []recorded
	// handle answers one request; nil answers 404.
	handle func(w http.ResponseWriter, r *http.Request)
}

type recorded struct{ Method, Path, Query, Auth string }

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.got = append(f.got, recorded{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")})
		f.mu.Unlock()
		if f.handle == nil {
			writeFail(w, 404, "not_found", "no such route")
			return
		}
		f.handle(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.got...)
}

// serve answers each path with a fixed data document.
func (f *fakeAPI) serve(routes map[string]any) {
	f.handle = func(w http.ResponseWriter, r *http.Request) {
		data, ok := routes[r.URL.Path]
		if !ok {
			writeFail(w, 404, "not_found", "no such route")
			return
		}
		writeOK(w, data)
	}
}

func writeOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "requestId": "req_test0001", "data": data})
}

func writeFail(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "requestId": "req_test0001",
		"error": map[string]any{"code": code, "message": msg, "retryable": false}})
}

func (f *fakeAPI) client(opts ...func(*Options)) *Client {
	o := Options{
		Origin: func() (string, error) { return f.srv.URL, nil },
		Token:  func() (string, error) { return testToken, nil },
	}
	for _, fn := range opts {
		fn(&o)
	}
	return NewClient(o)
}

// connect starts the server over an in-memory transport and returns a client
// session, as an MCP host would hold it.
func connect(t *testing.T, api *Client, logs io.Writer) *mcp.ClientSession {
	t.Helper()
	if logs == nil {
		logs = io.Discard
	}
	ctx := context.Background()
	srv := New(api, "test-1.2.3", slog.New(slog.NewTextHandler(logs, nil)))
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-host", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", name, err)
	}
	return res
}

func textOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("result has no content")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content is %T, not text", res.Content[0])
	}
	return tc.Text
}

// structured decodes the structured content of a successful result.
func structured(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(t, res))
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("structured content %s: %v", b, err)
	}
	return m
}

// toolError decodes the typed error of a failed result.
func toolError(t *testing.T, res *mcp.CallToolResult) ToolError {
	t.Helper()
	if !res.IsError {
		t.Fatalf("expected a tool error, got %s", textOf(t, res))
	}
	var te ToolError
	if err := json.Unmarshal([]byte(textOf(t, res)), &te); err != nil {
		t.Fatalf("error text is not a typed error: %q", textOf(t, res))
	}
	return te
}

func contains(t *testing.T, s string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(s, p) {
			t.Errorf("%q does not contain %q", s, p)
		}
	}
}
