package learn

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"applysmith/internal/answerbank/match"
)

// Candidate states and types.
const (
	StatePending  = "pending"
	StateApproved = "approved"
	StateRejected = "rejected"

	TypeNewEntry       = "new-entry"
	TypeExtendExamples = "extend-examples"
)

// Candidate sources, by the evidence that supports them.
const (
	SourceAbstain   = "abstain"
	SourceComposed  = "composed"
	SourceManual    = "manual"
	SourceModelFill = "model-fill"
)

// Candidate is a proposal the user decides on. A new-entry candidate proposes
// an entry; an extend-examples candidate proposes the seed question as one more
// example of an existing entry and never an answer.
type Candidate struct {
	ID            int64
	Key           string
	Type          string
	State         string
	Source        string
	TargetEntryID string
	// EntryID is the id the proposed entry gets on approval.
	EntryID      string
	SeedQuestion string
	Questions    []string
	Hosts        []string
	// ProposedKind is "fixed" or "pick-option" for a new entry.
	ProposedKind   string
	ProposedAnswer string
	// LongForm marks an answer withheld from the proposal because it is prose.
	LongForm    bool
	Flags       []string
	Support     int
	FirstSeenAt time.Time
	LastSeenAt  time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DecidedAt   *time.Time
	// Review is the model's sort of a waiting new entry (ReviewPending), and
	// ReviewedAt when it was stored; nil while it is not reviewed.
	Review     Review
	ReviewedAt *time.Time
}

// Review is the sort of one suggestion (Options.Review): Verdict says whether
// it is worth an entry and Reason why, in one line the user reads. Draft is an
// answer drafted from the user's own profile and CV for a reusable one, with
// the Cited facts it rests on; empty when none was drafted. The verdicts are the
// reviewer's; the store refuses any other.
type Review struct {
	Verdict string
	Reason  string
	Draft   string
	Cited   []string
}

// Description is the proposed entry description: the first 200 characters of
// the seed question.
func (c Candidate) Description() string { return clampRunes(c.SeedQuestion, 200) }

const candidateColumns = `id, key, type, state, source, target_entry_id, entry_id, seed_question, questions_json, hosts_json, proposed_kind,
	proposed_answer, long_form, flags_json, support_count, first_seen_at, last_seen_at, created_at, updated_at, decided_at,
	review_verdict, review_reason, review_draft, review_cited_json, reviewed_at`

type scanner interface{ Scan(dest ...any) error }

func scanCandidate(r scanner) (Candidate, error) {
	var c Candidate
	var qs, hs, fl, first, last, created, updated, cited string
	var decided, reviewed sql.NullString
	if err := r.Scan(&c.ID, &c.Key, &c.Type, &c.State, &c.Source, &c.TargetEntryID, &c.EntryID, &c.SeedQuestion, &qs, &hs, &c.ProposedKind,
		&c.ProposedAnswer, &c.LongForm, &fl, &c.Support, &first, &last, &created, &updated, &decided,
		&c.Review.Verdict, &c.Review.Reason, &c.Review.Draft, &cited, &reviewed); err != nil {
		return c, err
	}
	_ = json.Unmarshal([]byte(qs), &c.Questions)
	_ = json.Unmarshal([]byte(hs), &c.Hosts)
	_ = json.Unmarshal([]byte(fl), &c.Flags)
	_ = json.Unmarshal([]byte(cited), &c.Review.Cited)
	c.Questions, c.Hosts, c.Flags, c.Review.Cited = nonNilStrings(c.Questions), nonNilStrings(c.Hosts), nonNilStrings(c.Flags), nonNilStrings(c.Review.Cited)
	c.FirstSeenAt, c.LastSeenAt, c.CreatedAt, c.UpdatedAt = parseStamp(first), parseStamp(last), parseStamp(created), parseStamp(updated)
	if decided.Valid {
		t := parseStamp(decided.String)
		c.DecidedAt = &t
	}
	if reviewed.Valid {
		t := parseStamp(reviewed.String)
		c.ReviewedAt = &t
	}
	return c, nil
}

