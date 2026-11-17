package gmail

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"applysmith/internal/gmail/classify"
	"applysmith/internal/gmail/lifecycle"
)

// AlertFreshness is how recent the mail behind an alert must be.
const AlertFreshness = 24 * time.Hour

type alertRow struct {
	state string
	seq   int64
}

func (r *Repo) alertStates(ctx context.Context) (map[string]alertRow, error) {
	rows, err := r.st.Read().QueryContext(ctx, `SELECT thread_id, state, seq FROM gmail_alert_states`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]alertRow{}
	for rows.Next() {
		var id string
		var a alertRow
		if err := rows.Scan(&id, &a.state, &a.seq); err != nil {
			return nil, err
		}
		out[id] = a
	}
	return out, rows.Err()
}

// alertState is the latest incoming event plus owner plus lane when the thread
// is in an alert state, and the empty string otherwise.
func alertState(t Thread) string {
	l := t.View.Latest
	if l.Tier != classify.TierActionable || t.View.LatestRule == lifecycle.ContinuityRule {
		return ""
	}
	switch l.Type {
	case classify.TypeAssessment, classify.TypeInterview, classify.TypeReminder, classify.TypeReplyNeeded, classify.TypeActionNeeded:
		if t.View.Lane != lifecycle.LaneDoNow && t.View.Lane != lifecycle.LanePrepare {
			return ""
		}
	case classify.TypeOffer:
	default:
		return ""
	}
	return l.Type + "|" + t.View.Owner + "|" + t.View.Lane
}

var alertWhat = map[string]string{
	classify.TypeAssessment:   "online assessment",
	classify.TypeInterview:    "interview scheduled",
	classify.TypeReminder:     "interview reminder",
	classify.TypeReplyNeeded:  "reply needed",
	classify.TypeActionNeeded: "action required",
	classify.TypeOffer:        "offer received",
}

func employerName(s string) string {
	if s == "" || strings.EqualFold(s, classify.UnknownCompany) {
		return "Job mail"
	}
	return s
}

func roleName(s string) string {
	if s == "" || strings.EqualFold(s, classify.UnknownRole) {
		return ""
	}
	return s
}

// composeAlert words an alert. An offer states the outcome only.
func composeAlert(t Thread, seq int64) Alert {
	l := t.View.Latest
	a := Alert{MessageID: fmt.Sprintf("%s@%d", t.ID, seq), Title: employerName(t.Employer) + ": " + alertWhat[l.Type], Link: ThreadLink(t.ID)}
	var parts []string
	if r := roleName(t.Role); r != "" {
		parts = append(parts, r+".")
	}
	if l.Type != classify.TypeOffer && !l.Due.IsZero() {
		// A date alone is that date, not its UTC midnight in the local zone.
		parts = append(parts, "Due "+DueWords(l.Due, time.Local)+".")
	}
	a.Body = strings.Join(parts, " ")
	if l.Type == classify.TypeInterview || l.Type == classify.TypeReminder {
		a.InterviewStart = l.Due
	}
	return a
}

// EvaluateAlerts raises the instant alerts of threads that newly entered an
// alert state. States are written before anything is delivered, so a crash
// never alerts twice. Nothing alerts until a sync has finished; the first
// evaluation after that records the states and raises none; while alerts are
// off the states are still recorded. Each delivery fails on its own.
func (s *Stack) EvaluateAlerts(ctx context.Context) (int, error) {
	r := s.Repo
	if _, _, ok := r.Cursor(ctx); !ok {
		return 0, nil
	}
	now := r.now()
	threads, err := r.Threads(ctx, now)
	if err != nil {
		return 0, err
	}
	known, err := r.alertStates(ctx)
	if err != nil {
		return 0, err
	}
	seeded := r.stateOr(ctx, keyAlertsSeed) != ""
	on := r.Switches(ctx).Alerts
	type moved struct {
		thread, state string
		seq           int64
	}
	var changed []moved
	var raise []Alert
	for _, t := range threads {
		cur := alertState(t)
		prev, had := known[t.ID]
		if had && prev.state == cur || !had && cur == "" && seeded {
			continue
		}
		seq := prev.seq + 1
		changed = append(changed, moved{t.ID, cur, seq})
		fresh := now.Sub(t.View.Latest.Received) <= AlertFreshness
		if seeded && on && cur != "" && fresh {
			raise = append(raise, composeAlert(t, seq))
		}
	}
	if len(changed) == 0 && seeded {
		// Most ticks find every thread where it was: nothing is written.
		return 0, nil
	}
	err = r.st.Write(ctx, func(tx *sql.Tx) error {
		for _, c := range changed {
			if _, err := tx.ExecContext(ctx, `INSERT INTO gmail_alert_states (thread_id, state, seq, at) VALUES (?, ?, ?, ?)
				ON CONFLICT(thread_id) DO UPDATE SET state = excluded.state, seq = excluded.seq, at = excluded.at`, c.thread, c.state, c.seq, stamp(now)); err != nil {
				return err
			}
		}
		return setState(ctx, tx, map[string]string{keyAlertsSeed: "1"})
	})
	if err != nil {
		return 0, err
	}
	var errs []error
	sent := 0
	for _, a := range raise {
		if err := s.RaiseAlert(ctx, a); err != nil {
			errs = append(errs, err)
			continue
		}
		sent++
	}
	return sent, errors.Join(errs...)
}
