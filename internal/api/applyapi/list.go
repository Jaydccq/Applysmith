package applyapi

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"net/url"
	"slices"
	"strings"

	"applysmith/internal/agent"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/discovery"
	"applysmith/internal/documents"
	"applysmith/internal/gmail"
	"applysmith/internal/gmail/classify"
	"applysmith/internal/identity"
	"applysmith/internal/ledger"
	"applysmith/internal/tracker"
)

// Score thresholds of the lanes: apply now from 4.0, selective from 3.5.
const (
	ApplyNowFloor  = 4.0
	SelectiveFloor = 3.5
	// maxLaneRows bounds the tracker rows of one status the lanes are decided
	// over.
	maxLaneRows = 2000
	NoAddress   = "the row has no posting address"
	// MaxBatchRows bounds the rows of one list, and of the lanes the Ask Agent
	// reads.
	MaxBatchRows = 100
)

var applyLanes = []string{wire.LaneApplyNow, wire.LaneSelective, wire.LaneMarkedApplied, wire.LaneAwaiting}

// LaneRow is a tracker row in its Apply Next lane, with the skip-list entry
// that covers its company, why a list started now would leave it out (""
// when it would run the row) and its source check (wire.ApplyItem.SourceCheck).
type LaneRow struct {
	e       tracker.Entry
	skip    string
	leftOut string
	source  string
}

// Entry is the row's tracker entry.
func (r LaneRow) Entry() tracker.Entry { return r.e }

// laneFacts are what places a row besides the row itself, read once.
type laneFacts struct {
	skip    []string
	aliases map[int64][]string
	// waiting are the apply chain's submissions that wait for a confirmation
	// or a receipt, by identity key.
	waiting map[string]ledger.Waiting
	// liveness is a posting's stored liveness verdict (active, expired, or
	// mismatch: the page shows another posting), by identity key.
	liveness map[string]string
	// doubtful are the reports whose posting legitimacy is doubtful.
	doubtful map[int64]bool
	// verification is each report's verification line ("<method> <result>
	// <date>").
	verification map[int64]string
	// receipts are the tracker rows a Gmail receipt was linked to.
	receipts map[int64]bool
}

func (f laneFacts) keys(e tracker.Entry) []string { return rowKeys(e, f.aliases[e.ID]) }

// rowKeys are a row's identity keys: its own, its aliases and its address's.
func rowKeys(e tracker.Entry, aliases []string) []string {
	out := append([]string{e.IdentityKey}, aliases...)
	if k, err := identity.URLKey(e.PostingURL); err == nil && k != "" {
		out = append(out, k)
	}
	return out
}

func (f laneFacts) waits(e tracker.Entry) bool {
	return slices.ContainsFunc(f.keys(e), func(k string) bool { _, ok := f.waiting[k]; return ok })
}

// livenessOf is the row's stored verdict, expired over mismatch over active.
func (f laneFacts) livenessOf(e tracker.Entry) string {
	verdict := ""
	for _, k := range f.keys(e) {
		switch v := f.liveness[k]; v {
		case discovery.LiveExpired:
			return v
		case discovery.LiveMismatch:
			verdict = v
		case discovery.LiveActive:
			if verdict == "" {
				verdict = v
			}
		}
	}
	return verdict
}

// sourceCheck reads the posting's last check: a stored liveness verdict, else
// the verification line of its report. A liveness check that found another
// posting, or a report whose check failed, is failed; a report that could not
// see the posting live (pasted text, a page read that found no posting, an
// old report's unknown) is unknown; a row with neither says nothing.
func (f laneFacts) sourceCheck(e tracker.Entry, live string) string {
	switch live {
	case discovery.LiveMismatch:
		return wire.SourceCheckFailed
	case discovery.LiveActive:
		return ""
	}
	if e.ReportNumber == nil {
		return ""
	}
	line, ok := f.verification[*e.ReportNumber]
	if !ok {
		return ""
	}
	// The line is "<method> <result> <date>".
	switch fields := strings.Fields(line); {
	case len(fields) > 1 && (fields[1] == "live" || fields[1] == "pass"):
		return ""
	case len(fields) > 1 && fields[1] == "fail":
		return wire.SourceCheckFailed
	}
	return wire.SourceCheckUnknown
}