// Candidates lists the candidates, pending first, then approved, then
// rejected, the most recently updated (decided, once decided) first inside
// each. state filters when it is not empty. A candidate whose question is no
// question (a placeholder option, the label of a repeated-row field, a phone
// extension or a search prompt, IsNonQuestion) or one the model writes on
// every application is not listed, nor a waiting new entry whose
// question the bank now answers: a proposal stored before these were excluded,
// or before an entry came to answer it, stays out of view.
func (s *Service) Candidates(ctx context.Context, state string) ([]Candidate, error) {
	all, err := s.stored(ctx, state)
	if err != nil {
		return nil, err
	}
	loaded, bankErr := s.bank.Bank()
	// One matcher reads the bank for every candidate, so the bank is indexed
	// once per list, not once per waiting question.
	m := match.New(loaded.Bank, match.DefaultStages())
	out := []Candidate{}
	for _, c := range all {
		if IsNonQuestion(c.SeedQuestion) || match.ModelWritten(c.SeedQuestion, c.ProposedKind == "pick-option", false) {
			continue
		}
		if bankErr == nil && c.State == StatePending && c.Type == TypeNewEntry && s.bankAnswers(m, c) {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// bankAnswers reports whether the matcher, over the bank alone, fills the
// candidate's question as a free-text box on the host it was last seen on.
func (s *Service) bankAnswers(m *match.Matcher, c Candidate) bool {
	host := ""
	if len(c.Hosts) > 0 {
		host = c.Hosts[len(c.Hosts)-1]
	}
	return s.route(m, match.Facts{}, host, match.Posting{}, match.Question{Selector: "q", Text: c.SeedQuestion, Kind: match.KindText}).Fills()
}

// ReviewPending has Options.Review sort each waiting new-entry candidate that
// Candidates lists and that is not reviewed yet, one at a time, and stores each
// review in a write of its own, so no lock or transaction is held across a
// model call. A failed review leaves its candidate unreviewed, listed as
// before, for a later pass. An extend-examples candidate proposes no answer and
// is not reviewed. A call while a review runs returns at once and has the
// running one look again when it ends, so no candidate is reviewed twice. It
// returns how many it stored.
func (s *Service) ReviewPending(ctx context.Context) (int, error) {
	if s.review == nil {
		return 0, nil
	}
	s.revMu.Lock()
	if s.revRunning {
		s.revAgain = true
		s.revMu.Unlock()
		return 0, nil
	}
	s.revRunning = true
	s.revMu.Unlock()
	n := 0
	for {
		k, err := s.reviewPass(ctx)
		n += k
		s.revMu.Lock()
		again := s.revAgain && err == nil
		s.revAgain, s.revRunning = false, again
		s.revMu.Unlock()
		if !again {
			return n, err
		}
	}
}

func (s *Service) reviewPass(ctx context.Context) (int, error) {
	waiting, err := s.Candidates(ctx, StatePending)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range waiting {
		if c.Type != TypeNewEntry || c.ReviewedAt != nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return n, err
		}
		r, err := s.review(ctx, c)
		if err != nil {
			s.log.Warn("answer-bank suggestion left unreviewed", "candidate", c.ID, "error", err.Error())
			continue
		}
		stored, err := s.storeReview(ctx, c.ID, r)
		if err != nil {
			return n, err
		}
		if stored {
			n++
		}
	}
	if n > 0 && s.reviewed != nil {
		s.reviewed()
	}
	return n, nil
}

// storeReview writes the review of a candidate that is still waiting and not
// reviewed: one decided or reviewed while the model answered keeps its state.
func (s *Service) storeReview(ctx context.Context, id int64, r Review) (bool, error) {
	cited, _ := json.Marshal(nonNilStrings(r.Cited))
	var n int64
	err := s.st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE answer_candidates SET review_verdict = ?, review_reason = ?, review_draft = ?, review_cited_json = ?, reviewed_at = ?
			WHERE id = ? AND state = 'pending' AND type = 'new-entry' AND reviewed_at IS NULL`, r.Verdict, r.Reason, r.Draft, string(cited), stamp(s.now()), id)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return n == 1, err
}

// stored is every stored candidate in the order Candidates lists them, the
// hidden ones too: generation and reconciliation act on the rows themselves.
func (s *Service) stored(ctx context.Context, state string) ([]Candidate, error) {
	q := `SELECT ` + candidateColumns + ` FROM answer_candidates`
	var args []any
	if state != "" {
		q += ` WHERE state = ?`
		args = append(args, state)
	}
	q += ` ORDER BY CASE state WHEN 'pending' THEN 0 WHEN 'approved' THEN 1 ELSE 2 END, coalesce(decided_at, updated_at) DESC, id DESC`
	rows, err := s.st.Read().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Candidate{}
	for rows.Next() {
		c, err := scanCandidate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func loadCandidate(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id int64) (Candidate, error) {
	c, err := scanCandidate(q.QueryRowContext(ctx, `SELECT `+candidateColumns+` FROM answer_candidates WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return c, ErrNotFound
	}
	return c, err
}

// evidenceRef names one supporting row.
type evidenceRef struct {
	fill bool
	id   int64
}

func insertCandidate(ctx context.Context, tx *sql.Tx, c Candidate, refs []evidenceRef) (int64, error) {
	qs, _ := json.Marshal(nonNilStrings(c.Questions))
	hs, _ := json.Marshal(nonNilStrings(c.Hosts))
	fl, _ := json.Marshal(nonNilStrings(c.Flags))
	res, err := tx.ExecContext(ctx, `INSERT INTO answer_candidates (key, type, state, source, target_entry_id, entry_id, seed_question, questions_json, hosts_json,
		proposed_kind, proposed_answer, long_form, flags_json, support_count, first_seen_at, last_seen_at, created_at, updated_at)
		VALUES (?, ?, 'pending', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Key, c.Type, c.Source, c.TargetEntryID, c.EntryID, c.SeedQuestion, string(qs), string(hs), c.ProposedKind, c.ProposedAnswer, c.LongForm,
		string(fl), len(refs), stamp(c.FirstSeenAt), stamp(c.LastSeenAt), stamp(c.CreatedAt), stamp(c.UpdatedAt))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, addEvidence(ctx, tx, id, refs)
}

func addEvidence(ctx context.Context, tx *sql.Tx, id int64, refs []evidenceRef) error {
	for _, r := range refs {
		col := "event_id"
		if r.fill {
			col = "fill_id"
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO answer_candidate_evidence (candidate_id, `+col+`) VALUES (?, ?)`, id, r.id); err != nil {
			return err
		}
	}
	return nil
}

