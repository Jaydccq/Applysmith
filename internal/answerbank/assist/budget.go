package assist

import (
	"strings"
	"sync"
	"time"
)

// Budget limits of the model-assisted lanes.
const (
	// MaxOptionCalls and MaxCompositions bound one match request.
	MaxOptionCalls  = 8
	MaxCompositions = 5
	// MaxRecognitions bounds the entry-recognition calls of one request.
	MaxRecognitions = 8
	// SessionCeiling bounds the model calls of one session, every lane together.
	SessionCeiling = 60
	// SessionIdle is the quiet time after which a session starts again.
	SessionIdle = 10 * time.Minute
	// maxSessions bounds the table of live sessions.
	maxSessions = 512
)

// SessionKey is one page host plus the company: a multi-tenant application host
// is shared by employers, so the host alone would let one company spend
// another's calls.
func SessionKey(host, company string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		host = "unknown"
	}
	slug := strings.Trim(nonAlnumRE.ReplaceAllString(strings.ToLower(company), "-"), "-")
	if slug == "" {
		return host
	}
	return host + "::" + slug
}

type session struct {
	count int
	last  time.Time
}

// Sessions counts the model calls of each session. It is in memory and per
// process on purpose: a session is a ten-minute idle window, so a restart
// starts a new one anyway. It is a soft guard against a page that re-scans
// without end.
type Sessions struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[string]*session
}

// NewSessions returns an empty table over an injected clock.
func NewSessions(now func() time.Time) *Sessions {
	return &Sessions{now: now, m: map[string]*session{}}
}

// fresh returns the live state of key, replacing one idle for longer than
// SessionIdle. A check on an expired key creates the fresh state.
func (s *Sessions) fresh(key string) *session {
	now := s.now()
	st := s.m[key]
	if st == nil || now.Sub(st.last) > SessionIdle {
		if st == nil && len(s.m) >= maxSessions {
			for k, v := range s.m {
				if now.Sub(v.last) > SessionIdle {
					delete(s.m, k)
				}
			}
			if len(s.m) >= maxSessions {
				clear(s.m)
			}
		}
		st = &session{last: now}
		s.m[key] = st
	}
	return st
}

// Take spends one call of the session and reports false when none is left. The
// check and the spend are one step, so concurrent requests cannot overshoot.
func (s *Sessions) Take(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.fresh(key)
	if st.count >= SessionCeiling {
		return false
	}
	st.count++
	st.last = s.now()
	return true
}

// requestBudget counts the calls of one match request.
type requestBudget struct {
	options, compositions, recognitions int
}