func (s *Handlers) readLaneFacts(ctx context.Context) (laneFacts, error) {
	f := laneFacts{aliases: map[int64][]string{}, liveness: map[string]string{}, doubtful: map[int64]bool{}, verification: map[int64]string{}, receipts: map[int64]bool{}}
	// A profile that cannot be read shows no badge; a batch refuses to start on it.
	f.skip, _ = s.SkipEntries()
	lg, err := s.Ledger()
	if err != nil {
		return f, err
	}
	if f.waiting, err = lg.Waiting(ctx); err != nil {
		return f, err
	}
	st, err := s.DB()
	if err != nil {
		return f, err
	}
	db := st.Read()
	return f, errors.Join(
		EachRow(ctx, db, `SELECT entry_id, identity_key FROM tracker_aliases`, func(r *sql.Rows) error {
			var id int64
			var key string
			err := r.Scan(&id, &key)
			f.aliases[id] = append(f.aliases[id], key)
			return err
		}),
		EachRow(ctx, db, `SELECT identity_key, verdict FROM liveness_checks WHERE verdict IN (?, ?, ?)`, func(r *sql.Rows) error {
			var key, verdict string
			err := r.Scan(&key, &verdict)
			f.liveness[key] = verdict
			return err
		}, discovery.LiveExpired, discovery.LiveMismatch, discovery.LiveActive),
		EachRow(ctx, db, `SELECT report_number, legitimacy = 'doubtful', verification FROM report_index`, func(r *sql.Rows) error {
			var n int64
			var doubtful bool
			var line string
			err := r.Scan(&n, &doubtful, &line)
			f.doubtful[n], f.verification[n] = doubtful, line
			return err
		}),
		EachRow(ctx, db, `SELECT DISTINCT candidacy_id FROM gmail_outcomes WHERE signal_type = ? AND result IN (?, ?)`, func(r *sql.Rows) error {
			var id int64
			err := r.Scan(&id)
			f.receipts[id] = true
			return err
		}, classify.TypeApplied, gmail.OutcomeChanged, gmail.OutcomeNoChange),
	)
}

