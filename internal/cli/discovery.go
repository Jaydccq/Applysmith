package cli

import (
	"cmp"
	"context"
	"encoding/csv"
	"encoding/json"

	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"applysmith/internal/answerbank"
	"applysmith/internal/api"
	"applysmith/internal/api/wire"
	"applysmith/internal/discovery"
	"applysmith/internal/evaluate"
	"applysmith/internal/launch"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

const scanUsage = `usage:
  applysmith scan sources [--json]        (or --list)
  applysmith scan runs [--source s] [--limit n] [--json]   (or --runs)
  applysmith scan <source> [--scope s] [--sort score|recency] [--top-n n] [--mode auto|screen|deep] [--retry-known] [--budget 30m] [--json]
      [--search text] [--title t] [--city c] [--state s] [--url u] [--limit n] [--posted-within 24h|any] [--fn f] [--seniority s] [--employment e] [--remote]
      [--pages n] [--ignore-cooldown] [--force] [--easy-apply-only]

a search text, title, city, state or url makes the run ad hoc: it replaces the saved searches with one. --limit caps each saved search and never makes a run ad hoc.
the browser sources (linkedin, linkedin-feed, indeed, ziprecruiter, google-jobs, glassdoor, newgrad, jobright) read pages in the scan window and click nothing that writes; --pages sets the result pages read, --ignore-cooldown runs a source that is cooling down after a blocked run, --force runs despite critical memory pressure.

every command takes --workspace <dir>; with the app or a server running for the workspace a scan runs in it, and sources and runs read beside it
`

const livenessUsage = `usage:
  applysmith liveness <url> [--company c] [--role r] [--json]

liveness reads the posting in the managed browser's scan window and says whether it is active, expired, uncertain or a mismatch. It types and clicks nothing.
`

const inboxUsage = `usage:
  applysmith inbox list [--status s] [--limit n] [--json]
  applysmith inbox show <id> [--json]
  applysmith inbox add <url> [--json]
  applysmith inbox dismiss <id> [--json]
  applysmith inbox promote <id> | --top N
  applysmith inbox export [--format csv|json] [--status s] [--out file]

every command takes --workspace <dir> and works beside a running server; promote asks it to run the evaluations
`

const blockedUsage = `usage:
  applysmith blocked add <name> [--reason text] [--json]
  applysmith blocked remove <name>
  applysmith blocked list [--json]

every command takes --workspace <dir> and works beside a running server
`

const registryUsage = `usage:
  applysmith registry stats [--json]
  applysmith registry seed [--source url]... [--licence l]
  applysmith registry refresh
  applysmith registry add <url>
  applysmith registry import <file>
  applysmith registry prune
  applysmith registry revive <ats:slug>
  applysmith registry block <ats:slug>
  applysmith registry probe

every command takes --workspace <dir>; stats, add, revive and block work beside a running server, the others need it stopped
`

// discoveryDeps is what a discovery command runs with.
type discoveryDeps struct {
	ws      *workspace.Workspace
	st      *store.Store
	repo    *discovery.Repo
	portals workspace.Portals
	profile workspace.Profile
}

// withDiscovery opens the workspace database under the workspace lock, as
// the tracker commands do, and loads portals.yml and profile.yml.
func (e *env) withDiscovery(ctx context.Context, cmd, wsFlag string, fn func(discoveryDeps) int) int {
	return e.withDiscoveryAccess(ctx, cmd, wsFlag, accessExclusive, fn)
}

// withDiscoveryAccess is withDiscovery for a command that needs only the
// given access to the database.
func (e *env) withDiscoveryAccess(ctx context.Context, cmd, wsFlag string, access storeAccess, fn func(discoveryDeps) int) int {
	root, err := e.workspacePath(wsFlag)
	if err != nil {
		return e.fail(cmd, err)
	}
	ws, st, release, err := e.openStore(ctx, root, access)
	if err != nil {
		return e.fail(cmd, err)
	}
	defer release()
	d := discoveryDeps{ws: ws, st: st, repo: discovery.NewRepo(st, nil)}
	if d.portals, err = loadPortals(ws); err != nil {
		return e.fail(cmd, err)
	}
	if d.profile, err = loadProfile(ws); err != nil {
		return e.fail(cmd, err)
	}
	d.repo.RankWith(func() (workspace.Profile, workspace.Portals, error) { return d.profile, d.portals, nil })
	return fn(d)
}

// discoveryAccess is how a discovery subcommand opens the database: the
// reads named read-only, the single-row writes (add, dismiss, remove, and a
// registry board's revive and block) as one more writer beside a running
// server, and the rest, which download, sweep or evaluate, under the exclusive
// lock.
func discoveryAccess(sub string, reads ...string) storeAccess {
	switch {
	case slices.Contains(reads, sub):
		return accessRead
	case sub == "add" || sub == "dismiss" || sub == "remove" || sub == "revive" || sub == "block":
		return accessShared
	}
	return accessExclusive
}

func loadPortals(ws *workspace.Workspace) (workspace.Portals, error) {
	c, err := ws.Read(workspace.FilePortals)
	if err != nil {
		return workspace.Portals{}, err
	}
	if c.Revision == workspace.RevisionAbsent {
		return workspace.Portals{Version: 1}, nil
	}
	p, rep := workspace.ParsePortals(c.Data)
	if !rep.OK() {
		return p, fmt.Errorf("portals.yml is invalid:\n%s", rep.String())
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
		return p, fmt.Errorf("profile.yml is invalid:\n%s", rep.String())
	}
	return p, nil
}

// gateOf is the real eligibility gate, built from the profile and the
// blocked-companies store.
func gateOf(d discoveryDeps) discovery.Gate {
	return evaluate.ScannerGate(d.profile, evaluate.BlockedFromDiscovery(d.repo))
}

func (e *env) scanner(d discoveryDeps, enq discovery.Enqueuer) *discovery.Scanner {
	client := discovery.NewClient()
	if e.scanHTTP != nil {
		client.HTTP = e.scanHTTP
	}
	var secret func(string) string
	if v, err := e.vaultOf(d.ws); err == nil {
		secret = api.VaultSecret(v)
	}
	win := e.scanWindow
	if win == nil {
		win = launch.ScanWindow(d.ws, func(ctx context.Context) bool { return answerbank.New(d.ws, d.st).Settings(ctx).WatchScans })
	}
	return &discovery.Scanner{
		Browser: &discovery.BrowserEnv{Window: win},
		Repo:    d.repo, Client: client, Endpoints: e.scanEndpoints, Enqueuer: enq, Gate: gateOf(d), Secret: secret, Leads: d.repo,
		Progress: func(line string) { fmt.Fprintln(e.stderr, line) },
	}
}

// ---- scan ----

func (e *env) scan(ctx context.Context, args []string) int {
	fs := e.flagsWithUsage("scan", scanUsage)
	ws := fs.String("workspace", "", "workspace directory")
	list := fs.Bool("list", false, "list the sources")
	runs := fs.Bool("runs", false, "list recent scan runs")
	source := fs.String("source", "", "with --runs: only this source")
	limit := fs.Int("limit", 0, "with --runs: how many (default 10); otherwise the results to take from each saved search")
	search := fs.String("search", "", "an ad hoc search text")
	title := fs.String("title", "", "an ad hoc title")
	city := fs.String("city", "", "an ad hoc city")
	state := fs.String("state", "", "an ad hoc state")
	listURL := fs.String("url", "", "an ad hoc list URL or path")
	postedWithin := fs.String("posted-within", "", "freshness window such as 6h, 3d or 2w")
	fn := fs.String("fn", "", "speedrun craft")
	seniority := fs.String("seniority", "", "speedrun seniority")
	employment := fs.String("employment", "", "speedrun employment type")
	remote := fs.Bool("remote", false, "remote jobs only (speedrun, linkedin)")
	pages := fs.Int("pages", 0, "result pages a browser source reads (default per source)")
	ignoreCooldown := fs.Bool("ignore-cooldown", false, "run a browser source that is cooling down after a blocked run")
	force := fs.Bool("force", false, "run a browser scan although memory pressure is critical")
	easyOnly := fs.Bool("easy-apply-only", false, "linkedin: keep only Easy Apply rows")
	scope := fs.String("scope", "", "location scope: "+strings.Join(discovery.Scopes, ", "))
	sortBy := fs.String("sort", "", "score (default) or recency")
	topN := fs.Int("top-n", -1, "rows to enqueue for evaluation (source/profile default; 0 disables posting evaluation)")
	mode := fs.String("mode", "", "evaluation mode: auto, screen or deep")
	retry := fs.Bool("retry-known", false, "process rows that only the scan history knows")
	budget := fs.Duration("budget", 0, "wall-clock budget (default 30m)")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseMixed(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) == 1 && !*list && !*runs {
		switch pos[0] {
		case "sources":
			*list, pos = true, nil
		case "runs":
			*runs, pos = true, nil
		}
	}
	switch {
	case *list:
		return e.scanList(ctx, *ws, *asJSON)
	case *runs:
		return e.withDiscoveryAccess(ctx, "scan", *ws, accessRead, func(d discoveryDeps) int {
			n := cmp.Or(*limit, 10)
			recs, total, err := d.repo.ScanPage(ctx, discovery.ScanFilter{Source: *source, Limit: n})
			if err != nil {
				return e.fail("scan", err)
			}
			if *asJSON {
				return e.printJSON(wire.ScanList{Scans: wireAll(recs, api.WireScanRecord), Page: 1, Limit: wire.SafeInt(n), Total: wire.SafeInt(total)})
			}
			if len(recs) == 0 {
				fmt.Fprintln(e.stdout, "no scan runs yet; `applysmith scan sources` lists what can run")
				return 0
			}
			w := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "RUN\tSOURCE\tSTATUS\tSTARTED\tDISCOVERED\tINSERTED\tQUEUED")
			for _, r := range recs {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%d\t%d\n", r.ID, r.Source, r.Status, localTime(r.Started), r.Counts.Discovered, r.Counts.Inserted, r.Counts.Queued)
			}
			w.Flush()
			return 0
		})
	}
	if len(pos) != 1 {
		fmt.Fprint(e.stderr, scanUsage)
		return 2
	}
	if _, err := discovery.LookupSource(pos[0]); err != nil {
		var names []string
		for _, s := range discovery.Sources {
			names = append(names, s.Name)
		}
		if near, ok := workspace.Closest(pos[0], names); ok {
			fmt.Fprintf(e.stderr, "applysmith scan: unknown source %q; did you mean %q?\n", pos[0], near)
			return 2
		}
		fmt.Fprintf(e.stderr, "applysmith scan: %v; `applysmith scan sources` says which are ready\n", err)
		return 2
	}
	if *scope != "" {
		if err := discovery.ValidateScope(*scope); err != nil {
			return e.fail("scan", &usageError{err.Error()})
		}
	}
	root, err := e.workspacePath(*ws)
	if err != nil {
		return e.fail("scan", err)
	}
	if live, err := api.LockHeld(workspace.Open(root).StateDir()); err == nil && live {
		req := wire.ScanRequest{Source: pos[0], Scope: *scope, Sort: *sortBy, Mode: *mode, RetryKnown: *retry, BudgetSeconds: wire.SafeInt(budget.Seconds()),
			Search: *search, Title: *title, City: *city, State: *state, URL: *listURL, Limit: wire.SafeInt(*limit), PostedWithin: *postedWithin,
			Fn: *fn, Seniority: *seniority, Employment: *employment, Remote: *remote,
			Pages: wire.SafeInt(*pages), IgnoreCooldown: *ignoreCooldown, Force: *force, EasyApplyOnly: *easyOnly}
		if *topN >= 0 {
			n := wire.SafeInt(*topN)
			req.TopN = &n
		}
		return e.scanLive(ctx, workspace.Open(root), req, *asJSON)
	}
	return e.withEvaluator(ctx, "scan", *ws, func(x evalEnv) int {
		d := x.discoveryDeps
		if err := d.repo.ScanCloseOrphans(ctx); err != nil {
			return e.fail("scan", err)
		}
		req := discovery.ScanRequest{Source: pos[0], Portals: d.portals, Profile: d.profile, Scope: *scope, Sort: *sortBy, Mode: *mode, RetryKnown: *retry, Budget: *budget,
			Search: *search, Title: *title, City: *city, State: *state, URL: *listURL, Limit: *limit, Fn: *fn, Seniority: *seniority, Employment: *employment, Remote: *remote,
			Pages: *pages, IgnoreCooldown: *ignoreCooldown, Force: *force, EasyApplyOnly: *easyOnly}
		if *postedWithin == "any" {
			req.Window = -1
		} else if *postedWithin != "" {
			w, err := discovery.ParseWindow(*postedWithin)
			if err != nil {
				return e.fail("scan", err)
			}
			req.Window = w
		}
		if *topN >= 0 {
			req.TopN = topN
		}
		sc := e.scanner(d, evaluate.PoolEnqueuer{Pool: x.pool})
		sc.FeedModel = x.router.TaskFunc("feed-classify")
		sc.Unrouted = x.router.EvaluationUnrouted
		rec, err := sc.Run(ctx, req)
		if err != nil {
			return e.fail("scan "+pos[0], err)
		}
		// The jobs the scan queued end before the command does, and their
		// outcomes are on the run's record.
		if err := x.pool.Drain(ctx); err != nil {
			return e.fail("scan "+pos[0], err)
		}
		if rec, err = d.repo.ScanGet(ctx, rec.ID); err != nil {
			return e.fail("scan "+pos[0], err)
		}
		return e.printScan(api.WireScanRecord(rec), *asJSON)
	})
}

