package telegram

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// LeaseTTL is how far ahead the poller's lease expires. It is renewed every
// loop. A loop is about 26 seconds, and the longest one of a live poller is a
// long poll that ran to its deadline followed by the longest back-off, the cap
// and its quarter of jitter; the lease outlives that by 15 seconds.
const LeaseTTL = LongPollTimeout + max(ConflictCap, FailureCap)*5/4 + 15*time.Second

// ErrFenced means another poller took the lease; the holder must stop polling.
var ErrFenced = errors.New("telegram: the poller lease was taken by another process")

// Lease is the single-poller lease of one bot token.
type Lease struct {
	s     *State
	owner string
	gen   int64
}

// NewLease returns a lease for the named process.
func (s *State) NewLease(owner string) *Lease { return &Lease{s: s, owner: owner} }

// Generation is the fencing generation this lease holds.
func (l *Lease) Generation() int64 { return l.gen }

// Acquire takes the lease when it is free, expired or already ours. A new
// acquisition raises the fencing generation. ok is false while someone else
// holds a live lease.
func (l *Lease) Acquire(ctx context.Context) (ok bool, err error) {
	err = l.s.st.Write(ctx, func(tx *sql.Tx) error {
		var owner, exp string
		var gen int64
		qerr := tx.QueryRowContext(ctx, `SELECT owner, generation, expires_at FROM telegram_lease WHERE id = 1`).Scan(&owner, &gen, &exp)
		now := l.s.now()
		switch {
		case errors.Is(qerr, sql.ErrNoRows):
			gen = 1
		case qerr != nil:
			return qerr
		case owner == l.owner && now.Before(parseTS(exp)) && gen == l.gen:
			// Still ours: nothing new was acquired.
		case now.Before(parseTS(exp)) && owner != l.owner:
			return nil
		default:
			gen++
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO telegram_lease (id, owner, generation, expires_at) VALUES (1, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET owner = excluded.owner, generation = excluded.generation, expires_at = excluded.expires_at`,
			l.owner, gen, ts(now.Add(LeaseTTL))); err != nil {
			return err
		}
		l.gen, ok = gen, true
		return nil
	})
	return ok, err
}

// Renew extends the lease. A renewal with a stale generation fails with
// ErrFenced.
func (l *Lease) Renew(ctx context.Context) error {
	var n int64
	err := l.s.st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE telegram_lease SET expires_at = ? WHERE id = 1 AND owner = ? AND generation = ?`,
			ts(l.s.now().Add(LeaseTTL)), l.owner, l.gen)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrFenced
	}
	return nil
}

// Release gives the lease up when it is still ours.
func (l *Lease) Release(ctx context.Context) {
	_ = l.s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM telegram_lease WHERE id = 1 AND owner = ? AND generation = ?`, l.owner, l.gen)
		return err
	})
}

// LeaseHeld reports whether some process holds a live lease, which is how the
// command line tells a running poller from a stopped one.
func (s *State) LeaseHeld(ctx context.Context) bool {
	var exp string
	if err := s.st.Read().QueryRowContext(ctx, `SELECT expires_at FROM telegram_lease WHERE id = 1`).Scan(&exp); err != nil {
		return false
	}
	return s.now().Before(parseTS(exp))
}
