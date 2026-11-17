package documents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"applysmith/internal/llm"
)

// Limits of a refine request.
const (
	MaxInstruction = 2000
	MaxTurns       = 8
)

// Kinds of a conversation turn. A request carries instructions and decisions;
// the stored history adds the error turn of a failed revision.
const (
	TurnInstruction = "instruction"
	TurnDecision    = "decision"
	TurnError       = "error"

	DecisionAccepted = "accepted"
	DecisionRejected = "rejected"
)

// Proposal statuses.
const (
	ProposalPending   = "pending"
	ProposalAccepted  = "accepted"
	ProposalDiscarded = "discarded"
	ProposalFailed    = "failed"
)

// Turn is one entry of a refine conversation.
type Turn struct {
	Kind     string `json:"kind"`
	Text     string `json:"text,omitempty"`
	Decision string `json:"decision,omitempty"`
}

// Proposal is a model's revision of one stored version, kept apart from the
// version until the user accepts it.
type Proposal struct {
	ID          int64
	DocumentID  int64
	RunID       string
	Status      string
	Instruction string
	Kind        Kind
	Resume      *ResumeDraft
	Letter      *LetterDraft
	// Voice is the hash of voice.md as the revision of a letter read it.
	Voice string
	// Text is the readable text of the proposal and Diff its line diff against
	// the version it revises.
	Text      string
	Diff      []DiffLine
	Error     string
	CreatedAt string
}

// Refinement is what the drawer shows for a version: the pending proposal, if
// any, and the recent turns.
type Refinement struct {
	Pending *Proposal
	Turns   []Turn
}

var (
	// ErrNoModel refuses a refinement when no model is configured: a revision
	// is written by a model or not at all.
	ErrNoModel = errors.New("documents: a revision needs a model, and none is configured")
	// ErrProposalPending refuses work that a pending proposal blocks.
	ErrProposalPending = fmt.Errorf("%w: a revision proposal is pending for this document; accept or discard it first", ErrConflict)
)

func validateTurns(instruction string, turns []Turn) error {
	if n := utf8.RuneCountInString(instruction); n < 1 || n > MaxInstruction {
		return invalid("instruction", "send an instruction of 1 to %d characters", MaxInstruction)
	}
	if len(turns) > MaxTurns {
		return invalid("turns", "send at most %d earlier turns", MaxTurns)
	}
	for _, t := range turns {
		switch {
		case t.Kind == TurnInstruction && t.Decision == "" && t.Text != "" && utf8.RuneCountInString(t.Text) <= MaxInstruction:
		case t.Kind == TurnDecision && t.Text == "" && (t.Decision == DecisionAccepted || t.Decision == DecisionRejected):
		default:
			return invalid("turns", "a turn is an instruction with its text, or an accepted or rejected decision")
		}
	}
	return nil
}

// admitRefine refuses, before any run exists, a refinement that cannot
// succeed.
func (s *Service) admitRefine(ctx context.Context, r Request) error {
	if s.d.Model == nil {
		return ErrNoModel
	}
	d, err := s.Get(ctx, r.DocumentID)
	if err != nil {
		return err
	}
	if err := refinable(d); err != nil {
		return err
	}
	if s.refinePending(ctx, d.ID) {
		return ErrProposalPending
	}
	return nil
}

// refinable says whether a version can be revised or have a revision accepted:
// a submitted version never changes, and a newer version of the same kind
// would be passed over by a revision of this one.
func refinable(d Document) error {
	switch {
	case d.Kind != KindResume && d.Kind != KindCover:
		return invalid("documentId", "only a résumé or a cover letter can be revised")
	case d.FrozenAt != "":
		return ErrFrozen
	case !d.Current:
		return fmt.Errorf("%w: a newer version of this %s exists; revise that one", ErrConflict, d.Kind)
	}
	return nil
}

