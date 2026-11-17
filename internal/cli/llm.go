package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"applysmith/internal/api"
	"applysmith/internal/api/modelsapi"
	"applysmith/internal/api/wire"
	"applysmith/internal/llm"
	"applysmith/internal/onboarding"
)

const llmUsage = `usage:
  applysmith llm show [--json]
  applysmith llm configure --preset <name> [--key-stdin] [--fallback <preset>]
  applysmith llm configure --cli codex|claude
  applysmith llm keys set <provider>      the key is read from standard input or a hidden prompt, never from an argument
  applysmith llm keys clear <provider>
  applysmith llm signin chatgpt [--open]    sign in with your ChatGPT plan; the running server finishes the sign-in
  applysmith llm signout chatgpt [--forget]    sign out of ChatGPT and revoke the token
  applysmith llm set <task> <provider>/<model> [--thinking level] [--max-tokens n] [--timeout seconds] [--first-token seconds] [--fallback provider/model,...]
  applysmith llm group set <name> --mode order|rotate|usage --members provider/model,provider/model
  applysmith llm group rm <name>          a task row names a group as group/<name>, for example: applysmith llm set narrative group/<name>
  applysmith llm reset <task>
  applysmith llm budget <task> <usd>      the task's own daily spend limit; 0 removes it
  applysmith llm cap <usd>                the day's spend cap for every task; 0 turns it off
  applysmith llm test [provider] [--model <id>] [--json]   --model tries a model other than the provider's usual one
  applysmith llm bench option-pick --candidates provider/model,provider/model [--cases file.json] [--apply] [--json]
  applysmith llm cost [--days n]
  applysmith llm prices update
  applysmith llm debug-capture on [--minutes n] | off | status

at the cap, or at a task's budget for that task, calls move to a cheaper model, and unattended
evaluation (jd-extract, quick-screen, deep-eval) waits for the next day; a day is this computer's
local day, and amounts are US dollars.
every command takes --workspace <dir>
`

const keyArgRefused = "a key must not be passed as a command-line argument, because arguments appear in process listings and shell history; use --key-stdin or the hidden prompt"

func (e *env) llm(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(e.stderr, llmUsage)
		return 2
	}
	cmds := map[string]func(context.Context, []string) int{
		"show": e.llmShow, "configure": e.llmConfigure, "keys": e.llmKeys, "signin": e.llmSignin, "signout": e.llmSignout, "set": e.llmSet, "group": e.llmGroup, "reset": e.llmReset, "budget": e.llmBudget, "cap": e.llmCap,
		"test": e.llmTest, "bench": e.llmBench, "cost": e.llmCost, "prices": e.llmPrices, "debug-capture": e.llmDebugCapture,
	}
	if run, ok := cmds[args[0]]; ok {
		return run(ctx, args[1:])
	}
	fmt.Fprint(e.stderr, llmUsage)
	return 2
}

func (e *env) needRouter(cmd string, oe *onboarding.Env) bool {
	if oe.Router == nil || oe.Store == nil {
		e.report(cmd, &onboarding.EnvError{Message: "the workspace database does not exist yet; run `applysmith setup` or `applysmith init` first"})
		return false
	}
	return true
}

