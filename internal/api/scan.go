package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/discovery"
	"applysmith/internal/evaluate"
	"applysmith/internal/scheduler"
	"applysmith/internal/scheduler/jobs"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

// maxScanBudget bounds a requested scan budget; the default is 30 minutes.
const maxScanBudget = 6 * time.Hour

var errScanShutdown = errors.New("server shutting down")

// scanRuns tracks the scans this process runs, so they can be cancelled and
// so shutdown can wait for them.
type scanRuns struct {
	mu      sync.Mutex
	cancels map[string]context.CancelCauseFunc
	claimed map[string]bool // sources with a scan admitted and not yet ended
	closed  bool
	wg      sync.WaitGroup
}

func newScanRuns() *scanRuns {
	return &scanRuns{cancels: map[string]context.CancelCauseFunc{}, claimed: map[string]bool{}}
}

// claim reserves a source for a new scan. The scan record that the sweep
// writes does not exist yet when the request is admitted, so two requests that
// arrive together would otherwise both start; the claim ends with release.
func (r *scanRuns) claim(source string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.claimed[source] {
		return false
	}
	r.claimed[source] = true
	return true
}

// busy reports whether a scan of the source is admitted and not yet ended.
func (r *scanRuns) busy(source string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.claimed[source]
}

func (r *scanRuns) release(source string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.claimed, source)
}

// start runs fn in the background under a context that cancel and close end,
// and releases the source's claim when fn returns.
func (r *scanRuns) start(parent context.Context, id, source string, fn func(context.Context)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return apikit.Unavailable("the server is shutting down")
	}
	ctx, cancel := context.WithCancelCause(context.WithoutCancel(parent))
	r.cancels[id] = cancel
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer func() {
			r.mu.Lock()
			delete(r.cancels, id)
			delete(r.claimed, source)
			r.mu.Unlock()
			cancel(nil)
		}()
		fn(ctx)
	}()
	return nil
}

// cancel asks a scan to stop as a dashboard cancellation.
func (r *scanRuns) cancel(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.cancels[id]
	if ok {
		c(discovery.ErrCancelled)
	}
	return ok
}

// close interrupts every scan and waits for them to record their end.
func (r *scanRuns) close() {
	r.mu.Lock()
	r.closed = true
	for _, c := range r.cancels {
		c(errScanShutdown)
	}
	r.mu.Unlock()
	r.wg.Wait()
}

// packEnv is what the packs are built from: the machine's zone and the
// discovery sources the user's portals file enables.
func (s *Server) packEnv() (scheduler.PackEnv, *apikit.Error) {
	portals, e := loadPortals(s.cfg.Workspace)
	if e != nil {
		return scheduler.PackEnv{}, e
	}
	return jobs.PackEnv(portals), nil
}

// sourceTitle is a scan source's readable name ("Company job boards" for
// registry), else the name itself. The runs board titles scans with it.
func sourceTitle(name string) string {
	if src, err := discovery.LookupSource(name); err == nil {
		return src.Title
	}
	return name
}

func loadPortals(ws *workspace.Workspace) (workspace.Portals, *apikit.Error) {
	portals := workspace.Portals{Version: 1}
	c, err := ws.Read(workspace.FilePortals)
	if err != nil {
		return portals, apikit.NewErr(wire.CodeInternal, "internal error")
	}
	if c.Revision == workspace.RevisionAbsent {
		return portals, nil
	}
	portals, rep := workspace.ParsePortals(c.Data)
	if !rep.OK() {
		return portals, apikit.NewErr(wire.CodeConfigInvalid, "portals.yml is invalid: %s", rep.String())
	}
	return portals, nil
}

func loadProfile(ws *workspace.Workspace) (workspace.Profile, *apikit.Error) {
	c, err := ws.Read(workspace.FileProfile)
	if err != nil {
		return workspace.Profile{}, apikit.NewErr(wire.CodeInternal, "internal error")
	}
	if c.Revision == workspace.RevisionAbsent {
		return workspace.Profile{}, nil
	}
	profile, rep := workspace.ParseProfile(c.Data)
	if !rep.OK() {
		return profile, apikit.NewErr(wire.CodeConfigInvalid, "profile.yml is invalid: %s", rep.String())
	}
	return profile, nil
}

