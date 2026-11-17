package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"applysmith/internal/kb"
)

// showTyped prints a draft, repository or session named as draft:<id>,
// repo:<id> or session:<id>. ok is false for any other reference.
func (e *env) showTyped(ctx context.Context, svc *kb.Service, ref string) (code int, ok bool) {
	kind, rest, typed := strings.Cut(ref, ":")
	if !typed || (kind != "draft" && kind != "repo" && kind != "session") {
		return 0, false
	}
	var v any
	var err error
	switch kind {
	case "session":
		v, err = svc.GetMining(ctx, rest)
	default:
		n, perr := strconv.ParseInt(rest, 10, 64)
		if perr != nil || n < 1 {
			return e.fail("kb show", fmt.Errorf("%q is not a %s id", rest, kind)), true
		}
		if kind == "draft" {
			v, err = svc.GetDraft(ctx, n)
		} else {
			v, err = svc.GetRepo(ctx, n)
		}
	}
	if err != nil {
		return e.fail("kb show", err), true
	}
	return e.printJSON(v), true
}

func (e *env) kbConfirm(ctx context.Context, args []string) int {
	fs, ws, asJSON := e.kbFlags("confirm")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) < 1 {
		fmt.Fprint(e.stderr, kbUsage)
		return 2
	}
	id, err := strconv.ParseInt(pos[0], 10, 64)
	if err != nil || id < 1 {
		fmt.Fprintf(e.stderr, "applysmith kb confirm: %q is not a draft id\n", pos[0])
		return 2
	}
	var idx []int
	for _, p := range pos[1:] {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 {
			fmt.Fprintf(e.stderr, "applysmith kb confirm: %q is not a fact number\n", p)
			return 2
		}
		idx = append(idx, n-1)
	}
	return e.withKB(ctx, "confirm", *ws, func(svc *kb.Service) int {
		d, err := svc.ConfirmDraftFacts(ctx, id, idx)
		if err != nil {
			return e.fail("kb confirm", err)
		}
		if *asJSON {
			return e.printJSON(d)
		}
		fmt.Fprintf(e.stdout, "draft %d is now revision %d; its confirmed statements will be added to allowed_facts when it is published\n", d.ID, d.Revision)
		return 0
	})
}

func (e *env) kbRepo(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(e.stderr, kbUsage)
		return 2
	}
	switch args[0] {
	case "add":
		return e.kbRepoAdd(ctx, args[1:])
	case "list":
		return e.kbRepoList(ctx, args[1:])
	case "ingest":
		return e.kbRepoIngest(ctx, args[1:])
	case "remove":
		return e.kbRepoRemove(ctx, args[1:])
	}
	fmt.Fprint(e.stderr, kbUsage)
	return 2
}

func (e *env) kbRepoAdd(ctx context.Context, args []string) int {
	fs, ws, asJSON := e.kbFlags("repo add")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(e.stderr, kbUsage)
		return 2
	}
	return e.withKB(ctx, "repo add", *ws, func(svc *kb.Service) int {
		r, err := svc.AddRepo(ctx, pos[0])
		if err != nil {
			return e.fail("kb repo add", err)
		}
		if *asJSON {
			return e.printJSON(r)
		}
		fmt.Fprintf(e.stdout, "repo:%d %s/%s added; run `applysmith kb repo ingest %d` to read it\n", r.ID, r.Owner, r.Name, r.ID)
		return 0
	})
}

func (e *env) kbRepoList(ctx context.Context, args []string) int {
	fs, ws, asJSON := e.kbFlags("repo list")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return 2
	}
	return e.withKB(ctx, "repo list", *ws, func(svc *kb.Service) int {
		repos, err := svc.ListRepos(ctx)
		if err != nil {
			return e.fail("kb repo list", err)
		}
		if *asJSON {
			return e.printJSON(repos)
		}
		for _, r := range repos {
			fmt.Fprintf(e.stdout, "repo:%d  %s/%s  %s  commit %s  %s\n", r.ID, r.Owner, r.Name, r.Status, short(r.CommitSHA), r.IngestedAt)
		}
		fmt.Fprintf(e.stdout, "%d repositories\n", len(repos))
		return 0
	})
}

