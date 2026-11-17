// Clean-room: docs/provenance.md#CR-SCAN-SKELETON

package discovery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"applysmith/internal/workspace"
)

const (
	defaultBudget = 30 * time.Minute
	defaultTopN   = 15
	// openFilterCap is the rows a registry sweep inserts when the title filter
	// has no positive term and no --limit is named: with nothing to match, every
	// open role of every board passes, and one run would flood the inbox.
	openFilterCap         = 200
	offlineAfter          = 25
	maxRecordErrors       = 50
	flushEvery            = 2 * time.Second
	scanAccountingTimeout = 10 * time.Second
)

// ErrCancelled is the cancellation cause for a run stopped from the
// dashboard; any other cancellation is an interruption (SIGINT, SIGTERM).
var ErrCancelled = errors.New("scan cancelled")

var errHostOffline = errors.New("host offline")

var errScanBudget = errors.New("scan wall-clock budget reached")

// EvalRequest asks for the evaluation of one inbox row.
type EvalRequest struct {
	InboxID int64
	URL     string
	Mode    string // auto, screen or deep
	RunID   string // the scan run that promoted it
	Source  string
	// Text is the posting text when the row has no page to read (a lead's
	// own post). Title, Company and Location are the row's own; the
	// evaluation keeps them over what it reads from the page, whose heading
	// may be the employer's name rather than the role.
	Text, Title, Company, Location string
	// Scanned is the description the scan stored on the row.
	Scanned string
	// PostedAt is the date the source gave the posting; zero if it gave none.
	PostedAt time.Time
	// Review is the run id of the quick screen's review that this evaluation
	// in depth answers, when the inbox drain takes one (F-8).
	Review string
}

// Enqueuer starts an evaluation job and returns at once with its id.
type Enqueuer interface {
	Enqueue(ctx context.Context, req EvalRequest) (jobID string, err error)
}

// Gate is the deterministic eligibility gate. It returns the failing rule, or
// "" when the row passes. A nil Gate rejects nothing.
type Gate func(InboxRow) string

// Scanner runs scans.
type Scanner struct {
	Repo      *Repo
	Client    *Client
	Endpoints Endpoints
	Enqueuer  Enqueuer
	Gate      Gate
	// Secret returns the value of a vault secret, or "" when there is none.
	// Keyed sources read their key through it at run time.
	Secret func(name string) string
	// Mirror resolves a mirror row to the employer's posting; nil leaves
	// mirror rows unresolved.
	Mirror MirrorResolver
	// Leads receives the careers pages sources find; nil drops them after
	// counting.
	Leads LeadSink
	// Browser is the scan window and the clock of the browser sources; nil
	// leaves them unavailable.
	Browser *BrowserEnv
	// FeedModel answers one prompt of the LinkedIn feed classifier; nil leaves
	// the feed scan unable to classify.
	FeedModel func(ctx context.Context, system, prompt string) (string, error)
	// NeedsSignIn is told when a browser scan stops at a sign-in wall, and
	// PastSignIn when a browser scan completed past it.
	NeedsSignIn func(ctx context.Context, source, detail string)
	PastSignIn  func(ctx context.Context, source string)
	// Unrouted names an evaluation task with no model it can use, or "";
	// while it names one, the run's top rows wait in the inbox instead of
	// being sent to evaluation. Nil sends them.
	Unrouted func(context.Context) string
	// Progress receives one line per milestone; it may be nil.
	Progress func(string)

	// budget starts a run's wall-clock budget, a context that ends with
	// errScanBudget as its cause; nil starts a real timer. Tests set it to end
	// the budget on cue.
	budget func(context.Context, time.Duration) (context.Context, context.CancelFunc)
}

