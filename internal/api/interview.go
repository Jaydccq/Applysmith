package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/documents"
	"applysmith/internal/interview"
)

// interviewError maps an interview-service failure onto the API errors.
func interviewError(err error) error {
	switch {
	case errors.Is(err, interview.ErrNotFound):
		return apikit.NewErr(wire.CodeNotFound, "%s", err.Error())
	case errors.Is(err, interview.ErrConflict):
		return apikit.NewErr(wire.CodeConflict, "%s", err.Error())
	case errors.Is(err, interview.ErrInvalid), errors.Is(err, interview.ErrTooLarge):
		return apikit.NewErr(wire.CodeValidationFailed, "%s", err.Error())
	}
	return err
}

func (s *Server) interviews() (*interview.Service, error) {
	svc, err := s.docs()
	if err != nil {
		return nil, err
	}
	return svc.Interview(), nil
}

func toInterviewStage(st interview.Stage) wire.InterviewStage {
	return wire.InterviewStage{ID: st.ID, Type: st.Type, At: st.At, Format: st.Format, Interviewers: apikit.NonNil(st.Interviewers),
		UserConfirmed: st.UserConfirmed, Cancelled: st.Cancelled, Revision: apikit.Safe64(st.Revision), RecordedAt: apikit.Stamp(st.RecordedAt)}
}

func toInterviewFeedback(f interview.Feedback) wire.InterviewFeedback {
	return wire.InterviewFeedback{ID: apikit.Safe64(f.ID), Stage: f.Stage, From: f.From, Text: f.Text, RecordedAt: apikit.Stamp(f.RecordedAt)}
}

func toInterviewTurn(t interview.MockTurn) wire.InterviewMockTurn {
	return wire.InterviewMockTurn{ID: apikit.Safe64(t.ID), Stage: t.Stage, Question: t.Question, Answer: t.Answer, Coaching: t.Coaching, RecordedAt: apikit.Stamp(t.RecordedAt)}
}

func toInterviewPack(p interview.Pack) wire.InterviewPack {
	out := wire.InterviewPack{CandidacyID: apikit.Safe64(p.Candidacy), Stage: p.Stage, Revision: p.Revision, Generation: p.Generation, CreatedAt: apikit.Stamp(p.CreatedAt),
		RoleBrief: p.RoleBrief, CompanyBrief: p.CompanyBrief, Claims: apikit.NonNil(p.Claims), PriorFeedback: apikit.NonNil(p.PriorFeedback),
		Gaps: toInterviewSteps(p.Gaps), NextActions: toInterviewSteps(p.NextActions), Warnings: apikit.NonNil(p.Warnings),
		Hooks: []wire.InterviewHook{}, Questions: []wire.InterviewQuestion{}, Sources: []wire.InterviewSourceRef{}}
	if p.ReportNumber > 0 {
		n := apikit.Safe64(p.ReportNumber)
		out.ReportNumber = &n
	}
	for _, h := range p.Hooks {
		out.Hooks = append(out.Hooks, wire.InterviewHook{Text: h.Text, Sources: apikit.NonNil(h.Sources)})
	}
	for _, q := range p.Questions {
		out.Questions = append(out.Questions, wire.InterviewQuestion{Category: q.Category, Prompt: q.Prompt, StoryIDs: apikit.NonNil(q.StoryIDs), Sources: apikit.NonNil(q.Sources)})
	}
	for _, r := range p.Sources {
		out.Sources = append(out.Sources, wire.InterviewSourceRef{Kind: r.Kind, Ref: r.Ref, Label: r.Label, State: r.State})
	}
	return out
}

func toInterviewSteps(in []interview.Step) []wire.InterviewStep {
	out := []wire.InterviewStep{}
	for _, st := range in {
		out = append(out, wire.InterviewStep{Text: st.Text, Kind: st.Kind, Target: st.Target})
	}
	return out
}

