package apply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"applysmith/internal/answerbank/match"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
)

// An Operation is a stopped run's kept tab taken over for one call of the Ask
// Agent's apply tools: the page is read, filled, answered, left through a
// navigation control or reviewed, and the tab is kept again when the call ends.
// Every check the chain makes on those steps is the chain's own code: the fill
// is pageWork, an answer passes the checks of the ladder's rungs 2 and 3 and
// the fix planner, and a press goes through gates.PressControl at the walk
// stage. Nothing here submits; the run's own submit stage does that, on
// Resume, after its gates.
type Operation struct {
	r *Runner
	e *Exec
	s *session
	w *runWork
}

// runWork is what the apply tools remember of a run's page between two calls
// of one conversation. Nothing in it decides: each call reads the page again
// and takes the memory only when the page is the one it was made for.
type runWork struct {
	ps *pageState
	// engine is the engine run id of the fill, the key of the matcher's record.
	engine string
	// key is stateKey of the page the fill and the answers since made.
	key string
	jd  string
	// board is the posting as its board states it, read with jd: its location
	// places the posting for the matcher on every later call.
	board PostingRead
}

// ForgetWork drops what the apply tools remember of a run, once the run goes
// on by itself or is left to the user.
func (r *Runner) ForgetWork(runID string) { r.work.Delete(runID) }

const (
	// maxAnswerRows is how many answers one call may carry: the grounded rung's
	// page limit. Composed answers keep their own limit of maxComposedRows.
	maxAnswerRows = maxGroundedRows
	// maxShownRows bounds the page listing of answered fields.
	maxShownRows = 30
	// maxShownOptions bounds the options listed for one open field.
	maxShownOptions = 12
	// maxExcerpt bounds the posting text offered for jd: quotes.
	maxExcerpt = 2500
)

// Operate takes the run's kept tab over for one call. The caller holds the
// apply lane and ends the call with Close, which keeps the tab again. The
// request in is the run's own: it decides every audit row and nothing a tool
// says changes it.
func (r *Runner) Operate(ctx context.Context, in ResumeInput) (*Operation, error) {
	input := in.Input
	if input.Tool == "" {
		input.Tool = defaultTool
	}
	run := &Run{ID: in.RunID, Input: input, StartedAt: r.d.Now(), Build: r.d.Build, Outcome: Outcome{Kind: outcomeRunning}}
	key, stp := IdentityKey(input.URL)
	if stp != nil {
		return nil, errors.New(stp.reason)
	}
	run.Identity = key
	e := &Exec{r: r, Run: run, Ctx: ctx, resume: &in, noModel: true}
	if r.d.Reports != nil {
		if b, err := r.d.Reports.Bind(ctx, key); err == nil && b != nil {
			run.Report = b
		}
	}
	cls, stp := Classify(e.target())
	if stp != nil {
		return nil, errors.New(stp.reason)
	}
	run.ATS, run.Tenant, e.Cls = cls.ATS, cls.Tenant, cls
	win, err := r.d.Env.Attach(ctx)
	if err != nil {
		return nil, fmt.Errorf("the apply window is not available: %w", err)
	}
	act, ok := win.(ResumableWindow)
	if !ok {
		return nil, errors.New("the apply window cannot take over a kept tab")
	}
	e.Ver = &Verdict{Window: win}
	s := &session{r: r, e: e, win: act}
	e.sess = s
	s.docs = newDocResumes(r, e)
	tab, err := act.Reclaim(ctx, in.TabID, engine.Host{Matcher: r.matcherFor(e.Run.ID), Resumes: s.docs, StageDir: r.d.Page.StageDir})
	if err != nil {
		return nil, fmt.Errorf("the tab kept for the run is gone: %w", err)
	}
	s.fl = &fillLog{}
	s.tab, s.page = tab, &loggedFiller{Filler: tab.Page(), log: s.fl}
	o := &Operation{r: r, e: e, s: s}
	r.live.Add(1)
	if err := s.takeBack(ctx); err != nil {
		o.Close(ctx)
		return nil, fmt.Errorf("the tab kept for the run can no longer be read: %w", err)
	}
	v, _ := r.work.LoadOrStore(in.RunID, &runWork{})
	o.w = v.(*runWork)
	s.jd, s.board = o.w.jd, o.w.board
	// What the user changed by hand since the last call stays as it is.
	r.protectEdits(ctx, e, s)
	return o, nil
}