func (e *env) llmShow(ctx context.Context, args []string) int {
	fs := e.flags("llm show")
	wsFlag := fs.String("workspace", "", "workspace directory")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return 2
	}
	return e.withRead(ctx, "llm show", *wsFlag, func(oe *onboarding.Env) int {
		if oe.Router == nil && !*asJSON {
			fmt.Fprintln(e.stdout, "no provider is configured; run `applysmith llm configure --preset <name> --key-stdin`")
			return 0
		}
		if !e.needRouter("llm show", oe) {
			return 4
		}
		table, err := oe.Router.Show(ctx)
		if err != nil {
			return e.report("llm show", err)
		}
		if *asJSON {
			view, err := modelsapi.RoutingView(ctx, oe.Store, oe.Router, func(ids []string) map[string]bool {
				out := map[string]bool{}
				for _, id := range ids {
					_, err := oe.Vault.Test(llm.KeyName(id))
					out[id] = err == nil
				}
				return out
			})
			if err != nil {
				return e.report("llm show", err)
			}
			return e.printJSON(view)
		}
		tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
		if len(table.Rows) > 0 {
			fmt.Fprintln(tw, "TASK\tMODEL\tTHINKING\tSOURCE\tFALLBACKS")
		}
		agentRouted, lightRouted := false, false
		for _, r := range table.Rows {
			agentRouted = agentRouted || r.Task.ID == llm.AgentTask
			lightRouted = lightRouted || r.Task.ID == llm.AgentLightTask
			var fb []string
			for _, f := range r.Fallbacks {
				fb = append(fb, f.Provider+"/"+f.Model)
			}
			fmt.Fprintf(tw, "%s\t%s/%s\t%s\t%s\t%s\n", r.Task.ID, r.Provider, r.Model, r.Thinking, r.Source, strings.Join(fb, ","))
		}
		tw.Flush()
		s, err := llm.LoadSettings(ctx, oe.Store)
		if err != nil {
			return e.report("llm show", err)
		}
		for _, name := range slices.Sorted(maps.Keys(s.Groups)) {
			g := s.Groups[name]
			var members []string
			for _, m := range g.Members {
				members = append(members, m.Provider+"/"+m.Model)
			}
			fmt.Fprintf(e.stdout, "group/%s (%s): %s\n", name, g.Mode, strings.Join(members, ","))
		}
		if agentRouted {
			fmt.Fprintf(e.stdout, "%s: the gateway walks the fallbacks on the primary's wire, and pi tries the ones on another wire after them\n", llm.AgentTask)
		}
		switch {
		case lightRouted:
			fmt.Fprintf(e.stdout, "%s: routed, so intent routing is on: %s reads each new Ask Agent message and light turns run here, heavy turns on %s\n", llm.AgentLightTask, llm.IntentTask, llm.AgentTask)
		case len(table.Rows) > 0:
			fmt.Fprintf(e.stdout, "%s: not routed; route it with `applysmith llm set %s <provider>/<model>` to turn intent routing on\n", llm.AgentLightTask, llm.AgentLightTask)
		}
		// One line per reason, not one per unrouted task.
		byWhy := map[string][]string{}
		for id, why := range table.Unrouted {
			byWhy[why] = append(byWhy[why], id)
		}
		for _, why := range slices.Sorted(maps.Keys(byWhy)) {
			fmt.Fprintf(e.stdout, "unrouted (%s): %s\n", why, strings.Join(slices.Sorted(slices.Values(byWhy[why])), ", "))
		}
		if len(table.Unrouted) > 0 {
			fmt.Fprintln(e.stdout, "route them with `applysmith llm configure --preset <name> --key-stdin`")
		}
		for _, id := range slices.Sorted(maps.Keys(table.Broken)) {
			fmt.Fprintf(e.stdout, "broken: %s: %v\n", id, table.Broken[id])
		}
		if s.DailyUSDCap > 0 {
			fmt.Fprintf(e.stdout, "daily cap: $%.2f\n", s.DailyUSDCap)
		} else {
			fmt.Fprintln(e.stdout, "daily cap: off")
		}
		var budgets []string
		for _, id := range slices.Sorted(maps.Keys(s.Budgets)) {
			budgets = append(budgets, fmt.Sprintf("%s $%.2f", id, s.Budgets[id]))
		}
		if len(budgets) > 0 {
			fmt.Fprintf(e.stdout, "daily budgets: %s\n", strings.Join(budgets, ", "))
		}
		return 0
	})
}

// usdArg reads an amount in US dollars; the settings judge its range.
func usdArg(text string) (float64, error) {
	usd, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(usd) || math.IsInf(usd, 0) {
		return 0, &onboarding.RejectedError{Message: fmt.Sprintf("%q is not an amount in US dollars, such as 1.50", text)}
	}
	return usd, nil
}

