package assist

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"applysmith/internal/store"
)

// CacheTTL is how long an identical question, option set, fact set and
// grounding mode reuses the earlier reply.
const CacheTTL = 24 * time.Hour

// maxCacheRows bounds the stored replies; the oldest go first.
const maxCacheRows = 5000

// cacheIO bounds one database access.
const cacheIO = 5 * time.Second

// replyKey hashes the parts that decide a reply, each followed by a NUL. The
// text of a part is never stored: only the hash is.
func replyKey(lane string, parts ...string) string {
	h := sha256.New()
	for _, p := range append([]string{lane}, parts...) {
		h.Write([]byte(strings.ToValidUTF8(p, "")))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

type cacheEntry struct {
	value string
	at    time.Time
}

type flight struct {
	done  chan struct{}
	value string
	ok    bool
}

// ReplyCache holds the earlier replies of every lane for 24 hours and lets
// concurrent identical calls share one model call. With a store the replies
// survive a restart; without one they live in memory. A failed call is never
// stored, nor is a reply a fallback model wrote: it serves its own call, and
// the next identical call asks the route's primary again.
type ReplyCache struct {
	mu      sync.Mutex
	now     func() time.Time
	st      *store.Store
	log     *slog.Logger
	m       map[string]cacheEntry
	flights map[string]*flight
}

// NewReplyCache returns a cache over an injected clock; st may be nil.
func NewReplyCache(now func() time.Time, st *store.Store, log *slog.Logger) *ReplyCache {
	return &ReplyCache{now: now, st: st, log: log, m: map[string]cacheEntry{}, flights: map[string]*flight{}}
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// live returns the in-memory reply of a key while it is within the TTL. The
// caller holds c.mu.
func (c *ReplyCache) live(key string) (string, bool) {
	e, ok := c.m[key]
	if ok && c.now().Sub(e.at) >= CacheTTL {
		delete(c.m, key)
		ok = false
	}
	return e.value, ok
}

// Get returns a live earlier reply.
func (c *ReplyCache) Get(ctx context.Context, key string) (string, bool) {
	c.mu.Lock()
	v, ok := c.live(key)
	c.mu.Unlock()
	if ok {
		return v, true
	}
	if c.st == nil {
		return "", false
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cacheIO)
	defer cancel()
	var reply, created string
	err := c.st.Read().QueryRowContext(rctx, `SELECT reply, created_at FROM answer_reply_cache WHERE key_hash = ? AND expires_at > ?`,
		key, stamp(c.now())).Scan(&reply, &created)
	if err != nil {
		if err != sql.ErrNoRows {
			c.log.Warn("assist: reply cache read failed", "err", err)
		}
		return "", false
	}
	at, _ := time.Parse(time.RFC3339Nano, created)
	c.mu.Lock()
	c.m[key] = cacheEntry{value: reply, at: at}
	c.mu.Unlock()
	return reply, true
}

// Put stores a reply and deletes the expired ones; the table is bounded.
func (c *ReplyCache) Put(ctx context.Context, key, lane, mode, value string) {
	now := c.now()
	c.mu.Lock()
	if len(c.m) >= maxCacheRows {
		clear(c.m)
	}
	c.m[key] = cacheEntry{value: value, at: now}
	c.mu.Unlock()
	if c.st == nil {
		return
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cacheIO)
	defer cancel()
	err := c.st.Write(wctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(wctx, `DELETE FROM answer_reply_cache WHERE expires_at <= ?`, stamp(now)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(wctx, `INSERT INTO answer_reply_cache (key_hash, lane, mode, reply, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (key_hash) DO UPDATE SET lane = excluded.lane, mode = excluded.mode, reply = excluded.reply,
			created_at = excluded.created_at, expires_at = excluded.expires_at`,
			key, lane, mode, value, stamp(now), stamp(now.Add(CacheTTL))); err != nil {
			return err
		}
		_, err := tx.ExecContext(wctx, `DELETE FROM answer_reply_cache WHERE key_hash IN
			(SELECT key_hash FROM answer_reply_cache ORDER BY created_at DESC LIMIT -1 OFFSET ?)`, maxCacheRows)
		return err
	})
	if err != nil {
		c.log.Warn("assist: reply cache write failed", "err", err)
	}
}

// Do returns the cached reply, joins an identical call in flight, or runs fn and
// stores its reply. fromCache is true when fn did not run for this caller. A
// failing fn stores nothing and every follower runs its own attempt. fn runs
// under a context that markFallback reads.
func (c *ReplyCache) Do(ctx context.Context, key, lane, mode string, fn func(context.Context) (string, error)) (value string, fromCache bool, err error) {
	for {
		if v, ok := c.Get(ctx, key); ok {
			return v, true, nil
		}
		c.mu.Lock()
		if f, ok := c.flights[key]; ok {
			c.mu.Unlock()
			<-f.done
			if f.ok {
				return f.value, true, nil
			}
			continue
		}
		// A leader that finished after the Get above (its database read can be
		// slow) has stored its reply and removed its flight: read the memory
		// again under the lock that guards the flight table before leading.
		if v, ok := c.live(key); ok {
			c.mu.Unlock()
			return v, true, nil
		}
		f := &flight{done: make(chan struct{})}
		c.flights[key] = f
		c.mu.Unlock()

		mark := &fallbackMark{}
		v, err := fn(context.WithValue(ctx, fallbackKey{}, mark))
		if err == nil && !mark.served.Load() {
			c.Put(ctx, key, lane, mode, v)
		}
		c.mu.Lock()
		delete(c.flights, key)
		c.mu.Unlock()
		if err == nil {
			f.value, f.ok = v, true
		}
		close(f.done)
		return v, false, err
	}
}

// fallbackMark records that a fallback model wrote a reply within one cached
// call.
type fallbackMark struct{ served atomic.Bool }

type fallbackKey struct{}

// markFallback notes that a fallback model wrote a reply of the cached call
// ctx belongs to, so that call's reply is not stored.
func markFallback(ctx context.Context) {
	if m, ok := ctx.Value(fallbackKey{}).(*fallbackMark); ok {
		m.served.Store(true)
	}
}
