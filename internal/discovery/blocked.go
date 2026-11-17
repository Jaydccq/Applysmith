package discovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"applysmith/internal/workspace"
)

// BlockedCompany is one entry of the blocked-companies store.
type BlockedCompany struct {
	ID         int64
	Name       string
	Normalized string
	Reason     string
	AddedBy    string
	AddedAt    time.Time
}

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("discovery: not found")

// BlockCompany adds a company; adding one already blocked is a no-op.
func (r *Repo) BlockCompany(ctx context.Context, name, reason, by string) (BlockedCompany, error) {
	norm := NormalizeCompany(name)
	if norm == "" {
		return BlockedCompany{}, fmt.Errorf("%q names no employer", name)
	}
	err := r.s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO blocked_companies (name, normalized, reason, added_by, added_at) VALUES (?, ?, ?, ?, ?)`,
			strings.TrimSpace(name), norm, reason, by, r.stamp())
		return err
	})
	if err != nil {
		return BlockedCompany{}, err
	}
	list, err := r.queryBlocked(ctx, `WHERE normalized = ?`, norm)
	if err != nil || len(list) == 0 {
		return BlockedCompany{}, err
	}
	return list[0], nil
}

// UnblockCompany removes a company by name.
func (r *Repo) UnblockCompany(ctx context.Context, name string) error {
	norm := NormalizeCompany(name)
	return r.s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM blocked_companies WHERE normalized = ?`, norm)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: %q is not blocked", ErrNotFound, name)
		}
		return nil
	})
}

// BlockedCompanies lists the store.
func (r *Repo) BlockedCompanies(ctx context.Context) ([]BlockedCompany, error) {
	return r.queryBlocked(ctx, `ORDER BY normalized`)
}

func (r *Repo) queryBlocked(ctx context.Context, tail string, args ...any) ([]BlockedCompany, error) {
	rows, err := r.s.Read().QueryContext(ctx, `SELECT id, name, normalized, reason, added_by, added_at FROM blocked_companies `+tail, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BlockedCompany
	for rows.Next() {
		var b BlockedCompany
		var at string
		if err := rows.Scan(&b.ID, &b.Name, &b.Normalized, &b.Reason, &b.AddedBy, &at); err != nil {
			return nil, err
		}
		b.AddedAt, _ = time.Parse(timeLayout, at)
		out = append(out, b)
	}
	return out, rows.Err()
}

func (r *Repo) blockedNames(ctx context.Context) ([]string, error) {
	list, err := r.BlockedCompanies(ctx)
	out := make([]string, 0, len(list))
	for _, b := range list {
		out = append(out, b.Normalized)
	}
	return out, err
}

// BlockedRule returns the rule a scan drops a blocked employer with
// (Filters.CompanyBlocked) as the lists stand now: the blocked store over
// every name of the company, and the skip_companies of portals.yml where the
// repo reads the seeker's files (RankWith).
func (r *Repo) BlockedRule(ctx context.Context) (func(company string) bool, error) {
	blocked, err := r.blockedNames(ctx)
	if err != nil {
		return nil, err
	}
	var portals workspace.Portals
	if r.basis != nil {
		if _, p, err := r.basis(); err == nil {
			portals = p
		}
	}
	return NewFilters(portals, blocked).CompanyBlocked, nil
}
