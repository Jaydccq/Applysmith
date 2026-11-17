package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"applysmith/internal/api/trackerapi"
	"applysmith/internal/evaluate"
	"applysmith/internal/llm"
	"applysmith/internal/messages"
	"applysmith/internal/prompts"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

// followupPolicy is the follow-up cadence in force: policy.yml when it is
// valid, else the defaults, with the reason on stderr.
func (e *env) followupPolicy(ws *workspace.Workspace) workspace.Followup {
	pol, bad := ws.FollowupPolicy()
	if bad != nil {
		fmt.Fprintf(e.stderr, "applysmith tracker: policy.yml is not valid (%s); using the default follow-up cadence\n", bad.String())
	}
	return pol
}

func (e *env) trackerFollowups(ctx context.Context, args []string) int {
	tf := e.trackerFlags("followups")
	due := tf.fs.Bool("due", false, "only the suggestions that are due today")
	if !e.trackerParse(tf, "followups", args) {
		return 2
	}
	return e.withTracker(ctx, "tracker followups", *tf.ws, accessRead, func(tr *tracker.Tracker, ws *workspace.Workspace) int {
		list, err := tr.Followups(ctx, e.followupPolicy(ws), *due)
		if err != nil {
			return e.fail("tracker followups", err)
		}
		if *tf.asJSON {
			return e.printJSON(trackerapi.WireFollowups(list))
		}
		for _, s := range list {
			fmt.Fprintln(e.stdout, tracker.FormatSuggestion(s))
		}
		if len(list) == 0 {
			fmt.Fprintln(e.stdout, "no follow-ups to suggest")
		}
		return 0
	})
}

// parseSnooze reads "3d".
func parseSnooze(text string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSuffix(text, "d"))
	if err != nil || n < 1 || !strings.HasSuffix(text, "d") {
		return 0, fmt.Errorf("%q is not a snooze; use a whole number of days such as 3d", text)
	}
	return n, nil
}

func (e *env) trackerFollowup(ctx context.Context, args []string) int {
	tf := e.trackerFlags("followup")
	snooze := tf.fs.String("snooze", "", "push the next follow-up by this many days, such as 3d")
	undo := tf.fs.Bool("undo", false, "take back your latest follow-up or snooze")
	pos, err := parseMixed(tf.fs, args)
	if err != nil {
		return 2
	}
	if *undo && *snooze != "" {
		return e.trackerRefuse("followup", "--snooze and --undo cannot be combined")
	}
	id, ok := e.idArg("followup", pos, 1)
	if !ok {
		return 2
	}
	days := 0
	if *snooze != "" {
		if days, err = parseSnooze(*snooze); err != nil {
			fmt.Fprintf(e.stderr, "applysmith tracker followup: %v\n", err)
			return 2
		}
	}
	return e.withTracker(ctx, "tracker followup", *tf.ws, accessShared, func(tr *tracker.Tracker, ws *workspace.Workspace) int {
		pol := e.followupPolicy(ws)
		var row tracker.Entry
		var err error
		msg := "recorded a follow-up on %s"
		switch {
		case *undo:
			msg = "took back your latest follow-up or snooze on %s"
			row, err = tr.UndoFollowup(ctx, id, tracker.ActorUser, 0)
		case days > 0:
			msg = fmt.Sprintf("snoozed the next follow-up on %%s by %d days", days)
			row, err = tr.SnoozeFollowup(ctx, id, tracker.ActorUser, days, 0, pol)
		default:
			row, err = tr.RecordFollowup(ctx, id, tracker.ActorUser, 0, pol)
		}
		if err != nil {
			return e.fail("tracker followup", err)
		}
		if *tf.asJSON {
			return e.printJSON(trackerapi.WireCandidacy(row))
		}
		fmt.Fprintf(e.stdout, msg+"\n", rowName(row))
		return 0
	})
}

