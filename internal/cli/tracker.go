package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/api/trackerapi"
	"applysmith/internal/api/wire"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/tracker"
	"applysmith/internal/workspace"
)

const trackerUsage = `usage:
  applysmith tracker list [--status s[,s...]] [--company c] [--source s] [--applied-within days] [--reached s] [--moved-by actor] [--since date] [--query q] [--min-score n] [--archived] [--sort updated_desc|score_desc|date_desc|company_asc|role_asc|status_asc] [--page n] [--page-size n] [--json]
  applysmith tracker show <id> [--json]
  applysmith tracker set-status <id> <status> [--note text] [--on date] [--json]
  applysmith tracker note <id> <text> [--json]
  applysmith tracker note <id> --undo [--json]
  applysmith tracker add --company c --role r [--url u] [--status s] [--location l] [--applied-at date] [--json]
  applysmith tracker set-docs <id> --resume|--cover [--clear] [--json]
  applysmith tracker export [--format md|csv] [--status s[,s...]] [--company c] [--source s] [--applied-within days] [--reached s] [--moved-by actor] [--since date] [--min-score n] [--archived] [--out file]
  applysmith tracker dedup [--apply] [--json]
  applysmith tracker archive [--older-than 30d] [--include-skipped] [--dry-run] [--json]
  applysmith tracker unarchive <id> [--json]
  applysmith tracker outcomes [--from date] [--to date] [--as-of date] [--json]
  applysmith tracker followups [--due] [--json]
  applysmith tracker followup <id> [--snooze <N>d | --undo] [--json]

--reached keeps the rows moved to that status, --moved-by those that actor (user, gmail, apply_chain,
agent or evaluation) moved, on or after the local day --since (YYYY-MM-DD, required with either).
every command takes --workspace <dir> and works beside a running server
`

// trackerFlags is the flag set every tracker command shares.
type trackerFlags struct {
	fs     *flag.FlagSet
	ws     *string
	asJSON *bool
}

func (e *env) trackerFlags(name string) trackerFlags {
	text := usageLines("tracker "+name, trackerUsage)
	if text != "" {
		text += "\n"
	}
	fs := e.flagsWithUsage("tracker "+name, text)
	return trackerFlags{fs: fs, ws: fs.String("workspace", "", "workspace directory"), asJSON: fs.Bool("json", false, "print JSON")}
}

// parseMixed parses flags that may follow positional arguments, as in
// `set-status 5 evaluated --note text`.
func parseMixed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func (e *env) tracker(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(e.stderr, trackerUsage)
		return 2
	}
	cmds := map[string]func(context.Context, []string) int{
		"list": e.trackerList, "show": e.trackerShow, "set-status": e.trackerSetStatus, "note": e.trackerNote,
		"add": e.trackerAdd, "set-docs": e.trackerSetDocs, "export": e.trackerExport, "dedup": e.trackerDedup,
		"outcomes": e.trackerOutcomes, "archive": e.trackerArchive, "unarchive": e.trackerUnarchive, "followups": e.trackerFollowups, "followup": e.trackerFollowup,
	}
	if run, ok := cmds[args[0]]; ok {
		return run(ctx, args[1:])
	}
	fmt.Fprint(e.stderr, trackerUsage)
	return 2
}

// withTracker opens the workspace database for one command. Reads open it
// read-only and writes join it beside a running server (see openStore).
func (e *env) withTracker(ctx context.Context, cmd, wsFlag string, access storeAccess, fn func(*tracker.Tracker, *workspace.Workspace) int) int {
	root, err := e.workspacePath(wsFlag)
	if err != nil {
		return e.fail(cmd, err)
	}
	ws, st, release, err := e.openStore(ctx, root, access)
	if err != nil {
		return e.fail(cmd, err)
	}
	defer release()
	tr, err := tracker.New(st, tracker.Options{})
	if err != nil {
		return e.fail(cmd, err)
	}
	return fn(tr, ws)
}

