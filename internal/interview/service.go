package interview

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"applysmith/internal/kb"
	"applysmith/internal/llm"
	"applysmith/internal/store"
)

// Model is the part of the LLM router interview prep uses. It names the task,
// never a provider.
type Model interface {
	Structured(ctx context.Context, req llm.Request, schema json.RawMessage, out any) (*llm.Response, error)
}

// Stories is the story bank the packs link to and the coach checks against.
type Stories interface {
	MatchStories(ctx context.Context, text string, k int) ([]kb.StoryMatch, error)
	StoryTexts(ctx context.Context) ([]kb.StoryText, error)
}

// Deps is everything a Service needs. Stories, Model and Resume are optional:
// without a bank no question links a story, without a model the wording is
// deterministic, without a résumé reader the coach checks against the stories
// alone.
type Deps struct {
	Store   *store.Store
	Stories Stories
	Model   Model
	// Resume returns the text of the résumé submitted for a candidacy, or the
	// current one when none was submitted.
	Resume func(ctx context.Context, candidacy int64) (string, error)
	Now    func() time.Time
}

// Service keeps the interview records of one workspace.
type Service struct{ d Deps }

// New returns a Service over an open store.
func New(d Deps) (*Service, error) {
	if d.Store == nil {
		return nil, errors.New("interview: a store is required")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{d: d}, nil
}

func (s *Service) stamp() string { return s.d.Now().UTC().Format(time.RFC3339) }

func clipRunes(v string, n int) string {
	v = strings.TrimSpace(v)
	if utf8.RuneCountInString(v) <= n {
		return v
	}
	return strings.TrimSpace(string([]rune(v)[:n]))
}

// marshalEvent encodes an event payload and refuses one over the byte limit.
func marshalEvent(v any) (string, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	out := strings.TrimSuffix(b.String(), "\n")
	if len(out) > MaxEventBytes {
		return "", fmt.Errorf("%w: %d bytes", ErrTooLarge, len(out))
	}
	return out, nil
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// appendEvent writes one event and returns its id. The size check precedes
// the write, so a refused event leaves nothing behind.
func (s *Service) appendEvent(ctx context.Context, tx execer, candidacy int64, kind, stage string, payload any) (int64, error) {
	body, err := marshalEvent(payload)
	if err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO interview_events (candidacy_id, kind, stage_id, body, created_at) VALUES (?, ?, ?, ?, ?)`,
		candidacy, kind, stage, body, s.stamp())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CandidacyExists reports whether the tracker row exists.
func (s *Service) CandidacyExists(ctx context.Context, id int64) error {
	var n int
	err := s.d.Store.Read().QueryRowContext(ctx, `SELECT 1 FROM tracker_entries WHERE id = ?`, id).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: candidacy %d", ErrNotFound, id)
	}
	return err
}

// StageInput is a stage as the caller states it.
type StageInput struct {
	ID            string
	Type          string
	At            string
	Format        string
	Interviewers  []string
	UserConfirmed bool
	Cancelled     bool
	// ExpectedRevision is the revision read, 0 for a new stage; a newer one
	// is ErrConflict. Without it, a proposal never replaces a recorded stage.
	ExpectedRevision *int64
}

func oneOf(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// RecordStage appends a stage event. Recording an id again replaces what the
// view shows for it, which is how a proposed stage is confirmed and how a
// stage is cancelled or restored.
func (s *Service) RecordStage(ctx context.Context, candidacy int64, in StageInput) (Stage, error) {
	if !stageIDRE.MatchString(in.ID) {
		return Stage{}, invalid("the stage id is 2 to 64 lowercase letters, digits and hyphens")
	}
	if !oneOf(stageTypes, in.Type) {
		return Stage{}, invalid("the stage type must be one of %s", strings.Join(stageTypes, ", "))
	}
	if in.Format == "" {
		in.Format = "unknown"
	}
	if !oneOf(stageFormats, in.Format) {
		return Stage{}, invalid("the stage format must be one of %s", strings.Join(stageFormats, ", "))
	}
	if in.At != "" {
		if _, err := time.Parse(time.RFC3339, in.At); err != nil {
			return Stage{}, invalid("the stage date and time must be RFC 3339")
		}
	}
	if len(in.Interviewers) > MaxInterviewers {
		return Stage{}, invalid("a stage lists at most %d interviewers", MaxInterviewers)
	}
	names := []string{}
	for _, n := range in.Interviewers {
		if n = strings.TrimSpace(n); n == "" {
			continue
		}
		if utf8.RuneCountInString(n) > MaxInterviewer {
			return Stage{}, invalid("an interviewer name holds at most %d characters", MaxInterviewer)
		}
		names = append(names, n)
	}
	if err := s.CandidacyExists(ctx, candidacy); err != nil {
		return Stage{}, err
	}
	st := Stage{ID: in.ID, Type: in.Type, At: in.At, Format: in.Format, Interviewers: names, UserConfirmed: in.UserConfirmed, Cancelled: in.Cancelled, RecordedAt: s.stamp()}
	err := s.d.Store.Write(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(max(id), 0) FROM interview_events WHERE candidacy_id = ? AND kind = 'stage' AND stage_id = ?`,
			candidacy, st.ID).Scan(&st.Revision); err != nil {
			return err
		}
		switch {
		case in.ExpectedRevision != nil && *in.ExpectedRevision != st.Revision:
			return fmt.Errorf("%w: stage %s was changed elsewhere; reload it and try again", ErrConflict, st.ID)
		case in.ExpectedRevision == nil && !in.UserConfirmed && st.Revision > 0:
			return nil
		}
		var err error
		st.Revision, err = s.appendEvent(ctx, tx, candidacy, "stage", st.ID, st)
		return err
	})
	return st, err
}

