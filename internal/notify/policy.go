// Clean-room: docs/provenance.md#CR-RUNS-BOARD

package notify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"applysmith/internal/store"
)

// Kinds of announcement. The set is closed: the policy announces nothing else.
const (
	EventNeedsYou     = "needs-you"
	EventBlocked      = "blocked"
	EventDone         = "done"
	EventBatch        = "batch"
	EventGmailAlert   = "gmail-alert"
	EventGmailDigest  = "gmail-digest"
	EventCostCap      = "cost-cap"
	EventBurstSummary = "burst-summary"
	EventPromptResult = "prompt-result"
	EventGoalCheckin  = "goal-checkin"
	EventTriggerRes   = "trigger-result"
	// EventSignalHot and EventSignalDigest are the hiring-signal announcements:
	// a company entering the top tier and the daily digest.
	EventSignalHot    = "signal-hot"
	EventSignalDigest = "signal-digest"
	// EventModelGone announces a routed OpenRouter model that left the catalog.
	EventModelGone = "model-gone"
	// EventProviderRefused announces a model provider that refuses every call
	// as out of credit or with a dead key.
	EventProviderRefused = "provider-refused"
	// EventDaySummary is the once-a-day account of what was done for the
	// user and what is left for them.
	EventDaySummary = "day-summary"
	// EventInterviewReminder reminds the user of a recorded interview stage.
	EventInterviewReminder = "interview-reminder"
	// EventGmailReminder reminds the user of a job mail's open deadline.
	EventGmailReminder = "gmail-reminder"
	// EventMorningBrief is the once-a-day account of what waits for the user
	// this morning.
	EventMorningBrief = "morning-brief"
	// EventTaskUpdate is a delegated task of the Ask Agent that asks the
	// user or is done while they were not in its conversation.
	EventTaskUpdate = "task-update"
)

// Reach is which sinks an announcement goes to. The desktop gets every one;
// the phone (PhoneSink) is where the user is away from the desk, so it gets
// only what they can act on or asked to hear there.
type Reach int

const (
	// ReachAll is the desktop and the phone.
	ReachAll Reach = iota
	// ReachAsker is the desktop, and the phone when the work was asked for
	// from the phone (PolicyOptions.Asked).
	ReachAsker
	// ReachDesk is the desktop alone: the user acts on it in the dashboard.
	ReachDesk
)

// PhoneSink names the sink that reaches the user away from the desk.
const PhoneSink = "telegram"

// eventKinds is the closed set of announced kinds and the reach of each.
// Runs reach the phone only when asked for there: in the owner's workspace
// nearly every Telegram message of the first paired week was a stop or an end
// of work started at the desk, none was acted on from the phone, and an
// application stop can be handled only in the desktop's kept tab
// (docs/architecture/notify.md, "Policy").
var eventKinds = map[string]Reach{
	EventNeedsYou: ReachAsker, EventBlocked: ReachAsker, EventDone: ReachAsker,
	EventBatch: ReachDesk, EventModelGone: ReachDesk,
	EventGmailAlert: ReachAll, EventGmailReminder: ReachAll, EventInterviewReminder: ReachAll,
	EventTaskUpdate: ReachAll, EventGoalCheckin: ReachAll, EventPromptResult: ReachAll, EventTriggerRes: ReachAll,
	EventCostCap: ReachAll, EventProviderRefused: ReachAll,
	EventMorningBrief: ReachAll, EventDaySummary: ReachAll, EventGmailDigest: ReachAll,
	EventSignalHot: ReachAll, EventSignalDigest: ReachAll, EventBurstSummary: ReachAll,
}

// Timing rules of the policy.
const (
	DefaultDoneDelay = 20 * time.Second
	burstMax         = 3
	burstWindow      = time.Minute
	holdMax          = 24 * time.Hour
	sinkTimeout      = 15 * time.Second
	// UrgentInterviewWindow is how soon an interview must start for its Gmail
	// alert to be urgent.
	UrgentInterviewWindow = 24 * time.Hour
)

// PolicyOptions wire the policy.
type PolicyOptions struct {
	Store    *store.Store
	Board    *Board
	Settings *SettingsStore
	// Quiet reads the profile's quiet-hours group at each decision.
	Quiet     func(ctx context.Context) QuietHours
	Sinks     []Sink
	Now       func() time.Time
	Log       *slog.Logger
	DoneDelay time.Duration
	// ScanRollup words the announcement of a scan whose evaluations all
	// ended: the scan's counts and what the evaluations came to. "" keeps the
	// scan's own outcome sentence.
	ScanRollup func(ctx context.Context, scan Entry, members []Entry) string
	// Asked reports whether an entry's work was asked for from the phone, so
	// its stops and its end reach the phone too (ReachAsker). Nil: none was.
	Asked func(ctx context.Context, entryID string) bool
}

// Policy decides what is announced, when and to which sink. It is the board's
// observer.
type Policy struct {
	o      PolicyOptions
	sinks  map[string]Sink
	procMu sync.Mutex
	kick   chan struct{}
}

