package gmail

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/store"
)

// State keys of the gmail_state table.
const (
	keyMailbox        = "mailbox_address"
	keyConnectedAt    = "connected_at"
	keyLoginStatus    = "login_status"
	keyLoginError     = "login_error"
	keyLoginRefusedAt = "login_refused_at"
	// keyCursor is the UID mark up to which All Mail is read; keyBootstrapStart
	// is the mark a bootstrap in progress read before listing.
	keyCursor         = "cursor"
	keyCursorUpdated  = "cursor_updated_at"
	keyCursorResetAt  = "cursor_reset_at"
	keyResumeToken    = "resume_token"
	keyBootstrapStart = "bootstrap_mark"
)

const timeLayout = "2006-01-02T15:04:05.000Z"

func stamp(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseStamp(s string) (time.Time, bool) {
	t, err := time.Parse(timeLayout, s)
	return t, err == nil
}

// Repo is the Gmail view of the workspace database.
type Repo struct {
	st  *store.Store
	now func() time.Time
	// alive replaces the process check of the one-run claim (tests).
	alive func(pid int) bool
}

// NewRepo wraps a store. now defaults to the wall clock.
func NewRepo(st *store.Store, now func() time.Time) *Repo {
	if now == nil {
		now = time.Now
	}
	return &Repo{st: st, now: now}
}

// State returns one state value; ok is false when it was never set.
func (r *Repo) State(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := r.st.Read().QueryRowContext(ctx, `SELECT value FROM gmail_state WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

// stateOr returns a state value or the empty string.
func (r *Repo) stateOr(ctx context.Context, key string) string {
	v, _, _ := r.State(ctx, key)
	return v
}

// SetState stores values; an empty value deletes the key.
func (r *Repo) SetState(ctx context.Context, kv map[string]string) error {
	return r.st.Write(ctx, func(tx *sql.Tx) error { return setState(ctx, tx, kv) })
}

func setState(ctx context.Context, tx *sql.Tx, kv map[string]string) error {
	for k, v := range kv {
		var err error
		if v == "" {
			_, err = tx.ExecContext(ctx, `DELETE FROM gmail_state WHERE key = ?`, k)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO gmail_state (key, value) VALUES (?, ?)
				ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Demo reports whether the workspace is a demo.
func (r *Repo) Demo() bool { return r.st.Demo() }

// guardDemo refuses a Gmail job a demo workspace never runs (a sync, a
// digest): the demo gate's refusal is recorded and returned. Outside a demo it
// returns nil.
func (r *Repo) guardDemo(ctx context.Context) error {
	return r.st.GuardDemo(ctx, gates.DemoMail, IMAPHost)
}

// Mailbox is the authorized mailbox address, empty when none is connected.
func (r *Repo) Mailbox(ctx context.Context) string { return r.stateOr(ctx, keyMailbox) }

// Cursor is the stored UID mark and when it last covered All Mail.
func (r *Repo) Cursor(ctx context.Context) (id string, updated time.Time, ok bool) {
	id = r.stateOr(ctx, keyCursor)
	if id == "" {
		return "", time.Time{}, false
	}
	t, _ := parseStamp(r.stateOr(ctx, keyCursorUpdated))
	return id, t, true
}

// Audit appends an audit row.
func (r *Repo) Audit(ctx context.Context, actor, command, outcome string) error {
	return r.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO gmail_audit (at, actor, command, outcome) VALUES (?, ?, ?, ?)`,
			stamp(r.now()), actor, command, outcome)
		return err
	})
}