func (e *env) kbRepoIngest(ctx context.Context, args []string) int {
	fs, ws, asJSON := e.kbFlags("repo ingest")
	force := fs.Bool("force", false, "read a commit that was already ingested")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(e.stderr, kbUsage)
		return 2
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(pos[0], "repo:"), 10, 64)
	if err != nil || id < 1 {
		fmt.Fprintf(e.stderr, "applysmith kb repo ingest: %q is not a repository id\n", pos[0])
		return 2
	}
	return e.withKBModel(ctx, "repo ingest", *ws, func(svc *kb.Service) int {
		rep, err := svc.IngestRepo(ctx, id, *force)
		if err != nil {
			return e.fail("kb repo ingest", err)
		}
		if *asJSON {
			return e.printJSON(rep)
		}
		fmt.Fprintf(e.stdout, "%s at %s: %d files read (%d fetched, %d reused), %d skipped, %d redacted, %d drafts\n",
			rep.Status, short(rep.Commit), rep.Read, rep.Fetched, rep.Reused, len(rep.Skipped), len(rep.Redacted), len(rep.Drafts))
		for _, r := range rep.Redacted {
			fmt.Fprintf(e.stdout, "redacted %s: %s\n", r.Path, r.Reason)
		}
		for _, n := range rep.Notes {
			fmt.Fprintf(e.stdout, "note: %s\n", n)
		}
		return 0
	})
}

func (e *env) kbRepoRemove(ctx context.Context, args []string) int {
	fs, ws, _ := e.kbFlags("repo remove")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(e.stderr, kbUsage)
		return 2
	}
	return e.withKB(ctx, "repo remove", *ws, func(svc *kb.Service) int {
		if err := svc.Remove(ctx, "repo:"+strings.TrimPrefix(pos[0], "repo:")); err != nil {
			return e.fail("kb repo remove", err)
		}
		fmt.Fprintln(e.stdout, "removed; its unpublished drafts were deleted and published snippets stay")
		return 0
	})
}

func (e *env) kbMine(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(e.stderr, kbUsage)
		return 2
	}
	cmds := map[string]func(context.Context, []string) int{
		"suggest": e.kbMineSuggest, "start": e.kbMineStart, "run": e.kbMineRun, "reply": e.kbMineReply,
		"show": e.kbMineShow, "save": e.kbMineSave, "abandon": e.kbMineAbandon,
	}
	if run, ok := cmds[args[0]]; ok {
		return run(ctx, args[1:])
	}
	fmt.Fprint(e.stderr, kbUsage)
	return 2
}

func (e *env) kbMineSuggest(ctx context.Context, args []string) int {
	fs, ws, asJSON := e.kbFlags("mine suggest")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return 2
	}
	return e.withKB(ctx, "mine suggest", *ws, func(svc *kb.Service) int {
		sug, err := svc.SuggestQuadrants(ctx)
		if err != nil {
			return e.fail("kb mine suggest", err)
		}
		if *asJSON {
			return e.printJSON(sug)
		}
		for _, q := range sug {
			fmt.Fprintf(e.stdout, "%-9s score %d  %s\n", q.Quadrant, q.Score, strings.Join(q.Reasons, "; "))
		}
		return 0
	})
}

func printSession(e *env, m kb.MiningSession) {
	fmt.Fprintf(e.stdout, "session:%s  %s  %s\n", m.ID, m.Quadrant, m.State)
	if m.Target != "" {
		fmt.Fprintf(e.stdout, "a %s story for: %s\n", m.Essay, m.Target)
	}
	if m.Ignored {
		fmt.Fprintln(e.stdout, "that reply was not recorded: the session no longer takes replies")
	}
	switch {
	case m.Next != nil:
		fmt.Fprintf(e.stdout, "\n%s\n", m.Next.Text)
	case m.State == kb.MineReady:
		fmt.Fprintf(e.stdout, "\nevery field is filled; run `applysmith kb mine save %s`\n", m.ID)
	case m.State == kb.MineSaved:
		fmt.Fprintf(e.stdout, "\nsaved as draft:%d\n", m.DraftID)
	}
}

func (e *env) kbMineStart(ctx context.Context, args []string) int {
	fs, ws, asJSON := e.kbFlags("mine start")
	quadrant := fs.String("quadrant", "", "project, team, customer or failure")
	notesFile := fs.String("notes-file", "", "a file inside the workspace whose notes pre-fill the fields")
	target := fs.String("target", "", "an application question that asks for a story; its fields are asked")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 || (*quadrant == "" && *target == "") {
		fmt.Fprint(e.stderr, kbUsage)
		return 2
	}
	return e.withKBModel(ctx, "mine start", *ws, func(svc *kb.Service) int {
		m, err := svc.StartMining(ctx, kb.MiningStart{Quadrant: *quadrant, NotesFile: *notesFile, Target: *target})
		if err != nil {
			return e.fail("kb mine start", err)
		}
		if *asJSON {
			return e.printJSON(m)
		}
		printSession(e, m)
		return 0
	})
}

