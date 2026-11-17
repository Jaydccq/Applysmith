package gmail

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"applysmith/internal/gmail/classify"
	"applysmith/internal/gmail/lifecycle"
	"applysmith/internal/gmail/link"
)

// Inbox page sizes.
const (
	DefaultInboxLimit = 50
	MaxInboxLimit     = 200
)

// Thread is one Gmail thread as the action inbox reads it.
type Thread struct {
	ID string
	// Employer, Role and Subject come from the event that decided the state.
	Employer, Role, Subject string
	View                    lifecycle.View
	// LastIncoming is the newest incoming signal, LastUpdate the newest signal
	// of any direction.
	LastIncoming, LastUpdate time.Time
	// Link is the decision of the deciding event: auto, review or unlinked.
	Link        string
	CandidacyID int64
	// Newest is the newest message id of the thread, which fingerprints it.
	Newest  string
	Signals []SignalRow
	// Done says the user marked the thread done and no mail has come since, so
	// the view is the closed reading of MarkDone.
	Done bool
}

// ErrNotActionable refuses to mark done a thread that asks nothing of the user.
var ErrNotActionable = errors.New("gmail: that thread asks nothing of you")

// InboxKinds are the kinds a thread can be filtered by: the type of the event
// that decided it, or review when none did.
var InboxKinds = []string{classify.TypeReplyNeeded, classify.TypeActionNeeded, classify.TypeAssessment, classify.TypeInterview,
	classify.TypeReminder, classify.TypeOffer, classify.TypeRejection, classify.TypeApplied, classify.TypeReview}

// Kind is the type of the event that decided the thread; review when none did.
func (t Thread) Kind() string {
	if t.View.Latest.Type == "" {
		return classify.TypeReview
	}
	return t.View.Latest.Type
}

// newestMS is the received time of the newest message of the thread, of any
// direction, that can drive a lane: low-priority and suppressed mail asks
// nothing of the user, so it neither sets a done mark's cover nor lifts one.
func (t Thread) newestMS() int64 {
	var ms int64
	for _, s := range t.Signals {
		if s.Tier != classify.TierLow && s.Tier != classify.TierSuppressed {
			ms = max(ms, s.ReceivedMS)
		}
	}
	return ms
}

func (r *Repo) doneCovers(ctx context.Context) (map[string]int64, error) {
	rows, err := r.st.Read().QueryContext(ctx, `SELECT thread_id, covers_ms FROM gmail_thread_done`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id string
		var ms int64
		if err := rows.Scan(&id, &ms); err != nil {
			return nil, err
		}
		out[id] = ms
	}
	return out, rows.Err()
}

