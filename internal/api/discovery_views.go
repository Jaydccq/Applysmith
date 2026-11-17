package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/schedulesapi"
	"applysmith/internal/api/wire"
	"applysmith/internal/browser"
	"applysmith/internal/discovery"
	"applysmith/internal/scheduler"
	"applysmith/internal/signals"
	"applysmith/internal/store"
	"applysmith/internal/workspace"
)

// scanSources answers the Run tab: the catalog with readiness, a running scan,
// a cooldown and the last run of each source. A key-gated source needs its key
// in the vault; a session-gated one is shown as needing a sign-in only after
// its last run stopped at the sign-in wall, because no other record says so.
func (s *Server) scanSources(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	profile, e := loadProfile(s.cfg.Workspace)
	if e != nil {
		return apikit.Reply{}, e
	}
	ctx := c.R.Context()
	has := s.cfg.Vault.Has
	var sites []browser.SessionSite
	if s.cfg.SessionBrowser != nil {
		sites, _ = s.cfg.SessionBrowser.Sites(ctx) // an unreadable list reads as unchecked
	}
	chromeErr := error(nil)
	if s.cfg.ChromeCheck != nil {
		chromeErr = s.cfg.ChromeCheck(ctx)
	}
	// A portals.yml that cannot be read says nothing here; starting the scan
	// reports it.
	portals, portalsErr := loadPortals(s.cfg.Workspace)
	readable := portalsErr == nil
	registry, registryWhy, err := s.registryReadiness(ctx, d, portals, readable)
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.ScanSourceList{Sources: make([]wire.ScanSource, 0, len(discovery.Sources))}
	for _, src := range discovery.Sources {
		item := wire.ScanSource{Name: src.Name, Title: src.Title, Kind: src.Kind, Needs: src.Needs, KeyName: src.KeyName}
		var last *discovery.Record
		recent, err := d.repo.ScanList(ctx, src.Name, 1)
		if err != nil {
			return apikit.Reply{}, err
		}
		if len(recent) == 1 {
			last = &recent[0]
			rec := WireScanRecord(recent[0])
			item.LastScan = &rec
			item.Running = recent[0].Status == discovery.RunRunning
		}
		item.Readiness, item.Reason = s.sourceReadiness(src, has, chromeErr, sites, last)
		if src.Name == "registry" && registry != "" {
			item.Readiness, item.Reason = registry, registryWhy
		}
		if r, why := discovery.SearchReadiness(src, portals); readable && item.Readiness == "ready" && r != "" {
			item.Readiness, item.Reason = r, why
		}
		if until, cooling := d.repo.CooldownUntil(ctx, src.Name, discovery.CooldownHours(profile, src.Name), ""); cooling {
			item.CooldownUntil = apikit.TimePtr(until)
		}
		out.Sources = append(out.Sources, item)
	}
	return apikit.Reply{Data: out}, nil
}

// registryReadiness is the registry source's own state when a registry scan
// would read nothing and end skipped, with its reason, or "" when it has
// something to read. Switched off in portals.yml it is off, boards or none;
// switched on with no active board and no tracked company it needs boards
// (registry-empty).
func (s *Server) registryReadiness(ctx context.Context, d *domain, portals workspace.Portals, readable bool) (readiness, reason string, err error) {
	switch {
	case !readable:
		return "", "", nil
	case !portals.Registry.IsEnabled():
		return "off", "The registry scan is switched off in portals.yml (registry: enabled: false), so it reads nothing.", nil
	case slices.ContainsFunc(portals.Companies, func(c workspace.Company) bool { return c.IsEnabled() }):
		return "", "", nil
	}
	stats, err := d.repo.RegistryStats(ctx)
	if err != nil || stats[discovery.StatusActive] > 0 {
		return "", "", err
	}
	return "needs boards", "The registry is empty: download the lists of company boards for it to read.", nil
}

