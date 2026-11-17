package gmail

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// Digest kinds and triggers.
const (
	DigestDaily  = "daily"
	DigestWeekly = "weekly"

	TriggerSlot  = "scheduled"
	TriggerHand  = "manual"
	TriggerAgent = "agent"
)

// Digest statuses.
const (
	digestRunning = "running"
	digestDone    = "done"
	digestFailed  = "failed"
)

// Item sections of a digest: the lanes of a daily digest and the two lists of
// a weekly review.
const (
	SectionWaitingOnYou = "waiting_on_you"
	SectionFollowUp     = "follow_up"
)

// DigestItem is one thread line of a digest.
type DigestItem struct {
	Key        string `json:"key"`
	Section    string `json:"section"`
	Company    string `json:"company"`
	Role       string `json:"role,omitempty"`
	Lane       string `json:"lane"`
	State      string `json:"state"`
	Owner      string `json:"owner"`
	NextAction string `json:"nextAction"`
	Due        string `json:"due,omitempty"`
	Line       string `json:"line"`
	DraftKind  string `json:"draftKind,omitempty"`
	// Draft is a reply draft the product never sends.
	Draft string `json:"draft,omitempty"`
}

// DigestBody is the stored content of a digest.
type DigestBody struct {
	Items []DigestItem `json:"items"`
	Muted []DigestItem `json:"muted,omitempty"`
	// Omitted counts the qualifying threads beyond the 30 listed.
	Omitted  int `json:"omitted"`
	Receipts int `json:"receipts"`
	// Weekly review only.
	Funnel       map[string]int `json:"funnel,omitempty"`
	MoreWaiting  int            `json:"moreWaitingOnYou,omitempty"`
	MoreFollowUp int            `json:"moreFollowUp,omitempty"`
	Narrative    string         `json:"narrative,omitempty"`
	Text         string         `json:"text"`
}

// Digest is one stored digest or weekly review.
type Digest struct {
	ID           int64
	Kind         string
	Trigger      string
	Slot         time.Time
	Status       string
	CreatedAt    time.Time
	WindowStart  time.Time
	WindowEnd    time.Time
	Method       string
	Headline     string
	Body         DigestBody
	Delivery     map[string]string
	Error        string
	StartsWindow bool
}

// Typed failures of the digest routes.
var (
	ErrNoDigest    = errors.New("gmail: no such digest")
	ErrNotInDigest = errors.New("gmail: the digest neither lists nor mutes that thread")
	ErrBusy        = errors.New("gmail: a digest is already being produced")
)

const digestColumns = `id, kind, trigger_kind, COALESCE(slot, ''), status, created_at, window_start, window_end, method, headline, body_json, delivery_json, error, starts_window`

func scanDigest(row interface{ Scan(...any) error }) (Digest, error) {
	var d Digest
	var slot, created, ws, we, body, delivery string
	var starts int
	if err := row.Scan(&d.ID, &d.Kind, &d.Trigger, &slot, &d.Status, &created, &ws, &we, &d.Method, &d.Headline, &body, &delivery, &d.Error, &starts); err != nil {
		return Digest{}, err
	}
	d.Slot, _ = parseStamp(slot)
	d.CreatedAt, _ = parseStamp(created)
	d.WindowStart, _ = parseStamp(ws)
	d.WindowEnd, _ = parseStamp(we)
	d.StartsWindow = starts == 1
	_ = json.Unmarshal([]byte(body), &d.Body)
	_ = json.Unmarshal([]byte(delivery), &d.Delivery)
	return d, nil
}

// Digest returns one digest.
func (r *Repo) Digest(ctx context.Context, id int64) (Digest, error) {
	d, err := scanDigest(r.st.Read().QueryRowContext(ctx, `SELECT `+digestColumns+` FROM gmail_digests WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Digest{}, ErrNoDigest
	}
	return d, err
}

// Digests lists digests, newest first.
func (r *Repo) Digests(ctx context.Context, limit int) ([]Digest, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.st.Read().QueryContext(ctx, `SELECT `+digestColumns+` FROM gmail_digests ORDER BY id DESC LIMIT ?`, min(limit, 200))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Digest
	for rows.Next() {
		d, err := scanDigest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// claimDigest writes the row of a digest before anything is produced. A slot
// already claimed returns ok false, whatever became of the earlier attempt, so
// a failed slot is never retried on every tick.
func (r *Repo) claimDigest(ctx context.Context, d Digest) (id int64, ok bool, err error) {
	var slot any
	if !d.Slot.IsZero() {
		slot = stamp(d.Slot)
	}
	err = r.st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO gmail_digests (kind, trigger_kind, slot, status, created_at, window_start, window_end)
			VALUES (?, ?, ?, 'running', ?, ?, ?)`, d.Kind, d.Trigger, slot, stamp(d.CreatedAt), stamp(d.WindowStart), stamp(d.WindowEnd))
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return nil
		}
		id, err = res.LastInsertId()
		ok = err == nil
		return err
	})
	return id, ok, err
}