func (e *env) llmBudget(ctx context.Context, args []string) int {
	fs := e.flags("llm budget")
	wsFlag := fs.String("workspace", "", "workspace directory")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 2 {
		fmt.Fprint(e.stderr, llmUsage)
		return 2
	}
	usd, err := usdArg(pos[1])
	if err == nil {
		err = llm.ValidateBudget(pos[0], usd)
	}
	if err != nil {
		return e.report("llm budget", &onboarding.RejectedError{Message: err.Error()})
	}
	said := fmt.Sprintf("%s moves to a cheaper model once it has spent $%.2f in a day", pos[0], usd)
	if usd == 0 {
		said = pos[0] + " has no daily budget; the daily cap still applies"
	}
	return e.routingWrite(ctx, "llm budget", *wsFlag, wire.LLMRoutingPatch{Budgets: map[string]float64{pos[0]: usd}},
		func(r *llm.Router) error { return r.SetBudgets(ctx, map[string]float64{pos[0]: usd}) }, said)
}

func (e *env) llmCap(ctx context.Context, args []string) int {
	fs := e.flags("llm cap")
	wsFlag := fs.String("workspace", "", "workspace directory")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(e.stderr, llmUsage)
		return 2
	}
	usd, err := usdArg(pos[0])
	if err != nil {
		return e.report("llm cap", err)
	}
	said := fmt.Sprintf("once $%.2f is spent in a day, calls move to a cheaper model", usd)
	if usd == 0 {
		said = "the daily cap is off; task budgets still apply"
	}
	return e.routingWrite(ctx, "llm cap", *wsFlag, wire.LLMRoutingPatch{DailyUSDCap: &usd},
		func(r *llm.Router) error { return r.SetDailyCap(ctx, usd) }, said)
}

// readSecret reads one secret from standard input; on a terminal it is hidden.
func (e *env) readSecret(prompt string) (string, error) {
	if f, ok := e.in().(interface{ Fd() uintptr }); ok && e.isTerminal() {
		fmt.Fprint(e.stderr, prompt+" (not shown): ")
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(e.stderr)
		return strings.TrimSpace(string(b)), err
	}
	b, err := io.ReadAll(io.LimitReader(e.in(), 64<<10))
	return strings.TrimSpace(string(b)), err
}

func (e *env) llmConfigure(ctx context.Context, args []string) int {
	fs := e.flags("llm configure")
	wsFlag := fs.String("workspace", "", "workspace directory")
	preset := fs.String("preset", "", "provider preset: "+strings.Join(apiPresetNames(), ", "))
	cli := fs.String("cli", "", "command-line tool: codex or claude")
	keyStdin := fs.Bool("key-stdin", false, "read the API key from standard input")
	fallback := fs.String("fallback", "", "preset whose model becomes the first fallback")
	key := fs.String("key", "", "refused: a key is never accepted as an argument")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return 2
	}
	if *key != "" {
		fmt.Fprintf(e.stderr, "applysmith llm configure: %s\n", keyArgRefused)
		return 2
	}
	if (*preset == "") == (*cli == "") {
		fmt.Fprint(e.stderr, "applysmith llm configure: give exactly one of --preset or --cli\n"+llmUsage)
		return 2
	}
	if *cli != "" && (*keyStdin || *fallback != "") {
		fmt.Fprint(e.stderr, "applysmith llm configure: --key-stdin and --fallback go with --preset; a command-line tool signs in on its own\n"+llmUsage)
		return 2
	}
	return e.withWrite(ctx, "llm configure", *wsFlag, func(oe *onboarding.Env) int {
		if !e.needRouter("llm configure", oe) {
			return 4
		}
		name := *preset
		if *cli != "" {
			name = *cli
			if *cli != "codex" && *cli != "claude" {
				return e.report("llm configure", &onboarding.RejectedError{Message: "--cli must be codex or claude"})
			}
			fmt.Fprintln(e.stdout, onboarding.CLILimits)
		} else {
			p, ok := llm.PresetByName(name)
			if ok && p.Kind == llm.KindCLI {
				return e.report("llm configure", &onboarding.RejectedError{Message: fmt.Sprintf("%q is a command-line tool: use --cli %s", name, name)})
			}
			if !ok {
				return e.report("llm configure", &onboarding.RejectedError{Message: fmt.Sprintf("unknown preset %q; valid presets: %s", name, strings.Join(apiPresetNames(), ", "))})
			}
			if *keyStdin {
				v, err := e.readSecret(p.Label + " API key")
				if err != nil {
					return e.report("llm configure", &onboarding.EnvError{Message: "the key could not be read", Err: err})
				}
				if err := onboarding.SaveProviderKey(ctx, oe, name, v); err != nil {
					return e.report("llm configure", err)
				}
			}
		}
		if err := oe.Router.ConfigurePreset(ctx, name, llm.ConfigureOptions{Fallback: *fallback}); err != nil {
			var se *llm.SetupError
			if errors.As(err, &se) {
				return e.report("llm configure", &onboarding.HumanError{Instruction: se.Message})
			}
			return e.report("llm configure", &onboarding.RejectedError{Message: err.Error()})
		}
		fmt.Fprintf(e.stdout, "preset %s is active; run `applysmith llm test` to prove it\n", name)
		return 0
	})
}

