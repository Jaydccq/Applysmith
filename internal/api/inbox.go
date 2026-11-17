package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/trackerapi"
	"applysmith/internal/api/wire"
	"applysmith/internal/discovery"
	"applysmith/internal/evaluate"
	"applysmith/internal/store"
)

var inboxStatuses = []string{wire.InboxPending, wire.InboxQueued, wire.InboxEvaluated, wire.InboxDismissed, wire.InboxExpired}

func WireInboxRow(x discovery.InboxRow) wire.InboxRow {
	out := wire.InboxRow{
		ID: apikit.Safe64(x.ID), URL: x.URL, OriginalURL: x.OriginalURL, Aliases: apikit.NonNil(x.Aliases), IdentityKey: x.IdentityKey,
		Company: x.Company, Role: x.Role, Location: x.Location, Source: x.Source, Detail: x.Detail, Score: apikit.Safe(x.Score),
		Reasons: apikit.NonNil(x.Reasons), EarlyCareer: x.EarlyCareer, ClearanceRequired: x.ClearanceRequired, Flags: apikit.NonNil(x.Flags),
		Notes: x.Notes, Status: x.Status, Attempts: apikit.Safe(x.Attempts), JobID: x.JobID, RunID: x.RunID,
		FirstSeenAt: wire.Time{Time: x.FirstSeen.UTC()}, UpdatedAt: wire.Time{Time: x.Updated.UTC()},
	}
	if x.RequiredYears != nil {
		y := apikit.Safe(*x.RequiredYears)
		out.RequiredYears = &y
	}
	return out
}

// inboxError maps a discovery failure onto the API errors.
func inboxError(err error) error {
	switch {
	case errors.Is(err, discovery.ErrNotFound):
		return apikit.NewErr(wire.CodeNotFound, "no such inbox row")
	case errors.Is(err, discovery.ErrBadTransition):
		return apikit.NewErr(wire.CodeConflict, "%s", strings.TrimPrefix(err.Error(), discovery.ErrBadTransition.Error()+": "))
	}
	return err
}

func (s *Server) listInbox(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	q := apikit.QueryOf(c)
	status, runID := q.Str("status"), q.Str("runId")
	page, size := q.Paging(25)
	if status != "" && !slices.Contains(inboxStatuses, status) {
		q.Fail(apikit.InvalidField("status", "status must be one of %s", strings.Join(inboxStatuses, ", ")))
	}
	since := q.Day("since")
	if q.Err != nil {
		return apikit.Reply{}, q.Err
	}
	iq := discovery.InboxQuery{Status: status, RunID: runID, Text: q.Str("q"), Since: since, Limit: size, Offset: (page - 1) * size}
	rows, err := d.repo.InboxList(c.R.Context(), iq)
	if err != nil {
		return apikit.Reply{}, err
	}
	total, err := d.repo.InboxCount(c.R.Context(), iq)
	if err != nil {
		return apikit.Reply{}, err
	}
	counts, err := d.repo.InboxCounts(c.R.Context(), since)
	if err != nil {
		return apikit.Reply{}, err
	}
	st, err := s.db()
	if err != nil {
		return apikit.Reply{}, err
	}
	list, err := WireInboxRows(c.R.Context(), st, rows)
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: wire.InboxPage{Rows: list, Page: apikit.Safe(page), PageSize: apikit.Safe(size), Total: apikit.Safe(total), Counts: apikit.SafeMap(counts), Unrouted: d.router.EvaluationUnrouted(c.R.Context())}}, nil
}

// WireInboxRows is the listing form of inbox rows: an evaluated row also says
// what its run came to, its outcome, and the report number and score when it
// wrote a report.
func WireInboxRows(ctx context.Context, st *store.Store, rows []discovery.InboxRow) ([]wire.InboxRow, error) {
	out := make([]wire.InboxRow, len(rows))
	for i, x := range rows {
		out[i] = WireInboxRow(x)
		if x.Status != discovery.InboxEvaluated || x.JobID == "" {
			continue
		}
		res, _, found, err := evaluate.JobOutcome(ctx, st, x.JobID)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		out[i].Outcome, out[i].ReportScore = string(res.Outcome), res.Score
		if res.ReportNumber > 0 {
			n := apikit.Safe64(res.ReportNumber)
			out[i].ReportNumber = &n
		}
	}
	return out, nil
}

