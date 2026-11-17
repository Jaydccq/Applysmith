package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"applysmith/internal/api"
	"applysmith/internal/browser"
	"applysmith/internal/discovery"
	"applysmith/internal/evaluate"
	"applysmith/internal/identity"
	"applysmith/internal/kb"
	"applysmith/internal/launch"
	"applysmith/internal/llm"
	"applysmith/internal/prompts"
	"applysmith/internal/tracker"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

const evaluateUsage = `usage:
  applysmith evaluate <url|inbox-row-id> [--mode auto|screen|deep] [--force] [--title t] [--company c] [--location l] [--compensation c] [--source s] [--json]
  applysmith evaluate --file <path> | --text <posting text> [--url u] [same flags]
  applysmith evaluate gaps --report <number>[,<number>...] [--report <number>...] [--json]
  applysmith evaluate decode (--file <path> | --text <posting text>) [--title t] [--company c] [--location l] [--json]
  applysmith prompts list | show <name> | diff <name> | check

every command takes --workspace <dir> and works beside a running server; evaluate then hands the posting to it
`

// evalEnv is an evaluator wired to the workspace, for one command. pool runs
// the evaluations a scan or a promotion queues: with no server, the command
// itself is the only process that can run them.
type evalEnv struct {
	discoveryDeps
	ev      *evaluate.Evaluator
	pool    *evaluate.Pool
	router  *llm.Router
	browser *faultNoter
}

// faultNoter keeps the last browser read that the computer, not the page,
// stopped: an environment fault (no usable Chrome, its profile in use) or no
// browser at all. The evaluation fails as posting-unreadable either way; the
// command exits 4 for it and says what to do.
type faultNoter struct {
	discovery.PageReader
	mu    sync.Mutex
	fault error
}

func (r *faultNoter) Read(ctx context.Context, raw string) (discovery.PageRead, error) {
	p, err := r.PageReader.Read(ctx, raw)
	if browser.EnvironmentFault(err) || errors.Is(err, evaluate.ErrNeedsBrowser) {
		r.mu.Lock()
		r.fault = err
		r.mu.Unlock()
	}
	return p, err
}

func (r *faultNoter) noted() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fault
}

// withEvaluator opens the workspace database under the workspace lock and
// builds an evaluator whose model calls go through the router, and a started
// pool over it that is closed when the command ends.
func (e *env) withEvaluator(ctx context.Context, cmd, wsFlag string, fn func(evalEnv) int) int {
	// The evaluations of one command are numbered as they start, so the
	// progress lines of a scan's evaluations tell them apart.
	var mu sync.Mutex
	runs := map[string]int{}
	return e.withDiscovery(ctx, cmd, wsFlag, func(d discoveryDeps) int {
		id, err := d.ws.ID()
		if err != nil {
			return e.fail(cmd, err)
		}
		v, err := vault.New(id, e.backend, vault.WithGetenv(e.getenv))
		if err != nil {
			return e.fail(cmd, err)
		}
		tr, err := tracker.New(d.st, tracker.Options{})
		if err != nil {
			return e.fail(cmd, err)
		}
		router := llm.New(d.st, v, append([]llm.Option{llm.WithGetenv(e.getenv), llm.WithFake(evaluate.NewFake())}, e.llmOptions...)...)
		knowledge, err := kb.New(d.st, api.KnowledgeOptions(d.ws, d.st, router, v))
		if err != nil {
			return e.fail(cmd, err)
		}
		reader := &faultNoter{PageReader: e.livenessReader}
		if reader.PageReader == nil {
			reader.PageReader = launch.LivenessReader(d.ws)
		}
		ev, err := evaluate.New(evaluate.Deps{
			Store: d.st, Tracker: tr, Model: router, Prompts: prompts.New(filepath.Join(d.ws.Root, "prompts")),
			Profile: func(context.Context) (evaluate.Profile, error) { return evaluate.LoadProfile(d.ws) },
			Reader:  evaluate.ManagedReader{ATS: evaluate.ATSReader{Client: e.scanHTTP, Hosts: e.scanEndpoints, Boards: &evaluate.BoardCache{}}, Browser: reader}, Blocked: evaluate.BlockedFromDiscovery(d.repo), Inbox: evaluate.DiscoveryInbox{Repo: d.repo},
			ScanOutcome: evaluate.ScanOutcomes(d.repo), Knowledge: api.EvaluationKnowledge(knowledge), Closed: (&discovery.Checker{Repo: d.repo, Tracker: tr}).Record,
			CapReached: router.EvaluationHeld, Root: d.ws.Root, Emit: func(ev evaluate.Event) {
				if !ev.Final {
					mu.Lock()
					if runs[ev.RunID] == 0 {
						runs[ev.RunID] = len(runs) + 1
					}
					fmt.Fprintf(e.stderr, "evaluation %d: %s\n", runs[ev.RunID], ev.Phase)
					mu.Unlock()
				}
			},
		})
		if err != nil {
			return e.fail(cmd, err)
		}
		pool := evaluate.NewPool(ev, evaluate.PoolOptions{Concurrency: evaluate.LLMConcurrency(router, ev)})
		if err := pool.Start(ctx); err != nil {
			return e.fail(cmd, err)
		}
		defer pool.Close()
		return fn(evalEnv{discoveryDeps: d, ev: ev, pool: pool, router: router, browser: reader})
	})
}

