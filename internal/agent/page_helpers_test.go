package agent

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"

	"applysmith/internal/agent/pageread"
)

const examplePublicIP = "93.184.216.34"

// scriptResolver answers each name from a sequence of answers, the last one
// repeating, and counts the lookups.
type scriptResolver struct {
	mu    sync.Mutex
	seq   map[string][][]netip.Addr
	calls map[string]int
}

func (r *scriptResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.calls == nil {
		r.calls = map[string]int{}
	}
	seq, ok := r.seq[host]
	if !ok {
		return nil, errors.New("no such host")
	}
	i := min(r.calls[host], len(seq)-1)
	r.calls[host]++
	return seq[i], nil
}

func ips(s ...string) []netip.Addr {
	var out []netip.Addr
	for _, x := range s {
		out = append(out, netip.MustParseAddr(x))
	}
	return out
}

// pageRig is a page reader whose names resolve from a script and whose
// connections all end at a local test server, with a record of the addresses
// it was asked to connect to.
type pageRig struct {
	fetcher  *pageread.Fetcher
	resolver *scriptResolver
	mu       sync.Mutex
	dialed   []string
}

func newPageRig(t *testing.T, h http.HandlerFunc, seq map[string][][]netip.Addr) *pageRig {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	r := &pageRig{resolver: &scriptResolver{seq: seq}}
	r.fetcher = pageread.New(pageread.Options{Resolver: r.resolver, Timeout: 5 * time.Second,
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			r.mu.Lock()
			r.dialed = append(r.dialed, addr)
			r.mu.Unlock()
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		}})
	return r
}

func (r *pageRig) connections() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.dialed...)
}

func servePage(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}
}
