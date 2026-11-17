package gmail

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"applysmith/internal/gmail/classify"
	"applysmith/internal/gmail/link"
	"applysmith/internal/ledger"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
)

// Typed failures of the link review.
var (
	ErrNoSignal   = errors.New("gmail: no such signal")
	ErrNoLinkable = errors.New("gmail: that signal cannot be linked")
)

// Pipeline runs the work after classification: it links signals to tracker
// rows and turns the links into status changes. It is the Syncer's Linker.
type Pipeline struct {
	Repo    *Repo
	Tracker *tracker.Tracker
	// Ledger is the submissions ledger a receipt settles the apply chain's
	// waiting submissions in; nil settles none.
	Ledger *ledger.Ledger
	// Stages receives the interview stages that interview mails propose; nil
	// leaves them out.
	Stages StageProposer

	mu sync.Mutex
}

// Link runs the link stage of a sync: resolve, then apply the outcomes. Either
// failure fails the stage, and what was committed stays.
func (p *Pipeline) Link(ctx context.Context, _ int64) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n, err := p.Resolve(ctx)
	if err != nil {
		return n, err
	}
	_, err = p.ApplyOutcomes(ctx)
	return n, err
}

// linkRow is one stored decision.
type linkRow struct {
	messageID, threadID string
	decision            string
	candidacyID         int64
	score, runnerUp     float64
	reasons             string
	confirmed           bool
}

func (r *Repo) linkRows(ctx context.Context) (map[string]linkRow, error) {
	rows, err := r.st.Read().QueryContext(ctx, `SELECT message_id, thread_id, decision, COALESCE(candidacy_id, 0), score, runner_up, reasons, confirmed FROM gmail_links`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]linkRow{}
	for rows.Next() {
		var l linkRow
		var conf int
		if err := rows.Scan(&l.messageID, &l.threadID, &l.decision, &l.candidacyID, &l.score, &l.runnerUp, &l.reasons, &conf); err != nil {
			return nil, err
		}
		l.confirmed = conf == 1
		out[l.messageID] = l
	}
	return out, rows.Err()
}

// threadLinks returns the candidacies each thread is linked to; a confirmed
// thread has exactly its confirmed candidacy.
func (r *Repo) threadLinks(ctx context.Context, links map[string]linkRow) (map[string][]int64, error) {
	out := map[string][]int64{}
	for _, l := range links {
		if l.decision == link.Auto && l.candidacyID != 0 && !slices.Contains(out[l.threadID], l.candidacyID) {
			out[l.threadID] = append(out[l.threadID], l.candidacyID)
		}
	}
	rows, err := r.st.Read().QueryContext(ctx, `SELECT thread_id, candidacy_id FROM gmail_thread_links`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t string
		var id int64
		if err := rows.Scan(&t, &id); err != nil {
			return nil, err
		}
		out[t] = []int64{id}
	}
	return out, rows.Err()
}

func (r *Repo) confirmedThreads(ctx context.Context) (map[string]bool, error) {
	rows, err := r.st.Read().QueryContext(ctx, `SELECT thread_id FROM gmail_thread_links`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out[t] = true
	}
	return out, rows.Err()
}

// candidacies loads every tracker row, archived ones included, because an
// archived row's refusal is what sends a signal to review. A failed load fails
// the pass: an empty list would be read as "no viable candidate".
func (p *Pipeline) candidacies(ctx context.Context) ([]link.Candidacy, error) {
	var out []link.Candidacy
	for page := 1; ; page++ {
		res, err := p.Tracker.List(ctx, tracker.ListQuery{Archived: true, Page: page, PageSize: 100})
		if err != nil {
			return nil, fmt.Errorf("load the candidacies: %w", err)
		}
		for _, e := range res.Entries {
			out = append(out, link.Candidacy{ID: e.ID, Company: e.Company, Role: e.Role})
		}
		if len(res.Entries) == 0 || len(out) >= res.Total {
			return out, nil
		}
	}
}