// printScan prints a finished scan and returns its exit code: 0 for a
// completed or skipped run, else 1.
func (e *env) printScan(rec wire.ScanRecord, asJSON bool) int {
	if asJSON {
		e.printJSON(rec)
	} else {
		printRecord(e.stdout, rec)
		if rec.Counts.Inserted == 0 && rec.Counts.Filtered["stale"] > 0 {
			fmt.Fprintln(e.stdout, "every posting was older than the freshness window; --posted-within any keeps them")
		}
		if rec.Reason == discovery.ReasonRegistryEmpty {
			fmt.Fprintln(e.stdout, "the registry has no boards to read yet; `applysmith registry seed` downloads its lists")
		}
	}
	switch rec.Status {
	case discovery.RunCompleted, discovery.RunSkipped:
		return 0
	}
	return 1
}

func printRecord(w io.Writer, r wire.ScanRecord) {
	c := r.Counts
	fmt.Fprintln(w, strings.TrimSpace(fmt.Sprintf("run %s: %s %s %s", r.ID, r.Source, r.Status, r.Reason)))
	fmt.Fprintf(w, "  discovered %d = filtered %d + undated %d + duplicates %d + inserted %d\n", c.Discovered, sumCounts(c.Filtered), c.Undated, sumCounts(c.Duplicates), c.Inserted)
	for _, group := range []struct {
		name string
		m    map[string]wire.SafeInt
	}{{"filtered", c.Filtered}, {"duplicates", c.Duplicates}} {
		for _, k := range slices.Sorted(maps.Keys(group.m)) {
			fmt.Fprintf(w, "    %s:%s %d\n", group.name, k, group.m[k])
		}
	}
	fmt.Fprintf(w, "  queued %d, enriched %d, degraded %d, gate-rejected %d, board errors %d, boards visited %d\n", c.Queued, c.Enriched, c.EnrichmentDegraded, c.GateRejected, c.BoardErrors, r.BoardsVisited)
	for _, k := range slices.Sorted(maps.Keys(c.Outcomes)) {
		fmt.Fprintf(w, "    evaluated:%s %d\n", k, c.Outcomes[k])
	}
	if len(r.Truncated) > 0 {
		fmt.Fprintf(w, "  truncated boards (%d postings not read): %s\n", c.TruncatedPostings, strings.Join(r.Truncated, ", "))
	}
	for _, host := range slices.Sorted(maps.Keys(r.Deferred)) {
		fmt.Fprintf(w, "  deferred %d boards on %s\n", r.Deferred[host], host)
	}
	if r.Error != "" {
		fmt.Fprintf(w, "  error: %s\n", r.Error)
	}
}

