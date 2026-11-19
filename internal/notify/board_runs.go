// Clean-room: docs/provenance.md#CR-RUNS-BOARD

package notify

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"applysmith/internal/identity"
	"applysmith/internal/tracker"
)

// runsWindow is how far back a run record without an entry is picked up.
const runsWindow = DoneRetention

// ScanBatch starts the batch id of the evaluations a scan, or the inbox drain
// that follows scans, queued; that run's id follows it. The batch is told
// through that run's roll-up.
const ScanBatch = "scan:"

type runRow struct {
	id        string
	kind      string
	status    string
	stage     string
	failure   string
	detail    string // a scheduled run's history detail: what the failure was
	summary   string // the one line a scheduled job returned when it ended
	outcome   string // the line a dashboard pass ended with
	input     string
	scheduled bool
	// agent says the Ask Agent started the run (agent_run_origins).
	agent bool
	// job is "role at company" of the tracker row a document run names.
	job        string
	updated    string
	finishedAt string
	// parent is the run id of the inbox drain or the scan that queued an
	// evaluation, identity the evaluation's posting identity key.
	parent   string
	identity string
	// review is the run id of the quick screen's review an evaluation in depth
	// answers, when the inbox drain started it (F-8).
	review string
}

// runCols are the columns of a run record as SyncRuns reads it.
const runCols = `r.id, r.kind, r.status, r.stage, r.failure_reason,
		COALESCE((SELECT h.detail FROM schedule_history h WHERE h.schedule_id = r.schedule_id AND h.run_id = r.id ORDER BY h.id DESC LIMIT 1), ''),
		COALESCE((SELECT h.summary FROM schedule_history h WHERE h.schedule_id = r.schedule_id AND h.run_id = r.id ORDER BY h.id DESC LIMIT 1), ''),
		r.outcome, r.input_json, r.schedule_id IS NOT NULL, EXISTS (SELECT 1 FROM agent_run_origins o WHERE o.run_id = r.id),
		CASE WHEN r.kind = 'documents' THEN COALESCE((SELECT t.role || ' at ' || t.company FROM tracker_entries t
			WHERE t.id = json_extract(r.input_json, '$.CandidacyID') AND t.role <> '' AND t.company <> ''), '') ELSE '' END,
		r.updated_at, COALESCE(r.finished_at, ''),
		CASE WHEN r.kind = 'evaluate' THEN COALESCE(json_extract(r.input_json, '$.Drain'), (SELECT json_extract(sr.record_json, '$.params.run_id') FROM scan_runs sr
			WHERE sr.id = json_extract(r.input_json, '$.ScanRun')), '') ELSE '' END,
		COALESCE((SELECT j.identity_key FROM evaluation_jobs j WHERE j.run_id = r.id), ''),
		CASE WHEN r.kind = 'evaluate' THEN COALESCE(json_extract(r.input_json, '$.Review'), '') ELSE '' END,
		r.created_at AS created`

// syncRunsQuery reads two sets and never the whole runs table, which is not
// pruned: the records added after a rowid (the newest record of the last sync
// that reached every record; all of them at the first sync of a process) that
// are inside the window and have no entry, and the records of the board's own
// entries that moved, are working, or turned out to be the Ask Agent's.
const syncRunsQuery = `SELECT ` + runCols + `
	FROM runs r
	WHERE r.rowid > ? AND r.created_at > ? AND NOT EXISTS (SELECT 1 FROM board_entries b WHERE b.id = r.id)
	UNION ALL
	SELECT ` + runCols + `
	FROM board_entries b CROSS JOIN runs r ON r.id = b.id
	WHERE r.updated_at || '|' || r.status <> b.ref_updated OR b.state = 'working'
		OR (b.origin = 'user' AND EXISTS (SELECT 1 FROM agent_run_origins o WHERE o.run_id = r.id))
	ORDER BY created`