func (e *env) llmKeys(ctx context.Context, args []string) int {
	if len(args) < 2 || (args[0] != "set" && args[0] != "clear") {
		fmt.Fprint(e.stderr, llmUsage)
		return 2
	}
	action := args[0]
	fs := e.flags("llm keys " + action)
	wsFlag := fs.String("workspace", "", "workspace directory")
	pos, err := parseMixed(fs, args[1:])
	if err != nil || len(pos) < 1 {
		return 2
	}
	if len(pos) > 1 {
		fmt.Fprintf(e.stderr, "applysmith llm keys %s: %s\n", action, keyArgRefused)
		return 2
	}
	provider := pos[0]
	return e.withWrite(ctx, "llm keys "+action, *wsFlag, func(oe *onboarding.Env) int {
		if !e.needRouter("llm keys "+action, oe) {
			return 4
		}
		if action == "clear" {
			if err := oe.Router.ClearKey(provider); err != nil {
				return e.report("llm keys clear", &onboarding.EnvError{Message: "the key could not be removed", Err: err})
			}
			fmt.Fprintf(e.stdout, "removed the key of %s\n", provider)
			return 0
		}
		v, err := e.readSecret("API key for " + provider)
		if err != nil {
			return e.report("llm keys set", &onboarding.EnvError{Message: "the key could not be read", Err: err})
		}
		if err := onboarding.SaveProviderKey(ctx, oe, provider, v); err != nil {
			return e.report("llm keys set", err)
		}
		fmt.Fprintf(e.stdout, "stored the key of %s (present, not shown)\n", provider)
		return 0
	})
}

func (e *env) llmSet(ctx context.Context, args []string) int {
	fs := e.flags("llm set")
	wsFlag := fs.String("workspace", "", "workspace directory")
	thinking := fs.String("thinking", "", "thinking level")
	maxTokens := fs.Int("max-tokens", 0, "output ceiling")
	timeout := fs.Int("timeout", 0, "timeout in seconds")
	firstToken := fs.Int("first-token", 0, "abandon a streaming attempt whose first stream event is later than this many seconds (0 = off)")
	fallbacks := fs.String("fallback", "", "comma-separated provider/model fallbacks")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 2 {
		fmt.Fprint(e.stderr, llmUsage)
		return 2
	}
	prov, model, ok := strings.Cut(pos[1], "/")
	if !ok || prov == "" || model == "" {
		return e.report("llm set", &onboarding.RejectedError{Message: "the target is provider/model"})
	}
	row := llm.Route{Provider: prov, Model: model, Thinking: *thinking, MaxOutputTokens: *maxTokens, TimeoutSeconds: *timeout, FirstTokenSeconds: *firstToken}
	wr := wire.LLMRoute{Provider: prov, Model: model, Thinking: *thinking, MaxOutputTokens: wire.SafeInt(*maxTokens), TimeoutSeconds: wire.SafeInt(*timeout), FirstTokenSeconds: wire.SafeInt(*firstToken)}
	for _, f := range strings.Split(*fallbacks, ",") {
		if f = strings.TrimSpace(f); f != "" {
			p, m, ok := strings.Cut(f, "/")
			if !ok {
				return e.report("llm set", &onboarding.RejectedError{Message: "a fallback is provider/model"})
			}
			row.Fallbacks = append(row.Fallbacks, llm.Fallback{Provider: p, Model: m})
			wr.Fallbacks = append(wr.Fallbacks, wire.LLMFallback{Provider: p, Model: m})
		}
	}
	return e.routingWrite(ctx, "llm set", *wsFlag, wire.LLMRoutingPatch{Routes: map[string]*wire.LLMRoute{pos[0]: &wr}},
		func(r *llm.Router) error { return r.PatchRoutes(ctx, map[string]*llm.Route{pos[0]: &row}) }, fmt.Sprintf("%s now uses %s", pos[0], pos[1]))
}

