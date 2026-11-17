package mcpserver

import (
	"io"
	"net"
	"strconv"
	"testing"
)

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

func pipePair() (*io.PipeReader, *io.PipeWriter) { return io.Pipe() }

func portOf(t *testing.T, f *fakeAPI) int {
	t.Helper()
	_, port, err := net.SplitHostPort(f.srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	p, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
