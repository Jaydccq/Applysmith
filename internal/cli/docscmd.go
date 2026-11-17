package cli

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"applysmith/internal/api"
	"applysmith/internal/api/documentsapi"
	"applysmith/internal/documents"
	"applysmith/internal/documents/assets"
	"applysmith/internal/documents/toolchain"
	"applysmith/internal/evaluate"
	"applysmith/internal/kb"
	"applysmith/internal/llm"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
	"applysmith/internal/vault"
	"applysmith/internal/workspace"
)

const documentCommandsUsage = `usage:
  applysmith resume generate (--candidacy <id> | --baseline) [--pdf] [--paper a4|letter] [--pages 1|2] [--language <tag>] [--with-cover-letter] [--posting-file <path>] [--json]
  applysmith cover-letter --candidacy <id> [--pdf] [--paper a4|letter] [--language <tag>] [--posting-file <path>] [--json]
  applysmith documents list [--candidacy <id>] [--json]
  applysmith documents show <id> [--json]
  applysmith documents delete <id> [--submitted]
  applysmith documents review <id> --performed --sha256 <pdf-hash> [--json]

--pdf compiles and verifies the PDFs; without it only the structured sources and TeX are written.
resume generate and cover-letter each make one document; list names a version made before cv.md,
profile.yml, voice.md or the evaluation report changed, and the command that makes it again.
--submitted lets delete remove a version attached to a submitted application.
review records that you looked at the page previews of the stored PDF; the hash must be the PDF's
(documents show --json prints it), so a regenerated PDF reads as unreviewed again.
every command takes --workspace <dir>; documents list and show work beside a running server, the others need it stopped
`

// docEnv is a document service wired to the workspace, for one command.
type docEnv struct {
	svc *documents.Service
	ws  *workspace.Workspace
	st  *store.Store
}

// withDocuments builds the same document service the server runs, with the
// model routed through the router and the toolchain of this user. documents
// list and show read the database read-only, beside a running server; the
// other commands hold the workspace lock, and only they start the service,
// which settles the document runs a crashed process left.
func (e *env) withDocuments(ctx context.Context, cmd, wsFlag string, fn func(docEnv) int) int {
	root, err := e.workspacePath(wsFlag)
	if err != nil {
		return e.fail(cmd, err)
	}
	read := cmd == "documents list" || cmd == "documents show"
	access := accessExclusive
	if read {
		access = accessRead
	}
	ws, st, release, err := e.openStore(ctx, root, access)
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
	tr, err := tracker.New(st, tracker.Options{LinkedTables: []tracker.LinkedTable{{Table: "documents", Column: "candidacy_id"}}})
	if err != nil {
		return e.fail(cmd, err)
	}
	router := llm.New(st, v, append([]llm.Option{llm.WithGetenv(e.getenv), llm.WithFake(evaluate.NewFake())}, e.llmOptions...)...)
	knowledge, err := kb.New(st, api.KnowledgeOptions(ws, st, router, v))
	if err != nil {
		return e.fail(cmd, err)
	}
	cfg := e.toolchainConfig()
	svc, err := documents.New(documents.Deps{Store: st, Tracker: tr, Workspace: ws, Model: router, Retriever: knowledge, Stories: knowledge,
		Toolchain: func(ctx context.Context) (*toolchain.Toolchain, error) { return toolchain.Open(ctx, cfg) },
		Emit: func(ev documents.Event) {
			if !ev.Final {
				fmt.Fprintf(e.stderr, "documents: %s\n", ev.Stage)
			}
		}})
	if err != nil {
		return e.fail(cmd, err)
	}
	if !read {
		if err := svc.Start(ctx); err != nil {
			return e.fail(cmd, err)
		}
		defer svc.Close()
	}
	return fn(docEnv{svc: svc, ws: ws, st: st})
}

// docRequest is the flag set the generation commands share.
type docFlags struct {
	ws, paper, language, postingFile *string
	candidacy, pages                 *int
	pdf, asJSON                      *bool
}

