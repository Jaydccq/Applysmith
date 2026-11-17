package learn

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"applysmith/internal/answerbank/match"
)

// ConflictError is a decision the current state refuses; Reason is a stable
// code the caller can branch on.
type ConflictError struct {
	Reason  string
	Message string
}

func (e *ConflictError) Error() string { return e.Message }

// Reasons of a refused decision.
const (
	ReasonNotPending       = "not-pending"
	ReasonDuplicateID      = "duplicate-id"
	ReasonExampleCollision = "example-collision"
	ReasonPinned           = "pinned"
	ReasonNotRejected      = "not-rejected"
	ReasonNotApproved      = "not-approved"
	ReasonEntryChanged     = "entry-changed"
	ReasonTakesNoAnswer    = "takes-no-answer"
)

// OutcomeTakenBack is the outcome of the event a take-back of an approval
// records (Unapprove).
const OutcomeTakenBack = "taken-back"

func refuse(reason, format string, args ...any) error {
	return &ConflictError{Reason: reason, Message: fmt.Sprintf(format, args...)}
}

// Decision is the outcome of approving or rejecting a candidate.
type Decision struct {
	Candidate Candidate
	// EntryID is the entry the approval created or extended.
	EntryID string
}

// Approve applies a pending candidate to the user's bank through the bank
// service, validated and checked against the file's revision, and marks the
// candidate approved only after that write succeeded; any failure leaves it
// pending. The bank file and the database cannot share a transaction, so a
// failure between the two writes leaves a pending candidate the bank already
// holds: approving it again changes nothing in the bank and marks it approved,
// and Reconcile does the same without a request. Only an explicit call approves
// a candidate; nothing else here runs on its own. A non-empty answer is the
// user's own, stored in place of the proposed one in the same write; it
// becomes the candidate's proposal, so a take-back knows what was written.
func (s *Service) Approve(ctx context.Context, id int64, answer string) (Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := loadCandidate(ctx, s.st.Read(), id)
	if err != nil {
		return Decision{}, err
	}
	if c.State != StatePending {
		return Decision{}, refuse(ReasonNotPending, "candidate %d is %s, not pending", id, c.State)
	}
	if answer = strings.TrimSpace(answer); answer != "" {
		if c.Type != TypeNewEntry {
			return Decision{}, refuse(ReasonTakesNoAnswer, "this suggestion adds a phrasing and takes no answer")
		}
		c.ProposedAnswer = answer
	}
	ticks, err := s.ticksFor(ctx, c)
	if err != nil {
		return Decision{}, err
	}
	loaded, err := s.bank.File()
	if err != nil {
		return Decision{}, err
	}
	now := s.now()
	after, entryID, changed, err := applyCandidate(loaded.Bank, c, now, ticks)
	if err != nil {
		return Decision{}, err
	}
	if changed {
		if _, err := s.bank.SaveChange(loaded.Bank, after, loaded.Revision); err != nil {
			return Decision{}, err
		}
	}
	return s.decide(ctx, c, StateApproved, KindCandidateApproved, entryID, now)
}

