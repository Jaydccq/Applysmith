package api

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"applysmith/internal/agent"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/applyapi"
	"applysmith/internal/api/knowledgeapi"
	"applysmith/internal/api/wire"
	"applysmith/internal/discovery"
	"applysmith/internal/documents"
	"applysmith/internal/evaluate"
	"applysmith/internal/gmail"
	"applysmith/internal/gmail/lifecycle"
	"applysmith/internal/kb"
	"applysmith/internal/llm"
	"applysmith/internal/notify"
	"applysmith/internal/scheduler"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
)

// narrator words what the runs did: the outcome sentence of every run's end on
// the board (and so in its announcement), the roll-up of a scan. It reads the
// records the subsystems wrote and invents nothing.
type narrator struct {
	st   *store.Store
	repo *discovery.Repo
	kb   *kb.Service
	docs *documents.Service
}

// outcome is the board's Outcome hook: what a mirrored evaluation or scan that
// ended came to. "" leaves the board's failure words.
func (n narrator) outcome(ctx context.Context, kind notify.Kind, runID string) string {
	switch kind {
	case notify.KindEvaluate:
		if res, _, found, err := evaluate.JobOutcome(ctx, n.st, runID); err == nil && found {
			return evaluationSentence(res)
		}
	case notify.KindScan:
		if rec, err := n.repo.ScanByRun(ctx, runID); err == nil {
			return scanSentence(rec)
		}
	case notify.KindTask:
		return n.ingestSentence(ctx, runID)
	case notify.KindDocuments:
		return n.documentsSentence(ctx, runID)
	}
	return ""
}

// ingestSentence is what a repository read that succeeded came to, from the
// repository's saved report. A read that found nothing new saved nothing, so a
// report older than the run is the one it left.
func (n narrator) ingestSentence(ctx context.Context, runID string) string {
	var kind, status, input, created string
	err := n.st.Read().QueryRowContext(ctx, `SELECT kind, status, input_json, created_at FROM runs WHERE id = ?`, runID).Scan(&kind, &status, &input, &created)
	var in struct{ ID int64 }
	if err != nil || kind != knowledgeapi.IngestKind || status != wire.RunSucceeded || json.Unmarshal([]byte(input), &in) != nil {
		return ""
	}
	repo, err := n.kb.GetRepo(ctx, in.ID)
	if err != nil {
		return ""
	}
	if parseStamp(repo.IngestedAt).Before(parseStamp(created)) {
		repo.Report.Status = kb.IngestUpToDate
	}
	return knowledgeapi.IngestSentence(repo, repo.Report)
}

// rollupBands are the words of what a scan's evaluations came to, in the order
// the roll-up lists them. The first bestBands are worth naming, and a roll-up
// names at most bestNamed evaluations: an announcement holds 200 characters
// (notify.MaxNoticeBody).
var rollupBands = []string{"Apply now", "Selective", "passed the quick screen", "to review", "wait for the day's spend limit", "skipped", "already evaluated", "closed", "failed"}

const bestBands, bestNamed = 3, 2

// scanRollup is the policy's ScanRollup hook: what the evaluations a scan or
// the inbox drain queued came to, after the scan's own sentence ("Evaluated: 1
// Apply now, 2 Selective, 5 skipped."; the drain says "8 evaluated: ..."), then
// the best of them by name (F-2). A review the day's spend limit holds back says
// so (F-8).
func (n narrator) scanRollup(ctx context.Context, parent notify.Entry, members []notify.Entry) string {
	counts := map[string]int{}
	total := 0
	type named struct {
		band  int
		score float64
		name  string
	}
	var best []named
	for _, m := range members {
		band, score := n.memberBand(ctx, m)
		if band == "" {
			continue
		}
		counts[band]++
		total++
		if i := slices.Index(rollupBands, band); i >= 0 && i < bestBands {
			best = append(best, named{i, score, strings.TrimPrefix(m.Title, "Evaluate ")})
		}
	}
	list := bandList(counts)
	if list == "" {
		return ""
	}
	said := strings.TrimSpace(parent.Detail + " Evaluated: " + list + ".")
	if parent.Kind != notify.KindScan {
		said = fmt.Sprintf("%d evaluated: %s.", total, list)
	}
	if len(best) == 0 {
		return said
	}
	slices.SortStableFunc(best, func(a, b named) int { return cmp.Or(cmp.Compare(a.band, b.band), cmp.Compare(b.score, a.score)) })
	var names []string
	for _, b := range best[:min(len(best), bestNamed)] {
		names = append(names, fmt.Sprintf("%s %.1f", b.name, b.score))
	}
	return said + " Best: " + strings.Join(names, ", ") + "."
}

// memberBand is where one evaluation of a roll-up ended and its score, read
// from its record; "" for one the roll-up does not count (a review the user
// already answered).
func (n narrator) memberBand(ctx context.Context, m notify.Entry) (string, float64) {
	if m.Waiting() && m.Reason != "review" {
		return "failed", 0
	}
	res, _, found, err := evaluate.JobOutcome(ctx, n.st, m.ID)
	score := 0.0
	if err == nil && res.Score != nil {
		score = *res.Score
	}
	switch {
	case m.Waiting() && err == nil && res.Explanation == evaluate.CapHeldReview:
		return "wait for the day's spend limit", score
	case m.Waiting():
		return "to review", score
	case err != nil || !found:
		return "failed", 0
	case res.Review:
		return "", 0
	}
	return resultBand(res), score
}

// resultBand is where a finished evaluation put its posting, in the words of
// rollupBands.
func resultBand(res evaluate.Result) string {
	score := 0.0
	if res.Score != nil {
		score = *res.Score
	}
	switch {
	case res.Closed:
		return "closed"
	case res.Outcome == evaluate.OutcomeFailed || res.Outcome == "":
		return "failed"
	case res.Review:
		return "to review"
	case res.Outcome == evaluate.OutcomeDuplicate:
		return "already evaluated"
	case res.Outcome == evaluate.OutcomeGated || res.Decision == evaluate.DecisionDrop || res.Score == nil:
		return "skipped"
	case score >= applyapi.ApplyNowFloor && res.Outcome == evaluate.OutcomeReported:
		return "Apply now"
	case score >= applyapi.ApplyNowFloor:
		return "passed the quick screen"
	case score >= applyapi.SelectiveFloor:
		return "Selective"
	}
	return "skipped"
}