// scanRequest validates the wire request into a scan request and the
// normalized body that decides replay against conflict.
func scanRequest(req wire.ScanRequest) (discovery.ScanRequest, []byte, *apikit.Error) {
	req.Source = strings.ToLower(strings.TrimSpace(req.Source))
	if _, err := discovery.LookupSource(req.Source); err != nil {
		return discovery.ScanRequest{}, nil, apikit.InvalidField("source", "%s", err.Error())
	}
	if req.Scope != "" {
		if err := discovery.ValidateScope(req.Scope); err != nil {
			return discovery.ScanRequest{}, nil, apikit.InvalidField("scope", "%s", err.Error())
		}
	}
	if req.Sort != "" && req.Sort != discovery.SortScore && req.Sort != discovery.SortRecency {
		return discovery.ScanRequest{}, nil, apikit.InvalidField("sort", "sort must be score or recency")
	}
	if req.Mode != "" && !slices.Contains([]string{wire.ModeAuto, wire.ModeScreen, wire.ModeDeep}, req.Mode) {
		return discovery.ScanRequest{}, nil, apikit.InvalidField("mode", "mode must be auto, screen or deep")
	}
	if req.TopN != nil && (*req.TopN < 0 || *req.TopN > 500) {
		return discovery.ScanRequest{}, nil, apikit.InvalidField("topN", "topN must be between 0 and 500")
	}
	if b := time.Duration(req.BudgetSeconds) * time.Second; req.BudgetSeconds < 0 || b > maxScanBudget {
		return discovery.ScanRequest{}, nil, apikit.InvalidField("budgetSeconds", "budgetSeconds must be between 0 and %d", int(maxScanBudget.Seconds()))
	}
	if req.Limit < 0 || req.Limit > 1000 {
		return discovery.ScanRequest{}, nil, apikit.InvalidField("limit", "limit must be between 0 and 1000")
	}
	var window time.Duration
	if req.Pages < 0 || req.Pages > 50 {
		return discovery.ScanRequest{}, nil, apikit.InvalidField("pages", "pages must be between 0 and 50")
	}
	if req.PostedWithin == "any" {
		window = -1
	} else if req.PostedWithin != "" {
		var werr error
		if window, werr = discovery.ParseWindow(req.PostedWithin); werr != nil {
			return discovery.ScanRequest{}, nil, apikit.InvalidField("postedWithin", "%s", werr.Error())
		}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return discovery.ScanRequest{}, nil, apikit.NewErr(wire.CodeInternal, "internal error")
	}
	out := discovery.ScanRequest{Source: req.Source, Scope: req.Scope, Sort: req.Sort, Mode: req.Mode,
		RetryKnown: req.RetryKnown, Budget: time.Duration(req.BudgetSeconds) * time.Second,
		Search: req.Search, Title: req.Title, City: req.City, State: req.State, URL: req.URL, Limit: int(req.Limit), Window: window,
		Fn: req.Fn, Seniority: req.Seniority, Employment: req.Employment, Remote: req.Remote,
		Pages: int(req.Pages), IgnoreCooldown: req.IgnoreCooldown, Force: req.Force, EasyApplyOnly: req.EasyApplyOnly}
	if req.TopN != nil {
		n := int(*req.TopN)
		out.TopN = &n
	}
	return out, body, nil
}

func (s *Server) createScan(c *apikit.Call) (apikit.Reply, error) {
	key, e := apikit.IdempotencyKey(c)
	if e != nil {
		return apikit.Reply{}, e
	}
	var wreq wire.ScanRequest
	if e := apikit.Decode(c.Body, &wreq); e != nil {
		return apikit.Reply{}, e
	}
	ref, err := s.launchScan(c.R.Context(), c.P.ID, key, wreq)
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Status: http.StatusAccepted, Location: "/api/runs/" + ref.RunID, Data: wire.RunAccepted{RunID: ref.RunID}}, nil
}