// Close keeps the tab again and records what the page holds as the run's own,
// so a later call or a resumed run finds nothing here that the user changed.
func (o *Operation) Close(ctx context.Context) {
	ctx = context.WithoutCancel(ctx)
	o.s.saveHandoff(ctx)
	if err := o.s.tab.Keep(); err == nil {
		o.s.handoff(ctx)
	}
	o.r.live.Add(-1)
}

func (o *Operation) legalOn() bool {
	return o.r.d.Page.Settings != nil && o.r.d.Page.Settings().AutoAcceptLegalDeclarations
}

// walker is the walk's own presser over this operation's tab.
func (o *Operation) walker() *walker {
	return &walker{r: o.r, e: o.e, s: o.s, tm: o.r.d.Page.Timing.withDefaults(), pages: 1,
		opened: map[string]bool{}, seen: map[string]int{}, decided: map[string]bool{}, revalidated: map[string]bool{}}
}

// leftToUser says why the page is not the agent's to work on: a sign-in or a
// bot check stands, which only the user clears.
func leftToUser(st *PageState) string {
	switch g := glanceOf(st); {
	case g.SignIn:
		return "The page shows a sign-in. Signing in is the user's: use apply_leave to hand the run back."
	case g.Bot:
		return "The page shows a bot check. Clearing it is the user's: use apply_leave to hand the run back."
	}
	if d := DetectChallenge(st); d != nil && !st.HasForm() && d.ChallengeOnly {
		return "A challenge stands where the page should be (" + d.Kind + "). Clearing it is the user's: use apply_leave to hand the run back."
	}
	return ""
}

// current is the memory of the fill when it was made for this page, else nil.
func (o *Operation) current(st *PageState) *pageState {
	if o.w.ps != nil && o.w.key == stateKey(st) {
		return o.w.ps
	}
	return nil
}

func (o *Operation) remember(ps *pageState, st *PageState) {
	o.w.ps, o.w.engine, o.w.key, o.w.jd, o.w.board = ps, ps.runID, stateKey(st), o.s.jd, o.s.board
}

// Page reads the page: its fields, what is left open and why, the controls the
// agent may press and the state of the checks. evidence adds what an answer
// may cite.
func (o *Operation) Page(ctx context.Context, evidence bool) (string, error) {
	st, err := Probe(ctx, o.s.tab)
	if err != nil {
		return "", err
	}
	return o.render(ctx, st, evidence)
}

// Fill fills the page the way the walk does, with no model call: the engine's
// fill with the matcher, the answer bank, the self-identification answers and
// the files, then the check, the deterministic answers and the identity
// review. What the fill leaves open is the agent's to answer.
func (o *Operation) Fill(ctx context.Context) (string, error) {
	st, err := Probe(ctx, o.s.tab)
	if err != nil {
		return "", err
	}
	if why := leftToUser(st); why != "" {
		return why, nil
	}
	if !st.HasForm() {
		return "The page shows no form to fill. Read it with apply_page; a control that opens the application is pressed with apply_press.", nil
	}
	stp := o.s.pageWork(ctx, o.walker(), st)
	if len(o.s.pages) == 0 {
		if stp != nil {
			return "The fill stopped: " + stp.reason, nil
		}
		return "The page was not filled.", nil
	}
	ps := o.s.pages[len(o.s.pages)-1]
	after, err := Probe(ctx, o.s.tab)
	if err != nil {
		return "", err
	}
	o.remember(ps, after)
	text, err := o.render(ctx, after, false)
	if stp != nil {
		text += "\nBlocked: " + stp.reason
	}
	return text, err
}

