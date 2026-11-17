package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

func testLocator(t *testing.T, env map[string]string) (Locator, *workspace.Workspace) {
	t.Helper()
	ws := workspace.Open(t.TempDir())
	if err := os.MkdirAll(ws.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	return Locator{Workspace: ws, Backend: vault.NewMemory(), Getenv: func(k string) string { return env[k] }}, ws
}

func writeLock(t *testing.T, ws *workspace.Workspace, doc map[string]any) {
	t.Helper()
	b, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(ws.StateDir(), lockFileName), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLocate_PortFromLockFileOnly(t *testing.T) {
	l, ws := testLocator(t, nil)
	noServer := func(what string) {
		t.Helper()
		var te *ToolError
		if o, err := l.Origin(); !errors.As(err, &te) || te.Code != CodeServerNotRunning {
			t.Errorf("%s: origin %q, err %v; want server_not_running and no guessed port", what, o, err)
		}
	}
	noServer("no lock file")
	writeLock(t, ws, map[string]any{"pid": 1, "port": 48000, "origin": "http://127.0.0.1:48000"})
	if o, _ := l.Origin(); o != "http://127.0.0.1:48000" {
		t.Errorf("non-default port: %s", o)
	}
	writeLock(t, ws, map[string]any{"pid": 1, "origin": "http://localhost:48123"})
	noServer("an origin without a port")
	// A lock file cannot send the token to another host.
	writeLock(t, ws, map[string]any{"pid": 1, "port": 48000, "origin": "http://attacker.example.com:48000"})
	if o, _ := l.Origin(); o != "http://127.0.0.1:48000" {
		t.Errorf("foreign host in lock file: %s", o)
	}
	if err := os.WriteFile(filepath.Join(ws.StateDir(), lockFileName), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	noServer("broken lock file")
	writeLock(t, ws, map[string]any{"pid": 1})
	noServer("lock file without a port")
}

func TestLocate_CallsGoToTheLockFilePort(t *testing.T) {
	api := newFakeAPI(t)
	l, ws := testLocator(t, map[string]string{vault.EnvName(vault.APIToken): testToken})
	api.serve(map[string]any{"/api/candidacies": candidacyPage(), "/api/health": healthOf(workspace.PathKey(ws.Root))})
	writeLock(t, ws, map[string]any{"pid": 1, "port": portOf(t, api)})
	cs := connect(t, l.Client(), nil)
	structured(t, call(t, cs, "list_candidacies", map[string]any{}))
	structured(t, call(t, cs, "list_candidacies", map[string]any{}))
	// Before each call the server is asked who it is, without the token.
	r := api.requests()
	if len(r) != 4 {
		t.Fatalf("requests = %+v", r)
	}
	for i := 0; i < len(r); i += 2 {
		if r[i].Path != "/api/health" || r[i].Auth != "" || r[i+1].Path != "/api/candidacies" || r[i+1].Auth != "Bearer "+testToken {
			t.Errorf("requests = %+v", r)
		}
	}
}

// A lock file left by a stopped server names a port another program may hold
// now; the token never goes to a server that reports another workspace.
func TestLocate_TokenNeverGoesToAnotherWorkspacesServer(t *testing.T) {
	api := newFakeAPI(t)
	api.serve(map[string]any{"/api/candidacies": candidacyPage(), "/api/health": healthOf("0123456789ab")})
	l, ws := testLocator(t, map[string]string{vault.EnvName(vault.APIToken): testToken})
	writeLock(t, ws, map[string]any{"pid": 1, "port": portOf(t, api)})
	if te := toolError(t, call(t, connect(t, l.Client(), nil), "list_candidacies", map[string]any{})); te.Code != CodeServerNotRunning {
		t.Errorf("error = %+v", te)
	}
	for _, r := range api.requests() {
		if r.Auth != "" || r.Path != "/api/health" {
			t.Errorf("request sent to another workspace's server: %+v", r)
		}
	}
}

func healthOf(key string) map[string]any {
	return map[string]any{"status": "ok", "apiVersion": 1, "version": "1.0.0", "workspaceId": key, "startedAt": "2026-10-01T08:00:00.000Z"}
}

func TestLocate_TokenFromEnvironmentThenKeychain(t *testing.T) {
	l, ws := testLocator(t, nil)
	if _, err := l.Token(); !errors.Is(err, ErrNoToken) {
		t.Fatalf("uninitialised workspace: %v", err)
	}
	id, err := ws.ID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Token(); !errors.Is(err, ErrNoToken) {
		t.Fatalf("no stored token: %v", err)
	}
	v, _ := vault.New(id, l.Backend)
	if _, err := v.Set(vault.APIToken, "stored-token-0003"); err != nil {
		t.Fatal(err)
	}
	if tok, err := l.Token(); err != nil || tok != "stored-token-0003" {
		t.Fatalf("stored token = %q, %v", tok, err)
	}
	l.Getenv = func(k string) string {
		if k == vault.EnvName(vault.APIToken) {
			return "env-token-0004"
		}
		return ""
	}
	if tok, _ := l.Token(); tok != "env-token-0004" {
		t.Errorf("environment token = %q", tok)
	}
	// A rotation in the keychain is seen by the next read.
	l.Getenv = func(string) string { return "" }
	if _, err := v.Set(vault.APIToken, "rotated-token-0005"); err != nil {
		t.Fatal(err)
	}
	if tok, _ := l.Token(); tok != "rotated-token-0005" {
		t.Errorf("rotated token = %q", tok)
	}
}

func TestConfigSnippet(t *testing.T) {
	out, err := ConfigSnippet("/usr/local/bin/applysmith", "/data/ws", false)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	e := doc.MCPServers["applysmith"]
	if e.Command != "/usr/local/bin/applysmith" || len(e.Args) != 1 || e.Args[0] != "mcp" || e.Env != nil {
		t.Errorf("default workspace snippet = %s", out)
	}
	out, _ = ConfigSnippet("/usr/local/bin/applysmith", "/data/ws", true)
	_ = json.Unmarshal(out, &doc)
	if doc.MCPServers["applysmith"].Env["APPLYSMITH_WORKSPACE"] != "/data/ws" {
		t.Errorf("custom workspace snippet = %s", out)
	}
	if strings.Contains(strings.ToLower(string(out)), "token") {
		t.Error("snippet mentions a token")
	}
}

func TestCheck_PassesWhenEverythingIsUp(t *testing.T) {
	api := newFakeAPI(t)
	l, ws := testLocator(t, map[string]string{vault.EnvName(vault.APIToken): testToken})
	api.serve(map[string]any{"/api/health": healthOf(workspace.PathKey(ws.Root))})
	writeLock(t, ws, map[string]any{"pid": 1, "port": portOf(t, api)})
	var out bytes.Buffer
	if !WriteSteps(&out, RunCheck(context.Background(), l, nil)) {
		t.Fatalf("check failed:\n%s", out.String())
	}
	if n := strings.Count(out.String(), "pass  "); n != 4 || strings.Contains(out.String(), "fail") {
		t.Errorf("output:\n%s", out.String())
	}
	if strings.Contains(out.String(), testToken) {
		t.Error("the token was printed")
	}
}

func TestCheck_FailsWithARemedyWhenNoServerRuns(t *testing.T) {
	l, _ := testLocator(t, nil)
	l.Getenv = func(k string) string {
		if k == vault.EnvName(vault.APIToken) {
			return testToken
		}
		return ""
	}
	// Port 1 is never a server; the lock file names it.
	writeLock(t, l.Workspace, map[string]any{"pid": 1, "port": 1})
	var out bytes.Buffer
	if WriteSteps(&out, RunCheck(context.Background(), l, nil)) {
		t.Fatal("check passed with no server")
	}
	contains(t, out.String(), "fail  health", "applysmith serve")
}

func TestServe_ExitsCleanlyWhenInputCloses(t *testing.T) {
	pr, pw := pipePair()
	done := make(chan error, 1)
	var stdout bytes.Buffer
	go func() {
		done <- Serve(context.Background(), New(newFakeAPI(t).client(), "v", nil), pr, nopCloser{&stdout})
	}()
	_ = pw.Close()
	if err := <-done; err != nil {
		t.Errorf("Serve = %v, want nil on closed input", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout holds %q before any request", stdout.String())
	}
}

// Who answers on the port is asked before every call, without the token: the
// first call after another program took the port carries no token either.
func TestLocate_TheTokenNeverGoesToAProgramThatTookThePortBetweenCalls(t *testing.T) {
	api := newFakeAPI(t)
	l, ws := testLocator(t, map[string]string{vault.EnvName(vault.APIToken): testToken})
	var gone atomic.Bool // another program answers on the port now
	api.handle = func(w http.ResponseWriter, r *http.Request) {
		switch health := r.URL.Path == "/api/health"; {
		case health && gone.Load():
			writeOK(w, healthOf("0123456789ab"))
		case health:
			writeOK(w, healthOf(workspace.PathKey(ws.Root)))
		default:
			writeOK(w, candidacyPage())
		}
	}
	writeLock(t, ws, map[string]any{"pid": 1, "port": portOf(t, api)})
	cs := connect(t, l.Client(), nil)
	structured(t, call(t, cs, "list_candidacies", map[string]any{}))
	gone.Store(true)
	seen := len(api.requests())
	if te := toolError(t, call(t, cs, "list_candidacies", map[string]any{})); te.Code != CodeServerNotRunning {
		t.Errorf("error = %+v", te)
	}
	later := api.requests()[seen:]
	if len(later) == 0 {
		t.Fatal("the call asked nothing")
	}
	for _, r := range later {
		if r.Auth != "" || r.Path != "/api/health" {
			t.Errorf("the token went to an unverified program: %+v", r)
		}
	}
}

// After a call that got no API answer the server may be gone and another
// program may hold its port: the next call asks who answers before the token
// is sent.
func TestLocate_AFailedCallAsksWhoAnswersBeforeTheTokenIsSentAgain(t *testing.T) {
	for name, fail := range map[string]func(w http.ResponseWriter){
		"the connection drops": func(w http.ResponseWriter) {
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				_ = conn.Close()
			}
		},
		"an answer that is not the API's": func(w http.ResponseWriter) { _, _ = w.Write([]byte("<html>hello</html>")) },
	} {
		t.Run(name, func(t *testing.T) {
			api := newFakeAPI(t)
			l, ws := testLocator(t, map[string]string{vault.EnvName(vault.APIToken): testToken})
			var gone atomic.Bool // another program answers on the port now
			api.handle = func(w http.ResponseWriter, r *http.Request) {
				switch health := r.URL.Path == "/api/health"; {
				case !gone.Load() && health:
					writeOK(w, healthOf(workspace.PathKey(ws.Root)))
				case !gone.Load():
					writeOK(w, candidacyPage())
				case health:
					writeOK(w, healthOf("0123456789ab"))
				default:
					fail(w)
				}
			}
			writeLock(t, ws, map[string]any{"pid": 1, "port": portOf(t, api)})
			cs := connect(t, l.Client(), nil)
			structured(t, call(t, cs, "list_candidacies", map[string]any{}))
			gone.Store(true)
			toolError(t, call(t, cs, "list_candidacies", map[string]any{}))
			seen := len(api.requests())
			if te := toolError(t, call(t, cs, "list_candidacies", map[string]any{})); te.Code != CodeServerNotRunning {
				t.Errorf("error = %+v", te)
			}
			later := api.requests()[seen:]
			if len(later) == 0 {
				t.Fatal("the call after the failure asked nothing")
			}
			for _, r := range later {
				if r.Auth != "" || r.Path != "/api/health" {
					t.Errorf("the token went to an unverified program: %+v", r)
				}
			}
		})
	}
}