func (e *env) printJSON(v any) int {
	enc := json.NewEncoder(e.stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return 1
	}
	return 0
}

// wireAll converts a list for --json, which prints what the HTTP API answers.
func wireAll[T, U any](xs []T, conv func(T) U) []U {
	out := make([]U, len(xs))
	for i, x := range xs {
		out[i] = conv(x)
	}
	return out
}

func wireCounts(m map[string]int) map[string]wire.SafeInt {
	out := make(map[string]wire.SafeInt, len(m))
	for k, v := range m {
		out[k] = wire.SafeInt(v)
	}
	return out
}

// rowName names a tracker row as a person recognizes it.
func rowName(e tracker.Entry) string { return fmt.Sprintf("row %d (%s, %s)", e.ID, e.Company, e.Role) }

func parseID(text string) (int64, error) {
	id, err := strconv.ParseInt(text, 10, 64)
	if err != nil || id < 1 {
		return 0, fmt.Errorf("%q is not a row id; ids are positive integers", text)
	}
	return id, nil
}

// trackerRefuse names what is wrong with the arguments of a tracker
// subcommand and prints that subcommand's usage, not the whole group's.
func (e *env) trackerRefuse(sub, problem string) int {
	return e.refuseUsage("tracker "+sub, usageLines("tracker "+sub, trackerUsage), problem)
}

// trackerParse parses a subcommand that takes flags only.
func (e *env) trackerParse(tf trackerFlags, sub string, args []string) bool {
	if err := tf.fs.Parse(args); err != nil {
		return false
	}
	if tf.fs.NArg() > 0 {
		e.trackerRefuse(sub, fmt.Sprintf("unexpected argument %q", tf.fs.Arg(0)))
		return false
	}
	return true
}

// idArg parses the positional row id and checks the count of positional
// arguments, naming the first missing one from the usage (<status>).
func (e *env) idArg(cmd string, pos []string, want int) (int64, bool) {
	if len(pos) > want {
		e.trackerRefuse(cmd, fmt.Sprintf("unexpected argument %q", pos[want]))
		return 0, false
	}
	if len(pos) < want {
		missing := "<id>"
		if args := argWord.FindAllString(usageLines("tracker "+cmd, trackerUsage), -1); len(pos) < len(args) {
			missing = args[len(pos)]
		}
		e.trackerRefuse(cmd, missing+" is missing")
		return 0, false
	}
	id, err := parseID(pos[0])
	if err != nil {
		fmt.Fprintf(e.stderr, "applysmith tracker %s: %v\n", cmd, err)
		return 0, false
	}
	return id, true
}

var argWord = lazyregexp.New(`<[a-z-]+>`)