func (e *env) docFlags(name string, withPages bool) (*flag.FlagSet, docFlags) {
	fs := e.flagsWithUsage(name, documentCommandsUsage)
	f := docFlags{
		ws: fs.String("workspace", "", "workspace directory"), paper: fs.String("paper", "", "a4 or letter"),
		language: fs.String("language", "", "language tag"), postingFile: fs.String("posting-file", "", "read the job description from a file"),
		candidacy: fs.Int("candidacy", 0, "tracker row id"), pdf: fs.Bool("pdf", false, "compile and verify the PDFs"), asJSON: fs.Bool("json", false, "print JSON"),
		pages: new(int),
	}
	if withPages {
		f.pages = fs.Int("pages", 0, "résumé page limit, 1 or 2")
	}
	return fs, f
}

func (e *env) resumeCmd(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] != "generate" {
		fmt.Fprint(e.stderr, documentCommandsUsage)
		return 2
	}
	fs, f := e.docFlags("resume generate", true)
	baseline := fs.Bool("baseline", false, "a baseline résumé from the CV and profile, with no candidacy")
	withCover := fs.Bool("with-cover-letter", false, "also write the paired cover letter from the same manifest")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		return e.refuseUsage("resume generate", documentCommandsUsage, fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
	}
	req := documents.Request{Action: documents.ActionResume, Mode: documents.ModeTailored, CandidacyID: int64(*f.candidacy), PDF: *f.pdf,
		Options: documents.Options{Language: *f.language, Paper: assets.Paper(*f.paper), Pages: *f.pages}}
	if *baseline {
		req.Mode = documents.ModeBaseline
	}
	if *withCover {
		req.Action = documents.ActionPair
	}
	if (*baseline) == (*f.candidacy != 0) {
		fmt.Fprintln(e.stderr, "applysmith resume generate: give exactly one of --candidacy and --baseline")
		return 2
	}
	return e.generate(ctx, "resume generate", f, req)
}

func (e *env) coverCmd(ctx context.Context, args []string) int {
	fs, f := e.docFlags("cover-letter", false)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		return e.refuseUsage("cover-letter", documentCommandsUsage, fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
	}
	if *f.candidacy < 1 {
		return e.refuseUsage("cover-letter", documentCommandsUsage, "missing --candidacy <id>: the tracker row the letter is for (`applysmith tracker list` shows them)")
	}
	return e.generate(ctx, "cover-letter", f, documents.Request{Action: documents.ActionCover, Mode: documents.ModeTailored,
		CandidacyID: int64(*f.candidacy), PDF: *f.pdf, Options: documents.Options{Language: *f.language, Paper: assets.Paper(*f.paper)}})
}

// generate submits one request through the shared service and waits for it.
func (e *env) generate(ctx context.Context, cmd string, f docFlags, req documents.Request) int {
	if *f.postingFile != "" {
		b, err := os.ReadFile(*f.postingFile)
		if err != nil {
			return e.fail(cmd, err)
		}
		req.Posting = string(b)
	}
	return e.withDocuments(ctx, cmd, *f.ws, func(x docEnv) int {
		h, err := x.svc.Submit(ctx, documents.Claim{Principal: "cli", Key: fmt.Sprintf("cli-%d-%d", os.Getpid(), time.Now().UnixNano())}, req)
		if err != nil {
			return e.reportDocumentError(cmd, err)
		}
		res, err := x.svc.Wait(ctx, h.RunID)
		if err != nil {
			return e.fail(cmd, err)
		}
		if *f.asJSON {
			run, err := x.st.GetRun(ctx, res.RunID)
			if err != nil {
				return e.fail(cmd, err)
			}
			e.printJSON(documentsapi.WireDocumentRun(run, res))
		} else {
			e.printDocumentResult(x, ctx, res)
		}
		if !res.Published {
			return 1
		}
		return 0
	})
}

func (e *env) reportDocumentError(cmd string, err error) int {
	var unavailable *toolchain.UnavailableError
	if errors.As(err, &unavailable) {
		if len(unavailable.Report.Missing) == 0 {
			// Every tool is present: the smoke record does not match this
			// combination (a template or tool changed).
			fmt.Fprintf(e.stderr, "applysmith %s: %s: the toolchain has not passed its smoke test for this combination (smoke: %s); run `applysmith documents toolchain smoke`\n", cmd, toolchain.CodeUnavailable, unavailable.Report.Smoke)
			return 3
		}
		fmt.Fprintf(e.stderr, "applysmith %s: %s: new PDFs need %s; run `applysmith documents toolchain check`\n", cmd, toolchain.CodeUnavailable, strings.Join(unavailable.Report.Missing, ", "))
		return 3
	}
	if errors.Is(err, documents.ErrInvalid) || errors.Is(err, documents.ErrNoPosting) || errors.Is(err, documents.ErrBaselineCover) {
		fmt.Fprintf(e.stderr, "applysmith %s: %v\n", cmd, err)
		return 2
	}
	return e.fail(cmd, err)
}