// bandList says counts of rollupBands in their order: "1 Apply now, 5 skipped".
func bandList(counts map[string]int) string {
	var parts []string
	for _, band := range rollupBands {
		if c := counts[band]; c > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c, band))
		}
	}
	return strings.Join(parts, ", ")
}

// evaluationSentence says what an evaluation came to: the score with the lane
// it puts the posting in, and the report. A failure is left to the board's
// failure words, except one that could not read the posting: the board's words
// for that code say nothing of why, and the evaluation knows (a text too short,
// a page that needs the browser) and what to do about it.
func evaluationSentence(r evaluate.Result) string {
	report := ""
	if r.ReportNumber > 0 {
		report = fmt.Sprintf(", report %d", r.ReportNumber)
	}
	score := 0.0
	if r.Score != nil {
		score = *r.Score
	}
	switch {
	case r.Outcome == evaluate.OutcomeReported && r.Score != nil:
		return fmt.Sprintf("Scored %.1f (%s)%s.", score, scoreBand(score, true), report)
	case r.Outcome == evaluate.OutcomeScreened && r.Review:
		// The screen's reason, then why the review waits (F-8).
		said := []string{fmt.Sprintf("Open report %d: evaluate in depth or skip.", r.ReportNumber)}
		for _, s := range []string{evaluate.ReviewNote(r.ReviewReason), r.Explanation} {
			if s != "" {
				said = append(said, s)
			}
		}
		return strings.Join(said, " ")
	case r.Outcome == evaluate.OutcomeScreened && r.Decision == evaluate.DecisionDrop:
		return "Skipped by the quick screen" + report + "."
	case r.Outcome == evaluate.OutcomeScreened && r.Score != nil:
		return fmt.Sprintf("Quick screen %.1f (%s)%s.", score, scoreBand(score, false), report)
	case r.Outcome == evaluate.OutcomeGated:
		return "Skipped by your filters" + report + "."
	case r.Outcome == evaluate.OutcomeDuplicate:
		return "Already evaluated" + report + "."
	case r.Outcome == evaluate.OutcomeFailed && r.Reason == evaluate.ReasonUnreadable && r.Explanation != "":
		return strings.ToUpper(r.Explanation[:1]) + r.Explanation[1:] + "."
	}
	return ""
}

// scoreBand names where a score puts a posting, by the thresholds of the Apply
// Next lanes. A quick-screen score never makes a row Apply now: only a full
// evaluation ranks it there.
func scoreBand(score float64, final bool) string {
	switch {
	case score >= applyapi.ApplyNowFloor && final:
		return "Apply now"
	case score >= applyapi.ApplyNowFloor:
		return "passed; evaluate in full to rank it"
	case score >= applyapi.SelectiveFloor:
		return "Selective"
	}
	return "skip"
}

// scanStopWords word the reasons a scan ended without finishing.
var scanStopWords = map[string]string{
	"challenge": "the site showed a challenge page", discovery.ReasonNeedsSignIn: "the site needs you to sign in",
	discovery.ReasonCooldown: "the site is resting after a recent block", discovery.ReasonHostPressure: "the computer was too busy",
	discovery.ReasonNoCards: "the page layout no longer yields postings", discovery.ReasonLayoutChanged: "the page layout no longer yields postings",
	discovery.ReasonBudget: "its time ran out", discovery.ReasonNoKey: "its API key is missing",
	discovery.ReasonRegistryEmpty: "its list of boards is not downloaded yet; download it on Discovery, Sources and portals",
}

// scanSentence says what a scan came to: the counts of a finished one (with
// the task that had no model when it sent none to evaluation), the reason of
// one that stopped. The record's error text is never repeated: it
// can quote the page. A skipped scan's reason is the runtime's own sentence of
// what the scan lacks ("no saved searches for builtin in portals.yml"), so it
// is given as it is.
func scanSentence(rec discovery.Record) string {
	name := sourceTitle(rec.Source)
	c := rec.Counts
	queued := fmt.Sprintf("%d queued for evaluation", c.Queued)
	if rec.Unrouted != "" {
		queued += " (" + llm.NoModel("the "+rec.Unrouted+" task") + ")"
	}
	counts := fmt.Sprintf("%d found, %d new, %s", c.Discovered, c.Inserted, queued)
	switch rec.Status {
	case discovery.RunRunning:
		return ""
	case discovery.RunCompleted:
		return name + ": " + counts + "."
	}
	why, ok := scanStopWords[rec.Reason]
	switch {
	case ok:
	case rec.Status == discovery.RunSkipped && rec.Reason != "":
		why = "skipped (" + rec.Reason + ")"
	default:
		why = "it stopped before it finished"
	}
	if c.Discovered > 0 {
		return fmt.Sprintf("%s: %s; %s.", name, why, counts)
	}
	return name + ": " + why + "."
}

// ---- the day: what was done for the user today and what is left for them

// dayRange is one local day, from its midnight to now.
type dayRange struct {
	start, now time.Time
	date       string
}

func dayOf(now time.Time) dayRange {
	l := now.In(time.Local)
	start := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.Local)
	return dayRange{start: start, now: now, date: start.Format(time.DateOnly)}
}

// leftShown is how many waiting runs the day names one by one.
const leftShown = 5