func sumCounts[N int | wire.SafeInt](m map[string]N) N {
	var n N
	for _, v := range m {
		n += v
	}
	return n
}

// scanList prints each source with the state the stored runs, portals.yml and
// the keychain give it, as the Discovery page reads them: a missing key, a last
// run stopped at the sign-in page, no saved search, and a cooldown after a
// block. The sign-in check of the managed browser is the server's; this list
// does not run it.
func (e *env) scanList(ctx context.Context, wsFlag string, asJSON bool) int {
	return e.withDiscoveryAccess(ctx, "scan", wsFlag, accessRead, func(d discoveryDeps) int {
		hasKey := func(string) bool { return false }
		if id, err := d.ws.ID(); err == nil {
			if v, err := vault.New(id, e.backend, vault.WithGetenv(e.getenv)); err == nil {
				hasKey = func(name string) bool { val, err := v.Get(name); return err == nil && val != "" }
			}
		}
		out := wire.ScanSourceList{Sources: []wire.ScanSource{}}
		for _, s := range discovery.Sources {
			item := wire.ScanSource{Name: s.Name, Title: s.Title, Kind: s.Kind, Needs: s.Needs, Readiness: s.Readiness(hasKey)}
			if item.Readiness == "ready" {
				recent, err := d.repo.ScanList(ctx, s.Name, 1)
				if err != nil {
					return e.fail("scan", err)
				}
				if r, why := discovery.SearchReadiness(s, d.portals); r != "" {
					item.Readiness, item.Reason = r, why
				}
				if len(recent) == 1 && recent[0].Status == discovery.RunNeedsSignIn {
					item.Readiness, item.Reason = "needs sign-in", "The last run stopped at the sign-in page."
				}
			}
			if until, cooling := d.repo.CooldownUntil(ctx, s.Name, discovery.CooldownHours(d.profile, s.Name), ""); cooling {
				item.CooldownUntil = &wire.Time{Time: until}
			}
			out.Sources = append(out.Sources, item)
		}
		if asJSON {
			return e.printJSON(out)
		}
		w := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "SOURCE\tKIND\tNEEDS\tSTATE")
		for _, s := range out.Sources {
			needs, state := cmp.Or(s.Needs, "-"), s.Readiness
			if s.CooldownUntil != nil {
				state += ", cooling down until " + s.CooldownUntil.Time.Local().Format("2006-01-02 15:04")
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Name, s.Kind, needs, state)
		}
		w.Flush()
		return 0
	})
}

