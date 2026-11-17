package apply

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"applysmith/internal/engine"
	"applysmith/internal/gates"
	"applysmith/internal/store"
	"applysmith/internal/vault"
)

// ResumeInput continues a run that stopped for the user, in the tab it kept.
type ResumeInput struct {
	RunID string
	TabID string
	// Input is the request the run was started from. Its caller, tool, submit
	// flag and explicit request are the run's own: a Done press is not an input
	// to any gate and changes none of them.
	Input Input
	// Recheck decides the run's submit gate again, from the original turn's
	// request, its source trust and the posting. A decision that is not an
	// allow keeps the run from pressing Submit.
	Recheck func(ctx context.Context) gates.Decision
	// HoldSubmit, when not empty, stops the run before the final Submit and
	// names why. A resume pressed from Telegram sets it, because a submit is
	// confirmed in the dashboard.
	HoldSubmit string
	// Read receives the fresh read of the page before the run goes on.
	Read func(PageGlance)
}

// HoldConfirmation is the board reason of a run that filled its form and stopped
// before the final Submit, to be confirmed by the user.
const HoldConfirmation = "confirmation"

// dryRunHold is why a dry run the user started waits at Submit in its kept tab.
const dryRunHold = "it was started to fill only; Submit this form submits it from the kept tab"

// PageGlance is what a fresh read of a tab shows, short enough to put in a
// conversation. It holds no form value.
type PageGlance struct {
	URL      string
	Title    string
	Headings []string
	Controls int
	SignIn   bool
	Bot      bool
}

const maxGlanceHeadings = 3

func glanceOf(st *PageState) PageGlance {
	g := PageGlance{URL: redactURL(st.URL), Title: strings.TrimSpace(st.Title), SignIn: findWall(st) != nil, Bot: BotInterstitial(st.Title, st.Lines) != ""}
	for _, h := range st.Headings {
		if h = strings.TrimSpace(h); h != "" && len(g.Headings) < maxGlanceHeadings {
			g.Headings = append(g.Headings, clip(h, 80))
		}
	}
	for _, in := range st.Inputs {
		if !in.Furniture && in.Type != "hidden" {
			g.Controls++
		}
	}
	return g
}

// Summary says in a few sentences what the page shows now.
func (g PageGlance) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "The page is %s", g.URL)
	if g.Title != "" {
		fmt.Fprintf(&b, ", titled %q", clip(g.Title, 80))
	}
	b.WriteString(".")
	if len(g.Headings) > 0 {
		b.WriteString(" Headings: " + strings.Join(g.Headings, "; ") + ".")
	}
	switch {
	case g.SignIn:
		b.WriteString(" It still shows a sign-in.")
	case g.Bot:
		b.WriteString(" It shows a bot check.")
	case g.Controls > 0:
		fmt.Fprintf(&b, " It has %d form control(s).", g.Controls)
	default:
		b.WriteString(" It has no form.")
	}
	return vault.Redact(b.String())
}

// Resume continues a run that stopped for the user. It reads the kept tab
// again, never the earlier snapshot, and walks the application from the page it
// shows: a sign-in the page shows complete is not repeated, a field whose value
// differs from what the run left is not written over, and every rule of a new
// run applies, the submit gate included. The run keeps its id, so the runs board
// entry that was waiting is the one that goes on.
func (r *Runner) Resume(ctx context.Context, in ResumeInput) *Run {
	r.live.Add(1)
	defer r.live.Add(-1)
	input := in.Input
	if input.Tool == "" {
		input.Tool = defaultTool
	}
	run := &Run{ID: in.RunID, Input: input, StartedAt: r.d.Now(), Build: r.d.Build, Outcome: Outcome{Kind: outcomeRunning}}
	run.MessageHash = input.UserMessageHash
	if input.UserMessage != "" {
		run.MessageHash = gates.MessageHash(r.d.Salt, input.UserMessage)
	}
	if key, stp := IdentityKey(input.URL); stp == nil {
		run.Identity = key
	}
	if was := loadRecord(ctx, r.d.Store, in.RunID); !was.StartedAt.IsZero() {
		run.StartedAt = was.StartedAt
		for _, l := range was.Stages {
			if l.Status != StatusSkipped && l.Stage != StageRecord && l.Stage != StageLearn {
				run.Stages = append(run.Stages, l)
			}
		}
		run.Pages, run.Signins, run.Captcha = was.Pages, was.Signins, was.Captcha
	}
	e := &Exec{r: r, Run: run, Ctx: ctx, resume: &in, reread: len(run.Pages), solved: run.Captcha}
	b, berr := loadBounds(ctx, r.d.Store, in.RunID)
	e.spent, e.resends, e.resets = b.Solves, b.Resends, b.Resets
	stopPing := r.ping(e)
	defer stopPing()
	r.persist(e)
	if berr != nil {
		// What the earlier leg spent is unknown, so nothing more is spent.
		r.fail(e, &stop{class: ClassEnvironment, reason: "the run's earlier bounds could not be read: " + berr.Error(), at: StagePreflight})
	} else {
		r.exec(e)
	}
	r.finish(e)
	return run
}