// Review checks the page as it stands: the identity review again, the
// advisory review of the written values by the apply-check model, and the
// readiness verdict over the page.
func (o *Operation) Review(ctx context.Context) (string, error) {
	st, err := Probe(ctx, o.s.tab)
	if err != nil {
		return "", err
	}
	ps := o.current(st)
	if ps == nil {
		return "This page was not filled in this conversation (or it changed since): call apply_fill first.", nil
	}
	o.s.refreshAfterFix(ctx, ps)
	// The review is the one model call an operation asks for by name.
	o.e.noModel = false
	o.s.review(ctx, ps)
	o.e.noModel = true
	var b strings.Builder
	fmt.Fprintf(&b, "Identity review: %d mismatch(es)%s.\n", len(ps.mismatch), labelsOf(ps.mismatch, func(m gates.IdentityMismatch) string { return m.Label }))
	fmt.Fprintf(&b, "Independent review of the written values: %s%s.\n", firstNonEmpty(ps.report.Review, "skipped"), labelsOf(ps.flags, func(f gates.LabeledFlag) string { return f.Label }))
	v := gates.SubmitReadiness(gates.ReadinessInput{Pages: []gates.PageReport{ps.gatesReport(1)}, Final: true, PostingLocation: o.s.postingLoc()})
	_ = o.r.audit(o.e, v.Decision, st.URL, false)
	if v.Allowed() {
		b.WriteString("Readiness of this page: every check passes.")
	} else {
		b.WriteString("Readiness of this page: not ready: " + v.Reason)
	}
	return b.String(), nil
}

func labelsOf[T any](items []T, label func(T) string) string {
	if len(items) == 0 {
		return ""
	}
	var ls []string
	for _, it := range items {
		ls = append(ls, fmt.Sprintf("%q", clip(label(it), 80)))
	}
	return ": " + strings.Join(ls, ", ")
}

// Press presses one control of the page by its label through the walk's own
// press: the label is read again at press time and gates.PressControl at the
// walk stage allows navigation only, so Submit, Withdraw and Discard are
// refused whatever the agent names. The page the press leads to is returned.
func (o *Operation) Press(ctx context.Context, label string) (string, error) {
	st, err := Probe(ctx, o.s.tab)
	if err != nil {
		return "", err
	}
	if why := leftToUser(st); why != "" {
		return why, nil
	}
	ps := o.current(st)
	if st.HasForm() && ps == nil {
		// The walk fills a page before it leaves it.
		return "Nothing was pressed: this page was not filled in this conversation (or it changed since). Call apply_fill first.", nil
	}
	if ps != nil {
		// An identity mismatch or an unreadable page ends the walk before its next step.
		v := gates.SubmitReadiness(gates.ReadinessInput{Pages: []gates.PageReport{ps.gatesReport(1)}, Final: false, PostingLocation: o.s.postingLoc()})
		if !v.Allowed() {
			_ = o.r.audit(o.e, v.Decision, st.URL, false)
			return "Nothing was pressed: " + v.Reason + ". Fix it with apply_answer or hand the run back with apply_leave.", nil
		}
	}
	c, found := pressable(st, label)
	switch {
	case found == 0:
		return fmt.Sprintf("Nothing was pressed: no control reads %q. Read the page with apply_page for the labels.", label), nil
	case found > 1:
		return fmt.Sprintf("Nothing was pressed: %d controls read %q. Use apply_leave if the page needs a person to choose.", found, label), nil
	}
	w := o.walker()
	moved, stp := w.press(ctx, st, c)
	switch {
	case stp != nil && stp.class == ClassEnvironment:
		return "", errors.New(stp.reason)
	case stp != nil:
		return "Nothing was pressed: " + stp.reason, nil
	case w.coveredBy != "":
		return fmt.Sprintf("Nothing was pressed: %q is covered by %q.", clip(label, 80), clip(w.coveredBy, 80)), nil
	}
	after, lstp := w.look(ctx, true)
	if lstp != nil {
		if after, err = Probe(ctx, o.s.tab); err != nil {
			return "", err
		}
	}
	text, err := o.render(ctx, after, false)
	if !moved {
		return fmt.Sprintf("Pressed %q, but the page did not change%s.\n%s", clip(label, 80), validationWords(after), text), err
	}
	o.w.ps = nil
	return fmt.Sprintf("Pressed %q.\n%s", clip(label, 80), text), err
}