// FeedbackInput is feedback as the caller states it.
type FeedbackInput struct {
	Stage string
	From  string
	Text  string
}

// RecordFeedback appends feedback for an existing stage.
func (s *Service) RecordFeedback(ctx context.Context, candidacy int64, in FeedbackInput) (Feedback, error) {
	in.Text = strings.TrimSpace(in.Text)
	if in.From == "" {
		in.From = FromUser
	}
	switch {
	case !oneOf([]string{FromUser, FromEmail, FromInterviewer}, in.From):
		return Feedback{}, invalid("feedback comes from the user, an email or the interviewer")
	case in.Text == "":
		return Feedback{}, invalid("the feedback text is empty")
	case utf8.RuneCountInString(in.Text) > MaxFeedbackIn:
		return Feedback{}, invalid("feedback holds at most %d characters", MaxFeedbackIn)
	}
	if _, err := s.Stage(ctx, candidacy, in.Stage); err != nil {
		return Feedback{}, err
	}
	f := Feedback{Stage: in.Stage, From: in.From, Text: in.Text, RecordedAt: s.stamp()}
	err := s.d.Store.Write(ctx, func(tx *sql.Tx) error {
		id, err := s.appendEvent(ctx, tx, candidacy, "feedback", f.Stage, f)
		f.ID = id
		return err
	})
	return f, err
}

// MockInput is a practice answer.
type MockInput struct {
	Stage    string
	Question string
	Answer   string
}

// CheckMockTurn refuses a practice answer RecordMockTurn would refuse, so a
// caller can say so before it starts the coaching.
func (s *Service) CheckMockTurn(ctx context.Context, candidacy int64, in MockInput) (MockInput, Stage, error) {
	in.Question, in.Answer = strings.TrimSpace(in.Question), strings.TrimSpace(in.Answer)
	switch {
	case in.Question == "":
		return in, Stage{}, invalid("the question is empty")
	case in.Answer == "":
		return in, Stage{}, invalid("the answer is empty")
	case utf8.RuneCountInString(in.Question) > MaxMockQuestion:
		return in, Stage{}, invalid("a question holds at most %d characters", MaxMockQuestion)
	case utf8.RuneCountInString(in.Answer) > MaxMockAnswer:
		return in, Stage{}, invalid("an answer holds at most %d characters", MaxMockAnswer)
	}
	stage, err := s.Stage(ctx, candidacy, in.Stage)
	return in, stage, err
}

// RecordMockTurn coaches the answer and appends the turn; a cancelled
// coaching records nothing.
func (s *Service) RecordMockTurn(ctx context.Context, candidacy int64, in MockInput) (MockTurn, error) {
	in, stage, err := s.CheckMockTurn(ctx, candidacy, in)
	if err != nil {
		return MockTurn{}, err
	}
	t := MockTurn{Stage: in.Stage, Question: in.Question, Answer: in.Answer, RecordedAt: s.stamp()}
	t.Coaching = s.coach(ctx, candidacy, stage, in)
	if err := ctx.Err(); err != nil {
		return MockTurn{}, err
	}
	err = s.d.Store.Write(ctx, func(tx *sql.Tx) error {
		id, err := s.appendEvent(ctx, tx, candidacy, "mock-turn", t.Stage, t)
		t.ID = id
		return err
	})
	return t, err
}

// Stage returns the latest record of a stage id; a cancelled stage is refused.
func (s *Service) Stage(ctx context.Context, candidacy int64, id string) (Stage, error) {
	var body string
	err := s.d.Store.Read().QueryRowContext(ctx, `SELECT body FROM interview_events WHERE candidacy_id = ? AND kind = 'stage' AND stage_id = ? ORDER BY id DESC LIMIT 1`,
		candidacy, id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		if cerr := s.CandidacyExists(ctx, candidacy); cerr != nil {
			return Stage{}, cerr
		}
		return Stage{}, fmt.Errorf("%w: stage %q of candidacy %d", ErrNotFound, id, candidacy)
	}
	if err != nil {
		return Stage{}, err
	}
	var st Stage
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		return Stage{}, fmt.Errorf("interview: the record of stage %q cannot be read: %w", id, err)
	}
	if st.Cancelled {
		return Stage{}, invalid("stage %s is cancelled; restore it first", id)
	}
	return st, nil
}

// StageFeedback returns the feedback recorded for a candidacy, at most limit
// items: the stage's own first, then the other stages', each newest first. A
// debrief of one round is what the next round's pack most needs.
func (s *Service) StageFeedback(ctx context.Context, candidacy int64, stage string, limit int) ([]Feedback, error) {
	rows, err := s.d.Store.Read().QueryContext(ctx, `SELECT id, body FROM interview_events WHERE candidacy_id = ? AND kind = 'feedback' ORDER BY stage_id = ? DESC, id DESC`,
		candidacy, stage)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Feedback{}
	for rows.Next() && len(out) < limit {
		var id int64
		var body string
		if err := rows.Scan(&id, &body); err != nil {
			return nil, err
		}
		var f Feedback
		if json.Unmarshal([]byte(body), &f) != nil {
			continue
		}
		f.ID = id
		out = append(out, f)
	}
	return out, rows.Err()
}