func (s *Server) dismissInboxRow(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	if err := d.repo.InboxDismiss(c.R.Context(), id); err != nil {
		return apikit.Reply{}, inboxError(err)
	}
	row, err := d.repo.InboxGet(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, inboxError(err)
	}
	s.notify("scans")
	return apikit.Reply{Data: WireInboxRow(row)}, nil
}

// restoreInboxRow puts a dismissed row back to waiting.
func (s *Server) restoreInboxRow(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	if err := d.repo.InboxRestore(c.R.Context(), id); err != nil {
		return apikit.Reply{}, inboxError(err)
	}
	row, err := d.repo.InboxGet(c.R.Context(), id)
	if err != nil {
		return apikit.Reply{}, inboxError(err)
	}
	s.notify("scans")
	return apikit.Reply{Data: WireInboxRow(row)}, nil
}

// promoteInbox queues pending rows for evaluation behind the eligibility gate:
// the rows named, or the top best pending ones. The rows start evaluations,
// so they are admitted one by one and the answer says which were queued and
// which the gate kept pending.
func (s *Server) promoteInbox(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.InboxPromoteRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if req.Top != 0 && (len(req.IDs) > 0 || req.Top < 1 || req.Top > trackerapi.MaxBulkRows) {
		return apikit.Reply{}, apikit.InvalidField("top", "top must be between 1 and %d, without ids", trackerapi.MaxBulkRows)
	}
	if req.Top == 0 && (len(req.IDs) < 1 || len(req.IDs) > trackerapi.MaxBulkRows) {
		return apikit.Reply{}, apikit.InvalidField("ids", "ids must name between 1 and %d rows", trackerapi.MaxBulkRows)
	}
	ids := make([]int64, len(req.IDs))
	for i, id := range req.IDs {
		if id < 1 {
			return apikit.Reply{}, apikit.InvalidField("ids", "ids must be positive whole numbers")
		}
		ids[i] = int64(id)
	}
	if err := evaluationModelMissing(c.R.Context(), d); err != nil {
		return apikit.Reply{}, err
	}
	profile, e := loadProfile(s.cfg.Workspace)
	if e != nil {
		return apikit.Reply{}, e
	}
	gate := evaluate.ScannerGate(profile, evaluate.BlockedFromDiscovery(d.repo))
	enq := evaluate.PoolEnqueuer{Pool: d.pool, Priority: evaluate.PriorityUser}
	var queued []int64
	var rejected map[int64]string
	if req.Top > 0 {
		queued, rejected, err = d.repo.PromoteTop(c.R.Context(), enq, gate, int(req.Top))
	} else {
		queued, rejected, err = d.repo.Promote(c.R.Context(), enq, gate, ids...)
	}
	if len(queued) > 0 {
		s.notify("scans", "runs")
	}
	if err != nil {
		if errors.Is(err, evaluate.ErrQueueFull) {
			qe := apikit.Unavailable("the evaluation queue is full; retry shortly")
			qe.Details = &wire.ErrorDetails{Reason: wire.ReasonQueueFull}
			return apikit.Reply{}, qe
		}
		return apikit.Reply{}, inboxError(err)
	}
	out := wire.InboxPromoteResult{Queued: make([]wire.SafeInt, len(queued)), Rejected: map[string]string{}}
	for i, id := range queued {
		out.Queued[i] = apikit.Safe64(id)
	}
	for id, rule := range rejected {
		out.Rejected[strconv.FormatInt(id, 10)] = rule
	}
	return apikit.Reply{Data: out}, nil
}

