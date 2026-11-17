package documents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"applysmith/internal/documents/assets"
	"applysmith/internal/documents/toolchain"
	"applysmith/internal/interview"
	"applysmith/internal/kb"
	"applysmith/internal/llm"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
	"applysmith/internal/workspace"
)

// Model is the part of the LLM router the documents use. They name tasks,
// never providers or models.
type Model interface {
	Structured(ctx context.Context, req llm.Request, schema json.RawMessage, out any) (*llm.Response, error)
}

// Retriever is the evidence retrieval service of the knowledge base.
type Retriever interface {
	Search(ctx context.Context, q kb.Query) (kb.Result, error)
}

// Deps is everything a Service needs. Model, Retriever and Toolchain are
// optional: without a model the deterministic builders write the content,
// without a retriever the CV is the only evidence, and without a toolchain no
// PDF can be made.
type Deps struct {
	Store     *store.Store
	Tracker   *tracker.Tracker
	Workspace *workspace.Workspace
	Model     Model
	Retriever Retriever
	// Stories links interview questions to the story bank and checks mock
	// answers against it; nil leaves both out.
	Stories interview.Stories
	// Toolchain returns a toolchain whose smoke matches this machine, or an
	// *toolchain.UnavailableError naming what is missing.
	Toolchain func(ctx context.Context) (*toolchain.Toolchain, error)
	Now       func() time.Time
	Log       *slog.Logger
	// Emit receives run-stream events; optional.
	Emit func(Event)
}

// Queue and deadline bounds.
const (
	QueueMax = 20
	// RunTimeout bounds one run, model calls included.
	RunTimeout = 20 * time.Minute
	// LetterTimeout is the deadline of one letter model call, fallbacks
	// included. It holds a primary attempt that ends at its output ceiling
	// plus a whole fallback attempt: on DeepSeek a letter at thinking low
	// reasoned to its 6,144-token ceiling now and then (20 to 31 s, empty) and
	// the OpenRouter fallback then takes 14 to 16 s. At 45 s that fallback was
	// started, billed and cancelled, and the deterministic letter was used
	// (measured; docs/architecture/llm.md). The task now drafts with thinking
	// off (about 2 s), and a user route may still set a thinking level.
	LetterTimeout = 90 * time.Second
)

const (
	runKind      = "documents"
	runPrincipal = "documents"
)

var (
	errCancelled  = errors.New("document run cancelled")
	errShutdown   = errors.New("document service shut down")
	errNotRunning = errors.New("documents: the service is not running")
)

// Service makes and stores documents. One run builds at a time per workspace,
// as the toolchain spec requires; further requests wait in a bounded queue.
type Service struct {
	d  Deps
	iv *interview.Service

	mu      sync.Mutex
	ctx     context.Context
	stop    context.CancelCauseFunc
	started bool
	queue   []*pending
	// reserved counts the submits between their QueueMax check and their
	// enqueue, so a burst cannot pass the bound.
	reserved int
	running  *pending
	byRun    map[string]*pending
	wg       sync.WaitGroup
	// reviewMu serialises the read-modify-write of a version's visual-review file.
	reviewMu sync.Mutex
	// runTimeout bounds one run; tests shorten it.
	runTimeout time.Duration
	// pairBudget bounds the compile, verify and repair work of one pair;
	// tests shorten it.
	pairBudget time.Duration
	// builderFor wraps the toolchain for tests that script its output.
	builderFor func(*toolchain.Toolchain) pdfBuilder
	// beforeCommit runs inside the publish transaction once the version's
	// files are linked; tests stop the process there.
	beforeCommit func()
}

type pending struct {
	runID  string
	req    Request
	done   chan struct{}
	res    Result
	cancel context.CancelCauseFunc
	// deadline is when the run ends, once it has started.
	deadline time.Time
}