// pressable finds the one control of the page that reads label, whatever its
// kind: the gate, not this lookup, decides whether it may be pressed. The
// control keeps the walk's final-Apply mark, so the gate sees the same control
// the submit stage would.
func pressable(st *PageState, label string) (Control, int) {
	want := normText(label)
	cats := Categorize(st, true)
	final := map[int]bool{}
	for _, group := range [][]Control{cats.Submit, cats.InFormApply} {
		for _, c := range group {
			if c.FinalApply {
				final[c.I] = true
			}
		}
	}
	var hits []Control
	for _, c := range st.Controls {
		if c.Chat || (c.Chrome && !c.Modal) {
			continue
		}
		if slices.ContainsFunc(c.Labels, func(l string) bool { return normText(l) == want }) {
			c.FinalApply = final[c.I]
			hits = append(hits, c)
		}
	}
	if len(hits) != 1 {
		return Control{}, len(hits)
	}
	return hits[0], 1
}

// AnswerRow is one answer the agent gives to a field of the page: the field's
// id from the page listing, the value (the exact text of an option for a
// choice) and the citations that ground it. Change writes a value the user's
// own words name over the one the field holds (the agent checks the words).
type AnswerRow struct {
	ID     string
	Value  string
	Cite   []string
	Change bool
}

// Answer writes the agent's answers, each checked first by the code the
// ladder's rungs use. A field is taken under the rungs' input filter
// (takeable); a cited answer then passes the grounded rung's checks
// (parseGrounded: the citation kinds, the option, the own-claim rule; pinBank
// for a saved answer; the support check for the rest) and a composed answer
// the composed rung's (composedExclusionReason, parseComposed); a flagged value
// is corrected only as the fix planner corrects one (fixWrite). What passes is
// written through the engine's writer and verified against the page; the
// identity review runs again on the page after the writes. A refused row
// comes back with its reason and the field stays open.
func (o *Operation) Answer(ctx context.Context, rows []AnswerRow) (string, error) {
	st, err := Probe(ctx, o.s.tab)
	if err != nil {
		return "", err
	}
	if why := leftToUser(st); why != "" {
		return why, nil
	}
	ps := o.current(st)
	if ps == nil {
		return "Nothing was written: this page was not filled in this conversation (or it changed since). Call apply_fill first.", nil
	}
	if len(rows) > maxAnswerRows {
		return fmt.Sprintf("Nothing was written: at most %d answers in one call.", maxAnswerRows), nil
	}
	verdict := make([]string, len(rows))
	pl := o.plan(ctx, ps, rows, verdict)
	ws := o.check(ctx, ps, pl, verdict)
	kept := o.s.writeAnswers(ctx, ps.runID, ps, ws)
	for _, k := range kept {
		for _, p := range pl.picked {
			if p.row == k.idx && verdict[p.at] == "" {
				verdict[p.at] = "written"
			}
		}
	}
	for _, w := range ws {
		if sel := ps.rows[w.idx].Selector; !slices.ContainsFunc(kept, func(k answerWrite) bool { return k.idx == w.idx }) {
			for _, p := range pl.picked {
				if p.row == w.idx && verdict[p.at] == "" {
					verdict[p.at] = "refused: the page did not take it (" + ps.why[sel] + ")"
				}
			}
		}
	}
	if len(kept) > 0 {
		// A written answer can open follow-up questions, and the identity review
		// reads the page as it is after the writes.
		o.s.reveal(ctx, ps)
		o.s.refreshAfterFix(ctx, ps)
	}
	after, err := Probe(ctx, o.s.tab)
	if err != nil {
		return "", err
	}
	o.w.key = stateKey(after)
	var b strings.Builder
	for i, r := range rows {
		fmt.Fprintf(&b, "- %s: %s\n", clip(r.ID, 80), verdict[i])
	}
	still := 0
	for i, r := range ps.rows {
		if open(r) && !ps.replaced(i) {
			still++
		}
	}
	fmt.Fprintf(&b, "%d of %d answer(s) written; %d required field(s) are still open on this page.", len(kept), len(rows), still)
	if len(ps.mismatch) > 0 {
		b.WriteString(" The identity review found a mismatch" + labelsOf(ps.mismatch, func(m gates.IdentityMismatch) string { return m.Label }) + ": the walk does not go on until it is fixed.")
	}
	return b.String(), nil
}