// sourceFunnel reads what each source produced over the last days: postings
// fetched, new, evaluated, strong, applied, answered and interviewed.
func (s *Server) sourceFunnel(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	q := apikit.QueryOf(c)
	days := q.Integer("days", 30)
	if q.Err == nil && (days < 1 || days > 365) {
		q.Fail(apikit.InvalidField("days", "days must be between 1 and 365"))
	}
	if q.Err != nil {
		return apikit.Reply{}, q.Err
	}
	now := s.now()
	rows, err := d.repo.SourceFunnel(c.R.Context(), now.AddDate(0, 0, -days), now.Add(time.Minute))
	if err != nil {
		return apikit.Reply{}, err
	}
	out := wire.SourceFunnel{Days: days, Lanes: make([]wire.FunnelLane, 0, len(rows)), Total: wire.FunnelLane{Lane: "total"}}
	for _, r := range rows {
		l := wire.FunnelLane{Lane: r.Lane, Discovered: r.Discovered, Inserted: r.Inserted, Evaluated: r.Evaluated, Failed: r.Failed, GateRejected: r.GateRejected,
			High: r.High, Applied: r.Applied, Responded: r.Responded, Interview: r.Interview}
		out.Lanes = append(out.Lanes, l)
		out.Total.Discovered += l.Discovered
		out.Total.Inserted += l.Inserted
		out.Total.Evaluated += l.Evaluated
		out.Total.Failed += l.Failed
		out.Total.GateRejected += l.GateRejected
		out.Total.High += l.High
		out.Total.Applied += l.Applied
		out.Total.Responded += l.Responded
		out.Total.Interview += l.Interview
	}
	return apikit.Reply{Data: out}, nil
}

// funnelWeek compares the last 7 days with the 7 before.
func (s *Server) funnelWeek(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	// Whole local days, today and the six before, so the tracker can list
	// what a stage counts (reached since that day).
	now := s.now()
	since := dayOf(now).start.AddDate(0, 0, -6)
	cur, err := d.repo.StageCounts(c.R.Context(), since, now.Add(time.Minute))
	if err != nil {
		return apikit.Reply{}, err
	}
	prev, err := d.repo.StageCounts(c.R.Context(), since.AddDate(0, 0, -7), since)
	if err != nil {
		return apikit.Reply{}, err
	}
	return apikit.Reply{Data: wire.FunnelWeek{Since: since.Format(time.DateOnly), Current: wire.FunnelStages(cur), Previous: wire.FunnelStages(prev)}}, nil
}

// previewSchedule lists the next fire times of a recurrence the user has not
// saved, so the editor can show them before the save button is enabled.
func (s *Server) previewSchedule(c *apikit.Call) (apikit.Reply, error) {
	var req wire.SchedulePreviewRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	now := s.now()
	times, err := scheduler.PreviewFires(schedulesapi.FromPreset(req.Preset), now, now, scheduler.UpcomingCount)
	if err != nil {
		return apikit.Reply{}, apikit.InvalidField("preset", "%s", err.Error())
	}
	out := wire.SchedulePreview{Next: make([]wire.Time, len(times))}
	for i, t := range times {
		out.Next[i] = wire.Time{Time: t.UTC()}
	}
	return apikit.Reply{Data: out}, nil
}

// leadRunClaim is the claim a pass over the leads store holds, so a second
// pass of the same kind is refused while one runs.
func leadRunClaim(op string) string { return "leads-" + op }

// startPass admits one pass and runs it in the background.
func (s *Server) startPass(ctx context.Context, p apikit.Pass) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	st, err := s.db()
	if err != nil {
		return apikit.Reply{}, err
	}
	claimed := false
	ref, err := st.CreateRun(ctx, store.RunRequest{Principal: p.Principal, Operation: p.Operation, Key: p.Key, Kind: p.Kind, Input: p.Input,
		Admit: func(context.Context) error {
			if !claimed && !d.scans.claim(p.Claim) {
				return errPassActive
			}
			claimed = true
			return nil
		}})
	switch {
	case errors.Is(err, errPassActive):
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "%s", p.Busy)
	case errors.Is(err, store.ErrConflict):
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "this Idempotency-Key was used with a different request")
	case err != nil:
		if claimed {
			d.scans.release(p.Claim)
		}
		return apikit.Reply{}, err
	}
	if !ref.Replayed {
		if p.Begin != nil {
			p.Begin(ref.RunID)
		}
		if err := d.scans.start(s.bg, ref.RunID, p.Claim, func(ctx context.Context) { s.runPass(ctx, ref.RunID, p) }); err != nil {
			d.scans.release(p.Claim)
			s.endRun(ref.RunID, wire.RunFailed, "the server is shutting down")
			return apikit.Reply{}, err
		}
	}
	return apikit.Reply{Status: http.StatusAccepted, Location: "/api/runs/" + ref.RunID, Data: wire.RunAccepted{RunID: ref.RunID}}, nil
}

