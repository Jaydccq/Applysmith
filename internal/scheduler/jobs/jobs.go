// Package jobs registers the scheduler's job kinds. Each kind is a thin
// adapter over a service that exists elsewhere (the scanner, the Gmail
// syncer, the model router, the knowledge base); no scan, sync or evaluation
// logic lives here. A kind whose service belongs to a later task is not
// registered, and Pending names the task.
//
// Nothing here can submit an application, fill a form or press a control in
// the apply window: the package imports none of those entry points, and the
// scheduler's dependency test pins that.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"applysmith/internal/agent/memory"
	"applysmith/internal/discovery"
	"applysmith/internal/gmail"
	"applysmith/internal/kb"
	"applysmith/internal/llm"
	"applysmith/internal/scheduler"
	"applysmith/internal/signals"
	"applysmith/internal/workspace"
)

// ScanFunc runs one scan with the server's scanner, so a scheduled scan and a
// scan started from the dashboard are the same code.
type ScanFunc func(ctx context.Context, req discovery.ScanRequest, progress func(string)) (discovery.Record, error)

// Deps are the services the kinds call.
type Deps struct {
	Workspace *workspace.Workspace
	Scan      ScanFunc
	Gmail     *gmail.Stack
	Router    *llm.Router
	KB        *kb.Service
	// Memory is the agent's nightly consolidation; nil leaves its kind out.
	Memory *memory.Consolidator
	// Improve runs one Ask Agent self-improvement round; nil leaves
	// agent.improve out.
	Improve func(ctx context.Context) (string, error)
	// Registry re-runs the registry seed lists and probes the dead boards that
	// are due; nil leaves registry.refresh out.
	Registry func(ctx context.Context) (string, error)
	// Liveness audits old inbox rows and unapplied tracker rows; nil leaves
	// liveness.audit out.
	Liveness func(ctx context.Context) (string, error)
	// Signals runs one hiring-signal tick over the named collectors (all when
	// empty) and returns its summary line; nil leaves signals.collect out.
	Signals func(ctx context.Context, sources []string, progress func(string)) (string, error)
	// LeadsExpand turns careers-page leads into postings; LeadsEvaluate sends
	// stored leads to evaluation. nil leaves the kind out.
	LeadsExpand, LeadsEvaluate func(ctx context.Context) (string, error)
	// InboxEvaluate sends up to limit waiting reviews and inbox rows to
	// evaluation, the reviews first, as the work of the run runID; nil leaves
	// inbox.evaluate out.
	InboxEvaluate func(ctx context.Context, limit int, runID string) (string, error)
	// InboxWaiting reports whether reviews or inbox rows wait that
	// inbox.evaluate would take; with it, a scan that succeeds is followed by
	// inbox.evaluate.
	InboxWaiting func(ctx context.Context) (bool, error)
	// Archive runs the default tracker archive rules as the system actor and,
	// unless apply is true, only lists the rows it would archive; nil leaves
	// archive.sweep out.
	Archive func(ctx context.Context, apply bool) (string, error)
}

// Pending lists the kinds of the registry that are not registered yet, each
// with the task that provides its service. Every kind of the spec's registry
// has its service now.
func Pending() map[string]string {
	return map[string]string{}
}

// Register adds every kind whose service exists to the registry. Discovery
// sources are registered from the catalog, so a source added to it gets its
// kind without any other registration.
func Register(reg *scheduler.Registry, d Deps) error {
	var kinds []scheduler.Kind
	if d.Scan != nil && d.Workspace != nil {
		for _, src := range discovery.Sources {
			kinds = append(kinds, discoverKind(d, src))
		}
	}
	if d.Gmail != nil {
		kinds = append(kinds, gmailSync(d))
	}
	if d.Router != nil {
		kinds = append(kinds, costReport(d), priceCheck(d))
	}
	if d.KB != nil {
		kinds = append(kinds, knowledgeIngest(d))
	}
	if d.Memory != nil {
		kinds = append(kinds, memoryConsolidate(d))
	}
	if d.Improve != nil {
		kinds = append(kinds, agentImprove(d))
	}
	if d.Registry != nil {
		kinds = append(kinds, registryRefresh(d))
	}
	if d.Liveness != nil {
		kinds = append(kinds, livenessAudit(d))
	}
	if d.Signals != nil {
		kinds = append(kinds, signalsCollect(d))
	}
	if d.LeadsExpand != nil {
		kinds = append(kinds, leadsExpand(d))
	}
	if d.LeadsEvaluate != nil {
		kinds = append(kinds, leadsEvaluate(d))
	}
	if d.InboxEvaluate != nil {
		kinds = append(kinds, inboxEvaluate(d))
	}
	if d.Archive != nil {
		kinds = append(kinds, archiveSweep(d))
	}
	for _, k := range kinds {
		if err := reg.Register(k); err != nil {
			return err
		}
	}
	return nil
}