// NewPolicy returns a policy; Start runs its worker.
func NewPolicy(o PolicyOptions) *Policy {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.DoneDelay == 0 {
		o.DoneDelay = DefaultDoneDelay
	}
	if o.Settings == nil {
		o.Settings = NewSettingsStore(o.Store, o.Now)
	}
	p := &Policy{o: o, sinks: map[string]Sink{}, kick: make(chan struct{}, 1)}
	for _, s := range o.Sinks {
		p.sinks[s.Name()] = s
	}
	return p
}

// SetScanRollup installs the words of a scan's roll-up once the subsystems
// that own the records exist.
func (p *Policy) SetScanRollup(f func(ctx context.Context, scan Entry, members []Entry) string) {
	p.o.ScanRollup = f
}

// Event is one announcement to make.
type Event struct {
	Key   string
	Kind  string
	Title string
	Body  string
	Link  string

	Urgent bool
	// NeverHold keeps the announcement out of quiet hours. Only the result of
	// a scheduled prompt on a cadence may ask for it.
	NeverHold bool
	Delay     time.Duration

	entryID    string
	entryEpoch int64
	batchID    string
	onlySink   string
}

// Announce queues an announcement of the closed set of kinds. A key that was
// announced before is ignored, so a producer may call it again after a restart.
func (p *Policy) Announce(ctx context.Context, ev Event) error {
	if _, ok := eventKinds[ev.Kind]; !ok {
		return fmt.Errorf("notify: %q is not an announced event", ev.Kind)
	}
	if ev.Key == "" {
		return errors.New("notify: an announcement needs a key")
	}
	if ev.NeverHold && ev.Kind != EventPromptResult {
		return errors.New("notify: only the result of a scheduled prompt is never held")
	}
	return p.enqueue(ctx, ev)
}

// AnnounceTo is Announce for one sink only, so a producer whose text differs per
// channel (a digest sent in full to Telegram, as a headline to the desktop)
// queues one announcement for each, each with its own key.
func (p *Policy) AnnounceTo(ctx context.Context, sink string, ev Event) error {
	if _, ok := p.sinks[sink]; !ok {
		return fmt.Errorf("notify: %q is not a sink", sink)
	}
	if _, ok := eventKinds[ev.Kind]; !ok {
		return fmt.Errorf("notify: %q is not an announced event", ev.Kind)
	}
	if ev.Key == "" {
		return errors.New("notify: an announcement needs a key")
	}
	ev.onlySink = sink
	return p.enqueue(ctx, ev)
}