// saveDigest stores the content and outcome of a digest.
func (r *Repo) saveDigest(ctx context.Context, d Digest) error {
	body, _ := json.Marshal(d.Body)
	delivery, _ := json.Marshal(d.Delivery)
	if d.Delivery == nil {
		delivery = []byte("{}")
	}
	return r.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE gmail_digests SET status = ?, method = ?, headline = ?, body_json = ?, delivery_json = ?, error = ?, starts_window = ? WHERE id = ?`,
			d.Status, orMethod(d.Method), d.Headline, string(body), string(delivery), d.Error, b2i(d.StartsWindow), d.ID)
		return err
	})
}

func orMethod(m string) string {
	if m == "" {
		return MethodDeterministic
	}
	return m
}

// previousWindowEnd is the end of the newest done digest that starts the next
// window.
func (r *Repo) previousWindowEnd(ctx context.Context) (time.Time, bool) {
	var s string
	if err := r.st.Read().QueryRowContext(ctx, `SELECT window_end FROM gmail_digests WHERE starts_window = 1 AND status = 'done' ORDER BY id DESC LIMIT 1`).Scan(&s); err != nil {
		return time.Time{}, false
	}
	return parseStamp(s)
}

// Feedback is a rating of one thread.
type Feedback struct {
	DigestID  int64
	ThreadKey string
	Verdict   string
	Company   string
	Lane      string
	State     string
	At        time.Time
}

// Verdicts.
const (
	VerdictUp    = "up"
	VerdictDown  = "down"
	VerdictClear = "clear"
)

// ThreadVerdicts returns the verdict in force per thread: the newest row, with
// a clear removing it.
func (r *Repo) ThreadVerdicts(ctx context.Context) (map[string]Feedback, error) {
	rows, err := r.st.Read().QueryContext(ctx, `SELECT f.digest_id, f.thread_key, f.verdict, f.company, f.lane, f.state, f.at
		FROM gmail_digest_feedback f WHERE f.id = (SELECT MAX(id) FROM gmail_digest_feedback g WHERE g.thread_key = f.thread_key)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Feedback{}
	for rows.Next() {
		var f Feedback
		var at string
		if err := rows.Scan(&f.DigestID, &f.ThreadKey, &f.Verdict, &f.Company, &f.Lane, &f.State, &at); err != nil {
			return nil, err
		}
		f.At, _ = parseStamp(at)
		if f.Verdict != VerdictClear {
			out[f.ThreadKey] = f
		}
	}
	return out, rows.Err()
}

// RecentRatings returns the n newest ratings, for the digest prompt.
func (r *Repo) RecentRatings(ctx context.Context, n int) ([]Feedback, error) {
	rows, err := r.st.Read().QueryContext(ctx, `SELECT digest_id, thread_key, verdict, company, lane, state, at FROM gmail_digest_feedback
		WHERE verdict <> 'clear' ORDER BY id DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Feedback
	for rows.Next() {
		var f Feedback
		var at string
		if err := rows.Scan(&f.DigestID, &f.ThreadKey, &f.Verdict, &f.Company, &f.Lane, &f.State, &at); err != nil {
			return nil, err
		}
		f.At, _ = parseStamp(at)
		out = append(out, f)
	}
	return out, rows.Err()
}

// Rate records a verdict for a thread of a digest. The digest must list or
// mute the thread; the lane and state judged are those the digest showed.
func (r *Repo) Rate(ctx context.Context, digestID int64, threadKey, verdict string) error {
	if !slices.Contains([]string{VerdictUp, VerdictDown, VerdictClear}, verdict) {
		return errors.New("gmail: verdict must be up, down or clear")
	}
	d, err := r.Digest(ctx, digestID)
	if err != nil {
		return err
	}
	var judged *DigestItem
	for _, list := range [][]DigestItem{d.Body.Items, d.Body.Muted} {
		for i := range list {
			if list[i].Key == threadKey {
				judged = &list[i]
			}
		}
	}
	if judged == nil {
		return ErrNotInDigest
	}
	return r.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO gmail_digest_feedback (digest_id, thread_key, verdict, company, lane, state, at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			digestID, threadKey, verdict, judged.Company, judged.Lane, judged.State, stamp(r.now()))
		return err
	})
}

// muted reports whether a thumbs-down still holds: the thread has the lane and
// state the user judged.
func muted(f Feedback, t Thread) bool {
	return f.Verdict == VerdictDown && f.Lane == t.View.Lane && f.State == t.View.State
}