func discoverKind(d Deps, src discovery.SourceInfo) scheduler.Kind {
	browser := src.Kind == discovery.KindBrowser
	k := scheduler.Kind{
		Name:     "discover." + src.Name,
		Doc:      "Scans " + src.Title + " for postings and queues them for evaluation.",
		Leases:   []scheduler.Lease{scheduler.LeaseEvaluation},
		Drives:   scheduler.Drives{Evaluation: true},
		Network:  true,
		Artifact: "/discovery",
		Fields: []scheduler.Field{
			{Name: "scope", Type: scheduler.FieldString, Doc: "location scope: us, canada, uk, europe, asia-pacific or worldwide"},
			{Name: "sort", Type: scheduler.FieldString, Enum: []string{discovery.SortScore, discovery.SortRecency}, Doc: "which new postings go to evaluation first: the best score (the default) or the newest"},
			{Name: "mode", Type: scheduler.FieldString, Enum: []string{"auto", "screen", "deep"}, Doc: "how they are evaluated: auto, screen (a quick screen) or deep (in depth)"},
			{Name: "topN", Type: scheduler.FieldInt, Min: 0, Max: 500, Doc: "how many new postings go to evaluation; empty uses your scan default, 0 sends none"},
			{Name: "budgetSeconds", Type: scheduler.FieldInt, Min: 0, Max: int((6 * time.Hour).Seconds()), Doc: "how long the scan may run, in seconds; empty allows 30 minutes"},
			{Name: "retryKnown", Type: scheduler.FieldBool, Doc: "also take postings that only an earlier scan saw (never ones already tracked, reported or archived)"},
			{Name: "search", Type: scheduler.FieldString, Doc: "an ad hoc search text; it replaces the saved searches"},
			{Name: "title", Type: scheduler.FieldString, Doc: "an ad hoc title; it replaces the saved searches"},
			{Name: "city", Type: scheduler.FieldString, Doc: "an ad hoc city; it replaces the saved searches"},
			{Name: "state", Type: scheduler.FieldString, Doc: "an ad hoc state; it replaces the saved searches"},
			{Name: "limit", Type: scheduler.FieldInt, Min: 1, Max: 1000, Doc: "results per saved search"},
			{Name: "postedWithin", Type: scheduler.FieldString, Doc: "freshness window such as 6h, 3d or 2w"},
		},
		Check: func(in map[string]any) error {
			if w, ok := in["postedWithin"].(string); ok && w != "" {
				if _, err := discovery.ParseWindow(w); err != nil {
					return err
				}
			}
			if scope, ok := in["scope"].(string); ok && scope != "" {
				return discovery.ValidateScope(scope)
			}
			return nil
		},
	}
	if browser {
		k.Leases = append(k.Leases, scheduler.LeaseBrowser)
		k.Drives.ScanWindow = true
	}
	// A scan evaluates only its own top N. A scan schedule the user made has
	// no inbox evaluation after it, as the packs have, so the rows it leaves
	// waiting are drained by one right after it (F-1).
	if d.InboxWaiting != nil {
		k.Then = &scheduler.FollowUp{Kind: "inbox.evaluate", Due: d.InboxWaiting}
	}
	k.Run = func(ctx context.Context, in scheduler.Invocation) (string, error) {
		return runScan(ctx, d, src.Name, in)
	}
	return k
}

// ScreenWarning warns about a scan schedule whose evaluations stop at the
// quick screen: a screening score never places a row in Apply now.
func ScreenWarning(kind string, inputs map[string]any) string {
	if strings.HasPrefix(kind, "discover.") && inputs["mode"] == "screen" {
		return "mode screen stops every evaluation at the quick screen, and a screened row never reaches Apply now; use auto or deep to get rows to apply to"
	}
	return ""
}

