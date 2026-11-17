package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"applysmith/internal/api"
	"applysmith/internal/api/signalsapi"
	"applysmith/internal/llm"
	"applysmith/internal/signals"
)

const signalsUsage = `usage:
  applysmith signals tick [--sources signals-x,signals-hn] [--json]
  applysmith signals backfill --days N [--json]
  applysmith signals rescore
  applysmith signals replay --policy <file> [--json]
  applysmith signals decompose --company <name> [--json]
  applysmith signals status [--json]

tick runs the hiring-signal pipeline once: collect, identify, assess, tier, check postings, hand off, notify. --sources limits it to the named collectors, which then collect and ingest only.
backfill reads the SEC Form D indexes of the past N business days (needs the signals-edgar source enabled).
rescore recomputes every tier from stored events and assessments, with no model or network call.
replay prints the tiers a candidate policy file would give, next to the stored ones, and changes nothing; the file's declared version must equal its name.
every command takes --workspace <dir>; status, replay and decompose only read and work beside a running server, the others need it stopped
`

type signalsEnv struct {
	discoveryDeps
	svc *signals.Service
}

// withSignals opens the workspace with the given access and builds the signal
// service over the same router and vault the server uses. Status only reads,
// so it works beside the running app; the rest hold the workspace lock.
func (e *env) withSignals(ctx context.Context, cmd, wsFlag string, access storeAccess, fn func(signalsEnv) int) int {
	return e.withDiscoveryAccess(ctx, cmd, wsFlag, access, func(d discoveryDeps) int {
		v, err := e.vaultOf(d.ws)
		if err != nil {
			return e.fail(cmd, err)
		}
		router := llm.New(d.st, v, append([]llm.Option{llm.WithGetenv(e.getenv)}, e.llmOptions...)...)
		svc, err := signals.New(signals.Deps{Store: d.st, Discovery: d.repo, Model: router, Secret: api.VaultSecret(v), HTTP: e.scanHTTP, Endpoints: e.signalEndpoints,
			Settings: func(context.Context) (signals.Settings, error) {
				p, perr := loadPortals(d.ws)
				if perr != nil {
					return signals.Settings{}, perr
				}
				pr, perr := loadProfile(d.ws)
				return signals.Settings{Portals: p, Profile: pr}, perr
			}})
		if err != nil {
			return e.fail(cmd, err)
		}
		return fn(signalsEnv{discoveryDeps: d, svc: svc})
	})
}

func (e *env) signals(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(e.stderr, signalsUsage)
		return 2
	}
	switch args[0] {
	case "tick":
		return e.signalsTick(ctx, "tick", args[1:])
	case "backfill":
		return e.signalsTick(ctx, "backfill", args[1:])
	case "rescore":
		return e.signalsRescore(ctx, args[1:])
	case "replay":
		return e.signalsReplay(ctx, args[1:])
	case "decompose":
		return e.signalsDecompose(ctx, args[1:])
	case "status":
		return e.signalsStatus(ctx, args[1:])
	}
	fmt.Fprint(e.stderr, signalsUsage)
	return 2
}

func (e *env) signalsTick(ctx context.Context, sub string, args []string) int {
	fs := e.flags("signals " + sub)
	ws := fs.String("workspace", "", "workspace directory")
	sources := fs.String("sources", "", "limit the tick to these collectors")
	nDays := fs.Int("days", 0, "backfill: business days to read")
	asJSON := fs.Bool("json", false, "print JSON")
	if pos, err := parseMixed(fs, args); err != nil || len(pos) > 0 {
		if err == nil {
			fmt.Fprintf(e.stderr, "applysmith signals %s: unexpected argument %q\n", sub, pos[0])
		}
		return 2
	}
	req := signals.TickRequest{Progress: func(line string) { fmt.Fprintln(e.stderr, line) }}
	if sub == "backfill" {
		if *nDays < 1 || *nDays > 400 {
			fmt.Fprint(e.stderr, signalsUsage)
			return 2
		}
		req.Days, req.Sources = *nDays, []string{signals.SourceEdgar}
	} else {
		list, err := signals.ParseSources(*sources)
		if err != nil {
			fmt.Fprintf(e.stderr, "applysmith signals: %v\n", err)
			return 2
		}
		req.Sources = list
	}
	return e.withSignals(ctx, "signals", *ws, accessExclusive, func(x signalsEnv) int {
		sum, err := x.svc.Tick(ctx, req)
		var active *signals.ActiveError
		if errors.As(err, &active) {
			fmt.Fprintf(e.stderr, "applysmith signals: %v\n", err)
			return 1
		}
		if *asJSON {
			e.printJSON(signalsapi.WireSignalTick(sum))
		} else {
			e.printTick(sum)
		}
		if err != nil {
			fmt.Fprintf(e.stderr, "applysmith signals: the tick failed: %v\n", err)
			return 1
		}
		return 0
	})
}