// dayView is what was done for the user today, what is left for them and what
// waits on employers, with today's counts. Home's Today, the day summary, the
// morning brief, Telegram's /today and the Ask Agent's activity_today read it,
// so they say the same. told is the day said in words: only then do the
// waiting runs open with the question most of them ask, which routes every
// waiting run's questions; Home's Today shows no left lines.
func (s *Server) dayView(ctx context.Context, d *domain, now time.Time, told bool) (wire.DayView, error) {
	day := dayOf(now)
	n := d.narrator
	v := wire.DayView{Date: day.date, Did: []wire.DayLine{}, Left: []wire.DayLine{}, Employers: []wire.DayLine{}}
	var errs []error
	add := func(dst *[]wire.DayLine) func([]wire.DayLine, error) {
		return func(lines []wire.DayLine, err error) {
			*dst = append(*dst, lines...)
			errs = append(errs, err)
		}
	}
	// waiting counts the things left for the user that the lines stand for.
	waiting := 0
	left := func(lines []wire.DayLine, things int, err error) {
		add(&v.Left)(lines, err)
		waiting += things
	}
	add(&v.Did)(n.scanLines(ctx, day))
	add(&v.Did)(n.evaluationLines(ctx, day))
	add(&v.Did)(n.runLines(ctx, d.notify.board, day))
	add(&v.Did)(n.gmailLines(ctx, day))
	// An interview today or tomorrow leads, as on Home.
	left(s.interviewLines(ctx, now))
	// The question most waiting runs ask leads them: one answer moves them all.
	if told {
		add(&v.Left)(s.apply.AskedLine(ctx))
	}
	left(n.waitingLines(ctx, d.notify.board))
	lanes, err := s.readyLanes(ctx, day)
	errs = append(errs, err)
	if lanes.now > 0 {
		text := fmt.Sprintf("%d %s in Apply now", lanes.now, apikit.OneOr(lanes.now, "posting waits", "postings wait"))
		if lanes.fresh > 0 {
			text += fmt.Sprintf(", %d new today", lanes.fresh)
		}
		v.Left = append(v.Left, wire.DayLine{Text: text + ".", Link: "/apply-next?lane=apply-now"})
	}
	left(s.followupLines(ctx, d))
	left(mailLines(ctx, d, now))
	add(&v.Employers)(n.replyLines(ctx, day))
	stages, err := d.repo.StageCounts(ctx, day.start, now.Add(time.Minute))
	errs = append(errs, err)
	user, agent, err := n.appliedToday(ctx, day)
	errs = append(errs, err)
	v.Counts = wire.DayCounts{Found: apikit.Safe(stages.Found), Evaluated: apikit.Safe(stages.Evaluated), Ready: apikit.Safe(lanes.now + lanes.selective),
		AppliedByUser: apikit.Safe(user), AppliedByAgent: apikit.Safe(agent), Waiting: apikit.Safe(waiting)}
	return v, errors.Join(errs...)
}

// scanLines say what the scans found today, one line per source.
func (n narrator) scanLines(ctx context.Context, day dayRange) ([]wire.DayLine, error) {
	recs, _, err := n.repo.ScanPage(ctx, discovery.ScanFilter{From: day.start, Limit: 200})
	if err != nil {
		return nil, err
	}
	var order []string
	sum := map[string]*discovery.Record{}
	runs := map[string]int{}
	// The page is newest first: a source's newest record says how it stands.
	for _, rec := range recs {
		if rec.Status == discovery.RunRunning {
			continue
		}
		t, ok := sum[rec.Source]
		if !ok {
			cp := rec
			sum[rec.Source], t = &cp, &cp
			order = append(order, rec.Source)
		} else {
			t.Counts.Discovered += rec.Counts.Discovered
			t.Counts.Inserted += rec.Counts.Inserted
			t.Counts.Queued += rec.Counts.Queued
			if rec.Status == discovery.RunCompleted {
				t.Status = discovery.RunCompleted
			}
		}
		runs[rec.Source]++
	}
	var out []wire.DayLine
	for i := len(order) - 1; i >= 0; i-- {
		src := order[i]
		text := scanSentence(*sum[src])
		if runs[src] > 1 {
			text = strings.TrimSuffix(text, ".") + fmt.Sprintf(" (%d scans).", runs[src])
		}
		out = append(out, wire.DayLine{Text: text, Link: "/discovery?tab=monitor"})
	}
	return out, nil
}

// evaluationLines say what today's evaluations came to, in one line. It counts
// every evaluation that ended, a skip, a repeat and a failure included, so it
// runs higher than the day's evaluated count, which is the postings that got a
// report or a screening, each once; it opens the board's evaluation runs that
// ended today, the same runs.
func (n narrator) evaluationLines(ctx context.Context, day dayRange) ([]wire.DayLine, error) {
	results, err := evaluate.OutcomesSince(ctx, n.st, day.start)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, nil
	}
	counts := map[string]int{}
	type ranked struct {
		band   int
		score  float64
		report int64
	}
	var best []ranked
	for _, res := range results {
		band := resultBand(res)
		counts[band]++
		if i := slices.Index(rollupBands, band); i >= 0 && i < bestBands && res.Score != nil && res.ReportNumber > 0 {
			best = append(best, ranked{i, *res.Score, res.ReportNumber})
		}
	}
	text := fmt.Sprintf("Ran %d %s: %s.", len(results), apikit.OneOr(len(results), "evaluation", "evaluations"), bandList(counts))
	// The day's best postings by name, as a scan's roll-up names its own: the
	// counts alone leave the user to find which of 13 is worth the morning.
	slices.SortStableFunc(best, func(a, b ranked) int { return cmp.Or(cmp.Compare(a.band, b.band), cmp.Compare(b.score, a.score)) })
	var names []string
	for _, b := range best {
		if len(names) == dayBestNamed {
			break
		}
		var company, role string
		err := n.st.Read().QueryRowContext(ctx, `SELECT company, role FROM report_index WHERE report_number = ?`, b.report).Scan(&company, &role)
		if name := fmt.Sprintf("%s at %s %.1f", role, company, b.score); err == nil && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	if len(names) > 0 {
		text += " Best: " + strings.Join(names, ", ") + "."
	}
	return []wire.DayLine{{Text: text, Link: "/runs?kind=evaluate&since=" + day.date}}, nil
}

