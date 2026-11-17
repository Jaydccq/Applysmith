package llm

import (
	"time"
)

// Affinity, after magpie's gateway (gateway/affinity.go, its default "auto"
// mode): a conversation stays on the candidate that answered it last, so what
// that provider cached of the conversation is read again rather than sent
// afresh to another and paid for in full. Within a turn it always stays; on a
// new turn it stays while the last answer read or wrote enough of the cache and
// the cache has not gone cold. A provider that caches without reporting it
// (OpenAI's and DeepSeek's automatic prefix caches report reads only) is taken
// to have written the prompt it was sent. A resting candidate, or one the daily
// cap keeps out, is never kept. Sticks live in memory, like rests.

const (
	// cacheWorth is how many tokens read from or written to a cache make a
	// conversation worth keeping where it is across turns.
	cacheWorth = 1024
	// cacheCold is how long a provider keeps an unread prompt cached: five
	// minutes at Anthropic and OpenAI, the shortest there is.
	cacheCold = 5 * time.Minute
	// sticksKept bounds the conversations remembered; the oldest go first.
	sticksKept = 512
)

// stick is who answered a conversation last.
type stick struct {
	cand   string // provider/model
	turn   string
	at     time.Time
	cached int // tokens the answer read from or wrote to the cache
}

// stick records the candidate that answered a call.
func (r *Router) stick(call AgentCall, won *attempt) {
	if call.SessionID == "" {
		return
	}
	u := won.usage
	cached := u.CachedInput + u.CacheWrite
	if cached == 0 {
		cached = u.Input
	}
	now := r.now()
	r.restMu.Lock()
	defer r.restMu.Unlock()
	if _, ok := r.sticks[call.SessionID]; !ok && len(r.sticks) >= sticksKept {
		oldest, at := "", now
		for k, s := range r.sticks {
			if s.at.Before(at) {
				oldest, at = k, s.at
			}
		}
		delete(r.sticks, oldest)
	}
	r.sticks[call.SessionID] = stick{cand: won.cand.name(), turn: call.TurnID, at: now, cached: cached}
}

// affinityOrder moves the conversation's last answerer first when it is still
// worth keeping.
func (r *Router) affinityOrder(call AgentCall, cands []candidate) []candidate {
	r.restMu.Lock()
	st, ok := r.sticks[call.SessionID]
	r.restMu.Unlock()
	if !ok || call.SessionID == "" {
		return cands
	}
	at := -1
	for i, c := range cands {
		if c.name() == st.cand {
			at = i
		}
	}
	if at <= 0 {
		return cands
	}
	c := cands[at]
	if _, resting := r.restOf(c); resting || c.authOnly || (cands[0].downgraded && !c.downgraded) {
		return cands
	}
	if st.turn != call.TurnID && (st.cached < cacheWorth || r.now().Sub(st.at) > cacheCold) {
		return cands
	}
	c.kept = true
	out := append([]candidate{c}, cands[:at]...)
	return append(out, cands[at+1:]...)
}
