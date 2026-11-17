package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"applysmith/internal/api"
	"applysmith/internal/api/knowledgeapi"
	"applysmith/internal/kb"
	"applysmith/internal/llm"
	"applysmith/internal/vault"
)

const kbUsage = `usage:
  applysmith kb search <query> [--kind k]... [--k n] [--tag t]... [--archetype a]... [--company-kind c] [--json]
  applysmith kb list [--json]
  applysmith kb show <id> [--json]
  applysmith kb lint [--json]
  applysmith kb reindex [--json]
  applysmith kb delete <id>
  applysmith kb export <dir>
  applysmith kb import <file> [--category c] [--replace] [--json]
  applysmith kb drafts [--json]
  applysmith kb publish <draft id> [--story] [--json]
  applysmith kb confirm <draft id> [<fact number>...] [--json]
  applysmith kb repo add <owner/repo | https://github.com/owner/repo>
  applysmith kb repo list [--json]
  applysmith kb repo ingest <id> [--force] [--json]
  applysmith kb repo remove <id>
  applysmith kb mine suggest [--json]
  applysmith kb mine start (--quadrant project|team|customer|failure | --target <question>) [--notes-file path] [--json]
  applysmith kb mine run (--quadrant project|team|customer|failure | --target <question>) [--notes-file path]
  applysmith kb mine reply <session> <text>... [--json]
  applysmith kb mine show <session> [--json]
  applysmith kb mine save <session> [--json]
  applysmith kb mine abandon <session> [--json]

list shows documents, drafts, repositories (with the commit read), mining sessions, gaps and embeddings;
show and delete take a snippet id or repo:<id>, session:<id>, draft:<id>; delete also takes cache:gaps and cache:embeddings

every command takes --workspace <dir>; the reads (search, list, show, lint, drafts, export, repo list, mine suggest
and mine show) work beside a running server, the others need it stopped
`

func (e *env) kb(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(e.stderr, kbUsage)
		return 2
	}
	cmds := map[string]func(context.Context, []string) int{
		"search": e.kbSearch, "list": e.kbList, "show": e.kbShow, "lint": e.kbLint, "reindex": e.kbReindex,
		"delete": e.kbDelete, "export": e.kbExport, "import": e.kbImport, "drafts": e.kbDrafts, "publish": e.kbPublish,
		"confirm": e.kbConfirm, "repo": e.kbRepo, "mine": e.kbMine,
	}
	if run, ok := cmds[args[0]]; ok {
		return run(ctx, args[1:])
	}
	fmt.Fprint(e.stderr, kbUsage)
	return 2
}

// withKB opens the workspace and runs fn with the knowledge base: the reads
// beside a running server (they bring the index up to date with the files, as
// the server's own reads do, so they join as one more writer), every other
// command under the workspace lock, which a running server holds, so the
// command says so instead of opening a second store beside it.
func (e *env) withKB(ctx context.Context, cmd, wsFlag string, fn func(*kb.Service) int) int {
	return e.openKB(ctx, cmd, wsFlag, false, fn)
}

// withKBModel is withKB with the routed model and the optional GitHub token
// wired in, for the commands that read a repository or the user's notes.
func (e *env) withKBModel(ctx context.Context, cmd, wsFlag string, fn func(*kb.Service) int) int {
	return e.openKB(ctx, cmd, wsFlag, true, fn)
}

func (e *env) openKB(ctx context.Context, cmd, wsFlag string, withModel bool, fn func(*kb.Service) int) int {
	name := "kb " + cmd
	root, err := e.workspacePath(wsFlag)
	if err != nil {
		return e.fail(name, err)
	}
	access := accessExclusive
	switch cmd {
	case "search", "list", "show", "lint", "drafts", "export", "repo list", "mine suggest", "mine show":
		access = accessShared
	}
	ws, st, release, err := e.openStore(ctx, root, access)
	if err != nil {
		return e.fail(name, err)
	}
	defer release()
	id, err := ws.ID()
	if err != nil {
		return e.fail(name, err)
	}
	v, verr := vault.New(id, e.backend, vault.WithGetenv(e.getenv))
	if verr != nil {
		return e.fail(name, verr)
	}
	router := llm.New(st, v, append([]llm.Option{llm.WithGetenv(e.getenv)}, e.llmOptions...)...)
	opts := api.KnowledgeOptions(ws, st, router, v)
	if !withModel {
		opts.Model = nil
	}

	svc, err := kb.New(st, opts)
	if err != nil {
		return e.fail(name, err)
	}
	return fn(svc)
}