// parseAge reads "45d" or any time.ParseDuration value.
func parseAge(text string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(text, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 1 {
			return 0, fmt.Errorf("%q is not an age; use a number of days such as 45d", text)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(text)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%q is not an age; use a number of days such as 45d", text)
	}
	return d, nil
}

type listFlags struct {
	status, company, source, query, sort *string
	reached, movedBy, since              *string
	minScore                             *float64
	archived                             *bool
	page, pageSize, appliedWithin        *int
}

func addListFlags(fs *flag.FlagSet, paging bool) listFlags {
	l := listFlags{
		status: fs.String("status", "", "only these statuses, comma-separated"), company: fs.String("company", "", "company contains this text"),
		source: fs.String("source", "", "source contains this text"), appliedWithin: fs.Int("applied-within", 0, "only rows applied to in the last this many days"),
		minScore: fs.Float64("min-score", -1, "only rows scoring at least this"), archived: fs.Bool("archived", false, "include archived rows"),
		reached: fs.String("reached", "", "only rows moved to this status since --since"), movedBy: fs.String("moved-by", "", "only rows this actor moved since --since"),
		since: fs.String("since", "", "the local day, YYYY-MM-DD, --reached and --moved-by count from"),
	}
	if paging {
		l.query = fs.String("query", "", "free text over company and role")
		l.sort = fs.String("sort", "", "updated_desc (default), score_desc, date_desc, company_asc, role_asc or status_asc")
		l.page = fs.Int("page", 1, "page, from 1")
		l.pageSize = fs.Int("page-size", 25, "rows per page, 1 to 100")
	}
	return l
}

func (l listFlags) toQuery() (tracker.ListQuery, error) {
	statuses, err := statusesArg(*l.status)
	if err != nil {
		return tracker.ListQuery{}, err
	}
	q := tracker.ListQuery{Statuses: statuses, Company: *l.company, Source: *l.source, AppliedWithinDays: *l.appliedWithin, Archived: *l.archived,
		MovedBy: tracker.Actor(*l.movedBy), ReachedSince: *l.since}
	if q.Reached, err = statusesArg(*l.reached); err != nil {
		return tracker.ListQuery{}, err
	}
	if *l.minScore >= 0 {
		q.MinScore = l.minScore
	}
	if l.query != nil {
		q.Query, q.Sort, q.Page, q.PageSize = *l.query, *l.sort, *l.page, *l.pageSize
	}
	return q, nil
}

func (e *env) trackerList(ctx context.Context, args []string) int {
	tf := e.trackerFlags("list")
	lf := addListFlags(tf.fs, true)
	if !e.trackerParse(tf, "list", args) {
		return 2
	}
	lq, err := lf.toQuery()
	if err != nil {
		fmt.Fprintf(e.stderr, "applysmith tracker list: %v\n", err)
		return 2
	}
	return e.withTracker(ctx, "tracker list", *tf.ws, accessRead, func(tr *tracker.Tracker, _ *workspace.Workspace) int {
		page, err := tr.List(ctx, lq)
		if err != nil {
			return e.fail("tracker list", err)
		}
		if *tf.asJSON {
			return e.printJSON(trackerapi.WireCandidacyPage(page))
		}
		if page.Total == 0 {
			fmt.Fprintln(e.stdout, "no tracker rows match; `applysmith evaluate <url>` or `applysmith tracker add` adds one")
			return 0
		}
		w := newColumns(e.stdout)
		fmt.Fprintln(w, "ID\tDATE\tSCORE\tSTATUS\tCOMPANY\tROLE")
		for _, r := range page.Entries {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Date, scoreText(r), r.DisplayStatus(), r.Company, r.Role)
		}
		w.Flush()
		fmt.Fprintf(e.stdout, "page %d: %d shown, %d total\n", page.Page, len(page.Entries), page.Total)
		return 0
	})
}

func scoreText(r tracker.Entry) string {
	if r.Score == nil {
		return "-"
	}
	return fmt.Sprintf("%.1f", *r.Score)
}

func (e *env) trackerShow(ctx context.Context, args []string) int {
	tf := e.trackerFlags("show")
	pos, err := parseMixed(tf.fs, args)
	if err != nil {
		return 2
	}
	id, ok := e.idArg("show", pos, 1)
	if !ok {
		return 2
	}
	return e.withTracker(ctx, "tracker show", *tf.ws, accessRead, func(tr *tracker.Tracker, _ *workspace.Workspace) int {
		d, err := tr.Get(ctx, id)
		if err != nil {
			return e.fail("tracker show", err)
		}
		if *tf.asJSON {
			return e.printJSON(trackerapi.WireCandidacyDetail(d))
		}
		r := d.Entry
		fmt.Fprintf(e.stdout, "row %d: %s, %s\nstatus: %s\nscore: %s %s\ndate: %s\nurl: %s\nreport: %s\nnotes:\n%s\nevents:\n",
			r.ID, r.Company, r.Role, r.DisplayStatus(), scoreText(r), r.ScoreKind, r.Date, r.PostingURL, reportText(r), r.Notes)
		for _, ev := range d.Events {
			fmt.Fprintf(e.stdout, "  %s  %-10s  %s", ev.At, ev.Actor, ev.Kind)
			if ev.NewStatus != "" {
				fmt.Fprintf(e.stdout, "  %s -> %s", ev.PreviousStatus, ev.NewStatus)
			}
			if n := ev.Undoes(); n != 0 {
				fmt.Fprintf(e.stdout, "  undoes event %d", n)
			}
			fmt.Fprintln(e.stdout)
		}
		return 0
	})
}