// routingWrite makes a routing change and prints said. While a server holds
// the workspace, which the command cannot take beside it, the change goes to
// that server the way Settings, Models sends it; otherwise it is made here.
func (e *env) routingWrite(ctx context.Context, cmd, wsFlag string, patch wire.LLMRoutingPatch, local func(*llm.Router) error, said string) int {
	if ws, err := e.openWorkspace(wsFlag); err == nil {
		if held, err := api.LockHeld(ws.StateDir()); err == nil && held {
			srv, err := e.attachLive(ctx, ws)
			if err != nil {
				return e.fail(cmd, err)
			}
			var out wire.LLMRouting
			if err := e.liveSend(ctx, srv, http.MethodPatch, "/api/llm/routing", patch, &out); err != nil {
				return e.fail(cmd, err)
			}
			fmt.Fprintln(e.stdout, said)
			return 0
		}
	}
	return e.withWrite(ctx, cmd, wsFlag, func(oe *onboarding.Env) int {
		if !e.needRouter(cmd, oe) {
			return 4
		}
		if err := local(oe.Router); err != nil {
			return e.report(cmd, &onboarding.RejectedError{Message: err.Error()})
		}
		fmt.Fprintln(e.stdout, said)
		return 0
	})
}

func (e *env) llmGroup(ctx context.Context, args []string) int {
	if len(args) > 0 && (args[0] == "set" || args[0] == "rm") {
		fs := e.flags("llm group " + args[0])
		wsFlag := fs.String("workspace", "", "workspace directory")
		mode := fs.String("mode", "", "order, rotate or usage")
		members := fs.String("members", "", "comma-separated provider/model members")
		pos, err := parseMixed(fs, args[1:])
		if err != nil || len(pos) != 1 || args[0] == "rm" && (*mode != "" || *members != "") {
			fmt.Fprint(e.stderr, llmUsage)
			return 2
		}
		name := pos[0]
		var patch *llm.Group
		var wg *wire.LLMGroup
		said := "group/" + name + " is deleted"
		if args[0] == "set" {
			patch, wg = &llm.Group{Mode: *mode}, &wire.LLMGroup{Mode: *mode}
			for _, m := range strings.Split(*members, ",") {
				if m = strings.TrimSpace(m); m != "" {
					p, model, ok := strings.Cut(m, "/")
					if !ok {
						return e.report("llm group set", &onboarding.RejectedError{Message: "a member is provider/model"})
					}
					patch.Members = append(patch.Members, llm.Fallback{Provider: p, Model: model})
					wg.Members = append(wg.Members, wire.LLMFallback{Provider: p, Model: model})
				}
			}
			said = fmt.Sprintf("group/%s spreads calls over %d models (%s); route a task to it with `applysmith llm set <task> group/%s`", name, len(patch.Members), patch.Mode, name)
		}
		return e.routingWrite(ctx, "llm group "+args[0], *wsFlag, wire.LLMRoutingPatch{Groups: map[string]*wire.LLMGroup{name: wg}},
			func(r *llm.Router) error { return r.PatchRouting(ctx, nil, map[string]*llm.Group{name: patch}) }, said)
	}
	fmt.Fprint(e.stderr, llmUsage)
	return 2
}

func (e *env) llmReset(ctx context.Context, args []string) int {
	fs := e.flags("llm reset")
	wsFlag := fs.String("workspace", "", "workspace directory")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(e.stderr, llmUsage)
		return 2
	}
	return e.routingWrite(ctx, "llm reset", *wsFlag, wire.LLMRoutingPatch{Routes: map[string]*wire.LLMRoute{pos[0]: nil}},
		func(r *llm.Router) error { return r.PatchRoutes(ctx, map[string]*llm.Route{pos[0]: nil}) }, pos[0]+" follows the active preset again")
}