func (e *env) evaluate(ctx context.Context, args []string) int {
	if len(args) > 0 && args[0] == "gaps" {
		return e.evaluateGaps(ctx, args[1:])
	}
	if len(args) > 0 && args[0] == "decode" {
		return e.evaluateDecode(ctx, args[1:])
	}
	fs := e.flagsWithUsage("evaluate", evaluateUsage)
	ws := fs.String("workspace", "", "workspace directory")
	file := fs.String("file", "", "read the posting text from a file")
	text := fs.String("text", "", "the posting text")
	postingURL := fs.String("url", "", "the posting URL, with --file or --text")
	modeFlag := fs.String("mode", "deep", "auto, screen or deep")
	force := fs.Bool("force", false, "evaluate again even when a final report exists")
	title := fs.String("title", "", "title hint")
	company := fs.String("company", "", "company hint")
	location := fs.String("location", "", "location hint")
	comp := fs.String("compensation", "", "compensation text hint")
	source := fs.String("source", "cli", "source label")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseMixed(fs, args)
	if err != nil {
		return 2
	}
	req := evaluate.Request{URL: *postingURL, Text: *text, Title: *title, Company: *company, Location: *location,
		Compensation: *comp, Source: *source, Mode: evaluate.Mode(*modeFlag), Force: *force, Priority: evaluate.PriorityUser}
	if *file != "" {
		b, err := os.ReadFile(*file)
		if err != nil {
			return e.fail("evaluate", err)
		}
		req.Text = string(b)
	}
	if len(pos) > 1 || (len(pos) == 0 && req.Text == "" && req.URL == "") {
		fmt.Fprint(e.stderr, evaluateUsage)
		return 2
	}
	rowID, byRow := int64(0), false
	if len(pos) == 1 {
		if id, err := strconv.ParseInt(pos[0], 10, 64); err == nil {
			rowID, byRow = id, true
		} else if _, err := identity.CanonicalURL(pos[0]); err != nil {
			return e.refuseUsage("evaluate", evaluateUsage, "the argument is neither an inbox row id nor an http(s) posting URL; pasted text goes in --text or --file")
		} else {
			req.URL = pos[0]
		}
	}
	// A text too short to evaluate is refused before a run exists, as decode
	// refuses one, so a typo leaves nothing on the board.
	if req.Text != "" {
		if err := evaluate.CheckText(req.Text); err != nil {
			fmt.Fprintf(e.stderr, "applysmith evaluate: %v\n", err)
			return 2
		}
	}
	if req.Source == "cli" && byRow {
		req.Source = ""
	}
	root, err := e.workspacePath(*ws)
	if err != nil {
		return e.fail("evaluate", err)
	}
	if live, err := api.LockHeld(workspace.Open(root).StateDir()); err == nil && live {
		return e.evaluateLive(ctx, workspace.Open(root), req, rowID, *asJSON)
	}
	return e.withEvaluator(ctx, "evaluate", *ws, func(x evalEnv) int {
		var row *discovery.InboxRow
		if byRow {
			r, err := x.repo.InboxGet(ctx, rowID)
			if err != nil {
				return e.fail("evaluate", err)
			}
			row = &r
			req.FillFromInbox(r)
			if req.Source == "" {
				req.Source = r.Source
			}
		}
		req, err := evaluate.NormalizeRequest(req)
		if err != nil {
			return e.fail("evaluate", &usageError{strings.TrimPrefix(err.Error(), "evaluate: ")})
		}
		var res evaluate.Result
		run := func() (string, error) {
			var err error
			res, err = x.ev.RunNew(ctx, req)
			return res.RunID, err
		}
		// A pending row goes through the gate and queued, as a promotion does,
		// so that the finished evaluation can move it on.
		if row != nil && row.Status == discovery.InboxPending {
			_, rule, err := x.repo.QueueForEvaluation(ctx, *row, gateOf(x.discoveryDeps), run)
			if rule != "" {
				return e.fail("evaluate", fmt.Errorf("the eligibility gate keeps inbox row %d out of evaluation: %s", row.ID, rule))
			}
			if err != nil {
				return e.fail("evaluate", err)
			}
		} else if _, err := run(); err != nil {
			return e.fail("evaluate", err)
		}
		return e.printEvaluation(res, x.ws.Root, *asJSON, x.browser.noted())
	})
}

