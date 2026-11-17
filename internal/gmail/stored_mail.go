package gmail

import (
	"context"
	"database/sql"
	"errors"
)

// ErrNoMail means the message is not stored.
var ErrNoMail = errors.New("gmail: the message is not stored")

// StoredMail is the stored text of one message: the sender, the subject and
// the body while the retention rule keeps it, else the snippet.
type StoredMail struct {
	From, Subject, Body string
}

// StoredMail reads a message for a trigger. The body is third-party text; the
// caller filters it before a model sees it.
func (r *Repo) StoredMail(ctx context.Context, messageID string) (StoredMail, error) {
	var m StoredMail
	var snippet string
	err := r.st.Read().QueryRowContext(ctx, `SELECT from_header, subject, snippet FROM gmail_evidence WHERE message_id = ?`, messageID).Scan(&m.From, &m.Subject, &snippet)
	if errors.Is(err, sql.ErrNoRows) {
		return StoredMail{}, ErrNoMail
	}
	if err != nil {
		return StoredMail{}, err
	}
	err = r.st.Read().QueryRowContext(ctx, `SELECT body_text FROM gmail_bodies WHERE message_id = ?`, messageID).Scan(&m.Body)
	if errors.Is(err, sql.ErrNoRows) {
		m.Body = snippet
	} else if err != nil {
		return StoredMail{}, err
	}
	return m, nil
}