// EachRow hands every row of a query to scan.
func EachRow(ctx context.Context, db *sql.DB, q string, scan func(*sql.Rows) error, args ...any) error {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// allEntries reads every row a query matches, up to maxLaneRows.
func allEntries(ctx context.Context, tr *tracker.Tracker, q tracker.ListQuery) ([]tracker.Entry, error) {
	var out []tracker.Entry
	q.PageSize = 100
	for q.Page = 1; len(out) < maxLaneRows; q.Page++ {
		res, err := tr.List(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, res.Entries...)
		if q.Page*q.PageSize >= res.Total {
			break
		}
	}
	return out, nil
}

// ReadLanes places the tracker rows in the four Apply Next lanes, the way the
// apply-list status route specifies: Apply now and Selective best score first,
// Marked applied and Awaiting receipt newest change first, a row in one lane at
// most. A posting known to be closed (an expired liveness verdict or a closed
// posting fact) and a posting with duplicate evidence are in neither lane to
// apply in; a skip-listed company, a liveness mismatch and a doubtful posting
// move a row that scores 4.0 into Selective. Each row of those two lanes is
// checked by the list selection, so it says what a list would leave out.
//
// With lanes named, only those are placed and returned, and what only the
// others need is not read: the applied rows are for Marked applied and
// Awaiting receipt, the list selection for Apply now and Selective. With none
// named, all four are.
func (s *Handlers) ReadLanes(ctx context.Context, lanes ...string) (map[string][]LaneRow, error) {
	want := func(names ...string) bool {
		return len(lanes) == 0 || slices.ContainsFunc(names, func(l string) bool { return slices.Contains(lanes, l) })
	}
	toApply, sent := want(wire.LaneApplyNow, wire.LaneSelective), want(wire.LaneMarkedApplied, wire.LaneAwaiting)
	tr, err := s.Tracker()
	if err != nil {
		return nil, err
	}
	f, err := s.readLaneFacts(ctx)
	if err != nil {
		return nil, err
	}
	var evaluated, applied []tracker.Entry
	if toApply || want(wire.LaneAwaiting) {
		if evaluated, err = allEntries(ctx, tr, tracker.ListQuery{Status: tracker.Evaluated, Sort: tracker.SortScoreDesc}); err != nil {
			return nil, err
		}
	}
	if sent {
		if applied, err = allEntries(ctx, tr, tracker.ListQuery{Status: tracker.Applied, Sort: tracker.SortUpdatedDesc}); err != nil {
			return nil, err
		}
	}
	out := map[string][]LaneRow{}
	var open []tracker.Entry
	var cands []apply.Candidate
	for _, e := range evaluated {
		switch {
		case f.waits(e):
			out[wire.LaneAwaiting] = append(out[wire.LaneAwaiting], LaneRow{e: e})
		case !toApply, e.Score == nil || *e.Score < SelectiveFloor, *e.Score >= ApplyNowFloor && e.ScoreKind != tracker.ScoreFinal:
			// Not asked for, below the lanes, or a quick screen, which never
			// places a row in Apply now.
		default:
			open = append(open, e)
			if e.PostingURL != "" {
				cands = append(cands, apply.Candidate{URL: e.PostingURL, Company: e.Company, Role: e.Role, TrackerID: e.ID, ReportNumber: e.ReportNumber})
			}
		}
	}
	var checks []*apply.Skipped
	if toApply {
		if checks, err = apply.NewSelector(s.Evidence()).Check(ctx, cands, apply.SelectOptions{Now: s.Now(), Build: s.Build, SkipList: f.skip}); err != nil {
			return nil, err
		}
	}
	for _, e := range open {
		row := LaneRow{e: e, skip: skipCover(f.skip, e.Company), leftOut: NoAddress}
		if e.PostingURL != "" {
			sk := checks[0]
			checks = checks[1:]
			row.leftOut = ""
			if sk != nil && (sk.Closed || sk.Rule == apply.RuleApplied || sk.Rule == apply.RuleCompanyRole) {
				continue
			}
			if sk != nil {
				row.leftOut = sk.Reason
			}
		}
		live := f.livenessOf(e)
		if live == discovery.LiveExpired {
			continue
		}
		row.source = f.sourceCheck(e, live)
		review := live == discovery.LiveMismatch || (e.ReportNumber != nil && f.doubtful[*e.ReportNumber])
		lane := wire.LaneSelective
		if *e.Score >= ApplyNowFloor && !review && row.skip == "" {
			lane = wire.LaneApplyNow
		}
		out[lane] = append(out[lane], row)
	}
	for _, e := range applied {
		switch {
		case f.receipts[e.ID]:
		case e.AppliedBy == tracker.ByUser:
			out[wire.LaneMarkedApplied] = append(out[wire.LaneMarkedApplied], LaneRow{e: e})
		case e.AppliedBy == tracker.ByAgent && f.waits(e):
			out[wire.LaneAwaiting] = append(out[wire.LaneAwaiting], LaneRow{e: e})
		}
	}
	slices.SortStableFunc(out[wire.LaneAwaiting], func(a, b LaneRow) int {
		return cmp.Or(strings.Compare(b.e.UpdatedAt, a.e.UpdatedAt), cmp.Compare(b.e.ID, a.e.ID))
	})
	for lane := range out {
		if !want(lane) {
			delete(out, lane)
		}
	}
	return out, nil
}

// AgentApplyLanes reads today's apply list for the agent from the same lanes
// the Apply Next page shows.
func (s *Handlers) AgentApplyLanes() agent.ApplyLanes {
	return func(ctx context.Context) ([]agent.ApplyLane, error) {
		all, err := s.ReadLanes(ctx, wire.LaneApplyNow, wire.LaneSelective)
		if err != nil {
			return nil, err
		}
		var out []agent.ApplyLane
		for _, lane := range []string{wire.LaneApplyNow, wire.LaneSelective} {
			rows := all[lane]
			l := agent.ApplyLane{Lane: lane, Total: len(rows)}
			for _, r := range rows[:min(len(rows), MaxBatchRows)] {
				l.Items = append(l.Items, agent.ApplyItem{Company: r.e.Company, Role: r.e.Role, URL: r.e.PostingURL, Score: r.e.Score})
			}
			out = append(out, l)
		}
		return out, nil
	}
}

func postingHost(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	}
	return ""
}

