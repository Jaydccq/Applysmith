package cli

import (
	"context"
	"fmt"

	"applysmith/internal/launch"
	"applysmith/internal/workspace"
)

// serve runs the server until ctx is cancelled.
func (e *env) serve(ctx context.Context, args []string) int {
	port, err := e.defaultPort()
	if err != nil {
		return e.fail("serve", err)
	}
	fs := e.flags("serve")
	wsFlag := fs.String("workspace", "", "workspace directory")
	host := fs.String("host", "127.0.0.1", "loopback address to listen on")
	fs.IntVar(&port, "port", port, "port to listen on, 0 for any free port")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(e.stderr, "applysmith serve: unexpected argument %q\n", fs.Arg(0))
		return 2
	}

	root, err := e.workspacePath(*wsFlag)
	if err != nil {
		return e.fail("serve", err)
	}
	ws := workspace.Open(root)
	assets, err := e.dashboardAssets()
	if err != nil {
		return e.fail("serve", err)
	}
	srv, err := launch.Start(ctx, launch.Options{
		Workspace: ws, Backend: e.backend, Getenv: e.getenv, Version: version, BuildID: launch.BuildID(),
		Host: *host, Port: port, Assets: assets, RemoteTunnel: e.remoteTunnel, RemoteEvery: e.remoteEvery,
	})
	if err != nil {
		return e.fail("serve", err)
	}
	fmt.Fprintf(e.stdout, "Applysmith server for %s listening on %s\n", ws.Root, srv.Origin())
	open, err := srv.LaunchURL()
	if err != nil {
		_ = srv.Shutdown(context.Background())
		return e.fail("serve", err)
	}
	fmt.Fprintf(e.stdout, "Open the dashboard: %s\nThe link works once and expires after two minutes; `applysmith open` prints a new one.\n", open)

	<-ctx.Done()
	if err := srv.Shutdown(context.Background()); err != nil {
		return e.fail("serve", err)
	}
	return 0
}