// printEvaluation prints how an evaluation ended and exits by the contract in
// docs/setup/reference.md: 3 when no model is set up, because a person must
// supply a provider and its key, 4 (an environment failure) when the posting
// needed the browser and the computer could not give one (fault, the read
// faultNoter kept), and 1 for any other failure. A failure that says what to
// do says it on standard error; --json keeps the result on standard output.
func (e *env) printEvaluation(res evaluate.Result, root string, asJSON bool, fault error) int {
	failed := res.Outcome == evaluate.OutcomeFailed
	noBrowser := failed && fault != nil && res.Reason == evaluate.ReasonUnreadable
	why := ""
	if failed {
		why = res.Explanation
	}
	// What the computer lacks (Chrome, a free browser profile) is said with the
	// browser's own message, which names the fix at a terminal: the executable
	// it searched, the process that holds the profile. The explanation words
	// the same for the dashboard and the announcements.
	if noBrowser && (why == "" || browser.EnvironmentFault(fault) && !browser.Unresponsive(fault)) {
		why = fmt.Sprintf("this posting is read in the browser, which could not be used (%v); fix that, or paste the posting text with --text or --file", fault)
	}
	// A missing model is set up at a terminal with a command; the explanation
	// names only the page the other front ends open (llm.NoModel).
	if failed && res.Reason == string(llm.KindNoRoute) {
		why = "no model is set up for evaluation; run `applysmith llm configure --preset <name> --key-stdin` (or `--cli codex|claude`), or add a key in Settings, Models"
	}
	if why != "" {
		fmt.Fprintf(e.stderr, "applysmith evaluate: %s\n", why)
	}
	switch {
	case asJSON:
		e.printJSON(api.WireEvaluation(res, api.EvaluationStatus(res), ""))
	case why == "":
		printResult(e, res, root)
	}
	switch {
	case !failed:
		return 0
	case res.Reason == string(llm.KindNoRoute) || res.Reason == string(llm.KindUnconfigured):
		return 3
	case noBrowser:
		return 4
	}
	return 1
}

func printResult(e *env, r evaluate.Result, root string) {
	fmt.Fprintf(e.stdout, "%s", r.Outcome)
	if r.ReportNumber > 0 {
		fmt.Fprintf(e.stdout, " report %04d", r.ReportNumber)
	}
	if r.Score != nil {
		fmt.Fprintf(e.stdout, " score %.1f", *r.Score)
	}
	if r.Recommendation != "" {
		fmt.Fprintf(e.stdout, " (%s)", r.Recommendation)
	}
	if r.Reason != "" {
		fmt.Fprintf(e.stdout, " reason %s", r.Reason)
	}
	fmt.Fprintln(e.stdout)
	if files, _ := filepath.Glob(filepath.Join(root, "reports", fmt.Sprintf("%04d-*.md", r.ReportNumber))); len(files) > 0 {
		fmt.Fprintf(e.stdout, "report: %s\n", files[len(files)-1])
	}
}

func (e *env) prompts(args []string) int {
	fs := e.flagsWithUsage("prompts", evaluateUsage)
	ws := fs.String("workspace", "", "workspace directory")
	pos, err := parseMixed(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) == 0 {
		fmt.Fprint(e.stderr, evaluateUsage)
		return 2
	}
	root, err := e.workspacePath(*ws)
	if err != nil {
		return e.fail("prompts", err)
	}
	set := prompts.New(filepath.Join(root, "prompts"))
	switch pos[0] {
	case "list":
		for _, n := range set.Names() {
			_, override, err := set.Source(n)
			if err != nil {
				return e.fail("prompts", err)
			}
			state := "embedded"
			if override {
				state = "override"
			}
			fmt.Fprintf(e.stdout, "%s\t%s\n", n, state)
		}
	case "show", "diff":
		if len(pos) != 2 {
			fmt.Fprint(e.stderr, evaluateUsage)
			return 2
		}
		if pos[0] == "show" {
			text, _, err := set.Source(pos[1])
			if err != nil {
				return e.fail("prompts", err)
			}
			fmt.Fprint(e.stdout, text)
		} else {
			d, err := set.Diff(pos[1])
			if err != nil {
				return e.fail("prompts", err)
			}
			fmt.Fprint(e.stdout, d)
		}
	case "check":
		errs := set.Check()
		for _, err := range errs {
			fmt.Fprintln(e.stderr, err)
		}
		if len(errs) > 0 {
			return 1
		}
		fmt.Fprintln(e.stdout, "prompts ok")
	default:
		fmt.Fprint(e.stderr, evaluateUsage)
		return 2
	}
	return 0
}