// Reconcile marks approved every pending candidate whose proposal the bank
// already holds: the case of an approval that wrote the bank and stopped before
// it marked the candidate. A candidate the bank does not hold, or cannot take
// unchanged, is left alone. It returns the number marked.
func (s *Service) Reconcile(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, err := s.stored(ctx, StatePending)
	if err != nil || len(pending) == 0 {
		return 0, err
	}
	loaded, err := s.bank.File()
	if err != nil {
		return 0, err
	}
	now, n := s.now(), 0
	for _, c := range pending {
		_, entryID, changed, err := applyCandidate(loaded.Bank, c, now, nil)
		if err != nil || changed {
			continue
		}
		if _, err := s.decide(ctx, c, StateApproved, KindCandidateApproved, entryID, now); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Reject marks a pending candidate rejected. It returns only after the support
// has doubled, or when the user undoes the rejection (Restore).
func (s *Service) Reject(ctx context.Context, id int64) (Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := loadCandidate(ctx, s.st.Read(), id)
	if err != nil {
		return Decision{}, err
	}
	if c.State != StatePending {
		return Decision{}, refuse(ReasonNotPending, "candidate %d is %s, not pending", id, c.State)
	}
	return s.decide(ctx, c, StateRejected, KindCandidateRejected, c.rejectedEntry(), s.now())
}

// rejectedEntry is the entry a rejection is recorded against.
func (c Candidate) rejectedEntry() string {
	if c.TargetEntryID != "" {
		return c.TargetEntryID
	}
	return c.EntryID
}

// Restore puts a rejected candidate back in the queue as the rejection found
// it, the undo of the rejection: pending, undecided, in its place, and
// without the candidate-rejected event, which tells of a decision the user
// took back (the event kinds have none for an undo). A candidate that is no
// longer rejected stays as it is, and so does one the bank settled since:
// restoring it would put back a stale proposal.
func (s *Service) Restore(ctx context.Context, id int64) (Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := loadCandidate(ctx, s.st.Read(), id)
	if err != nil {
		return Decision{}, err
	}
	if c.State != StateRejected || c.DecidedAt == nil {
		return Decision{}, refuse(ReasonNotRejected, "candidate %d is %s, not rejected", id, c.State)
	}
	loaded, err := s.bank.File()
	if err != nil {
		return Decision{}, err
	}
	if why := settledBy(loaded.Bank, c, s.now()); why != "" {
		return Decision{}, refuse(ReasonEntryChanged, "this suggestion no longer fits your Answer Bank (%s), so it stays rejected", why)
	}
	// The rejection's event is queued, not written, when its press returns.
	s.Flush()
	err = s.st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE answer_candidates SET state = 'pending', decided_at = NULL WHERE id = ? AND state = 'rejected'`, c.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return refuse(ReasonNotRejected, "candidate %d is no longer rejected", c.ID)
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM answer_events WHERE kind = ? AND at = ? AND entry_id = ?`,
			KindCandidateRejected, stamp(*c.DecidedAt), shapedID(c.rejectedEntry(), entryIDRE))
		return err
	})
	if err != nil {
		return Decision{}, err
	}
	c, err = loadCandidate(ctx, s.st.Read(), c.ID)
	return Decision{Candidate: c}, err
}

// Unapprove takes an approval back, the undo of Approve: it removes what the
// approval wrote, the new entry or the phrasing, and puts the candidate back in
// the queue, pending and undecided. One the bank changed since (the entry
// edited, renamed or deleted, the phrasing gone, the entry pinned) is refused
// and stays approved: a take-back never removes the user's own work. The
// candidate is set pending before the bank is written and set back when the
// write fails. The take-back is recorded as an event of the approval's kind
// with the outcome taken-back.
func (s *Service) Unapprove(ctx context.Context, id int64) (Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := loadCandidate(ctx, s.st.Read(), id)
	if err != nil {
		return Decision{}, err
	}
	if c.State != StateApproved || c.DecidedAt == nil {
		return Decision{}, refuse(ReasonNotApproved, "candidate %d is %s, not approved", id, c.State)
	}
	ticks, err := s.ticksFor(ctx, c)
	if err != nil {
		return Decision{}, err
	}
	loaded, err := s.bank.File()
	if err != nil {
		return Decision{}, err
	}
	after, entryID, why := takeBack(loaded.Bank, c, ticks)
	if why != "" {
		return Decision{}, refuse(ReasonEntryChanged, "%s, so the approval stays", why)
	}
	mark := func(from, to string, at any) error {
		return s.st.Write(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, `UPDATE answer_candidates SET state = ?, decided_at = ? WHERE id = ? AND state = ?`, to, at, c.ID, from)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return refuse(ReasonNotApproved, "candidate %d is no longer approved", c.ID)
			}
			return nil
		})
	}
	if err := mark(StateApproved, StatePending, nil); err != nil {
		return Decision{}, err
	}
	if _, err := s.bank.SaveChange(loaded.Bank, after, loaded.Revision); err != nil {
		_ = mark(StatePending, StateApproved, stamp(*c.DecidedAt))
		return Decision{}, err
	}
	s.Record(ctx, Event{Kind: KindCandidateApproved, At: s.now(), Host: firstHost(c), Question: c.SeedQuestion, EntryID: entryID, Outcome: OutcomeTakenBack})
	c, err = loadCandidate(ctx, s.st.Read(), c.ID)
	return Decision{Candidate: c, EntryID: entryID}, err
}