// startLeadRun admits one pass over the leads store (expand or evaluate).
func (s *Server) startLeadRun(op string, run func(context.Context, *domain, []int64) (string, error)) apikit.Handler {
	return func(c *apikit.Call) (apikit.Reply, error) {
		key, e := apikit.IdempotencyKey(c)
		if e != nil {
			return apikit.Reply{}, e
		}
		var req wire.LeadRunRequest
		if len(c.Body) > 0 {
			if e := apikit.Decode(c.Body, &req); e != nil {
				return apikit.Reply{}, e
			}
		}
		ids := make([]int64, len(req.IDs))
		for i, id := range req.IDs {
			if id < 1 {
				return apikit.Reply{}, apikit.InvalidField("ids", "ids must be positive whole numbers")
			}
			ids[i] = int64(id)
		}
		slices.Sort(ids)
		d, err := s.dom()
		if err != nil {
			return apikit.Reply{}, err
		}
		body, _ := json.Marshal(ids)
		return s.startPass(c.R.Context(), apikit.Pass{Operation: "leads." + op, Kind: "leads", Stage: "leads", Claim: leadRunClaim(op),
			Busy: fmt.Sprintf("a %s pass over the leads is already running", op), Key: key, Principal: c.P.ID, Input: body,
			Run: func(ctx context.Context, _ func(string)) (string, error) { return run(ctx, d, ids) }})
	}
}

// signalTickClaim is the source key under which a tick is the one active run.
const signalTickClaim = "signals-tick"

func (s *Server) createSignalTick(c *apikit.Call) (apikit.Reply, error) {
	key, e := apikit.IdempotencyKey(c)
	if e != nil {
		return apikit.Reply{}, e
	}
	var req wire.SignalTickRequest
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	if _, err := signals.ParseSources(strings.Join(req.Sources, ",")); err != nil {
		return apikit.Reply{}, apikit.InvalidField("sources", "%s", err.Error())
	}
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	st, err := s.db()
	if err != nil {
		return apikit.Reply{}, err
	}
	body := []byte(fmt.Sprintf("%q", strings.Join(req.Sources, ",")))
	claimed := false
	ref, err := st.CreateRun(c.R.Context(), store.RunRequest{Principal: c.P.ID, Operation: "signals.tick", Key: key, Kind: "signals", Input: body,
		Admit: func(ctx context.Context) error {
			if active, err := d.signals.ActiveTick(ctx); err != nil {
				return err
			} else if active != 0 {
				return &signals.ActiveError{TickID: active}
			}
			if !claimed && !d.scans.claim(signalTickClaim) {
				return &signals.ActiveError{}
			}
			claimed = true
			return nil
		}})
	var active *signals.ActiveError
	switch {
	case errors.As(err, &active):
		if active.TickID == 0 {
			return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "a tick is already starting")
		}
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "%s", active.Error())
	case errors.Is(err, store.ErrConflict):
		return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "this Idempotency-Key was used with a different request")
	case err != nil:
		if claimed {
			d.scans.release(signalTickClaim)
		}
		return apikit.Reply{}, err
	}
	if !ref.Replayed {
		if err := d.scans.start(s.bg, ref.RunID, signalTickClaim, func(ctx context.Context) { s.runSignalTick(ctx, d, ref.RunID, req.Sources) }); err != nil {
			d.scans.release(signalTickClaim)
			s.endRun(ref.RunID, wire.RunFailed, "the server is shutting down")
			return apikit.Reply{}, err
		}
	}
	return apikit.Reply{Status: http.StatusAccepted, Location: "/api/runs/" + ref.RunID, Data: wire.RunAccepted{RunID: ref.RunID}}, nil
}

// runSignalTick executes one tick and records how it ended on the run row and
// the run stream.
func (s *Server) runSignalTick(ctx context.Context, d *domain, runID string, sources []string) {
	st := s.store.Load()
	end := context.WithoutCancel(ctx)
	_ = st.SetRun(end, runID, wire.RunRunning, "tick", "")
	s.notify("runs")
	sum, err := d.signals.Tick(ctx, signals.TickRequest{Sources: sources, RunID: runID,
		Progress: func(line string) { s.runs.publish(runID, "progress", wire.RunProgress{RunID: runID, Line: line}) }})
	final := wire.RunFinal{RunID: runID, Status: wire.RunSucceeded, Outcome: sum.Line()}
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(err, errScanShutdown):
		final.Status, final.Reason = wire.RunCancelled, "cancelled"
	case err != nil:
		final.Status, final.Reason = wire.RunFailed, fmt.Sprintf("%.200s", err.Error())
	}
	failure := ""
	if final.Status == wire.RunFailed {
		failure = final.Reason
	}
	if err := st.SetRun(end, runID, final.Status, "", failure); err != nil {
		s.log.Error("recording the end of a tick failed", "run", runID, "error", err.Error())
	}
	s.runs.publish(runID, "final", final)
	s.runs.retire(runID)
	s.notify("runs")
}

var errPassActive = errors.New("a pass of this kind is running")