// ---- inbox ----

func (e *env) inbox(ctx context.Context, args []string) int {
	if len(args) == 0 || isHelpFlag(args[0]) {
		fmt.Fprint(e.stderr, inboxUsage)
		return 2
	}
	sub, rest := args[0], args[1:]
	fs, parse := e.subFlags("inbox", sub, inboxUsage)
	ws := fs.String("workspace", "", "workspace directory")
	asJSON := fs.Bool("json", false, "print JSON")
	status := fs.String("status", "", "only this status")
	limit := fs.Int("limit", 25, "rows to list")
	top := fs.Int("top", 0, "promote the best N pending rows")
	format := fs.String("format", "csv", "export format: csv or json")
	out := fs.String("out", "", "export to this file")
	pos, ok := parse(rest)
	if !ok {
		return 2
	}
	if sub == "promote" {
		var id int64
		switch {
		case *top > 0 && len(pos) == 0:
		case len(pos) == 1 && *top == 0:
			if id, ok = e.rowID("promote", pos); !ok {
				return 2
			}
		default:
			fmt.Fprint(e.stderr, inboxUsage)
			return 2
		}
		return e.inboxPromote(ctx, *ws, id, *top)
	}
	if sub == "export" && *format != "csv" && *format != "json" {
		return e.refuseUsage("inbox export", inboxUsage, fmt.Sprintf("unknown export format %q; use csv or json", *format))
	}
	access := discoveryAccess(sub, "list", "show", "export")
	return e.withDiscoveryAccess(ctx, "inbox "+sub, *ws, access, func(d discoveryDeps) int {
		fail := func(err error) int { return e.fail("inbox "+sub, err) }
		switch sub {
		case "list":
			counts, err := d.repo.InboxCounts(ctx, time.Time{})
			if err != nil {
				return fail(err)
			}
			if _, ok := counts[strings.ToLower(*status)]; !ok && *status != "" {
				fmt.Fprintf(e.stderr, "applysmith inbox list: %q is not an inbox status; the statuses are %s\n", *status, strings.Join(slices.Sorted(maps.Keys(counts)), ", "))
				return 2
			}
			*status = strings.ToLower(*status)
			rows, err := d.repo.InboxList(ctx, discovery.InboxQuery{Status: *status, Limit: *limit})
			if err != nil {
				return fail(err)
			}
			total := counts[*status]
			if *status == "" {
				total = sumCounts(counts)
			}
			if *asJSON {
				list, err := api.WireInboxRows(ctx, d.st, rows)
				if err != nil {
					return fail(err)
				}
				return e.printJSON(wire.InboxPage{Rows: list, Page: 1, PageSize: wire.SafeInt(*limit), Total: wire.SafeInt(total), Counts: wireCounts(counts)})
			}
			if total == 0 {
				fmt.Fprintf(e.stdout, "no %s; `applysmith scan <source>` or `applysmith inbox add <url>` adds some\n", strings.TrimSpace(*status+" inbox rows"))
				return 0
			}
			w := newColumns(e.stdout)
			fmt.Fprintln(w, "ID\tSCORE\tSTATUS\tSOURCE\tCOMPANY\tROLE\tLOCATION")
			for _, r := range rows {
				fmt.Fprintf(w, "%d\t%d\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Score, r.Status, r.Source, r.Company, r.Role, r.Location)
			}
			w.Flush()
			if len(rows) < total {
				fmt.Fprintf(e.stdout, "%d of %d rows; --limit %d shows them all\n", len(rows), total, total)
			}
			return 0
		case "show":
			id, ok := e.rowID(sub, pos)
			if !ok {
				return 2
			}
			r, err := d.repo.InboxGet(ctx, id)
			if err != nil {
				return fail(err)
			}
			if *asJSON {
				list, err := api.WireInboxRows(ctx, d.st, []discovery.InboxRow{r})
				if err != nil {
					return fail(err)
				}
				return e.printJSON(list[0])
			}
			fmt.Fprintf(e.stdout, "id: %d\nstatus: %s\nurl: %s\nidentity: %s\ncompany: %s\nrole: %s\nlocation: %s\nsource: %s %s\nscore: %d\nreasons: %s\nearly-career: %v\nclearance-required: %v\nrequired-years: %s\nflags: %s\nnotes: %s\n",
				r.ID, r.Status, r.URL, r.IdentityKey, r.Company, r.Role, r.Location, r.Source, r.Detail, r.Score, strings.Join(r.Reasons, "; "),
				r.EarlyCareer, r.ClearanceRequired, yearsText(r.RequiredYears), strings.Join(r.Flags, ", "), r.Notes)
			return 0
		case "add":
			if len(pos) != 1 {
				fmt.Fprint(e.stderr, inboxUsage)
				return 2
			}
			res, err := d.repo.InboxAddURL(ctx, pos[0])
			if err != nil {
				return fail(err)
			}
			if *asJSON {
				out := inboxAdded{Duplicate: res.Duplicate, MatchID: wire.SafeInt(res.MatchID)}
				if res.Row != nil {
					row := api.WireInboxRow(*res.Row)
					out = inboxAdded{Row: &row}
				}
				return e.printJSON(out)
			}
			if res.Row == nil {
				fmt.Fprintf(e.stdout, "not added: already known to the %s (row %d)\n", res.Duplicate, res.MatchID)
				return 0
			}
			fmt.Fprintf(e.stdout, "added inbox row %d: %s\n", res.Row.ID, res.Row.URL)
			return 0
		case "dismiss":
			id, ok := e.rowID(sub, pos)
			if !ok {
				return 2
			}
			if err := d.repo.InboxDismiss(ctx, id); err != nil {
				return fail(err)
			}
			r, err := d.repo.InboxGet(ctx, id)
			if err != nil {
				return fail(err)
			}
			if *asJSON {
				list, err := api.WireInboxRows(ctx, d.st, []discovery.InboxRow{r})
				if err != nil {
					return fail(err)
				}
				return e.printJSON(list[0])
			}
			fmt.Fprintf(e.stdout, "dismissed inbox row %d (%s, %s)\n", id, r.Company, r.Role)
			return 0
		case "export":
			rows, err := d.repo.InboxList(ctx, discovery.InboxQuery{Status: *status})
			if err != nil {
				return fail(err)
			}
			if *out == "" {
				err = exportInbox(e.stdout, *format, rows)
			} else {
				err = writeFileAtomic(*out, func(w io.Writer) error { return exportInbox(w, *format, rows) })
			}
			if err != nil {
				return fail(err)
			}
			return 0
		}
		fmt.Fprint(e.stderr, inboxUsage)
		return 2
	})
}

// inboxPromote queues row id, or the top best pending rows, for evaluation.
// A running server holds the workspace, so it is asked to, and its pool runs
// the evaluations; with no server the command waits for its own pool to
// finish them, since nothing else would run what it queued.
func (e *env) inboxPromote(ctx context.Context, wsFlag string, id int64, top int) int {
	fail := func(err error) int { return e.fail("inbox promote", err) }
	root, err := e.workspacePath(wsFlag)
	if err != nil {
		return fail(err)
	}
	if live, err := api.LockHeld(workspace.Open(root).StateDir()); err == nil && live {
		srv, err := e.attachLive(ctx, workspace.Open(root))
		if err != nil {
			return fail(err)
		}
		req := wire.InboxPromoteRequest{Top: wire.SafeInt(top)}
		if id > 0 {
			req.IDs = []wire.SafeInt{wire.SafeInt(id)}
		}
		var res wire.InboxPromoteResult
		if err := e.liveSend(ctx, srv, http.MethodPost, "/api/inbox/promote", req, &res); err != nil {
			return fail(err)
		}
		rejected := map[int64]string{}
		for k, v := range res.Rejected {
			n, _ := strconv.ParseInt(k, 10, 64)
			rejected[n] = v
		}
		e.printPromoted(len(res.Queued), rejected)
		if len(res.Queued) > 0 {
			fmt.Fprintln(e.stdout, "the evaluations run in the app; `applysmith inbox list` shows how they end")
		}
		return 0
	}
	return e.withEvaluator(ctx, "inbox promote", wsFlag, func(x evalEnv) int {
		enq := evaluate.PoolEnqueuer{Pool: x.pool, Priority: evaluate.PriorityUser}
		var queued []int64
		var rejected map[int64]string
		if top > 0 {
			queued, rejected, err = x.repo.PromoteTop(ctx, enq, gateOf(x.discoveryDeps), top)
		} else {
			queued, rejected, err = x.repo.Promote(ctx, enq, gateOf(x.discoveryDeps), id)
		}
		if err != nil {
			return fail(err)
		}
		if err := x.pool.Drain(ctx); err != nil {
			return fail(err)
		}
		e.printPromoted(len(queued), rejected)
		for _, id := range queued {
			row, err := x.repo.InboxGet(ctx, id)
			if err != nil {
				return fail(err)
			}
			fmt.Fprintf(e.stdout, "row %d (%s, %s): %s\n", id, row.Company, row.Role, row.Status)
		}
		return 0
	})
}

func (e *env) printPromoted(queued int, rejected map[int64]string) {
	fmt.Fprintf(e.stdout, "queued %d rows for evaluation (%d rejected by the gate)\n", queued, len(rejected))
	for _, id := range slices.Sorted(maps.Keys(rejected)) {
		fmt.Fprintf(e.stdout, "row %d: kept out by the gate: %s\n", id, rejected[id])
	}
}

func yearsText(y *int) string {
	if y == nil {
		return "none"
	}
	return strconv.Itoa(*y)
}

func (e *env) rowID(cmd string, pos []string) (int64, bool) {
	if len(pos) != 1 {
		fmt.Fprint(e.stderr, inboxUsage)
		return 0, false
	}
	id, err := parseID(pos[0])
	if err != nil {
		fmt.Fprintf(e.stderr, "applysmith inbox %s: %v\n", cmd, err)
		return 0, false
	}
	return id, true
}

func exportInbox(w io.Writer, format string, rows []discovery.InboxRow) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(wireAll(rows, api.WireInboxRow))
	case "csv":
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"id", "status", "score", "company", "role", "location", "source", "url", "identity_key", "early_career", "clearance_required", "required_years", "first_seen"})
		for _, r := range rows {
			_ = cw.Write([]string{strconv.FormatInt(r.ID, 10), r.Status, strconv.Itoa(r.Score), tracker.CSVSafe(r.Company), tracker.CSVSafe(r.Role),
				tracker.CSVSafe(r.Location), tracker.CSVSafe(r.Source), tracker.CSVSafe(r.URL), tracker.CSVSafe(r.IdentityKey),
				strconv.FormatBool(r.EarlyCareer), strconv.FormatBool(r.ClearanceRequired), yearsText(r.RequiredYears), r.FirstSeen.Format(time.RFC3339)})
		}
		cw.Flush()
		return cw.Error()
	}
	return fmt.Errorf("unknown export format %q; use csv or json", format)
}