// takeBack returns the bank without what the approval of c wrote, the entry
// it touched, or why the bank no longer holds that write as it was made. ticks
// are the approval's (applyCandidate).
func takeBack(bank match.Bank, c Candidate, ticks []string) (match.Bank, string, string) {
	out := bank.Clone()
	if c.Type == TypeExtendExamples {
		at := slices.IndexFunc(out.Entries, func(e match.Entry) bool { return e.ID == c.TargetEntryID })
		if at < 0 {
			return out, "", "the entry it added a phrasing to is gone"
		}
		t := &out.Entries[at]
		i := slices.Index(t.Examples, strings.TrimSpace(c.SeedQuestion))
		switch {
		case t.Pinned:
			return out, "", "the entry it added a phrasing to was pinned since"
		case i < 0:
			return out, "", "the phrasing it added was changed since"
		}
		t.Examples = slices.Delete(slices.Clone(t.Examples), i, i+1)
		return out, t.ID, ""
	}
	at := slices.IndexFunc(out.Entries, func(e match.Entry) bool { return e.ID == c.EntryID })
	if at < 0 {
		return out, "", "the entry it added is gone"
	}
	got := out.Entries[at]
	out.Entries = slices.Delete(out.Entries, at, at+1)
	made, _, _, err := applyCandidate(out, c, time.Time{}, ticks)
	if err != nil {
		return out, "", "the entry it added was changed since"
	}
	want := made.Entries[len(made.Entries)-1]
	want.CreatedAt = got.CreatedAt
	if !reflect.DeepEqual(want, got) {
		return out, "", "the entry it added was edited since"
	}
	return out, got.ID, ""
}

// settledBy says how the bank as it stands settled the candidate's proposal,
// and is empty while approving it would still change the bank: a new entry's
// question is not answered by a curated entry, and the entry it extends or
// creates takes it.
func settledBy(bank match.Bank, c Candidate, now time.Time) string {
	if c.Type == TypeNewEntry && curatedQuestions(bank)[Normalize(c.SeedQuestion)] {
		return "an entry answers this question now"
	}
	switch _, _, changed, err := applyCandidate(bank, c, now, nil); {
	case err != nil:
		return strings.TrimPrefix(err.Error(), ErrNotFound.Error()+": ")
	case !changed:
		return "it holds this already"
	}
	return ""
}