// dayBestNamed is how many of the day's best evaluations its line names.
const dayBestNamed = 3

// runLines say what today's applications and documents came to. A failed
// application is named without its detail, which can quote the page. Runs
// that came to the same words are one line with their count, opening the
// newest, as the left lines fold their repeats. A stopped application whose
// posting a later run got past (filled, submitted or still running) is left
// out: the later run tells the posting's day, as the inbox drops the stop.
func (n narrator) runLines(ctx context.Context, b *notify.Board, day dayRange) ([]wire.DayLine, error) {
	done, err := b.List(ctx, notify.Filter{States: []notify.State{notify.Done}, Kinds: []notify.Kind{notify.KindApply, notify.KindDocuments}, Limit: notify.MaxLimit})
	if err != nil {
		return nil, err
	}
	gotPast, err := n.stopsGotPast(ctx)
	if err != nil {
		return nil, err
	}
	var out []wire.DayLine
	at, runs := map[string]int{}, map[string]int{}
	for i := len(done) - 1; i >= 0; i-- {
		e := done[i]
		if e.FinishedAt.Before(day.start) || e.Outcome == notify.OutcomeCancelled || gotPast[e.ID] {
			continue
		}
		name, end := e.Title, ""
		switch {
		case e.Outcome != notify.OutcomeOK:
			end = " stopped before it finished."
		case e.Kind == notify.KindDocuments:
			end = " is ready."
		case strings.HasPrefix(e.Detail, "Submitted to "):
			name = e.Detail
		case e.Detail != "":
			end = ": " + e.Detail
		default:
			end = " finished."
		}
		key, link := name+end, cmp.Or(e.Link, "/runs/"+e.ID)
		runs[key]++
		if j, ok := at[key]; ok {
			out[j] = wire.DayLine{Text: fmt.Sprintf("%s (%d runs)%s", name, runs[key], end), Link: link}
			continue
		}
		at[key] = len(out)
		out = append(out, wire.DayLine{Text: key, Link: link})
	}
	return out, nil
}