// SyncRuns mirrors the run records of the long operations (evaluations, scans,
// documents and scheduled jobs) onto the board, so every one of them has an
// entry with the run id and no producer needs a status store of its own for
// display. It also keeps a mirrored working entry alive while its record has
// not ended, and reads every working entry again until it has moved: a sync
// out of time between a record's stamp and its move leaves it working. It
// returns the number of entries it changed. A record with no entry is looked
// for among those added since the last complete sync, so a sync costs what
// changed, not the size of the runs table.
func (b *Board) SyncRuns(ctx context.Context) (int, error) {
	var newest int64
	if err := b.st.Read().QueryRowContext(ctx, `SELECT COALESCE(max(rowid), 0) FROM runs`).Scan(&newest); err != nil {
		return 0, err
	}
	rows, err := b.st.Read().QueryContext(ctx, syncRunsQuery, b.runsSeen.Load(), ts(b.o.Now().Add(-runsWindow)))
	if err != nil {
		return 0, err
	}
	var todo []runRow
	for rows.Next() {
		var r runRow
		var created string
		if err := rows.Scan(&r.id, &r.kind, &r.status, &r.stage, &r.failure, &r.detail, &r.summary, &r.outcome, &r.input, &r.scheduled, &r.agent, &r.job, &r.updated, &r.finishedAt,
			&r.parent, &r.identity, &r.review, &created); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, r)
	}
	rows.Close()
	n := 0
	// Every new record gets its entry before any entry moves, so the
	// evaluations a scan queued are on the board when the scan's end is judged.
	// A sync out of time stops there, the record it was on included (its
	// write waited for a busy writer); what it did not reach follows at the
	// next.
	allOpened := true
	for _, r := range todo {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		_, opened, err := b.openRun(ctx, r)
		switch {
		case err != nil && ctx.Err() != nil:
			return n, ctx.Err()
		case err != nil:
			allOpened = false
			b.o.Log.Warn("a run could not be put on the board", "run", r.id, "error", err.Error())
		case opened:
			n++
		}
	}
	for _, r := range todo {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		changed, err := b.mirror(ctx, r)
		switch {
		case err != nil && ctx.Err() != nil:
			return n, ctx.Err()
		case err != nil:
			b.o.Log.Warn("a run could not be mirrored onto the board", "run", r.id, "error", err.Error())
		case changed:
			n++
		}
	}
	// Every record read has its entry or never gets one, so the next sync
	// starts after them. A record that could not be opened is read again.
	if allOpened {
		b.runsSeen.Store(newest)
	}
	return n, nil
}

// openRun opens the entry of a run record that has none. An evaluation a scan
// or the inbox drain queued joins that run's batch and takes its origin: it is
// that run's work, told through its roll-up (F-2). The drain's own evaluations
// include its evaluations in depth of reviews (F-8); a row the drain sends that
// a scan found is the drain's, not that scan's.
func (b *Board) openRun(ctx context.Context, r runRow) (Entry, bool, error) {
	kind, ok := entryKind(r)
	if !ok {
		return Entry{}, false, nil
	}
	cur, err := b.Get(ctx, r.id)
	if err != ErrNotFound {
		return cur, false, err
	}
	origin, batch := ownOrigin(r), ""
	if r.parent != "" {
		batch = ScanBatch + r.parent
		if p, err := b.Get(ctx, r.parent); err == nil {
			origin = p.Origin
		}
	}
	cur, err = b.Open(ctx, OpenArgs{ID: r.id, Kind: kind, Origin: origin, Title: b.title(ctx, r, kind), Stage: r.stage, BatchID: batch, Link: "/runs/" + r.id,
		Ref: "run:" + r.id, RefUpdated: r.updated + "|" + r.status})
	return cur, err == nil, err
}

func ownOrigin(r runRow) Origin {
	switch {
	case r.scheduled:
		return OriginSchedule
	case r.agent:
		return OriginAgent
	}
	return OriginUser
}