func runScan(ctx context.Context, d Deps, source string, in scheduler.Invocation) (string, error) {
	portals, err := loadPortals(d.Workspace)
	if err != nil {
		return "", err
	}
	profile, err := loadProfile(d.Workspace)
	if err != nil {
		return "", err
	}
	req := discovery.ScanRequest{Source: source, Portals: portals, Profile: profile, RunID: in.RunID}
	req.Scope, _ = in.Inputs["scope"].(string)
	req.Sort, _ = in.Inputs["sort"].(string)
	req.Mode, _ = in.Inputs["mode"].(string)
	req.RetryKnown, _ = in.Inputs["retryKnown"].(bool)
	if n, ok := intInput(in.Inputs["topN"]); ok {
		req.TopN = &n
	}
	if n, ok := intInput(in.Inputs["budgetSeconds"]); ok {
		req.Budget = time.Duration(n) * time.Second
	}
	req.Search, _ = in.Inputs["search"].(string)
	req.Title, _ = in.Inputs["title"].(string)
	req.City, _ = in.Inputs["city"].(string)
	req.State, _ = in.Inputs["state"].(string)
	if n, ok := intInput(in.Inputs["limit"]); ok {
		req.Limit = n
	}
	if w, _ := in.Inputs["postedWithin"].(string); w != "" {
		d, err := discovery.ParseWindow(w)
		if err != nil {
			return "", err
		}
		req.Window = d
	}
	rec, err := d.Scan(ctx, req, in.Progress)
	if err != nil {
		return "", err
	}
	c := rec.Counts
	summary := fmt.Sprintf("%d discovered, %d new, %d queued for evaluation", c.Discovered, c.Inserted, c.Queued)
	switch rec.Status {
	case discovery.RunSkipped:
		if rec.Reason == discovery.ReasonNoKey {
			return summary, scheduler.NeedsYou(ReasonNeedsKey, fmt.Errorf("the %s scan has no key: add it in Settings", source))
		}
		return summary, nil
	case discovery.RunCompleted:
		switch {
		case rec.SpendCapped():
			summary += "; spend cap reached, the searches left run first next time"
		case rec.Reason == discovery.ReasonBudget:
			summary += "; scan budget reached"
		}
		return summary, nil
	case discovery.RunNeedsSignIn:
		return summary, scheduler.NeedsYou(scheduler.ReasonNeedsSignIn, fmt.Errorf("the %s scan needs you to sign in: %s", source, firstNonEmpty(rec.Reason, rec.Error)))
	case discovery.RunCancelled:
		return summary, context.Canceled
	case discovery.RunFailed:
		if rec.Retryable {
			return summary, scheduler.Transient(fmt.Errorf("the %s scan failed: %s", source, firstNonEmpty(rec.Reason, rec.Error, strings.Join(rec.Errors, "; "))))
		}
	case discovery.RunBlocked:
		return summary, scheduler.NeedsYou(rec.Reason, fmt.Errorf("the %s scan was blocked: %s", source, firstNonEmpty(rec.Reason, rec.Error)))
	}
	return summary, fmt.Errorf("the %s scan %s: %s", source, rec.Status, firstNonEmpty(rec.Reason, rec.Error, strings.Join(rec.Errors, "; ")))
}

// ReasonNeedsKey marks a scheduled scan skipped for want of its key. The
// notification layer raises one item per source per day for it.
const ReasonNeedsKey = "needs-key"

// ReasonNeedsGmailSignIn marks a Gmail sync that could not sign in: no app
// password is stored or Gmail refuses it. The notification layer raises the
// one Gmail sign-in item for it.
const ReasonNeedsGmailSignIn = "needs-gmail-sign-in"

// gmailSyncSummary is what a Gmail sync that ran returns.
func gmailSyncSummary(c gmail.Counts) string {
	return fmt.Sprintf("%d listed, %d stored, %d for review, %d linked", c.Listed, c.Stored, c.Review, c.Linked)
}

// The summaries of a Gmail sync that did nothing: it read no mail, or another
// sync was reading it.
var gmailSyncNone = gmailSyncSummary(gmail.Counts{})

const gmailSyncBusy = "a sync was already running"