// belowFloor reads the document floor of a final report from the report index,
// as the document service does: whether it waits below 4.5 with no override. A
// screening or gated report, which no override covers, and a report it cannot
// read are not.
func (s *Handlers) belowFloor(ctx context.Context, report int64) bool {
	st, err := s.DB()
	if err != nil {
		return false
	}
	var kind string
	var eligible bool
	err = st.Read().QueryRowContext(ctx, `SELECT kind, documents_eligible FROM report_index WHERE report_number = ?`, report).Scan(&kind, &eligible)
	return err == nil && kind == apply.ReportFinal && !eligible
}

// letterFloor reports whether the document settings write a cover letter a
// form asks for only above the score floor, so a row below it offers the
// user's override. A setting that cannot be read keeps the floor, as the
// documents service does.
func (s *Handlers) letterFloor(ctx context.Context) bool {
	docs, err := s.Docs()
	if err != nil || docs == nil {
		return true
	}
	p, err := docs.Preferences(ctx)
	return err != nil || p.CoverWhenAsked != documents.CoverAlways
}

func (s *Handlers) List(c *apikit.Call) (apikit.Reply, error) {
	q := apikit.QueryOf(c)
	page, size := q.Paging(10)
	lanes := applyLanes
	if raw := c.R.URL.Query()["lane"]; len(raw) > 0 {
		lanes = nil
		for _, l := range raw {
			for _, one := range strings.Split(l, ",") {
				one = strings.TrimSpace(one)
				if !slices.Contains(applyLanes, one) {
					return apikit.Reply{}, apikit.InvalidField("lane", "lane must be one of %s", strings.Join(applyLanes, ", "))
				}
				if !slices.Contains(lanes, one) {
					lanes = append(lanes, one)
				}
			}
		}
	}
	if q.Err != nil {
		return apikit.Reply{}, q.Err
	}
	all, err := s.ReadLanes(c.R.Context(), lanes...)
	if err != nil {
		return apikit.Reply{}, s.TrackerError(err)
	}
	out := wire.ApplyList{Lanes: []wire.ApplyLane{}, Page: apikit.Safe(page), PageSize: apikit.Safe(size),
		Counts: wire.ApplyCounts{States: map[string]wire.SafeInt{}, Lanes: map[string]wire.SafeInt{}}}
	floor := s.letterFloor(c.R.Context())
	for _, lane := range lanes {
		rows := all[lane]
		item := wire.ApplyLane{Lane: lane, Items: []wire.ApplyItem{}, Total: apikit.Safe(len(rows))}
		from := min((page-1)*size, len(rows))
		for _, r := range rows[from:min(from+size, len(rows))] {
			e := r.e
			row := wire.ApplyItem{ID: apikit.Safe64(e.ID), Company: e.Company, Role: e.Role, Score: e.Score, Lane: lane, Date: e.Date, Version: apikit.Safe(e.Version),
				PostingURL: e.PostingURL, PostingHost: postingHost(e.PostingURL), SkipEntry: r.skip, LeftOut: r.leftOut, SourceCheck: r.source}
			if e.ReportNumber != nil {
				n := apikit.Safe64(*e.ReportNumber)
				row.ReportNumber = &n
				row.LetterOffer = floor && s.belowFloor(c.R.Context(), *e.ReportNumber)
			}
			s.decorateApplyItem(c.R.Context(), &row)
			out.Counts.States[row.State]++
			item.Items = append(item.Items, row)
		}
		out.Counts.Lanes[lane] = item.Total
		out.Lanes = append(out.Lanes, item)
	}
	return apikit.Reply{Data: out}, nil
}