func (p *Policy) enqueue(ctx context.Context, ev Event) error {
	title, body := Compose(ev.Title, ev.Body)
	now := p.o.Now()
	err := p.o.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO notify_outbox (key, entry_id, entry_epoch, batch_id, kind, urgent, never_hold, only_sink, title, body, link,
			state, due_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?) ON CONFLICT(key) DO NOTHING`,
			ev.Key, ev.entryID, ev.entryEpoch, ev.batchID, ev.Kind, b2i(ev.Urgent), b2i(ev.NeverHold), ev.onlySink, title, body, ev.Link,
			ts(now.Add(ev.Delay)), ts(now))
		return err
	})
	if err != nil {
		return err
	}
	p.Kick()
	return nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// GmailAlert is an instant alert about a message. InterviewStart is the parsed
// start time when the message is about an interview.
type GmailAlert struct {
	MessageID      string
	Title          string
	Body           string
	Link           string
	InterviewStart time.Time
}

// AnnounceGmailAlert queues a Gmail instant alert. It is urgent only for an
// interview that starts within the next 24 hours; every other alert is normal.
func (p *Policy) AnnounceGmailAlert(ctx context.Context, a GmailAlert) error {
	now := p.o.Now()
	urgent := !a.InterviewStart.IsZero() && a.InterviewStart.After(now) && a.InterviewStart.Sub(now) <= UrgentInterviewWindow
	return p.Announce(ctx, Event{Key: "gmail-alert|" + a.MessageID, Kind: EventGmailAlert, Title: a.Title, Body: a.Body, Link: a.Link, Urgent: urgent})
}

// AnnounceCostCap queues the daily cost-cap alert as a normal announcement and
// reports whether it was accepted for delivery.
func (p *Policy) AnnounceCostCap(ctx context.Context, day, message string) bool {
	err := p.Announce(ctx, Event{Key: "cost-cap|" + day, Kind: EventCostCap, Title: "Daily model spend cap reached", Body: message, Link: "/usage"})
	if err != nil {
		p.o.Log.Error("the cost-cap alert could not be queued", "error", err.Error())
	}
	return err == nil
}

// Kick asks the worker to run a pass soon.
func (p *Policy) Kick() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

// Start runs the worker that delivers announcements as they fall due. The
// scheduler's tick runs the same pass, so a stopped worker loses nothing.
func (p *Policy) Start(ctx context.Context) {
	go func() {
		timer := time.NewTimer(time.Hour)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-p.kick:
			case <-timer.C:
			}
			if err := p.Process(ctx); err != nil && ctx.Err() == nil {
				p.o.Log.Warn("an announcement pass failed", "error", err.Error())
			}
			next := p.nextDue(ctx)
			if next <= 0 {
				next = time.Hour
			}
			timer.Reset(next)
		}
	}()
}

// nextDue is how long until the next announcement that is waiting for its due
// time, or zero when none is.
func (p *Policy) nextDue(ctx context.Context) time.Duration {
	var due sql.NullString
	if err := p.o.Store.Read().QueryRowContext(ctx, `SELECT MIN(due_at) FROM notify_outbox WHERE state = 'pending' AND due_at > ?`, ts(p.o.Now())).Scan(&due); err != nil || !due.Valid {
		return 0
	}
	return max(parseTS(due.String).Sub(p.o.Now()), 100*time.Millisecond)
}

// Changed implements Observer.
func (p *Policy) Changed(ctx context.Context, before *Entry, after Entry) {
	switch after.State {
	case NeedsYou, Blocked:
		_, told := p.fold(ctx, after)
		if scan, ok := strings.CutPrefix(after.BatchID, ScanBatch); ok {
			// The evaluations a scan queued are told only through its roll-up.
			p.scanEnded(ctx, scan, p.o.Settings.Get(ctx))
			return
		}
		if told {
			return
		}
		// The newer announcement replaces any earlier one of this entry when it
		// is delivered.
		if err := p.enqueue(ctx, p.entryEvent(after)); err != nil {
			p.o.Log.Error("an announcement could not be queued", "entry", after.ID, "error", err.Error())
		}
		return
	case Done:
		p.withdraw(ctx, after.ID, Handled)
		set := p.o.Settings.Get(ctx)
		if scan, ok := strings.CutPrefix(after.BatchID, ScanBatch); ok {
			p.scanEnded(ctx, scan, set)
			return
		}
		if after.Kind == KindScan {
			p.scanEnded(ctx, after.ID, set)
			return
		}
		if after.BatchID != "" {
			p.rollup(ctx, after.BatchID, set)
			return
		}
		// The inbox drain, a scheduled task, may have queued evaluations that
		// ended before it did; a task that queued none keeps its own done rule.
		if after.Kind == KindTask {
			p.scanEnded(ctx, after.ID, set)
			return
		}
		if announcesDone(after, set) {
			ev := p.entryEvent(after)
			ev.Delay = p.o.DoneDelay
			if err := p.enqueue(ctx, ev); err != nil {
				p.o.Log.Error("an announcement could not be queued", "entry", after.ID, "error", err.Error())
			}
		}
		return
	}
	if before != nil {
		p.withdraw(ctx, after.ID, Handled)
	}
}

// Retired implements Observer.
func (p *Policy) Retired(ctx context.Context, e Entry) { p.withdraw(ctx, e.ID, Handled) }

// announcesDone is the done rule: a finished entry is announced when the user
// or the agent started it and it ended ok or failed, or when a scheduled scan
// failed and the user switched failed-scan announcements on.
func announcesDone(e Entry, s Settings) bool {
	switch e.Origin {
	case OriginUser, OriginAgent:
		return e.Outcome == OutcomeOK || e.Outcome == OutcomeFailed
	case OriginSchedule:
		return e.Outcome == OutcomeFailed && e.Kind == KindScan && s.FailedScans
	}
	return false
}

func (p *Policy) entryEvent(e Entry) Event {
	link := e.Link
	if link == "" {
		link = "/runs/" + e.ID
	}
	ev := Event{Key: fmt.Sprintf("entry|%s|%s|%s|%d", e.ID, e.State, orStr(e.Reason, e.Outcome), e.Epoch), Title: e.Title, Link: link,
		entryID: e.ID, entryEpoch: e.Epoch}
	switch e.State {
	case NeedsYou:
		ev.Kind, ev.Urgent, ev.Body = EventNeedsYou, true, needsYouText(e)
	case Blocked:
		ev.Kind, ev.Urgent, ev.Body = EventBlocked, true, blockedText(e)
	default:
		ev.Kind, ev.Body = EventDone, doneText(e)
	}
	return ev
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func needsYouText(e Entry) string {
	t := map[string]string{
		"sign-in": "Sign in to continue.", "answer": "An answer is needed to continue.", "challenge": "A challenge needs you.",
		"review": "A review is waiting for you.", "one-time-code": "A verification code is needed.", "confirmation": "A confirmation is waiting for you.",
		"may-have-submitted": "This may already have been submitted. Check before trying again.", "other": "This needs you.",
	}[e.Reason]
	return withDetail(t, e.Reason, e.Detail)
}

func blockedText(e Entry) string {
	t := map[string]string{
		"credential-refused": "The saved sign-in was refused.", "lockout": "The account is locked.", "lane-unavailable": "The browser is not available.",
		"key-missing": "A key is missing.", "cap-reached": "A spending cap was reached.", "no-connection": "There is no connection.", "other": "This is stopped.",
	}[e.Reason]
	return withDetail(t, e.Reason, e.Detail)
}

// withDetail adds the producer's detail line, except for a one-time code,
// whose detail must never be echoed.
func withDetail(text, reason, detail string) string {
	if detail == "" || reason == "one-time-code" {
		return text
	}
	return text + " " + detail
}

// doneText is the body of a finished entry: the outcome sentence its producer
// wrote, after "It failed." for a failure.
func doneText(e Entry) string {
	if e.Outcome == OutcomeFailed {
		return withDetail("It failed.", "", e.Detail)
	}
	return orStr(e.Detail, "It finished.")
}

// scanEnded announces a scan once it and every evaluation it queued have
// ended, by the done rule of the scan: one announcement of the scan's entry
// with its counts and what the evaluations came to (F-2). A scheduled scan's
// evaluations, reviews among them, are left to the day summary. The inbox
// drain, a scheduled task, is told the same way whenever it queued an
// evaluation: the evaluations it sends are told only through it (F-2, F-8). A
// task that queued none keeps its own done rule.
func (p *Policy) scanEnded(ctx context.Context, scanID string, s Settings) {
	scan, err := p.o.Board.Get(ctx, scanID)
	if err != nil || scan.State != Done {
		return
	}
	members, err := p.o.Board.Members(ctx, ScanBatch+scanID)
	if err != nil || (!announcesDone(scan, s) && (scan.Kind == KindScan || len(members) == 0)) {
		return
	}
	for _, m := range members {
		if m.State == Working {
			return
		}
	}
	ev := p.entryEvent(scan)
	ev.Delay = p.o.DoneDelay
	if len(members) > 0 && p.o.ScanRollup != nil {
		ev.Body = orStr(p.o.ScanRollup(ctx, scan, members), ev.Body)
	}
	if err := p.enqueue(ctx, ev); err != nil {
		p.o.Log.Error("a scan's roll-up could not be queued", "entry", scanID, "error", err.Error())
	}
}

// fold makes the repeated stops of one piece of work one entry: the earlier
// unacknowledged waits of the same work for the same state and reason are
// acknowledged into the newer entry, which takes over what was announced for
// them, so a closed notification is never lost and the stop is told once
// (F-2). It reports how many it folded and whether one of them was told
// already. An application or an evaluation is the same work only for the same
// posting, by its identity key: their titles name the company, and two
// postings there are two stops. Other kinds are the same work by title.
func (p *Policy) fold(ctx context.Context, e Entry) (int, bool) {
	same, arg := `title = ?`, any(e.Title)
	switch e.Kind {
	case KindApply:
		same, arg = `id IN (SELECT o.run_id FROM apply_runs o JOIN apply_runs n ON n.identity_key = o.identity_key WHERE n.run_id = ? AND n.identity_key <> '')`, e.ID
	case KindEvaluate:
		same, arg = `id IN (SELECT o.run_id FROM evaluation_jobs o JOIN evaluation_jobs n ON n.identity_key = o.identity_key WHERE n.run_id = ? AND n.identity_key <> '')`, e.ID
	}
	reps, err := p.o.Board.query(ctx, `SELECT `+entryColumns+` FROM board_entries WHERE id <> ? AND kind = ? AND `+same+` AND state = ? AND reason = ?
		AND acked_at IS NULL`, e.ID, string(e.Kind), arg, string(e.State), e.Reason)
	if err != nil || len(reps) == 0 {
		return 0, false
	}
	told := false
	now := ts(p.o.Now())
	err = p.o.Store.Write(ctx, func(tx *sql.Tx) error {
		for _, r := range reps {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM notify_outbox WHERE entry_id = ? AND entry_epoch = ? AND state IN ('pending', 'held', 'settled')`,
				r.ID, r.Epoch).Scan(&n); err != nil {
				return err
			}
			told = told || n > 0
			if _, err := tx.ExecContext(ctx, `UPDATE board_entries SET acked_at = ?, updated_at = ?, revision = revision + 1 WHERE id = ?`, now, now, r.ID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE notify_outbox SET entry_id = ?, entry_epoch = ? WHERE entry_id = ? AND entry_epoch = ?`, e.ID, e.Epoch, r.ID, r.Epoch); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE OR REPLACE notify_notices SET handle = ? WHERE handle = ?`, e.ID, r.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		p.o.Log.Error("repeated stops could not be folded", "entry", e.ID, "error", err.Error())
		return 0, false
	}
	p.o.Board.fire(TopicRuns, TopicInbox)
	return len(reps), told
}