// decide marks the candidate. The decision event is telemetry: it is queued
// after the state write and a failure to store it never fails the decision.
// updated_at stays the proposal's own, so a candidate whose decision is undone
// returns to its place in the queue.
func (s *Service) decide(ctx context.Context, c Candidate, state, kind, entryID string, now time.Time) (Decision, error) {
	err := s.st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE answer_candidates SET state = ?, decided_at = ?, proposed_answer = ? WHERE id = ? AND state = 'pending'`,
			state, stamp(now), c.ProposedAnswer, c.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return refuse(ReasonNotPending, "candidate %d is no longer pending", c.ID)
		}
		return nil
	})
	if err != nil {
		return Decision{}, err
	}
	s.Record(ctx, Event{Kind: kind, At: now, Host: firstHost(c), Question: c.SeedQuestion, EntryID: entryID, Outcome: state})
	c, err = loadCandidate(ctx, s.st.Read(), c.ID)
	return Decision{Candidate: c, EntryID: entryID}, err
}

func firstHost(c Candidate) string {
	if len(c.Hosts) > 0 {
		return c.Hosts[0]
	}
	return ""
}

// applyCandidate returns the bank with the candidate applied. changed is false
// when the bank already holds it, which makes approval idempotent. ticks, when
// not nil, make a pick-option candidate a multi-select entry that ticks them
// (ticksFor); an empty list leaves that entry for the user to fill.
func applyCandidate(bank match.Bank, c Candidate, now time.Time, ticks []string) (out match.Bank, entryID string, changed bool, err error) {
	out = bank.Clone()
	if c.Type == TypeExtendExamples {
		return applyExample(out, c)
	}
	if cur, ok := out.Find(c.EntryID); ok {
		if cur.EffectiveCreatedBy() == match.CreatedByAgent && slices.ContainsFunc(cur.Examples, func(e string) bool { return Normalize(e) == Normalize(c.SeedQuestion) }) {
			return out, c.EntryID, false, nil
		}
		return out, "", false, refuse(ReasonDuplicateID, "an entry with id %q already exists", c.EntryID)
	}
	e := match.Entry{ID: c.EntryID, Description: c.Description(), Kind: match.KindFixed}
	e.Examples = []string{c.SeedQuestion}
	e.State, e.CreatedBy, e.NeedsReview, e.CreatedAt = match.StateActive, match.CreatedByAgent, true, now.UTC().Format(time.RFC3339)
	switch {
	case c.ProposedKind == "pick-option" && ticks != nil:
		e.Kind = match.KindMultiSelect
		if len(ticks) > 0 {
			e.Selected = slices.Clone(ticks)
		}
	case c.ProposedKind == "pick-option":
		e.Kind, e.Preferred = match.KindPickOption, []string{}
		if c.ProposedAnswer != "" {
			e.Preferred, e.FallbackText = []string{c.ProposedAnswer}, c.ProposedAnswer
		}
	default:
		e.Answer = c.ProposedAnswer
	}
	e.Placeholder = c.ProposedAnswer == ""
	out.Entries = append(out.Entries, e)
	return out, e.ID, true, nil
}

// ticksFor is what a pick-option candidate seen on a group of checkboxes
// ticks: the options its answer names, an empty list when it proposes none.
// It is nil for any other candidate. An answer that names none of the options
// is refused, since its entry would tick nothing.
func (s *Service) ticksFor(ctx context.Context, c Candidate) ([]string, error) {
	if c.Type != TypeNewEntry || c.ProposedKind != "pick-option" {
		return nil, nil
	}
	pcs, err := s.CandidateContexts(ctx, []int64{c.ID})
	if err != nil {
		return nil, err
	}
	pc := pcs[c.ID]
	switch {
	case !pc.Multiple:
		return nil, nil
	case c.ProposedAnswer == "":
		return []string{}, nil
	}
	ticks := match.OptionsNamed(c.ProposedAnswer, pc.Options)
	if len(ticks) == 0 {
		return nil, refuse(ReasonNotAnOption, "name one or more of its options: %s", strings.Join(pc.Options, ", "))
	}
	return ticks, nil
}

func applyExample(bank match.Bank, c Candidate) (match.Bank, string, bool, error) {
	at := slices.IndexFunc(bank.Entries, func(e match.Entry) bool { return e.ID == c.TargetEntryID })
	if at < 0 {
		return bank, "", false, fmt.Errorf("%w: the target entry %q no longer exists", ErrNotFound, c.TargetEntryID)
	}
	target := &bank.Entries[at]
	norm := Normalize(c.SeedQuestion)
	if slices.ContainsFunc(target.Examples, func(e string) bool { return Normalize(e) == norm }) {
		return bank, target.ID, false, nil
	}
	if target.Pinned {
		return bank, "", false, refuse(ReasonPinned, "entry %q is pinned; unpin it or add the example yourself", target.ID)
	}
	for _, e := range bank.Entries {
		if e.ID == target.ID {
			continue
		}
		if Normalize(e.Description) == norm || slices.ContainsFunc(e.Examples, func(x string) bool { return Normalize(x) == norm }) {
			return bank, "", false, refuse(ReasonExampleCollision, "the phrasing is already an example of entry %q", e.ID)
		}
	}
	target.Examples = append(slices.Clone(target.Examples), strings.TrimSpace(c.SeedQuestion))
	return bank, target.ID, true, nil
}