// Idle reports whether a scheduled job that succeeded did nothing, from its
// kind and the summary it returned. The runs board acknowledges such a run as
// it ends: the ten-minute Gmail sync would otherwise fill the finished runs.
func Idle(kind, summary string) bool {
	return kind == "gmail.sync" && (summary == gmailSyncNone || summary == gmailSyncBusy)
}

func gmailSync(d Deps) scheduler.Kind {
	return scheduler.Kind{
		Name:     "gmail.sync",
		Doc:      "Reads new job mail, classifies it and links outcomes to the tracker.",
		Leases:   []scheduler.Lease{scheduler.LeaseGmail, scheduler.LeaseTracker},
		Drives:   scheduler.Drives{GmailRead: true, TrackerWrite: true},
		Network:  true,
		Artifact: "/gmail",
		Run: func(ctx context.Context, in scheduler.Invocation) (string, error) {
			rec, err := d.Gmail.Syncer.Run(ctx, gmail.TriggerScheduled)
			switch {
			case errors.Is(err, gmail.ErrAlreadyRunning):
				// The watcher's sync, or a manual one, reads the same mail.
				return gmailSyncBusy, nil
			case gmail.IsAuthFailure(err):
				return "", scheduler.NeedsYou(ReasonNeedsGmailSignIn, err)
			case err != nil:
				return "", err
			}
			summary := gmailSyncSummary(rec.Counts)
			if rec.Status == "failed" {
				// Run records staged failures and returns nil error. Preserve the
				// sign-in class for scheduler retries and needs-you notices.
				failure := fmt.Errorf("the sync failed at %s: %s", rec.FailedStage, rec.Error)
				if gmail.AuthFailureText(rec.Error) {
					return summary, scheduler.NeedsYou(ReasonNeedsGmailSignIn, failure)
				}
				return summary, failure
			}
			return summary, nil
		},
	}
}

// costReport computes the day's model spend. It is shown on the run; delivery
// to the user goes through the notification policy of task 5.4.
func costReport(d Deps) scheduler.Kind {
	return scheduler.Kind{
		Name:     "cost.report",
		Doc:      "Totals the last day's model calls and spend.",
		Artifact: "/usage",
		Run: func(ctx context.Context, in scheduler.Invocation) (string, error) {
			byTask, _, err := d.Router.CostReport(ctx, 1)
			if err != nil {
				return "", err
			}
			var calls, unpriced int
			var cost float64
			for _, r := range byTask {
				calls += r.Calls
				cost += r.CostUSD
				unpriced += r.Unpriced
			}
			return fmt.Sprintf("%d model calls, $%.2f in the last day, %d unpriced", calls, cost, unpriced), nil
		},
	}
}

func priceCheck(d Deps) scheduler.Kind {
	return scheduler.Kind{
		Name:     "price.check",
		Doc:      "Compares the embedded model prices with the live catalog.",
		Network:  true,
		Artifact: "/usage",
		Run: func(ctx context.Context, in scheduler.Invocation) (string, error) {
			drift, err := d.Router.CheckPriceDrift(ctx)
			if err != nil {
				return "", fmt.Errorf("the price catalog could not be read: %w", err)
			}
			return fmt.Sprintf("%d prices differ from the live catalog", len(drift)), nil
		},
	}
}

func knowledgeIngest(d Deps) scheduler.Kind {
	return scheduler.Kind{
		Name:     "knowledge.ingest",
		Doc:      "Re-reads the connected repositories into knowledge drafts. It never publishes.",
		Leases:   []scheduler.Lease{scheduler.LeaseEvaluation},
		Drives:   scheduler.Drives{Evaluation: true},
		Network:  true,
		Artifact: "/wiki",
		Run: func(ctx context.Context, in scheduler.Invocation) (string, error) {
			repos, err := d.KB.ListRepos(ctx)
			if err != nil {
				return "", err
			}
			var drafts int
			var errs []error
			for _, r := range repos {
				rep, err := d.KB.IngestRepo(ctx, r.ID, false)
				if err != nil {
					errs = append(errs, fmt.Errorf("repository %d: %w", r.ID, err))
					continue
				}
				drafts += len(rep.Drafts)
			}
			return fmt.Sprintf("%d repositories, %d drafts", len(repos), drafts), errors.Join(errs...)
		},
	}
}