// picked ties a row of the call to the page row it answers.
type picked struct{ at, row int }

// answerPlan is the call's rows sorted by the path each takes.
type answerPlan struct {
	grounded, composed []*ladderRow
	cites              map[*ladderRow][]string
	values             map[*ladderRow]string
	at                 map[*ladderRow]int
	fixes              []answerWrite
	picked             []picked
}

// plan routes each row of the call to a path, refusing the ones no path takes.
func (o *Operation) plan(ctx context.Context, ps *pageState, rows []AnswerRow, verdict []string) *answerPlan {
	pl := &answerPlan{cites: map[*ladderRow][]string{}, values: map[*ladderRow]string{}, at: map[*ladderRow]int{}}
	byID := map[string]int{}
	for i, r := range ps.rows {
		byID[r.Selector] = i
	}
	res := &engine.PageResult{RunID: ps.runID}
	_, facts := groundedFacts(o.s.own(ctx))
	flagged := map[string]fixTarget{}
	for _, t := range o.s.fixTargets(res, ps) {
		if t.flagged {
			flagged[t.row.sel] = t
		}
	}
	seen := map[string]bool{}
	composed := 0
	for at, r := range rows {
		i, ok := byID[r.ID]
		value := strings.TrimSpace(r.Value)
		switch {
		case !ok:
			verdict[at] = "refused: no such field on this page; use an id from apply_page"
			continue
		case seen[r.ID]:
			verdict[at] = "refused: the field appears twice in this call"
			continue
		case value == "":
			verdict[at] = "refused: no value"
			continue
		}
		seen[r.ID] = true
		if t, ok := flagged[r.ID]; ok {
			// A value the review doubts is corrected with a profile fact alone.
			w, ok := fixWrite(t, facts, factKey(r.Cite), value)
			if !ok {
				verdict[at] = "refused: a flagged value is corrected only with a value a profile fact holds, cited as fact:<key>"
				continue
			}
			pl.fixes = append(pl.fixes, w)
			pl.picked = append(pl.picked, picked{at, i})
			continue
		}
		row := ps.rows[i]
		if r.Change {
			// The user's own words replace what the field holds, but a field
			// left to the user stays theirs to change on the page.
			if verdict[at] = o.change(ps, i, value, pl); verdict[at] == "" {
				pl.picked = append(pl.picked, picked{at, i})
			}
			continue
		}
		switch {
		case !open(row) || ps.replaced(i):
			verdict[at] = "refused: the field is not an open required field"
			continue
		case row.Outcome == "failed":
			verdict[at] = "refused: the fill's write to it failed; it is left to the user"
			continue
		}
		l, why, recorded := o.s.takeable(ps.runID, ps, i, o.legalOn(), false)
		if recorded {
			ps.setWhy(row.Selector, why)
		}
		if l == nil {
			verdict[at] = "refused: left to the user (" + why + ")"
			continue
		}
		pl.at[l], pl.values[l], pl.cites[l] = at, value, r.Cite
		pl.picked = append(pl.picked, picked{at, i})
		switch {
		case len(r.Cite) > 0:
			o.s.readKnowledge(ctx, l)
			pl.grounded = append(pl.grounded, l)
		case composedExclusionReason(l) != "":
			verdict[at] = "refused: " + reasonNotComposable + ": only an open-ended question is answered without a citation"
		case composed >= maxComposedRows:
			verdict[at] = fmt.Sprintf("refused: at most %d composed answers in one call", maxComposedRows)
		default:
			composed++
			pl.composed = append(pl.composed, l)
		}
	}
	return pl
}