func (e *env) kbFlags(name string) (*flag.FlagSet, *string, *bool) {
	fs := e.flags("kb " + name)
	return fs, fs.String("workspace", "", "workspace directory"), fs.Bool("json", false, "print JSON")
}

func (e *env) kbSearch(ctx context.Context, args []string) int {
	fs, ws, asJSON := e.kbFlags("search")
	var kinds, tags, arch stringList
	fs.Var(&kinds, "kind", "restrict to a source kind or snippet category (repeatable)")
	fs.Var(&tags, "tag", "boost snippets with this tag (repeatable)")
	fs.Var(&arch, "archetype", "archetype tag for behavioral stories (repeatable)")
	k := fs.Int("k", 0, "results, default 4, at most 20")
	companyKind := fs.String("company-kind", "", "company kind to prefer")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) == 0 {
		fmt.Fprint(e.stderr, kbUsage)
		return 2
	}
	return e.withKB(ctx, "search", *ws, func(svc *kb.Service) int {
		res, err := svc.Search(ctx, kb.Query{Query: strings.Join(pos, " "), Kinds: kinds, K: *k, Tags: tags, ArchetypeTags: arch, CompanyKind: *companyKind})
		if err != nil {
			return e.fail("kb search", err)
		}
		if *asJSON {
			return e.printJSON(res)
		}
		facts := fmt.Sprintf("%d facts", len(res.Facts))
		if len(res.Facts) == 1 {
			facts = "1 fact"
		}
		switch res.Status {
		case kb.StatusNoMatch:
			fmt.Fprintln(e.stdout, "nothing in the knowledge base matches")
		case kb.StatusSourcesUnavailable:
			fmt.Fprintln(e.stdout, "no knowledge source could be read")
		case kb.StatusPartial:
			fmt.Fprintf(e.stdout, "%s; some knowledge sources could not be read\n", facts)
		default:
			fmt.Fprintln(e.stdout, facts)
		}
		for _, f := range res.Facts {
			fmt.Fprintf(e.stdout, "\n%.2f  %s  [%s %s]\n%s\n", f.Score, f.ID, f.Trust, f.Stage, f.Text)
			fmt.Fprintf(e.stdout, "  source: %s %s\n", f.SourceKind, f.SourceRef)
			if len(f.ConflictsWith) > 0 {
				fmt.Fprintf(e.stdout, "  conflicts with: %s\n", strings.Join(f.ConflictsWith, ", "))
			}
		}
		for _, f := range res.Failed {
			fmt.Fprintf(e.stdout, "\nunreadable source %s: %s\n", f.Source, f.Error)
		}
		return 0
	})
}

