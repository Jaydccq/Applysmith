package kb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Answer is a user-supplied reply to a follow-up question.
type Answer struct {
	QuestionID string `json:"questionId"`
	Dimension  string `json:"dimension"`
	Kind       string `json:"kind"`
	Text       string `json:"text"`
	UpdatedAt  string `json:"updatedAt"`
}

// FollowUp is a question that would make a story draft publishable or more
// complete.
type FollowUp struct {
	ID        string `json:"id"`
	Dimension string `json:"dimension"`
	Kind      string `json:"kind"`
	Question  string `json:"question"`
}

var dimensionQuestions = map[string]string{
	"metric":          "What was the real, measured result? Give the number and what it was measured against.",
	"risk":            "What could have gone wrong, and what did you do about it?",
	"conflict":        "Who disagreed with you, and about what?",
	"customer-impact": "Which customers or users were affected, and what changed for them?",
	"tradeoff":        "What did you choose between, and what did you give up?",
	"learning":        "What did you learn, and what do you do differently now?",
}

func todoID(line string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(line)))
	return "todo-" + hex.EncodeToString(sum[:])[:8]
}

// FollowUps generates the questions for a story draft from its TODO markers and
// from the dimensions its principles require but it does not yet carry.
func (s *Service) FollowUps(ctx context.Context, draftID int64) ([]FollowUp, error) {
	d, err := s.GetDraft(ctx, draftID)
	if err != nil {
		return nil, err
	}
	if d.Kind != DraftStory {
		return nil, fmt.Errorf("%w: draft %d is not a story", ErrInvalid, draftID)
	}
	return s.followUps(ctx, d)
}

func (s *Service) followUps(ctx context.Context, d Draft) ([]FollowUp, error) {
	sn, err := ParseSnippet("draft.md", []byte(d.Markdown))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	var out []FollowUp
	seen := map[string]bool{}
	for _, line := range strings.Split(sn.body, "\n") {
		if todoRE.MatchString(line) {
			id := todoID(line)
			if !seen[id] {
				seen[id] = true
				out = append(out, FollowUp{ID: id, Dimension: "metric", Kind: "metric",
					Question: "Replace the TODO with the real figure: " + strings.TrimSpace(line)})
			}
		}
	}
	comps, err := s.Competencies()
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, dim := range sn.Dimensions {
		have[dim] = true
	}
	for _, f := range d.Facts {
		if f.Dimension != "" {
			have[f.Dimension] = true
		}
	}
	var missing []string
	for _, c := range comps {
		if !contains(sn.Principles, c.ID) {
			continue
		}
		for _, dim := range c.RequiredDimensions {
			if !have[dim] && !contains(missing, dim) {
				missing = append(missing, dim)
			}
		}
	}
	sort.Strings(missing)
	for _, dim := range missing {
		if dim == "metric" && len(out) > 0 {
			continue
		}
		out = append(out, FollowUp{ID: "dim-" + dim, Dimension: dim, Kind: kindOf(dim), Question: dimensionQuestions[dim]})
	}
	return out, nil
}

func kindOf(dimension string) string {
	if dimension == "metric" {
		return "metric"
	}
	return "text"
}

func (s *Service) answers(ctx context.Context, key string) ([]Answer, error) {
	rows, err := s.st.Read().QueryContext(ctx, `SELECT question_id, dimension, kind, text, updated_at FROM kb_story_answers WHERE draft_key = ? ORDER BY question_id`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Answer{}
	for rows.Next() {
		var a Answer
		if err := rows.Scan(&a.QuestionID, &a.Dimension, &a.Kind, &a.Text, &a.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Answers lists the recorded answers of a draft's key.
func (s *Service) Answers(ctx context.Context, draftID int64) ([]Answer, error) {
	d, err := s.GetDraft(ctx, draftID)
	if err != nil {
		return nil, err
	}
	return s.answers(ctx, d.Key)
}

// RecordAnswer stores the user's answer to a follow-up question and folds it
// into the draft as a new revision, which clears the matching TODO or missing
// dimension. The answer row and the revision are written together. Recording
// the same answer to a question the draft no longer asks changes nothing. It
// never publishes. The dashboard and the agent both call this one function.
func (s *Service) RecordAnswer(ctx context.Context, draftID int64, questionID, text string) (Draft, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Draft{}, fmt.Errorf("%w: the answer is empty", ErrInvalid)
	}
	if err := CheckSecrets("answer", text); err != nil {
		return Draft{}, err
	}
	d, err := s.publishable(ctx, draftID)
	if err != nil {
		return d, err
	}
	if d.Kind != DraftStory {
		return d, fmt.Errorf("%w: draft %d is not a story", ErrInvalid, draftID)
	}
	qs, err := s.followUps(ctx, d)
	if err != nil {
		return d, err
	}
	var q *FollowUp
	for i := range qs {
		if qs[i].ID == questionID {
			q = &qs[i]
		}
	}
	if q == nil {
		prior, err := s.answers(ctx, d.Key)
		if err != nil {
			return d, err
		}
		for _, a := range prior {
			if a.QuestionID == questionID && a.Text == text {
				return d, nil
			}
		}
		return d, fmt.Errorf("%w: draft %d has no open question %q", ErrNotFound, draftID, questionID)
	}
	folded, facts, err := foldAnswer(d, *q, text)
	if err != nil {
		return d, err
	}
	now := s.stamp()
	nd, err := s.refineDraft(ctx, d.ID, folded, facts, nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO kb_story_answers (draft_key, question_id, dimension, kind, text, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (draft_key, question_id) DO UPDATE SET text = excluded.text, dimension = excluded.dimension, kind = excluded.kind, updated_at = excluded.updated_at`,
			d.Key, q.ID, q.Dimension, q.Kind, text, now, now)
		return err
	})
	if err != nil {
		return d, err
	}
	return nd, nil
}

// todoHintRE is a TODO marker with the hint that may follow it up to the end
// of its sentence, as in "TODO: add the real figure.".
var todoHintRE = lazyregexp.New(`\bTODO\b(?::[^.\n]*\.?)?`)

// foldAnswer puts one answer into the draft text and facts. A metric answer
// replaces the TODO marker of its line, hint included; every answer is kept as a
// user-supplied fact with its dimension.
func foldAnswer(d Draft, q FollowUp, text string) (string, []DraftFact, error) {
	md := d.Markdown
	if strings.HasPrefix(q.ID, "todo-") {
		replaced := false
		lines := strings.Split(md, "\n")
		for i, line := range lines {
			if !replaced && todoRE.MatchString(line) && todoID(line) == q.ID {
				lines[i] = todoHintRE.ReplaceAllLiteralString(line, text)
				replaced = true
			}
		}
		if !replaced {
			return "", nil, fmt.Errorf("%w: the TODO for %q is gone", ErrConflict, q.ID)
		}
		md = strings.Join(lines, "\n")
	}
	facts := append([]DraftFact{}, d.Facts...)
	facts = append(facts, DraftFact{Text: text, Origin: FactUser, Metric: q.Kind == "metric", Edited: true, Dimension: q.Dimension})
	return md, facts, nil
}
