package api

import (
	"math"
	"sync"
	"time"
)

// Limit classes. The health route has a bucket of its own.
type Limit string

const (
	LimitGeneral    Limit = "general"
	LimitEvaluation Limit = "evaluation"
	LimitPairing    Limit = "pairing"
	// LimitInput is the browser panel's input route: a person typing and
	// moving the pointer sends more requests than every other control.
	LimitInput Limit = "input"
	// LimitAsset is the static dashboard: a cold load fetches every page's
	// chunks at once (about 75 files), so the general bucket refused the third
	// load in a minute. It is keyed by source address, as a public route is.
	LimitAsset  Limit = "asset"
	limitHealth Limit = "health"
	// limitDashboardRead takes a general read of the dashboard session, which
	// only the loopback listener accepts: a person moving quickly between pages
	// asks for every card again (Home about 16 reads, Settings 20), which spent
	// the general budget in half a minute. The session's writes stay general.
	limitDashboardRead Limit = "dashboard-read"
)

const (
	rateWindow       = 60 * time.Second
	maxStreamsPerKey = 8
	maxStreamsTotal  = 64
	pruneAbove       = 4096
)

var limitCounts = map[Limit]int{
	LimitGeneral:    240,
	LimitEvaluation: 120,
	LimitPairing:    5,
	LimitInput:      3000,
	LimitAsset:      3000,
	limitHealth:     600,
	// A page a second for a minute, each as heavy as Settings.
	limitDashboardRead: 1200,
}

// limiter is a sliding-window counter per bucket key. The numbers are
// constants on purpose: no environment variable can loosen them.
type limiter struct {
	mu      sync.Mutex
	now     func() time.Time
	buckets map[string][]time.Time
}

func newLimiter(now func() time.Time) *limiter {
	return &limiter{now: now, buckets: map[string][]time.Time{}}
}

// allow records a request and returns true, or returns false with the whole
// seconds (at least one) until the oldest counted request leaves the window.
func (l *limiter) allow(key string, class Limit) (bool, int) {
	limit := limitCounts[class]
	now := l.now()
	cutoff := now.Add(-rateWindow)
	l.mu.Lock()
	defer l.mu.Unlock()
	id := string(class) + "|" + key
	hits := l.buckets[id]
	i := 0
	for i < len(hits) && !hits[i].After(cutoff) {
		i++
	}
	hits = hits[i:]
	if len(hits) >= limit {
		l.buckets[id] = hits
		wait := int(math.Ceil(hits[0].Add(rateWindow).Sub(now).Seconds()))
		return false, max(wait, 1)
	}
	l.buckets[id] = append(hits, now)
	if len(l.buckets) > pruneAbove {
		l.pruneLocked(cutoff)
	}
	return true, 0
}

func (l *limiter) pruneLocked(cutoff time.Time) {
	for k, hits := range l.buckets {
		if len(hits) == 0 || !hits[len(hits)-1].After(cutoff) {
			delete(l.buckets, k)
		}
	}
}