// statusArg reads a status in any letter case ("Applied"); a refusal goes
// out without its package label.
func statusArg(text string) (tracker.Status, error) {
	st, err := tracker.ParseStatus(strings.ToLower(text))
	return st, statusError(text, err)
}

// statusesArg reads a comma-separated list of statuses in any letter case.
func statusesArg(text string) ([]tracker.Status, error) {
	sts, err := tracker.ParseStatuses(strings.ToLower(text))
	return sts, statusError(text, err)
}

// statusError words a status the tracker does not know and names the
// nearest one, as a mistyped command is named.
func statusError(text string, err error) error {
	if err == nil {
		return nil
	}
	var names []string
	for _, s := range tracker.Statuses() {
		names = append(names, string(s))
	}
	msg := failureText("tracker", err)
	for _, part := range strings.Split(strings.ToLower(text), ",") {
		part = strings.TrimSpace(part)
		if near, ok := workspace.Closest(part, names); ok && !slices.Contains(names, part) {
			return fmt.Errorf("%s; did you mean %q?", msg, near)
		}
	}
	return errors.New(msg)
}

func reportText(r tracker.Entry) string {
	if r.ReportNumber == nil {
		return "-"
	}
	return strconv.FormatInt(*r.ReportNumber, 10)
}

func (e *env) trackerSetStatus(ctx context.Context, args []string) int {
	tf := e.trackerFlags("set-status")
	note := tf.fs.String("note", "", "a note to record with the change")
	on := tf.fs.String("on", "", "date of the application (YYYY-MM-DD), for applied")
	pos, err := parseMixed(tf.fs, args)
	if err != nil {
		return 2
	}
	id, ok := e.idArg("set-status", pos, 2)
	if !ok {
		return 2
	}
	to, err := statusArg(pos[1])
	if err != nil {
		fmt.Fprintf(e.stderr, "applysmith tracker set-status: %v\n", err)
		return 2
	}
	return e.withTracker(ctx, "tracker set-status", *tf.ws, accessShared, func(tr *tracker.Tracker, _ *workspace.Workspace) int {
		res, err := tr.SetStatus(ctx, tracker.StatusChange{ID: id, To: to, Actor: tracker.ActorUser, Note: *note, On: *on})
		if err != nil {
			return e.fail("tracker set-status", err)
		}
		if *tf.asJSON {
			return e.printJSON(trackerapi.WireStatusResult(res))
		}
		switch {
		case !res.Changed:
			fmt.Fprintf(e.stdout, "%s is already %s\n", rowName(res.Entry), res.Entry.DisplayStatus())
		case res.Correction:
			fmt.Fprintf(e.stdout, "%s is now %s (recorded as a correction)\n", rowName(res.Entry), res.Entry.DisplayStatus())
		default:
			fmt.Fprintf(e.stdout, "%s is now %s\n", rowName(res.Entry), res.Entry.DisplayStatus())
		}
		return 0
	})
}

func (e *env) trackerNote(ctx context.Context, args []string) int {
	tf := e.trackerFlags("note")
	undo := tf.fs.Bool("undo", false, "take back your latest note on the row")
	pos, err := parseMixed(tf.fs, args)
	if err != nil {
		return 2
	}
	switch {
	case len(pos) == 0:
		return e.trackerRefuse("note", "<id> is missing")
	case *undo && len(pos) > 1:
		return e.trackerRefuse("note", "--undo takes no note text")
	case !*undo && len(pos) == 1:
		return e.trackerRefuse("note", "<text> is missing")
	}
	id, ok := e.idArg("note", pos[:1], 1)
	if !ok {
		return 2
	}
	return e.withTracker(ctx, "tracker note", *tf.ws, accessShared, func(tr *tracker.Tracker, _ *workspace.Workspace) int {
		if *undo {
			return e.trackerUndoNote(ctx, tr, id, *tf.asJSON)
		}
		row, err := tr.AddNote(ctx, id, tracker.ActorUser, strings.Join(pos[1:], " "), 0)
		if err != nil {
			return e.fail("tracker note", err)
		}
		if *tf.asJSON {
			return e.printJSON(trackerapi.WireCandidacy(row))
		}
		fmt.Fprintf(e.stdout, "note added to %s\n", rowName(row))
		return 0
	})
}