// ScanRequest is one scan. Flags override the profile's scan defaults.
type ScanRequest struct {
	Source     string
	Portals    workspace.Portals
	Profile    workspace.Profile
	Scope      string
	Sort       string
	TopN       *int
	Mode       string
	RetryKnown bool
	Budget     time.Duration

	// The query-like flags. Any of Search, Title, City, State or URL makes the
	// run ad hoc: it replaces the source's saved queries with one search.
	Search, Title, City, State, URL string
	// Limit caps the results of each query; it never makes a run ad hoc.
	Limit int
	// Window is the freshness window; zero uses the source's default.
	Window time.Duration
	// Fn, Seniority, Employment and Remote are the Speedrun board filters.
	Fn, Seniority, Employment string
	Remote                    bool

	// Pages is how many result pages a browser source reads (zero: the
	// source's default). IgnoreCooldown runs a source that is cooling down and
	// Force runs a scan the host-pressure check refused. EasyApplyOnly keeps
	// only the Easy Apply rows of a LinkedIn scan.
	Pages                                int
	IgnoreCooldown, Force, EasyApplyOnly bool

	// RunID names the run record the dashboard started this scan under; it is
	// kept on the scan record so a page can follow and cancel the run.
	RunID string
}

type insertedRow struct {
	id int64
	p  Posting
	// board and n place the row in the order the sweep listed it.
	board, n int
}

// run is the state of one scan.
type run struct {
	sc      *Scanner
	req     ScanRequest
	repo    *Repo
	client  *Client
	filters Filters
	scope   string
	// scopeFrom names where the scope came from, for the record.
	scopeFrom string
	order     string
	known     *Known

	detailSem chan struct{}
	dedupMu   sync.Mutex

	mu        sync.Mutex
	rec       Record
	inserted  []insertedRow
	lastFlush time.Time
	consecNet int
	reached   bool
	offline   bool
	pending   []Board
	budgetHit bool
	// insertCap is the rows a board sweep may insert in all (zero: no cap);
	// capUsed counts the rows taken so far and capHit marks the cap reached.
	insertCap, capUsed int
	capHit             bool
	fatal              error
	cancelWork         context.CancelCauseFunc

	// Feed sources.
	key            string
	feedIdx        int
	queriesRun     int
	queryFails     int
	queryTransient bool
	firstFail      error
	layout         string
	endReason      string

	// Browser sources.
	bs           *bscan
	hostDegraded bool
}

// Run executes one scan and records it. The record is closed on every exit
// path. A refused start (unknown source, bad scope, a scan of
// the same source already running) creates no record.
func (sc *Scanner) Run(ctx context.Context, req ScanRequest) (Record, error) {
	_, err := LookupSource(req.Source)
	if err != nil {
		return Record{}, err
	}
	if req.Budget <= 0 {
		req.Budget = defaultBudget
	}
	start := sc.budget
	if start == nil {
		start = func(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
			return context.WithTimeoutCause(ctx, d, errScanBudget)
		}
	}
	ctx, cancel := start(ctx, req.Budget)
	defer cancel()
	r, err := sc.newRun(ctx, req)
	if err != nil {
		return Record{}, err
	}
	rec, err := sc.Repo.ScanStart(ctx, req.Source, r.params())
	if err != nil {
		return Record{}, err
	}
	r.rec = rec
	return r.finish(ctx, r.execute(ctx))
}