const draftsUsage = `usage:
  applysmith outreach (--candidacy <id> | --company <name> [--role <title>]) --contact recruiter|hiring_manager|peer|interviewer [--name <n>] [--about <text>] [--date <d>] [--json]
  applysmith followup-draft --candidacy <id> [--kind recruiter_ping|status_check|thank_you] [--name <n>] [--context <text>] [--json]
  applysmith evaluate decode (--file <path> | --text <posting text>) [--title t] [--company c] [--location l] [--json]

outreach and followup-draft print a message for you to send yourself; nothing is sent or stored.
--about is what you know about the contact and --context what you remember of the last exchange;
they are the only sources about the person and the conversation.
evaluate decode reads a posting layer by layer against your profile; it is not a score and writes no report.
every command takes --workspace <dir> and works while the server runs
`

// draftEnv is the store, tracker and model router one drafting command uses.
type draftEnv struct {
	ws     *workspace.Workspace
	st     *store.Store
	tr     *tracker.Tracker
	router *llm.Router
}

// withDrafting opens the workspace database shared with a running server and
// routes model calls through the router, as the server does.
func (e *env) withDrafting(ctx context.Context, cmd, wsFlag string, fn func(draftEnv) int) int {
	root, err := e.workspacePath(wsFlag)
	if err != nil {
		return e.fail(cmd, err)
	}
	ws, st, release, err := e.openStore(ctx, root, accessShared)
	if err != nil {
		return e.fail(cmd, err)
	}
	defer release()
	id, err := ws.ID()
	if err != nil {
		return e.fail(cmd, err)
	}
	v, err := vault.New(id, e.backend, vault.WithGetenv(e.getenv))
	if err != nil {
		return e.fail(cmd, err)
	}
	tr, err := tracker.New(st, tracker.Options{})
	if err != nil {
		return e.fail(cmd, err)
	}
	router := llm.New(st, v, append([]llm.Option{llm.WithGetenv(e.getenv), llm.WithFake(evaluate.NewFake())}, e.llmOptions...)...)
	return fn(draftEnv{ws: ws, st: st, tr: tr, router: router})
}

func (x draftEnv) messages() (*messages.Service, error) {
	return messages.New(messages.Deps{Store: x.st, Tracker: x.tr, Workspace: x.ws, Model: x.router, Prompts: prompts.New(filepath.Join(x.ws.Root, "prompts"))})
}

