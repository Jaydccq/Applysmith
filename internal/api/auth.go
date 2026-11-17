package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"applysmith/internal/store"
	"applysmith/internal/vault"
)

const (
	// SessionCookie is the dashboard's session cookie.
	SessionCookie = "applysmith_session"
	// SessionKeyHeader carries the second secret of a cookie session. The
	// cookie goes to every server on the host (cookies ignore ports); the key
	// stays in the dashboard page's own storage, which does not.
	SessionKeyHeader = "X-Applysmith-Session-Key"
	// SessionKeyFragment names the key in the fragment of the redirect that
	// issues a session. A fragment never reaches a server or a log.
	SessionKeyFragment = "k"
	sessionTTL         = 30 * 24 * time.Hour
	maxSessions        = 20
	secretBytes        = 32
	maxPairing         = 5
	pairingTTL         = 120 * time.Second
	pairingLen         = 10
	pairingAlpha       = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	failedClaimCap     = 10
	// A token supplied from the environment is whatever the operator typed;
	// refusing a short one keeps the loopback API from being guessable.
	minTokenBytes = secretBytes
)

func randomSecret() (string, error) {
	var b [secretBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func digest(s string) [sha256.Size]byte { return sha256.Sum256([]byte(s)) }

// matches compares two fixed-length digests in constant time.
func matches(a, b [sha256.Size]byte) bool { return subtle.ConstantTimeCompare(a[:], b[:]) == 1 }

// sessions keeps only the SHA-256 of each live session value. The digests are
// also written to the workspace database once it is attached, so a session
// survives a server restart; the 30-day idle lifetime and the limit of twenty
// sessions hold across it.
type sessions struct {
	mu    sync.Mutex
	now   func() time.Time
	db    *store.Store // nil until attach; sessions are then memory-only
	items []*session   // least recently used first
}

type session struct {
	sum       [sha256.Size]byte // of the cookie value
	keySum    [sha256.Size]byte // of the key the page presents in a header
	used      time.Time
	persisted time.Time // the used time the database holds
}

// touchEvery is how often a session's use is written back: a request does not
// write the database, except for the first one in this long.
const touchEvery = time.Hour

func hexSum(d [sha256.Size]byte) string { return hex.EncodeToString(d[:]) }

func unhexSum(s string) ([sha256.Size]byte, bool) {
	var d [sha256.Size]byte
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != len(d) {
		return d, false
	}
	copy(d[:], b)
	return d, true
}

const usedLayout = time.RFC3339Nano

// attach loads the sessions of the database, dropping the expired and any
// beyond the limit, and keeps them there from now on.
func (s *sessions) attach(ctx context.Context, st *store.Store) error {
	rows, err := st.Read().QueryContext(ctx, `SELECT cookie_sha256, key_sha256, used_at FROM dashboard_sessions ORDER BY used_at, cookie_sha256`)
	if err != nil {
		return err
	}
	var loaded []*session
	var junk []string // rows that cannot be read as a session
	for rows.Next() {
		var cookie, key, used string
		if err := rows.Scan(&cookie, &key, &used); err != nil {
			rows.Close()
			return err
		}
		cs, ok1 := unhexSum(cookie)
		ks, ok2 := unhexSum(key)
		at, err := time.Parse(usedLayout, used)
		if !ok1 || !ok2 || err != nil {
			junk = append(junk, cookie)
			continue
		}
		loaded = append(loaded, &session{sum: cs, keySum: ks, used: at, persisted: at})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = loaded
	gone := s.dropExpiredLocked()
	for len(s.items) > maxSessions {
		gone = append(gone, s.items[0])
		s.items = s.items[1:]
	}
	s.db = st
	drop := junk
	for _, it := range gone {
		drop = append(drop, hexSum(it.sum))
	}
	return s.writeLocked(ctx, nil, drop)
}

// writeLocked stores one session (nil for none) and deletes the sessions with
// the given cookie digests, in one transaction. Without a database it does
// nothing.
func (s *sessions) writeLocked(ctx context.Context, put *session, drop []string) error {
	if s.db == nil || (put == nil && len(drop) == 0) {
		return nil
	}
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		for _, d := range drop {
			if _, err := tx.ExecContext(ctx, `DELETE FROM dashboard_sessions WHERE cookie_sha256 = ?`, d); err != nil {
				return err
			}
		}
		if put == nil {
			return nil
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO dashboard_sessions (cookie_sha256, key_sha256, used_at) VALUES (?, ?, ?)
			ON CONFLICT (cookie_sha256) DO UPDATE SET key_sha256 = excluded.key_sha256, used_at = excluded.used_at`,
			hexSum(put.sum), hexSum(put.keySum), put.used.UTC().Format(usedLayout))
		return err
	})
}

func cookieSums(gone []*session) []string {
	out := make([]string, 0, len(gone))
	for _, g := range gone {
		out = append(out, hexSum(g.sum))
	}
	return out
}

// issue creates a session and returns the cookie value and the key that goes
// with it.
func (s *sessions) issue() (value, key string, err error) {
	if value, err = randomSecret(); err != nil {
		return "", "", err
	}
	if key, err = randomSecret(); err != nil {
		return "", "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	gone := s.dropExpiredLocked()
	evict := 0
	if len(s.items) >= maxSessions {
		gone = append(gone, s.items[0])
		evict = 1
	}
	now := s.now()
	it := &session{sum: digest(value), keySum: digest(key), used: now, persisted: now}
	if err := s.writeLocked(context.Background(), it, cookieSums(gone)); err != nil {
		return "", "", fmt.Errorf("store the session: %w", err)
	}
	s.items = append(s.items[evict:], it)
	return value, key, nil
}

// rekey gives the live session behind the cookie value a new key and returns
// it. The old key stops working. It is how a browser that kept its cookie but
// lost the page's storage gets back in, without a second session.
func (s *sessions) rekey(value string) (key string, ok bool, err error) {
	if value == "" {
		return "", false, nil
	}
	if key, err = randomSecret(); err != nil {
		return "", false, err
	}
	sum := digest(value)
	s.mu.Lock()
	defer s.mu.Unlock()
	drop := cookieSums(s.dropExpiredLocked())
	for _, it := range s.items {
		if matches(sum, it.sum) {
			next := *it
			next.keySum, next.used, next.persisted = digest(key), s.now(), s.now()
			if err := s.writeLocked(context.Background(), &next, drop); err != nil {
				return "", false, fmt.Errorf("store the session: %w", err)
			}
			*it = next
			return key, true, nil
		}
	}
	_ = s.writeLocked(context.Background(), nil, drop)
	return "", false, nil
}

// live reports whether the cookie value belongs to a live session, without
// asking for the key: the dashboard document itself is a navigation, which
// cannot carry a header.
func (s *sessions) live(value string) bool { return s.check(value, "", false) }

// valid reports whether the cookie value and the key belong to the same live
// session. Both comparisons run over every session, in constant time.
func (s *sessions) valid(value, key string) bool { return s.check(value, key, true) }

func (s *sessions) check(value, key string, needKey bool) bool {
	if value == "" || (needKey && key == "") {
		return false
	}
	sum, keySum := digest(value), digest(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	drop := cookieSums(s.dropExpiredLocked())
	found := -1
	for i, it := range s.items {
		if matches(sum, it.sum) && (!needKey || matches(keySum, it.keySum)) {
			found = i
		}
	}
	var put *session
	if found >= 0 {
		it := s.items[found]
		it.used = s.now()
		s.items = append(append(s.items[:found:found], s.items[found+1:]...), it)
		if it.used.Sub(it.persisted) >= touchEvery {
			put = it
		}
	}
	// The database bookkeeping is best effort: a failed write leaves the row as
	// it was and a later use tries again.
	if err := s.writeLocked(context.Background(), put, drop); err == nil && put != nil {
		put.persisted = put.used
	}
	return found >= 0
}

// dropExpiredLocked forgets the sessions idle for the lifetime and returns
// them, so the caller can delete their rows.
func (s *sessions) dropExpiredLocked() (gone []*session) {
	cutoff := s.now().Add(-sessionTTL)
	kept := s.items[:0]
	for _, it := range s.items {
		if it.used.After(cutoff) {
			kept = append(kept, it)
		} else {
			gone = append(gone, it)
		}
	}
	s.items = kept
	return gone
}

// tokenGuard holds the digest of the current bearer token. The value itself
// stays in the vault.
type tokenGuard struct {
	sum atomic.Pointer[[sha256.Size]byte]
}

func (g *tokenGuard) set(token string) {
	d := digest(token)
	g.sum.Store(&d)
}

func (g *tokenGuard) valid(token string) bool {
	cur := g.sum.Load()
	if cur == nil || token == "" {
		return false
	}
	return matches(digest(token), *cur)
}

// bearerFrom extracts the token of an Authorization header. A header with
// another scheme is a credential that this server cannot accept.
func bearerFrom(r *http.Request) (token string, present bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	scheme, rest, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", true
	}
	return strings.TrimSpace(rest), true
}

// loadToken reads the API token from the vault (environment first) or creates
// it on first start. A token that exists but cannot be read is an error:
// replacing it silently would lock out every paired client.
func loadToken(v *vault.Vault) (string, error) {
	tok, err := v.Get(vault.APIToken)
	switch {
	case err == nil:
		if len(tok) < minTokenBytes {
			return "", fmt.Errorf("the API token is shorter than %d bytes; set a longer one (a random 32-byte value, base64url encoded, is what the server creates)", minTokenBytes)
		}
		return tok, nil
	case vault.IsCode(err, vault.CodeNotFound):
		tok, err = randomSecret()
		if err != nil {
			return "", err
		}
		if _, err := v.Set(vault.APIToken, tok); err != nil {
			return "", err
		}
		return tok, nil
	}
	return "", err
}

// ErrEnvManaged is returned when the token comes from the environment, which
// only its owner can change.
var ErrEnvManaged = errors.New("the API token is supplied by the environment")

// RotateStoredToken replaces the API token in the vault and returns the new
// value. A running server keeps checking the old token until it is told; use
// Server.RotateToken for that.
func RotateStoredToken(v *vault.Vault) (string, error) {
	if src, err := v.Test(vault.APIToken); err == nil && src == vault.SourceEnv {
		return "", ErrEnvManaged
	}
	tok, err := randomSecret()
	if err != nil {
		return "", err
	}
	if _, err := v.Set(vault.APIToken, tok); err != nil {
		return "", err
	}
	return tok, nil
}

// pairing holds the outstanding one-time codes.
type pairing struct {
	mu     sync.Mutex
	now    func() time.Time
	codes  []pairingCode
	failed int // wrong claims since a code was last issued or claimed
}

type pairingCode struct {
	sum     [sha256.Size]byte
	expires time.Time
}

func newPairingCode() (string, error) {
	var raw [pairingLen]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	out := make([]byte, pairingLen)
	for i, b := range raw {
		out[i] = pairingAlpha[int(b)&31]
	}
	return string(out), nil
}

func (p *pairing) create() (string, time.Time, error) {
	code, err := newPairingCode()
	if err != nil {
		return "", time.Time{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dropExpiredLocked()
	if len(p.codes) >= maxPairing {
		p.codes = p.codes[1:]
	}
	exp := p.now().Add(pairingTTL)
	p.codes = append(p.codes, pairingCode{sum: digest(code), expires: exp})
	p.failed = 0
	return code, exp, nil
}

// claim consumes the code if it is live. Enough failures discard every
// outstanding code, so guessing cannot continue against a code that exists.
// The failures counted are those since a code was last issued or claimed.
func (p *pairing) claim(code string) bool {
	sum := digest(strings.ToUpper(strings.TrimSpace(code)))
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dropExpiredLocked()
	found := -1
	for i, c := range p.codes {
		if matches(sum, c.sum) {
			found = i
		}
	}
	if found >= 0 {
		p.codes = append(p.codes[:found:found], p.codes[found+1:]...)
		p.failed = 0
		return true
	}
	p.failed++
	if p.failed >= failedClaimCap {
		p.codes, p.failed = nil, 0
	}
	return false
}

func (p *pairing) dropExpiredLocked() {
	kept := p.codes[:0]
	for _, c := range p.codes {
		if c.expires.After(p.now()) {
			kept = append(kept, c)
		}
	}
	p.codes = kept
}