func (e *env) llmTest(ctx context.Context, args []string) int {
	fs := e.flags("llm test")
	wsFlag := fs.String("workspace", "", "workspace directory")
	model := fs.String("model", "", "the model id to test (default: the provider's usual model)")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) > 1 {
		return 2
	}
	provider := ""
	if len(pos) == 1 {
		provider = pos[0]
	}
	if ws, err := e.openWorkspace(*wsFlag); err == nil {
		if live, err := api.LockHeld(ws.StateDir()); err == nil && live {
			srv, err := e.attachLive(ctx, ws)
			if err != nil {
				return e.fail("llm test", err)
			}
			lt, err := e.liveProviderTest(ctx, srv, provider, *model)
			if err != nil {
				return e.fail("llm test", err)
			}
			return e.printTest(llm.TestResult{Provider: lt.Provider, Model: lt.Model, Outcome: lt.Outcome, ErrorClass: lt.ErrorClass,
				Fix: lt.Fix, LatencyMS: int(lt.LatencyMs)}, lt, *asJSON)
		}
	}
	return e.withWrite(ctx, "llm test", *wsFlag, func(oe *onboarding.Env) int {
		if !e.needRouter("llm test", oe) {
			return 4
		}
		res, err := oe.Router.Test(ctx, provider, *model)
		var se *llm.SetupError
		if errors.As(err, &se) {
			return e.report("llm test", &onboarding.HumanError{Instruction: se.Message})
		}
		if err != nil {
			return e.report("llm test", err)
		}
		return e.printTest(res, modelsapi.WireTestResult(res), *asJSON)
	})
}

func (e *env) printTest(res llm.TestResult, w *wire.LLMTestResult, asJSON bool) int {
	if asJSON {
		e.printJSON(w)
	} else {
		fmt.Fprintln(e.stdout, onboarding.TestLine(res))
	}
	if !res.OK() {
		return 1
	}
	return 0
}

// liveProviderTest runs the test in the server that holds the workspace and
// waits for its result; the command cannot take the workspace beside it.
func (e *env) liveProviderTest(ctx context.Context, srv *liveServer, provider, model string) (*wire.LLMTestResult, error) {
	var before wire.LLMRouting
	if err := e.liveCall(ctx, srv, http.MethodGet, "/api/llm/routing", &before); err != nil {
		return nil, err
	}
	if provider == "" {
		provider = before.Preset
	}
	if provider == "" {
		return nil, errors.New("no provider is configured; apply a preset in Settings, Models")
	}
	var started wire.LLMTestStarted
	if err := e.liveSend(ctx, srv, http.MethodPost, "/api/llm/providers/"+url.PathEscape(provider)+"/test", wire.LLMTestRequest{Model: model}, &started); err != nil {
		return nil, err
	}
	var quiet silence
	for {
		select {
		case <-ctx.Done():
			return nil, errors.New("interrupted; the test keeps running in the server")
		case <-time.After(evaluatePoll):
		}
		var v wire.LLMRouting
		err := e.liveCall(ctx, srv, http.MethodGet, "/api/llm/routing", &v)
		if !quiet.tolerates(ctx, e, err) {
			return nil, err
		}
		if err != nil || v.Testing != "" {
			continue
		}
		if v.LastTest == nil || before.LastTest != nil && v.LastTest.TestedAt.Equal(before.LastTest.TestedAt.Time) {
			return nil, errors.New("the test ended without a result; the server log says why")
		}
		return v.LastTest, nil
	}
}

