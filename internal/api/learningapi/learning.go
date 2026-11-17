// Package learningapi serves what the answer bank learns from the user in the
// local API: the fill events and system writes the client reports, the manual
// fills, the answer candidates and the bank's usage. internal/api registers
// the handlers.
package learningapi

import (
	"context"
	"errors"
	"time"

	"applysmith/internal/answerbank/learn"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
)

// Deps is what the learning handlers use from the server.
type Deps struct {
	Learning   func() (*learn.Service, error)
	LearnError func(error) error // shared with the apply views; maps the bank's errors too
	// TouchFill keeps an extension fill the call names from timing out.
	TouchFill func(ctx context.Context, runIDs ...string)
	// OpenQuestions and MoveAnswered go on with the waiting runs an answer
	// the user kept completes.
	OpenQuestions func(ctx context.Context) map[string]int
	MoveAnswered  func(ctx context.Context, before map[string]int)
}

// Handlers serves the learning routes.
type Handlers struct{ Deps }

func New(d Deps) *Handlers { return &Handlers{Deps: d} }

func toLearnReport(r *wire.LearningReport) *learn.Report {
	if r == nil {
		return nil
	}
	return &learn.Report{Note: r.Note, Tag: r.Tag, Role: r.Role, AccessibleName: r.AccessibleName, Path: r.Path, NearbyText: r.NearbyText,
		URL: r.URL, FrameURL: r.FrameURL, ShadowDepth: r.ShadowDepth, Snippet: r.Snippet}
}

func (s *Handlers) Events(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Learning()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.LearningEventsRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if len(req.Events) == 0 {
		return apikit.Reply{}, apikit.InvalidField("events", "events must hold 1 to %d entries", learn.MaxBatch)
	}
	if len(req.Events) > learn.MaxBatch {
		return apikit.Reply{}, apikit.NewErr(wire.CodePayloadTooLarge, "an upload holds 1 to %d events; split it", learn.MaxBatch)
	}
	ups := make([]learn.Upload, len(req.Events))
	runIDs := make([]string, len(req.Events))
	for i, ev := range req.Events {
		runIDs[i] = ev.RunID
		u := learn.Upload{EventID: ev.EventID, Kind: ev.Kind, Host: ev.Host, Question: ev.Question, SectionPath: ev.Section, ControlKind: ev.ControlKind, InputType: ev.InputType,
			Autocomplete: ev.Autocomplete, Options: ev.Options, At: ev.At.Time, Reason: ev.Reason, Before: ev.Before, After: ev.After,
			OptionLabel: ev.OptionLabel, EntryID: ev.EntryID, Origin: ev.Origin, RunID: ev.RunID, Selector: ev.Selector, TrustedInput: ev.TrustedInput,
			Report: toLearnReport(ev.Report)}
		if ev.ReportNumber != nil {
			u.ReportNumber = int64(*ev.ReportNumber)
		}
		ups[i] = u
	}
	res, err := svc.Ingest(c.R.Context(), ups)
	if err != nil {
		return apikit.Reply{}, s.LearnError(err)
	}
	s.TouchFill(c.R.Context(), runIDs...)
	// Candidate generation runs after the response is decided and never delays it.
	svc.AfterIngest()
	out := wire.LearningEventsResult{Accepted: wire.SafeInt(res.Accepted), Duplicates: apikit.NonNil(res.Duplicates), Rejected: make([]wire.LearningEventRejection, 0, len(res.Rejected))}
	for _, r := range res.Rejected {
		out.Rejected = append(out.Rejected, wire.LearningEventRejection{EventID: r.EventID, Reason: r.Reason})
	}
	return apikit.Reply{Data: out}, nil
}

// RecordSystemWrite remembers a value the client's own pass wrote into a
// control, which never passes through the match route.
func (s *Handlers) RecordSystemWrite(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Learning()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.SystemWriteRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if e := RequireIssuedRun(c.R.Context(), svc, req.RunID); e != nil {
		return apikit.Reply{}, e
	}
	s.TouchFill(c.R.Context(), req.RunID)
	if req.Selector == "" {
		return apikit.Reply{}, apikit.InvalidField("selector", "selector is required")
	}
	if req.Value == "" {
		return apikit.Reply{}, apikit.InvalidField("value", "value is required")
	}
	if err := svc.RecordSystemWrites(c.R.Context(), req.RunID, learn.SystemWrite{Selector: req.Selector, Value: req.Value}); err != nil {
		if errors.Is(err, learn.ErrSystemWrite) {
			return apikit.Reply{}, apikit.InvalidField("value", "%s", err.Error())
		}
		return apikit.Reply{}, s.LearnError(err)
	}
	return apikit.Reply{Data: wire.SystemWriteRecorded{Recorded: true}}, nil
}

