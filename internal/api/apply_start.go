package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"applysmith/internal/agent"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/applyapi"
	"applysmith/internal/api/trackerapi"
	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/discovery"
	"applysmith/internal/gates"
	"applysmith/internal/notify"
	"applysmith/internal/workspace"
)

const (
	maxApplyURL      = 2048
	maxApplyName     = 120
	defaultBatchRows = 5
	// batchCandidates bounds the lane rows read before the selection leaves
	// duplicates and holds out.
	batchCandidates = 400
	// queueTouch is how often the entries of queued starts are marked alive,
	// well inside the board's stale sweep.
	queueTouch  = time.Minute
	notRunnable = "it is no longer in Apply now or Selective"
)

// startApplyRun starts one posting. An explicit single address is not held
// back by the skip list; the dialog states whether the employer is on it.
func (s *Server) startApplyRun(c *apikit.Call) (apikit.Reply, error) {
	caller, e := applyapi.StartCaller(c)
	if e != nil {
		return apikit.Reply{}, e
	}
	var req wire.ApplyRunRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	req.URL = strings.TrimSpace(req.URL)
	u, perr := url.Parse(req.URL)
	switch {
	case len(req.URL) > maxApplyURL || perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil:
		return apikit.Reply{}, apikit.InvalidField("url", "url must be an http or https address of at most %d characters without credentials", maxApplyURL)
	case len([]rune(req.Company)) > maxApplyName:
		return apikit.Reply{}, apikit.InvalidField("company", "company has at most %d characters", maxApplyName)
	case len([]rune(req.Role)) > maxApplyName:
		return apikit.Reply{}, apikit.InvalidField("role", "role has at most %d characters", maxApplyName)
	}
	if e := applyapi.ConfirmSubmit(req.Submit, req.Confirmed); e != nil {
		return apikit.Reply{}, e
	}
	if req.Resubmit && !req.Submit {
		return apikit.Reply{}, apikit.InvalidField("resubmit", "a re-send is a submit and needs submit")
	}
	if req.Resubmit {
		// The route takes one posting, so the flag never stands beside a list.
		// A decision that cannot be recorded refuses the re-send.
		door := gates.ResubmitDoor(gates.ResubmitInput{Caller: caller, CLIFlag: true})
		if err := s.recordGate(c.R.Context(), c.P, door); err != nil {
			return apikit.Reply{}, err
		}
		if !door.Allowed() {
			return apikit.Reply{}, apikit.NewErr(wire.CodeForbidden, "%s", door.Reason)
		}
	}
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	l, err := d.apply.launch(c.R.Context(), agent.ApplyRequest{Tool: gates.ToolAutofillApplication, Rows: []agent.ApplyRow{{URL: req.URL, Company: req.Company, Role: req.Role, Resubmit: req.Resubmit}},
		Submit: req.Submit, ExplicitRequest: true, Caller: caller}, true)
	if err != nil {
		return apikit.Reply{}, applyapi.RefuseStart(err)
	}
	return apikit.Reply{Status: http.StatusAccepted, Location: "/api/runs/" + l.id, Data: wire.RunAccepted{RunID: l.id, Ahead: apikit.Safe(l.ahead)}}, nil
}