// runFailure says why a document or prep-pack run published nothing, in the
// dashboard's words, and what to do when the user can act.
func runFailure(reason string) string {
	why := map[string]string{
		documents.ReasonNoContact:  "your profile has no name or email for the contact block; `applysmith setup apply identity --file <path|->` adds them",
		documents.ReasonNoPosting:  "the job description is needed; pass it with --posting-file <path>",
		documents.ReasonBelowFloor: "the posting score is below the floor for a PDF; `applysmith report override <number>` lets it have one",
		documents.ReasonToolchain:  "the document tools are missing; run `applysmith documents toolchain check`",
		documents.ReasonNoDraft:    "the text did not pass the grounding checks",
		documents.ReasonStale:      "the stored text changed after it was written; run the command again",
		documents.ReasonTimeout:    "it took too long; run the command again",
	}[reason]
	if why == "" {
		return "failed: " + reason
	}
	return "failed (" + reason + "): " + why
}

func (e *env) printDocumentResult(x docEnv, ctx context.Context, res documents.Result) {
	if !res.Published {
		fmt.Fprintln(e.stdout, runFailure(res.Reason))
		for _, w := range res.Warnings {
			fmt.Fprintf(e.stdout, "  %s\n", w)
		}
		return
	}
	fmt.Fprintf(e.stdout, "published (%s content)\n", res.Generation)
	tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
	for _, id := range res.Documents {
		if d, err := x.svc.Get(ctx, id); err == nil {
			fmt.Fprintf(tw, "  %d\t%s\tv%d\t%s\tpdf %s\t%s\n", d.ID, d.Kind, d.Version, d.Paper, d.PDFState, cmp.Or(d.PDFPath, d.Path))
		}
	}
	tw.Flush()
	for _, w := range res.Warnings {
		fmt.Fprintf(e.stdout, "  note: %s\n", w)
	}
}

func (e *env) documentsList(ctx context.Context, args []string) int {
	fs := e.flags("documents list")
	ws := fs.String("workspace", "", "workspace directory")
	cand := fs.Int("candidacy", 0, "tracker row id; omit for the baseline documents")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		return e.refuseUsage("documents list", documentCommandsUsage, fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
	}
	return e.withDocuments(ctx, "documents list", *ws, func(x docEnv) int {
		docs, err := x.svc.List(ctx, int64(*cand))
		if err != nil {
			return e.fail("documents list", err)
		}
		if *asJSON {
			return e.printDocuments(ctx, x, "documents list", docs, false)
		}
		if len(docs) == 0 && *cand == 0 {
			fmt.Fprintln(e.stdout, "no baseline documents; `applysmith documents list --candidacy <id>` lists a tracker row's documents")
			return 0
		}
		if len(docs) == 0 {
			fmt.Fprintf(e.stdout, "no documents for tracker row %d\n", *cand)
			return 0
		}
		tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
		var stale []string
		for _, d := range docs {
			mark := ""
			if d.Ready {
				mark = " ready"
			}
			fmt.Fprintf(tw, "%d\t%s\tv%d\t%s\tpdf %s%s\t%d pages\t%s\n", d.ID, d.Kind, d.Version, d.Paper, d.PDFState, mark, d.Pages, d.Path)
			if changed := x.svc.SourcesChanged(ctx, d); len(changed) > 0 {
				stale = append(stale, fmt.Sprintf("%d was made before %s changed; `%s` makes a new one", d.ID, strings.Join(changed, " and "), regenerateCommand(d)))
			}
		}
		if code := flushOK(tw); code != 0 {
			return code
		}
		for _, line := range stale {
			fmt.Fprintln(e.stdout, line)
		}
		return 0
	})
}

