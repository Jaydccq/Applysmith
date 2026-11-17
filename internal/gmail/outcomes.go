package gmail

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"applysmith/internal/gmail/classify"
	"applysmith/internal/gmail/link"
	"applysmith/internal/ledger"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
)

// Results of one outcome.
const (
	OutcomeChanged      = "changed"
	OutcomeNoChange     = "no_change"
	OutcomeReview       = "review"
	OutcomeConfirmation = "confirmation"
)

// StatusOutcome is the ledger row of one message applied to the tracker.
type StatusOutcome struct {
	MessageID   string
	CandidacyID int64
	SignalType  string
	Result      string
	From, To    string
	// Reason is the tracker's refusal, set for a review outcome.
	Reason string
	At     time.Time
}

// OutcomeStats says what one pass did.
type OutcomeStats struct {
	Changed, NoChange, Review, Confirmations, AlreadyProcessed int
}

// targetOf maps a signal type to the status it asks for. The empty status
// means the type changes none.
func targetOf(signalType string) tracker.Status {
	switch signalType {
	case classify.TypeApplied:
		return tracker.Applied
	case classify.TypeReplyNeeded, classify.TypeActionNeeded:
		return tracker.Responded
	case classify.TypeAssessment, classify.TypeInterview:
		return tracker.Interview
	case classify.TypeRejection:
		return tracker.Rejected
	case classify.TypeOffer:
		return tracker.Offer
	}
	return ""
}

type pending struct {
	sig  SignalRow
	cand int64
}

// ApplyOutcomes turns the auto-linked and user-confirmed signals into status
// changes. The signals of one candidacy replay in received-time order against
// a running status that starts from its current one. Each message id is
// applied once; the tracker's table decides what is allowed.
func (p *Pipeline) ApplyOutcomes(ctx context.Context) (OutcomeStats, error) {
	var st OutcomeStats
	sigs, err := p.Repo.Signals(ctx)
	if err != nil {
		return st, err
	}
	links, err := p.Repo.linkRows(ctx)
	if err != nil {
		return st, err
	}
	done, err := p.Repo.outcomeIDs(ctx)
	if err != nil {
		return st, err
	}
	byCand := map[int64][]pending{}
	for _, s := range sigs {
		l, ok := links[s.MessageID]
		if !ok || l.decision != link.Auto || l.candidacyID == 0 || !linkable(s) || targetOf(s.Type) == "" {
			continue
		}
		if s.Tier != classify.TierActionable && !l.confirmed {
			continue
		}
		if done[s.MessageID] {
			st.AlreadyProcessed++
			continue
		}
		byCand[l.candidacyID] = append(byCand[l.candidacyID], pending{s, l.candidacyID})
	}
	ids := make([]int64, 0, len(byCand))
	for id := range byCand {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if err := p.replay(ctx, id, byCand[id], &st); err != nil {
			return st, err
		}
	}
	return st, nil
}

func (p *Pipeline) replay(ctx context.Context, id int64, list []pending, st *OutcomeStats) error {
	slices.SortStableFunc(list, func(a, b pending) int {
		if c := a.sig.ReceivedMS - b.sig.ReceivedMS; c != 0 {
			return int(max(min(c, 1), -1))
		}
		return compareStrings(a.sig.MessageID, b.sig.MessageID)
	})
	d, err := p.Tracker.Get(ctx, id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		// A link to a row that is gone is a question for the user, not a no-op.
		for _, pd := range list {
			o := StatusOutcome{MessageID: pd.sig.MessageID, CandidacyID: id, SignalType: pd.sig.Type, Result: OutcomeReview,
				Reason: "the tracker row this mail was linked to no longer exists", To: string(targetOf(pd.sig.Type))}
			if err := p.Repo.recordOutcome(ctx, o); err != nil {
				return err
			}
			st.Review++
		}
		return nil
	}
	running := d.Entry.Status
	keys := append([]string{d.Entry.IdentityKey}, d.Aliases...)
	for _, pd := range list {
		o := StatusOutcome{MessageID: pd.sig.MessageID, CandidacyID: id, SignalType: pd.sig.Type, From: string(running)}
		to := targetOf(pd.sig.Type)
		var settles *ledger.Waiting
		if to == tracker.Applied {
			if settles, err = p.receiptSettles(ctx, keys, pd.sig); err != nil {
				return err
			}
		}
		switch {
		case to == tracker.Offer:
			if err := p.offer(ctx, pd, running, &o); err != nil {
				return err
			}
		case running == to, running.Submitted() && !running.Terminal() && to.Order() <= running.Order():
			o.Result = OutcomeNoChange
		default:
			change := tracker.StatusChange{ID: id, To: to, Actor: tracker.ActorGmail,
				Detail: map[string]any{"source_message_id": pd.sig.MessageID, "signal_type": pd.sig.Type}}
			if settles != nil {
				// The receipt of the apply chain's own submission keeps it the
				// agent's (owner decision F-5).
				change.AppliedBy = tracker.ByAgent
			}
			res, err := p.Tracker.SetStatus(ctx, change)
			var refused *tracker.RefusedError
			switch {
			case errors.As(err, &refused):
				o.Result, o.Reason, o.To = OutcomeReview, refused.Reason, string(to)
			case err != nil:
				return err
			case res.Changed:
				o.Result, o.To = OutcomeChanged, string(to)
				running = res.Entry.Status
			default:
				o.Result = OutcomeNoChange
			}
		}
		if err := p.Repo.recordOutcome(ctx, o); err != nil {
			return err
		}
		if settles != nil && o.Result != OutcomeReview {
			if err := p.Ledger.SettleWaiting(ctx, settles.ID, ledger.ConfirmReceipt, pd.sig.MessageID); err != nil && !errors.Is(err, ledger.ErrNotWaiting) {
				return err
			}
		}
		if pd.sig.Type == classify.TypeInterview {
			p.proposeStage(ctx, id, pd.sig)
		}
		switch o.Result {
		case OutcomeChanged:
			st.Changed++
		case OutcomeNoChange:
			st.NoChange++
		case OutcomeReview:
			st.Review++
		case OutcomeConfirmation:
			st.Confirmations++
		}
	}
	return nil
}