// ---- blocked ----

func (e *env) blocked(ctx context.Context, args []string) int {
	if len(args) == 0 || isHelpFlag(args[0]) {
		fmt.Fprint(e.stderr, blockedUsage)
		return 2
	}
	sub := args[0]
	fs, parse := e.subFlags("blocked", sub, blockedUsage)
	ws := fs.String("workspace", "", "workspace directory")
	reason := fs.String("reason", "", "why the company is blocked")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, ok := parse(args[1:])
	if !ok {
		return 2
	}
	return e.withDiscoveryAccess(ctx, "blocked "+sub, *ws, discoveryAccess(sub, "list"), func(d discoveryDeps) int {
		switch {
		case sub == "add" && len(pos) == 1:
			b, err := d.repo.BlockCompany(ctx, pos[0], *reason, "user")
			if err != nil {
				return e.fail("blocked add", err)
			}
			if *asJSON {
				return e.printJSON(api.WireBlocked(b))
			}
			fmt.Fprintf(e.stdout, "blocked %s (matches %q)\n", b.Name, b.Normalized)
			return 0
		case sub == "remove" && len(pos) == 1:
			if err := d.repo.UnblockCompany(ctx, pos[0]); err != nil {
				return e.fail("blocked remove", err)
			}
			fmt.Fprintf(e.stdout, "unblocked %s\n", pos[0])
			return 0
		case sub == "list" && len(pos) == 0:
			list, err := d.repo.BlockedCompanies(ctx)
			if err != nil {
				return e.fail("blocked list", err)
			}
			if *asJSON {
				return e.printJSON(wire.BlockedCompanyList{Companies: wireAll(list, api.WireBlocked)})
			}
			if len(list) == 0 {
				fmt.Fprintln(e.stdout, "no blocked companies; `applysmith blocked add <name>` adds one")
			}
			w := newColumns(e.stdout)
			for _, b := range list {
				fmt.Fprintf(w, "%s\t%s\t%s\n", b.Name, b.AddedAt.Local().Format("2006-01-02"), b.Reason)
			}
			return flushOK(w)
		}
		fmt.Fprint(e.stderr, blockedUsage)
		return 2
	})
}