// runPass runs a pass and records how it ended on the run row and the stream.
// A cancelled pass stops between items: the passes check the context before
// each one.
func (s *Server) runPass(ctx context.Context, runID string, p apikit.Pass) {
	st := s.store.Load()
	end := context.WithoutCancel(ctx)
	_ = st.SetRun(end, runID, wire.RunRunning, p.Stage, "")
	s.notify("runs")
	summary, err := p.Run(ctx, func(line string) { s.runs.publish(runID, "progress", wire.RunProgress{RunID: runID, Line: line}) })
	final := wire.RunFinal{RunID: runID, Status: wire.RunSucceeded, Outcome: summary}
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(err, errScanShutdown):
		final.Status, final.Reason = wire.RunCancelled, "cancelled"
	case err != nil:
		final.Status, final.Reason = wire.RunFailed, fmt.Sprintf("%.200s", err.Error())
	}
	failure := ""
	if final.Status == wire.RunFailed {
		failure = final.Reason
	}
	// The outcome is written before the end, so the board's sync of the end
	// already reads it.
	if final.Status == wire.RunSucceeded && summary != "" {
		if err := st.SetRunOutcome(end, runID, summary); err != nil {
			s.log.Error("recording the outcome of a background pass failed", "run", runID, "kind", p.Kind, "error", err.Error())
		}
	}
	if err := st.SetRun(end, runID, final.Status, "", failure); err != nil {
		s.log.Error("recording the end of a background pass failed", "run", runID, "kind", p.Kind, "error", err.Error())
	}
	s.runs.publish(runID, "final", final)
	s.runs.retire(runID)
	s.notify("runs", "scans")
}

// expandLeadsNow is the dashboard's expansion of careers pages; the listed
// leads are read again even when their page is in the ledger.
func (s *Server) expandLeadsNow(ctx context.Context, d *domain, ids []int64) (string, error) {
	portals, profile, err := s.leadSettings()
	if err != nil {
		return "", err
	}
	res, err := s.newScanner(d, profile, nil).ExpandLeads(ctx, discovery.ExpandRequest{Portals: portals, Profile: profile, Force: ids})
	return res.Summary(), err
}

// evaluateLeadsNow sends the eligible leads, or only the listed ones, to
// evaluation.
func (s *Server) evaluateLeadsNow(ctx context.Context, d *domain, ids []int64) (string, error) {
	portals, profile, err := s.leadSettings()
	if err != nil {
		return "", err
	}
	res, err := s.newScanner(d, profile, nil).EvaluateLeads(ctx, discovery.EvaluateRequest{Portals: portals, Profile: profile, IDs: ids})
	return res.Summary(), err
}

func (s *Server) leadSettings() (workspace.Portals, workspace.Profile, error) {
	portals, e := loadPortals(s.cfg.Workspace)
	if e != nil {
		return portals, workspace.Profile{}, e
	}
	profile, e := loadProfile(s.cfg.Workspace)
	if e != nil {
		return portals, profile, e
	}
	return portals, profile, nil
}

// sourceReadiness says whether a source can run now and, when it cannot or
// should not yet, why. A browser source needs the managed browser and Google
// Chrome; one that needs a site session also needs the sign-in check not to
// have found the site signed out, or the last run not to have stopped at the
// sign-in page.
func (s *Server) sourceReadiness(src discovery.SourceInfo, has func(string) bool, chromeErr error, sites []browser.SessionSite, last *discovery.Record) (readiness, reason string) {
	if r := src.Readiness(has); r != "ready" {
		return r, "Add its API key in Settings."
	}
	if src.Kind != discovery.KindBrowser {
		return "ready", ""
	}
	if s.cfg.SessionBrowser == nil {
		return "needs browser", "No managed browser is set up on this server."
	}
	if chromeErr != nil {
		return "needs browser", chromeErr.Error()
	}
	if src.Needs != "session" {
		return "ready", ""
	}
	state, checked := "", time.Time{}
	for _, site := range sites {
		if site.Domain == src.Host || strings.HasSuffix(site.Domain, "."+src.Host) {
			state = site.SignIn
			if site.SignInCheckedAt != nil {
				checked = site.SignInCheckedAt.Time
			}
			break
		}
	}
	atWall := last != nil && last.Status == discovery.RunNeedsSignIn && !last.Started.Before(checked)
	on := ""
	if !checked.IsZero() {
		on = " on " + checked.Format("2006-01-02")
	}
	switch {
	case atWall:
		return "needs sign-in", "The last run stopped at the sign-in page."
	case state == "signed_out":
		return "needs sign-in", "The sign-in check" + on + " found you signed out."
	case state == "checkpoint":
		return "needs sign-in", "The sign-in check" + on + " found a security checkpoint."
	case state == "unknown" || state == "":
		return "sign-in unchecked", "Sign-in has not been checked yet."
	}
	return "ready", ""
}