// regenerateCommand makes that one document again, without its pair.
func regenerateCommand(d documents.Document) string {
	target := "--baseline"
	if d.CandidacyID != nil {
		target = fmt.Sprintf("--candidacy %d", *d.CandidacyID)
	}
	if d.Kind == documents.KindCover {
		return "applysmith cover-letter " + target
	}
	return "applysmith resume generate " + target
}

// printDocuments prints documents as GET /api/documents answers them, one of
// them alone when one is asked for.
func (e *env) printDocuments(ctx context.Context, x docEnv, cmd string, docs []documents.Document, one bool) int {
	list, err := documentsapi.DocumentList(ctx, x.svc, docs)
	if err != nil {
		return e.fail(cmd, err)
	}
	if one {
		return e.printJSON(list.Documents[0])
	}
	return e.printJSON(list)
}

func flushOK(tw interface{ Flush() error }) int {
	if tw.Flush() != nil {
		return 1
	}
	return 0
}

func (e *env) documentsShow(ctx context.Context, args []string) int {
	fs := e.flags("documents show")
	ws := fs.String("workspace", "", "workspace directory")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(e.stderr, documentCommandsUsage)
		return 2
	}
	id, err := parseID(pos[0])
	if err != nil {
		fmt.Fprint(e.stderr, documentCommandsUsage)
		return 2
	}
	return e.withDocuments(ctx, "documents show", *ws, func(x docEnv) int {
		d, err := x.svc.Get(ctx, id)
		if err != nil {
			return e.fail("documents show", err)
		}
		if *asJSON {
			return e.printDocuments(ctx, x, "documents show", []documents.Document{d}, true)
		}
		fmt.Fprintf(e.stdout, "%s v%d (%s, %s content, %s)\npath: %s\npdf: %s %s\n", d.Kind, d.Version, d.Mode, d.Generation, d.Paper, d.Path, d.PDFState, d.PDFPath)
		for _, w := range d.Warnings {
			fmt.Fprintf(e.stdout, "note: %s\n", w)
		}
		return 0
	})
}

func (e *env) documentsDelete(ctx context.Context, args []string) int {
	fs := e.flags("documents delete")
	ws := fs.String("workspace", "", "workspace directory")
	submitted := fs.Bool("submitted", false, "also delete a version attached to a submitted application")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(e.stderr, documentCommandsUsage)
		return 2
	}
	id, err := parseID(pos[0])
	if err != nil {
		fmt.Fprint(e.stderr, documentCommandsUsage)
		return 2
	}
	return e.withDocuments(ctx, "documents delete", *ws, func(x docEnv) int {
		if err := x.svc.Delete(ctx, id, *submitted); err != nil {
			if errors.Is(err, documents.ErrFrozen) {
				fmt.Fprintf(e.stderr, "applysmith documents delete: %v; add --submitted to delete it anyway\n", err)
				return 3
			}
			return e.fail("documents delete", err)
		}
		fmt.Fprintf(e.stdout, "deleted document %d\n", id)
		return 0
	})
}

func (e *env) documentsReview(ctx context.Context, args []string) int {
	fs := e.flags("documents review")
	ws := fs.String("workspace", "", "workspace directory")
	performed := fs.Bool("performed", false, "confirm that you looked at the previews of this PDF")
	hash := fs.String("sha256", "", "SHA-256 of the PDF you looked at")
	asJSON := fs.Bool("json", false, "print the verification record as JSON")
	pos, err := parseMixed(fs, args)
	var id int64
	if err == nil && len(pos) == 1 {
		id, err = parseID(pos[0])
	}
	if err != nil || len(pos) != 1 || !*performed || *hash == "" {
		fmt.Fprint(e.stderr, documentCommandsUsage)
		return 2
	}
	return e.withDocuments(ctx, "documents review", *ws, func(x docEnv) int {
		v, err := x.svc.RecordVisualReview(ctx, id, *hash)
		if err != nil {
			return e.reportDocumentError("documents review", err)
		}
		if *asJSON {
			return e.printJSON(documentsapi.WireDocumentVerification(v))
		}
		fmt.Fprintf(e.stdout, "visual review of document %d recorded against pdf %s\n", id, v.VisualReview.PDFSHA256)
		return 0
	})
}