func (e *env) printTick(sum signals.TickSummary) {
	w := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SOURCE\tSTATUS\tDOCS\tNEW\tREASON")
	for _, s := range sum.Sources {
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\n", s.ID, s.Status, s.Docs, s.New, s.Reason)
	}
	w.Flush()
	fmt.Fprintf(e.stdout, "tick %d %s: %s\n", sum.TickID, sum.Status, sum.Line())
	for _, a := range sum.Alarms {
		fmt.Fprintf(e.stdout, "alarm %s: %s %s\n", a.Source, a.Status, a.RequiredAction)
	}
	for _, n := range sum.Notes {
		fmt.Fprintf(e.stdout, "note: %s\n", n)
	}
}

func (e *env) signalsRescore(ctx context.Context, args []string) int {
	fs := e.flags("signals rescore")
	ws := fs.String("workspace", "", "workspace directory")
	if _, err := parseMixed(fs, args); err != nil {
		return 2
	}
	return e.withSignals(ctx, "signals", *ws, accessExclusive, func(x signalsEnv) int {
		if err := x.svc.Rescore(ctx); err != nil {
			return e.fail("signals", err)
		}
		tiers, err := x.svc.Repo().Tiers(ctx)
		if err != nil {
			return e.fail("signals", err)
		}
		count := map[string]int{}
		for _, t := range tiers {
			count[t.Tier]++
		}
		fmt.Fprintf(e.stdout, "rescored %d companies: %d hot, %d warm, %d watch, %d cold\n", len(tiers), count[signals.TierHot], count[signals.TierWarm], count[signals.TierWatch], count[signals.TierCold])
		return 0
	})
}

func (e *env) signalsReplay(ctx context.Context, args []string) int {
	fs := e.flags("signals replay")
	ws := fs.String("workspace", "", "workspace directory")
	file := fs.String("policy", "", "candidate policy file")
	asJSON := fs.Bool("json", false, "print JSON")
	if _, err := parseMixed(fs, args); err != nil || *file == "" {
		fmt.Fprint(e.stderr, signalsUsage)
		return 2
	}
	data, err := os.ReadFile(*file)
	if err != nil {
		return e.fail("signals", err)
	}
	p, err := signals.LoadPolicyFile(data, *file)
	if err != nil {
		fmt.Fprintf(e.stderr, "applysmith signals: %v\n", err)
		return 2
	}
	return e.withSignals(ctx, "signals", *ws, accessRead, func(x signalsEnv) int {
		rows, err := x.svc.Repo().Replay(ctx, p)
		if err != nil {
			return e.fail("signals", err)
		}
		if *asJSON {
			return e.printJSON(rows)
		}
		w := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "COMPANY\tBEFORE\tAFTER\tMOMENTUM")
		for _, r := range rows {
			fmt.Fprintf(w, "%s\t%s\t%s\t%.3f\n", r.Company, r.Before, r.After, r.Momentum)
		}
		return noErr(w.Flush())
	})
}

func noErr(err error) int {
	if err != nil {
		return 1
	}
	return 0
}