// LaterRunRetires reports whether a later run of an application's posting
// retires an entry: the newer run is the one that goes on, so an earlier stop,
// waiting or blocked, is stale. A possible submission stays for the user to
// check (F-3).
func LaterRunRetires(e Entry) bool {
	return e.Kind == KindApply && e.Waiting() && !e.Acked() && e.Reason != "may-have-submitted"
}

// FoldRepeats settles, at every start, the waits that fold and a run's start
// left: both act after their own commit (fold when a stop arrives, the start's
// retirement for its own posting), so a process stopped in between or a write
// that failed there leaves a repeat or a stale stop on the board. An
// application's stop whose posting has a run that started after its own is
// acknowledged, as that start would have done (LaterRunRetires); then of each
// kind, piece of work, state and reason the newest wait stays and the older
// ones are acknowledged into it. Nothing is announced and no tab is closed; a
// second pass finds nothing. It returns how many waits it acknowledged.
func (p *Policy) FoldRepeats(ctx context.Context) (int, error) {
	n, err := p.retireSuperseded(ctx)
	if err != nil {
		return n, err
	}
	waits, err := p.o.Board.query(ctx, `SELECT `+entryColumns+` FROM board_entries WHERE state IN ('needs-you', 'blocked') AND acked_at IS NULL
		ORDER BY state_at DESC, id DESC`)
	if err != nil {
		return n, err
	}
	for _, e := range waits {
		// A newer wait of the same work acknowledged it already.
		cur, err := p.o.Board.Get(ctx, e.ID)
		if err != nil || cur.Acked() {
			continue
		}
		k, _ := p.fold(ctx, cur)
		n += k
	}
	return n, nil
}

