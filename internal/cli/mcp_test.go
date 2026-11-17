package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A real server, the real `applysmith mcp` process logic and the official SDK
// client, connected over the pipes a host would use.
func TestMCPServesTheRunningServerOverStdio(t *testing.T) {
	te := newTestEnv(t)
	te.startServe()

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	var errw syncBuffer
	e := te.env(outW, &errw)
	e.stdin = inR
	done := make(chan int, 1)
	go func() {
		done <- e.run(context.Background(), []string{"mcp", "--workspace", te.ws})
		_ = outW.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-host", Version: "0"}, nil).Connect(ctx, &mcp.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		t.Fatalf("connect: %v (stderr: %s)", err, errw.String())
	}
	if info := cs.InitializeResult().ServerInfo; info.Name != "applysmith" || info.Version != version {
		t.Errorf("server info = %+v", info)
	}
	list, err := cs.ListTools(ctx, nil)
	if err != nil || len(list.Tools) != 5 {
		t.Fatalf("tools = %v, %v", list, err)
	}

	text := func(name string, args map[string]any) (string, bool) {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return res.Content[0].(*mcp.TextContent).Text, res.IsError
	}
	if out, isErr := text("health", map[string]any{}); isErr || !strings.Contains(out, `"serverRunning":true`) || !strings.Contains(out, `"components"`) {
		t.Errorf("health = %s", out)
	}
	if out, isErr := text("list_candidacies", map[string]any{}); isErr || !strings.Contains(out, `"total":0`) {
		t.Errorf("list_candidacies = %s", out)
	}
	if out, isErr := text("get_application_by_url", map[string]any{"url": "https://jobs.example.com/none"}); isErr || !strings.Contains(out, `"found":false`) {
		t.Errorf("get_application_by_url = %s", out)
	}
	if out, isErr := text("apply_list_status", map[string]any{}); isErr || !strings.Contains(out, `"items":[]`) {
		t.Errorf("apply_list_status = %s", out)
	}
	if out, isErr := text("get_report", map[string]any{"number": 99999}); !isErr || !strings.Contains(out, `"code":"not_found"`) {
		t.Errorf("get_report = %s", out)
	}

	_ = cs.Close() // closes the server's standard input
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code %d after input closed (stderr: %s)", code, errw.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("mcp did not exit when its input closed")
	}
}

func TestMCPPrintConfig(t *testing.T) {
	te := newTestEnv(t)
	o := te.run(context.Background(), "mcp", "--print-config", "--workspace", te.ws)
	if o.code != 0 {
		t.Fatalf("%+v", o)
	}
	var doc struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(o.stdout), &doc); err != nil {
		t.Fatalf("not JSON: %q", o.stdout)
	}
	got := doc.MCPServers["applysmith"]
	if !filepath.IsAbs(got.Command) || len(got.Args) != 1 || got.Args[0] != "mcp" || got.Env["APPLYSMITH_WORKSPACE"] != te.ws {
		t.Errorf("config = %s", o.stdout)
	}

	te.vars["APPLYSMITH_WORKSPACE"] = ""
	def := filepath.Join(te.home, "Applysmith")
	if err := os.Mkdir(def, 0o700); err != nil {
		t.Fatal(err)
	}
	o = te.run(context.Background(), "mcp", "--print-config", "--workspace", def)
	if o.code != 0 || strings.Contains(o.stdout, "APPLYSMITH_WORKSPACE") || strings.Contains(strings.ToLower(o.stdout), "token") {
		t.Errorf("default workspace config = %+v", o)
	}
}

func TestMCPCheck(t *testing.T) {
	te := newTestEnv(t)
	if o := te.run(context.Background(), "mcp", "--check", "--workspace", te.ws); o.code != 1 || !strings.Contains(o.stdout, "fail") || !strings.Contains(o.stdout, "applysmith") {
		t.Errorf("no server: %+v", o)
	}
	te.startServe()
	o := te.run(context.Background(), "mcp", "--check", "--workspace", te.ws)
	if o.code != 0 || strings.Contains(o.stdout, "fail") || strings.Count(o.stdout, "pass") != 4 {
		t.Errorf("server up: %+v", o)
	}
}

func TestMCPRejectsBadArguments(t *testing.T) {
	te := newTestEnv(t)
	for _, args := range [][]string{{"mcp", "extra"}, {"mcp", "--check", "--print-config"}, {"mcp", "--bogus"}} {
		if o := te.run(context.Background(), args...); o.code != 2 {
			t.Errorf("%v: %+v", args, o)
		}
	}
}

// The app binary is not called applysmith, so a client registered with the path it
// resolves to would start the app, not the CLI.
func TestMCPPrintConfig_NamesTheLinkWhenTheBinaryIsTheApp(t *testing.T) {
	dir := t.TempDir()
	// Separate folders: the default macOS volume does not tell Applysmith from applysmith.
	if err := os.MkdirAll(filepath.Join(dir, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(dir, "app", "Applysmith")
	if err := os.WriteFile(app, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "bin", "applysmith")
	if err := os.Symlink(app, link); err != nil {
		t.Fatal(err)
	}
	if got := commandPath(app, link); got != link {
		t.Errorf("started through the link: got %q, want %q", got, link)
	}
	if got := commandPath(link, link); got != link {
		t.Errorf("a binary called applysmith keeps its own path: got %q", got)
	}
	if got := commandPath(app, "no-such-command-name"); got != app {
		t.Errorf("an unfindable name falls back to the binary: got %q", got)
	}
}