// resumePages is the browser stages of a resumed run: the kept tab is taken
// over instead of opened, and the walk starts at the page it shows.
func (r *Runner) resumePages(ctx context.Context, e *Exec) (res *stop) {
	lost := func(reason string) *stop {
		return &stop{class: ClassEnvironment, board: "lane-unavailable", reason: reason, at: StageAccount}
	}
	act, ok := windowOf(e).(ResumableWindow)
	if !ok {
		return lost("the apply window cannot take over a kept tab")
	}
	s := &session{r: r, e: e, win: act}
	e.sess = s
	s.docs = newDocResumes(r, e)
	start := r.d.Now()
	tab, err := act.Reclaim(ctx, e.resume.TabID, engine.Host{Matcher: r.matcherFor(e.Run.ID), Resumes: s.docs, StageDir: r.d.Page.StageDir})
	if err != nil {
		r.record(e, StageAccount, StatusFailed, "the kept tab could not be taken over: "+err.Error(), start)
		return lost("the tab kept for you is gone: " + err.Error())
	}
	s.fl = &fillLog{}
	s.tab, s.page = tab, &loggedFiller{Filler: tab.Page(), log: s.fl}
	defer func() {
		if p := recover(); p != nil {
			res = internalError(p, debug.Stack())
		}
		s.end(ctx, res)
	}()

	if err := s.takeBack(ctx); err != nil {
		r.record(e, StageAccount, StatusFailed, "the kept tab could not be read: "+err.Error(), start)
		return lost("the tab kept for you can no longer be read: " + err.Error())
	}
	st, err := Probe(ctx, s.tab)
	if err != nil {
		r.record(e, StageAccount, StatusFailed, "the kept tab could not be read: "+err.Error(), start)
		return lost("the tab kept for you can no longer be read: " + err.Error())
	}
	s.entry = st
	glance := glanceOf(st)
	if e.resume.Read != nil {
		e.resume.Read(glance)
	}
	if glance.SignIn {
		// The user pressed Done with the sign-in still showing. The run does not
		// sign in again: a second password attempt is not what Done asked for.
		r.record(e, StageAccount, StatusFailed, "resumed: the page still shows the sign-in", start)
		return &stop{class: ClassCredential, board: "sign-in", reason: "the page still shows the sign-in; finish it in the tab and press Done again", at: StageAccount}
	}
	r.record(e, StageAccount, StatusOK, "resumed in the kept tab: "+glance.Summary(), start)
	r.protectEdits(ctx, e, s)
	return r.walkOn(ctx, e, s)
}

func windowOf(e *Exec) Window {
	if e.Ver == nil {
		return nil
	}
	return e.Ver.Window
}

// resumeHold says why a resumed run that would submit must stop before the
// press, or "" when the submit gate allows it again. The decision is audited
// like any other gate decision of the run.
func (r *Runner) resumeHold(ctx context.Context, e *Exec) string {
	rs := e.resume
	if rs == nil {
		return ""
	}
	if rs.Recheck != nil {
		d := rs.Recheck(ctx)
		_ = r.audit(e, d, e.Run.Input.URL, false)
		if d.Verdict != gates.Allow && d.Verdict != gates.AllowWithTrace {
			return d.Reason
		}
	}
	return rs.HoldSubmit
}

// ---- what the user changed by hand

// keptFields are the controls of the resumed page whose value is not what the
// run left, by document (a frame's id, "" for the top document) and selector.
// The value is the user's, held in memory for this run only; an empty value is
// a field the user cleared.
type keptFields map[string]map[string]string

// protectEdits compares the page with the snapshot taken when the run handed
// the tab over, document by document: the top one and every frame the
// snapshot names. A control that holds a value the snapshot does not account
// for, or no value where the snapshot had one, is the user's (s.keep). Until
// the resumed run fills a page, the snapshot's frames are the ones it covers
// (s.covered), so its own hand-over records them again.
func (r *Runner) protectEdits(ctx context.Context, e *Exec, s *session) {
	left := loadHandoff(ctx, r.d.Store, e.Run.ID)
	keep := keptFields{}
	docs := handoffDocs(left)
	for _, doc := range docs {
		rows, err := s.inspectDoc(ctx, doc)
		if err != nil {
			continue
		}
		for _, row := range rows {
			if row.InspectionOnly || row.Kind == "file" {
				continue
			}
			was, had := left[doc][row.Selector]
			if (row.Value != "" && (!had || was != r.fieldHash(e.Run.ID, row.Selector, row.Value))) || (row.Value == "" && had && was != r.fieldHash(e.Run.ID, row.Selector, "")) {
				if keep[doc] == nil {
					keep[doc] = map[string]string{}
				}
				keep[doc][row.Selector] = row.Value
			}
		}
	}
	s.keep, s.covered = keep, docs[1:]
}