// change plans a value the user's own words name over the one row i holds: a
// field the rungs may take, one box or one option, written over the page's or
// this run's value. It returns the refusal, "" when the write is planned.
func (o *Operation) change(ps *pageState, i int, value string, pl *answerPlan) string {
	row := ps.rows[i]
	if row.Outcome == "failed" {
		return "refused: the fill's write to it failed; it is left to the user"
	}
	l, why, recorded := o.s.takeable(ps.runID, ps, i, o.legalOn(), false)
	if recorded {
		ps.setWhy(row.Selector, why)
	}
	switch {
	case l == nil:
		return "refused: left to the user (" + why + ")"
	case match.ControlKind(l.kind) == match.KindCheckbox || l.multi:
		return "refused: a group of boxes is changed by the user on the page"
	}
	w := answerWrite{idx: i, value: value, rung: "user", overwrite: true, replace: true}
	if isOptionKind(l.kind) {
		k := slices.IndexFunc(l.options, func(o string) bool { return normText(o) == normText(value) })
		if k < 0 {
			return "refused: a change to a choice needs the exact text of one of its options"
		}
		w.value, w.option = l.options[k], l.options[k]
	}
	pl.fixes = append(pl.fixes, w)
	return ""
}

// factKey is the key of the first fact: citation, "" for none.
func factKey(cites []string) string {
	for _, c := range cites {
		if k, ok := strings.CutPrefix(strings.TrimSpace(c), "fact:"); ok {
			return strings.TrimSpace(k)
		}
	}
	return ""
}

// check runs the rungs' own checks over the planned rows, in the rungs' order,
// and returns what passed as writes.
func (o *Operation) check(ctx context.Context, ps *pageState, pl *answerPlan, verdict []string) []answerWrite {
	ws := slices.Clone(pl.fixes)
	own := o.s.own(ctx)
	if len(pl.grounded) > 0 {
		_, facts := groundedFacts(own)
		entries := groundedBank(own.Bank, nil, nil)
		jd := o.s.excerpt(ps)
		answers := o.s.pinBank(ctx, ps.runID, ps, parseGrounded(replyOf(pl.grounded, pl, true), pl.grounded, facts, entries, jd))
		if needsSupport(answers) {
			// The cited text must support a value that is not a saved answer's own.
			system, user := supportPrompt(answers, facts, jd)
			o.e.noModel = false
			text, why := o.e.ask(ctx, taskComplete, system, user, 512, false)
			o.e.noModel = true
			if why != "" {
				// The rung lets such an answer stand when its support check cannot
				// run; a model-free path to a write is not one the agent gets.
				for _, a := range answers {
					if a.entry == "" {
						a.row.why = "support-check-unavailable (" + why + ")"
					}
				}
				answers = slices.DeleteFunc(answers, func(a groundedAnswer) bool { return a.entry == "" })
			} else {
				answers = applySupport(text, answers)
			}
		}
		for _, a := range answers {
			ws = append(ws, answerWrite{idx: a.row.idx, value: a.value, option: a.option, rung: "agent", entry: a.entry, knowledge: a.kb})
		}
		refuse(pl, pl.grounded, answers, verdict)
	}
	if len(pl.composed) > 0 {
		answers := parseComposed(replyOf(pl.composed, pl, false), pl.composed, own)
		for _, a := range answers {
			ws = append(ws, answerWrite{idx: a.row.idx, value: a.value, option: a.option, rung: "agent"})
		}
		refuse(pl, pl.composed, answers, verdict)
	}
	return ws
}

// refuse gives each row a path took and the checks dropped the reason the
// check left on it.
func refuse(pl *answerPlan, rows []*ladderRow, kept []groundedAnswer, verdict []string) {
	for _, l := range rows {
		if !answeredIn(kept, l) {
			verdict[pl.at[l]] = "refused: " + firstNonEmpty(l.why, reasonDeclined)
		}
	}
}