// startApplyBatch runs the rows of the chosen lanes, or the rows the user
// chose, one after another. A row of an employer on the batch skip list is left
// to the user whatever the lane. While the apply window is held the batch waits
// for it.
func (s *Server) startApplyBatch(c *apikit.Call) (apikit.Reply, error) {
	caller, e := applyapi.StartCaller(c)
	if e != nil {
		return apikit.Reply{}, e
	}
	var req wire.ApplyBatchRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	lanes := req.Lanes
	if len(lanes) == 0 {
		lanes = []string{wire.LaneApplyNow}
	}
	for _, l := range lanes {
		if l != wire.LaneApplyNow && l != wire.LaneSelective {
			return apikit.Reply{}, apikit.InvalidField("lanes", "only the %s and %s lanes can be run", wire.LaneApplyNow, wire.LaneSelective)
		}
	}
	limit := int(req.Limit)
	if limit == 0 {
		limit = defaultBatchRows
	}
	if limit < 1 || limit > applyapi.MaxBatchRows {
		return apikit.Reply{}, apikit.InvalidField("limit", "limit must be 1 to %d", applyapi.MaxBatchRows)
	}
	switch {
	case len(req.Candidacies) > applyapi.MaxBatchRows:
		return apikit.Reply{}, apikit.InvalidField("candidacies", "a batch names at most %d rows", applyapi.MaxBatchRows)
	case len(req.Candidacies) > 0 && len(req.Companies) > 0:
		return apikit.Reply{}, apikit.InvalidField("candidacies", "name rows or companies, not both")
	}
	if e := applyapi.ConfirmSubmit(req.Submit, req.Confirmed); e != nil {
		return apikit.Reply{}, e
	}
	rows, skipped, err := s.batchRows(c.R.Context(), req, lanes, limit)
	if err != nil {
		return apikit.Reply{}, err
	}
	if len(rows) == 0 {
		return apikit.Reply{}, apikit.WithReason(apikit.InvalidField("lanes", "no row in these lanes can be run"), "nothing-to-run")
	}
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	l, err := d.apply.launch(c.R.Context(), agent.ApplyRequest{Tool: gates.ToolApplyFromList, Rows: rows, Submit: req.Submit, ExplicitRequest: true, Caller: caller, Selected: true}, true)
	if err != nil {
		return apikit.Reply{}, applyapi.RefuseStart(err)
	}
	return apikit.Reply{Status: http.StatusAccepted, Location: "/api/runs/" + l.id,
		Data: wire.ApplyBatchAccepted{RunID: l.id, BatchID: l.batch, Rows: apikit.Safe(len(rows)), Skipped: skipped, Ahead: apikit.Safe(l.ahead)}}, nil
}

// batchRows reads the rows of the lanes in order, or the rows the user chose,
// then lets the selection layer leave out what a list must not run: a
// duplicate, a stop that still holds, the skip list. A row with no posting
// address, and one another start already holds, is reported and does not
// count against the limit. A request that names companies runs only their rows.
func (s *Server) batchRows(ctx context.Context, req wire.ApplyBatchRequest, lanes []string, limit int) ([]agent.ApplyRow, []wire.ApplySkippedRow, error) {
	skip, err := s.batchSkipEntries(ctx)
	if err != nil {
		return nil, nil, err
	}
	all, err := s.apply.ReadLanes(ctx)
	if err != nil {
		return nil, nil, trackerapi.TrackerError(err)
	}
	var chosen []applyapi.LaneRow
	skipped := []wire.ApplySkippedRow{}
	if len(req.Candidacies) > 0 {
		limit = len(req.Candidacies)
		byID := map[int64]applyapi.LaneRow{}
		for _, lane := range []string{wire.LaneApplyNow, wire.LaneSelective} {
			for _, r := range all[lane] {
				byID[r.Entry().ID] = r
			}
		}
		for _, id := range req.Candidacies {
			if r, ok := byID[int64(id)]; ok {
				chosen = append(chosen, r)
				continue
			}
			left := wire.ApplySkippedRow{Company: fmt.Sprintf("Row %d", id), Reason: notRunnable}
			if tr, err := s.tracker(); err == nil {
				if row, err := tr.Get(ctx, int64(id)); err == nil {
					left.Company, left.Role = row.Entry.Company, row.Entry.Role
				}
			}
			skipped = append(skipped, left)
		}
	} else {
		for _, lane := range lanes {
			chosen = append(chosen, all[lane]...)
		}
	}
	held := s.heldPostings(ctx)
	var cands []apply.Candidate
	seen := map[string]bool{}
	for _, r := range chosen {
		e := r.Entry()
		switch {
		case e.PostingURL == "":
			skipped = append(skipped, wire.ApplySkippedRow{Company: e.Company, Role: e.Role, Reason: applyapi.NoAddress})
		case held[e.PostingURL]:
			skipped = append(skipped, wire.ApplySkippedRow{Company: e.Company, Role: e.Role, Reason: "another start already has it queued or running"})
		case !seen[e.PostingURL] && len(cands) < batchCandidates:
			seen[e.PostingURL] = true
			cands = append(cands, apply.Candidate{URL: e.PostingURL, Company: e.Company, Role: e.Role, TrackerID: e.ID, ReportNumber: e.ReportNumber})
		}
	}
	sel, err := s.selector().Select(ctx, cands, apply.SelectOptions{Submit: req.Submit, RetryToday: req.RetryToday, Named: len(req.Companies) > 0,
		NamedCompanies: req.Companies, Now: s.now(), Build: s.cfg.BuildID, SkipList: skip, Limit: limit})
	if errors.Is(err, apply.ErrEvidenceUnreadable) {
		return nil, nil, apikit.NewErr(wire.CodeUnavailable, "the duplicate evidence cannot be read, so a list that submits does not start")
	}
	if err != nil {
		return nil, nil, err
	}
	rows := make([]agent.ApplyRow, len(sel.Rows))
	for i, r := range sel.Rows {
		rows[i] = agent.ApplyRow{URL: r.URL, EmployerURL: r.EmployerURL, Company: r.Company, Role: r.Role, Named: r.Named, Mirrors: r.Mirrors}
	}
	for _, k := range sel.Skipped {
		skipped = append(skipped, wire.ApplySkippedRow{Company: k.Company, Role: k.Role, Reason: k.Reason})
	}
	return slices.Clip(rows), skipped, nil
}