// kbMineRun walks a whole session at the terminal: one question, one line of
// standard input, until every field is filled; then it saves the draft.
func (e *env) kbMineRun(ctx context.Context, args []string) int {
	fs, ws, _ := e.kbFlags("mine run")
	quadrant := fs.String("quadrant", "", "project, team, customer or failure")
	notesFile := fs.String("notes-file", "", "a file inside the workspace whose notes pre-fill the fields")
	target := fs.String("target", "", "an application question that asks for a story; its fields are asked")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 || (*quadrant == "" && *target == "") {
		fmt.Fprint(e.stderr, kbUsage)
		return 2
	}
	return e.withKBModel(ctx, "mine run", *ws, func(svc *kb.Service) int {
		m, err := svc.StartMining(ctx, kb.MiningStart{Quadrant: *quadrant, NotesFile: *notesFile, Target: *target})
		if err != nil {
			return e.fail("kb mine run", err)
		}
		in := bufio.NewScanner(e.in())
		in.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for m.State == kb.MineCollecting {
			fmt.Fprintf(e.stdout, "\n%s\n> ", m.Next.Text)
			if !in.Scan() {
				fmt.Fprintf(e.stdout, "\nstopped; resume with `applysmith kb mine reply %s <text>`\n", m.ID)
				return 3
			}
			if m, err = svc.MiningReply(ctx, m.ID, in.Text(), len(m.Log)); err != nil {
				if errors.Is(err, kb.ErrInvalid) {
					fmt.Fprintf(e.stderr, "%v\n", err)
					continue
				}
				return e.fail("kb mine run", err)
			}
		}
		d, err := svc.SaveMining(ctx, m.ID)
		if err != nil {
			return e.fail("kb mine run", err)
		}
		fmt.Fprintf(e.stdout, "\nsaved as draft:%d; it is not retrievable until you edit and publish it (applysmith kb publish %d --story)\n", d.ID, d.ID)
		return 0
	})
}

func (e *env) kbMineReply(ctx context.Context, args []string) int {
	fs, ws, asJSON := e.kbFlags("mine reply")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) < 2 {
		fmt.Fprint(e.stderr, kbUsage)
		return 2
	}
	return e.withKBModel(ctx, "mine reply", *ws, func(svc *kb.Service) int {
		id := strings.TrimPrefix(pos[0], "session:")
		cur, err := svc.GetMining(ctx, id)
		if err != nil {
			return e.fail("kb mine reply", err)
		}
		m, err := svc.MiningReply(ctx, id, strings.Join(pos[1:], " "), len(cur.Log))
		if err != nil {
			return e.fail("kb mine reply", err)
		}
		if *asJSON {
			return e.printJSON(m)
		}
		printSession(e, m)
		return 0
	})
}

func (e *env) kbMineShow(ctx context.Context, args []string) int {
	fs, ws, asJSON := e.kbFlags("mine show")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(e.stderr, kbUsage)
		return 2
	}
	return e.withKB(ctx, "mine show", *ws, func(svc *kb.Service) int {
		m, err := svc.GetMining(ctx, strings.TrimPrefix(pos[0], "session:"))
		if err != nil {
			return e.fail("kb mine show", err)
		}
		if *asJSON {
			return e.printJSON(m)
		}
		printSession(e, m)
		for _, l := range m.Log {
			fmt.Fprintf(e.stdout, "  [%s %s] %s\n", l.Role, l.Field, l.Text)
		}
		return 0
	})
}

func (e *env) kbMineSave(ctx context.Context, args []string) int {
	fs, ws, asJSON := e.kbFlags("mine save")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(e.stderr, kbUsage)
		return 2
	}
	return e.withKB(ctx, "mine save", *ws, func(svc *kb.Service) int {
		d, err := svc.SaveMining(ctx, strings.TrimPrefix(pos[0], "session:"))
		if err != nil {
			return e.fail("kb mine save", err)
		}
		if *asJSON {
			return e.printJSON(d)
		}
		fmt.Fprintf(e.stdout, "draft:%d saved; it is not retrievable until you publish it\n", d.ID)
		return 0
	})
}

func (e *env) kbMineAbandon(ctx context.Context, args []string) int {
	fs, ws, asJSON := e.kbFlags("mine abandon")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(e.stderr, kbUsage)
		return 2
	}
	return e.withKB(ctx, "mine abandon", *ws, func(svc *kb.Service) int {
		m, err := svc.AbandonMining(ctx, strings.TrimPrefix(pos[0], "session:"))
		if err != nil {
			return e.fail("kb mine abandon", err)
		}
		if *asJSON {
			return e.printJSON(m)
		}
		printSession(e, m)
		return 0
	})
}