func (e *env) signalsDecompose(ctx context.Context, args []string) int {
	fs := e.flags("signals decompose")
	ws := fs.String("workspace", "", "workspace directory")
	name := fs.String("company", "", "company name")
	asJSON := fs.Bool("json", false, "print JSON")
	if _, err := parseMixed(fs, args); err != nil || *name == "" {
		fmt.Fprint(e.stderr, signalsUsage)
		return 2
	}
	return e.withSignals(ctx, "signals", *ws, accessRead, func(x signalsEnv) int {
		c, err := x.svc.Repo().FindCompany(ctx, *name)
		if err != nil {
			fmt.Fprintf(e.stderr, "applysmith signals: no company named %q\n", *name)
			return 1
		}
		rows, momentum, err := x.svc.Repo().Decompose(ctx, x.svc.Policy(), c.ID)
		if err != nil {
			return e.fail("signals", err)
		}
		if *asJSON {
			return e.printJSON(map[string]any{"company": c.Name, "momentum": momentum, "events": rows})
		}
		w := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "EVENT\tTYPE\tINTENT\tTYPE-W\tSTRENGTH\tCONF\tSOURCE\tDECAY\tCONTRIBUTION")
		for _, r := range rows {
			fmt.Fprintf(w, "%d\t%s\t%s\t%.2f\t%.2f\t%.2f\t%.2f\t%.3f\t%.3f\n", r.EventID, r.EventType, r.Intent, r.Type, r.Strength, r.Confidence, r.Source, r.Decay, r.Contribution)
		}
		w.Flush()
		fmt.Fprintf(e.stdout, "%s momentum %.3f\n", c.Name, momentum)
		return 0
	})
}

func (e *env) signalsStatus(ctx context.Context, args []string) int {
	fs := e.flags("signals status")
	ws := fs.String("workspace", "", "workspace directory")
	asJSON := fs.Bool("json", false, "print JSON")
	if _, err := parseMixed(fs, args); err != nil {
		return 2
	}
	return e.withSignals(ctx, "signals", *ws, accessRead, func(x signalsEnv) int {
		st, err := x.svc.Status(ctx)
		if err != nil {
			return e.fail("signals", err)
		}
		if *asJSON {
			return e.printJSON(signalsapi.WireSignalStatus(st))
		}
		if t := st.LastTick; t != nil {
			fmt.Fprintf(e.stdout, "last tick %d %s at %s: %s\n", t.TickID, t.Status, localTime(t.StartedAt), t.Line())
		} else {
			fmt.Fprintln(e.stdout, "no tick has run")
		}
		fmt.Fprintf(e.stdout, "tiers: %d hot, %d warm, %d watch, %d cold; %d assessments in review\n",
			st.Tiers[signals.TierHot], st.Tiers[signals.TierWarm], st.Tiers[signals.TierWatch], st.Tiers[signals.TierCold], st.ReviewOpen)
		o := st.Outcomes
		lead := ""
		if o.Hits > 0 {
			lead = fmt.Sprintf(", median lead %.0f days", o.MedianLead)
		}
		fmt.Fprintf(e.stdout, "outcomes: %d hits, %d misses, %d pending; %s%s\n", o.Hits, o.Misses, o.Pending, hitRate(o.Hits, o.Misses, o.HitRate), lead)
		for _, typ := range []string{signals.TypeFunding, signals.TypeExpansion, signals.TypeProduct, signals.TypeLeadership, signals.TypeOther} {
			if h, ok := o.ByType[typ]; ok {
				fmt.Fprintf(e.stdout, "  %s: %d hits, %d misses, %d pending; %s\n", typ, h.Hits, h.Misses, h.Pending, hitRate(h.Hits, h.Misses, h.HitRate))
			}
		}
		w := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "SOURCE\tENABLED\tLAST PRODUCED\tLAST STATUS")
		for _, l := range st.Lanes {
			last := "never"
			if !l.LastProduced.IsZero() {
				last = localTime(l.LastProduced)
			}
			fmt.Fprintf(w, "%s\t%v\t%s\t%s\n", l.Source, l.Enabled, last, strings.TrimSpace(l.LastStatus))
		}
		return noErr(w.Flush())
	})
}

// hitRate words the share of resolved outcomes that were hits; with none
// resolved there is no rate, not a rate of 0 %.
func hitRate(hits, misses int, rate float64) string {
	if hits+misses == 0 {
		return "none resolved yet"
	}
	return fmt.Sprintf("hit rate %.0f%%", rate*100)
}
