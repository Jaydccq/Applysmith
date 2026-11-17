// Package mcpserver is the read-only Model Context Protocol server behind
// `applysmith mcp`. It reaches data only through the running local API, with GET
// requests to a fixed list of routes.
package mcpserver

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// New builds the server: five read-only tools, the tools capability alone, and
// logging to the given logger (standard error in the product).
func New(api *Client, version string, log *slog.Logger) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "applysmith", Version: version}, &mcp.ServerOptions{
		Logger:       log,
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	})
	s.AddReceivingMiddleware(rewriteValidation)
	tools{api: api}.register(s)
	return s
}

// Serve runs the server on in and out until in closes. A closed input is a
// normal end.
func Serve(ctx context.Context, s *mcp.Server, in io.ReadCloser, out io.WriteCloser) error {
	err := s.Run(ctx, &mcp.IOTransport{Reader: in, Writer: out})
	if err == nil || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
