package apply

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"sync"
	"time"

	"applysmith/internal/gmail"
	"applysmith/internal/gmail/classify"
	"applysmith/internal/store"
)

// windowsTTL is how long the read of the run records is reused: the classifier
// asks once per message, and a sync classifies many in a row.
const windowsTTL = 10 * time.Second

// RunWindows supplies the Gmail classifier with the sign-up windows of this
// product's own runs: the runs that created an applicant-tracking account,
// with their job and sign-up hosts and their start, end and account-creation
// times. Mail an employer's system sends inside such a window about that
// account is the product's own doing, not an employer reply.
type RunWindows struct {
	st  *store.Store
	now func() time.Time

	mu     sync.Mutex
	at     time.Time
	cached []classify.SignupWindow
}

var _ gmail.SignupWindowSource = (*RunWindows)(nil)

// NewRunWindows reads the run records of the workspace database.
func NewRunWindows(st *store.Store, now func() time.Time) *RunWindows {
	return &RunWindows{st: st, now: now}
}

// SignupWindows is gmail.SignupWindowSource. A failed read keeps the windows
// of the last good one.
func (w *RunWindows) SignupWindows() []classify.SignupWindow {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	if !w.at.IsZero() && now.Sub(w.at) < windowsTTL {
		return w.cached
	}
	runs, err := w.read(now)
	if err != nil {
		return w.cached
	}
	w.at, w.cached = now, classify.Windows(runs)
	return w.cached
}

func (w *RunWindows) read(now time.Time) ([]classify.SignupRun, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := w.st.Read().QueryContext(ctx, `SELECT r.started_at, r.finished_at, r.posting_url, r.signins_json,
		(SELECT max(a.at) FROM signup_attempts a WHERE a.run_id = r.run_id)
		FROM apply_runs r WHERE r.signins_json LIKE '%"created-account"%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []classify.SignupRun
	for rows.Next() {
		var started, posting, signins string
		var finished, attempt *string
		if err := rows.Scan(&started, &finished, &posting, &signins, &attempt); err != nil {
			return nil, err
		}
		var recs []SigninRecord
		if json.Unmarshal([]byte(signins), &recs) != nil {
			continue
		}
		start := parseStamp(started)
		end := now
		if finished != nil {
			end = parseStamp(*finished)
		}
		created := end
		if attempt != nil {
			created = parseStamp(*attempt)
		}
		jobHost := ""
		if u, err := url.Parse(strings.TrimSpace(posting)); err == nil {
			jobHost = u.Hostname()
		}
		for _, rec := range recs {
			if rec.Succeeded == "created-account" {
				runs = append(runs, classify.SignupRun{JobHost: jobHost, SignupHost: rec.Host, Start: start, End: end, AccountCreated: created})
			}
		}
	}
	return runs, rows.Err()
}

func parseStamp(s string) time.Time {
	t, err := time.Parse(stampLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