// retireSuperseded acknowledges the stops of applications whose posting has a
// run that started after theirs, which an earlier process left on the board.
// It returns how many it acknowledged.
func (p *Policy) retireSuperseded(ctx context.Context) (int, error) {
	stale, err := p.o.Board.query(ctx, `SELECT `+entryColumns+` FROM board_entries WHERE kind = 'apply' AND state IN ('needs-you', 'blocked') AND acked_at IS NULL
		AND id IN (SELECT o.run_id FROM apply_runs o JOIN apply_runs n ON n.identity_key = o.identity_key AND n.started_at > o.started_at WHERE o.identity_key <> '')`)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range stale {
		if !LaterRunRetires(e) {
			continue
		}
		if _, err := p.o.Board.Ack(ctx, e.ID); err != nil {
			p.o.Log.Warn("a stop a later run superseded stayed on the board", "entry", e.ID, "error", err.Error())
			continue
		}
		n++
	}
	return n, nil
}

// rollup announces a batch once its last member has finished. Members that
// need the user were announced on their own when they happened.
func (p *Policy) rollup(ctx context.Context, batch string, s Settings) {
	members, err := p.o.Board.Members(ctx, batch)
	if err != nil || len(members) == 0 {
		return
	}
	var done, waiting int
	var last time.Time
	announce := false
	for _, m := range members {
		switch m.State {
		case Working:
			return
		case Done:
			done++
			announce = announce || announcesDone(m, s)
		case NeedsYou, Blocked:
			waiting++
		}
		last = latest(last, m.StateAt)
	}
	if !announce {
		return
	}
	ev := Event{Key: fmt.Sprintf("batch|%s|%d", batch, last.UnixMilli()), Kind: EventBatch, Title: "Batch finished",
		Body: fmt.Sprintf("%d of %d finished, %d need you", done, len(members), waiting), Link: "/runs", Delay: p.o.DoneDelay, batchID: batch}
	if err := p.enqueue(ctx, ev); err != nil {
		p.o.Log.Error("a rollup could not be queued", "batch", batch, "error", err.Error())
	}
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

type outboxRow struct {
	ID         int64
	Key        string
	EntryID    string
	EntryEpoch int64
	BatchID    string
	Kind       string
	Urgent     bool
	NeverHold  bool
	OnlySink   string
	Title      string
	Body       string
	Link       string
	State      string
	DueAt      time.Time
	CreatedAt  time.Time
	HeldAt     time.Time
}

func (r outboxRow) announcement() Announcement {
	return Announcement{ID: r.ID, EntryID: r.EntryID, Kind: r.Kind, Urgent: r.Urgent, Title: r.Title, Body: r.Body, Link: r.Link}
}

const outboxColumns = `id, key, entry_id, entry_epoch, batch_id, kind, urgent, never_hold, only_sink, title, body, link, state, due_at, created_at, held_at`

func scanOutbox(rows scanner) (outboxRow, error) {
	var r outboxRow
	var urgent, never int
	var due, created string
	var held sql.NullString
	err := rows.Scan(&r.ID, &r.Key, &r.EntryID, &r.EntryEpoch, &r.BatchID, &r.Kind, &urgent, &never, &r.OnlySink, &r.Title, &r.Body, &r.Link,
		&r.State, &due, &created, &held)
	r.Urgent, r.NeverHold = urgent == 1, never == 1
	r.DueAt, r.CreatedAt, r.HeldAt = parseTS(due), parseTS(created), parseTS(held.String)
	return r, err
}

// Process runs one pass: it releases what quiet hours held once the window has
// ended, delivers what is due, retries what failed and sends a summary for
// what the burst limit coalesced. It is safe to call from several places.
func (p *Policy) Process(ctx context.Context) error {
	p.procMu.Lock()
	defer p.procMu.Unlock()
	if now := p.o.Now(); !p.quietAt(ctx, now) {
		p.foldHeld(ctx, now, p.o.Settings.Get(ctx))
		p.summaries(ctx, now)
	}
	rows, err := p.o.Store.Read().QueryContext(ctx, `SELECT `+outboxColumns+` FROM notify_outbox WHERE state IN ('pending', 'held') ORDER BY urgent DESC, id`)
	if err != nil {
		return err
	}
	var open []outboxRow
	for rows.Next() {
		r, err := scanOutbox(rows)
		if err != nil {
			rows.Close()
			return err
		}
		open = append(open, r)
	}
	rows.Close()
	// The quiet-hours decision is read after the rows, never before: a row
	// queued while this pass was starting must not be judged by a snapshot that
	// predates it (a window the user had just switched on would be missed).
	now := p.o.Now()
	set := p.o.Settings.Get(ctx)
	quiet := p.quietAt(ctx, now)
	for _, r := range open {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		p.handle(ctx, r, now, set, quiet)
	}
	return nil
}

func (p *Policy) quietAt(ctx context.Context, now time.Time) bool {
	return p.o.Quiet != nil && p.o.Quiet(ctx).Active(now)
}

func (p *Policy) handle(ctx context.Context, r outboxRow, now time.Time, set Settings, quiet bool) {
	ref := r.CreatedAt
	if r.State == "held" && !r.HeldAt.IsZero() {
		ref = r.HeldAt
	}
	switch {
	case now.Sub(ref) > holdMax:
		p.o.Log.Warn("an announcement could not be delivered for 24 hours and was dropped", "id", r.ID, "kind", r.Kind, "title", r.Title)
		p.finish(ctx, r.ID, "dropped", now)
	case r.DueAt.After(now):
	case !p.valid(ctx, r):
		p.finish(ctx, r.ID, "discarded", now)
	case quiet && !r.Urgent && !r.NeverHold:
		if r.State == "pending" {
			p.hold(ctx, r, now, set)
		}
	default:
		p.deliver(ctx, r, now, set)
	}
}

func (p *Policy) finish(ctx context.Context, id int64, state string, now time.Time) {
	err := p.o.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE notify_outbox SET state = ?, settled_at = ? WHERE id = ?`, state, ts(now), id)
		return err
	})
	if err != nil {
		p.o.Log.Error("an announcement could not be settled", "id", id, "error", err.Error())
	}
}

// hold stores a normal announcement during quiet hours and puts it in the
// passive in-page list at once: only the interruption waits.
func (p *Policy) hold(ctx context.Context, r outboxRow, now time.Time, set Settings) {
	err := p.o.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE notify_outbox SET state = 'held', held_at = ? WHERE id = ?`, ts(now), r.ID)
		return err
	})
	if err != nil {
		p.o.Log.Error("an announcement could not be held", "id", r.ID, "error", err.Error())
		return
	}
	for _, s := range p.o.Sinks {
		if n, ok := s.(Noticer); ok && (r.OnlySink == "" || r.OnlySink == s.Name()) && p.enabled(s.Name(), set) {
			n.Notice(ctx, r.announcement())
		}
	}
}