func (e *env) kbList(ctx context.Context, args []string) int {
	fs, ws, asJSON := e.kbFlags("list")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return 2
	}
	return e.withKB(ctx, "list", *ws, func(svc *kb.Service) int {
		inv, err := svc.Inventory(ctx)
		if err != nil {
			return e.fail("kb list", err)
		}
		if *asJSON {
			return e.printJSON(inv)
		}
		tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tKIND\tCATEGORY\tTITLE\tFINDINGS")
		for _, en := range inv.Documents {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\n", en.ID, en.Kind, en.Category, en.Title, len(en.Lint))
		}
		tw.Flush()
		fmt.Fprintf(e.stdout, "%d documents\n", len(inv.Documents))
		for _, d := range inv.Drafts {
			fmt.Fprintf(e.stdout, "draft:%d  %s  %s rev %d  %s\n", d.ID, d.SnippetID, d.Kind, d.Revision, d.Status)
		}
		for _, r := range inv.Repos {
			fmt.Fprintf(e.stdout, "repo:%d  %s/%s  %s  commit %s\n", r.ID, r.Owner, r.Name, r.Status, short(r.CommitSHA))
		}
		for _, m := range inv.Sessions {
			fmt.Fprintf(e.stdout, "session:%s  %s  %s\n", m.ID, m.Quadrant, m.State)
		}
		fmt.Fprintf(e.stdout, "%d gaps, %d answers, %d embeddings\n", len(inv.Gaps), len(inv.Answers), inv.Embeddings)
		return 0
	})
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	if sha == "" {
		return "-"
	}
	return sha
}

func (e *env) kbShow(ctx context.Context, args []string) int {
	fs, ws, asJSON := e.kbFlags("show")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(e.stderr, kbUsage)
		return 2
	}
	return e.withKB(ctx, "show", *ws, func(svc *kb.Service) int {
		if code, ok := e.showTyped(ctx, svc, pos[0]); ok {
			return code
		}
		d, err := svc.Show(ctx, pos[0])
		if err != nil {
			return e.fail("kb show", err)
		}
		if *asJSON {
			return e.printJSON(knowledgeapi.WireKBDocument(d))
		}
		fmt.Fprintf(e.stdout, "path: %s\nhash: %s\n", d.Path, d.ContentHash)
		for _, f := range d.Lint {
			fmt.Fprintf(e.stdout, "lint %s: %s\n", f.Code, f.Message)
		}
		fmt.Fprintf(e.stdout, "\n%s", d.Markdown)
		return 0
	})
}

func (e *env) kbLint(ctx context.Context, args []string) int {
	fs, ws, asJSON := e.kbFlags("lint")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return 2
	}
	return e.withKB(ctx, "lint", *ws, func(svc *kb.Service) int {
		list, err := svc.List(ctx)
		if err != nil {
			return e.fail("kb lint", err)
		}
		warnings, err := svc.Warnings(ctx)
		if err != nil {
			return e.fail("kb lint", err)
		}
		if warnings == nil {
			warnings = []string{}
		}
		type row struct {
			ID       string       `json:"id"`
			Findings []kb.Finding `json:"findings"`
		}
		rows := []row{}
		for _, en := range list {
			if len(en.Lint) > 0 {
				rows = append(rows, row{en.ID, en.Lint})
			}
		}
		if *asJSON {
			return e.printJSON(map[string]any{"warnings": warnings, "snippets": rows})
		}
		for _, w := range warnings {
			fmt.Fprintf(e.stdout, "warning: %s\n", w)
		}
		for _, r := range rows {
			for _, f := range r.Findings {
				fmt.Fprintf(e.stdout, "%s: %s: %s\n", r.ID, f.Code, f.Message)
			}
		}
		fmt.Fprintf(e.stdout, "%d warnings, %d snippets with findings\n", len(warnings), len(rows))
		return 0
	})
}

func (e *env) kbReindex(ctx context.Context, args []string) int {
	fs, ws, asJSON := e.kbFlags("reindex")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return 2
	}
	return e.withKB(ctx, "reindex", *ws, func(svc *kb.Service) int {
		rep, err := svc.Reindex(ctx)
		if err != nil {
			return e.fail("kb reindex", err)
		}
		if *asJSON {
			return e.printJSON(rep)
		}
		fmt.Fprintf(e.stdout, "reindexed %d documents, %d warnings, %d unreadable sources\n", rep.Changed, len(rep.Warnings), len(rep.Failures))
		return 0
	})
}

func (e *env) kbDelete(ctx context.Context, args []string) int {
	fs, ws, _ := e.kbFlags("delete")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(e.stderr, kbUsage)
		return 2
	}
	return e.withKB(ctx, "delete", *ws, func(svc *kb.Service) int {
		if err := svc.Remove(ctx, pos[0]); err != nil {
			return e.fail("kb delete", err)
		}
		fmt.Fprintf(e.stdout, "deleted %s\n", pos[0])
		return 0
	})
}