// ---- registry ----

func (e *env) registry(ctx context.Context, args []string) int {
	if len(args) == 0 || isHelpFlag(args[0]) {
		fmt.Fprint(e.stderr, registryUsage)
		return 2
	}
	sub := args[0]
	fs, parse := e.subFlags("registry", sub, registryUsage)
	ws := fs.String("workspace", "", "workspace directory")
	asJSON := fs.Bool("json", false, "print JSON")
	licence := fs.String("licence", "", "licence of the seed list")
	var sources stringList
	fs.Var(&sources, "source", "a slug list URL (repeatable)")
	pos, ok := parse(args[1:])
	if !ok {
		return 2
	}
	if (sub == "seed" || sub == "stats") && len(pos) > 0 {
		fmt.Fprintf(e.stderr, "applysmith registry %s: unexpected argument %q\n", sub, pos[0])
		return 2
	}
	return e.withDiscoveryAccess(ctx, "registry "+sub, *ws, discoveryAccess(sub, "stats"), func(d discoveryDeps) int {
		fail := func(err error) int { return e.fail("registry "+sub, err) }
		switch {
		case sub == "stats":
			stats, err := d.repo.RegistryStats(ctx)
			if err != nil {
				return fail(err)
			}
			if *asJSON {
				return e.printJSON(stats)
			}
			fmt.Fprintf(e.stdout, "active %d, dead %d, blocked %d\n", stats[discovery.StatusActive], stats[discovery.StatusDead], stats[discovery.StatusBlocked])
			return 0
		case sub == "seed":
			seeder := &discovery.Seeder{Repo: d.repo, Dir: filepath.Join(d.ws.StateDir(), "downloads"), HTTP: e.scanHTTPOrDefault()}
			list := discovery.DefaultSeedSources()
			if len(sources) > 0 {
				list = nil
				for _, u := range sources {
					list = append(list, discovery.SeedSource{URL: u, Licence: *licence})
				}
			}
			fmt.Fprintf(e.stderr, "downloading %d board list(s); this can take a few minutes\n", len(list))
			results := seeder.Seed(ctx, list)
			for _, r := range results {
				if r.Err != nil {
					fmt.Fprintf(e.stderr, "%s: %v\n", r.URL, r.Err)
					continue
				}
				fmt.Fprintf(e.stdout, "%s: %d rows, %d new, %d rejected\n", r.URL, r.Rows, r.Inserted, r.Rejected)
			}
			if err := discovery.SeedFailed(results); err != nil {
				return fail(err)
			}
			return 0
		case sub == "refresh" && len(pos) == 0:
			seeder := &discovery.Seeder{Repo: d.repo, Dir: filepath.Join(d.ws.StateDir(), "downloads"), HTTP: e.scanHTTPOrDefault()}
			fmt.Fprintln(e.stderr, "downloading the board lists and probing the dead boards; this can take a few minutes")
			res, err := discovery.RegistryRefresh(ctx, seeder, e.scanner(d, nil), d.portals, nil)
			if err != nil {
				return fail(err)
			}
			fmt.Fprintf(e.stdout, "%d new boards, %d dead boards probed, %d revived\n", res.New(), res.Probe.Probed, res.Probe.Revived)
			return 0
		case sub == "add" && len(pos) == 1:
			b, err := discovery.ResolveBoard(pos[0])
			if err != nil {
				return fail(fmt.Errorf("%s names no supported board", pos[0]))
			}
			n, err := d.repo.RegistryAdd(ctx, "manual", b)
			if err != nil {
				return fail(err)
			}
			fmt.Fprintf(e.stdout, "%s: %d new\n", b.Key(), n)
			return 0
		case sub == "import" && len(pos) == 1:
			data, err := os.ReadFile(pos[0])
			if err != nil {
				return fail(err)
			}
			boards, rejected, err := discovery.ParseSeedList(data)
			if err != nil {
				return fail(err)
			}
			n, err := d.repo.RegistryAdd(ctx, "import "+filepath.Base(pos[0]), boards...)
			if err != nil {
				return fail(err)
			}
			fmt.Fprintf(e.stdout, "%d rows, %d new, %d rejected\n", len(boards), n, rejected)
			return 0
		case sub == "prune" && len(pos) == 0:
			n, err := d.repo.RegistryPrune(ctx)
			if err != nil {
				return fail(err)
			}
			fmt.Fprintf(e.stdout, "pruned %d dead boards\n", n)
			return 0
		case (sub == "revive" || sub == "block") && len(pos) == 1:
			b, err := discovery.ParseBoardKey(pos[0])
			if err != nil {
				return fail(err)
			}
			status := discovery.StatusActive
			if sub == "block" {
				status = discovery.StatusBlocked
			}
			if err := d.repo.RegistrySetStatus(ctx, b, status); err != nil {
				return fail(err)
			}
			fmt.Fprintf(e.stdout, "%s is now %s\n", b.Key(), status)
			return 0
		case sub == "probe" && len(pos) == 0:
			res, err := e.scanner(d, nil).ProbeDead(ctx, d.portals)
			if err != nil {
				return fail(err)
			}
			fmt.Fprintf(e.stdout, "probed %d dead boards, %d revived\n", res.Probed, res.Revived)
			return 0
		}
		fmt.Fprint(e.stderr, registryUsage)
		return 2
	})
}