// valid reports whether the announcement still describes the world: its entry
// is still in the announced state and unacknowledged, or its batch is still
// finished and has something unacknowledged in it.
func (p *Policy) valid(ctx context.Context, r outboxRow) bool {
	switch {
	case r.EntryID != "":
		e, err := p.o.Board.Get(ctx, r.EntryID)
		return err == nil && e.Epoch == r.EntryEpoch && !e.Acked()
	case r.BatchID != "":
		members, err := p.o.Board.Members(ctx, r.BatchID)
		if err != nil || len(members) == 0 {
			return false
		}
		open := false
		for _, m := range members {
			if m.State == Working {
				return false
			}
			open = open || !m.Acked()
		}
		return open
	}
	return true
}

func (p *Policy) enabled(name string, s Settings) bool {
	switch name {
	case "desktop":
		return s.Desktop
	case "telegram":
		return s.Telegram
	}
	return true
}

// reaches reports whether the announcement goes to the named sink: its kind's
// reach (eventKinds) for the phone, and every announcement for the others.
func (p *Policy) reaches(ctx context.Context, name string, r outboxRow) bool {
	if name != PhoneSink {
		return true
	}
	switch eventKinds[r.Kind] {
	case ReachAll:
		return true
	case ReachAsker:
		return r.EntryID != "" && p.o.Asked != nil && p.o.Asked(ctx, r.EntryID)
	}
	return false
}

// deliver offers the announcement to each sink that is on, available and
// reached by it. A sink that is on but unavailable and keeps an in-page list
// gets it there instead. A sink that fails leaves the others unaffected and is
// tried again on the next pass.
func (p *Policy) deliver(ctx context.Context, r outboxRow, now time.Time, set Settings) {
	a := r.announcement()
	settled := true
	for _, s := range p.o.Sinks {
		name := s.Name()
		if (r.OnlySink != "" && r.OnlySink != name) || !p.enabled(name, set) || !p.reaches(ctx, name, r) {
			continue
		}
		if !s.Available(ctx) {
			if n, ok := s.(Noticer); ok {
				n.Notice(ctx, a)
			}
			continue
		}
		if p.hasDelivery(ctx, r.ID, name) {
			continue
		}
		normal := !r.Urgent && r.Kind != EventBurstSummary
		if normal && p.windowFull(ctx, name, now) {
			p.record(ctx, r.ID, name, "coalesced", "", false, now)
			continue
		}
		ext, err := p.call(ctx, s, a)
		switch {
		case err == nil:
			p.record(ctx, r.ID, name, "delivered", ext, normal, now)
			p.supersede(ctx, s, r)
			if n, ok := s.(Noticer); ok {
				n.Seen(ctx, a.Handle())
			}
		case errors.Is(err, ErrGone):
			p.o.Log.Warn("an announcement was dropped because its destination is gone", "sink", name, "id", r.ID, "title", r.Title)
			p.record(ctx, r.ID, name, "dropped", "", false, now)
		default:
			settled = false
			p.o.Log.Warn("a sink could not deliver an announcement", "sink", name, "id", r.ID, "error", err.Error())
		}
	}
	if settled {
		p.finish(ctx, r.ID, "settled", now)
	}
}