// RequireIssuedRun refuses a run id the server never issued.
func RequireIssuedRun(ctx context.Context, svc *learn.Service, runID string) *apikit.Error {
	if runID == "" {
		return apikit.InvalidField("runId", "runId is required")
	}
	if !svc.RunIssued(ctx, runID) {
		return apikit.InvalidField("runId", "runId is not a run this server issued")
	}
	return nil
}

func (s *Handlers) ListManualFills(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Learning()
	if err != nil {
		return apikit.Reply{}, err
	}
	q := apikit.QueryOf(c)
	page, size := q.Paging(100)
	if q.Err != nil {
		return apikit.Reply{}, q.Err
	}
	fills, total, err := svc.ManualFills(c.R.Context(), size, (page-1)*size)
	if err != nil {
		return apikit.Reply{}, s.LearnError(err)
	}
	out := wire.ManualFillList{Fills: make([]wire.ManualFill, 0, len(fills)), Total: wire.SafeInt(total)}
	for _, f := range fills {
		m := wire.ManualFill{ID: wire.SafeInt(f.ID), At: wire.Time{Time: f.At}, ReceivedAt: wire.Time{Time: f.ReceivedAt}, Host: f.Host, Question: f.Question,
			ControlKind: f.ControlKind, Value: f.Value, OptionLabel: f.OptionLabel, Withheld: string(f.Withheld), GroundTruth: f.GroundTruth, PriorValue: f.PriorValue,
			EntryID: f.EntryID, RunID: f.RunID, ReportNumber: safePtr(f.ReportNumber)}
		out.Fills = append(out.Fills, m)
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Handlers) DeleteManualFill(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Learning()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	if err := svc.DeleteManualFill(c.R.Context(), id); err != nil {
		return apikit.Reply{}, s.LearnError(err)
	}
	return apikit.Reply{Data: wire.ManualFillDeleted{ID: wire.SafeInt(id)}}, nil
}

func safePtr(n *int64) *wire.SafeInt {
	if n == nil {
		return nil
	}
	v := wire.SafeInt(*n)
	return &v
}

func toWireCandidate(c learn.Candidate) wire.AnswerCandidate {
	out := wire.AnswerCandidate{ID: wire.SafeInt(c.ID), Key: c.Key, Type: c.Type, State: c.State, Source: c.Source, TargetEntryID: c.TargetEntryID,
		EntryID: c.EntryID, SeedQuestion: c.SeedQuestion, Questions: apikit.NonNil(c.Questions), Hosts: apikit.NonNil(c.Hosts), ProposedKind: c.ProposedKind,
		Sections: []string{}, Options: []string{}, ProposedAnswer: c.ProposedAnswer, LongForm: c.LongForm, Flags: apikit.NonNil(c.Flags), Support: wire.SafeInt(c.Support),
		FirstSeenAt: wire.Time{Time: c.FirstSeenAt}, LastSeenAt: wire.Time{Time: c.LastSeenAt}, CreatedAt: wire.Time{Time: c.CreatedAt}, UpdatedAt: wire.Time{Time: c.UpdatedAt},
		Review: wire.AnswerCandidateReview{Verdict: c.Review.Verdict, Reason: c.Review.Reason, Draft: c.Review.Draft, Cited: apikit.NonNil(c.Review.Cited)}}
	if c.DecidedAt != nil {
		out.DecidedAt = &wire.Time{Time: *c.DecidedAt}
	}
	return out
}

func WireCandidates(cs []learn.Candidate) []wire.AnswerCandidate {
	out := make([]wire.AnswerCandidate, 0, len(cs))
	for _, c := range cs {
		out = append(out, toWireCandidate(c))
	}
	return out
}

// withPageContext adds the sections and options of each stored candidate's
// evidence. A candidate that is not stored (a dry run) has none.
func withPageContext(ctx context.Context, svc *learn.Service, cs []wire.AnswerCandidate) ([]wire.AnswerCandidate, error) {
	ids := make([]int64, 0, len(cs))
	for _, c := range cs {
		if c.ID > 0 {
			ids = append(ids, int64(c.ID))
		}
	}
	pcs, err := svc.CandidateContexts(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range cs {
		pc := pcs[int64(cs[i].ID)]
		cs[i].Sections, cs[i].Options, cs[i].Multiple = apikit.NonNil(pc.Sections), apikit.NonNil(pc.Options), pc.Multiple
	}
	return cs, nil
}

func (s *Handlers) ListCandidates(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Learning()
	if err != nil {
		return apikit.Reply{}, err
	}
	state := apikit.QueryOf(c).Str("state")
	switch state {
	case "", wire.CandidatePending, wire.CandidateApproved, wire.CandidateRejected:
	default:
		return apikit.Reply{}, apikit.InvalidField("state", "state must be pending, approved or rejected")
	}
	cs, err := svc.Candidates(c.R.Context(), state)
	if err != nil {
		return apikit.Reply{}, s.LearnError(err)
	}
	out, err := withPageContext(c.R.Context(), svc, WireCandidates(cs))
	if err != nil {
		return apikit.Reply{}, s.LearnError(err)
	}
	edits, err := s.WireEdits(c.R.Context(), svc, "")
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: wire.AnswerCandidateList{Candidates: out, Edits: edits}}, nil
}

// WireEdits are the edits the keep press is offered for (learn.Service.Edits),
// a run's own when runID is given.
func (s *Handlers) WireEdits(ctx context.Context, svc *learn.Service, runID string) ([]wire.AnswerEdit, error) {
	edits, err := svc.Edits(ctx, runID)
	if err != nil {
		return nil, s.LearnError(err)
	}
	out := make([]wire.AnswerEdit, 0, len(edits))
	for _, e := range edits {
		out = append(out, wireEdit(e))
	}
	return out, nil
}

func wireEdit(e learn.Edit) wire.AnswerEdit {
	return wire.AnswerEdit{FillID: wire.SafeInt(e.FillID), EntryID: e.EntryID, Host: e.Host, Question: e.Question, Before: e.Before, After: e.After}
}

// KeepManualFill is POST /api/answer-bank/manual-fills/{id}/keep, the keep
// press: the value the user wrote by hand becomes the answer of the entry
// whose value it replaced (learn.Service.KeepEdit). A waiting run whose
// questions the new answer answers moves as after any answer.
func (s *Handlers) KeepManualFill(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Learning()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	before := s.OpenQuestions(c.R.Context())
	saved, err := svc.KeepEdit(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, s.LearnError(err)
	}
	if saved.Changed {
		s.MoveAnswered(c.R.Context(), before)
	}
	return apikit.Reply{Data: wire.AnswerBankSaved{Revision: saved.Revision, Changed: saved.Changed}}, nil
}

// LeaveManualFill is POST /api/answer-bank/manual-fills/{id}/leave, the keep
// press's other answer: the entry keeps its answer and the corrections to that
// value stop counting (learn.Service.LeaveEdit). It answers with the edit it
// left. A stale entry it makes active again moves a waiting run whose
// questions it now answers, as after any answer.
func (s *Handlers) LeaveManualFill(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Learning()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	before := s.OpenQuestions(c.R.Context())
	left, saved, err := svc.LeaveEdit(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, s.LearnError(err)
	}
	if saved.Changed {
		s.MoveAnswered(c.R.Context(), before)
	}
	return apikit.Reply{Data: wireEdit(left)}, nil
}

// UndoManualFill is POST /api/answer-bank/manual-fills/{id}/undo, the Undo of
// a keep or leave press (learn.Service.UndoEdit).
func (s *Handlers) UndoManualFill(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Learning()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	saved, err := svc.UndoEdit(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, s.LearnError(err)
	}
	return apikit.Reply{Data: wire.AnswerBankSaved{Revision: saved.Revision, Changed: saved.Changed}}, nil
}

// GenerateCandidates is the dashboard's "Look for new suggestions" press: it
// answers with what generation did, and the new suggestions are reviewed in the
// background after it (learn.Service.ReviewLater).
func (s *Handlers) GenerateCandidates(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Learning()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.AnswerCandidateGenerate
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	var opts learn.GenerateOptions
	if req.SinceDays != nil {
		if *req.SinceDays < 1 || *req.SinceDays > 365 {
			return apikit.Reply{}, apikit.InvalidField("sinceDays", "sinceDays must be between 1 and 365")
		}
		opts.SinceDays = int(*req.SinceDays)
	}
	res, err := svc.Generate(c.R.Context(), opts)
	if err != nil {
		return apikit.Reply{}, s.LearnError(err)
	}
	svc.ReviewLater()
	cands, err := withPageContext(c.R.Context(), svc, WireCandidates(res.Candidates))
	if err != nil {
		return apikit.Reply{}, s.LearnError(err)
	}
	return apikit.Reply{Data: wire.AnswerCandidateGenerateResult{Created: wire.SafeInt(res.Created), Refreshed: wire.SafeInt(res.Refreshed),
		Reproposed: wire.SafeInt(res.Reproposed), Corrections: wire.SafeInt(res.Corrections), Candidates: cands}}, nil
}

func (s *Handlers) decideAnswerCandidate(c *apikit.Call, decide func(*learn.Service, int64) (learn.Decision, error)) (apikit.Reply, error) {
	svc, err := s.Learning()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	d, err := decide(svc, id)
	if err != nil {
		return apikit.Reply{}, s.LearnError(err)
	}
	return apikit.Reply{Data: wire.AnswerCandidateDecision{Candidate: toWireCandidate(d.Candidate), EntryID: d.EntryID}}, nil
}

func (s *Handlers) ApproveCandidate(c *apikit.Call) (apikit.Reply, error) {
	var req wire.AnswerCandidateApprove
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	return s.decideAnswerCandidate(c, func(svc *learn.Service, id int64) (learn.Decision, error) {
		return svc.Approve(c.R.Context(), id, req.Answer)
	})
}

// UnapproveCandidate is the undo of an approval.
func (s *Handlers) UnapproveCandidate(c *apikit.Call) (apikit.Reply, error) {
	return s.decideAnswerCandidate(c, func(svc *learn.Service, id int64) (learn.Decision, error) { return svc.Unapprove(c.R.Context(), id) })
}

func (s *Handlers) RejectCandidate(c *apikit.Call) (apikit.Reply, error) {
	return s.decideAnswerCandidate(c, func(svc *learn.Service, id int64) (learn.Decision, error) { return svc.Reject(c.R.Context(), id) })
}

// RestoreCandidate is the undo of a rejection.
func (s *Handlers) RestoreCandidate(c *apikit.Call) (apikit.Reply, error) {
	return s.decideAnswerCandidate(c, func(svc *learn.Service, id int64) (learn.Decision, error) { return svc.Restore(c.R.Context(), id) })
}

func (s *Handlers) Usage(c *apikit.Call) (apikit.Reply, error) {
	svc, err := s.Learning()
	if err != nil {
		return apikit.Reply{}, err
	}
	rows, err := svc.Usage(c.R.Context(), learn.UsageWindow)
	if err != nil {
		return apikit.Reply{}, s.LearnError(err)
	}
	out := wire.AnswerBankUsage{WindowDays: wire.SafeInt(learn.UsageWindow / (24 * time.Hour)), Rows: make([]wire.AnswerBankUsageRow, 0, len(rows))}
	for _, u := range rows {
		out.Rows = append(out.Rows, wire.AnswerBankUsageRow{EntryID: u.EntryID, Matches: wire.SafeInt(u.Matches), Applies: wire.SafeInt(u.Applies),
			Abstains: wire.SafeInt(u.Abstains), Corrections: wire.SafeInt(u.Corrections), LastMatchedAt: u.LastMatchedAt, LastAppliedAt: u.LastAppliedAt,
			LastCorrectedAt: u.LastCorrectedAt, LastActivityAt: u.LastActivityAt, AverageConfidence: u.AverageConfidence, ExampleQuestions: apikit.NonNil(u.ExampleQuestions)})
	}
	return apikit.Reply{Data: out}, nil
}