// trackerUndoNote takes back the user's latest note against the version it
// read, so the line it prints is the one that left the row.
func (e *env) trackerUndoNote(ctx context.Context, tr *tracker.Tracker, id int64, asJSON bool) int {
	before, err := tr.Get(ctx, id)
	if err != nil {
		return e.fail("tracker note", err)
	}
	row, err := tr.UndoNote(ctx, id, tracker.ActorUser, before.Entry.Version)
	if err != nil {
		return e.fail("tracker note", err)
	}
	if asJSON {
		return e.printJSON(trackerapi.WireCandidacy(row))
	}
	was, now := strings.Split(before.Entry.Notes, "\n"), strings.Split(row.Notes, "\n")
	for i, line := range was {
		if i >= len(now) || now[i] != line {
			fmt.Fprintf(e.stdout, "took back your note on %s: %s\n", rowName(row), line)
			return 0
		}
	}
	fmt.Fprintf(e.stdout, "took back your latest note on %s\n", rowName(row))
	return 0
}

func (e *env) trackerAdd(ctx context.Context, args []string) int {
	tf := e.trackerFlags("add")
	company := tf.fs.String("company", "", "company")
	role := tf.fs.String("role", "", "role")
	url := tf.fs.String("url", "", "posting URL")
	status := tf.fs.String("status", "evaluated", "starting status")
	location := tf.fs.String("location", "", "place the posting lists")
	appliedAt := tf.fs.String("applied-at", "", "date applied (YYYY-MM-DD)")
	if !e.trackerParse(tf, "add", args) {
		return 2
	}
	if *company == "" || *role == "" {
		return e.trackerRefuse("add", "--company and --role are both required")
	}
	st, err := statusArg(*status)
	if err != nil {
		fmt.Fprintf(e.stderr, "applysmith tracker add: %v\n", err)
		return 2
	}
	return e.withTracker(ctx, "tracker add", *tf.ws, accessShared, func(tr *tracker.Tracker, _ *workspace.Workspace) int {
		row, err := tr.Add(ctx, tracker.AddRow{Company: *company, Role: *role, URL: *url, Status: st, Location: *location, AppliedAt: *appliedAt})
		var dup *tracker.DuplicateError
		if errors.As(err, &dup) {
			return e.fail("tracker add", fmt.Errorf("duplicate_candidacy: row %d already covers this posting (%s)", dup.ID, dup.Matched()))
		}
		if err != nil {
			return e.fail("tracker add", err)
		}
		if *tf.asJSON {
			return e.printJSON(trackerapi.WireCandidacy(row))
		}
		fmt.Fprintf(e.stdout, "row %d added as %s\n", row.ID, row.DisplayStatus())
		return 0
	})
}