// inboxEvaluate is the body of the scheduled inbox.evaluate job: the reviews
// waiting on the board go to evaluation in depth first (F-8), then the rows a
// scan left waiting, without a press, behind the gate a promotion uses and
// never once the day's model spend limit is reached, nor while an evaluation
// task has no model. The evaluations name the job's run, whose roll-up tells
// them (F-2).
func (s *Server) inboxEvaluate(d *domain, st *store.Store) func(context.Context, int, string) (string, error) {
	return func(ctx context.Context, limit int, runID string) (string, error) {
		if task := d.router.EvaluationUnrouted(ctx); task != "" {
			return fmt.Sprintf("nothing sent to evaluation: the %s task has no model it can use; choose one under Settings, Models", task), nil
		}
		profile, e := loadProfile(s.cfg.Workspace)
		if e != nil {
			return "", e
		}
		reviews, err := evaluate.OpenReviews(ctx, st, limit)
		if err != nil {
			return "", err
		}
		gate := evaluate.ScannerGate(profile, evaluate.BlockedFromDiscovery(d.repo))
		res, err := d.repo.EvaluateWaiting(ctx, evaluate.PoolEnqueuer{Pool: d.pool, Drain: runID}, gate, limit, d.router.EvaluationHeld, reviews)
		if res.Reviews+res.Queued+res.Rejected > 0 {
			s.notify("scans", "runs")
		}
		return res.Summary(), err
	}
}

// inboxWaiting says whether the inbox drain has work after a scan: a waiting
// row it would try, or a review it takes first (F-8).
func inboxWaiting(d *domain, st *store.Store) func(context.Context) (bool, error) {
	return func(ctx context.Context) (bool, error) {
		if ok, err := d.repo.HasWaiting(ctx); ok || err != nil {
			return ok, err
		}
		reviews, err := evaluate.OpenReviews(ctx, st, 1)
		return len(reviews) > 0, err
	}
}

func WireBlocked(b discovery.BlockedCompany) wire.BlockedCompany {
	return wire.BlockedCompany{ID: apikit.Safe64(b.ID), Name: b.Name, Normalized: b.Normalized, Reason: b.Reason, AddedBy: b.AddedBy, AddedAt: wire.Time{Time: b.AddedAt.UTC()}}
}

func (s *Server) blockedList(c *apikit.Call) (wire.BlockedCompanyList, error) {
	d, err := s.dom()
	if err != nil {
		return wire.BlockedCompanyList{}, err
	}
	list, err := d.repo.BlockedCompanies(c.R.Context())
	if err != nil {
		return wire.BlockedCompanyList{}, err
	}
	out := wire.BlockedCompanyList{Companies: make([]wire.BlockedCompany, len(list))}
	for i, b := range list {
		out.Companies[i] = WireBlocked(b)
	}
	return out, nil
}

func (s *Server) listBlocked(c *apikit.Call) (apikit.Reply, error) {
	out, err := s.blockedList(c)
	return apikit.Reply{Data: out}, err
}

func (s *Server) blockCompany(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	var req wire.BlockedCompanyRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if len(req.Reason) > 500 || len(req.Name) > 200 {
		return apikit.Reply{}, apikit.InvalidField("name", "name holds at most 200 characters and reason at most 500")
	}
	if discovery.NormalizeCompany(req.Name) == "" {
		return apikit.Reply{}, apikit.InvalidField("name", "%q names no employer", req.Name)
	}
	b, err := d.repo.BlockCompany(c.R.Context(), req.Name, req.Reason, string(c.P.Kind))
	if err != nil {
		return apikit.Reply{}, err
	}
	s.notify("scans")
	return apikit.Reply{Status: http.StatusCreated, Data: WireBlocked(b)}, nil
}

func (s *Server) unblockCompany(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	if name == "" {
		return apikit.Reply{}, apikit.InvalidField("name", "name of the company to unblock is required")
	}
	if err := d.repo.UnblockCompany(c.R.Context(), name); err != nil {
		if errors.Is(err, discovery.ErrNotFound) {
			return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "that company is not blocked")
		}
		return apikit.Reply{}, err
	}
	s.notify("scans")
	out, err := s.blockedList(c)
	return apikit.Reply{Data: out}, err
}

// inboxGate is the gate the inbox's Evaluate applies, built as the profile
// stands at each call, for the Ask Agent's list_pipeline to mark the rows it
// turns away.
func (s *Server) inboxGate(repo *discovery.Repo) func(context.Context) (discovery.Gate, error) {
	return func(context.Context) (discovery.Gate, error) {
		profile, e := loadProfile(s.cfg.Workspace)
		if e != nil {
			return nil, e
		}
		return evaluate.ScannerGate(profile, evaluate.BlockedFromDiscovery(repo)), nil
	}
}
