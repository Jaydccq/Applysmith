package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	"applysmith/internal/mcpserver"
	"applysmith/internal/workspace"
)

const mcpUsage = `usage:
  applysmith mcp [--workspace <dir>]                run the read-only MCP server on standard input and output
  applysmith mcp --print-config [--workspace <dir>] print the JSON that registers the server in an MCP client
  applysmith mcp --check [--workspace <dir>]        check that a client could reach the running server
`

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// mcp runs the MCP server over standard input and output, or one of its two
// helpers. Standard output carries protocol messages only.
func (e *env) mcp(ctx context.Context, args []string) int {
	fs := e.flags("mcp")
	wsFlag := fs.String("workspace", "", "workspace directory")
	printConfig := fs.Bool("print-config", false, "print the client configuration")
	check := fs.Bool("check", false, "check the path from a client to the running server")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 || (*printConfig && *check) {
		fmt.Fprint(e.stderr, mcpUsage)
		return 2
	}
	root, wsErr := e.workspacePath(*wsFlag)
	switch {
	case *check:
		return e.mcpCheck(ctx, root, wsErr)
	case wsErr != nil:
		return e.fail("mcp", wsErr)
	case *printConfig:
		return e.mcpPrintConfig(root)
	}
	loc := e.mcpLocator(root)
	log := slog.New(slog.NewTextHandler(e.stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	in, ok := e.stdin.(io.ReadCloser)
	if !ok {
		in = io.NopCloser(e.stdin)
	}
	err := mcpserver.Serve(ctx, mcpserver.New(loc.Client(), version, log), in, nopWriteCloser{e.stdout})
	if err != nil && !errors.Is(err, context.Canceled) {
		return e.fail("mcp", err)
	}
	return 0
}

func (e *env) mcpLocator(root string) mcpserver.Locator {
	return mcpserver.Locator{Workspace: workspace.Open(root), Backend: e.backend, Getenv: e.getenv}
}

func (e *env) mcpCheck(ctx context.Context, root string, wsErr error) int {
	var loc mcpserver.Locator
	if wsErr == nil {
		loc = e.mcpLocator(root)
	}
	if mcpserver.WriteSteps(e.stdout, mcpserver.RunCheck(ctx, loc, wsErr)) {
		return 0
	}
	return 1
}

func (e *env) mcpPrintConfig(root string) int {
	exe, err := os.Executable()
	if err != nil {
		return e.fail("mcp", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = commandPath(resolved, os.Args[0])
	}
	// The default is what resolution gives with no flag and no environment.
	def, defErr := workspace.Resolve(workspace.ResolveOptions{Getenv: func(string) string { return "" }, HomeDir: e.homeDir, ConfigDir: e.configDir})
	out, err := mcpserver.ConfigSnippet(exe, root, defErr != nil || def != root)
	if err != nil {
		return e.fail("mcp", err)
	}
	fmt.Fprintln(e.stdout, string(out))
	return 0
}

// commandPath is what an MCP client must run to get this CLI. The desktop app
// answers as the CLI only under the name applysmith, so when the binary behind
// the process is not called applysmith the client is given the path the command
// was started by, not the app binary it resolves to.
func commandPath(resolved, argv0 string) string {
	if filepath.Base(resolved) == Name {
		return resolved
	}
	if p, err := exec.LookPath(argv0); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
	}
	return resolved
}
