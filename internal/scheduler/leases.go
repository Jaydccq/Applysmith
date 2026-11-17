package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"slices"
	"sync"
	"time"

	"applysmith/internal/store"
)

// leaseTTL is how long a lease lives without a renewal. Every tick renews the
// leases of active local holders, including after a clock gap. A different
// scheduler may take over an expired lease after this instance stops ticking;
// the exclusive workspace lock prevents two live schedulers in production.
const leaseTTL = 5 * time.Minute

// leaseSet is the resource_leases table seen from one scheduler.
type leaseSet struct {
	st       *store.Store
	now      func() time.Time
	instance string
	pid      int
	mu       sync.Mutex // serializes local holder lifetime with table changes
	active   map[string]bool
}

func newLeaseSet(st *store.Store, now func() time.Time, instance string) *leaseSet {
	return &leaseSet{st: st, now: now, instance: instance, pid: os.Getpid(), active: make(map[string]bool)}
}

// busyLease names a lease that cannot be taken and who holds it.
type busyLease struct {
	Lease  Lease
	Holder string
}

func (b busyLease) String() string { return fmt.Sprintf("lease %s is held by %s", b.Lease, b.Holder) }

// free reports the first wanted lease that has a live holder.
func (l *leaseSet) free(ctx context.Context, want []Lease) (*busyLease, error) {
	if len(want) == 0 {
		return nil, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := ts(l.now())
	rows, err := l.st.Read().QueryContext(ctx, `SELECT name, holder, owner_instance, expires_at FROM resource_leases`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, holder, owner, expires string
		if err := rows.Scan(&name, &holder, &owner, &expires); err != nil {
			return nil, err
		}
		if slices.Contains(want, Lease(name)) && (expires > now || owner == l.instance && l.active[holder]) {
			return &busyLease{Lease: Lease(name), Holder: holder}, nil
		}
	}
	return nil, rows.Err()
}

// acquire takes every wanted lease or none. Expired leases can be taken
// over unless their holder is still active in this instance.
func (l *leaseSet) acquire(ctx context.Context, holder string, want []Lease) (*busyLease, error) {
	if len(want) == 0 {
		return nil, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active[holder] {
		return nil, fmt.Errorf("lease holder %q is already active", holder)
	}
	var busy *busyLease
	err := l.st.Write(ctx, func(tx *sql.Tx) error {
		now := ts(l.now())
		// A reusable timer label may have a stale row after a failed release.
		// It is inactive here; do not attach that old resource to the new work.
		if _, err := tx.ExecContext(ctx, `DELETE FROM resource_leases WHERE holder = ? AND owner_instance = ?`, holder, l.instance); err != nil {
			return err
		}
		if err := l.renewActive(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM resource_leases WHERE expires_at <= ?`, now); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT name, holder FROM resource_leases`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var name, h string
			if err := rows.Scan(&name, &h); err != nil {
				_ = rows.Close()
				return err
			}
			if busy == nil && slices.Contains(want, Lease(name)) {
				busy = &busyLease{Lease: Lease(name), Holder: h}
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil || busy != nil {
			return err
		}
		expires := ts(l.now().Add(leaseTTL))
		for _, name := range want {
			if _, err := tx.ExecContext(ctx, `INSERT INTO resource_leases (name, holder, owner_pid, owner_instance, acquired_at, expires_at)
				VALUES (?, ?, ?, ?, ?, ?)`, string(name), holder, l.pid, l.instance, now, expires); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil && busy == nil {
		l.active[holder] = true
	}
	return busy, err
}

// release drops the leases a holder took, whatever the outcome of its job.
func (l *leaseSet) release(ctx context.Context, holder string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	// A failed delete must still stop renewal of this finished holder.
	delete(l.active, holder)
	return l.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM resource_leases WHERE holder = ? AND owner_instance = ?`, holder, l.instance)
		return err
	})
}

// renew extends only leases whose local holder has not released them.
func (l *leaseSet) renew(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.st.Write(ctx, func(tx *sql.Tx) error { return l.renewActive(ctx, tx) })
}

// renewActive runs with mu held, before expired rows are swept. A suspended
// process can resume with live jobs or timer workers beyond the stored TTL.
func (l *leaseSet) renewActive(ctx context.Context, tx *sql.Tx) error {
	expires := ts(l.now().Add(leaseTTL))
	for holder := range l.active {
		if _, err := tx.ExecContext(ctx, `UPDATE resource_leases SET expires_at = ? WHERE owner_instance = ? AND holder = ?`,
			expires, l.instance, holder); err != nil {
			return err
		}
	}
	return nil
}

// sweep deletes expired leases and, when all is set, every lease this
// scheduler holds (graceful stop).
func (l *leaseSet) sweep(ctx context.Context, all bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if all {
		clear(l.active)
	}
	return l.st.Write(ctx, func(tx *sql.Tx) error {
		if err := l.renewActive(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM resource_leases WHERE expires_at <= ?`, ts(l.now())); err != nil {
			return err
		}
		if all {
			_, err := tx.ExecContext(ctx, `DELETE FROM resource_leases WHERE owner_instance = ?`, l.instance)
			return err
		}
		return nil
	})
}

// TryLeases takes leases for work that runs beside the job queue, such as a
// built-in timer's worker, so a scheduled job that needs the same resource
// does not start under it. ok is false while another holder has one; release
// drops them once. Reusing an active holder label is refused.
func (s *Scheduler) TryLeases(ctx context.Context, holder string, want ...Lease) (release func(), ok bool, err error) {
	if len(want) == 0 {
		return func() {}, true, nil
	}
	busy, err := s.leases.acquire(ctx, holder, want)
	if err != nil || busy != nil {
		return nil, false, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			if err := s.leases.release(context.WithoutCancel(ctx), holder); err != nil {
				s.log.Error("releasing leases failed", "holder", holder, "error", err.Error())
			}
		})
	}, true, nil
}