func memoryConsolidate(d Deps) scheduler.Kind {
	return scheduler.Kind{
		Name:     "memory.consolidate",
		Doc:      "Reads the conversations that ended since the last pass and proposes facts and rules for the user to review. It never creates a trusted fact or an active rule.",
		Leases:   []scheduler.Lease{scheduler.LeaseEvaluation},
		Drives:   scheduler.Drives{Evaluation: true},
		Network:  true,
		Artifact: "/ask",
		Run: func(ctx context.Context, in scheduler.Invocation) (string, error) {
			return d.Memory.Consolidate(ctx)
		},
	}
}

func agentImprove(d Deps) scheduler.Kind {
	return scheduler.Kind{
		Name:     "agent.improve",
		Doc:      "Replays recent Ask Agent turns that went wrong under candidate guidance, without side effects, and adopts a candidate that does better on them and no worse on a regression suite. It never changes a gate, a tool or the fixed prompt.",
		Leases:   []scheduler.Lease{scheduler.LeaseEvaluation},
		Drives:   scheduler.Drives{Evaluation: true},
		Network:  true,
		Artifact: "/settings",
		Run: func(ctx context.Context, in scheduler.Invocation) (string, error) {
			return d.Improve(ctx)
		},
	}
}

func registryRefresh(d Deps) scheduler.Kind {
	return scheduler.Kind{
		Name:     "registry.refresh",
		Doc:      "Re-reads the public slug lists, adding new boards only, and probes the dead boards that are due.",
		Network:  true,
		Artifact: "/discovery",
		Run: func(ctx context.Context, in scheduler.Invocation) (string, error) {
			return d.Registry(ctx)
		},
	}
}

func livenessAudit(d Deps) scheduler.Kind {
	return scheduler.Kind{
		Name:     "liveness.audit",
		Doc:      "Checks inbox rows older than a week and evaluated rows with no application, in the scan window.",
		Leases:   []scheduler.Lease{scheduler.LeaseBrowser, scheduler.LeaseTracker},
		Drives:   scheduler.Drives{ScanWindow: true, TrackerWrite: true},
		Network:  true,
		Artifact: "/discovery",
		Run: func(ctx context.Context, in scheduler.Invocation) (string, error) {
			return d.Liveness(ctx)
		},
	}
}

func leadsExpand(d Deps) scheduler.Kind {
	return scheduler.Kind{
		Name:     "leads.expand",
		Doc:      "Reads the careers pages of stored leads, at most 20 a run, and adds the postings they list to the inbox.",
		Network:  true,
		Artifact: "/discovery",
		Run: func(ctx context.Context, in scheduler.Invocation) (string, error) {
			summary, err := d.LeadsExpand(ctx)
			var temporary *discovery.TransientError
			var limited *discovery.RateLimitedError
			if errors.As(err, &temporary) || errors.As(err, &limited) {
				err = scheduler.Transient(err)
			}
			return summary, err
		},
	}
}

func leadsEvaluate(d Deps) scheduler.Kind {
	return scheduler.Kind{
		Name:     "leads.evaluate",
		Doc:      "Recovers posting leads from their boards and sends up to 25 leads a run to evaluation.",
		Leases:   []scheduler.Lease{scheduler.LeaseBrowser, scheduler.LeaseEvaluation},
		Drives:   scheduler.Drives{Evaluation: true, EvaluationReadsPages: true},
		Network:  true,
		Artifact: "/discovery",
		Run: func(ctx context.Context, in scheduler.Invocation) (string, error) {
			return d.LeadsEvaluate(ctx)
		},
	}
}