func (s *Server) getInterviewWorkspace(c *apikit.Call) (apikit.Reply, error) {
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	iv, err := s.interviews()
	if err != nil {
		return apikit.Reply{}, err
	}
	w, err := iv.Workspace(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, interviewError(err)
	}
	out := wire.InterviewWorkspace{CandidacyID: apikit.Safe64(w.CandidacyID), Stages: []wire.InterviewStage{}, Cancelled: []wire.InterviewStage{}, Feedback: []wire.InterviewFeedback{},
		MockTurns: []wire.InterviewMockTurn{}, Packs: []wire.InterviewPackInfo{}, Degraded: w.Degraded, Repair: w.Repair, NextActions: toInterviewSteps(w.NextActions)}
	docs, err := s.docs()
	if err != nil {
		return apikit.Reply{}, err
	}
	packRun, packStage, err := docs.ActivePack(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, err
	}
	if packRun != "" {
		out.PackRun = &wire.InterviewPackRun{RunID: packRun, Stage: packStage}
	}
	st, err := s.db()
	if err != nil {
		return apikit.Reply{}, err
	}
	var run, input string
	err = st.Read().QueryRowContext(c.R.Context(), `SELECT id, input_json FROM runs WHERE kind = ? AND status IN ('queued', 'running')
		AND json_extract(input_json, '$.candidacy') = ? ORDER BY created_at DESC LIMIT 1`, interviewCoachKind, id).Scan(&run, &input)
	switch {
	case err == nil:
		out.CoachRun = &wire.InterviewCoachRun{RunID: run}
		_ = json.Unmarshal([]byte(input), out.CoachRun)
	case !errors.Is(err, sql.ErrNoRows):
		return apikit.Reply{}, err
	}
	for _, st := range w.Stages {
		out.Stages = append(out.Stages, toInterviewStage(st))
	}
	for _, st := range w.Cancelled {
		out.Cancelled = append(out.Cancelled, toInterviewStage(st))
	}
	for _, f := range w.Feedback {
		out.Feedback = append(out.Feedback, toInterviewFeedback(f))
	}
	for _, t := range w.MockTurns {
		out.MockTurns = append(out.MockTurns, toInterviewTurn(t))
	}
	for _, p := range w.Packs {
		out.Packs = append(out.Packs, wire.InterviewPackInfo{Stage: p.Stage, Revision: p.Revision, Generation: p.Generation, CreatedAt: apikit.Stamp(p.CreatedAt)})
	}
	return apikit.Reply{Data: out}, nil
}

// getInterviewsSoon lists the stages on today or tomorrow of the applications
// still open, for Home to put in front of the user with the drawer one click away.
func (s *Server) getInterviewsSoon(c *apikit.Call) (apikit.Reply, error) {
	found, err := s.interviewsSoon(c.R.Context(), s.now())
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.InterviewsSoon{Items: []wire.InterviewSoon{}}
	for _, f := range found {
		out.Items = append(out.Items, f.item)
	}
	return apikit.Reply{Data: out}, nil
}

// soonStage is a stage interviewsSoon selects, with its time.
type soonStage struct {
	at   time.Time
	item wire.InterviewSoon
}

// interviewsSoon is the one rule for the interviews in front of the user: the
// stages, not cancelled, of applications not archived, rejected or discarded
// whose time falls on the local today or tomorrow, soonest first. Home's list,
// the day's lines and the interview reminders read it.
func (s *Server) interviewsSoon(ctx context.Context, now time.Time) ([]soonStage, error) {
	iv, err := s.interviews()
	if err != nil {
		return nil, err
	}
	st, err := s.db()
	if err != nil {
		return nil, err
	}
	rows, err := st.Read().QueryContext(ctx, `SELECT id, company, role FROM tracker_entries t WHERE archived_at IS NULL AND status NOT IN ('rejected', 'discarded')
		AND EXISTS (SELECT 1 FROM interview_events e WHERE e.candidacy_id = t.id AND e.kind = 'stage') ORDER BY id`)
	if err != nil {
		return nil, err
	}
	type row struct {
		id            int64
		company, role string
	}
	var open []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.company, &r.role); err != nil {
			rows.Close()
			return nil, err
		}
		open = append(open, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	start := dayOf(now).start
	end := start.AddDate(0, 0, 2)
	var found []soonStage
	for _, r := range open {
		w, err := iv.Workspace(ctx, r.id)
		if err != nil {
			return nil, interviewError(err)
		}
		for _, stage := range w.Stages {
			at, err := time.Parse(time.RFC3339, stage.At)
			if err != nil || at.Before(start) || !at.Before(end) {
				continue
			}
			item := wire.InterviewSoon{CandidacyID: apikit.Safe64(r.id), Company: r.company, Role: r.role, Stage: toInterviewStage(stage)}
			for _, p := range w.Packs {
				item.Packed = item.Packed || p.Stage == stage.ID
			}
			for _, f := range w.Feedback {
				item.Debriefed = item.Debriefed || f.Stage == stage.ID
			}
			found = append(found, soonStage{at, item})
		}
	}
	slices.SortStableFunc(found, func(a, b soonStage) int { return a.at.Compare(b.at) })
	return found, nil
}