// updatePending writes a refreshed pending candidate and recounts its support
// from the evidence rows it holds.
func updatePending(ctx context.Context, tx *sql.Tx, c Candidate, refs []evidenceRef, replace bool) error {
	if replace {
		if _, err := tx.ExecContext(ctx, `DELETE FROM answer_candidate_evidence WHERE candidate_id = ?`, c.ID); err != nil {
			return err
		}
	}
	if err := addEvidence(ctx, tx, c.ID, refs); err != nil {
		return err
	}
	return writeProposal(ctx, tx, c)
}

// reproposeRejected returns a rejected candidate to pending on fresh evidence,
// to be reviewed again.
func reproposeRejected(ctx context.Context, tx *sql.Tx, c Candidate, refs []evidenceRef) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM answer_candidate_evidence WHERE candidate_id = ?`, c.ID); err != nil {
		return err
	}
	if err := addEvidence(ctx, tx, c.ID, refs); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE answer_candidates SET state = 'pending', decided_at = NULL, review_verdict = '', review_reason = '', review_draft = '',
		review_cited_json = '[]', reviewed_at = NULL WHERE id = ?`, c.ID); err != nil {
		return err
	}
	return writeProposal(ctx, tx, c)
}

func writeProposal(ctx context.Context, tx *sql.Tx, c Candidate) error {
	qs, _ := json.Marshal(nonNilStrings(c.Questions))
	hs, _ := json.Marshal(nonNilStrings(c.Hosts))
	fl, _ := json.Marshal(nonNilStrings(c.Flags))
	_, err := tx.ExecContext(ctx, `UPDATE answer_candidates SET source = ?, seed_question = ?, questions_json = ?, hosts_json = ?, proposed_kind = ?,
		proposed_answer = ?, long_form = ?, flags_json = ?, first_seen_at = ?, last_seen_at = ?, updated_at = ?,
		support_count = (SELECT count(*) FROM answer_candidate_evidence WHERE candidate_id = answer_candidates.id) WHERE id = ?`,
		c.Source, c.SeedQuestion, string(qs), string(hs), c.ProposedKind, c.ProposedAnswer, c.LongForm, string(fl),
		stamp(c.FirstSeenAt), stamp(c.LastSeenAt), stamp(c.UpdatedAt), c.ID)
	return err
}

// union appends the items of b that a lacks, keeping order.
func union(a, b []string) []string {
	out := slices.Clone(a)
	for _, s := range b {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// longest returns the longest text; the first one wins a tie, since a longer
// question carries the richer token set.
func longest(texts []string) string {
	best := ""
	for _, t := range texts {
		if len([]rune(t)) > len([]rune(best)) {
			best = t
		}
	}
	return best
}

// mintEntryID is custom-<slug>-<6 hex>: the normalized question with hyphens
// for spaces, cut to 32 characters ("entry" when nothing is left), and six
// random hex digits.
func (s *Service) mintEntryID(question string) (string, error) {
	slug := clampRunes(strings.ReplaceAll(Normalize(question), " ", "-"), 32)
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "entry"
	}
	var b [3]byte
	if _, err := s.rand.Read(b[:]); err != nil {
		return "", err
	}
	const hex = "0123456789abcdef"
	var out [6]byte
	for i, v := range b {
		out[2*i], out[2*i+1] = hex[v>>4], hex[v&15]
	}
	return "custom-" + slug + "-" + string(out[:]), nil
}