// replyOf writes the agent's answers as the reply the rung's parser reads, so
// the parser is the rung's and not a copy of it.
func replyOf(rows []*ladderRow, pl *answerPlan, cited bool) string {
	var objs []map[string]any
	for n, l := range rows {
		o := map[string]any{"id": n + 1, "answer": pl.values[l]}
		if cited {
			o["cite"] = pl.cites[l]
		}
		objs = append(objs, o)
	}
	raw, _ := json.Marshal(objs)
	return string(raw)
}

// ---- the page as the agent reads it

// render writes what the agent needs of the page.
func (o *Operation) render(ctx context.Context, st *PageState, evidence bool) (string, error) {
	var b strings.Builder
	g := glanceOf(st)
	b.WriteString(g.Summary() + "\n")
	if why := leftToUser(st); why != "" {
		b.WriteString(why + "\n")
	}
	ps := o.current(st)
	var err error
	if ps == nil {
		err = o.listInspected(ctx, &b)
	} else {
		o.listRows(&b, ps)
	}
	cats := Categorize(st, true)
	nav := controlLabels(append(slices.Clone(cats.Nav), cats.Openers...))
	if len(nav) > 0 {
		b.WriteString("Controls apply_press may press: " + strings.Join(nav, " | ") + "\n")
	}
	if sub := controlLabels(append(slices.Clone(cats.Submit), cats.InFormApply...)); len(sub) > 0 {
		b.WriteString("Final controls, never pressed by a tool (the run's own submit stage presses them after its checks): " + strings.Join(sub, " | ") + "\n")
	}
	if v := validationWords(st); v != "" {
		b.WriteString("The page's messages" + v + "\n")
	}
	if evidence && ps != nil {
		o.listEvidence(ctx, &b, ps)
	}
	return strings.TrimRight(b.String(), "\n"), err
}

func controlLabels(cs []Control) []string {
	var out []string
	for _, c := range cs {
		if len(c.Labels) == 0 {
			continue
		}
		if l := fmt.Sprintf("%q", clip(c.Labels[0], 60)); !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	return out
}

// listInspected lists the controls the engine reads on a page the fill has
// not been run on in this conversation.
func (o *Operation) listInspected(ctx context.Context, b *strings.Builder) error {
	rows, err := o.s.inspect(ctx)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		b.WriteString("No form controls were read.\n")
		return nil
	}
	b.WriteString("Not filled yet in this conversation: apply_fill reads and fills the page. Controls:\n")
	for i, r := range rows {
		if i == maxShownRows {
			fmt.Fprintf(b, "... and %d more\n", len(rows)-i)
			break
		}
		fmt.Fprintf(b, "- %s | %q | %s%s\n", r.Selector, clip(r.Label, 100), r.Kind, map[bool]string{true: " | required", false: ""}[r.Required])
	}
	return nil
}

// listRows lists the page the fill read: what is open and whether the agent
// may answer it, then what is filled, then the state of the checks.
func (o *Operation) listRows(b *strings.Builder, ps *pageState) {
	legalOn := o.legalOn()
	filled := 0
	var opened []string
	for i, r := range ps.rows {
		if r.Outcome == "filled" || r.Outcome == "prefilled" {
			filled++
		}
		if !open(r) || ps.replaced(i) {
			continue
		}
		l, why, _ := o.s.takeable(ps.runID, ps, i, legalOn, false)
		line := fmt.Sprintf("- %s | %q | %s", r.Selector, clip(r.Label, 160), r.Kind)
		switch {
		case isCodeBox(r.Label):
			line += " | a verification code: left to the user"
		case r.Outcome == "failed":
			line += " | the fill's write failed: left to the user"
		case l == nil:
			line += " | left to the user (" + why + ")"
		default:
			if len(l.options) > 0 {
				line += " | options: " + optionList(l.options)
			}
			line += " | open: " + clip(firstNonEmpty(ps.why[r.Selector], reasonOf(r)), 120)
		}
		opened = append(opened, line)
	}
	fmt.Fprintf(b, "Form: %d field(s) read, %d filled, %d required still open.\n", len(ps.rows), filled, len(opened))
	if len(opened) > 0 {
		b.WriteString("Open required fields (id | question | kind):\n" + strings.Join(opened, "\n") + "\n")
	}
	var done []string
	for _, r := range ps.rows {
		if r.Outcome != "filled" && r.Outcome != "prefilled" {
			continue
		}
		v := firstNonEmpty(r.OptionLabel, r.Value)
		if _, ident := identityFieldOf(r.Label); ident || r.FileName != "" {
			v = "(held)"
		}
		done = append(done, fmt.Sprintf("%s = %s", clip(r.Label, 60), clip(strings.Join(strings.Fields(v), " "), 80)))
	}
	if len(done) > 0 {
		shown := done[:min(len(done), maxShownRows)]
		b.WriteString("Filled: " + strings.Join(shown, "; "))
		if len(done) > len(shown) {
			fmt.Fprintf(b, "; and %d more", len(done)-len(shown))
		}
		b.WriteString("\n")
	}
	b.WriteString(o.checks(ps))
}