func (s *Server) recordInterviewStage(c *apikit.Call) (apikit.Reply, error) {
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	var w wire.InterviewStageRequest
	if e := apikit.Decode(c.Body, &w); e != nil {
		return apikit.Reply{}, e
	}
	if w.ExpectedRevision == nil {
		return apikit.Reply{}, apikit.InvalidField("expectedRevision", "expectedRevision is the stage revision read, 0 for a new stage")
	}
	iv, err := s.interviews()
	if err != nil {
		return apikit.Reply{}, err
	}
	rev := int64(*w.ExpectedRevision)
	st, err := iv.RecordStage(c.R.Context(), id, interview.StageInput{ID: w.ID, Type: w.Type, At: w.At, Format: w.Format, Interviewers: w.Interviewers,
		UserConfirmed: w.UserConfirmed, Cancelled: w.Cancelled, ExpectedRevision: &rev})
	if err != nil {
		return apikit.Reply{}, interviewError(err)
	}
	s.notify("tracker")
	return apikit.Reply{Status: http.StatusCreated, Data: toInterviewStage(st)}, nil
}

func (s *Server) createInterviewPack(c *apikit.Call) (apikit.Reply, error) {
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	var w wire.InterviewPackRequest
	if e := apikit.Decode(c.Body, &w); e != nil {
		return apikit.Reply{}, e
	}
	return s.documents.Submit(c, documents.Request{Action: documents.ActionInterview, CandidacyID: id, Stage: c.R.PathValue("stage")})
}

func (s *Server) getInterviewPack(c *apikit.Call) (apikit.Reply, error) {
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	q := apikit.QueryOf(c)
	revision := q.Integer("revision", 0)
	if q.Err == nil && revision < 0 {
		q.Fail(apikit.InvalidField("revision", "revision is 1 or more"))
	}
	if q.Err != nil {
		return apikit.Reply{}, q.Err
	}
	iv, err := s.interviews()
	if err != nil {
		return apikit.Reply{}, err
	}
	p, err := iv.Pack(c.R.Context(), id, c.R.PathValue("stage"), revision)
	if err != nil {
		return apikit.Reply{}, interviewError(err)
	}
	return apikit.Reply{Data: toInterviewPack(p)}, nil
}

func (s *Server) recordInterviewFeedback(c *apikit.Call) (apikit.Reply, error) {
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	var w wire.InterviewFeedbackRequest
	if e := apikit.Decode(c.Body, &w); e != nil {
		return apikit.Reply{}, e
	}
	iv, err := s.interviews()
	if err != nil {
		return apikit.Reply{}, err
	}
	f, err := iv.RecordFeedback(c.R.Context(), id, interview.FeedbackInput{Stage: c.R.PathValue("stage"), From: w.From, Text: w.Text})
	if err != nil {
		return apikit.Reply{}, interviewError(err)
	}
	return apikit.Reply{Status: http.StatusCreated, Data: toInterviewFeedback(f)}, nil
}

// interviewCoachKind is the run kind of coaching a practice answer: the
// coach note is a model call that outlasts a request, so it runs in the
// background, one per application, and its input keeps the answer so the
// drawer can show it again while it runs.
const interviewCoachKind = "interview.coach"

func (s *Server) coachInterviewAnswer(c *apikit.Call) (apikit.Reply, error) {
	key, e := apikit.IdempotencyKey(c)
	if e != nil {
		return apikit.Reply{}, e
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	var w wire.InterviewMockTurnRequest
	if e := apikit.Decode(c.Body, &w); e != nil {
		return apikit.Reply{}, e
	}
	iv, err := s.interviews()
	if err != nil {
		return apikit.Reply{}, err
	}
	in, _, err := iv.CheckMockTurn(c.R.Context(), id, interview.MockInput{Stage: c.R.PathValue("stage"), Question: w.Question, Answer: w.Answer})
	if err != nil {
		return apikit.Reply{}, interviewError(err)
	}
	input, _ := json.Marshal(map[string]any{"candidacy": id, "stage": in.Stage, "question": in.Question, "answer": in.Answer})
	return s.startPass(c.R.Context(), apikit.Pass{Operation: "interview.coach", Kind: interviewCoachKind, Stage: "coach", Claim: fmt.Sprintf("interview-coach-%d", id),
		Busy: "an answer of this application is already being coached", Key: key, Principal: c.P.ID, Input: input,
		Run: func(ctx context.Context, progress func(string)) (string, error) {
			progress("Checking your answer against your résumé and story bank")
			if _, err := iv.RecordMockTurn(ctx, id, in); err != nil {
				return "", interviewError(err)
			}
			return "The coaching is saved with the practice answer.", nil
		}})
}