func (b *Board) mirror(ctx context.Context, r runRow) (bool, error) {
	kind, ok := entryKind(r)
	if !ok {
		return false, nil
	}
	origin := ownOrigin(r)
	stamp := r.updated + "|" + r.status
	cur, changed, err := b.openRun(ctx, r)
	if err != nil {
		return false, err
	}
	if r.review != "" {
		b.closeReview(ctx, r.review)
	}
	state, mv := desiredState(r)
	// A user-facing wait set by another producer is not overwritten by the
	// record's end.
	held := cur.Waiting() && state != Working
	move := !held && state != Working && (cur.State != state || cur.Reason != mv.Reason || cur.Outcome != mv.Outcome) && CanMove(cur.State, state)
	detail := ""
	if state != Working && !held {
		detail = clip(b.outcome(ctx, kind, r), MaxDetail)
	}
	// The agent records a run's origin just after the run exists, so a sync in
	// between opened the entry as the user's; the next sync corrects it.
	var fix *Origin
	if r.agent && cur.Origin == OriginUser {
		fix = &origin
	}
	// The stage, the origin and the title are written before the move, and the
	// outcome travels with the move, so the announcement the move makes says
	// what the run came to, for which posting, and is judged by who started it.
	// The title is read again at the end: an evaluation knows its posting's
	// names once its report exists.
	p := Patch{Origin: fix}
	if cur.Stage != r.stage {
		p.Stage = &r.stage
	}
	if move {
		if title := clip(b.title(ctx, r, kind), MaxTitle); title != cur.Title {
			p.Title = &title
		}
	}
	if cur.RefUpdated != stamp {
		p.RefUpdated = &stamp
	}
	if !move && detail != "" && cur.Detail != detail {
		p.Detail = &detail
	}
	if p != (Patch{}) {
		if _, err := b.Update(ctx, r.id, p); err != nil {
			return changed, err
		}
		changed = true
	}
	switch {
	case move:
		mv.Detail = detail
		if kind == KindEvaluate && state == Done && mv.Outcome == OutcomeOK {
			if err := b.resolveReviews(ctx, r, detail); err != nil {
				return changed, err
			}
		}
		if _, err := b.Move(ctx, r.id, state, mv); err != nil {
			return changed, err
		}
		changed = true
		if r.scheduled && mv.Outcome == OutcomeOK && b.o.Idle != nil && b.o.Idle(r.kind, r.summary) {
			if _, err := b.Ack(ctx, r.id); err != nil {
				return changed, err
			}
		}
	case state == Working && cur.State == Working:
		if err := b.Touch(ctx, r.id); err != nil {
			return changed, err
		}
	}
	return changed, nil
}

// closeReview closes the review the inbox drain sent to an evaluation in depth
// into that evaluation's entry once the entry exists: nothing waits for the
// user while it runs, and the drain's roll-up tells what it came to (F-2,
// F-8). The review is acknowledged too, so nothing announces it.
func (b *Board) closeReview(ctx context.Context, id string) {
	waits := func(e Entry) bool { return e.State == NeedsYou && e.Reason == "review" }
	e, err := b.Get(ctx, id)
	if err != nil || !waits(e) {
		return
	}
	_, err = b.Move(ctx, id, Done, MoveArgs{Outcome: OutcomeOK, Detail: "Sent to an evaluation in depth by the inbox evaluation, which tells what it came to.", If: waits})
	if err == nil {
		_, err = b.Ack(ctx, id)
	}
	if err != nil && !errors.Is(err, ErrStateChanged) && ctx.Err() == nil {
		b.o.Log.Warn("a review sent to an evaluation in depth stayed open", "entry", id, "error", err.Error())
	}
}

// resolveReviews finishes the reviews earlier evaluations of the same posting
// wait on once a later evaluation of it ended: that evaluation (the user's
// "Evaluate in full") is the answer. They are acknowledged too, so nothing
// announces them. It runs before the evaluation's own entry moves: a sync out
// of time here leaves that entry working, and the next sync resolves them.
func (b *Board) resolveReviews(ctx context.Context, r runRow, detail string) error {
	if r.identity == "" {
		return nil
	}
	ids, err := b.ids(ctx, `SELECT b.id FROM board_entries b JOIN evaluation_jobs j ON j.run_id = b.id
		WHERE j.identity_key = ? AND b.id <> ? AND b.state = 'needs-you' AND b.reason = 'review'`, r.identity, r.id)
	if err != nil {
		return err
	}
	for _, id := range ids {
		_, err := b.Move(ctx, id, Done, MoveArgs{Outcome: OutcomeOK, Detail: strings.TrimSpace("Evaluated again. " + detail)})
		if err == nil {
			_, err = b.Ack(ctx, id)
		}
		switch {
		case err != nil && ctx.Err() != nil:
			return ctx.Err()
		case err != nil:
			b.o.Log.Warn("a review a later evaluation answered stayed open", "entry", id, "error", err.Error())
		}
	}
	return nil
}