func dueOf(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func lifecycleSignals(rows []SignalRow) []lifecycle.Signal {
	out := make([]lifecycle.Signal, len(rows))
	for i, r := range rows {
		out[i] = lifecycle.Signal{MessageID: r.MessageID, Type: r.Type, Tier: r.Tier, Direction: r.Direction, RuleID: r.RuleID,
			Received: time.UnixMilli(r.ReceivedMS).UTC(), Due: dueOf(r.DueMS)}
	}
	return out
}

// Threads derives every thread that belongs in the inbox. Lane, owner and
// attention are computed here from the stored signals, never at the caller.
func (r *Repo) Threads(ctx context.Context, now time.Time) ([]Thread, error) {
	sigs, err := r.Signals(ctx)
	if err != nil {
		return nil, err
	}
	links, err := r.linkRows(ctx)
	if err != nil {
		return nil, err
	}
	done, err := r.doneCovers(ctx)
	if err != nil {
		return nil, err
	}
	by := map[string][]SignalRow{}
	var order []string
	for _, s := range sigs {
		if _, ok := by[s.ThreadID]; !ok {
			order = append(order, s.ThreadID)
		}
		by[s.ThreadID] = append(by[s.ThreadID], s)
	}
	var out []Thread
	for _, id := range order {
		rows := by[id]
		view, ok := lifecycle.Derive(lifecycleSignals(rows), now)
		if !ok {
			continue
		}
		t := Thread{ID: id, View: view, Signals: rows, Link: link.Unlinked}
		// A done mark covers the mail seen when it was made; a newer shown
		// message, of any direction, lifts it.
		if covers, ok := done[id]; ok && view.Actionable() && t.newestMS() <= covers {
			t.View, t.Done = view.Done(), true
		}
		for _, s := range rows {
			at := time.UnixMilli(s.ReceivedMS).UTC()
			if at.After(t.LastUpdate) {
				t.LastUpdate, t.Newest = at, s.MessageID
			}
			if s.Direction == classify.DirIncoming && s.Tier != classify.TierSuppressed && s.Tier != classify.TierLow && at.After(t.LastIncoming) {
				t.LastIncoming = at
			}
		}
		decider := deciding(rows, view)
		t.Employer, t.Role, t.Subject = decider.Employer, decider.Role, decider.Subject
		if l, ok := links[decider.MessageID]; ok {
			t.Link, t.CandidacyID = l.decision, l.candidacyID
		}
		out = append(out, t)
	}
	return out, nil
}

// deciding is the signal that decided the thread's state, or the newest
// incoming one when no event did.
func deciding(rows []SignalRow, v lifecycle.View) SignalRow {
	var newest SignalRow
	for _, s := range rows {
		if s.MessageID == v.Latest.MessageID && v.Latest.MessageID != "" {
			return s
		}
		if s.Direction == classify.DirIncoming && s.ReceivedMS >= newest.ReceivedMS {
			newest = s
		}
	}
	return newest
}

// InboxQuery filters and pages the inbox.
type InboxQuery struct {
	Lane, Owner, Link, Kind string
	// Text keeps the threads whose employer, role or subject holds every word
	// of it, whatever the case.
	Text string
	// Candidacy keeps the threads linked to that tracker row, by an automatic
	// or a confirmed link; zero keeps all.
	Candidacy int64
	Limit     int
	Cursor    string
}

func attentionRank(a string) int {
	switch a {
	case lifecycle.AttentionUrgent:
		return 0
	case lifecycle.AttentionAction:
		return 1
	case lifecycle.AttentionStale:
		return 2
	}
	return 3
}

type cursor struct {
	Att  int    `json:"a"`
	MS   int64  `json:"m"`
	Last string `json:"t"`
}

func encodeCursor(t Thread) string {
	raw, _ := json.Marshal(cursor{attentionRank(t.View.Attention), t.LastUpdate.UnixMilli(), t.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(s string) (cursor, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	var c cursor
	if err != nil || json.Unmarshal(raw, &c) != nil || c.Last == "" {
		return cursor{}, false
	}
	return c, true
}

// covers reports whether the thread sorts at or before the cursor position.
func (c cursor) covers(t Thread) bool {
	a, m := attentionRank(t.View.Attention), t.LastUpdate.UnixMilli()
	switch {
	case a != c.Att:
		return a < c.Att
	case m != c.MS:
		return m > c.MS
	}
	return t.ID <= c.Last
}

// Page is one page of the inbox.
type Page struct {
	Threads []Thread
	// Next is the opaque cursor of the next page, empty on the last.
	Next string
	// Total counts every thread the filters keep, whatever the page holds.
	Total int
}

// Inbox returns one page sorted by attention and then recency. An invalid
// cursor reads as none, and a page holds at most 200 threads.
func (r *Repo) Inbox(ctx context.Context, q InboxQuery, now time.Time) (Page, error) {
	all, err := r.Threads(ctx, now)
	if err != nil {
		return Page{}, err
	}
	var linked map[string][]int64
	if q.Candidacy != 0 {
		links, err := r.linkRows(ctx)
		if err == nil {
			linked, err = r.threadLinks(ctx, links)
		}
		if err != nil {
			return Page{}, err
		}
	}
	words := strings.Fields(strings.ToLower(q.Text))
	var rows []Thread
	for _, t := range all {
		if q.Lane != "" && t.View.Lane != q.Lane || q.Owner != "" && t.View.Owner != q.Owner || q.Link != "" && t.Link != q.Link ||
			q.Kind != "" && t.Kind() != q.Kind || q.Candidacy != 0 && !slices.Contains(linked[t.ID], q.Candidacy) {
			continue
		}
		hay := strings.ToLower(t.Employer + " " + t.Role + " " + t.Subject)
		if slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(hay, w) }) {
			continue
		}
		rows = append(rows, t)
	}
	slices.SortStableFunc(rows, func(a, b Thread) int {
		if d := attentionRank(a.View.Attention) - attentionRank(b.View.Attention); d != 0 {
			return d
		}
		if c := b.LastUpdate.Compare(a.LastUpdate); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	total := len(rows)
	if c, ok := decodeCursor(q.Cursor); ok {
		i := 0
		for i < len(rows) && c.covers(rows[i]) {
			i++
		}
		rows = rows[i:]
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultInboxLimit
	}
	limit = min(limit, MaxInboxLimit)
	page := Page{Total: total}
	if len(rows) > limit {
		page.Next = encodeCursor(rows[limit-1])
		rows = rows[:limit]
	}
	page.Threads = rows
	return page, nil
}

// RowMail is the mail state of one tracker row: its most pressing open linked
// thread and how many open threads it has.
type RowMail struct {
	Thread Thread
	Open   int
}

// MailByCandidacy reads every thread once and returns, for each tracker row
// with an open linked thread, the most pressing one: the first lane in digest
// order, then attention, then the newest. It reads through the same links and
// the same lifecycle as the inbox, so a done or dismissed thread (closed lane)
// and a receipt are not open.
func (r *Repo) MailByCandidacy(ctx context.Context, now time.Time) (map[int64]RowMail, error) {
	all, err := r.Threads(ctx, now)
	if err != nil {
		return nil, err
	}
	links, err := r.linkRows(ctx)
	if err != nil {
		return nil, err
	}
	linked, err := r.threadLinks(ctx, links)
	if err != nil {
		return nil, err
	}
	pressing := func(a, b Thread) bool {
		if d := lifecycle.LaneOrder(a.View.Lane) - lifecycle.LaneOrder(b.View.Lane); d != 0 {
			return d < 0
		}
		if d := attentionRank(a.View.Attention) - attentionRank(b.View.Attention); d != 0 {
			return d < 0
		}
		return a.LastUpdate.After(b.LastUpdate)
	}
	out := map[int64]RowMail{}
	for _, t := range all {
		if !slices.Contains(openLanes, t.View.Lane) {
			continue
		}
		for _, id := range linked[t.ID] {
			m := out[id]
			if m.Open == 0 || pressing(t, m.Thread) {
				m.Thread = t
			}
			m.Open++
			out[id] = m
		}
	}
	return out, nil
}

// openLanes are the lanes of a thread that still has something to do or wait
// for; closed and receipt-only threads are not open.
var openLanes = []string{lifecycle.LaneDoNow, lifecycle.LanePrepare, lifecycle.LaneReview, lifecycle.LaneWaiting}

func (r *Repo) threadOf(ctx context.Context, id string) (Thread, error) {
	all, err := r.Threads(ctx, r.now())
	if err != nil {
		return Thread{}, err
	}
	for _, t := range all {
		if t.ID == id {
			return t, nil
		}
	}
	return Thread{}, ErrNoThread
}

// MarkDone ends the action of a thread that asks one of the user: it leaves
// the do-now, prepare and review lanes for the closed lane and stays readable
// there. The mark covers the mail the thread holds now; a newer message lifts
// it. Marking a thread already done changes nothing.
func (r *Repo) MarkDone(ctx context.Context, id string) error {
	t, err := r.threadOf(ctx, id)
	if err != nil || t.Done {
		return err
	}
	if !t.View.Actionable() {
		return ErrNotActionable
	}
	return r.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO gmail_thread_done (thread_id, covers_ms, done_at) VALUES (?, ?, ?)
			ON CONFLICT(thread_id) DO UPDATE SET covers_ms = excluded.covers_ms, done_at = excluded.done_at`, id, t.newestMS(), stamp(r.now()))
		return err
	})
}

// ReopenDone undoes MarkDone. A thread that is not marked stays as it is.
func (r *Repo) ReopenDone(ctx context.Context, id string) error {
	return r.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM gmail_thread_done WHERE thread_id = ?`, id)
		return err
	})
}

// DismissThread marks the visible incoming signals of a thread as not job
// related, the mark RejectLink makes, and returns their ids so the undo can
// take exactly those marks off. A message that arrives afterwards is a new
// signal and brings the thread back.
func (r *Repo) DismissThread(ctx context.Context, id string) ([]string, error) {
	t, err := r.threadOf(ctx, id)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, s := range t.Signals {
		if s.Direction == classify.DirIncoming && s.Tier != classify.TierSuppressed && s.Tier != classify.TierLow {
			ids = append(ids, s.MessageID)
		}
	}
	// One time for the whole dismissal: RestoreThread finds its marks by it.
	at := stamp(r.now())
	err = r.st.Write(ctx, func(tx *sql.Tx) error {
		for _, m := range ids {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO gmail_link_rejections (message_id, rejected_at) VALUES (?, ?)`, m, at); err != nil {
				return err
			}
		}
		return nil
	})
	return ids, err
}

// RestoreThread undoes the newest DismissThread of a thread: it takes off the
// rejection marks of the thread's messages written at that time, as
// RestoreLink does one by one, and returns how many it took off.
func (r *Repo) RestoreThread(ctx context.Context, id string) (int, error) {
	var n int64
	err := r.st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM gmail_link_rejections
			WHERE message_id IN (SELECT message_id FROM gmail_evidence WHERE thread_id = ?)
			AND rejected_at = (SELECT max(rj.rejected_at) FROM gmail_link_rejections rj
				JOIN gmail_evidence e ON e.message_id = rj.message_id WHERE e.thread_id = ?)`, id, id)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return int(n), err
}

// Thread actions: what the buttons of a Gmail alert or reminder and the Ask
// Agent do to a thread. Each changes only Applysmith's own reading of the
// thread and has an undo; Gmail is never written.
const (
	ActDone    = "done"    // MarkDone; undone by ActReopen
	ActReopen  = "reopen"  // ReopenDone
	ActDismiss = "dismiss" // not job mail; undone by ActRestore
	ActRestore = "restore" // RestoreThread
	ActSnooze  = "snooze"  // remind of the deadline tomorrow at 09:00
)

// ThreadActions is the closed set of thread actions.
var ThreadActions = []string{ActDone, ActReopen, ActDismiss, ActRestore, ActSnooze}

// ErrUnknownAction refuses an action outside ThreadActions.
var ErrUnknownAction = errors.New("gmail: unknown thread action")

// Refusal is a thread action that changed nothing, worded for the user. It
// unwraps to the reason: ErrNoThread, ErrNotActionable, ErrNoDeadline, or none.
type Refusal struct {
	Why string
	Err error
}

func (e *Refusal) Error() string { return e.Why }
func (e *Refusal) Unwrap() error { return e.Err }

// Act runs one thread action and says what it did in a sentence for the user.
// An action that would change nothing is a *Refusal.
func (r *Repo) Act(ctx context.Context, action, id string, loc *time.Location) (string, error) {
	var said string
	var err error
	switch action {
	case ActDone:
		err, said = r.MarkDone(ctx, id), "Marked done. New mail in this thread brings it back."
	case ActReopen:
		err, said = r.ReopenDone(ctx, id), "Reopened."
	case ActDismiss:
		var ids []string
		if ids, err = r.DismissThread(ctx, id); err == nil && len(ids) == 0 {
			return "", &Refusal{Why: "nothing in this thread is left to set aside"}
		}
		said = "Set aside as not job mail. New mail in this thread brings it back."
	case ActRestore:
		var n int
		if n, err = r.RestoreThread(ctx, id); err == nil && n == 0 {
			return "", &Refusal{Why: "nothing in this thread was set aside"}
		}
		said = "Put back with the job mail."
	case ActSnooze:
		var until time.Time
		until, err = r.Snooze(ctx, id, loc)
		said = "I will remind you again " + until.In(loc).Format("Mon Jan 2 at 15:04") + "."
	default:
		return "", ErrUnknownAction
	}
	switch {
	case errors.Is(err, ErrNoThread):
		return "", &Refusal{Why: "this thread is no longer in Applysmith", Err: err}
	case errors.Is(err, ErrNotActionable):
		return "", &Refusal{Why: "this thread asks nothing of you now; nothing changed", Err: err}
	case errors.Is(err, ErrNoDeadline):
		return "", &Refusal{Why: "this thread has no open deadline now; nothing changed", Err: err}
	case err != nil:
		return "", err
	}
	return said, nil
}

// TimelineEntry is one signal of a thread with its evidence.
type TimelineEntry struct {
	SignalRow
	Evidence json.RawMessage
	Link     string
	Reasons  []string
	// Outcome is what the message did to the tracker, once applied.
	Outcome *StatusOutcome
}

// ThreadDetail is a thread with its timeline.
type ThreadDetail struct {
	Thread
	Timeline []TimelineEntry
}

// ThreadDetail returns one thread with every signal and its evidence trail.
func (r *Repo) ThreadDetail(ctx context.Context, id string, now time.Time) (ThreadDetail, bool, error) {
	all, err := r.Threads(ctx, now)
	if err != nil {
		return ThreadDetail{}, false, err
	}
	for _, t := range all {
		if t.ID != id {
			continue
		}
		links, err := r.linkRows(ctx)
		if err != nil {
			return ThreadDetail{}, false, err
		}
		d := ThreadDetail{Thread: t}
		for _, s := range t.Signals {
			e := TimelineEntry{SignalRow: s, Link: link.Unlinked}
			if l, ok := links[s.MessageID]; ok {
				e.Link, e.Reasons = l.decision, reasonList(l.reasons)
			}
			var detail string
			if err := r.st.Read().QueryRowContext(ctx, `SELECT detail_json FROM gmail_decisions WHERE message_id = ? ORDER BY decided_at DESC, rowid DESC LIMIT 1`, s.MessageID).Scan(&detail); err == nil {
				e.Evidence = json.RawMessage(detail)
			}
			if o, ok, err := r.outcomeOf(ctx, s.MessageID); err != nil {
				return ThreadDetail{}, false, err
			} else if ok {
				e.Outcome = &o
			}
			d.Timeline = append(d.Timeline, e)
		}
		return d, true, nil
	}
	return ThreadDetail{}, false, nil
}

// Fingerprint identifies the state of a thread for the caches of summaries and
// drafts: the newest message and the classifier version.
func (t Thread) Fingerprint(extra ...string) string {
	h := sha256.Sum256([]byte(strings.Join(append([]string{t.Newest, classify.Version}, extra...), "|")))
	return hex.EncodeToString(h[:8])
}
