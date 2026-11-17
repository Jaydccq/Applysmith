package cli

import (
	"context"
	"fmt"
	"strings"

	"applysmith/internal/api"
	"applysmith/internal/api/wire"
	"applysmith/internal/discovery"
	"applysmith/internal/evaluate"
	"applysmith/internal/tracker"
)

const leadsUsage = `usage:
  applysmith leads list [--state new|evaluated|dismissed] [--category c] [--sort newest|confidence] [--limit n] [--json]
  applysmith leads expand [--limit n] [--force id]...
  applysmith leads evaluate [--limit n]
  applysmith leads dismiss <id> [--reason text]

expand reads the careers pages of stored leads, at most 20 a run, and adds the postings they list to the inbox; a page already looked at is not read again unless --force names its lead.
evaluate recovers posting leads from the boards that own them and sends at most 25 leads a run to evaluation.
list --sort confidence puts the surest leads first, the newest first among equals.
every command takes --workspace <dir>; list and dismiss work beside a running server, expand and evaluate need it stopped
`

type idList []int64

func (l *idList) String() string { return fmt.Sprint(*l) }
func (l *idList) Set(v string) error {
	id, err := parseID(v)
	if err != nil {
		return err
	}
	*l = append(*l, id)
	return nil
}

func (e *env) leads(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(e.stderr, leadsUsage)
		return 2
	}
	sub := args[0]
	fs, parse := e.subFlags("leads", sub, leadsUsage)
	ws := fs.String("workspace", "", "workspace directory")
	state := fs.String("state", "", "only leads in this state")
	category := fs.String("category", "", "only leads in this category")
	sortBy := fs.String("sort", "newest", "list: newest or confidence (surest first)")
	limit := fs.Int("limit", 0, "list: how many leads (default 25); expand and evaluate: the per-run cap")
	reason := fs.String("reason", "", "why the lead is dismissed")
	asJSON := fs.Bool("json", false, "print JSON")
	var force idList
	fs.Var(&force, "force", "expand: read this lead's page again (repeatable)")
	pos, ok := parse(args[1:])
	if !ok {
		return 2
	}
	fail := func(err error) int { return e.fail("leads "+sub, err) }
	switch {
	case sub == "list" && len(pos) == 0:
		if *sortBy != "newest" && *sortBy != "confidence" {
			fmt.Fprintf(e.stderr, "applysmith leads list: --sort must be newest or confidence\n\n%s", leadsUsage)
			return 2
		}
		return e.withDiscoveryAccess(ctx, "leads list", *ws, accessRead, func(d discoveryDeps) int {
			n := *limit
			if n <= 0 {
				n = 25
			}
			f := discovery.LeadFilter{State: *state, Category: *category, Limit: n, ByConfidence: *sortBy == "confidence"}
			list, err := d.repo.LeadList(ctx, f)
			if err != nil {
				return fail(err)
			}
			if *asJSON {
				return e.printLeadPage(ctx, d, f, list)
			}
			if len(list) == 0 {
				fmt.Fprintln(e.stdout, "no leads; the linkedin-feed scan and careers pages without a board add them")
				return 0
			}
			w := newColumns(e.stdout)
			fmt.Fprintln(w, "ID\tSTATE\tCATEGORY\tSOURCE\tCOMPANY\tROLES\tEXPANDED")
			for _, l := range list {
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", l.ID, l.State, l.Category, l.Source, l.Company, strings.Join(l.Roles, "; "), l.Expanded)
			}
			return flushOK(w)
		})
	case sub == "expand" && len(pos) == 0:
		return e.withDiscovery(ctx, "leads expand", *ws, func(d discoveryDeps) int {
			res, err := e.scanner(d, nil).ExpandLeads(ctx, discovery.ExpandRequest{Portals: d.portals, Profile: d.profile, Limit: *limit, Force: force})
			if err != nil {
				return fail(err)
			}
			fmt.Fprintln(e.stdout, res.Summary())
			return 0
		})
	case sub == "evaluate" && len(pos) == 0:
		return e.withEvaluator(ctx, "leads evaluate", *ws, func(x evalEnv) int {
			d := x.discoveryDeps
			res, err := e.scanner(d, evaluate.PoolEnqueuer{Pool: x.pool}).EvaluateLeads(ctx, discovery.EvaluateRequest{Portals: d.portals, Profile: d.profile, Limit: *limit})
			if err != nil {
				return fail(err)
			}
			if err := x.pool.Drain(ctx); err != nil {
				return fail(err)
			}
			fmt.Fprintln(e.stdout, res.Summary())
			return 0
		})
	case sub == "dismiss" && len(pos) == 1:
		id, err := parseID(pos[0])
		if err != nil {
			fmt.Fprintf(e.stderr, "applysmith leads dismiss: %v\n", err)
			return 2
		}
		return e.withDiscoveryAccess(ctx, "leads dismiss", *ws, accessShared, func(d discoveryDeps) int {
			why := *reason
			if why == "" {
				why = "dismissed by the user"
			}
			if err := d.repo.LeadDismiss(ctx, id, why); err != nil {
				return fail(err)
			}
			fmt.Fprintf(e.stdout, "dismissed lead %d\n", id)
			return 0
		})
	}
	fmt.Fprint(e.stderr, leadsUsage)
	return 2
}

// printLeadPage prints the leads as GET /api/leads answers them.
func (e *env) printLeadPage(ctx context.Context, d discoveryDeps, f discovery.LeadFilter, list []discovery.Lead) int {
	total, err := d.repo.LeadCount(ctx, f)
	if err != nil {
		return e.fail("leads list", err)
	}
	counts, err := d.repo.LeadCounts(ctx, f)
	if err != nil {
		return e.fail("leads list", err)
	}
	tr, err := tracker.New(d.st, tracker.Options{})
	if err != nil {
		return e.fail("leads list", err)
	}
	rows, err := api.WireLeads(ctx, d.st, d.repo, tr, list)
	if err != nil {
		return e.fail("leads list", err)
	}
	return e.printJSON(wire.LeadPage{Rows: rows, Page: 1, PageSize: wire.SafeInt(f.Limit), Total: wire.SafeInt(total), Counts: wireCounts(counts)})
}