func (p *Policy) call(ctx context.Context, s Sink, a Announcement) (ext string, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("the %s sink panicked: %v", s.Name(), rec)
		}
	}()
	cctx, cancel := context.WithTimeout(ctx, sinkTimeout)
	defer cancel()
	return s.Announce(cctx, a)
}

func (p *Policy) hasDelivery(ctx context.Context, id int64, sink string) bool {
	var n int
	_ = p.o.Store.Read().QueryRowContext(ctx, `SELECT count(*) FROM notify_deliveries WHERE outbox_id = ? AND sink = ?`, id, sink).Scan(&n)
	return n > 0
}

// windowFull reports whether the sink already delivered its three normal
// announcements in the last 60 seconds.
func (p *Policy) windowFull(ctx context.Context, sink string, now time.Time) bool {
	var n int
	_ = p.o.Store.Read().QueryRowContext(ctx, `SELECT count(*) FROM notify_deliveries WHERE sink = ? AND normal = 1 AND state = 'delivered' AND at > ?`,
		sink, ts(now.Add(-burstWindow))).Scan(&n)
	return n >= burstMax
}

func (p *Policy) record(ctx context.Context, id int64, sink, state, ext string, normal bool, now time.Time) {
	err := p.o.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO notify_deliveries (outbox_id, sink, state, ext_id, normal, at) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(outbox_id, sink) DO NOTHING`, id, sink, state, ext, b2i(normal), ts(now))
		return err
	})
	if err != nil {
		p.o.Log.Error("a delivery could not be recorded", "id", id, "sink", sink, "error", err.Error())
	}
}

type shown struct {
	a   Announcement
	ext string
}

// shownOf lists what the sink still shows for the entry: delivered and not
// withdrawn or superseded, and older than before when before is not zero.
func (p *Policy) shownOf(ctx context.Context, entryID, sink string, before int64) []shown {
	q := `SELECT o.id, o.kind, o.urgent, o.title, o.body, o.link, d.ext_id FROM notify_outbox o JOIN notify_deliveries d ON d.outbox_id = o.id
		WHERE o.entry_id = ? AND d.sink = ? AND d.state = 'delivered'`
	args := []any{entryID, sink}
	if before > 0 {
		q += ` AND o.id < ?`
		args = append(args, before)
	}
	rows, err := p.o.Store.Read().QueryContext(ctx, q+` ORDER BY o.id`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []shown
	for rows.Next() {
		s := shown{a: Announcement{EntryID: entryID}}
		var urgent int
		if rows.Scan(&s.a.ID, &s.a.Kind, &urgent, &s.a.Title, &s.a.Body, &s.a.Link, &s.ext) != nil {
			return out
		}
		s.a.Urgent = urgent == 1
		out = append(out, s)
	}
	return out
}

func (p *Policy) setDelivery(ctx context.Context, id int64, sink, state string) {
	_ = p.o.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE notify_deliveries SET state = ? WHERE outbox_id = ? AND sink = ?`, state, id, sink)
		return err
	})
}

// supersede closes what the sink still shows of the entry's earlier
// announcements now that a newer one was delivered. The sink may not allow it
// any more; that is not an error.
func (p *Policy) supersede(ctx context.Context, s Sink, r outboxRow) {
	if r.EntryID == "" {
		return
	}
	for _, old := range p.shownOf(ctx, r.EntryID, s.Name(), r.ID) {
		if err := s.Withdraw(ctx, old.a, old.ext, Superseded); err != nil {
			p.o.Log.Debug("an earlier announcement could not be marked superseded", "sink", s.Name(), "error", err.Error())
		}
		p.setDelivery(ctx, old.a.ID, s.Name(), "superseded")
	}
}

// withdraw closes everything the sinks still show for an entry.
func (p *Policy) withdraw(ctx context.Context, entryID string, why Why) {
	for name, s := range p.sinks {
		if n, ok := s.(Noticer); ok {
			n.Forget(ctx, entryID)
		}
		for _, old := range p.shownOf(ctx, entryID, name, 0) {
			if err := s.Withdraw(ctx, old.a, old.ext, why); err != nil {
				p.o.Log.Debug("a notification could not be withdrawn", "sink", name, "error", err.Error())
			}
			p.setDelivery(ctx, old.a.ID, name, "withdrawn")
		}
	}
}