// llmBench replays cases through each candidate route and prints the table of
// results, best first. With --apply the winner becomes the task's primary.
func (e *env) llmBench(ctx context.Context, args []string) int {
	fs := e.flags("llm bench")
	wsFlag := fs.String("workspace", "", "workspace directory")
	cands := fs.String("candidates", "", "comma-separated provider/model candidates")
	casesFile := fs.String("cases", "", "JSON file of cases (default: the built-in invented set)")
	apply := fs.Bool("apply", false, "route the winner as the task's primary")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 || *cands == "" {
		fmt.Fprint(e.stderr, llmUsage)
		return 2
	}
	var list []llm.Fallback
	for _, c := range strings.Split(*cands, ",") {
		p, m, ok := strings.Cut(strings.TrimSpace(c), "/")
		if !ok || p == "" || m == "" {
			return e.report("llm bench", &onboarding.RejectedError{Message: "a candidate is provider/model"})
		}
		list = append(list, llm.Fallback{Provider: p, Model: m})
	}
	cases := llm.DefaultBenchCases()
	if *casesFile != "" {
		raw, err := os.ReadFile(*casesFile)
		if err == nil {
			cases, err = llm.ParseBenchCases(raw)
		}
		if err != nil {
			return e.report("llm bench", &onboarding.RejectedError{Message: err.Error()})
		}
	}
	return e.withWrite(ctx, "llm bench", *wsFlag, func(oe *onboarding.Env) int {
		if !e.needRouter("llm bench", oe) {
			return 4
		}
		fmt.Fprintf(e.stderr, "bench %s: %d cases x %d candidates = %d model calls\n", pos[0], len(cases), len(list), len(cases)*len(list))
		run, err := oe.Router.Bench(ctx, pos[0], list, cases)
		if err != nil {
			return e.report("llm bench", &onboarding.RejectedError{Message: err.Error()})
		}
		var won *llm.Fallback
		var applyErr error
		if *apply {
			w, err := oe.Router.ApplyBench(ctx, run)
			if err != nil {
				applyErr = err
			} else {
				won = &w
			}
		}
		if *asJSON {
			e.printJSON(modelsapi.WireBench(run))
		} else {
			tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "CANDIDATE\tCORRECT\tWRONG\tNO ANSWER\tP50\tP95\tCOST USD")
			for i, r := range run.Results {
				cost := "unpriced"
				if r.CostUSD != nil {
					cost = fmt.Sprintf("%.4f", *r.CostUSD)
				}
				mark := ""
				if i == 0 {
					mark = " *"
				}
				fmt.Fprintf(tw, "%s/%s%s\t%d/%d\t%d\t%d\t%dms\t%dms\t%s\n", r.Provider, r.Model, mark, r.Correct, run.Cases, r.Wrong, r.NoAnswer, r.P50Ms, r.P95Ms, cost)
			}
			tw.Flush()
			fmt.Fprintln(e.stdout, "* best: fewest wrong, then most correct, then lowest p50, then lowest cost")
		}
		if applyErr != nil {
			return e.report("llm bench", &onboarding.RejectedError{Message: applyErr.Error()})
		}
		if won != nil {
			fmt.Fprintf(e.stdout, "%s now uses %s/%s\n", pos[0], won.Provider, won.Model)
		}
		return 0
	})
}

func (e *env) llmCost(ctx context.Context, args []string) int {
	fs := e.flags("llm cost")
	wsFlag := fs.String("workspace", "", "workspace directory")
	days := fs.Int("days", 7, "days to report")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 || *days < 1 {
		return 2
	}
	return e.withRead(ctx, "llm cost", *wsFlag, func(oe *onboarding.Env) int {
		if oe.Router == nil {
			fmt.Fprintln(e.stdout, "no model call has been recorded")
			return 0
		}
		byTask, byProvider, err := oe.Router.CostReport(ctx, *days)
		if err != nil {
			return e.report("llm cost", err)
		}
		for _, part := range []struct {
			name string
			rows []llm.ReportRow
		}{{"task", byTask}, {"provider", byProvider}} {
			tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintf(tw, "%s\tCALLS\tSUCCESS\tFALLBACK\tIN\tOUT\tCACHED\tWASTED\tRUNS\tPER RUN\tCOST USD\tUNPRICED\tP50\tP95\n", strings.ToUpper(part.name))
			var in, out, wasted int
			for _, r := range part.rows {
				fmt.Fprintf(tw, "%s\t%d\t%.0f%%\t%.0f%%\t%s\t%s\t%s\t%s\t%d\t%s\t%.4f\t%d\t%s\t%s\n", r.Name, r.Calls, r.SuccessRate*100, r.FallbackRate*100,
					tokenCount(r.InputTokens), tokenCount(r.OutputTokens), tokenCount(r.CachedTokens), tokenCount(r.WastedTokens), r.Runs, tokenCount(r.PerRunTokens()),
					r.CostUSD, r.Unpriced, r.P50.Round(time.Millisecond), r.P95.Round(time.Millisecond))
				in, out, wasted = in+r.InputTokens, out+r.OutputTokens, wasted+r.WastedTokens
			}
			fmt.Fprintf(tw, "total\t\t\t\t%s\t%s\t\t%s\n", tokenCount(in), tokenCount(out), tokenCount(wasted))
			tw.Flush()
		}
		return 0
	})
}

