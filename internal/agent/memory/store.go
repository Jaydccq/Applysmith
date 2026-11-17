// Package memory is what the Ask Agent knows about the user between
// conversations: facts, operating rules and the nightly consolidation that
// proposes more of both. Nothing here reaches a gate. A fact is data, a rule
// is prompt guidance, and anything the agent or the consolidation writes
// starts as a proposal that the user must accept.
package memory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"applysmith/internal/store"
)

// Limits from the specification.
const (
	MaxFactChars   = 500
	MaxActiveFacts = 500
	MaxRuleChars   = 300
	MaxActiveRules = 30
	MaxPendingRule = 20
)

// Fact kinds, subjects, sources and trust levels.
const (
	SubjectUser        = "user"
	SubjectCompany     = "company"
	SubjectApplication = "application"

	SourceUser      = "user_declared"
	SourceExtracted = "agent_extracted"

	Trusted     = "trusted"
	NeedsReview = "needs_review"

	RuleProposed = "proposed"
	RuleActive   = "active"
	RuleRetired  = "retired"

	ReasonInstructionShaped = "instruction_shaped"
)

var kinds = map[string]bool{"preference": true, "goal": true, "constraint": true, "decision": true, "context": true, "note": true}

// Errors the tools and the dashboard map to messages.
var (
	// ErrRefused means the text was aimed at the assistant and was dropped.
	ErrRefused = errors.New("memory: the text reads as an instruction to the assistant, so it was refused")
	// ErrFull means 500 facts are already active.
	ErrFull = errors.New("memory: 500 facts are already stored; forget some first")
	// ErrTombstoned means the user forgot this text and extraction may not add it back.
	ErrTombstoned = errors.New("memory: the user forgot this fact, so it is not added again")
	// ErrRuleGrantsPermission means a rule tried to change what the agent may do.
	ErrRuleGrantsPermission = errors.New("memory: a rule is guidance only and cannot change gates, permissions or confirmations")
	// ErrInvalid marks input the store cannot accept, such as an empty text or an
	// unknown kind; the message says what to fix.
	ErrInvalid = errors.New("memory: invalid input")
	// ErrRulesFull means the pending or the active limit is reached.
	ErrRulesFull = errors.New("memory: the rule limit is reached")
)

// Fact is one stored statement about the user.
type Fact struct {
	ID         string
	Kind       string
	Subject    string
	SubjectRef string
	Body       string
	Source     string
	Trust      string
	PII        string
	Supersedes string
	ValidFrom  time.Time
	CreatedAt  time.Time
}

// NewFact is a fact to store.
type NewFact struct {
	Kind       string
	Subject    string
	SubjectRef string
	Body       string
	Source     string
}

// Outcome says what a save did.
type Outcome string

const (
	// Saved stored a fact.
	Saved Outcome = "saved"
	// Reviewed put the text in the review queue instead of storing a fact.
	Reviewed Outcome = "reviewed"
	// Duplicate found the same fact already stored.
	Duplicate Outcome = "duplicate"
)

// SaveResult is the outcome of a save, an amendment or a candidate.
type SaveResult struct {
	Outcome  Outcome
	Fact     Fact   // set for Saved and Duplicate
	ReviewID string // set for Reviewed
}

// Rule is a piece of operating guidance.
type Rule struct {
	ID        string
	Body      string
	State     string
	Source    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ReviewItem is a statement that waits for the user.
type ReviewItem struct {
	ID, Kind, Subject, SubjectRef, Body, Reason, Source string
	CreatedAt                                           time.Time
}

// Store is the memory over the workspace database.
type Store struct {
	db  *store.Store
	now func() time.Time
}

// New returns a store; now defaults to the wall clock.
func New(db *store.Store, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now}
}

const layout = "2006-01-02T15:04:05.000Z"

func stamp(t time.Time) string { return t.UTC().Format(layout) }

func parse(v string) time.Time { t, _ := time.Parse(layout, v); return t }

func hashOf(body string) string {
	sum := sha256.Sum256([]byte(normalize(body)))
	return hex.EncodeToString(sum[:])
}

// invalidInput is an ErrInvalid that keeps its own wording.
type invalidInput string

func (e invalidInput) Error() string        { return string(e) }
func (e invalidInput) Is(target error) bool { return target == ErrInvalid }

func invalidf(format string, args ...any) error { return invalidInput(fmt.Sprintf(format, args...)) }