func (e *env) trackerSetDocs(ctx context.Context, args []string) int {
	tf := e.trackerFlags("set-docs")
	resume := tf.fs.Bool("resume", false, "the resume PDF flag")
	cover := tf.fs.Bool("cover", false, "the cover letter flag")
	clear := tf.fs.Bool("clear", false, "clear the flag instead of setting it")
	pos, err := parseMixed(tf.fs, args)
	if err != nil {
		return 2
	}
	id, ok := e.idArg("set-docs", pos, 1)
	if !ok {
		return 2
	}
	if !*resume && !*cover {
		return e.trackerRefuse("set-docs", "--resume or --cover is required")
	}
	return e.withTracker(ctx, "tracker set-docs", *tf.ws, accessShared, func(tr *tracker.Tracker, _ *workspace.Workspace) int {
		var row tracker.Entry
		for _, d := range []struct {
			on  bool
			doc tracker.Doc
		}{{*resume, tracker.DocResume}, {*cover, tracker.DocCover}} {
			if !d.on {
				continue
			}
			var err error
			if row, err = tr.SetDocumentFlag(ctx, id, tracker.ActorUser, d.doc, !*clear); err != nil {
				return e.fail("tracker set-docs", err)
			}
		}
		if *tf.asJSON {
			return e.printJSON(trackerapi.WireCandidacy(row))
		}
		fmt.Fprintf(e.stdout, "%s: resume %t, cover letter %t\n", rowName(row), row.ResumePDF, row.CoverLetter)
		return 0
	})
}

const reportUsage = `usage:
  applysmith report override <number> [--json]

override lets a report below the 4.5 floor have a tailored résumé and cover-letter PDF; it is recorded on the tracker row.
every command takes --workspace <dir> and works beside a running server
`

func (e *env) reportCmd(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] != "override" {
		fmt.Fprint(e.stderr, reportUsage)
		return 2
	}
	fs := e.flagsWithUsage("report override", reportUsage+"\n")
	tf := trackerFlags{fs: fs, ws: fs.String("workspace", "", "workspace directory"), asJSON: fs.Bool("json", false, "print JSON")}
	pos, err := parseMixed(tf.fs, args[1:])
	if err != nil {
		return 2
	}
	if len(pos) != 1 {
		fmt.Fprint(e.stderr, reportUsage)
		return 2
	}
	number, err := parseID(pos[0])
	if err != nil {
		fmt.Fprintf(e.stderr, "applysmith report override: %v\n", err)
		return 2
	}
	return e.withTracker(ctx, "report override", *tf.ws, accessShared, func(tr *tracker.Tracker, _ *workspace.Workspace) int {
		row, err := tr.OverrideDocuments(ctx, number, tracker.ActorUser)
		if err != nil {
			return e.fail("report override", err)
		}
		if *tf.asJSON {
			return e.printJSON(trackerapi.WireCandidacy(row))
		}
		fmt.Fprintf(e.stdout, "report %d may have tailored PDFs; recorded on row %d (%s, %s)\n", number, row.ID, row.Company, row.Role)
		return 0
	})
}

func (e *env) trackerExport(ctx context.Context, args []string) int {
	tf := e.trackerFlags("export")
	lf := addListFlags(tf.fs, false)
	format := tf.fs.String("format", tracker.FormatMarkdown, "md or csv")
	out := tf.fs.String("out", "", "write to this file instead of standard output")
	if !e.trackerParse(tf, "export", args) {
		return 2
	}
	lq, err := lf.toQuery()
	if err != nil {
		fmt.Fprintf(e.stderr, "applysmith tracker export: %v\n", err)
		return 2
	}
	return e.withTracker(ctx, "tracker export", *tf.ws, accessRead, func(tr *tracker.Tracker, _ *workspace.Workspace) int {
		if *out == "" {
			if err := tr.Export(ctx, e.stdout, *format, lq); err != nil {
				return e.fail("tracker export", err)
			}
			return 0
		}
		if err := writeFileAtomic(*out, func(w io.Writer) error { return tr.Export(ctx, w, *format, lq) }); err != nil {
			return e.fail("tracker export", err)
		}
		return 0
	})
}