// outcome is the detail of a record that ended: its subsystem's sentence for
// it, else the summary a scheduled job returned when it succeeded (the inbox
// drain's counts) or the line a dashboard pass ended with, else the words for
// its failure.
func (b *Board) outcome(ctx context.Context, kind Kind, r runRow) string {
	if b.o.Outcome != nil {
		if s := b.o.Outcome(ctx, kind, r.id); s != "" {
			return s
		}
	}
	if r.status == "succeeded" {
		if r.summary != "" {
			return r.summary
		}
		if r.outcome != "" {
			return r.outcome
		}
	}
	return failureWords(r.failure, r.detail)
}

// desiredState is the board state a run record stands for.
func desiredState(r runRow) (State, MoveArgs) {
	switch r.status {
	case "succeeded":
		return Done, MoveArgs{Outcome: OutcomeOK}
	case "cancelled":
		return Done, MoveArgs{Outcome: OutcomeCancelled}
	case "review_required":
		return NeedsYou, MoveArgs{Reason: "review"}
	case "failed":
		if r.failure == "interrupted" {
			return Done, MoveArgs{Outcome: OutcomeInterrupted}
		}
		return Done, MoveArgs{Outcome: OutcomeFailed}
	}
	return Working, MoveArgs{}
}

// startedTasks are the run kinds the user starts from the Knowledge page or
// the interview drawer that the board shows as tasks.
var startedTasks = []string{"kb.ingest", "kb.brief", "kb.propose", "interview.coach"}

func entryKind(r runRow) (Kind, bool) {
	switch {
	case r.kind == "evaluate":
		return KindEvaluate, true
	case r.kind == "scan", strings.HasPrefix(r.kind, "discover."):
		return KindScan, true
	case r.kind == "documents":
		return KindDocuments, true
	case r.scheduled, slices.Contains(startedTasks, r.kind):
		return KindTask, true
	}
	// Fills the browser extension started are not units of work to watch.
	return "", false
}

// failureCodes words the failure codes runs record (the model router's failure
// kinds and the evaluator's reasons), because the board shows the failure as
// the entry's detail. A failure recorded as a sentence is shown as it is.
var failureCodes = map[string]string{
	"auth":                      "The model provider refused the API key. Replace it in Settings, Models, then retry.",
	"unconfigured":              "No model provider is set up for this task. Add a key in Settings, Models.",
	"no-route":                  "No model provider is set up for this task. Add a key in Settings, Models.",
	"network":                   "The model provider could not be reached. Check the internet connection, then retry.",
	"rate-limited":              "The model provider is rate-limiting this key. Retry in a few minutes.",
	"overloaded":                "The model provider is overloaded. Retry in a few minutes.",
	"timeout":                   "The work did not finish in time. Retry.",
	"stream-stalled":            "The model stopped answering mid-reply. Retry.",
	"runaway":                   "The model's reply ran far past its length limit. Retry.",
	"deadline-exhausted":        "The model did not answer in time. Retry.",
	"model-gone":                "The routed model is no longer offered. Pick another in Settings, Models.",
	"bad-request":               "The model provider refused the request.",
	"invalid-structured-output": "The model's answer could not be read. Retry.",
	"posting-unreadable":        "The posting could not be read.",
	"profile-unavailable":       "The profile could not be read. Check profile.yml in Profile.",
	"report-write-failed":       "The report could not be saved. Check the free disk space.",
}

// failureWords is the detail of a failed run: the words for its code, else
// the scheduler's own account of the failure (a scheduled job records only the
// code "error" on the run), else the code.
func failureWords(code, detail string) string {
	if code == "" {
		return ""
	}
	if w, ok := failureCodes[code]; ok {
		return w
	}
	if detail != "" {
		return detail
	}
	return code
}