func (e *env) kbExport(ctx context.Context, args []string) int {
	fs, ws, _ := e.kbFlags("export")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(e.stderr, kbUsage)
		return 2
	}
	return e.withKB(ctx, "export", *ws, func(svc *kb.Service) int {
		if err := svc.Export(ctx, pos[0]); err != nil {
			return e.fail("kb export", err)
		}
		fmt.Fprintf(e.stdout, "exported to %s\n", pos[0])
		return 0
	})
}

func (e *env) kbImport(ctx context.Context, args []string) int {
	fs, ws, asJSON := e.kbFlags("import")
	category := fs.String("category", "", "projects, experience, behavioral or answer-patterns")
	replace := fs.Bool("replace", false, "take an id that already exists")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(e.stderr, kbUsage)
		return 2
	}
	data, err := os.ReadFile(pos[0])
	if err != nil {
		return e.fail("kb import", err)
	}
	return e.withKB(ctx, "import", *ws, func(svc *kb.Service) int {
		d, err := svc.ImportFile(ctx, kb.ImportInput{Name: filepath.Base(pos[0]), Data: data, Category: *category, Replace: *replace})
		if err != nil {
			return e.fail("kb import", err)
		}
		if *asJSON {
			return e.printJSON(d)
		}
		fmt.Fprintf(e.stdout, "draft %d (%s) created; it is not retrievable until it is published\n", d.ID, d.SnippetID)
		return 0
	})
}

func (e *env) kbDrafts(ctx context.Context, args []string) int {
	fs, ws, asJSON := e.kbFlags("drafts")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return 2
	}
	return e.withKB(ctx, "drafts", *ws, func(svc *kb.Service) int {
		list, err := svc.ListDrafts(ctx, false)
		if err != nil {
			return e.fail("kb drafts", err)
		}
		if *asJSON {
			return e.printJSON(list)
		}
		tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "DRAFT\tSNIPPET\tKIND\tREV\tSTATUS")
		for _, d := range list {
			fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%s\n", d.ID, d.SnippetID, d.Kind, d.Revision, d.Status)
		}
		if err := tw.Flush(); err != nil {
			return e.fail("kb drafts", err)
		}
		return 0
	})
}

func (e *env) kbPublish(ctx context.Context, args []string) int {
	fs, ws, asJSON := e.kbFlags("publish")
	story := fs.Bool("story", false, "publish a story draft through the story gates")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(e.stderr, kbUsage)
		return 2
	}
	id, err := strconv.ParseInt(pos[0], 10, 64)
	if err != nil || id < 1 {
		fmt.Fprintf(e.stderr, "applysmith kb publish: %q is not a draft id\n", pos[0])
		return 2
	}
	return e.withKB(ctx, "publish", *ws, func(svc *kb.Service) int {
		var out kb.Published
		var err error
		if *story {
			out, err = svc.PublishStory(ctx, id)
		} else {
			out, err = svc.PublishDraft(ctx, id)
		}
		var gate *kb.GateError
		if errors.As(err, &gate) {
			for _, f := range gate.Findings {
				fmt.Fprintf(e.stderr, "%s %s: %s\n", f.Severity, f.Code, f.Message)
			}
		}
		if err != nil {
			return e.fail("kb publish", err)
		}
		if *asJSON {
			return e.printJSON(out)
		}
		fmt.Fprintf(e.stdout, "published %s as %s\n", out.SnippetID, out.Path)
		for _, f := range out.Lint {
			fmt.Fprintf(e.stdout, "lint %s: %s\n", f.Code, f.Message)
		}
		for _, f := range out.StoryFindings {
			fmt.Fprintf(e.stdout, "%s %s: %s\n", f.Severity, f.Code, f.Message)
		}
		return 0
	})
}