func optionList(options []string) string {
	shown := options[:min(len(options), maxShownOptions)]
	out := make([]string, len(shown))
	for i, o := range shown {
		out[i] = fmt.Sprintf("%q", clip(o, 80))
	}
	s := strings.Join(out, " | ")
	if len(options) > len(shown) {
		s += fmt.Sprintf(" | and %d more", len(options)-len(shown))
	}
	return s
}

// checks is the state of the page's checks in a line or two.
func (o *Operation) checks(ps *pageState) string {
	var parts []string
	if len(ps.mismatch) > 0 {
		parts = append(parts, "identity mismatch"+labelsOf(ps.mismatch, func(m gates.IdentityMismatch) string { return m.Label }))
	}
	if len(ps.flags) > 0 {
		parts = append(parts, "values the review doubts"+labelsOf(ps.flags, func(f gates.LabeledFlag) string { return f.Label }))
	}
	if len(ps.notHeld)+len(ps.unreadable) > 0 {
		parts = append(parts, fmt.Sprintf("%d written value(s) the page does not hold", len(ps.notHeld)+len(ps.unreadable)))
	}
	v := gates.SubmitReadiness(gates.ReadinessInput{Pages: []gates.PageReport{ps.gatesReport(1)}, Final: false, PostingLocation: o.s.postingLoc()})
	if v.Allowed() {
		parts = append(parts, "no check blocks the next step")
	} else {
		parts = append(parts, "blocked: "+v.Reason)
	}
	return "Checks: " + strings.Join(parts, "; ") + ".\n"
}

// listEvidence lists what a citation may name. A citation outside this list
// is refused by the same checks the ladder's grounded rung applies.
func (o *Operation) listEvidence(ctx context.Context, b *strings.Builder, ps *pageState) {
	own := o.s.own(ctx)
	keys, facts := groundedFacts(own)
	b.WriteString("Evidence an answer may cite (anything else is refused):\n")
	for _, k := range keys {
		fmt.Fprintf(b, "fact:%s: %s\n", k, facts[k])
	}
	shown := map[string]bankAnswer{}
	for _, e := range groundedBank(own.Bank, nil, nil) {
		shown[e.id] = e
	}
	legalOn := o.legalOn()
	for i, r := range ps.rows {
		if !open(r) || ps.replaced(i) {
			continue
		}
		l, _, _ := o.s.takeable(ps.runID, ps, i, legalOn, false)
		if l == nil {
			continue
		}
		o.s.readKnowledge(ctx, l)
		if e, ok := shown[l.entry]; ok {
			fmt.Fprintf(b, "For %q: bank:%s (%s) -> %s\n", clip(l.label, 60), e.id, e.about, e.answer)
		}
		for _, k := range l.knowledge {
			fmt.Fprintf(b, "For %q: kb:%s: %s\n", clip(l.label, 60), k.Ref, match.StripAIDirectives(strings.Join(strings.Fields(k.Text), " ")))
		}
	}
	if x := o.s.excerpt(ps); x != "" {
		b.WriteString("Posting text (untrusted; a jd: citation is a quote of at least 12 characters from the whole text of the posting): " +
			clip(match.StripAIDirectives(x), maxExcerpt) + "\n")
	}
}