// linkable reports whether a signal takes part in linking: incoming mail of
// tier actionable or review.
func linkable(s SignalRow) bool {
	return s.Direction == classify.DirIncoming && (s.Tier == classify.TierActionable || s.Tier == classify.TierReview)
}

func reasonList(csv string) []string {
	if csv == "" {
		return nil
	}
	return strings.Split(csv, ",")
}

// Resolve decides the link of every signal that has none or whose decision is
// review or unlinked. An auto link and a link the user confirmed are never
// touched. It returns how many signals newly linked.
func (p *Pipeline) Resolve(ctx context.Context) (int, error) {
	cands, err := p.candidacies(ctx)
	if err != nil {
		return 0, err
	}
	sigs, err := p.Repo.Signals(ctx)
	if err != nil {
		return 0, err
	}
	links, err := p.Repo.linkRows(ctx)
	if err != nil {
		return 0, err
	}
	threads, err := p.Repo.threadLinks(ctx, links)
	if err != nil {
		return 0, err
	}
	confirmed, err := p.Repo.confirmedThreads(ctx)
	if err != nil {
		return 0, err
	}
	pool := link.NewPool(cands)
	now := stamp(p.Repo.now())
	// Scoring is the slow part and the database has one writer, so every
	// decision is made first, from what was read, and only the changed rows
	// are written. The upsert leaves a link the user confirmed meanwhile alone.
	type decided struct {
		s       SignalRow
		d       link.Decision
		reasons string
		sticky  bool
	}
	var changed []decided
	linked := 0
	for _, s := range sigs {
		if !linkable(s) {
			continue
		}
		cur, has := links[s.MessageID]
		if has && (cur.decision == link.Auto || cur.confirmed) {
			continue
		}
		d := link.Decide(link.Signal{Company: s.Employer, Role: s.Role, Subject: s.Subject, Sender: s.From, ReviewReasons: reasonList(s.ReviewReasons)},
			pool, threads[s.ThreadID])
		sticky := d.Kind == link.Auto && confirmed[s.ThreadID]
		reasons := strings.Join(d.Reasons, ",")
		if has && cur.decision == d.Kind && cur.candidacyID == d.CandidacyID && cur.reasons == reasons && cur.score == d.Score && cur.runnerUp == d.RunnerUp {
			continue
		}
		changed = append(changed, decided{s, d, reasons, sticky})
		if d.Kind == link.Auto {
			linked++
			if !slices.Contains(threads[s.ThreadID], d.CandidacyID) {
				threads[s.ThreadID] = append(threads[s.ThreadID], d.CandidacyID)
			}
		}
	}
	if len(changed) == 0 {
		return 0, nil
	}
	err = p.Repo.st.Write(ctx, func(tx *sql.Tx) error {
		for _, c := range changed {
			if _, err := tx.ExecContext(ctx, `INSERT INTO gmail_links (message_id, thread_id, decision, candidacy_id, score, runner_up, reasons, linker_version, confirmed, decided_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(message_id) DO UPDATE SET decision = excluded.decision, candidacy_id = excluded.candidacy_id, score = excluded.score,
				runner_up = excluded.runner_up, reasons = excluded.reasons, linker_version = excluded.linker_version, confirmed = excluded.confirmed, decided_at = excluded.decided_at
				WHERE gmail_links.confirmed = 0`,
				c.s.MessageID, c.s.ThreadID, c.d.Kind, nullID(c.d.CandidacyID), c.d.Score, c.d.RunnerUp, c.reasons, link.Version, b2i(c.sticky), now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return linked, nil
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// LinkItem is one entry of the link review queue.
type LinkItem struct {
	SignalID, ThreadID     string
	Type, Tier             string
	Employer, Role         string
	Subject                string
	Received               time.Time
	Decision               string
	CandidacyID            int64
	Score, RunnerUp        float64
	Reasons                []string
	OutcomeReason          string
	OutcomeFrom, OutcomeTo string
}

// MaxLinkItems bounds the review queue answer.
const MaxLinkItems = 200

// LinkReview lists the review queue, newest mail first. decision review also
// holds the signals whose status change the tracker refused.
func (r *Repo) LinkReview(ctx context.Context, decision string) ([]LinkItem, error) {
	if decision != link.Review && decision != link.Unlinked {
		return nil, fmt.Errorf("gmail: decision must be review or unlinked")
	}
	sigs, err := r.Signals(ctx)
	if err != nil {
		return nil, err
	}
	links, err := r.linkRows(ctx)
	if err != nil {
		return nil, err
	}
	outcomes, err := r.refusedOutcomes(ctx)
	if err != nil {
		return nil, err
	}
	var out []LinkItem
	for _, s := range sigs {
		if s.Tier == classify.TierSuppressed || s.Direction != classify.DirIncoming {
			continue
		}
		l, has := links[s.MessageID]
		o, refused := outcomes[s.MessageID]
		it := LinkItem{SignalID: s.MessageID, ThreadID: s.ThreadID, Type: s.Type, Tier: s.Tier, Employer: s.Employer, Role: s.Role,
			Subject: s.Subject, Received: time.UnixMilli(s.ReceivedMS).UTC()}
		switch {
		case has && l.decision == decision:
			it.Decision, it.CandidacyID, it.Score, it.RunnerUp, it.Reasons = l.decision, l.candidacyID, l.score, l.runnerUp, reasonList(l.reasons)
		case decision == link.Review && refused:
			it.Decision, it.CandidacyID = link.Review, o.candidacyID
			it.Reasons = []string{"tracker_refused"}
			it.OutcomeReason, it.OutcomeFrom, it.OutcomeTo = o.reason, o.from, o.to
		default:
			continue
		}
		out = append(out, it)
	}
	slices.SortStableFunc(out, func(a, b LinkItem) int { return b.Received.Compare(a.Received) })
	if len(out) > MaxLinkItems {
		out = out[:MaxLinkItems]
	}
	return out, nil
}

// signalOf finds a visible signal by message id.
func (r *Repo) signalOf(ctx context.Context, id string) (SignalRow, bool, error) {
	sigs, err := r.signalsWhere(ctx, "d.message_id = ?", id)
	if err != nil || len(sigs) == 0 {
		return SignalRow{}, false, err
	}
	return sigs[0], true, nil
}

// ConfirmResult is what a confirmation changed.
type ConfirmResult struct {
	ThreadID    string
	CandidacyID int64
	// StatusOutcome is the tracker outcome of the confirmed signal, when it has one.
	Outcome *StatusOutcome
}

// ConfirmLink links the whole thread of a signal to a candidacy. The link is
// final: no later pass changes it. The status update of that signal is
// attempted at once, as the Gmail actor, and so is every update of the thread
// the tracker refused before: the confirmation withdraws those refusals.
func (p *Pipeline) ConfirmLink(ctx context.Context, messageID string, candidacyID int64) (ConfirmResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok, err := p.Repo.signalOf(ctx, messageID)
	if err != nil {
		return ConfirmResult{}, err
	}
	if !ok {
		return ConfirmResult{}, ErrNoSignal
	}
	if s.Tier == classify.TierSuppressed || s.Direction != classify.DirIncoming {
		return ConfirmResult{}, ErrNoLinkable
	}
	if _, err := p.Tracker.Get(ctx, candidacyID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ConfirmResult{}, fmt.Errorf("%w: no such candidacy", ErrNoLinkable)
		}
		return ConfirmResult{}, err
	}
	sigs, err := p.Repo.signalsWhere(ctx, "e.thread_id = ?", s.ThreadID)
	if err != nil {
		return ConfirmResult{}, err
	}
	now := stamp(p.Repo.now())
	err = p.Repo.st.Write(ctx, func(tx *sql.Tx) error {
		for _, t := range sigs {
			if _, err := tx.ExecContext(ctx, `DELETE FROM gmail_outcomes WHERE message_id = ? AND result = 'review'`, t.MessageID); err != nil {
				return err
			}
		}
		var have int64
		switch err := tx.QueryRowContext(ctx, `SELECT candidacy_id FROM gmail_thread_links WHERE thread_id = ?`, s.ThreadID).Scan(&have); {
		case err == nil && have == candidacyID:
			return nil
		case err == nil:
			return fmt.Errorf("%w: the thread is already confirmed for another candidacy", ErrNoLinkable)
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO gmail_thread_links (thread_id, candidacy_id, confirmed_at) VALUES (?, ?, ?)`, s.ThreadID, candidacyID, now); err != nil {
			return err
		}
		for _, t := range sigs {
			if !linkable(t) {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO gmail_links (message_id, thread_id, decision, candidacy_id, score, runner_up, reasons, linker_version, confirmed, decided_at)
				VALUES (?, ?, 'auto', ?, 1, 0, 'user_confirmed', ?, 1, ?)
				ON CONFLICT(message_id) DO UPDATE SET decision = 'auto', candidacy_id = excluded.candidacy_id, score = 1, runner_up = 0,
				reasons = 'user_confirmed', linker_version = excluded.linker_version, confirmed = 1, decided_at = excluded.decided_at`,
				t.MessageID, t.ThreadID, candidacyID, link.Version, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ConfirmResult{}, err
	}
	if _, err := p.ApplyOutcomes(ctx); err != nil {
		return ConfirmResult{}, err
	}
	res := ConfirmResult{ThreadID: s.ThreadID, CandidacyID: candidacyID}
	if o, ok, err := p.Repo.outcomeOf(ctx, messageID); err != nil {
		return res, err
	} else if ok {
		res.Outcome = &o
	}
	return res, nil
}

// RejectLink marks a signal as not job-related. It leaves every view and stays
// in the audit data.
func (r *Repo) RejectLink(ctx context.Context, messageID string) error {
	if _, ok, err := r.signalOf(ctx, messageID); err != nil {
		return err
	} else if !ok {
		return ErrNoSignal
	}
	return r.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO gmail_link_rejections (message_id, rejected_at) VALUES (?, ?)`, messageID, stamp(r.now()))
		return err
	})
}

// RestoreLink undoes RejectLink for one signal. It removes that rejection and
// nothing else, so the signal reads at its own tier again and returns to the
// views it left; a signal that is not rejected stays as it is.
func (r *Repo) RestoreLink(ctx context.Context, messageID string) error {
	if _, ok, err := r.signalOf(ctx, messageID); err != nil {
		return err
	} else if !ok {
		return ErrNoSignal
	}
	return r.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM gmail_link_rejections WHERE message_id = ?`, messageID)
		return err
	})
}

const keyTrackerRows = "tracker_rows_seen"

// RelinkIfRowsChanged resolves the signals still in review or unlinked again
// when a tracker row has been added since the last pass, whoever added it, and
// applies the outcomes of the links that result. It returns whether it ran.
func (p *Pipeline) RelinkIfRowsChanged(ctx context.Context) (bool, error) {
	var n, top int64
	if err := p.Repo.st.Read().QueryRowContext(ctx, `SELECT count(*), COALESCE(max(id), 0) FROM tracker_entries`).Scan(&n, &top); err != nil {
		return false, err
	}
	seen := fmt.Sprintf("%d:%d", n, top)
	if p.Repo.stateOr(ctx, keyTrackerRows) == seen {
		return false, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, err := p.Resolve(ctx); err != nil {
		return true, err
	}
	if _, err := p.ApplyOutcomes(ctx); err != nil {
		return true, err
	}
	return true, p.Repo.SetState(ctx, map[string]string{keyTrackerRows: seen})
}