// launchScan admits a scan and starts it in the background. The dashboard's
// route and the Telegram scan button both come through it.
func (s *Server) launchScan(ctx context.Context, principal, key string, wreq wire.ScanRequest) (store.RunRef, error) {
	req, body, e := scanRequest(wreq)
	if e != nil {
		return store.RunRef{}, e
	}
	var e2 *apikit.Error
	if req.Portals, e2 = loadPortals(s.cfg.Workspace); e2 != nil {
		return store.RunRef{}, e2
	}
	if req.Profile, e2 = loadProfile(s.cfg.Workspace); e2 != nil {
		return store.RunRef{}, e2
	}
	d, err := s.dom()
	if err != nil {
		return store.RunRef{}, err
	}
	st, err := s.db()
	if err != nil {
		return store.RunRef{}, err
	}
	claimed := false
	ref, err := st.CreateRun(ctx, store.RunRequest{
		Principal: principal, Operation: "scan", Key: key, Kind: "scan", Input: body,
		Admit: func(ctx context.Context) error {
			if err := refuseRunningScan(ctx, d.repo, req.Source); err != nil {
				return err
			}
			if !claimed && !d.scans.claim(req.Source) {
				return &discovery.RunningError{Source: req.Source}
			}
			claimed = true
			return nil
		},
	})
	var running *discovery.RunningError
	switch {
	case errors.As(err, &running):
		if running.RunID == "" {
			return store.RunRef{}, apikit.NewErr(wire.CodeConflict, "a %s scan is already running", running.Source)
		}
		return store.RunRef{}, apikit.NewErr(wire.CodeConflict, "%s", running.Error())
	case errors.Is(err, store.ErrConflict):
		return store.RunRef{}, apikit.NewErr(wire.CodeConflict, "this Idempotency-Key was used with a different request")
	case err != nil:
		if claimed {
			d.scans.release(req.Source)
		}
		return store.RunRef{}, err
	}
	if !ref.Replayed {
		req.RunID = ref.RunID
		if err := d.scans.start(s.bg, ref.RunID, req.Source, func(ctx context.Context) { s.runScan(ctx, d, ref.RunID, req) }); err != nil {
			d.scans.release(req.Source)
			s.endRun(ref.RunID, wire.RunFailed, "the server is shutting down")
			return store.RunRef{}, err
		}
	}
	return ref, nil
}

// refuseRunningScan is the admission check of a new scan: one scan per source
// at a time.
func refuseRunningScan(ctx context.Context, repo *discovery.Repo, source string) error {
	recent, err := repo.ScanList(ctx, source, 1)
	if err != nil {
		return err
	}
	if len(recent) == 1 && recent[0].Status == discovery.RunRunning {
		return &discovery.RunningError{Source: source, RunID: recent[0].ID}
	}
	return nil
}

// endRun closes a run row that never got as far as running.
func (s *Server) endRun(id, status, reason string) {
	if st := s.store.Load(); st != nil {
		_ = st.SetRun(context.WithoutCancel(s.bg), id, status, "", reason)
	}
}

// runScan executes one scan and records how it ended on the run row and the
// run stream.
func (s *Server) runScan(ctx context.Context, d *domain, runID string, req discovery.ScanRequest) {
	st := s.store.Load()
	end := context.WithoutCancel(ctx)
	_ = st.SetRun(end, runID, wire.RunRunning, "sweep", "")
	s.notify("runs", "scans")
	scanner := s.newScanner(d, req.Profile, func(line string) { s.runs.publish(runID, "progress", wire.RunProgress{RunID: runID, Line: line}) })
	rec, err := scanner.Run(ctx, req)
	final := wire.RunFinal{RunID: runID, ScanID: rec.ID, Outcome: rec.Status, Reason: rec.Reason}
	switch {
	case err != nil:
		final.Status, final.Reason = wire.RunFailed, fmt.Sprintf("%.200s", err.Error())
	case rec.Status == discovery.RunCompleted || rec.Status == discovery.RunSkipped:
		final.Status = wire.RunSucceeded
	case rec.Status == discovery.RunCancelled:
		final.Status = wire.RunCancelled
	default:
		final.Status = wire.RunFailed
		if final.Reason == "" {
			final.Reason = rec.Status
		}
	}
	failure := ""
	if final.Status == wire.RunFailed {
		failure = final.Reason
	}
	if err := st.SetRun(end, runID, final.Status, "", failure); err != nil {
		s.log.Error("recording the end of a scan failed", "run", runID, "error", err.Error())
	}
	s.runs.publish(runID, "final", final)
	s.runs.retire(runID)
	s.notify("runs", "scans")
}

// newScanner builds the scanner the dashboard and the scheduler both use.
func (s *Server) newScanner(d *domain, profile workspace.Profile, progress func(string)) *discovery.Scanner {
	scanner := &discovery.Scanner{
		Repo: d.repo, Client: discovery.NewClient(), Endpoints: s.cfg.Domain.ScanEndpoints,
		Enqueuer:    evaluate.PoolEnqueuer{Pool: d.pool},
		Gate:        evaluate.ScannerGate(profile, evaluate.BlockedFromDiscovery(d.repo)),
		Progress:    progress,
		Secret:      VaultSecret(s.cfg.Vault),
		Mirror:      scanMirror{s.mirrorSources(d)},
		Leads:       d.repo,
		FeedModel:   d.router.TaskFunc("feed-classify"),
		Unrouted:    d.router.EvaluationUnrouted,
		NeedsSignIn: s.scanNeedsSignIn,
		PastSignIn:  s.scanPastSignIn,
	}
	if w := s.cfg.Domain.ScanWindow; w != nil {
		scanner.Browser = &discovery.BrowserEnv{Window: w}
	}
	if h := s.cfg.Domain.ScanHTTP; h != nil {
		scanner.Client.HTTP = h
	}
	return scanner
}

