package notify

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/store"
)

// noticesKept is how many notices the list keeps; older ones are deleted as
// newer ones arrive, and every one after DoneRetention (Policy.Prune).
const noticesKept = 100

// Desktop is the desktop channel. It has two outputs: the notification stream,
// which the desktop app turns into native notifications, and the Notices list
// of the dashboard's top bar (table notify_notices), which is passive: it never
// makes a sound or a banner. The sink is available only while a client (the
// desktop app) is subscribed to the stream; the policy then hands what it would
// have announced to the list instead, so nothing is delivered to nobody.
type Desktop struct {
	o DesktopOptions
}

// DesktopOptions wire the desktop channel.
type DesktopOptions struct {
	Store *store.Store
	Now   func() time.Time
	Log   *slog.Logger
	// Publish writes one event to the notification stream.
	Publish func(event string, payload any)
	// Attached reports whether a client is subscribed to the stream.
	Attached func() bool
	// Changed is called after the Notices list changed.
	Changed func()
}

// NewDesktop returns the desktop channel.
func NewDesktop(o DesktopOptions) *Desktop {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Changed == nil {
		o.Changed = func() {}
	}
	return &Desktop{o: o}
}

// Name implements Sink.
func (*Desktop) Name() string { return "desktop" }

// Available implements Sink: a client shows the stream.
func (d *Desktop) Available(context.Context) bool { return d.o.Attached != nil && d.o.Attached() }

// Announce implements Sink. An announcement with an identifier that is already
// shown replaces it.
func (d *Desktop) Announce(_ context.Context, a Announcement) (string, error) {
	d.o.Publish(wire.NotifyAnnounce, wire.NotificationAnnounce{ID: a.Handle(), Title: a.Title, Body: a.Body, Link: a.Link, Urgent: a.Urgent, Entry: a.EntryID != ""})
	return a.Handle(), nil
}

// Withdraw implements Sink. A superseded announcement is replaced by identity,
// so there is nothing to close.
func (d *Desktop) Withdraw(_ context.Context, a Announcement, ext string, why Why) error {
	if why == Superseded {
		return nil
	}
	d.o.Publish(wire.NotifyWithdraw, wire.NotificationWithdraw{ID: ext})
	return nil
}

// Notice implements Noticer: the announcement goes to the Notices list under
// its handle, replacing an earlier notice of the same entry. The same text
// again (a held item released while no client is attached) stays as it was,
// read or not.
func (d *Desktop) Notice(ctx context.Context, a Announcement) {
	d.add(ctx, sql.NullString{String: a.Handle(), Valid: true}, a.Title, a.Body, a.Link)
}

// Text adds a plain message to the Notices list, such as the confirmation of a
// pairing.
func (d *Desktop) Text(ctx context.Context, title, body, link string) {
	title, body = Compose(title, body)
	d.add(ctx, sql.NullString{}, title, body, link)
}

func (d *Desktop) add(ctx context.Context, handle sql.NullString, title, body, link string) {
	var n int64
	err := d.o.Store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO notify_notices (handle, title, body, link, created_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(handle) DO UPDATE SET title = excluded.title, body = excluded.body, link = excluded.link, created_at = excluded.created_at, read_at = NULL
			WHERE title <> excluded.title OR body <> excluded.body OR link <> excluded.link`, handle, title, body, link, ts(d.o.Now()))
		if err == nil {
			n, err = res.RowsAffected()
		}
		if err != nil || n == 0 {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM notify_notices WHERE id NOT IN (SELECT id FROM notify_notices ORDER BY id DESC LIMIT ?)`, noticesKept)
		return err
	})
	if err != nil {
		d.o.Log.Error("a notice could not be stored", "title", title, "error", err.Error())
	} else if n > 0 {
		d.o.Changed()
	}
}

// Forget implements Noticer: the entry was handled, so its notice is no longer
// news. Every change of an entry asks, so the write is made only when there is
// a notice to remove.
func (d *Desktop) Forget(ctx context.Context, entryID string) {
	var n int
	if d.o.Store.Read().QueryRowContext(ctx, `SELECT count(*) FROM notify_notices WHERE handle = ?`, entryID).Scan(&n); n == 0 {
		return
	}
	err := d.o.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM notify_notices WHERE handle = ?`, entryID)
		return err
	})
	if err != nil {
		d.o.Log.Warn("a handled notice could not be removed", "entry", entryID, "error", err.Error())
		return
	}
	d.o.Changed()
}

// Seen implements Noticer: the announcement under handle was shown as a
// desktop notification (a held item released after quiet hours, alone or in
// their summary), so its notice is no longer unread. Every delivery asks, so
// the write is made only when there is an unread notice to mark.
func (d *Desktop) Seen(ctx context.Context, handle string) {
	var n int
	if d.o.Store.Read().QueryRowContext(ctx, `SELECT count(*) FROM notify_notices WHERE handle = ? AND read_at IS NULL`, handle).Scan(&n); n == 0 {
		return
	}
	err := d.o.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE notify_notices SET read_at = ? WHERE handle = ?`, ts(d.o.Now()), handle)
		return err
	})
	if err != nil {
		d.o.Log.Warn("a shown notice could not be marked read", "handle", handle, "error", err.Error())
		return
	}
	d.o.Changed()
}

// Notices lists the notices, newest first, and counts the unread ones.
func (d *Desktop) Notices(ctx context.Context) (wire.NoticeList, error) {
	out := wire.NoticeList{Notices: []wire.Notice{}}
	rows, err := d.o.Store.Read().QueryContext(ctx, `SELECT id, title, body, link, created_at, read_at IS NOT NULL FROM notify_notices ORDER BY id DESC`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var n wire.Notice
		var created string
		if err := rows.Scan(&n.ID, &n.Title, &n.Body, &n.Link, &created, &n.Read); err != nil {
			return out, err
		}
		n.CreatedAt = wire.Time{Time: parseTS(created)}
		if !n.Read {
			out.Unread++
		}
		out.Notices = append(out.Notices, n)
	}
	return out, rows.Err()
}

// Read marks the notices as read: those named, or every one when ids is empty.
func (d *Desktop) Read(ctx context.Context, ids []int64) error {
	now := ts(d.o.Now())
	err := d.o.Store.Write(ctx, func(tx *sql.Tx) error {
		if len(ids) == 0 {
			_, err := tx.ExecContext(ctx, `UPDATE notify_notices SET read_at = ? WHERE read_at IS NULL`, now)
			return err
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE notify_notices SET read_at = ? WHERE id = ? AND read_at IS NULL`, now, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		d.o.Changed()
	}
	return err
}