func (e *env) scanHTTPOrDefault() *http.Client {
	if e.scanHTTP != nil {
		return e.scanHTTP
	}
	return &http.Client{}
}

// inboxAdded is what inbox add --json prints: the row that was added, or the
// store that already holds the posting.
type inboxAdded struct {
	Row       *wire.InboxRow `json:"row,omitempty"`
	Duplicate string         `json:"duplicate,omitempty"`
	MatchID   wire.SafeInt   `json:"matchId,omitempty"`
}

// ---- liveness ----

func (e *env) liveness(ctx context.Context, args []string) int {
	fs := e.flagsWithUsage("liveness", livenessUsage)
	ws := fs.String("workspace", "", "workspace directory")
	company := fs.String("company", "", "the company the posting should show")
	role := fs.String("role", "", "the role the posting should show")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseMixed(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 {
		fmt.Fprint(e.stderr, livenessUsage)
		return 2
	}
	return e.withDiscovery(ctx, "liveness", *ws, func(d discoveryDeps) int {
		tr, err := tracker.New(d.st, tracker.Options{})
		if err != nil {
			return e.fail("liveness", err)
		}
		reader := e.livenessReader
		if reader == nil {
			reader = launch.LivenessReader(d.ws)
		}
		c := &discovery.Checker{Repo: d.repo, Reader: reader, Tracker: tr}
		l, err := c.Check(ctx, pos[0], discovery.Expect{Company: *company, Role: *role})
		if err != nil {
			return e.fail("liveness", err)
		}
		if *asJSON {
			return e.printJSON(l)
		}
		fmt.Fprintf(e.stdout, "%s (%s)\n", l.Verdict, l.Reason)
		return 0
	})
}