var taskTitles = map[string]string{
	"gmail.sync": "Gmail sync", "cost.report": "Model cost report", "price.check": "Model price check",
	"knowledge.ingest": "Knowledge ingest", "kb.propose": "Propose stories", "memory.consolidate": "Memory consolidation", "agent.improve": "Ask Agent self-improvement",
	"inbox.evaluate": "Inbox evaluation", "registry.refresh": "Job board registry refresh", "liveness.audit": "Posting liveness audit",
	"leads.expand": "Careers pages of leads", "leads.evaluate": "Lead evaluation", "archive.sweep": "Tracker archive sweep",
	"signals.collect": "Hiring signals", "interview.coach": "Interview answer coaching",
}

// TaskTitle is the human name of a scheduler job kind, the title of a task
// that no schedule of its own kind names; it is "" for a kind it does not
// know. Every kind the scheduler registers, but a scan's, has one.
func TaskTitle(kind string) string { return taskTitles[kind] }

// documentWords names a document run by what it makes, so a pair does not
// read "Documents pair".
var documentWords = map[string]string{
	"resume": "Tailored resume", "cover-letter": "Cover letter", "pair": "Resume and cover letter",
	"pdf": "Document PDFs", "edit": "Document edit", "refine": "Document revision", "interview": "Interview prep",
}

// Names are what the workspace calls a posting; either may be "".
type Names struct{ Company, Role string }

// postingSources read the company and role of the postings among the keys
// bound at %s, as rows of (key, company, role), in the order the sources name
// them: a tracker row (by its key, else by an alias), an inbox row, the newest
// report. A source's queries are tried in turn for the keys it has not
// answered, and the first row of a key is its answer.
var postingSources = [][]string{
	{`SELECT identity_key, company, role FROM tracker_entries WHERE identity_key IN (%s)`,
		`SELECT a.identity_key, t.company, t.role FROM tracker_aliases a JOIN tracker_entries t ON t.id = a.entry_id WHERE a.identity_key IN (%s) ORDER BY t.id`},
	{`SELECT identity_key, company, role FROM inbox WHERE identity_key IN (%s) ORDER BY id`},
	{`SELECT identity_key, company, role FROM report_index WHERE identity_key IN (%s) ORDER BY report_number DESC`},
}

// PostingNames is what the workspace knows a posting by: the company, then the
// role, each from the first of postingSources that holds it. A placeholder an
// evaluation writes when the page named none is no name, and neither is a role
// that only repeats the company (a page whose heading is the employer's name).
// A role stored as a page's document title is shown without the job site's
// name and the company's segment (tracker.RoleName); the row keeps what it was
// given. A source that cannot be read names nothing.
func (b *Board) PostingNames(ctx context.Context, key string) Names {
	return b.postingNames(ctx, []string{key})[key]
}