func (e *env) outreachCmd(ctx context.Context, args []string) int {
	const cmd = "outreach"
	fs := e.flagsWithUsage(cmd, draftsUsage)
	ws := fs.String("workspace", "", "workspace directory")
	cand := fs.Int64("candidacy", 0, "tracker row id")
	company := fs.String("company", "", "company, when there is no tracker row")
	role := fs.String("role", "", "role, with --company")
	kind := fs.String("contact", "", "recruiter, hiring_manager, peer or interviewer")
	name := fs.String("name", "", "the contact's name")
	about := fs.String("about", "", "what you know about the contact")
	date := fs.String("date", "", "the meeting date, for an interviewer")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var problems []string
	if *kind == "" {
		problems = append(problems, "missing --contact recruiter|hiring_manager|peer|interviewer")
	}
	if (*cand == 0) == (*company == "") {
		problems = append(problems, "name the application with exactly one of --candidacy <id> or --company <name>")
	}
	if fs.NArg() > 0 {
		problems = append(problems, fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
	}
	if len(problems) > 0 {
		return e.refuseUsage(cmd, draftsUsage, problems...)
	}
	return e.withDrafting(ctx, cmd, *ws, func(x draftEnv) int {
		svc, err := x.messages()
		if err != nil {
			return e.fail(cmd, err)
		}
		d, err := svc.Outreach(ctx, messages.OutreachRequest{Candidacy: *cand, Company: *company, Role: *role,
			Contact: messages.Contact{Kind: *kind, Name: *name, Note: *about, Date: *date}})
		return e.printDraft(cmd, d, err, *asJSON)
	})
}

func (e *env) followupDraftCmd(ctx context.Context, args []string) int {
	const cmd = "followup-draft"
	fs := e.flagsWithUsage(cmd, draftsUsage)
	ws := fs.String("workspace", "", "workspace directory")
	cand := fs.Int64("candidacy", 0, "tracker row id")
	kind := fs.String("kind", "", "recruiter_ping, status_check or thank_you; default from the row's status")
	name := fs.String("name", "", "the contact's name")
	exchange := fs.String("context", "", "what you remember of the last exchange")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *cand < 1 || fs.NArg() > 0 {
		problems := []string{"missing --candidacy <id>"}
		if *cand >= 1 {
			problems = []string{fmt.Sprintf("unexpected argument %q", fs.Arg(0))}
		}
		return e.refuseUsage(cmd, draftsUsage, problems...)
	}
	return e.withDrafting(ctx, cmd, *ws, func(x draftEnv) int {
		svc, err := x.messages()
		if err != nil {
			return e.fail(cmd, err)
		}
		d, err := svc.Followup(ctx, messages.FollowupRequest{Candidacy: *cand, Kind: *kind, Contact: *name, Context: *exchange})
		return e.printDraft(cmd, d, err, *asJSON)
	})
}

// printDraft prints a draft, or the refusal: a request the drafts cannot work
// from is a usage error (2), a cadence refusal needs the user (3) and a reply
// no message of which passed the checks is a failure (1).
func (e *env) printDraft(cmd string, d messages.Draft, err error, asJSON bool) int {
	switch {
	case errors.Is(err, messages.ErrInvalid), errors.Is(err, messages.ErrNotFound):
		fmt.Fprintf(e.stderr, "applysmith %s: %v\n", cmd, err)
		return 2
	case errors.Is(err, messages.ErrCadence):
		fmt.Fprintf(e.stderr, "applysmith %s: %v\n", cmd, err)
		return 3
	case err != nil:
		return e.fail(cmd, err)
	}
	if asJSON {
		return e.printJSON(d)
	}
	fmt.Fprint(e.stdout, d.Text())
	return 0
}

func (e *env) evaluateDecode(ctx context.Context, args []string) int {
	const cmd = "evaluate decode"
	fs := e.flagsWithUsage(cmd, draftsUsage)
	ws := fs.String("workspace", "", "workspace directory")
	file := fs.String("file", "", "read the posting text from a file")
	text := fs.String("text", "", "the posting text")
	title := fs.String("title", "", "title hint")
	company := fs.String("company", "", "company hint")
	location := fs.String("location", "", "location hint")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 || (*file == "") == (*text == "") {
		problems := []string{"give the posting with exactly one of --file <path> or --text <text>"}
		if fs.NArg() > 0 {
			problems = []string{fmt.Sprintf("unexpected argument %q", fs.Arg(0))}
		}
		return e.refuseUsage(cmd, draftsUsage, problems...)
	}
	req := evaluate.DecodeRequest{Text: *text, Title: *title, Company: *company, Location: *location}
	if *file != "" {
		b, err := os.ReadFile(*file)
		if err != nil {
			return e.fail(cmd, err)
		}
		req.Text = string(b)
	}
	return e.withDrafting(ctx, cmd, *ws, func(x draftEnv) int {
		ev, err := evaluate.New(evaluate.Deps{Store: x.st, Tracker: x.tr, Model: x.router, Prompts: prompts.New(filepath.Join(x.ws.Root, "prompts")),
			Profile: func(context.Context) (evaluate.Profile, error) { return evaluate.LoadProfile(x.ws) }, Root: x.ws.Root})
		if err != nil {
			return e.fail(cmd, err)
		}
		d, err := ev.Decode(ctx, req)
		if errors.Is(err, evaluate.ErrDecodeInput) {
			fmt.Fprintf(e.stderr, "applysmith %s: %v\n", cmd, err)
			return 2
		}
		if err != nil {
			return e.fail(cmd, err)
		}
		if *asJSON {
			return e.printJSON(d)
		}
		fmt.Fprint(e.stdout, d.Markdown())
		return 0
	})
}