// writeFileAtomic fills a temporary file beside path and renames it over path
// only when fill succeeds, so a failed export never truncates a file the user
// already has.
func writeFileAtomic(path string, fill func(io.Writer) error) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := fill(f); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (e *env) trackerDedup(ctx context.Context, args []string) int {
	tf := e.trackerFlags("dedup")
	apply := tf.fs.Bool("apply", false, "merge the groups (default is a dry run)")
	if !e.trackerParse(tf, "dedup", args) {
		return 2
	}
	return e.withTracker(ctx, "tracker dedup", *tf.ws, dedupAccess(*apply), func(tr *tracker.Tracker, _ *workspace.Workspace) int {
		var groups []tracker.DedupGroup
		var err error
		if *apply {
			groups, err = tr.DedupApply(ctx, tracker.ActorUser)
		} else {
			groups, err = tr.DedupPlan(ctx)
		}
		if err != nil {
			return e.fail("tracker dedup", err)
		}
		if *tf.asJSON {
			return e.printJSON(trackerapi.WireDuplicates(groups))
		}
		for _, g := range groups {
			verdict := "would merge"
			switch {
			case g.NeedsUser:
				verdict = "needs-user (" + g.Reason + ")"
			case g.Dismissed:
				verdict = "kept apart (marked not duplicates on the dashboard)"
			case g.Merged:
				verdict = "merged"
			}
			fmt.Fprintf(e.stdout, "rows %v: keep %d as %s, %s [%s]\n", g.Members, g.Survivor, g.Status, verdict, g.Rule)
		}
		if len(groups) == 0 {
			fmt.Fprintln(e.stdout, "no duplicate rows")
		} else if !*apply {
			fmt.Fprintln(e.stdout, "dry run; pass --apply to merge")
		}
		return 0
	})
}

// archiveAccess and dedupAccess open read-only for the dry runs.
func archiveAccess(dry bool) storeAccess {
	if dry {
		return accessRead
	}
	return accessShared
}

func dedupAccess(apply bool) storeAccess {
	if apply {
		return accessShared
	}
	return accessRead
}

func (e *env) trackerArchive(ctx context.Context, args []string) int {
	tf := e.trackerFlags("archive")
	older := tf.fs.String("older-than", "30d", "age of the last event")
	skipped := tf.fs.Bool("include-skipped", false, "also archive skipped rows older than 60 days")
	dry := tf.fs.Bool("dry-run", false, "print the rows that would move")
	if !e.trackerParse(tf, "archive", args) {
		return 2
	}
	age, err := parseAge(*older)
	if err != nil {
		fmt.Fprintf(e.stderr, "applysmith tracker archive: %v\n", err)
		return 2
	}
	return e.withTracker(ctx, "tracker archive", *tf.ws, archiveAccess(*dry), func(tr *tracker.Tracker, _ *workspace.Workspace) int {
		rows, err := tr.Archive(ctx, tracker.ActorUser, tracker.ArchiveOptions{OlderThan: age, IncludeSkipped: *skipped, DryRun: *dry})
		if err != nil {
			return e.fail("tracker archive", err)
		}
		if *tf.asJSON {
			return e.printJSON(wireAll(rows, trackerapi.WireCandidacy))
		}
		verb := "archived"
		if *dry {
			verb = "would archive"
		}
		for _, r := range rows {
			fmt.Fprintf(e.stdout, "%s row %d: %s, %s (%s)\n", verb, r.ID, r.Company, r.Role, r.Status)
		}
		fmt.Fprintf(e.stdout, "%s %d rows\n", verb, len(rows))
		return 0
	})
}

func (e *env) trackerUnarchive(ctx context.Context, args []string) int {
	tf := e.trackerFlags("unarchive")
	pos, err := parseMixed(tf.fs, args)
	if err != nil {
		return 2
	}
	id, ok := e.idArg("unarchive", pos, 1)
	if !ok {
		return 2
	}
	return e.withTracker(ctx, "tracker unarchive", *tf.ws, accessShared, func(tr *tracker.Tracker, _ *workspace.Workspace) int {
		row, err := tr.Unarchive(ctx, id, tracker.ActorUser)
		if err != nil {
			return e.fail("tracker unarchive", err)
		}
		if *tf.asJSON {
			return e.printJSON(trackerapi.WireCandidacy(row))
		}
		fmt.Fprintf(e.stdout, "%s is no longer archived\n", rowName(row))
		return 0
	})
}
