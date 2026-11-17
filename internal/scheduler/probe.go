package scheduler

import (
	"context"
	"net"
	"time"
)

const (
	probeLimit = 3 * time.Second
	probeCache = 30 * time.Second
)

// probeHosts are well-known addresses. A connection attempt to an address, not
// a name lookup, because a resolver answers from its cache while offline.
var probeHosts = []string{"1.1.1.1:443", "9.9.9.9:443"}

type reachCache struct {
	at time.Time
	ok bool
}

// reachable reports whether the host can reach the internet, probing at most
// once per 30 seconds however many schedules fall due.
func (s *Scheduler) reachable(ctx context.Context) bool {
	now := s.clock.Now()
	s.mu.Lock()
	if !s.reach.at.IsZero() && now.Sub(s.reach.at) < probeCache {
		ok := s.reach.ok
		s.mu.Unlock()
		return ok
	}
	s.mu.Unlock()
	probe := s.o.Probe
	if probe == nil {
		probe = dialAny
	}
	ok := probe(ctx)
	s.mu.Lock()
	s.reach = reachCache{at: now, ok: ok}
	s.mu.Unlock()
	return ok
}

// dialAny succeeds when any probe host accepts a connection within the limit.
func dialAny(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, probeLimit)
	defer cancel()
	results := make(chan bool, len(probeHosts))
	for _, h := range probeHosts {
		go func() {
			var d net.Dialer
			c, err := d.DialContext(ctx, "tcp", h)
			if err == nil {
				_ = c.Close()
			}
			results <- err == nil
		}()
	}
	for range probeHosts {
		if <-results {
			return true
		}
	}
	return false
}