// tokenCount is a token total as the cost report shows it: exact under ten
// thousand, then in thousands or millions.
func tokenCount(n int) string {
	switch {
	case n < 10_000:
		return strconv.Itoa(n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprintf("%.2fM", float64(n)/1e6)
}

func (e *env) llmPrices(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] != "update" {
		fmt.Fprint(e.stderr, llmUsage)
		return 2
	}
	fs := e.flags("llm prices update")
	wsFlag := fs.String("workspace", "", "workspace directory")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
		return 2
	}
	return e.withWrite(ctx, "llm prices update", *wsFlag, func(oe *onboarding.Env) int {
		if !e.needRouter("llm prices update", oe) {
			return 4
		}
		changes, err := oe.Router.UpdatePrices(ctx)
		if err != nil {
			return e.report("llm prices update", &onboarding.EnvError{Message: "the price catalog could not be fetched", Err: err})
		}
		if len(changes) == 0 {
			fmt.Fprintln(e.stdout, "no price changed")
		}
		for _, c := range changes {
			fmt.Fprintf(e.stdout, "%s: input %g -> %g, output %g -> %g\n", c.Key, c.Old.InputPerM, c.New.InputPerM, c.Old.OutputPerM, c.New.OutputPerM)
		}
		return 0
	})
}

// llmDebugCapture switches prompt and completion capture on for a bounded
// time, off, or reports its state. The server process reads the same setting.
func (e *env) llmDebugCapture(ctx context.Context, args []string) int {
	fs := e.flags("llm debug-capture")
	wsFlag := fs.String("workspace", "", "workspace directory")
	minutes := fs.Int("minutes", 15, "minutes to keep capture on, 1 to 120")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 || (pos[0] != "on" && pos[0] != "off" && pos[0] != "status") {
		fmt.Fprint(e.stderr, llmUsage)
		return 2
	}
	show := func(oe *onboarding.Env) int {
		if !e.needRouter("llm debug-capture", oe) {
			return 4
		}
		if until, on := oe.Router.DebugCaptureUntil(ctx); on {
			fmt.Fprintf(e.stdout, "debug capture: on until %s\n", until.Local().Format(time.RFC3339))
		} else {
			fmt.Fprintln(e.stdout, "debug capture: off")
		}
		return 0
	}
	if pos[0] == "status" {
		return e.withRead(ctx, "llm debug-capture", *wsFlag, show)
	}
	return e.withWrite(ctx, "llm debug-capture", *wsFlag, func(oe *onboarding.Env) int {
		if !e.needRouter("llm debug-capture", oe) {
			return 4
		}
		m := *minutes
		if pos[0] == "off" {
			m = 0
		} else if m < 1 {
			return e.report("llm debug-capture", &onboarding.RejectedError{Message: "debug capture lasts 1 to 120 minutes"})
		}
		if err := oe.Router.SetDebugCapture(ctx, m); err != nil {
			var se *llm.SetupError
			if errors.As(err, &se) {
				return e.report("llm debug-capture", &onboarding.RejectedError{Message: se.Message})
			}
			return e.report("llm debug-capture", err)
		}
		return show(oe)
	})
}

// apiPresetNames lists the presets --preset takes; the command-line tools are
// configured with --cli.
func apiPresetNames() []string {
	var out []string
	for _, n := range llm.PresetNames() {
		if p, _ := llm.PresetByName(n); p.Kind != llm.KindCLI {
			out = append(out, n)
		}
	}
	return out
}