// heldPostings are the posting addresses a queued start or an open batch
// holds; a list that cannot be read holds none.
func (s *Server) heldPostings(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	if d, err := s.dom(); err == nil && d.apply != nil {
		for _, u := range d.apply.queuedPostings() {
			out[u] = true
		}
	}
	if st, err := s.db(); err == nil {
		_ = applyapi.EachRow(ctx, st.Read(), `SELECT r.url FROM apply_batch_rows r JOIN apply_batches b ON b.batch_id = r.batch_id
			WHERE b.closed_at IS NULL AND r.state IN ('pending', 'started')`, func(r *sql.Rows) error {
			var u string
			err := r.Scan(&u)
			out[u] = true
			return err
		})
	}
	return out
}

// listEvidence is the duplicate and stop evidence of this workspace. The
// user's answer bank, profile and CV are the data a stop on missing data waits
// for.
func (s *Server) listEvidence() apply.SQLEvidence {
	ws := s.cfg.Workspace
	return apply.SQLEvidence{Store: s.store.Load(),
		DataSavedAt: apply.NewestModTime(ws.Path(workspace.FileAnswerBank), ws.Path(workspace.FileProfile), ws.Path(workspace.FileCV))}
}

// selector is the list selection over this workspace's evidence. Mirror rows
// are resolved ahead of the walk and the selected rows are probed for liveness
// through the apply window, unless a run holds that window.
func (s *Server) selector() *apply.Selector {
	sel := apply.NewSelector(s.listEvidence())
	d, err := s.dom()
	if err != nil {
		return sel
	}
	mirrors := s.mirrorSources(d)
	sel.Resolve = func(ctx context.Context, c apply.Candidate) (apply.Resolution, error) {
		r := mirrors.Resolve(ctx, c.URL, apply.MirrorHint{Company: c.Company, Role: c.Role})
		return apply.Resolution{URL: r.URL, Reason: r.Reason}, nil
	}
	sel.Record = apply.StorePrecheck{Store: s.store.Load()}
	if s.cfg.ApplyWindow != nil && d.apply != nil && !d.apply.driving() {
		sel.Probe = apply.StoredProber{Store: storedLiveness{d.repo}, Next: windowProber{open: s.cfg.ApplyWindow}, Now: s.now}
	}
	return sel
}

// storedLiveness reads and keeps the liveness verdicts the discovery checks
// store, so a posting seen today is not loaded again.
type storedLiveness struct{ repo *discovery.Repo }

func (l storedLiveness) Get(ctx context.Context, key string) (apply.StoredVerdict, bool, error) {
	rec, ok, err := l.repo.LivenessGet(ctx, key)
	return apply.StoredVerdict{Verdict: rec.Verdict, Reason: rec.Reason, CheckedAt: rec.CheckedAt}, ok, err
}

func (l storedLiveness) Save(ctx context.Context, key, rawURL string, v apply.StoredVerdict) error {
	return l.repo.LivenessSave(ctx, key, rawURL, discovery.Liveness{Verdict: v.Verdict, Reason: v.Reason}, discovery.LivenessMethod)
}

// windowProber probes through the apply window, attaching it when the first
// page needs it. A window that cannot be reached leaves every page unknown,
// which the chain diagnoses when the row runs.
type windowProber struct {
	open func(context.Context) (apply.Window, error)
}

// probeAttachWait bounds how long a list waits for the browser before it
// starts: a probe is an optimisation, and a browser that is slow to attach is
// the chain's to diagnose.
const probeAttachWait = 8 * time.Second