// New returns a stopped Service; call Start before submitting.
func New(d Deps) (*Service, error) {
	if d.Store == nil || d.Tracker == nil || d.Workspace == nil {
		return nil, errors.New("documents: a store, tracker and workspace are required")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	s := &Service{d: d, byRun: map[string]*pending{}, runTimeout: RunTimeout, pairBudget: toolchain.PairBudget}
	iv, err := interview.New(interview.Deps{Store: d.Store, Stories: d.Stories, Model: d.Model, Resume: s.submittedResume, Now: d.Now})
	if err != nil {
		return nil, err
	}
	s.iv = iv
	return s, nil
}

// Interview is the interview record keeper that shares this service's runs.
func (s *Service) Interview() *interview.Service { return s.iv }

// Start settles the runs a dead process left behind and accepts work. Nothing
// left behind is run again: a restart cannot repeat model work.
func (s *Service) Start(ctx context.Context) error {
	now := s.d.Now().UTC().Format(time.RFC3339)
	err := s.d.Store.Write(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{
			`UPDATE runs SET status = 'failed', stage = '', failure_reason = 'interrupted', updated_at = ?1, finished_at = ?1 WHERE kind = 'documents' AND status IN ('queued', 'running')`,
			`UPDATE document_jobs SET outcome = 'failed', finished_at = ?1 WHERE outcome IS NULL`,
		} {
			if _, err := tx.ExecContext(ctx, q, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctx, s.stop = context.WithCancelCause(context.Background())
	s.started = true
	return nil
}

// Close stops accepting work, interrupts the running build, settles the
// queued runs as interrupted and waits.
func (s *Service) Close() {
	s.mu.Lock()
	if s.stop != nil {
		s.stop(errShutdown)
	}
	s.started = false
	queued := s.queue
	s.queue = nil
	for _, p := range queued {
		delete(s.byRun, p.runID)
	}
	s.mu.Unlock()
	for _, p := range queued {
		s.settleQueued(p, ReasonInterrupted)
	}
	s.wg.Wait()
}

// Claim scopes an Idempotency-Key to the caller that sent it.
type Claim struct{ Principal, Key string }

// Handle identifies a submitted run.
type Handle struct {
	RunID    string
	Replayed bool
}

// jobKind is the kind a job row records: an edit is a job of the document kind
// it changes, since the row's kinds name what a run makes.
func jobKind(r Request) string {
	switch {
	case r.Action != ActionEdit:
		return string(r.Action)
	case r.Resume != nil:
		return string(KindResume)
	}
	return string(KindCover)
}

func operationOf(a Action) string { return "documents." + string(a) }

// Submit validates the request, records the run and queues it. A repeat of a
// claimed key returns the original run and queues nothing; the same key with
// other input is store.ErrConflict.
func (s *Service) Submit(ctx context.Context, c Claim, req Request) (Handle, error) {
	req, err := s.normalize(ctx, req)
	if err != nil {
		return Handle{}, err
	}
	if err := s.admit(ctx, &req); err != nil {
		return Handle{}, err
	}
	s.mu.Lock()
	started, full := s.started, len(s.queue)+s.reserved >= QueueMax
	if started && !full {
		s.reserved++
	}
	s.mu.Unlock()
	switch {
	case !started:
		return Handle{}, errNotRunning
	case full:
		return Handle{}, ErrQueueFull
	}
	queued := false
	defer func() {
		if !queued {
			s.mu.Lock()
			s.reserved--
			s.mu.Unlock()
		}
	}()
	input, err := json.Marshal(req)
	if err != nil {
		return Handle{}, err
	}
	// An edit, PDF or refinement names a document; its run belongs to that
	// document's candidacy, so one candidacy never has two runs at once.
	candidacy := req.CandidacyID
	if candidacy == 0 && req.DocumentID > 0 {
		if d, err := s.Get(ctx, req.DocumentID); err == nil && d.CandidacyID != nil {
			candidacy = *d.CandidacyID
		}
	}
	ref, err := s.d.Store.CreateRun(ctx, store.RunRequest{
		Principal: c.Principal, Operation: operationOf(req.Action), Key: c.Key, Kind: runKind, Input: input,
		Admit: func(ctx context.Context) error {
			if candidacy == 0 {
				return nil
			}
			id, err := s.ActiveRun(ctx, candidacy, req.Action == ActionInterview)
			if err == nil && id != "" {
				err = &RunActiveError{RunID: id}
			}
			return err
		},
		Created: func(tx *sql.Tx, runID string) error {
			var cand any
			if candidacy > 0 {
				cand = candidacy
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO document_jobs (run_id, kind, candidacy_id, created_at) VALUES (?, ?, ?, ?)`,
				runID, jobKind(req), cand, s.d.Now().UTC().Format(time.RFC3339))
			return err
		},
	})
	if err != nil {
		return Handle{}, err
	}
	if ref.Replayed {
		return Handle{RunID: ref.RunID, Replayed: true}, nil
	}
	queued = true
	if err := s.enqueue(ref.RunID, req); err != nil {
		return Handle{}, err
	}
	return Handle{RunID: ref.RunID}, nil
}

// ActiveRun is the id of the run of a candidacy that has not ended: an
// interview pack run when packs is set, else any other document run. Empty
// when none is working.
func (s *Service) ActiveRun(ctx context.Context, candidacy int64, packs bool) (string, error) {
	var id string
	err := s.d.Store.Read().QueryRowContext(ctx, `SELECT j.run_id FROM document_jobs j JOIN runs r ON r.id = j.run_id
		WHERE j.candidacy_id = ? AND (j.kind = 'interview') = ? AND r.status IN ('queued', 'running') ORDER BY j.created_at DESC LIMIT 1`, candidacy, packs).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// ActivePack is the interview pack run of a candidacy that has not ended and
// the stage it builds; empty when none is working.
func (s *Service) ActivePack(ctx context.Context, candidacy int64) (run, stage string, err error) {
	if run, err = s.ActiveRun(ctx, candidacy, true); err != nil || run == "" {
		return run, "", err
	}
	var input string
	if err = s.d.Store.Read().QueryRowContext(ctx, `SELECT input_json FROM runs WHERE id = ?`, run).Scan(&input); err != nil {
		return "", "", err
	}
	var req Request
	err = json.Unmarshal([]byte(input), &req)
	return run, req.Stage, err
}

// normalize fills defaults from the preferences and validates the options.
func (s *Service) normalize(ctx context.Context, r Request) (Request, error) {
	switch r.Action {
	case ActionResume, ActionCover, ActionPair, ActionPDF, ActionEdit, ActionRefine, ActionInterview:
	default:
		return r, invalid("action", "unknown action %q", r.Action)
	}
	if r.Actor == "" {
		r.Actor = tracker.ActorUser
	}
	if r.Action == ActionInterview {
		if r.CandidacyID < 1 {
			return r, invalid("candidacyId", "an interview pack needs a candidacy")
		}
		if r.Stage == "" {
			return r, invalid("stage", "an interview pack needs a stage")
		}
		return r, nil
	}
	if r.Action == ActionPDF || r.Action == ActionEdit || r.Action == ActionRefine {
		if r.DocumentID < 1 {
			return r, invalid("documentId", "a document id is required")
		}
		r.PDF = r.Action == ActionPDF
		if r.Action == ActionRefine {
			r.Instruction = strings.TrimSpace(r.Instruction)
			return r, validateTurns(r.Instruction, r.Turns)
		}
		if r.Action == ActionEdit && r.ProposalID > 0 {
			return s.acceptRequest(ctx, r)
		}
		if r.Action == ActionEdit && (r.Resume == nil) == (r.Letter == nil) {
			return r, invalid("content", "send the text of exactly one document: resume or letter")
		}
		return r, nil
	}
	switch r.Mode {
	case "":
		r.Mode = ModeTailored
	case ModeTailored, ModeBaseline:
	default:
		return r, invalid("mode", "mode must be tailored or baseline")
	}
	if r.Mode == ModeBaseline {
		if r.CandidacyID != 0 {
			return r, invalid("candidacyId", "a baseline résumé has no candidacy")
		}
		if r.Action != ActionResume {
			return r, ErrBaselineCover
		}
	} else if r.CandidacyID < 1 {
		return r, invalid("candidacyId", "a tailored run needs a candidacy")
	}
	if r.Action == ActionCover && r.Pages != 0 {
		return r, invalid("pages", "a cover letter is exactly one page")
	}
	prefs, err := s.Preferences(ctx)
	if err != nil {
		return r, err
	}
	if r.Language == "" {
		r.Language = prefs.Language
	}
	if _, ok := lookupLanguage(r.Language); !ok {
		return r, invalid("language", "unsupported language %q; supported: %s", r.Language, joinList(Languages()))
	}
	if r.Paper == "" {
		r.Paper = prefs.Paper
	}
	if r.Paper != assets.PaperA4 && r.Paper != assets.PaperLetter {
		return r, invalid("paper", "paper must be a4 or letter")
	}
	if r.Pages == 0 {
		r.Pages = prefs.ResumePages
	}
	if r.Pages != 1 && r.Pages != 2 {
		return r, invalid("pages", "the résumé page limit is 1 or 2")
	}
	if len([]rune(r.Posting)) > 200_000 {
		return r, invalid("posting", "the job description holds at most 200000 characters")
	}
	return r, nil
}

// admit refuses, before any run exists, a request that cannot succeed: an
// unknown candidacy, a tailored run without a job description, a PDF below the
// score floor, a PDF with no usable toolchain.
func (s *Service) admit(ctx context.Context, r *Request) error {
	if r.Action == ActionInterview {
		if _, err := s.candidacy(ctx, r.CandidacyID); err != nil {
			return err
		}
		if _, err := s.iv.Stage(ctx, r.CandidacyID, r.Stage); err != nil {
			return fmt.Errorf("%w: %v", ErrNotFound, err)
		}
		return nil
	}
	if r.Action == ActionRefine {
		return s.admitRefine(ctx, *r)
	}
	if r.Action == ActionEdit {
		if r.ProposalID > 0 {
			if d, err := s.Get(ctx, r.DocumentID); err != nil {
				return err
			} else if err := refinable(d); err != nil {
				return err
			}
		}
		_, _, err := s.prepareEdit(ctx, *r)
		return err
	}
	if r.Action == ActionPDF {
		d, err := s.Get(ctx, r.DocumentID)
		if err != nil {
			return err
		}
		if err := pdfable(d); err != nil {
			return err
		}
		if s.refinePending(ctx, d.ID) {
			return ErrProposalPending
		}
		if d.CandidacyID != nil {
			if err := s.Eligible(ctx, *d.CandidacyID); err != nil {
				return err
			}
		}
		return s.requireToolchain(ctx, r)
	}
	if r.Mode == ModeTailored {
		if _, err := s.candidacy(ctx, r.CandidacyID); err != nil {
			return err
		}
		if _, _, err := s.postingFor(ctx, *r); err != nil {
			return err
		}
		if r.PDF && !s.askedAlways(ctx, *r) {
			if err := s.Eligible(ctx, r.CandidacyID); err != nil {
				return err
			}
		}
	}
	if r.PDF {
		return s.requireToolchain(ctx, r)
	}
	return nil
}

func (s *Service) requireToolchain(ctx context.Context, r *Request) error {
	if s.d.Toolchain == nil {
		return &toolchain.UnavailableError{Report: toolchain.Report{State: toolchain.StateUnavailable, Missing: []string{"toolchain"}}}
	}
	tc, err := s.d.Toolchain(ctx)
	r.toolchain = tc
	return err
}

// candidacy reads the tracker row a tailored run is for.
func (s *Service) candidacy(ctx context.Context, id int64) (tracker.Entry, error) {
	det, err := s.d.Tracker.Get(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return tracker.Entry{}, fmt.Errorf("%w: candidacy %d", ErrNotFound, id)
		}
		return tracker.Entry{}, err
	}
	return det.Entry, nil
}

// askedAlways reports a cover letter an application form asks for while the
// preferences write one whatever the posting scored.
func (s *Service) askedAlways(ctx context.Context, r Request) bool {
	if !r.Asked || r.Action != ActionCover {
		return false
	}
	p, err := s.Preferences(ctx)
	return err == nil && p.CoverWhenAsked == CoverAlways
}

// Eligible enforces the score floor for a tailored PDF: the report index says
// whether the posting's final-kind score clears it or the user overrode it.
func (s *Service) Eligible(ctx context.Context, candidacy int64) error {
	e, err := s.candidacy(ctx, candidacy)
	if err != nil {
		return err
	}
	if e.ReportNumber == nil {
		return &BelowFloorError{}
	}
	var ok int
	err = s.d.Store.Read().QueryRowContext(ctx, `SELECT documents_eligible FROM report_index WHERE report_number = ?`, *e.ReportNumber).Scan(&ok)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && ok == 0) {
		score := e.Score
		if e.ScoreKind != tracker.ScoreFinal {
			score = nil
		}
		return &BelowFloorError{Score: score, Report: *e.ReportNumber}
	}
	return err
}

// enqueue takes over a submit's reserved place. A service closed since the
// check settles the recorded run as interrupted instead of queuing it.
func (s *Service) enqueue(runID string, req Request) error {
	p := &pending{runID: runID, req: req, done: make(chan struct{})}
	s.mu.Lock()
	s.reserved--
	if !s.started {
		s.mu.Unlock()
		s.settleQueued(p, ReasonInterrupted)
		return errNotRunning
	}
	defer s.mu.Unlock()
	s.byRun[runID] = p
	s.queue = append(s.queue, p)
	s.dispatchLocked()
	return nil
}

func (s *Service) dispatchLocked() {
	if !s.started || s.running != nil || len(s.queue) == 0 {
		return
	}
	p := s.queue[0]
	s.queue = s.queue[1:]
	s.running = p
	ctx, cancel := context.WithCancelCause(s.ctx)
	p.cancel = cancel
	s.wg.Add(1)
	go s.work(ctx, p)
}

func (s *Service) work(ctx context.Context, p *pending) {
	defer s.wg.Done()
	res := s.execute(ctx, p.runID, p.req)
	s.mu.Lock()
	p.cancel(nil)
	p.res = res
	delete(s.byRun, p.runID)
	s.running = nil
	close(p.done)
	s.dispatchLocked()
	s.mu.Unlock()
}

// Cancel stops a run at its next boundary and kills the active tool process
// group; a queued run never starts. The previous documents stay unchanged.
func (s *Service) Cancel(runID string) bool {
	s.mu.Lock()
	p, ok := s.byRun[runID]
	if !ok {
		s.mu.Unlock()
		return false
	}
	if p.cancel != nil {
		p.cancel(errCancelled)
		s.mu.Unlock()
		return true
	}
	for i, q := range s.queue {
		if q == p {
			s.queue = append(s.queue[:i:i], s.queue[i+1:]...)
			break
		}
	}
	delete(s.byRun, runID)
	s.mu.Unlock()
	s.settleQueued(p, ReasonCancelled)
	return true
}

func (s *Service) settleQueued(p *pending, reason string) {
	res := Result{RunID: p.runID, Reason: reason}
	s.finish(p.runID, p.req, res)
	p.res = res
	close(p.done)
}

// Wait returns a run's result once it has ended.
func (s *Service) Wait(ctx context.Context, runID string) (Result, error) {
	s.mu.Lock()
	p, ok := s.byRun[runID]
	s.mu.Unlock()
	if ok {
		select {
		case <-p.done:
			return p.res, nil
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
	return s.Outcome(ctx, runID)
}

// Remaining is the time a running run has left before its deadline; ok is
// false for a run that is queued, ended or unknown.
func (s *Service) Remaining(runID string) (left time.Duration, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.byRun[runID]
	if p == nil || p.deadline.IsZero() {
		return 0, false
	}
	return max(time.Until(p.deadline), 0), true
}
