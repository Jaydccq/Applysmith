package notify

import (
	"context"
	"errors"
	"strconv"
)

// Announcement is what a sink is asked to show.
type Announcement struct {
	ID      int64 // the outbox row
	EntryID string
	Kind    string
	Urgent  bool
	Title   string
	Body    string
	Link    string
}

// Handle is the identifier a sink shows the announcement under: the entry id
// for an entry, so a later announcement of the same entry replaces the earlier
// one, and a number of its own otherwise.
func (a Announcement) Handle() string {
	if a.EntryID != "" {
		return a.EntryID
	}
	return "n-" + strconv.FormatInt(a.ID, 10)
}

// Why an announcement is withdrawn.
type Why int

// Withdrawal reasons.
const (
	// Handled: the entry left the announced state, was acknowledged or deleted.
	Handled Why = iota
	// Superseded: a newer announcement of the same entry replaces it.
	Superseded
)

// ErrGone means the sink's destination no longer exists (a chat that was
// unpaired). The announcement is dropped for that sink and not retried.
var ErrGone = errors.New("notify: the destination is gone")

// Sink delivers announcements to one place.
type Sink interface {
	Name() string
	// Available reports whether the sink can deliver at all now. A sink that
	// is not available is skipped, not retried: Telegram without a pairing is
	// absent.
	Available(ctx context.Context) bool
	// Announce shows the announcement and returns the sink's handle for it.
	Announce(ctx context.Context, a Announcement) (ext string, err error)
	// Withdraw undoes an earlier announcement. ext is what Announce returned.
	Withdraw(ctx context.Context, a Announcement, ext string, why Why) error
}

// Noticer is implemented by a sink that also keeps a passive in-page list (the
// desktop channel's Notices). The policy puts there what quiet hours hold, at
// once, and what the sink could not show because it was unavailable; Forget
// removes an entry's notice once the entry was handled; Seen marks a notice
// read once the sink showed its announcement after all.
type Noticer interface {
	Notice(ctx context.Context, a Announcement)
	Forget(ctx context.Context, entryID string)
	Seen(ctx context.Context, handle string)
}