// summaries turns what the burst limit coalesced into one summary per sink,
// once the sink's window has room again.
func (p *Policy) summaries(ctx context.Context, now time.Time) {
	for name := range p.sinks {
		if p.windowFull(ctx, name, now) {
			continue
		}
		var n int
		if err := p.o.Store.Read().QueryRowContext(ctx, `SELECT count(*) FROM notify_deliveries WHERE sink = ? AND state = 'coalesced'`, name).Scan(&n); err != nil || n == 0 {
			continue
		}
		ev := Event{Key: "burst|" + name + "|" + strconv.FormatInt(now.UnixMilli(), 10), Kind: EventBurstSummary, Title: "More updates",
			Body: fmt.Sprintf("%d more updates", n), Link: "/runs", onlySink: name}
		title, body := Compose(ev.Title, ev.Body)
		err := p.o.Store.Write(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `INSERT INTO notify_outbox (key, kind, urgent, only_sink, title, body, link, state, due_at, created_at)
				VALUES (?, ?, 0, ?, ?, ?, ?, 'pending', ?, ?) ON CONFLICT(key) DO NOTHING`, ev.Key, ev.Kind, name, title, body, ev.Link, ts(now), ts(now)); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `UPDATE notify_deliveries SET state = 'summarized' WHERE sink = ? AND state = 'coalesced'`, name)
			return err
		})
		if err != nil {
			p.o.Log.Error("a burst summary could not be queued", "sink", name, "error", err.Error())
		}
	}
}

// foldHeld turns what quiet hours held into one summary per sink when two or
// more items wait for it, so the end of the window brings one message that
// lists them in order instead of a burst. A Gmail digest keeps its own message:
// its pages are the content.
func (p *Policy) foldHeld(ctx context.Context, now time.Time, set Settings) {
	rows, err := p.o.Store.Read().QueryContext(ctx, `SELECT `+outboxColumns+` FROM notify_outbox WHERE state = 'held' AND kind != ? ORDER BY id`, EventGmailDigest)
	if err != nil {
		return
	}
	var held []outboxRow
	for rows.Next() {
		r, err := scanOutbox(rows)
		if err != nil {
			break
		}
		held = append(held, r)
	}
	rows.Close()
	for _, s := range p.o.Sinks {
		name := s.Name()
		if len(held) < 2 || !p.enabled(name, set) || !s.Available(ctx) {
			continue
		}
		var mine []outboxRow
		var titles []string
		link := ""
		for _, r := range held {
			if (r.OnlySink != "" && r.OnlySink != name) || !p.reaches(ctx, name, r) || now.Sub(r.HeldAt) > holdMax || p.hasDelivery(ctx, r.ID, name) || !p.valid(ctx, r) {
				continue
			}
			mine, titles = append(mine, r), append(titles, r.Title)
			if link == "" || link == r.Link {
				link = r.Link
			} else {
				link = "/runs"
			}
		}
		if len(mine) < 2 {
			continue
		}
		title, body := Compose(fmt.Sprintf("%d updates while quiet hours were on", len(mine)), strings.Join(titles, "; "))
		key := "quiet|" + name + "|" + strconv.FormatInt(mine[len(mine)-1].ID, 10)
		err := p.o.Store.Write(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `INSERT INTO notify_outbox (key, kind, urgent, only_sink, title, body, link, state, due_at, created_at)
				VALUES (?, ?, 0, ?, ?, ?, ?, 'pending', ?, ?) ON CONFLICT(key) DO NOTHING`, key, EventBurstSummary, name, title, body, link, ts(now), ts(now)); err != nil {
				return err
			}
			for _, r := range mine {
				if _, err := tx.ExecContext(ctx, `INSERT INTO notify_deliveries (outbox_id, sink, state, ext_id, normal, at) VALUES (?, ?, 'summarized', '', 0, ?)
					ON CONFLICT(outbox_id, sink) DO NOTHING`, r.ID, name, ts(now)); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			p.o.Log.Error("the quiet-hours summary could not be queued", "sink", name, "error", err.Error())
			continue
		}
		// The summary is due now and goes out in this pass, while the sink
		// is available; it shows these, so their notices are read.
		if n, ok := s.(Noticer); ok {
			for _, r := range mine {
				n.Seen(ctx, r.announcement().Handle())
			}
		}
	}
}

// TestResult is what a test notification did on one sink: "sent", "off" (the
// switch is off), "unavailable" (Telegram without a token or a paired chat) or
// "failed" with the sink's error.
type TestResult struct {
	Sink  string
	State string
	Err   error
}

// Test sends a test notification to every sink at once. It is not queued:
// the user asked for it, so neither quiet hours nor the burst limit apply.
func (p *Policy) Test(ctx context.Context) []TestResult {
	set := p.o.Settings.Get(ctx)
	a := Announcement{Kind: EventBurstSummary, Title: "Test notification", Body: "Notifications from Applysmith reach you here."}
	var out []TestResult
	for _, s := range p.o.Sinks {
		r := TestResult{Sink: s.Name(), State: "sent"}
		switch {
		case !p.enabled(r.Sink, set):
			r.State = "off"
		case !s.Available(ctx):
			r.State = "unavailable"
		default:
			if _, err := p.call(ctx, s, a); err != nil {
				r.State, r.Err = "failed", err
			}
		}
		out = append(out, r)
	}
	return out
}

// Prune deletes announcements that ended more than 14 days ago, with their
// deliveries, and notices older than that.
func (p *Policy) Prune(ctx context.Context) error {
	cut := ts(p.o.Now().Add(-DoneRetention))
	return p.o.Store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM notify_outbox WHERE state IN ('settled', 'discarded', 'dropped') AND settled_at < ?`, cut); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM notify_notices WHERE created_at < ?`, cut)
		return err
	})
}