func (sc *Scanner) newRun(ctx context.Context, req ScanRequest) (*run, error) {
	defaults := req.Profile.Scan.Defaults[req.Source]
	scope, scopeFrom := req.Scope, "flag"
	if scope == "" {
		scope, scopeFrom = defaults.LocationScope, "profile scan.defaults"
	}
	if scope == "" {
		// The evaluation's geography rule reads the same lists in the same order.
		countries, from := req.Profile.EligibilityGate.AllowedCountries, "eligibility_gate.allowed_countries"
		if len(countries) == 0 {
			countries, from = req.Profile.WorkAuthorization.AuthorizedCountries, "work_authorization.authorized_countries"
		}
		scope, scopeFrom = ScopeFromCountries(countries), from
	}
	if err := ValidateScope(scope); err != nil {
		return nil, err
	}
	order := req.Sort
	if order == "" {
		order = SortScore
	}
	if order != SortScore && order != SortRecency {
		return nil, fmt.Errorf("unknown sort %q; valid sorts are score, recency", order)
	}
	mode := req.Mode
	if mode == "" {
		mode = defaults.EvaluationMode
	}
	if mode != "" && mode != "auto" && mode != "screen" && mode != "deep" {
		return nil, fmt.Errorf("unknown evaluation mode %q; valid modes are auto, screen, deep", mode)
	}
	req.Mode = evalMode(req.Source, mode)
	if req.TopN == nil {
		n := defaultTopN
		if req.Source == "linkedin-feed" {
			n = feedDefaultEval
		}
		if defaults.EvaluateTopN != nil {
			n = *defaults.EvaluateTopN
		}
		if req.Source == "linkedin-feed" && defaults.EvaluateLimit != nil {
			n = *defaults.EvaluateLimit
		}
		req.TopN = &n
	}
	blocked, err := sc.Repo.blockedNames(ctx)
	if err != nil {
		return nil, err
	}
	base := sc.Client
	if base == nil {
		base = NewClient()
	}
	client := base.Session(req.Portals.Concurrency.EffectiveList())
	r := &run{sc: sc, req: req, repo: sc.Repo, client: client, scope: scope, scopeFrom: scopeFrom, order: order,
		filters:   NewFilters(req.Portals, blocked),
		detailSem: make(chan struct{}, req.Portals.Concurrency.EffectiveDetail())}
	if feedReaderFor(req.Source) != nil {
		return r, nil
	}
	r.insertCap = req.Limit
	if r.insertCap <= 0 && req.Source == "registry" && len(req.Portals.Filters.Title.Positive) == 0 {
		r.insertCap = openFilterCap
	}
	return r, nil
}

// params records every effective parameter and where it came from.
func (r *run) params() map[string]any {
	p := r.req.Portals
	out := map[string]any{
		"scope": r.scope, "scope_from": r.scopeFrom, "sort": r.order, "evaluate_top_n": *r.req.TopN, "evaluation_mode": r.req.Mode,
		"retry_known": r.req.RetryKnown, "insert_cap": r.insertCap, "budget_seconds": int(r.req.Budget.Seconds()),
		"concurrency_list": p.Concurrency.EffectiveList(), "concurrency_detail": p.Concurrency.EffectiveDetail(),
		"filters": map[string]int{
			"title_positive": len(p.Filters.Title.Positive), "title_negative": len(p.Filters.Title.Negative),
			"seniority_boost": len(p.Filters.Title.SeniorityBoost), "locations_include": len(p.Filters.Locations.Include),
			"locations_exclude": len(p.Filters.Locations.Exclude), "skip_companies": len(p.Filters.SkipCompanies),
		},
	}
	if r.req.RunID != "" {
		out["run_id"] = r.req.RunID
	}
	return out
}

func (r *run) note(format string, a ...any) {
	if r.sc.Progress != nil {
		r.sc.Progress(fmt.Sprintf("[%s] ", r.req.Source) + fmt.Sprintf(format, a...))
	}
}

// outcome is how execute ended.
type outcome struct {
	retryable      bool
	status, reason string
	err            error
}

// execute runs the sweep and the top-N enqueue.
func (r *run) execute(ctx context.Context) (out outcome) {
	defer func() {
		if p := recover(); p != nil {
			out = outcome{status: RunFailed, err: fmt.Errorf("scan panicked: %v", p)}
		}
		// Admission may return after accepting the last job, or break its
		// loop without an error. Neither can hide a cancellation or budget.
		if endedByContext(ctx, out.err) && (out.err != nil || out.status == RunCompleted || out.status == RunInterrupted || out.status == RunCancelled) {
			out = r.interrupted(ctx)
		}
	}()
	known, err := r.repo.LoadKnown(ctx)
	if err != nil {
		return outcome{status: RunFailed, err: err}
	}
	r.known = known
	if feedReaderFor(r.req.Source) != nil {
		return r.executeFeed(ctx)
	}
	boards, skip, err := r.collectBoards(ctx)
	if err != nil {
		return outcome{status: RunFailed, err: err}
	}
	if skip != "" {
		return outcome{status: RunSkipped, reason: skip}
	}
	if r.insertCap == openFilterCap && r.req.Limit <= 0 {
		r.note("WARNING: portals.yml filters.title.positive is empty, so every role on every board passes; inserting at most %d rows (set title terms, or pass --limit)", openFilterCap)
	}
	r.sweep(ctx, boards)
	switch {
	case r.offline:
		return outcome{status: RunFailed, reason: "host-offline", err: errors.New("every board visit failed at the network level; no board was struck and nothing was written")}
	case r.fatal != nil:
		return outcome{status: RunFailed, err: r.fatal}
	case ctx.Err() != nil:
		return r.interrupted(ctx)
	}
	if err := r.enqueueTop(ctx); err != nil {
		if endedByContext(ctx, err) {
			return r.interrupted(ctx)
		}
		return outcome{status: RunFailed, err: err}
	}
	out = outcome{status: RunCompleted}
	if r.capHit {
		out.reason = ReasonLimit
	}
	return out
}