func checkFact(f *NewFact) error {
	f.Body = strings.TrimSpace(f.Body)
	f.SubjectRef = strings.TrimSpace(f.SubjectRef)
	if f.Subject == "" {
		f.Subject = SubjectUser
	}
	if f.Kind == "" {
		f.Kind = "note"
	}
	switch {
	case f.Body == "":
		return invalidf("memory: a fact needs some text")
	case len(strings.Fields(f.Body)) < 2:
		return invalidf("memory: a fact is a statement of at least two words, not a bare token")
	case utf8.RuneCountInString(f.Body) > MaxFactChars:
		return invalidf("memory: a fact is at most %d characters", MaxFactChars)
	case !kinds[f.Kind]:
		return invalidf("memory: %q is not a fact kind", f.Kind)
	case f.Subject != SubjectUser && f.Subject != SubjectCompany && f.Subject != SubjectApplication:
		return invalidf("memory: %q is not a fact subject", f.Subject)
	case f.Subject != SubjectUser && f.SubjectRef == "":
		return invalidf("memory: a %s fact names the %s", f.Subject, f.Subject)
	case f.Subject == SubjectUser:
		f.SubjectRef = ""
	}
	return nil
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Store) count(ctx context.Context, q execer, query string, args ...any) (int, error) {
	var n int
	return n, q.QueryRowContext(ctx, query, args...).Scan(&n)
}

// Save stores a fact. Text aimed at the assistant is refused; text that
// tells the agent how to answer goes to the review queue; a fact the user
// declared is trusted and one the agent extracted needs review. A repeat of an
// active fact is not stored twice, and an extracted fact the user once forgot
// is discarded.
func (s *Store) Save(ctx context.Context, nf NewFact) (SaveResult, error) {
	if nf.Source == "" {
		nf.Source = SourceUser
	}
	if err := checkFact(&nf); err != nil {
		return SaveResult{}, err
	}
	if InjectionShaped(nf.Body) {
		return SaveResult{}, ErrRefused
	}
	var res SaveResult
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		if InstructionShaped(nf.Body) {
			id := store.NewID("rev")
			_, err := tx.ExecContext(ctx, `INSERT INTO memory_review (id, kind, subject, subject_ref, body, reason, source, created_at) VALUES (?,?,?,?,?,?,?,?)`,
				id, nf.Kind, nf.Subject, nf.SubjectRef, nf.Body, ReasonInstructionShaped, nf.Source, stamp(s.now()))
			res = SaveResult{Outcome: Reviewed, ReviewID: id}
			return err
		}
		h := hashOf(nf.Body)
		if f, ok, err := activeByHash(ctx, tx, h, nf.Subject, nf.SubjectRef); err != nil || ok {
			res = SaveResult{Outcome: Duplicate, Fact: f}
			return err
		}
		if nf.Source == SourceExtracted {
			if n, err := s.count(ctx, tx, `SELECT count(*) FROM memory_tombstones WHERE hash = ?`, h); err != nil || n > 0 {
				if err == nil {
					err = ErrTombstoned
				}
				return err
			}
		}
		if n, err := s.count(ctx, tx, `SELECT count(*) FROM memory_facts WHERE status = 'active'`); err != nil || n >= MaxActiveFacts {
			if err == nil {
				err = ErrFull
			}
			return err
		}
		f, err := s.insert(ctx, tx, nf, "")
		res = SaveResult{Outcome: Saved, Fact: f}
		return err
	})
	return res, err
}