// refinePending says whether a pending proposal blocks the version.
func (s *Service) refinePending(ctx context.Context, documentID int64) bool {
	var n int
	err := s.d.Store.Read().QueryRowContext(ctx, `SELECT count(*) FROM document_proposals WHERE document_id = ? AND status = 'pending'`, documentID).Scan(&n)
	return err != nil || n > 0
}

// acceptRequest fills an accepting edit with the proposal's content. The
// proposal must be pending and belong to the document.
func (s *Service) acceptRequest(ctx context.Context, r Request) (Request, error) {
	p, err := s.Proposal(ctx, r.ProposalID)
	switch {
	case err != nil:
		return r, err
	case p.DocumentID != r.DocumentID:
		return r, fmt.Errorf("%w: proposal %d is not for document %d", ErrNotFound, p.ID, r.DocumentID)
	case p.Status != ProposalPending:
		return r, fmt.Errorf("%w: proposal %d was already %s", ErrConflict, p.ID, p.Status)
	}
	r.Resume, r.Letter, r.Voice = p.Resume, p.Letter, p.Voice
	return r, nil
}

func (j *job) runRefine(ctx context.Context) (res Result, err error) {
	defer func() {
		if err != nil {
			j.recordRefineFailure(ctx, err)
		}
	}()
	if err = j.stage(ctx, StageDraft); err != nil {
		return Result{}, err
	}
	s := j.s
	d, err := s.Get(ctx, j.req.DocumentID)
	if err != nil {
		return Result{}, err
	}
	if err = refinable(d); err != nil {
		return Result{}, err
	}
	v, _, err := s.loadVersion(ctx, d, j.req)
	if err != nil {
		return Result{}, err
	}
	var content any
	var oldText, newText string
	switch {
	case v.resume != nil && d.Kind == KindResume:
		cur := *v.resume
		var draft ResumeDraft
		if err = j.callRefine(ctx, "resume-write", refineResumePrompt(v.m, cur, v.req.Pages, j.req), resumeSchema, &draft); err != nil {
			return Result{}, err
		}
		next := cur
		next.Generation, next.ResumeDraft = GenModel, normalizeResume(draft)
		if g := checkResume(v.m, next); !g.OK() {
			j.keep("resume-refine-rejected.json", next)
			return Result{}, fmt.Errorf("%w: %s", ErrNoValidDraft, codes(g))
		}
		if sameContent(next.ResumeDraft, cur.ResumeDraft) {
			return Result{}, invalid("instruction", "the revision changed nothing")
		}
		content, oldText, newText = next.ResumeDraft, resumeMarkdown(v.m, cur), resumeMarkdown(v.m, next)
	case v.letter != nil && d.Kind == KindCover:
		cur := *v.letter
		var paired ResumeDoc
		if v.resume != nil {
			paired = *v.resume
		}
		var draft LetterDraft
		voice, read := j.voiceRule()
		if err = j.callRefine(ctx, "cover-letter", refineLetterPrompt(v.m, cur, paired, j.req)+voice, letterSchema, &draft); err != nil {
			return Result{}, err
		}
		next := cur
		next.Generation, next.LetterDraft = GenModel, normalizeLetter(draft)
		if g := checkLetter(v.m, next); !g.OK() {
			j.keep("cover-letter-refine-rejected.json", next)
			return Result{}, fmt.Errorf("%w: %s", ErrNoValidDraft, codes(g))
		}
		if sameContent(next.LetterDraft, cur.LetterDraft) {
			return Result{}, invalid("instruction", "the revision changed nothing")
		}
		content, oldText, newText = letterProposal{next.LetterDraft, read}, letterMarkdown(v.m, cur), letterMarkdown(v.m, next)
	default:
		return Result{}, invalid("documentId", "the stored version holds no %s to revise", d.Kind)
	}
	id, err := j.storeProposal(ctx, d.ID, content, newText, lineDiff(oldText, newText))
	if err != nil {
		return Result{}, err
	}
	return Result{Published: true, Proposal: id, Generation: GenModel}, nil
}