// stopsGotPast names the stopped applications whose posting, by the identity
// key their records hold, has a later run that did not stop.
func (n narrator) stopsGotPast(ctx context.Context) (map[string]bool, error) {
	rows, err := n.st.Read().QueryContext(ctx, `SELECT a.run_id FROM apply_runs a WHERE a.outcome_kind = 'stopped' AND a.identity_key <> '' AND EXISTS (SELECT 1
		FROM apply_runs b WHERE b.identity_key = a.identity_key AND b.started_at > a.started_at AND COALESCE(b.outcome_kind, '') <> 'stopped')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// gmailLines say how Gmail moved the tracker today: each row not archived
// once, by the status Gmail last moved it to, so the count is the rows the
// tracker's movedBy=gmail list holds.
func (n narrator) gmailLines(ctx context.Context, day dayRange) ([]wire.DayLine, error) {
	rows, err := n.st.Read().QueryContext(ctx, `SELECT new_status, count(*) FROM tracker_events WHERE id IN (SELECT max(id) FROM tracker_events
		WHERE actor = 'gmail' AND kind IN ('created', 'status_changed', 'status_corrected') AND new_status <> '' AND at >= ?
		AND entry_id IN (SELECT id FROM tracker_entries WHERE archived_at IS NULL) GROUP BY entry_id) GROUP BY new_status ORDER BY new_status`,
		day.start.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	total := 0
	var parts []string
	for rows.Next() {
		var status string
		var c int
		if err := rows.Scan(&status, &c); err != nil {
			return nil, err
		}
		total += c
		parts = append(parts, fmt.Sprintf("%d %s", c, strings.ToUpper(status[:1])+status[1:]))
	}
	if err := rows.Err(); err != nil || total == 0 {
		return nil, err
	}
	return []wire.DayLine{{Text: fmt.Sprintf("Gmail moved %d tracker %s: %s.", total, apikit.OneOr(total, "row", "rows"), strings.Join(parts, ", ")), Link: "/tracker?movedBy=gmail&since=" + day.date}}, nil
}

// blockedWords say what stopped a blocked run.
var blockedWords = map[string]string{
	"credential-refused": "the saved sign-in was refused", "lockout": "the account is locked", "lane-unavailable": "the browser is not available",
	"key-missing": "a key is missing", "cap-reached": "a spending cap was reached", "no-connection": "there is no connection", "other": "something is missing",
}

// waitingLines name the runs that wait for the user, the newest stop first and
// the repeats of one stop in one line, and count every waiting run. A morning's
// brief names what came in since the last one; the stops it named before are
// counted after them instead of being named again every day.
func (n narrator) waitingLines(ctx context.Context, b *notify.Board) ([]wire.DayLine, int, error) {
	inbox, err := b.Inbox(ctx)
	if err != nil || len(inbox) == 0 {
		return nil, 0, err
	}
	stop, err := n.stopKeys(ctx, inbox)
	if err != nil {
		return nil, 0, err
	}
	var order []string
	repeats := map[string][]notify.Entry{}
	for _, e := range inbox {
		k := stop(e)
		if repeats[k] == nil {
			order = append(order, k)
		}
		repeats[k] = append(repeats[k], e)
	}
	slices.SortStableFunc(order, func(a, b string) int {
		ra, rb := repeats[a], repeats[b]
		return rb[len(rb)-1].StateAt.Compare(ra[len(ra)-1].StateAt)
	})
	var out []wire.DayLine
	more := len(inbox)
	for _, k := range order[:min(len(order), leftShown)] {
		g := repeats[k]
		more -= len(g)
		// The newest repeat owns the stop, as the board's fold has it.
		e, title := g[len(g)-1], g[len(g)-1].Title
		if len(g) > 1 {
			title += fmt.Sprintf(" (%d runs)", len(g))
		}
		var text string
		switch {
		case e.Kind == notify.KindEvaluate && e.Reason == "review":
			text = title + ": evaluate in depth or skip."
		case e.State == notify.Blocked:
			text = title + " is stopped: " + cmp.Or(blockedWords[e.Reason], "something is missing") + "."
		default:
			// A schedule's item is titled "<schedule> needs you" already.
			text = strings.TrimSuffix(title, " needs you") + " needs " + cmp.Or(needsYouWords[e.Reason], "something from you") + "."
		}
		out = append(out, wire.DayLine{Text: text, Link: cmp.Or(e.Link, "/runs/"+e.ID)})
	}
	if more > 0 {
		out = append(out, wire.DayLine{Text: fmt.Sprintf("%d more %s for you.", more, apikit.OneOr(more, "run waits", "runs wait")), Link: "/runs"})
	}
	return out, len(inbox), nil
}

// stopKeys keys each waiting entry by its stop, as the board's fold does: an
// application or an evaluation is the same stop only for the same posting, by
// the identity key its record holds, so two postings at one company stay two;
// any other kind is the same stop by title. An application or an evaluation
// whose record names no posting is a stop of its own.
func (n narrator) stopKeys(ctx context.Context, inbox []notify.Entry) (func(notify.Entry) string, error) {
	ids := make([]string, 0, len(inbox))
	for _, e := range inbox {
		ids = append(ids, e.ID)
	}
	list, _ := json.Marshal(ids)
	rows, err := n.st.Read().QueryContext(ctx, `SELECT 'apply', run_id, identity_key FROM apply_runs WHERE identity_key <> '' AND run_id IN (SELECT value FROM json_each(?))
		UNION ALL SELECT 'evaluate', run_id, identity_key FROM evaluation_jobs WHERE identity_key <> '' AND run_id IN (SELECT value FROM json_each(?))`, string(list), string(list))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	posting := map[string]string{}
	for rows.Next() {
		var kind, id, key string
		if err := rows.Scan(&kind, &id, &key); err != nil {
			return nil, err
		}
		posting[kind+"|"+id] = key
	}
	return func(e notify.Entry) string {
		work := "title|" + e.Title
		if e.Kind == notify.KindApply || e.Kind == notify.KindEvaluate {
			work = "run|" + e.ID
			if key, ok := posting[string(e.Kind)+"|"+e.ID]; ok {
				work = "posting|" + key
			}
		}
		return string(e.Kind) + "|" + string(e.State) + "|" + e.Reason + "|" + work
	}, rows.Err()
}

// laneTotals are the two lanes a run applies from, and the Apply now rows
// evaluated today.
type laneTotals struct{ now, selective, fresh int }

func (s *Server) readyLanes(ctx context.Context, day dayRange) (laneTotals, error) {
	var t laneTotals
	all, err := s.apply.ReadLanes(ctx, wire.LaneApplyNow, wire.LaneSelective)
	if err != nil {
		return t, err
	}
	for _, r := range all[wire.LaneApplyNow] {
		t.now++
		if r.Entry().Date == day.date {
			t.fresh++
		}
	}
	t.selective = len(all[wire.LaneSelective])
	return t, nil
}

// followupLines say how many follow-ups are due: a follow-up after applying
// and a check for a reply after a response. A row whose cadence is exhausted
// asks nothing of the user today. The tracker itself sends nothing; the day
// says it.
func (s *Server) followupLines(ctx context.Context, d *domain) ([]wire.DayLine, int, error) {
	pol, _ := s.cfg.Workspace.FollowupPolicy()
	all, err := d.tracker.Followups(ctx, pol, true)
	if err != nil {
		return nil, 0, err
	}
	var due []tracker.Suggestion
	for _, f := range all {
		if f.Kind == tracker.KindFollowUp || f.Kind == tracker.KindReplyCheck {
			due = append(due, f)
		}
	}
	if len(due) == 0 {
		return nil, 0, nil
	}
	text := fmt.Sprintf("%d follow-ups are due.", len(due))
	if len(due) == 1 {
		text = fmt.Sprintf("A follow-up to %s is due.", due[0].Company)
	}
	return []wire.DayLine{{Text: text, Link: "/tracker#tracker-followups"}}, len(due), nil
}

// mailLines say how much job mail waits in Gmail's Do now lane.
func mailLines(ctx context.Context, d *domain, now time.Time) ([]wire.DayLine, int, error) {
	if d.gmail == nil || !d.gmail.Service.Readable(ctx) {
		return nil, 0, nil
	}
	page, err := d.gmail.Repo.Inbox(ctx, gmail.InboxQuery{Lane: lifecycle.LaneDoNow, Limit: MaxInboxProbe}, now)
	if err != nil || len(page.Threads) == 0 {
		return nil, 0, err
	}
	c := len(page.Threads)
	return []wire.DayLine{{Text: fmt.Sprintf("%d job %s in Gmail %s you now.", c, apikit.OneOr(c, "email", "emails"), apikit.OneOr(c, "needs", "need")), Link: "/gmail?lane=do_now"}}, c, nil
}

// replyLines say how many applications wait for a reply, the rows applied to
// that no response has moved yet, and how long ago the oldest of them was sent,
// by the applied date its row records. The days are counted on the calendar of
// the clock the tracker dates its rows by.
func (n narrator) replyLines(ctx context.Context, day dayRange) ([]wire.DayLine, error) {
	rows, err := n.st.Read().QueryContext(ctx, `SELECT company, COALESCE(applied_at, '') FROM tracker_entries WHERE status = 'applied' AND archived_at IS NULL
		ORDER BY applied_at IS NULL, applied_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	c := 0
	var oldest, sent string
	for rows.Next() {
		var company, at string
		if err := rows.Scan(&company, &at); err != nil {
			return nil, err
		}
		c++
		if c == 1 {
			oldest, sent = company, at
		}
	}
	if err := rows.Err(); err != nil || c == 0 {
		return nil, err
	}
	text := fmt.Sprintf("Waiting for a reply: %d %s", c, apikit.OneOr(c, "application", "applications"))
	if from, err := time.Parse(time.DateOnly, sent); err == nil {
		today, _ := time.Parse(time.DateOnly, day.now.Format(time.DateOnly))
		ago := "today"
		if days := int(today.Sub(from).Hours() / 24); days > 0 {
			ago = fmt.Sprintf("%d %s ago", days, apikit.OneOr(days, "day", "days"))
		}
		text += fmt.Sprintf(", %s %s %s", apikit.OneOr(c, "sent to", "the oldest sent to"), oldest, ago)
	}
	return []wire.DayLine{{Text: text + ".", Link: "/tracker?status=applied"}}, nil
}

// interviewLines are the interviews in front of the user as Home's Needs you
// lists them: a stage still ahead today or tomorrow, and one over today whose
// debrief is not written. A stage's time is only ever its own, read from the
// invitation by the Gmail sync or entered by the user.
func (s *Server) interviewLines(ctx context.Context, now time.Time) ([]wire.DayLine, int, error) {
	found, err := s.interviewsSoon(ctx, now)
	if err != nil {
		return nil, 0, err
	}
	var lines []wire.DayLine
	for _, f := range found {
		what := f.item.Company + ", " + stageName(f.item.Stage.Type)
		link := interviewLink(f.item.CandidacyID)
		day := "tomorrow"
		if dayOf(f.at).date == dayOf(now).date {
			day = "today"
		}
		switch {
		case f.at.After(now):
			lines = append(lines, wire.DayLine{Text: fmt.Sprintf("Interview %s at %s: %s.", day, f.at.In(time.Local).Format("15:04"), what), Link: link})
		case !f.item.Debriefed:
			lines = append(lines, wire.DayLine{Text: "Write the debrief of today's interview: " + what + ".", Link: link})
		}
	}
	return lines, len(lines), nil
}

// stageName is a stage type as the dashboard shows it ("system_design" is
// "System design").
func stageName(typ string) string {
	name := strings.ReplaceAll(typ, "_", " ")
	if name == "" {
		return name
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

func interviewLink(candidacy wire.SafeInt) string {
	return fmt.Sprintf("/tracker?interview=%d", candidacy)
}

// appliedToday counts the rows not archived that became applied today, by who
// applied: the rows /tracker?reached=applied&since={today} lists.
func (n narrator) appliedToday(ctx context.Context, day dayRange) (user, agent int, err error) {
	rows, err := n.st.Read().QueryContext(ctx, `SELECT t.applied_by, count(DISTINCT e.entry_id) FROM tracker_events e JOIN tracker_entries t ON t.id = e.entry_id
		WHERE e.kind IN ('created', 'status_changed', 'status_corrected') AND e.new_status = 'applied' AND e.at >= ? AND t.archived_at IS NULL GROUP BY t.applied_by`, day.start.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var by sql.NullString
		var c int
		if err := rows.Scan(&by, &c); err != nil {
			return 0, 0, err
		}
		if by.String == string(tracker.ByAgent) {
			agent += c
		} else {
			user += c
		}
	}
	return user, agent, rows.Err()
}

// dayText is the view in plain text: the counts, what was done, what is left
// and what waits on employers. links adds each line's dashboard link, which
// the Ask Agent reads: a waiting run's line carries the run id run_questions
// takes (live, the agent knew "Apply to 1password needs an answer" and could
// only ask the user to find the run on the Runs page).
func dayText(v wire.DayView, links bool) string {
	c := v.Counts
	var b strings.Builder
	fmt.Fprintf(&b, "Today: %d new, %d evaluated, %d ready to apply, %d applied by you, %d by the agent.", c.Found, c.Evaluated, c.Ready, c.AppliedByUser, c.AppliedByAgent)
	part := func(head string, lines []wire.DayLine) {
		if len(lines) == 0 {
			return
		}
		b.WriteString("\n\n" + head)
		for _, l := range lines {
			b.WriteString("\n- " + l.Text)
			if links && l.Link != "" {
				b.WriteString(" (" + l.Link + ")")
			}
		}
	}
	part("Done for you:", v.Did)
	part("Left for you:", v.Left)
	if len(v.Did) == 0 && len(v.Left) == 0 {
		b.WriteString("\n\nNothing was done for you today, and nothing waits for you.")
	}
	part("Waiting on employers:", v.Employers)
	return b.String()
}

// activityToday serves the Ask Agent's activity_today with the day's text.
func (s *Server) activityToday(d *domain) agent.ActivityToday {
	return func(ctx context.Context) (string, error) {
		v, err := s.dayView(ctx, d, s.now(), true)
		if err != nil {
			return "", err
		}
		return dayText(v, true), nil
	}
}

func (s *Server) getDay(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	v, err := s.dayView(c.R.Context(), d, s.now(), false)
	return apikit.Reply{Data: v}, err
}

// ---- the day summary: once a day, through the announcement policy

const (
	// daySummaryHour is the local hour the day summary goes out. The evening
	// Gmail digest falls in the same slot and carries it.
	daySummaryHour = gmail.EveningHour
	// digestWait is how long the summary waits for that digest before it goes
	// out on its own.
	digestWait = 15 * time.Minute
	// daySummaryMeta keeps the claim of the day's summary.
	daySummaryMeta = "notify.day-summary"
)

// dayClaim is the day summary decided for a day: once decided it is never
// decided again, so a restart or a second process sends nothing more. Digest
// is the Gmail digest that carries it; Empty marks a day with nothing to say.
// The text is kept for the Telegram pages, read when the summary is sent.
type dayClaim struct {
	Day      string `json:"day"`
	Digest   int64  `json:"digest,omitempty"`
	Empty    bool   `json:"empty,omitempty"`
	Title    string `json:"title,omitempty"`
	Headline string `json:"headline,omitempty"`
	Text     string `json:"text,omitempty"`
}

// daySummary is the day view as the summary says it. Its title counts the
// things that wait for the user, not the lines that tell them, and the postings
// ready to apply apart. What waits on employers alone asks nothing of the user,
// so it makes no day worth a summary.
func daySummary(v wire.DayView) dayClaim {
	if len(v.Did) == 0 && len(v.Left) == 0 {
		return dayClaim{Day: v.Date, Empty: true}
	}
	c := v.Counts
	wait := "nothing waits for you"
	if c.Waiting > 0 {
		wait = fmt.Sprintf("%d %s for you", c.Waiting, apikit.OneOr(int(c.Waiting), "thing waits", "things wait"))
	}
	if c.Ready > 0 {
		wait += fmt.Sprintf(", %d ready to apply", c.Ready)
	}
	title := fmt.Sprintf("Today: %d new, %d evaluated, %d applied; %s", c.Found, c.Evaluated, c.AppliedByUser+c.AppliedByAgent, wait)
	var lines []string
	for _, part := range [][]wire.DayLine{v.Did, v.Left, v.Employers} {
		for _, l := range part {
			lines = append(lines, l.Text)
		}
	}
	return dayClaim{Day: v.Date, Title: title, Headline: strings.Join(lines, " "), Text: dayText(v, false)}
}

func dayLink(day string) string { return "/?day=" + day }

// claimDay records c under key (the day summary's or the morning brief's)
// unless the day has one already. made reports whether c is the day's claim
// now: this call recorded it, or it is the claim the same digest made before.
func claimDay(ctx context.Context, st *store.Store, key string, c dayClaim) (got dayClaim, made bool, err error) {
	err = st.Write(ctx, func(tx *sql.Tx) error {
		var raw string
		err := tx.QueryRowContext(ctx, `SELECT value FROM workspace_meta WHERE key = ?`, key).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && json.Unmarshal([]byte(raw), &got) == nil && got.Day == c.Day {
			made = c.Digest != 0 && got.Digest == c.Digest
			return nil
		}
		b, _ := json.Marshal(c)
		got, made = c, true
		_, err = tx.ExecContext(ctx, `INSERT INTO workspace_meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, string(b))
		return err
	})
	return got, made, err
}

func readDayClaim(ctx context.Context, st *store.Store, key string) (dayClaim, error) {
	var raw string
	err := st.Read().QueryRowContext(ctx, `SELECT value FROM workspace_meta WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return dayClaim{}, nil
	}
	var c dayClaim
	if err == nil {
		_ = json.Unmarshal([]byte(raw), &c)
	}
	return c, err
}

// daySummaryTimer sends the day summary at 18:00 local time, at most once a
// day and nothing on an empty day. It is a normal announcement: quiet hours
// hold it. While an evening Gmail digest is due it waits for the digest, which
// carries the summary in one push.
type daySummaryTimer struct {
	s *Server
	d *domain
}

func (daySummaryTimer) Name() string { return "day-summary" }
func (daySummaryTimer) Describe() string {
	return "The day summary at 18:00 local time: what was done for you today and what is left, once a day and never on an empty day"
}

func (daySummaryTimer) Status(context.Context, time.Time) scheduler.TimerStatus {
	return scheduler.TimerStatus{Active: true}
}

func (t daySummaryTimer) Tick(ctx context.Context, now time.Time) error {
	day := dayOf(now)
	l := day.start
	slot := time.Date(l.Year(), l.Month(), l.Day(), daySummaryHour, 0, 0, 0, time.Local)
	if now.Before(slot) || now.Sub(slot) > gmail.CatchUp {
		return nil
	}
	st := t.d.narrator.st
	if c, err := readDayClaim(ctx, st, daySummaryMeta); err != nil || c.Day == day.date {
		return err
	}
	if now.Sub(slot) < digestWait && (gmailDigestTimer{d: t.d}).Status(ctx, now).Active {
		return nil
	}
	v, err := t.s.dayView(ctx, t.d, now, true)
	if err != nil {
		return err
	}
	c, made, err := claimDay(ctx, st, daySummaryMeta, daySummary(v))
	if err != nil || !made || c.Empty {
		return err
	}
	return t.d.notify.policy.Announce(ctx, notify.Event{Key: "day-summary|" + c.Day, Kind: notify.EventDaySummary, Title: c.Title, Body: c.Headline, Link: dayLink(c.Day)})
}

// ---- the morning brief: once a day, through the announcement policy

const (
	// morningBriefHour is the local hour the morning brief goes out, the hour
	// of the morning Gmail digest.
	morningBriefHour = gmail.MorningHour
	// morningBriefLate is how long after its hour a missed brief still goes
	// out: past noon it is no longer the morning's.
	morningBriefLate = 4 * time.Hour
	// morningBriefMeta keeps the claim of the day's morning brief.
	morningBriefMeta = "notify.morning-brief"
)

// morningBrief is what waits for the user this morning: the day view's left
// lines, the runs that wait on them named. A morning with nothing left asks
// nothing of the user.
func morningBrief(v wire.DayView) dayClaim {
	if len(v.Left) == 0 {
		return dayClaim{Day: v.Date, Empty: true}
	}
	c := v.Counts
	var parts []string
	if c.Waiting > 0 {
		parts = append(parts, fmt.Sprintf("%d %s for you", c.Waiting, apikit.OneOr(int(c.Waiting), "thing waits", "things wait")))
	}
	if c.Ready > 0 {
		parts = append(parts, fmt.Sprintf("%d ready to apply", c.Ready))
	}
	lines := make([]string, len(v.Left))
	for i, l := range v.Left {
		lines[i] = l.Text
	}
	return dayClaim{Day: v.Date, Title: "This morning: " + strings.Join(parts, ", "), Headline: strings.Join(lines, " ")}
}

// morningBriefTimer sends the morning brief at 08:00 local time, at most once
// a day and nothing when nothing waits. It is a normal announcement: quiet
// hours hold it.
type morningBriefTimer struct {
	s *Server
	d *domain
}

func (morningBriefTimer) Name() string { return "morning-brief" }
func (morningBriefTimer) Describe() string {
	return "The morning brief at 08:00 local time: what waits for you today, once a day and never when nothing does"
}

func (morningBriefTimer) Status(context.Context, time.Time) scheduler.TimerStatus {
	return scheduler.TimerStatus{Active: true}
}

func (t morningBriefTimer) Tick(ctx context.Context, now time.Time) error {
	day := dayOf(now)
	l := day.start
	slot := time.Date(l.Year(), l.Month(), l.Day(), morningBriefHour, 0, 0, 0, time.Local)
	if now.Before(slot) || now.Sub(slot) > morningBriefLate {
		return nil
	}
	st := t.d.narrator.st
	if c, err := readDayClaim(ctx, st, morningBriefMeta); err != nil || c.Day == day.date {
		return err
	}
	v, err := t.s.dayView(ctx, t.d, now, true)
	if err != nil {
		return err
	}
	c, made, err := claimDay(ctx, st, morningBriefMeta, morningBrief(v))
	if err != nil || !made || c.Empty {
		return err
	}
	return t.d.notify.policy.Announce(ctx, notify.Event{Key: "morning-brief|" + c.Day, Kind: notify.EventMorningBrief, Title: c.Title, Body: c.Headline, Link: dayLink(c.Day)})
}

// ---- interview reminders: through the announcement policy

const (
	// interviewEveHour is the local hour of the day before a stage when its
	// reminder goes out, an hour after the day summary.
	interviewEveHour = 19
	// interviewLastCall is how long before a stage with no prep pack the second
	// reminder goes out.
	interviewLastCall = 2 * time.Hour
)

// reminderDue names the reminder of a stage at at that is due at now: "eve"
// from 19:00 of the day before it (on loc's calendar) to that day's end, "soon"
// in its last two hours while it has no prep pack, "" otherwise. Where both
// fall, the later one is due.
func reminderDue(at, now time.Time, loc *time.Location, packed bool) string {
	if !now.Before(at) {
		return ""
	}
	if !packed && at.Sub(now) <= interviewLastCall {
		return "soon"
	}
	a := at.In(loc)
	day := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, loc)
	eve := time.Date(a.Year(), a.Month(), a.Day()-1, interviewEveHour, 0, 0, 0, loc)
	if !now.Before(eve) && now.Before(day) {
		return "eve"
	}
	return ""
}

// interviewReminderTimer reminds the user of each stage interviewsSoon
// selects: once the evening before, and once more two hours before a stage
// with no prep pack. They are normal announcements: quiet hours hold them. The
// key names the stage, the reminder and the stage's time, so a reminder is
// made once and a stage moved to another time is reminded of again.
type interviewReminderTimer struct {
	s *Server
	d *domain
}

func (interviewReminderTimer) Name() string { return "interview-reminders" }
func (interviewReminderTimer) Describe() string {
	return "Interview reminders: at 19:00 the day before a recorded stage, and two hours before one with no prep pack"
}

func (interviewReminderTimer) Status(context.Context, time.Time) scheduler.TimerStatus {
	return scheduler.TimerStatus{Active: true}
}

func (t interviewReminderTimer) Tick(ctx context.Context, now time.Time) error {
	found, err := t.s.interviewsSoon(ctx, now)
	if err != nil {
		return err
	}
	var errs []error
	for _, f := range found {
		kind := reminderDue(f.at, now, time.Local, f.item.Packed)
		if kind == "" {
			continue
		}
		body := "No prep pack yet."
		if f.item.Packed {
			body = "Prep pack ready."
		}
		errs = append(errs, t.d.notify.policy.Announce(ctx, notify.Event{
			Key:   fmt.Sprintf("interview-reminder|%d|%s|%s|%s", f.item.CandidacyID, f.item.Stage.ID, kind, f.at.UTC().Format(time.RFC3339)),
			Kind:  notify.EventInterviewReminder,
			Title: fmt.Sprintf("Interview %s: %s, %s", f.at.In(time.Local).Format("Mon Jan 2 at 15:04"), f.item.Company, stageName(f.item.Stage.Type)),
			Body:  body, Link: interviewLink(f.item.CandidacyID),
		}))
	}
	return errors.Join(errs...)
}

// carryDay is the day summary an evening digest of today carries: the digest
// claims the day, so the timer sends nothing more. ok is false for any other
// digest, a day another digest or the timer already claimed, or an empty day.
func (s *Server) carryDay(ctx context.Context, dig gmail.Digest) (dayClaim, bool) {
	d, err := s.dom()
	if err != nil || dig.Slot.IsZero() {
		return dayClaim{}, false
	}
	now := s.now()
	slot := dig.Slot.In(time.Local)
	if slot.Hour() != daySummaryHour || slot.Format(time.DateOnly) != dayOf(now).date {
		return dayClaim{}, false
	}
	switch c, err := readDayClaim(ctx, d.narrator.st, daySummaryMeta); {
	case err != nil || (c.Day == dayOf(now).date && c.Digest != dig.ID):
		return dayClaim{}, false
	case c.Day == dayOf(now).date:
		// The digest's other channel claimed it already.
		return c, !c.Empty
	}
	v, err := s.dayView(ctx, d, now, true)
	if err != nil {
		s.log.Warn("the day summary could not join the evening digest", "error", err.Error())
		return dayClaim{}, false
	}
	c := daySummary(v)
	c.Digest = dig.ID
	got, made, err := claimDay(ctx, d.narrator.st, daySummaryMeta, c)
	return got, err == nil && made && !got.Empty
}

// dayPages are the Telegram pages of the day summary a link names, and of the
// one a digest carries.
func (s *Server) dayPages(ctx context.Context, d *domain, day string, digest int64) []string {
	c, err := readDayClaim(ctx, d.narrator.st, daySummaryMeta)
	if err != nil || c.Text == "" || (day != "" && c.Day != day) || (digest != 0 && c.Digest != digest) {
		return nil
	}
	return gmail.SplitPages(c.Text, gmail.TelegramPageChars)
}