// attachWithin opens the window, giving up after d without waiting for an
// opener that ignores its context.
func attachWithin(ctx context.Context, open func(context.Context) (apply.Window, error), d time.Duration) (apply.Window, error) {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	type result struct {
		w   apply.Window
		err error
	}
	done := make(chan result, 1)
	go func() {
		w, err := open(ctx)
		done <- result{w, err}
	}()
	select {
	case r := <-done:
		return r.w, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p windowProber) Probe(ctx context.Context, targets []apply.ProbeTarget) ([]apply.ProbeVerdict, error) {
	win, err := attachWithin(ctx, p.open, probeAttachWait)
	if err != nil || win == nil {
		out := make([]apply.ProbeVerdict, len(targets))
		for i := range out {
			out[i] = apply.ProbeVerdict{Result: apply.ProbeUnknown, Reason: "the apply window is not available"}
		}
		return out, nil
	}
	return apply.LivenessProber{Window: win}.Probe(ctx, targets)
}

// windowHold says whether a run holds the apply window. Letting go of it
// starts the next queued start.
type windowHold struct {
	held  atomic.Bool
	freed func()
}

func (w *windowHold) Load() bool { return w.held.Load() }

func (w *windowHold) CompareAndSwap(old, next bool) bool { return w.held.CompareAndSwap(old, next) }

func (w *windowHold) Store(v bool) {
	w.held.Store(v)
	if !v && w.freed != nil {
		w.freed()
	}
}

// queuedStart is a confirmed start from the dashboard or the command line that
// waits for the apply window. Its first run takes id, the id the start
// answered with.
type queuedStart struct {
	id, batch string
	req       agent.ApplyRequest
	rows      []apply.ListRow
}

// expanded is the destination of a shortened link (lnkd.in, bit.ly), so a run
// starts on, and is known by, the posting the link stands for. Any other link,
// or one that does not expand, is kept.
func (a *applyStack) expanded(ctx context.Context, raw string) string {
	if discovery.LinkKind(raw) != discovery.LinkShortener {
		return raw
	}
	final, _ := (&discovery.Expander{HTTP: a.s.cfg.Domain.ScanHTTP}).Expand(ctx, raw)
	return final
}

// launched names a start: its first run's id, its batch, and how many starts
// go before it when it waits (the one holding the window included).
type launched struct {
	id, batch string
	ahead     int
}

// launch begins the rows in the background and returns once the first run has
// an id. While a run or a fill holds the apply window, or starts already wait
// for it, a start that may wait is queued and answered at once; any other is
// refused with applyapi.ErrLaneBusy.
func (a *applyStack) launch(ctx context.Context, req agent.ApplyRequest, wait bool) (launched, error) {
	rows := make([]apply.ListRow, len(req.Rows))
	for i, r := range req.Rows {
		rows[i] = apply.ListRow{URL: a.expanded(ctx, r.URL), EmployerURL: r.EmployerURL, Company: r.Company, Role: r.Role, Named: r.Named, Mirrors: r.Mirrors, Resubmit: r.Resubmit}
	}
	var l launched
	if req.Tool == gates.ToolApplyFromList {
		l.batch = "list_" + a.s.now().UTC().Format("20060102T150405.000")
	}
	first := make(chan startResult, 1)
	a.mu.Lock()
	switch {
	case a.closed:
		a.mu.Unlock()
		return l, errors.New("the server is stopping")
	case len(a.queue) > 0 || a.fills.any() || a.lentAway("") || !a.busy.CompareAndSwap(false, true):
		defer a.mu.Unlock()
		if !wait {
			return l, applyapi.ErrLaneBusy
		}
		q := &queuedStart{id: a.runner.NewRunID(), req: req, rows: rows}
		if l.batch != "" {
			// Named after its run, so two starts queued at once never share a batch.
			q.batch = "list_" + strings.TrimPrefix(q.id, "run_")
		}
		a.queue = append(a.queue, q)
		l.id, l.batch, l.ahead = q.id, q.batch, len(a.queue)
		a.openQueued(ctx, q, l.ahead)
		if !a.touching {
			a.touching = true
			a.wg.Go(a.touchQueue)
		}
		return l, nil
	}
	a.wg.Go(func() {
		defer a.busy.Store(false)
		a.runList(a.s.bg, req, rows, l.batch, first, "")
	})
	a.mu.Unlock()
	select {
	case r := <-first:
		l.id = r.id
		return l, r.err
	case <-ctx.Done():
		return l, ctx.Err()
	case <-time.After(startWait):
		return l, errors.New("the run did not start in time")
	}
}

// next starts the oldest queued start once the apply window is free.
func (a *applyStack) next() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || len(a.queue) == 0 || a.fills.any() || a.lentAway("") || !a.busy.CompareAndSwap(false, true) {
		return
	}
	q := a.queue[0]
	a.queue = a.queue[1:]
	a.renumber(0)
	a.wg.Go(func() {
		defer a.busy.Store(false)
		first := make(chan startResult, 1)
		a.runList(a.s.bg, q.req, q.rows, q.batch, first, q.id)
		// A start whose rows started no run ends its entry with why.
		if r := <-first; r.err != nil {
			if _, err := a.d.notify.board.Move(context.WithoutCancel(a.s.bg), q.id, notify.Done, notify.MoveArgs{Outcome: notify.OutcomeFailed, Detail: r.err.Error()}); err != nil {
				a.s.log.Warn("a queued start that never ran could not be ended on the board", "run", q.id, "error", err.Error())
			}
		}
	})
}