// scheduledScan runs a scan for the scheduler. It takes the same per-source
// claim as a dashboard scan, so the two never run the same source at once.
func (s *Server) scheduledScan(d *domain) jobs.ScanFunc {
	return func(ctx context.Context, req discovery.ScanRequest, progress func(string)) (discovery.Record, error) {
		if !d.scans.claim(req.Source) {
			return discovery.Record{}, &discovery.RunningError{Source: req.Source}
		}
		defer d.scans.release(req.Source)
		rec, err := s.newScanner(d, req.Profile, progress).Run(ctx, req)
		if err == nil {
			s.afterScheduledScan(ctx, d, rec)
		}
		return rec, err
	}
}

func (s *Server) listScans(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	q := apikit.QueryOf(c)
	f := discovery.ScanFilter{Source: q.Str("source"), Status: q.Str("status"), Limit: q.Integer("limit", 20)}
	page := q.Integer("page", 1)
	if f.Limit < 1 || f.Limit > 100 {
		q.Fail(apikit.InvalidField("limit", "limit must be between 1 and 100"))
	}
	if page < 1 {
		q.Fail(apikit.InvalidField("page", "page must be at least 1"))
	}
	f.From, f.To = q.Day("from"), q.Day("to")
	if q.Err != nil {
		return apikit.Reply{}, q.Err
	}
	// The end date is a whole local day.
	if !f.To.IsZero() {
		f.To = f.To.AddDate(0, 0, 1)
	}
	f.Offset = (page - 1) * f.Limit
	recs, total, err := d.repo.ScanPage(c.R.Context(), f)
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.ScanList{Scans: make([]wire.ScanRecord, len(recs)), Page: apikit.Safe(page), Limit: apikit.Safe(f.Limit), Total: apikit.Safe(total)}
	for i, r := range recs {
		out.Scans[i] = WireScanRecord(r)
	}
	return apikit.Reply{Data: out}, nil
}

func (s *Server) getScan(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	rec, err := d.repo.ScanGet(c.R.Context(), c.R.PathValue("id"))
	if errors.Is(err, discovery.ErrNotFound) {
		return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "no such scan")
	}
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: WireScanRecord(rec)}, nil
}

func WireScanRecord(r discovery.Record) wire.ScanRecord {
	c := r.Counts
	runID, _ := r.Params["run_id"].(string)
	return wire.ScanRecord{
		ID: r.ID, Source: r.Source, RunID: runID, Status: r.Status, Reason: r.Reason, Error: r.Error,
		StartedAt: wire.Time{Time: r.Started.UTC()}, UpdatedAt: wire.Time{Time: r.Updated.UTC()}, FinishedAt: apikit.TimePtr(r.Finished),
		DurationMs: apikit.Safe64(r.DurationMS), BoardsVisited: apikit.Safe(r.Boards),
		Counts: wire.ScanCounts{
			Discovered: apikit.Safe(c.Discovered), Filtered: apikit.SafeMap(c.Filtered), Undated: apikit.Safe(c.Undated), Duplicates: apikit.SafeMap(c.Duplicates),
			Inserted: apikit.Safe(c.Inserted), Queued: apikit.Safe(c.Queued), Enriched: apikit.Safe(c.Enriched), EnrichmentDegraded: apikit.Safe(c.EnrichmentDegraded),
			EnrichmentFailed: apikit.Safe(c.EnrichmentFailed), EnrichmentSkipped: apikit.Safe(c.EnrichmentSkipped), GateRejected: apikit.Safe(c.GateRejected),
			Outcomes: apikit.SafeMap(c.Outcomes), TruncatedPostings: apikit.Safe(c.TruncatedPostings), BoardErrors: apikit.Safe(c.BoardErrors),
		},
		Truncated: r.Truncated, Deferred: apikit.SafeMap(r.Deferred), Suspended: r.Suspended, Errors: r.Errors,
	}
}