// inboxEvaluate drains the inbox: the quick-screen reviews waiting on the board
// go to evaluation in depth first (F-8), then the rows a scan left waiting
// outside its own top N, without a press. Like leads.evaluate it queues
// evaluations that may read a page in the scan window, so it holds both leases.
func inboxEvaluate(d Deps) scheduler.Kind {
	return scheduler.Kind{
		Name: "inbox.evaluate",
		Doc: fmt.Sprintf("Sends postings waiting in the inbox to evaluation: first the quick-screen reviews that wait for an evaluation in depth, then the best-ranked rows that pass the eligibility gate, up to %d a run (the limit input changes it). It sends none once the day's model spend cap or an evaluation task's budget is reached, and never fills, signs up or submits.",
			discovery.InboxEvaluateLimit),
		Leases:   []scheduler.Lease{scheduler.LeaseBrowser, scheduler.LeaseEvaluation},
		Drives:   scheduler.Drives{Evaluation: true, EvaluationReadsPages: true},
		Network:  true,
		Artifact: "/discovery",
		Fields:   []scheduler.Field{{Name: "limit", Type: scheduler.FieldInt, Min: 1, Max: 100, Doc: "rows sent to evaluation per run"}},
		Run: func(ctx context.Context, in scheduler.Invocation) (string, error) {
			limit := discovery.InboxEvaluateLimit
			if n, ok := intInput(in.Inputs["limit"]); ok {
				limit = n
			}
			return d.InboxEvaluate(ctx, limit, in.RunID)
		},
	}
}

// archiveSweep applies the tracker's default archive rules. It previews unless
// the schedule sets apply: archiving hides rows from every default list, so the
// first run of a new schedule must not do it unasked.
func archiveSweep(d Deps) scheduler.Kind {
	return scheduler.Kind{
		Name:     "archive.sweep",
		Doc:      "Lists the rejected and discarded tracker rows with no change for 30 days, and archives them when the schedule sets apply. Archived rows keep their ids, events and identity keys.",
		Leases:   []scheduler.Lease{scheduler.LeaseTracker},
		Drives:   scheduler.Drives{TrackerWrite: true},
		Artifact: "/tracker",
		Fields:   []scheduler.Field{{Name: "apply", Type: scheduler.FieldBool, Doc: "archive the rows instead of only listing them"}},
		Run: func(ctx context.Context, in scheduler.Invocation) (string, error) {
			apply, _ := in.Inputs["apply"].(bool)
			return d.Archive(ctx, apply)
		},
	}
}

func intInput(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), n == float64(int(n))
	}
	return 0, false
}

func firstNonEmpty(ss ...string) string {
	if i := slices.IndexFunc(ss, func(s string) bool { return s != "" }); i >= 0 {
		return ss[i]
	}
	return ""
}

func loadPortals(ws *workspace.Workspace) (workspace.Portals, error) {
	empty := workspace.Portals{Version: 1}
	c, err := ws.Read(workspace.FilePortals)
	if err != nil {
		return empty, err
	}
	if c.Revision == workspace.RevisionAbsent {
		return empty, nil
	}
	p, rep := workspace.ParsePortals(c.Data)
	if !rep.OK() {
		return empty, fmt.Errorf("portals.yml is invalid: %s", rep.String())
	}
	return p, nil
}

func loadProfile(ws *workspace.Workspace) (workspace.Profile, error) {
	c, err := ws.Read(workspace.FileProfile)
	if err != nil {
		return workspace.Profile{}, err
	}
	if c.Revision == workspace.RevisionAbsent {
		return workspace.Profile{}, nil
	}
	p, rep := workspace.ParseProfile(c.Data)
	if !rep.OK() {
		return p, fmt.Errorf("profile.yml is invalid: %s", rep.String())
	}
	return p, nil
}

func signalsCollect(d Deps) scheduler.Kind {
	return scheduler.Kind{
		Name:     "signals.collect",
		Doc:      "Runs one hiring-signal tick: collects public signals, assesses and tiers companies, checks the postings of the admitted ones and hands qualifying postings to the inbox. It never submits or fills an application.",
		Leases:   []scheduler.Lease{scheduler.LeaseEvaluation},
		Drives:   scheduler.Drives{Evaluation: true},
		Network:  true,
		Artifact: "/leads?tab=signals",
		Fields:   []scheduler.Field{{Name: "sources", Type: scheduler.FieldString, Doc: "comma-separated signal sources to collect from; a limited tick only collects"}},
		Check: func(in map[string]any) error {
			if s, ok := in["sources"].(string); ok {
				_, err := signals.ParseSources(s)
				return err
			}
			return nil
		},
		Run: func(ctx context.Context, in scheduler.Invocation) (string, error) {
			list, _ := in.Inputs["sources"].(string)
			sources, err := signals.ParseSources(list)
			if err != nil {
				return "", err
			}
			return d.Signals(ctx, sources, in.Progress)
		},
	}
}