// receiptSettles is the submission a receipt settles: the apply chain's
// waiting press of the linked row's posting, made no later than the mail
// arrived (a receipt may carry a date up to ReceiptLead before the press).
func (p *Pipeline) receiptSettles(ctx context.Context, keys []string, sig SignalRow) (*ledger.Waiting, error) {
	if p.Ledger == nil {
		return nil, nil
	}
	w, ok, err := p.Ledger.WaitingFor(ctx, keys)
	if err != nil || !ok || time.UnixMilli(sig.ReceivedMS).Before(w.At.Add(-ReceiptLead)) {
		return nil, err
	}
	return &w, nil
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// offer records a confirmation request; the status stays as it is.
func (p *Pipeline) offer(ctx context.Context, pd pending, running tracker.Status, o *StatusOutcome) error {
	if running.Terminal() || running.Order() >= tracker.Offer.Order() {
		o.Result = OutcomeNoChange
		return nil
	}
	o.Result, o.To = OutcomeConfirmation, string(tracker.Offer)
	return p.Repo.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO gmail_confirmations (message_id, thread_id, candidacy_id, status, state, created_at)
			VALUES (?, ?, ?, 'offer', 'pending', ?)`, pd.sig.MessageID, pd.sig.ThreadID, pd.cand, stamp(p.Repo.now()))
		return err
	})
}

func (r *Repo) recordOutcome(ctx context.Context, o StatusOutcome) error {
	return r.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO gmail_outcomes (message_id, candidacy_id, signal_type, result, from_status, to_status, reason, at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, o.MessageID, o.CandidacyID, o.SignalType, o.Result, o.From, o.To, o.Reason, stamp(r.now()))
		return err
	})
}

func (r *Repo) outcomeIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := r.st.Read().QueryContext(ctx, `SELECT message_id FROM gmail_outcomes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

type refusal struct {
	candidacyID int64
	reason      string
	from, to    string
}

func (r *Repo) refusedOutcomes(ctx context.Context) (map[string]refusal, error) {
	rows, err := r.st.Read().QueryContext(ctx, `SELECT message_id, candidacy_id, reason, from_status, to_status FROM gmail_outcomes WHERE result = 'review'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]refusal{}
	for rows.Next() {
		var id string
		var f refusal
		if err := rows.Scan(&id, &f.candidacyID, &f.reason, &f.from, &f.to); err != nil {
			return nil, err
		}
		out[id] = f
	}
	return out, rows.Err()
}

func (r *Repo) outcomeOf(ctx context.Context, messageID string) (StatusOutcome, bool, error) {
	var o StatusOutcome
	var at string
	err := r.st.Read().QueryRowContext(ctx, `SELECT message_id, candidacy_id, signal_type, result, from_status, to_status, reason, at FROM gmail_outcomes WHERE message_id = ?`, messageID).
		Scan(&o.MessageID, &o.CandidacyID, &o.SignalType, &o.Result, &o.From, &o.To, &o.Reason, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return StatusOutcome{}, false, nil
	}
	o.At, _ = parseStamp(at)
	return o, err == nil, err
}

// Move is a status a message moved on its own, with its thread and subject.
type Move struct {
	StatusOutcome
	ThreadID, Subject string
}

// Moves lists the status changes mails made since a time, newest first, at
// most limit.
func (r *Repo) Moves(ctx context.Context, since time.Time, limit int) ([]Move, error) {
	rows, err := r.st.Read().QueryContext(ctx, `SELECT o.message_id, o.candidacy_id, o.signal_type, o.from_status, o.to_status, o.at,
		COALESCE(e.thread_id, ''), COALESCE(e.subject, '') FROM gmail_outcomes o LEFT JOIN gmail_evidence e ON e.message_id = o.message_id
		WHERE o.result = 'changed' AND o.at >= ? ORDER BY o.at DESC, o.message_id LIMIT ?`, stamp(since), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Move
	for rows.Next() {
		m := Move{StatusOutcome: StatusOutcome{Result: OutcomeChanged}}
		var at string
		if err := rows.Scan(&m.MessageID, &m.CandidacyID, &m.SignalType, &m.From, &m.To, &at, &m.ThreadID, &m.Subject); err != nil {
			return nil, err
		}
		m.At, _ = parseStamp(at)
		out = append(out, m)
	}
	return out, rows.Err()
}

// Confirmation is a request for the user to confirm an offer.
type Confirmation struct {
	ID          int64
	MessageID   string
	ThreadID    string
	CandidacyID int64
	Status      string
	State       string
	CreatedAt   time.Time
}

// Confirmations lists the requests in a state, newest first.
func (r *Repo) Confirmations(ctx context.Context, state string) ([]Confirmation, error) {
	rows, err := r.st.Read().QueryContext(ctx, `SELECT id, message_id, thread_id, candidacy_id, status, state, created_at FROM gmail_confirmations WHERE state = ? ORDER BY id DESC`, state)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Confirmation
	for rows.Next() {
		var c Confirmation
		var at string
		if err := rows.Scan(&c.ID, &c.MessageID, &c.ThreadID, &c.CandidacyID, &c.Status, &c.State, &at); err != nil {
			return nil, err
		}
		c.CreatedAt, _ = parseStamp(at)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ErrNoConfirmation is returned for an unknown or already decided request.
var ErrNoConfirmation = errors.New("gmail: no pending confirmation with that id")

// confirmation reads one request; an unknown one is ErrNoConfirmation.
func (r *Repo) confirmation(ctx context.Context, id int64) (Confirmation, error) {
	var c Confirmation
	var at string
	err := r.st.Read().QueryRowContext(ctx, `SELECT id, message_id, thread_id, candidacy_id, status, state, created_at FROM gmail_confirmations WHERE id = ?`, id).
		Scan(&c.ID, &c.MessageID, &c.ThreadID, &c.CandidacyID, &c.Status, &c.State, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNoConfirmation
	}
	return c, err
}

// RestoreConfirmation puts a decided offer request back to pending. The undo
// of a confirmation also puts the row back to the status it had, in the same
// transaction and only while the confirmation is still its latest status
// change (store.ErrConflict otherwise, with nothing changed); it returns that
// status. A pending or unknown request is ErrNoConfirmation.
func (p *Pipeline) RestoreConfirmation(ctx context.Context, id int64) (tracker.Status, error) {
	c, err := p.Repo.confirmation(ctx, id)
	if err != nil {
		return "", err
	}
	back := func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE gmail_confirmations SET state = 'pending', decided_at = NULL WHERE id = ? AND state = ?`, id, c.State)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNoConfirmation
		}
		return nil
	}
	switch c.State {
	case "dismissed":
		return "", p.Repo.st.Write(ctx, back)
	case "confirmed":
		e, err := p.Tracker.RevertStatus(ctx, c.CandidacyID, "source_message_id", c.MessageID, back)
		return e.Status, err
	}
	return "", ErrNoConfirmation
}

// DecideConfirmation settles an offer request. Confirming moves the row to
// offer as the user, because the tracker never lets the Gmail actor set that
// status; dismissing leaves the row as it is.
func (p *Pipeline) DecideConfirmation(ctx context.Context, id int64, confirm bool) error {
	c, err := p.Repo.confirmation(ctx, id)
	if err == nil && c.State != "pending" {
		return ErrNoConfirmation
	}
	if err != nil {
		return err
	}
	state := "dismissed"
	if confirm {
		state = "confirmed"
		_, err := p.Tracker.SetStatus(ctx, tracker.StatusChange{ID: c.CandidacyID, To: tracker.Offer, Actor: tracker.ActorUser,
			Note: "Offer confirmed from a Gmail message.", Detail: map[string]any{"source_message_id": c.MessageID, "signal_type": classify.TypeOffer}})
		if err != nil {
			return err
		}
	}
	return p.Repo.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE gmail_confirmations SET state = ?, decided_at = ? WHERE id = ? AND state = 'pending'`, state, stamp(p.Repo.now()), id)
		return err
	})
}