// endedByContext distinguishes cancellation from an independent failure that
// happened while the same context was ending.
func endedByContext(ctx context.Context, err error) bool {
	if ctx.Err() == nil {
		return false
	}
	switch e := err.(type) {
	case nil:
		return true
	case interface{ Unwrap() []error }:
		causes := e.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !endedByContext(ctx, cause) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		if cause := e.Unwrap(); cause != nil {
			return endedByContext(ctx, cause)
		}
	}
	return err == ctx.Err() || err == context.Cause(ctx)
}

func (r *run) interrupted(ctx context.Context) outcome {
	if errors.Is(context.Cause(ctx), errScanBudget) {
		return outcome{status: RunCompleted, reason: ReasonBudget}
	}
	if errors.Is(context.Cause(ctx), ErrCancelled) {
		return outcome{status: RunCancelled, reason: "cancelled"}
	}
	return outcome{status: RunInterrupted, reason: "interrupted"}
}

// finish closes the record on every exit path, even when ctx is done.
func (r *run) finish(ctx context.Context, out outcome) (Record, error) {
	r.mu.Lock()
	rec := r.rec.clone()
	r.mu.Unlock()
	rec.Status, rec.Reason = out.status, out.reason
	rec.Retryable = out.status == RunFailed && (out.retryable || out.reason == "host-offline" || retryableFailure(out.err))
	if out.err != nil {
		rec.Error = out.err.Error()
	}
	if rec.Status == RunCompleted {
		if err := rec.Counts.Validate(); err != nil {
			rec.Status, rec.Error = RunFailed, err.Error()
		}
	}
	end, cancel := context.WithTimeout(context.WithoutCancel(ctx), scanAccountingTimeout)
	defer cancel()
	closed, err := r.repo.ScanFinish(end, rec)
	if err != nil {
		closed.Status, closed.Retryable = RunFailed, false
		closed.Error = joinErr(closed.Error, "recording the run: "+err.Error())
		err = fmt.Errorf("recording scan result: %w", err)
	}
	r.note("%s: %s", closed.Status, summary(closed))
	return closed, err
}

func joinErr(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

func summary(rec Record) string {
	c := rec.Counts
	return fmt.Sprintf("discovered %d, filtered %d, duplicates %d, inserted %d, queued %d, board errors %d",
		c.Discovered, sumMap(c.Filtered), sumMap(c.Duplicates), c.Inserted, c.Queued, c.BoardErrors)
}

// noteError keeps a bounded list of board-level problems in the record.
func (r *run) noteError(format string, a ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.rec.Errors) < maxRecordErrors {
		r.rec.Errors = append(r.rec.Errors, fmt.Sprintf(format, a...))
	}
}

// flush saves the counts reached so far, at most every couple of seconds.
func (r *run) flush(ctx context.Context) {
	r.mu.Lock()
	if r.repo.now().Sub(r.lastFlush) < flushEvery {
		r.mu.Unlock()
		return
	}
	r.lastFlush = r.repo.now()
	rec := r.rec.clone()
	r.mu.Unlock()
	end, cancel := context.WithTimeout(context.WithoutCancel(ctx), scanAccountingTimeout)
	defer cancel()
	_ = r.repo.ScanSave(end, rec)
}

// retryableFailure uses typed causes only. Challenge/status walls have their
// own terminal statuses and never reach this classifier as retryable failures.
func retryableFailure(err error) bool {
	var temporary *TransientError
	var network net.Error
	return errors.As(err, &temporary) || errors.As(err, &network) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrOffline) || errors.Is(err, errHostOffline)
}