// handoffDocs are the documents a snapshot covers: the top one, read even
// without a snapshot, then the frames it names.
func handoffDocs(left map[string]map[string]string) []string {
	docs := []string{""}
	for _, doc := range slices.Sorted(maps.Keys(left)) {
		if doc != "" {
			docs = append(docs, doc)
		}
	}
	return docs
}

// edited names the kept controls of each document, for the engine's fill.
func (k keptFields) edited() map[string][]string {
	out := map[string][]string{}
	for doc, fields := range k {
		out[doc] = slices.Sorted(maps.Keys(fields))
	}
	return out
}

func (r *Runner) fieldHash(runID, selector, value string) string {
	sum := sha256.Sum256([]byte(r.d.Salt + "\x00" + runID + "\x00" + selector + "\x00" + value))
	return hex.EncodeToString(sum[:8])
}

// filter takes out of a batch of writes to one document the ones aimed at a
// control the user changed, and answers each as skipped.
func (k keptFields) filter(doc string, res []engine.Resolution) (pass []engine.Resolution, skipped []engine.AnswerResult) {
	if len(k[doc]) == 0 {
		return res, nil
	}
	for _, x := range res {
		if _, mine := k[doc][x.Selector]; mine {
			skipped = append(skipped, engine.AnswerResult{Selector: x.Selector, Outcome: "skipped", Reason: "user-edited"})
			continue
		}
		pass = append(pass, x)
	}
	return pass, skipped
}

const maxHandoffFields = 400

// handoffUsers stands in the snapshot for a control this leg of the run left to
// the user: no value hashes to it, so the next leg takes the control for the
// user's too, whatever it holds by then.
const handoffUsers = "user"

// saveHandoff records what the page holds as the run hands it over, document
// by document: the top one and every frame the page's fill covered. A fault
// is dropped: the stop is already decided.
func (s *session) saveHandoff(ctx context.Context) {
	defer func() { _ = recover() }()
	st := s.r.d.Store
	if st == nil || s.page == nil {
		return
	}
	id := s.e.Run.ID
	docs, n := map[string]map[string]string{}, 0
	for _, doc := range append([]string{""}, s.covered...) {
		rows, err := s.inspectDoc(ctx, doc)
		if err != nil {
			continue
		}
		fields := map[string]string{}
		for _, row := range rows {
			if n >= maxHandoffFields {
				break
			}
			if !row.InspectionOnly && row.Kind != "file" {
				fields[row.Selector] = s.r.fieldHash(id, row.Selector, row.Value)
				if _, mine := s.keep[doc][row.Selector]; mine {
					fields[row.Selector] = handoffUsers
				}
				n++
			}
		}
		docs[doc] = fields
	}
	if len(docs) == 0 {
		return
	}
	raw, _ := json.Marshal(docs)
	wctx := context.WithoutCancel(ctx)
	_ = st.Write(wctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(wctx, `INSERT INTO apply_handoffs (run_id, fields, created_at) VALUES (?, ?, ?)
			ON CONFLICT (run_id) DO UPDATE SET fields = excluded.fields, created_at = excluded.created_at`, id, string(raw), s.r.d.Now().UTC().Format(stampLayout))
		return err
	})
}

func loadHandoff(ctx context.Context, st *store.Store, runID string) map[string]map[string]string {
	if st == nil {
		return nil
	}
	var raw string
	if err := st.Read().QueryRowContext(ctx, `SELECT fields FROM apply_handoffs WHERE run_id = ?`, runID).Scan(&raw); err != nil {
		return nil
	}
	var out map[string]map[string]string
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// loadRecord reads what a run recorded before it stopped (its start, stage
// lines, page reports, sign-ins and captcha lines), so the resumed run's record
// continues it. A run with no row has a zero start.
func loadRecord(ctx context.Context, st *store.Store, runID string) Run {
	var was Run
	if st == nil {
		return was
	}
	var started, stages, pages, signins, captcha string
	if err := st.Read().QueryRowContext(ctx, `SELECT started_at, stages_json, pages_json, signins_json, captcha_json FROM apply_runs WHERE run_id = ?`, runID).
		Scan(&started, &stages, &pages, &signins, &captcha); err != nil {
		return was
	}
	at, err := time.Parse(stampLayout, started)
	if err != nil {
		return was
	}
	was.StartedAt = at
	_ = json.Unmarshal([]byte(stages), &was.Stages)
	_ = json.Unmarshal([]byte(pages), &was.Pages)
	_ = json.Unmarshal([]byte(signins), &was.Signins)
	_ = json.Unmarshal([]byte(captcha), &was.Captcha)
	return was
}