// callRefine runs the model call of a refinement under the task of the
// document's own drafting, so the routing table and its cap apply as they do
// to a draft.
func (j *job) callRefine(ctx context.Context, task, prompt string, schema json.RawMessage, out any) error {
	err := j.callAs(ctx, refineSystemPrompt, task, prompt, schema, out, RevisionTimeout)
	switch {
	case err == nil, ctx.Err() != nil:
		return err
	case errors.Is(err, llm.ErrNoRoute), errors.Is(err, llm.ErrUnconfigured):
		return ErrNoModel
	}
	return fmt.Errorf("documents: the model call failed (%s)", errKind(err))
}

// letterProposal is the stored content of a letter's revision: the text and
// the voice notes it was written in.
type letterProposal struct {
	LetterDraft
	Voice string `json:"voiceSha256,omitempty"`
}

func (j *job) storeProposal(ctx context.Context, documentID int64, content any, text string, diff []DiffLine) (int64, error) {
	raw, _ := json.Marshal(content)
	diffRaw, _ := json.Marshal(diff)
	turns, _ := json.Marshal(j.req.Turns)
	now := j.s.d.Now().UTC().Format(time.RFC3339)
	var id int64
	err := j.s.d.Store.Write(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM document_proposals WHERE document_id = ? AND status = 'pending'`, documentID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrProposalPending
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO document_proposals (document_id, run_id, status, instruction, turns_json, content_json, text, diff_json, created_at)
			VALUES (?, ?, 'pending', ?, ?, ?, ?, ?, ?)`, documentID, j.runID, j.req.Instruction, string(turns), string(raw), text, string(diffRaw), now)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

// recordRefineFailure keeps a failed revision as an error turn. The draft is
// untouched and stays editable.
func (j *job) recordRefineFailure(ctx context.Context, err error) {
	msg := relativePaths(refineErrorText(ctx, err), j.s.d.Workspace.Root)
	turns, _ := json.Marshal(j.req.Turns)
	now := j.s.d.Now().UTC().Format(time.RFC3339)
	wctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	werr := j.s.d.Store.Write(wctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(wctx, `INSERT INTO document_proposals (document_id, run_id, status, instruction, turns_json, error, created_at, decided_at)
			VALUES (?, ?, 'failed', ?, ?, ?, ?, ?)`, j.req.DocumentID, j.runID, j.req.Instruction, string(turns), msg, now, now)
		return err
	})
	if werr != nil {
		j.s.d.Log.Warn("documents: recording the failed revision failed", "run", j.runID, "err", werr)
	}
}

func refineErrorText(ctx context.Context, err error) string {
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, errTimeout):
		return "the revision ran out of time"
	case errors.Is(cause, errCancelled), errors.Is(cause, errShutdown):
		return "the revision was cancelled"
	}
	switch {
	case errors.Is(err, ErrNoModel):
		return "no model is configured for the revision"
	case errors.Is(err, ErrNoValidDraft):
		return "the revision was refused by the grounding checks (" + strings.TrimPrefix(err.Error(), ErrNoValidDraft.Error()+": ") + ")"
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrConflict), errors.Is(err, ErrFrozen):
		return strings.TrimPrefix(err.Error(), "documents: ")
	}
	return "the revision failed (" + errKind(err) + ")"
}

// Proposal reads one proposal.
func (s *Service) Proposal(ctx context.Context, id int64) (Proposal, error) {
	return s.scanProposal(s.d.Store.Read().QueryRowContext(ctx, proposalSelect+` WHERE p.id = ?`, id), id)
}

const proposalSelect = `SELECT p.id, p.document_id, coalesce(p.run_id, ''), p.status, p.instruction, d.kind, p.content_json, p.text, p.diff_json, p.error, p.created_at
	FROM document_proposals p JOIN documents d ON d.id = p.document_id`