// dequeue takes a queued start off the queue and ends its entry as cancelled;
// false when no queued start has the id.
func (a *applyStack) dequeue(ctx context.Context, id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	i := slices.IndexFunc(a.queue, func(q *queuedStart) bool { return q.id == id })
	if i < 0 {
		return false
	}
	a.queue = slices.Delete(a.queue, i, i+1)
	a.renumber(i)
	if _, err := a.d.notify.board.Move(context.WithoutCancel(ctx), id, notify.Done, notify.MoveArgs{Outcome: notify.OutcomeCancelled, Detail: "Cancelled before it started."}); err != nil {
		a.s.log.Warn("a cancelled queued start could not be ended on the board", "run", id, "error", err.Error())
	}
	return true
}

// queuedPostings are the posting addresses of the queued starts.
func (a *applyStack) queuedPostings() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, q := range a.queue {
		for _, r := range q.rows {
			out = append(out, r.URL)
		}
	}
	return out
}

func (a *applyStack) queuedPosting(raw string) bool { return slices.Contains(a.queuedPostings(), raw) }

// openQueued puts a queued start on the board as working at stage queued. The
// caller holds a.mu, so the start cannot begin before its entry exists.
func (a *applyStack) openQueued(ctx context.Context, q *queuedStart, ahead int) {
	title := applyTitle(a.named(ctx, q.rows[0]))
	if len(q.rows) > 1 {
		title = fmt.Sprintf("Apply to %d postings", len(q.rows))
	}
	_, err := a.d.notify.board.Open(context.WithoutCancel(ctx), notify.OpenArgs{ID: q.id, Kind: notify.KindApply, Origin: notify.OriginUser, Title: title,
		Stage: wire.BoardStageQueued, Detail: queuedDetail(ahead), BatchID: q.batch, Link: "/runs/" + q.id, Ref: "apply:" + q.id})
	if err != nil {
		a.s.log.Warn("a queued start could not be put on the board", "run", q.id, "error", err.Error())
	}
}

// renumber tells the queued starts from position i on how many are now ahead.
// The caller holds a.mu.
func (a *applyStack) renumber(i int) {
	for j, q := range a.queue[i:] {
		detail := queuedDetail(i + j + 1)
		if _, err := a.d.notify.board.Update(context.WithoutCancel(a.s.bg), q.id, notify.Patch{Detail: &detail}); err != nil {
			a.s.log.Warn("a queued start's place could not be updated on the board", "run", q.id, "error", err.Error())
		}
	}
}

func queuedDetail(ahead int) string {
	return fmt.Sprintf("Queued (%d ahead): it starts when the apply window is free.", ahead)
}

// touchQueue keeps the entries of queued starts alive on the board while any
// wait, and ends with the queue or the server.
func (a *applyStack) touchQueue() {
	t := time.NewTicker(queueTouch)
	defer t.Stop()
	for {
		select {
		case <-a.s.bg.Done():
			return
		case <-t.C:
		}
		a.mu.Lock()
		ids := make([]string, len(a.queue))
		for i, q := range a.queue {
			ids[i] = q.id
		}
		a.touching = len(ids) > 0
		a.mu.Unlock()
		if len(ids) == 0 {
			return
		}
		for _, id := range ids {
			if err := a.d.notify.board.Touch(context.WithoutCancel(a.s.bg), id); err != nil {
				a.s.log.Warn("a queued start could not be kept on the board", "run", id, "error", err.Error())
			}
		}
	}
}
