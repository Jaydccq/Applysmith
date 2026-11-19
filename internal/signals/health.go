package signals

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"applysmith/internal/lazyregexp"
)

// Alarm reports a lane that produced documents before and none now.
type Alarm struct {
	Source         string    `json:"source"`
	Status         string    `json:"status"`
	LastProduced   time.Time `json:"lastProduced"`
	RequiredAction string    `json:"requiredAction,omitempty"`
	Retryable      bool      `json:"retryable"`
}

var (
	authRE    = lazyregexp.New(`(?i)\b(?:auth\w*|session|credentials?|keys?|unauthori[sz]ed|forbidden|sign[- ]?in|log[- ]?in|token)\b`)
	billingRE = lazyregexp.New(`(?i)\b(?:billing|credits?|payment|quota|top[- ]?up|insufficient funds)\b`)
)

// ClassifyStatus reads a status text: an authentication, session, credential
// or key problem, or a billing, credit, payment or quota problem, needs the
// user and is not retried.
func ClassifyStatus(source, status string) (action string, retryable bool) {
	switch {
	case billingRE.MatchString(status):
		return "Top up the account of " + source + " or raise its quota, then run a tick.", false
	case authRE.MatchString(status):
		return "Check the credentials or key of " + source + " and store a valid one, then run a tick.", false
	}
	return "", true
}

// LastProduced reads when a source last produced a document.
func (r *Repo) LastProduced(ctx context.Context, source string) (time.Time, error) {
	var at sql.NullString
	err := r.s.Read().QueryRowContext(ctx, `SELECT last_produced_at FROM signal_lanes WHERE source_id = ?`, source).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	return parseNullTS(at), err
}

// NoteLane records the result of a source in a tick.
func (r *Repo) NoteLane(ctx context.Context, source string, produced bool, status string) error {
	return r.s.Write(ctx, func(tx *sql.Tx) error {
		now := r.stamp()
		if produced {
			_, err := tx.ExecContext(ctx, `INSERT INTO signal_lanes (source_id, last_produced_at, last_status, updated_at) VALUES (?, ?, ?, ?)
				ON CONFLICT (source_id) DO UPDATE SET last_produced_at = excluded.last_produced_at, last_status = excluded.last_status, updated_at = excluded.updated_at`, source, now, status, now)
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO signal_lanes (source_id, last_status, updated_at) VALUES (?, ?, ?)
			ON CONFLICT (source_id) DO UPDATE SET last_status = excluded.last_status, updated_at = excluded.updated_at`, source, status, now)
		return err
	})
}

// laneAlarm decides one source's alarm before its result is recorded: it was
// configured and enabled, produced on an earlier tick, and produced nothing
// now. A source that never produced or is disabled raises none.
func (s *Service) laneAlarm(ctx context.Context, res SourceResult, enabled bool, action string) (*Alarm, error) {
	if !enabled || res.Docs > 0 {
		return nil, nil
	}
	last, err := s.repo.LastProduced(ctx, res.ID)
	if err != nil || last.IsZero() {
		return nil, err
	}
	status := strings.TrimSpace(res.Status)
	if res.Reason != "" {
		status = res.Reason
	}
	if strings.EqualFold(strings.TrimSuffix(status, "."), "ok") {
		// A clean run is not the cause: the alarm is that it returned nothing.
		status = "Ran without error and returned no documents."
	}
	a := &Alarm{Source: res.ID, Status: status, LastProduced: last, Retryable: true}
	if action != "" {
		a.RequiredAction, a.Retryable = action, false
	} else if act, retry := ClassifyStatus(res.ID, status); !retry {
		a.RequiredAction, a.Retryable = act, false
	}
	return a, nil
}