// decorateApplyItem adds the newest attempt for the posting, found by its
// identity key, and the state the row is in: waiting in a batch or behind the
// apply window, working, where its last run left it, or skipped when a list
// would leave it out with no run of its own to say why. A run's stop is read
// for its owner alone; the item carries no form value.
func (s *Handlers) decorateApplyItem(ctx context.Context, it *wire.ApplyItem) {
	it.State = wire.ApplyStateIdle
	if it.SkipEntry != "" {
		it.State = wire.ApplyStateNeedsUser
	}
	if it.PostingURL == "" {
		return
	}
	st, err := s.DB()
	if err != nil {
		return
	}
	key, stp := apply.IdentityKey(it.PostingURL)
	if stp != nil {
		return
	}
	var queued, started int
	_ = st.Read().QueryRowContext(ctx, `SELECT COALESCE(sum(r.state = 'pending'), 0), COALESCE(sum(r.state = 'started'), 0)
		FROM apply_batch_rows r JOIN apply_batches b ON b.batch_id = r.batch_id WHERE b.closed_at IS NULL AND r.url = ?`, it.PostingURL).Scan(&queued, &started)
	if s.QueuedPosting(it.PostingURL) {
		queued++
	}
	run, err := scanRun(st.Read().QueryRowContext(ctx, `SELECT `+runColumns+` FROM apply_runs WHERE identity_key = ? ORDER BY started_at DESC LIMIT 1`, key))
	if err == nil {
		it.RunID = run.id
		out := run.wireOutcome()
		if run.kind == "running" {
			out = wire.ApplyOutcome{Kind: run.kind}
		}
		it.Attempt = &wire.ApplyAttempt{RunID: run.id, At: apikit.Stamp(run.started), Mode: modeOf(run.submit), Outcome: out}
		it.KeptTab = s.KeptTab(run.id)
		it.FormWaiting = it.KeptTab && run.kind == string(apply.OutcomeFilled) && s.FormWaits(ctx, run.id)
		switch {
		case run.kind == "submitted":
			it.State = wire.ApplyStateSubmitted
		case it.FormWaiting:
			it.State = wire.ApplyStateNeedsUser
		case run.kind == "stopped" && out.Owner == wire.OwnerUser:
			it.State = wire.ApplyStateNeedsUser
		case run.kind == "stopped":
			it.State = wire.ApplyStateFailed
		}
	}
	// A row the user marked applied is theirs: a run that stopped before
	// leaves it neither failed nor waiting on them.
	if it.Lane == wire.LaneMarkedApplied && (it.State == wire.ApplyStateFailed || it.State == wire.ApplyStateNeedsUser) {
		it.State = wire.ApplyStateIdle
	}
	switch {
	case started > 0 || (err == nil && run.kind == "running"):
		it.State = wire.ApplyStateRunning
	case queued > 0:
		it.State = wire.ApplyStateQueued
	case it.State == wire.ApplyStateIdle && it.LeftOut != "":
		it.State = wire.ApplyStateSkipped
	}
}

// ConfirmSubmitted is the user's statement that an application in Awaiting
// receipt went through (owner decision F-5): a row not yet applied enters
// applied as the agent's, and the apply chain's waiting submission is settled
// as confirmed by the user, never as confirmed by the page.
func (s *Handlers) ConfirmSubmitted(c *apikit.Call) (apikit.Reply, error) {
	actor, e := s.Actor(c.P)
	if e != nil {
		return apikit.Reply{}, e
	}
	id, e := apikit.PathID(c, "id")
	if e != nil {
		return apikit.Reply{}, e
	}
	tr, err := s.Tracker()
	if err != nil {
		return apikit.Reply{}, err
	}
	lg, err := s.Ledger()
	if err != nil {
		return apikit.Reply{}, err
	}
	ctx := c.R.Context()
	row, err := tr.Get(ctx, id)
	if err != nil {
		return apikit.Reply{}, s.TrackerError(err)
	}
	w, ok, err := lg.WaitingFor(ctx, rowKeys(row.Entry, row.Aliases))
	if err != nil {
		return apikit.Reply{}, err
	}
	if !ok {
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "no submission Applysmith made of this posting waits for a confirmation")
	}
	entry := row.Entry
	if !entry.Status.Submitted() {
		res, err := tr.SetStatus(ctx, tracker.StatusChange{ID: id, To: tracker.Applied, Actor: actor, AppliedBy: tracker.ByAgent})
		if err != nil {
			return apikit.Reply{}, s.TrackerError(err)
		}
		entry = res.Entry
	}
	if err := lg.SettleWaiting(ctx, w.ID, ledger.ConfirmUser, ""); err != nil && !errors.Is(err, ledger.ErrNotWaiting) {
		return apikit.Reply{}, err
	}
	s.Notify("tracker")
	return apikit.Reply{Data: s.WireCandidacy(entry)}, nil
}