func (s *Service) scanProposal(r rowScanner, id int64) (Proposal, error) {
	var p Proposal
	var content, diff string
	err := r.Scan(&p.ID, &p.DocumentID, &p.RunID, &p.Status, &p.Instruction, &p.Kind, &content, &p.Text, &diff, &p.Error, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Proposal{}, fmt.Errorf("%w: proposal %d", ErrNotFound, id)
	}
	if err != nil {
		return Proposal{}, err
	}
	_ = json.Unmarshal([]byte(diff), &p.Diff)
	if content != "" {
		switch p.Kind {
		case KindResume:
			var rd ResumeDraft
			if json.Unmarshal([]byte(content), &rd) == nil {
				p.Resume = &rd
			}
		case KindCover:
			var lp letterProposal
			if json.Unmarshal([]byte(content), &lp) == nil {
				p.Letter, p.Voice = &lp.LetterDraft, lp.Voice
			}
		}
	}
	return p, nil
}

// Refinement reads the pending proposal of a version and its recent turns: for
// each earlier request the instruction and how it ended, newest last.
func (s *Service) Refinement(ctx context.Context, documentID int64) (Refinement, error) {
	if _, err := s.Get(ctx, documentID); err != nil {
		return Refinement{}, err
	}
	rows, err := s.d.Store.Read().QueryContext(ctx, proposalSelect+` WHERE p.document_id = ? ORDER BY p.id DESC LIMIT ?`, documentID, MaxTurns)
	if err != nil {
		return Refinement{}, err
	}
	defer rows.Close()
	var ps []Proposal
	for rows.Next() {
		p, err := s.scanProposal(rows, 0)
		if err != nil {
			return Refinement{}, err
		}
		ps = append(ps, p)
	}
	if err := rows.Err(); err != nil {
		return Refinement{}, err
	}
	out := Refinement{Turns: []Turn{}}
	var turns []Turn
	for i := len(ps) - 1; i >= 0; i-- {
		p := ps[i]
		turns = append(turns, Turn{Kind: TurnInstruction, Text: p.Instruction})
		switch p.Status {
		case ProposalAccepted:
			turns = append(turns, Turn{Kind: TurnDecision, Decision: DecisionAccepted})
		case ProposalDiscarded:
			turns = append(turns, Turn{Kind: TurnDecision, Decision: DecisionRejected})
		case ProposalFailed:
			turns = append(turns, Turn{Kind: TurnError, Text: p.Error})
		case ProposalPending:
			out.Pending = &p
		}
	}
	if len(turns) > MaxTurns {
		turns = turns[len(turns)-MaxTurns:]
	}
	out.Turns = append(out.Turns, turns...)
	return out, nil
}

// Discard rejects a pending proposal. The version, its PDF and its
// verification are not touched.
func (s *Service) Discard(ctx context.Context, documentID, proposalID int64) error {
	p, err := s.Proposal(ctx, proposalID)
	if err != nil {
		return err
	}
	if p.DocumentID != documentID {
		return fmt.Errorf("%w: proposal %d is not for document %d", ErrNotFound, proposalID, documentID)
	}
	now := s.d.Now().UTC().Format(time.RFC3339)
	return s.d.Store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE document_proposals SET status = 'discarded', decided_at = ? WHERE id = ? AND status = 'pending'`, now, proposalID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: proposal %d was already %s", ErrConflict, proposalID, p.Status)
		}
		return nil
	})
}

// acceptIn marks the proposal accepted inside the transaction that publishes
// the version, so a proposal is accepted exactly when its version exists.
func acceptIn(ctx context.Context, tx *sql.Tx, id int64, now string) error {
	res, err := tx.ExecContext(ctx, `UPDATE document_proposals SET status = 'accepted', decided_at = ? WHERE id = ? AND status = 'pending'`, now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: proposal %d is no longer pending", ErrConflict, id)
	}
	return nil
}