// postingNames is PostingNames of many keys, at most one read per query of a
// source.
func (b *Board) postingNames(ctx context.Context, keys []string) map[string]Names {
	out := make(map[string]Names, len(keys))
	keys = slices.DeleteFunc(slices.Clone(keys), func(k string) bool { return k == "" })
	if len(keys) == 0 {
		return out
	}
	// found[key] holds the names each source gave it, in source order; a key
	// whose names are whole stays out of the sources after.
	found := map[string][]Names{}
	whole := func(k string) bool { n := mergeNames(found[k]); return n.Company != "" && n.Role != "" }
	for _, source := range postingSources {
		answered := map[string]bool{}
		for _, q := range source {
			var args []any
			for _, k := range keys {
				if !answered[k] && !whole(k) {
					args = append(args, k)
				}
			}
			if len(args) == 0 {
				break
			}
			rows, err := b.st.Read().QueryContext(ctx, fmt.Sprintf(q, strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")), args...)
			if err != nil {
				continue
			}
			for rows.Next() {
				var key string
				var n Names
				if rows.Scan(&key, &n.Company, &n.Role) == nil && !answered[key] {
					answered[key] = true
					found[key] = append(found[key], Names{Company: strings.TrimSpace(n.Company), Role: strings.TrimSpace(n.Role)})
				}
			}
			rows.Close()
		}
	}
	for key, list := range found {
		out[key] = mergeNames(list)
	}
	return out
}

// mergeNames is a posting's names from what the sources gave it, in order:
// the first company that names one, and the first role that does.
func mergeNames(list []Names) Names {
	named := func(s string) bool { return s != "" && !tracker.IsPlaceholderName(s) }
	var n Names
	for _, f := range list {
		if n.Company == "" && named(f.Company) {
			n.Company = f.Company
		}
	}
	for _, f := range list {
		role := tracker.RoleName(f.Role, n.Company)
		if n.Role == "" && named(role) && !strings.EqualFold(role, n.Company) {
			n.Role = role
		}
	}
	return n
}

// Named is the names a run recorded for its posting, a placeholder counting as
// none, with those it lacks filled from PostingNames. A recorded role is a
// stored name too (a bound report's), so it is shown as PostingNames shows one.
func (b *Board) Named(ctx context.Context, key string, own Names) Names {
	return named(own, b.PostingNames(ctx, key))
}

func named(own, known Names) Names {
	name := func(s string) string {
		if s = strings.TrimSpace(s); tracker.IsPlaceholderName(s) {
			return ""
		}
		return s
	}
	company := cmp.Or(name(own.Company), known.Company)
	return Names{company, tracker.RoleName(cmp.Or(name(own.Role), known.Role), company)}
}

// fitRole joins a title around its role: head + role + tail. A title over
// MaxTitle gives up part of the role, marked with an ellipsis, and keeps the
// rest; the company is in head or tail, so it is never the part cut. With no
// room for even one character of the role the title is returned whole and the
// caller's clip decides.
func fitRole(head, role, tail string) string {
	room := MaxTitle - utf8.RuneCountInString(head) - utf8.RuneCountInString(tail)
	if room >= 2 {
		role = clip(role, room)
	}
	return head + role + tail
}

// ApplyTitle is an application entry's title once its posting's company is
// known: the company and, when known, the role.
func ApplyTitle(n Names) string {
	if n.Role != "" {
		return fitRole("Apply to "+n.Company+": ", n.Role, "")
	}
	return "Apply to " + n.Company
}

// RenameWaits gives the waits already on the board, named when they opened or
// their run ended, the names the workspace knows of their postings now. An
// entry nothing better names keeps its title, nothing is announced, and a
// second pass renames nothing. It returns how many entries it renamed.
func (b *Board) RenameWaits(ctx context.Context) (int, error) {
	waits, err := b.query(ctx, `SELECT `+entryColumns+` FROM board_entries WHERE kind IN ('apply', 'evaluate') AND state IN ('needs-you', 'blocked')
		AND acked_at IS NULL`)
	if err != nil {
		return 0, err
	}
	n := 0
	for i, known := range b.knownTitles(ctx, waits) {
		e, title := waits[i], clip(known, MaxTitle)
		if title == "" || title == e.Title {
			continue
		}
		if _, err := b.Update(ctx, e.ID, Patch{Title: &title}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// knownTitles are, for each entry, the title what is known of its posting
// gives it: an application's from the names its run recorded, filled by the
// run's identity key; an evaluation's as its record's end would give it. An
// element is "" when that names nothing: no company, or nothing an
// evaluation's record lacked. It reads once per kind and source, not per entry.
func (b *Board) knownTitles(ctx context.Context, es []Entry) []string {
	type applied struct {
		key, link string
		own       Names
	}
	var applyIDs, evalIDs []string
	for _, e := range es {
		switch e.Kind {
		case KindApply:
			applyIDs = append(applyIDs, e.ID)
		case KindEvaluate:
			evalIDs = append(evalIDs, e.ID)
		}
	}
	applies, evals := map[string]applied{}, map[string]runRow{}
	for id, a := range b.rowsByID(ctx, applyIDs, `SELECT run_id, identity_key, company, role, posting_url FROM apply_runs WHERE run_id IN (%s)`, 5) {
		applies[id] = applied{a[0], a[3], Names{a[1], a[2]}}
	}
	for id, a := range b.rowsByID(ctx, evalIDs, `SELECT r.id, r.input_json, COALESCE(j.identity_key, '') FROM runs r LEFT JOIN evaluation_jobs j ON j.run_id = r.id WHERE r.id IN (%s)`, 3) {
		evals[id] = runRow{id: id, kind: "evaluate", input: a[0], identity: a[1]}
	}
	var keys []string
	for _, a := range applies {
		keys = append(keys, a.key)
	}
	for _, r := range evals {
		keys = append(keys, r.identity)
	}
	known := b.postingNames(ctx, keys)
	out := make([]string, len(es))
	for i, e := range es {
		switch e.Kind {
		case KindApply:
			a, ok := applies[e.ID]
			if !ok {
				continue
			}
			n := named(a.own, known[a.key])
			if u, err := url.Parse(a.link); n.Company == "" && err == nil && u.Hostname() != "" && strings.HasPrefix(e.Title, "Apply to "+u.Hostname()) {
				// An entry titled by its raw host takes the name the posting's address gives it now.
				n.Company = PostingSlug(a.link)
			}
			if n.Company != "" {
				out[i] = ApplyTitle(n)
			}
		case KindEvaluate:
			if r, ok := evals[e.ID]; ok && known[r.identity] != (Names{}) {
				if t := runTitle(r, KindEvaluate, known[r.identity]); t != runTitle(r, KindEvaluate, Names{}) {
					out[i] = t
				}
			}
		}
	}
	return out
}

// rowsByID reads a query of width text columns (its %s the ids' marks, its
// first column the id) for many ids at once and returns the other columns by id.
func (b *Board) rowsByID(ctx context.Context, ids []string, query string, width int) map[string][]string {
	out := map[string][]string{}
	if len(ids) == 0 {
		return out
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := b.st.Read().QueryContext(ctx, fmt.Sprintf(query, strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")), args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		cols := make([]string, width)
		ptrs := make([]any, width)
		for i := range cols {
			ptrs[i] = &cols[i]
		}
		if rows.Scan(ptrs...) != nil {
			continue
		}
		out[cols[0]] = cols[1:]
	}
	return out
}

// shown gives the apply and evaluation entries of a listing the title their
// postings' known names give them now, so an entry opened before its posting
// had a tracker row, or titled by a board slug or a host, reads like the rest.
// The stored title is left as it is.
func (b *Board) shown(ctx context.Context, es []Entry) []Entry {
	for i, known := range b.knownTitles(ctx, es) {
		if t := clip(known, MaxTitle); t != "" {
			es[i].Title = t
		}
	}
	return es
}

// Shown is the entry as every read that leaves the process shows it: the
// listings' titles (shown), for the single entries an answer carries.
func (b *Board) Shown(ctx context.Context, e Entry) Entry {
	return b.shown(ctx, []Entry{e})[0]
}

// jobSites name the job boards whose pages a posting is read from, by host.
var jobSites = map[string]string{"glassdoor.com": "Glassdoor", "linkedin.com": "LinkedIn", "indeed.com": "Indeed", "ziprecruiter.com": "ZipRecruiter"}

// PostingSlug is the name an address gives its posting's employer when
// nothing better is known: the board name of a supported ATS, the tenant of a
// Workday host (Workday's own careers site reads "Workday"), else the host.
// It is "" for an address that cannot be read.
func PostingSlug(raw string) string {
	if key, ok := identity.ATSKey(raw); ok {
		_, rest, _ := strings.Cut(key, ":")
		slug, _, _ := strings.Cut(rest, "/")
		return slug
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if tenant, ok := strings.CutSuffix(host, ".myworkdayjobs.com"); ok {
		tenant, _, _ = strings.Cut(tenant, ".")
		if tenant == "workday" {
			return "Workday"
		}
		return tenant
	}
	return host
}

// postingSite says where an evaluated posting is when no one names its
// company: " at " and the employer an ATS address names, " on " and a job
// board's or a host's name; "" for an address that cannot be read.
func postingSite(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	switch slug := PostingSlug(raw); {
	case host == "":
		return ""
	case slug != host:
		return " at " + slug
	}
	host = strings.TrimPrefix(host, "www.")
	for suffix, site := range jobSites {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return " on " + site
		}
	}
	return " on " + host
}

// title is the title of a run record's entry. An evaluation takes what the
// workspace knows of its posting where its row named nothing; a scheduled task
// takes the name of the schedule it is an occurrence of, the owner's words.
func (b *Board) title(ctx context.Context, r runRow, kind Kind) string {
	var known Names
	switch kind {
	case KindEvaluate:
		known = b.PostingNames(ctx, r.identity)
	case KindTask:
		if name := b.scheduleName(ctx, r.id); name != "" {
			return name
		}
	case KindScan:
		if src := scanSource(r); src != "" && b.o.ScanTitle != nil {
			if name := b.o.ScanTitle(src); name != "" {
				return "Scan " + name
			}
		}
	}
	return runTitle(r, kind, known)
}

// scanSource is the source a scan run reads: a scheduled scan's kind names it,
// a scan started by hand its request.
func scanSource(r runRow) string {
	if src, ok := strings.CutPrefix(r.kind, "discover."); ok {
		return src
	}
	var in struct{ Source string }
	_ = json.Unmarshal([]byte(r.input), &in)
	return in.Source
}

// scheduleName is the name of the schedule a run is an occurrence of: a
// schedule of the run's own kind, so the follow-up a schedule's run left (the
// inbox evaluation after a scan) is not titled as that schedule. It is ""
// when there is none.
func (b *Board) scheduleName(ctx context.Context, runID string) string {
	var name string
	_ = b.st.Read().QueryRowContext(ctx, `SELECT s.name FROM runs r JOIN schedules s ON s.id = r.schedule_id AND s.job_kind = r.kind WHERE r.id = ?`,
		runID).Scan(&name)
	return strings.TrimSpace(name)
}

func runTitle(r runRow, kind Kind, known Names) string {
	in := map[string]any{}
	_ = json.Unmarshal([]byte(r.input), &in)
	field := func(name string) string {
		for k, v := range in {
			if strings.EqualFold(k, name) {
				s, _ := v.(string)
				return s
			}
		}
		return ""
	}
	switch {
	case kind == KindScan:
		return strings.TrimSpace("Scan " + scanSource(r))
	case r.kind == "kb.ingest":
		return "Read " + field("repo")
	case r.kind == "kb.brief":
		return orStr("Brief for "+field("company"), "Company brief")
	case kind == KindEvaluate:
		// The workspace's name for the posting ranks over the request's, which
		// can be a lower-cased board slug.
		role, company := cmp.Or(known.Role, strings.TrimSpace(field("title"))), cmp.Or(known.Company, strings.TrimSpace(field("company")))
		where := ""
		if company != "" {
			where = " at " + company
		} else {
			where = postingSite(field("url"))
		}
		if role == "" && where == "" {
			return "Evaluate a posting"
		}
		return fitRole("Evaluate ", cmp.Or(role, "a posting"), where)
	case kind == KindDocuments:
		what := orStr(documentWords[field("action")], "Documents "+strings.TrimSpace(field("action")))
		if r.job != "" {
			return what + " for " + r.job
		}
		return what
	}
	return orStr(TaskTitle(r.kind), r.kind)
}

// RaiseArgs describe a task that waits for the user or is stopped.
type RaiseArgs struct {
	ID     string
	Kind   Kind
	Origin Origin
	Title  string
	State  State // NeedsYou or Blocked
	Reason string
	Detail string
	Link   string
}

// Raise puts an entry in the inbox. It opens the entry when it does not exist,
// brings a finished one back to work first when the move needs it, and does
// nothing when the entry already waits for the same reason.
func (b *Board) Raise(ctx context.Context, a RaiseArgs) (Entry, error) {
	e, err := b.Open(ctx, OpenArgs{ID: a.ID, Kind: a.Kind, Origin: a.Origin, Title: a.Title, Link: a.Link})
	if err != nil {
		return Entry{}, err
	}
	if e.State == a.State && e.Reason == a.Reason {
		return e, nil
	}
	if !CanMove(e.State, a.State) && !(e.State == a.State) {
		if e, err = b.Move(ctx, a.ID, Working, MoveArgs{}); err != nil {
			return Entry{}, err
		}
	}
	return b.Move(ctx, a.ID, a.State, MoveArgs{Reason: a.Reason, Detail: a.Detail})
}

// Resolve finishes an entry that waited for the user once the cause is gone. An
// entry that does not wait is left alone.
func (b *Board) Resolve(ctx context.Context, id string) error {
	e, err := b.Get(ctx, id)
	if err == ErrNotFound || (err == nil && !e.Waiting()) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = b.Move(ctx, id, Done, MoveArgs{Outcome: OutcomeOK})
	return err
}