func (s *Store) insert(ctx context.Context, tx execer, nf NewFact, supersedes string) (Fact, error) {
	now := s.now()
	trust := NeedsReview
	if nf.Source == SourceUser {
		trust = Trusted
	}
	f := Fact{ID: store.NewID("fact"), Kind: nf.Kind, Subject: nf.Subject, SubjectRef: nf.SubjectRef, Body: nf.Body, Source: nf.Source,
		Trust: trust, PII: piiLevel(nf.Body), Supersedes: supersedes, ValidFrom: now, CreatedAt: now}
	_, err := tx.ExecContext(ctx, `INSERT INTO memory_facts (id, kind, subject, subject_ref, body, body_hash, source, trust, pii, supersedes, valid_from, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, f.ID, f.Kind, f.Subject, f.SubjectRef, f.Body, hashOf(f.Body), f.Source, f.Trust, f.PII, f.Supersedes, stamp(now), stamp(now))
	return f, err
}

const factColumns = `id, kind, subject, subject_ref, body, source, trust, pii, supersedes, valid_from, created_at`

func scanFact(r interface{ Scan(...any) error }) (Fact, error) {
	var f Fact
	var from, created string
	if err := r.Scan(&f.ID, &f.Kind, &f.Subject, &f.SubjectRef, &f.Body, &f.Source, &f.Trust, &f.PII, &f.Supersedes, &from, &created); err != nil {
		return Fact{}, err
	}
	f.ValidFrom, f.CreatedAt = parse(from), parse(created)
	return f, nil
}

func activeByHash(ctx context.Context, q execer, hash, subject, ref string) (Fact, bool, error) {
	f, err := scanFact(q.QueryRowContext(ctx, `SELECT `+factColumns+` FROM memory_facts WHERE status = 'active' AND body_hash = ? AND subject = ? AND subject_ref = ?`, hash, subject, ref))
	if errors.Is(err, sql.ErrNoRows) {
		return Fact{}, false, nil
	}
	return f, err == nil, err
}

// Amend replaces an active fact with a new version and marks the old one
// superseded. The same screening as a save applies; a text that would go to
// review leaves the fact as it was.
func (s *Store) Amend(ctx context.Context, id, body string) (SaveResult, error) {
	var res SaveResult
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		old, err := scanFact(tx.QueryRowContext(ctx, `SELECT `+factColumns+` FROM memory_facts WHERE id = ? AND status = 'active'`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: no active fact %s", store.ErrNotFound, id)
		}
		if err != nil {
			return err
		}
		nf := NewFact{Kind: old.Kind, Subject: old.Subject, SubjectRef: old.SubjectRef, Body: body, Source: SourceUser}
		if err := checkFact(&nf); err != nil {
			return err
		}
		if InjectionShaped(nf.Body) {
			return ErrRefused
		}
		if InstructionShaped(nf.Body) {
			rid := store.NewID("rev")
			_, err := tx.ExecContext(ctx, `INSERT INTO memory_review (id, kind, subject, subject_ref, body, reason, source, created_at) VALUES (?,?,?,?,?,?,?,?)`,
				rid, nf.Kind, nf.Subject, nf.SubjectRef, nf.Body, ReasonInstructionShaped, nf.Source, stamp(s.now()))
			res = SaveResult{Outcome: Reviewed, ReviewID: rid}
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE memory_facts SET status = 'superseded' WHERE id = ?`, id); err != nil {
			return err
		}
		f, err := s.insert(ctx, tx, nf, id)
		res = SaveResult{Outcome: Saved, Fact: f}
		return err
	})
	return res, err
}

// Forget deletes a fact with every earlier version of it and keeps only the
// hash of each text, so extraction cannot add it again.
func (s *Store) Forget(ctx context.Context, id string) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		cur := id
		first := true
		for cur != "" {
			var hash, prev string
			err := tx.QueryRowContext(ctx, `SELECT body_hash, supersedes FROM memory_facts WHERE id = ?`, cur).Scan(&hash, &prev)
			if errors.Is(err, sql.ErrNoRows) && first {
				return fmt.Errorf("%w: no fact %s", store.ErrNotFound, id)
			}
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO memory_tombstones (hash, created_at) VALUES (?,?)`, hash, stamp(s.now())); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM memory_facts WHERE id = ?`, cur); err != nil {
				return err
			}
			cur, first = prev, false
		}
		return nil
	})
}

// Facts lists the active facts, newest first. trust filters to one level; an
// empty trust lists both.
func (s *Store) Facts(ctx context.Context, trust string) ([]Fact, error) {
	q, args := `SELECT `+factColumns+` FROM memory_facts WHERE status = 'active'`, []any{}
	if trust != "" {
		q, args = q+` AND trust = ?`, append(args, trust)
	}
	rows, err := s.db.Read().QueryContext(ctx, q+` ORDER BY created_at DESC, id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Fact
	for rows.Next() {
		f, err := scanFact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ApproveFact makes a candidate fact trusted. It is the user's act: no tool
// calls it.
func (s *Store) ApproveFact(ctx context.Context, id string) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE memory_facts SET trust = 'trusted' WHERE id = ? AND status = 'active'`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: no active fact %s", store.ErrNotFound, id)
		}
		return nil
	})
}

// Review lists the statements that wait for the user, oldest first.
func (s *Store) Review(ctx context.Context) ([]ReviewItem, error) {
	rows, err := s.db.Read().QueryContext(ctx, `SELECT id, kind, subject, subject_ref, body, reason, source, created_at FROM memory_review ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReviewItem
	for rows.Next() {
		var r ReviewItem
		var at string
		if err := rows.Scan(&r.ID, &r.Kind, &r.Subject, &r.SubjectRef, &r.Body, &r.Reason, &r.Source, &at); err != nil {
			return nil, err
		}
		r.CreatedAt = parse(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// DismissReview removes a statement from the review queue.
func (s *Store) DismissReview(ctx context.Context, id string) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM memory_review WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: no review item %s", store.ErrNotFound, id)
		}
		return nil
	})
}
