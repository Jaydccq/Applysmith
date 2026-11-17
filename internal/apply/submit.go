package apply

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"applysmith/internal/answerbank/learn"
	"applysmith/internal/browser"
	"applysmith/internal/gates"
	"applysmith/internal/gmail"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/ledger"
)

// afterWalk turns the walk's end into stage lines, runs the final readiness
// verdict, and, when the run submits, the submit and confirm stages.
func (r *Runner) afterWalk(ctx context.Context, e *Exec, s *session, start time.Time, wr *walkResult, st *stop) *stop {
	if s.docs != nil {
		e.Run.Warnings = append(e.Run.Warnings, s.docs.skips()...)
	}
	notes := ""
	if len(s.walkNotes) > 0 {
		notes = "; " + strings.Join(s.walkNotes, "; ")
	}
	if st != nil {
		st.at = firstStage(st.at, StageFill)
		r.record(e, StageFill, StatusFailed, st.reason+notes, start)
		return st
	}
	end := fmt.Sprintf("%d page(s), walk ended %s: %s", len(s.pages), wr.End, wr.Detail) + notes
	if wr.End != EndSubmit {
		if stp := r.stalledOnAnswers(e, s, start, wr, end); stp != nil {
			return stp
		}
		r.record(e, StageFill, StatusFailed, end, start)
		class, board := wr.End.Class(wr.Offers), walkBoard(wr.End)
		reason := "walk ended " + string(wr.End) + ": " + wr.Detail
		switch yours := onlyYoursEmpty(wr); {
		case wr.End == EndStalled && existingRecordRE.MatchString(wr.Detail):
			// The account exists and the saved password was refused: the user signs
			// in or resets it.
			class, board = ClassCredential, "sign-in"
		case len(yours) > 0:
			// Only the user's own identifiers stand between the page and its next
			// step (Boeing's "SSN"): the form waits for the user, not on a defect.
			class, board = ClassAnswerGap, boardNeedsUser
			reason = "the form requires " + strings.Join(yours, ", ") + ", which a run never types; enter it in the kept tab and press Done (the walk ended " + string(wr.End) + ")"
		}
		return &stop{class: class, board: board, reason: reason, at: StageFill}
	}
	r.record(e, StageFill, StatusOK, end, start)
	var checkMs, fixMs int64
	var fixed, abstainedN, failedN, attempts, calls int
	for _, ps := range s.pages {
		checkMs += ps.report.CheckMs
		fixMs += ps.report.FixMs
		fixed += ps.report.FixFixed
		abstainedN += ps.report.FixAbstained
		failedN += ps.report.FixFailed
		attempts += ps.report.FixAttempts
		calls += ps.report.FixPlannerCalls
	}
	checkStart := r.d.Now()
	v := s.finalVerdict()
	_ = r.audit(e, v.Decision, wr.Last.URL, false)
	if !v.Allowed() {
		r.record(e, StageCheck, StatusFailed, v.Reason, checkStart)
		return &stop{class: Class(v.Class), board: s.checkBoard(v), reason: "check: " + v.Reason, at: StageCheck}
	}
	if !s.readAnyControl() {
		// A walk that ended at Submit without one control read on any page has
		// verified nothing, whatever the pages' own reports say.
		pr := gates.PageReport{Number: len(s.pages) + 1, ReReadFailed: true}
		nv := gates.SubmitReadiness(gates.ReadinessInput{Pages: []gates.PageReport{pr}, Final: true})
		_ = r.audit(e, nv.Decision, wr.Last.URL, false)
		r.record(e, StageCheck, StatusFailed, "no control was read on any page", checkStart)
		return &stop{class: Class(nv.Class), reason: "check: no control was read on any page of the walk; " + nv.Reason, at: StageCheck}
	}
	if !s.wroteApplication() {
		r.record(e, StageCheck, StatusFailed, "the walk wrote only contact details", checkStart)
		return &stop{class: ClassPosting, reason: "check: not an application: the walk wrote only contact details and answered nothing", at: StageCheck}
	}
	r.record(e, StageCheck, StatusOK, fmt.Sprintf("%d page(s) verified in %d ms", len(s.pages), checkMs), checkStart)
	r.record(e, StageFix, StatusOK, fmt.Sprintf("%d fix loop(s): %d attempt(s), %d fixed, %d abstained, %d failed, %d planner call(s), in %d ms",
		fixLoops(s), attempts, fixed, abstainedN, failedN, calls, fixMs), checkStart)
	return r.submitStage(ctx, e, s, wr)
}

// reviewMarks names what a fill-only run leaves for the user's review instead
// of stopping: the values the review doubted, and the required statements
// only the user makes, left blank; "" when there are none.
func (s *session) reviewMarks() string {
	var doubted, yours []string
	for _, ps := range s.pages {
		for _, f := range ps.flags {
			if !gates.FlagGate(f.Flag, f.Label, s.postingLoc()).Allowed() {
				doubted = append(doubted, strconv.Quote(clip(f.Label, 80)))
			}
		}
		for _, l := range ps.leftovers {
			if l.Reason == ReasonStatementBlank {
				yours = append(yours, strconv.Quote(clip(l.Label, 80)))
			}
		}
	}
	out := ""
	if len(doubted) > 0 {
		out += "; marked for your review: " + strings.Join(doubted, ", ")
	}
	if len(yours) > 0 {
		out += "; left for your own answer: " + strings.Join(yours, ", ")
	}
	return out
}

// stalledOnAnswers reads a walk that ended against the page's own demands (the
// step would not advance, or Next led nowhere) while required rows stand
// unanswered: the form asked what the user's data does not answer, so the stop
// is the check's answer gap that names those rows, not a defect of the walk.
// Anything else the verdict finds (an unreadable page, an identity mismatch)
// keeps the walk's own class. A disabled final Submit with no required row open
// is read by disabledOnBlankRows.
func (r *Runner) stalledOnAnswers(e *Exec, s *session, start time.Time, wr *walkResult, end string) *stop {
	switch wr.End {
	case EndStalled, EndNoNavigation, EndSubmitDisabled:
	default:
		return nil
	}
	if existingRecordRE.MatchString(wr.Detail) {
		return nil
	}
	v := s.finalVerdict()
	if v.Code != gates.CodeRequiredUnanswered {
		if wr.End == EndSubmitDisabled && v.Allowed() {
			return r.disabledOnBlankRows(e, s, start, wr, end)
		}
		return nil
	}
	var url string
	if wr.Last != nil {
		url = wr.Last.URL
	}
	_ = r.audit(e, v.Decision, url, false)
	r.record(e, StageFill, StatusOK, end, start)
	r.record(e, StageCheck, StatusFailed, v.Reason, start)
	return &stop{class: ClassAnswerGap, board: s.checkBoard(v), reason: "check: " + v.Reason + "; the walk ended " + string(wr.End) + ": " + wr.Detail, at: StageCheck}
}

// onlyYoursEmpty are the quoted labels of the required boxes still empty on a
// step that would not go on, when every one is a national identifier (a
// social security, passport or tax number), which no run ever types; nil
// otherwise.
func onlyYoursEmpty(wr *walkResult) []string {
	switch wr.End {
	case EndStalled, EndNoNavigation, EndSubmitDisabled:
	default:
		return nil
	}
	if wr.Last == nil {
		return nil
	}
	var out []string
	for _, l := range wr.Last.Stall.Empty {
		if learn.Classify(l, "text", "", "", nil) != learn.WithheldGovernmentID {
			return nil
		}
		out = append(out, strconv.Quote(l))
	}
	return out
}

// maxBlankRowsNamed caps how many unanswered rows a stop names.
const maxBlankRowsNamed = 5

// disabledOnBlankRows reads a walk that ended with the final Submit disabled
// while the last page holds questions the run left blank (abstained or skipped,
// none marked required): a page that keeps its own Submit disabled until they
// are answered is evidence that it wants them, so the stop is an answer gap
// naming those rows. The run answers and ticks nothing because of it. With no
// such row there is nothing to tell why the control is disabled, and the walk
// keeps its own class.
func (r *Runner) disabledOnBlankRows(e *Exec, s *session, start time.Time, wr *walkResult, end string) *stop {
	labels := s.blankRows()
	if len(labels) == 0 {
		return nil
	}
	more := ""
	if len(labels) > maxBlankRowsNamed {
		more = fmt.Sprintf(" (and %d more)", len(labels)-maxBlankRowsNamed)
		labels = labels[:maxBlankRowsNamed]
	}
	for i, l := range labels {
		labels[i] = fmt.Sprintf("%q", clipText(l, 100))
	}
	reason := "the page keeps Submit disabled and these rows stand unanswered: " + strings.Join(labels, ", ") + more
	r.record(e, StageFill, StatusOK, end, start)
	r.record(e, StageCheck, StatusFailed, reason, start)
	return &stop{class: ClassAnswerGap, board: "answer", reason: "check: " + reason + "; the walk ended " + string(wr.End) + ": " + wr.Detail, at: StageCheck}
}

// blankRows lists the labels of the last page's rows the run left without an
// answer: abstained or skipped, neither filled nor prefilled. A row the page
// itself closed to the run (hidden, read-only, edited by the user) is
// not a question left blank.
func (s *session) blankRows() []string {
	if len(s.pages) == 0 {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, row := range s.pages[len(s.pages)-1].rows {
		if row.Outcome != "abstained" && row.Outcome != "skipped" {
			continue
		}
		switch row.Reason {
		case "hidden", "readonly", "user-edited":
			continue
		}
		if row.Label == "" || seen[row.Label] {
			continue
		}
		seen[row.Label] = true
		out = append(out, row.Label)
	}
	return out
}

var existingRecordRE = lazyregexp.New(`(?i)existing\s+(?:record|account)\s+with\s+that\s+(?:email|e-?mail)`)

func fixLoops(s *session) int {
	n := 0
	for _, ps := range s.pages {
		n += ps.report.FixLoops
	}
	return n
}

func firstStage(a, b Stage) Stage {
	if a != "" {
		return a
	}
	return b
}

// pressOutcome is what the press produced.
type pressOutcome struct {
	dispatched bool
	at         time.Time
	status     int
	body       string
	evidence   string
	// kind is "pressed", "refused", "validation", "challenge" or "unknown".
	kind      string
	responses []browser.Response
	// skipN is how many observed responses belong to an earlier press.
	skipN int
	// missing are the questions the application service named as missing.
	missing []string
}

// submitStage presses the final Submit only after the submit authorization,
// the readiness verdict (already allowed), the duplicate guard and the press
// claim; exactly one press; then confirm. Cancellation is checked before the
// claim; once the press was dispatched the run reaches its verdict and record.
func (r *Runner) submitStage(ctx context.Context, e *Exec, s *session, wr *walkResult) *stop {
	start := r.d.Now()
	in := e.Run.Input
	auth := gates.SubmitAuthorization(gates.SubmitAuthInput{Caller: in.Caller, Tool: in.Tool, Submit: in.Submit, ExplicitRequest: in.ExplicitRequest, Demo: r.demo()})
	if err := r.audit(e, auth, in.URL, false); err != nil {
		r.record(e, StageSubmit, StatusFailed, err.Error(), start)
		return &stop{class: ClassEnvironment, reason: err.Error(), at: StageSubmit}
	}
	if !auth.Allowed() {
		switch auth.Code {
		case gates.CodeSubmitNotRequested:
			marks := s.reviewMarks()
			detail := "dry run: Submit not pressed" + marks
			if in.BatchID == "" && (in.Caller == gates.CallerDashboard || in.Caller == gates.CallerCLI) {
				// The user's own dry run of one posting keeps its filled tab at
				// Submit, for "Submit this form" after a confirmation in the
				// dashboard. A list's rows close theirs: a tab per filled row
				// would push the kept tabs of its stops past the cap.
				e.held = dryRunHold + marks
				detail += "; the filled tab is kept for your confirmation"
			}
			r.record(e, StageSubmit, StatusSkipped, detail, start)
			r.record(e, StageConfirm, StatusSkipped, "dry run: nothing to confirm", start)
			return nil
		case gates.CodeDemoWorkspace:
			r.record(e, StageSubmit, StatusSkipped, "dry run: "+auth.Code+": "+auth.Reason, start)
			r.record(e, StageConfirm, StatusSkipped, "dry run: nothing to confirm", start)
			return nil
		}
		r.record(e, StageSubmit, StatusFailed, auth.Reason, start)
		return &stop{class: ClassDefect, reason: "submit refused: " + auth.Reason, at: StageSubmit}
	}

	if why := r.resumeHold(ctx, e); why != "" {
		// The form is filled and stays so in the tab; the press is the user's.
		e.held = why
		r.record(e, StageSubmit, StatusSkipped, "held for your confirmation: "+why, start)
		r.record(e, StageConfirm, StatusSkipped, "nothing to confirm", start)
		return nil
	}

	// Look again: the control and the page may have changed since the walk.
	st, err := Probe(ctx, s.tab)
	if err != nil {
		return r.submitFail(e, start, ClassEnvironment, "the page could not be read before Submit: "+err.Error())
	}
	cats := Categorize(st, true)
	if len(cats.Submit) == 0 {
		return r.submitFail(e, start, ClassDefect, "no Submit control stands on the form")
	}
	if cats.Submit[0].Disabled {
		return r.submitFail(e, start, ClassDefect, "the Submit control is disabled"+validationWords(st))
	}
	if e.Run.ATS == ATSLinkedIn {
		// A session challenge means LinkedIn is asking about the account: nothing
		// is pressed and nothing is solved.
		switch sessionChallenge(st) {
		case "login_redirect":
			return r.submitFailBoard(e, start, ClassCredential, "sign-in", "LinkedIn redirected to its login or checkpoint page; Submit was not pressed")
		case "captcha":
			return r.submitFailBoard(e, start, ClassVerification, "challenge", "bot check: a captcha challenge stands on the LinkedIn page; Submit was not pressed")
		}
	}
	if kind := unsolvedWidget(st); kind != "" {
		// Evidence point 1: an unsolved checkbox widget gets its token in the
		// response field, the same probe is re-read, then the press. Without a
		// solver, or when the gate or the service says no, the form is the user's.
		again, ok := r.beforePress(ctx, e, s, st)
		if !ok || unsolvedWidget(again) != "" {
			return r.submitFailBoard(e, start, ClassVerification, "challenge", "bot check: an unsolved "+kind+" widget stands on the form; the filled tab is kept for you")
		}
		st = again
		cats = Categorize(st, true)
		if len(cats.Submit) == 0 {
			return r.submitFail(e, start, ClassDefect, "no Submit control stands on the form")
		}
	}
	if missing := r.unansweredToggles(ctx, s); len(missing) > 0 {
		return r.submitFailBoard(e, start, ClassAnswerGap, "answer", "required yes/no question(s) appear unanswered just before Submit: "+strings.Join(missing, "; ")+"; Submit was not pressed")
	}
	if ctx.Err() != nil {
		return r.submitFail(e, start, ClassEnvironment, "cancelled before the press claim")
	}

	claim, cerr := r.d.Ledger.Claim(ctx, e.claimRequest())
	if cerr != nil {
		if d, ok := ledger.DecisionOf(cerr); ok {
			_ = r.audit(e, d, in.URL, false)
			class := Class(d.Class)
			if class == "" {
				class = ClassEnvironment
			}
			return r.submitFail(e, start, class, d.Reason)
		}
		return r.submitFail(e, start, ClassEnvironment, "the press claim failed: "+cerr.Error())
	}
	for _, d := range claim.Decisions {
		if err := r.audit(e, d, in.URL, false); err != nil {
			r.settle(e, claim.ID, ledger.Verdict{})
			return r.submitFail(e, start, ClassEnvironment, err.Error())
		}
	}
	e.hold(claim)
	r.persist(e)
	if h := r.d.Hooks.AfterClaim; h != nil {
		h()
	}
	if ctx.Err() != nil {
		r.settle(e, claim.ID, ledger.Verdict{})
		return r.submitFail(e, start, ClassEnvironment, "cancelled before the press")
	}

	var watch ResponseWatch
	if w, werr := s.tab.WatchResponses(ctx); werr == nil {
		watch = w
		defer watch.Stop()
	} else {
		e.Run.Warnings = append(e.Run.Warnings, "network observation unavailable: "+werr.Error())
	}

	loc, err := Locate(ctx, s.tab, cats.Submit[0].I)
	if err != nil {
		r.settle(e, claim.ID, ledger.Verdict{})
		return r.submitFail(e, start, ClassEnvironment, "the Submit control could not be located: "+err.Error())
	}
	press := gates.PressControl(gates.ControlInput{Unattended: e.unattended(), Stage: gates.StageSubmit, Found: loc.Found && loc.Visible, Labels: loc.Labels, FinalApply: cats.Submit[0].FinalApply, SubmitAuthorized: auth.Allowed(), ClaimHeld: true})
	if err := r.audit(e, press, in.URL, false); err != nil {
		r.settle(e, claim.ID, ledger.Verdict{})
		return r.submitFail(e, start, ClassEnvironment, err.Error())
	}
	if !press.Allowed() {
		r.settle(e, claim.ID, ledger.Verdict{})
		return r.submitFail(e, start, ClassDefect, "no submit control was pressed: "+press.Reason)
	}
	if !loc.Hit {
		r.settle(e, claim.ID, ledger.Verdict{})
		return r.submitFail(e, start, ClassDefect, "the Submit control is covered by "+loc.Blocker+"; nothing was pressed")
	}
	if ctx.Err() != nil {
		r.settle(e, claim.ID, ledger.Verdict{})
		return r.submitFail(e, start, ClassEnvironment, "cancelled before the press")
	}

	// The press signals the instant before its input is dispatched.
	po := &pressOutcome{dispatched: true, at: r.d.Now()}
	e.Run.Pressed = true
	e.Run.Presses = 1
	clickErr := s.tab.Click(ctx, loc.X, loc.Y)
	if h := r.d.Hooks.AfterPress; h != nil {
		h()
	}
	ctx = context.WithoutCancel(ctx)
	e.Ctx = ctx
	if clickErr != nil {
		po.kind = "unknown"
		po.evidence = "the browser failed while pressing: " + clickErr.Error()
	}
	r.classify(ctx, e, s, watch, po)
	if po.kind == "refused" {
		// The Greenhouse post-submit security code: read the emailed code, type
		// it into every empty box and press Submit once more, never a third time.
		again := func(c context.Context) *stop { return r.pressSubmitAgain(c, e, s, auth) }
		first := po.responses
		po.skipN = len(first)
		if retried, why := r.securityCode(ctx, e, s, po, again); retried {
			po.kind, po.status, po.body = "", 0, ""
			r.classify(ctx, e, s, watch, po)
			if po.kind == "refused" {
				po.evidence = fmt.Sprintf("read the emailed security code and retried once: the retry was refused: HTTP %d %s", po.status, refusedBodyText(po.body))
			}
		} else if why != "" {
			po.evidence = "read the emailed security code and retried once: the code was not read or typed: " + why
		}
	}
	if po.kind == "refused" && po.evidence == "" && challengeRefusal(po.status, po.body) {
		// A 428 (Greenhouse's captcha-failed with no code recipient) or a
		// refusal that names a captcha is a wall a person clears in the page.
		po.kind, po.evidence = "challenge", fmt.Sprintf("the application service answered HTTP %d %s", po.status, refusedBodyText(po.body))
	}
	if po.kind == "challenge" {
		// Evidence point 2: a puzzle stands where the confirmation should be.
		again := func(c context.Context) *stop { return r.pressSubmitAgain(c, e, s, auth) }
		if r.afterPress(ctx, e, s, watch, again) {
			po.kind, po.evidence = "", ""
			r.classify(ctx, e, s, watch, po)
		}
	}

	repairNote, repairPressed := "", false
	if po.kind == "validation" || (po.kind == "refused" && len(po.missing) > 0) {
		// The page names a validation problem on the form that is still open,
		// or Ashby bounced the form with its own errorMessages: one repair round
		// on this claim, never a second. On Ashby the saved form's lost Yes/No
		// answers are pressed again first.
		again := func(c context.Context) *stop { return r.pressSubmitAgain(c, e, s, auth) }
		evidence := po.missing
		if po.kind == "validation" && s.after != nil {
			evidence = s.after.Validation
		}
		seen := po.skipN + len(po.responses)
		if watch != nil {
			seen = len(watch.Responses()) // every answer to the first press
		}
		saved := po.kind == "refused" && s.repairSaved(ctx)
		pressed, note := r.repairRound(ctx, e, s, evidence, again)
		if !pressed && saved && e.Run.Repaired == "" {
			e.Run.Repaired = "saved form"
			if st := again(ctx); st == nil {
				pressed, note = true, "repair round: the saved form's Yes/No answers pressed again"
			}
		}
		repairNote, repairPressed = note, pressed
		if pressed {
			po.skipN = seen
			po.kind, po.status, po.body, po.evidence = "", 0, "", ""
			r.classify(ctx, e, s, watch, po)
		}
	}

	s.combinedSettled(ctx)

	var v ledger.Verdict
	v.PressDispatched = true
	switch po.kind {
	case "refused", "validation", "challenge":
		v.RefusalProven = true
	case "pressed":
		v.Landed = true
	}
	fail := (*stop)(nil)
	switch po.kind {
	case "refused":
		reason := fmt.Sprintf("the application service refused the write: HTTP %d %s", po.status, refusedBodyText(po.body))
		if po.evidence != "" {
			reason = po.evidence
		}
		fail = &stop{class: ClassDefect, reason: reason + "; the filled form is left open", at: StageSubmit}
		if repairPressed {
			fail.board = boardNeedsUser
		}
	case "validation":
		fail = &stop{class: ClassDefect, reason: "the page names a validation problem: " + po.evidence + "; the filled form is left open", at: StageSubmit}
		if repairPressed {
			fail.board = boardNeedsUser
		}
	case "challenge":
		fail = &stop{class: ClassVerification, board: "challenge", reason: "the page served a verification challenge (detected from: " + po.evidence + "); the filled form is left open", at: StageSubmit}
	case "unknown":
		fail = &stop{class: ClassDefect, reason: "submit clicked; no verdict could be read: " + po.evidence, at: StageSubmit}
	}
	if repairNote != "" {
		result := "landed"
		if fail != nil {
			result = "refused again"
		}
		if !strings.Contains(repairNote, "result=") {
			repairNote += " result=" + result
		}
		e.Run.Repaired = repairNote
	}
	if fail != nil {
		if repairNote != "" {
			fail.reason += "; " + repairNote
		}
		if po.kind == "refused" || po.kind == "validation" {
			var named []string
			if po.kind == "validation" && s.after != nil {
				named = s.after.Validation
			}
			s.recordRefusal(po.missing, named)
		}
		r.settle(e, claim.ID, v)
		r.record(e, StageSubmit, StatusFailed, fail.reason, start)
		if po.kind == "unknown" {
			e.Run.MayHaveSubmitted = true
		}
		return fail
	}
	if strings.Contains(repairNote, "result=landed") {
		r.record(e, StageSubmit, StatusOK, "Submit pressed twice on one claim; "+repairNote, start)
	} else {
		r.record(e, StageSubmit, StatusOK, "Submit pressed once", start)
	}
	return r.confirm(ctx, e, s, po, claim.ID)
}

// unansweredToggles reads the required Yes/No groups that show no answer just
// before the press (Ashby refused a Yes the write had seen land). A group is
// named only when a second read after the re-read wait still shows it
// unanswered, so a page mid-render does not block the press.
func (r *Runner) unansweredToggles(ctx context.Context, s *session) []string {
	p, err := ReadPrePress(ctx, s.tab)
	if err != nil || len(p.Toggles) == 0 {
		return nil
	}
	if !sleepCtx(ctx, r.d.Page.Check.withDefaults().Reread) {
		return nil
	}
	if p, err = ReadPrePress(ctx, s.tab); err != nil {
		return nil
	}
	return p.Toggles
}

func (r *Runner) submitFail(e *Exec, start time.Time, class Class, reason string) *stop {
	return r.submitFailBoard(e, start, class, "", reason)
}

func (r *Runner) submitFailBoard(e *Exec, start time.Time, class Class, board, reason string) *stop {
	r.record(e, StageSubmit, StatusFailed, reason, start)
	return &stop{class: class, board: board, reason: reason, at: StageSubmit}
}

// pressSubmitAgain presses the same Submit control once more at the second
// evidence point. It makes no second claim: the run still holds the first.
func (r *Runner) pressSubmitAgain(ctx context.Context, e *Exec, s *session, auth gates.Decision) *stop {
	if e.Run.Presses >= maxSubmitPresses {
		return &stop{class: ClassDefect, reason: "Submit was already pressed twice on this claim"}
	}
	st, err := Probe(ctx, s.tab)
	if err != nil {
		return &stop{class: ClassEnvironment, reason: err.Error()}
	}
	cats := Categorize(st, true)
	if len(cats.Submit) == 0 {
		return &stop{class: ClassDefect, reason: "no Submit control stands on the form"}
	}
	loc, err := Locate(ctx, s.tab, cats.Submit[0].I)
	if err != nil {
		return &stop{class: ClassEnvironment, reason: err.Error()}
	}
	press := gates.PressControl(gates.ControlInput{Unattended: e.unattended(), Stage: gates.StageSubmit, Found: loc.Found && loc.Visible, Labels: loc.Labels, FinalApply: cats.Submit[0].FinalApply, SubmitAuthorized: auth.Allowed(), ClaimHeld: true})
	if aerr := r.audit(e, press, e.Run.Input.URL, false); aerr != nil {
		return &stop{class: ClassEnvironment, reason: aerr.Error()}
	}
	if !press.Allowed() || !loc.Hit {
		return &stop{class: ClassDefect, reason: "the Submit control was not pressed again"}
	}
	e.Run.Presses++
	if err := s.tab.Click(ctx, loc.X, loc.Y); err != nil {
		return &stop{class: ClassEnvironment, reason: err.Error()}
	}
	return nil
}

// hold records the press claim on the run. A correction's claim names the row
// it supersedes, which the claim decided again under the lock.
func (e *Exec) hold(c ledger.Claim) {
	e.Run.Claim = c.ID
	e.Run.Correction, e.Run.Supersedes = c.Supersedes != 0, c.Supersedes
}

// settle finishes the claim row; a failure is a warning, the row stays
// submitting and ages into may-have-submitted, never into submitted.
func (r *Runner) settle(e *Exec, claimID string, v ledger.Verdict) gates.LedgerState {
	company, role, num := "", "", (*int64)(nil)
	if e.Run.Report != nil {
		company, role = e.Run.Report.Company, e.Run.Report.Role
		n := e.Run.Report.Number
		num = &n
	}
	v.Company, v.Role, v.ReportNumber = company, role, num
	st, err := r.d.Ledger.Finish(context.WithoutCancel(e.Ctx), claimID, v)
	if err != nil {
		e.Run.Warnings = append(e.Run.Warnings, "the claim could not be settled: "+err.Error())
	}
	return st
}

// unsolvedWidget names a captcha widget that the page is showing with an empty
// response field. Invisible and hidden widgets do not count.
func unsolvedWidget(st *PageState) string {
	empty := len(st.Resp) == 0
	for _, f := range st.Resp {
		if f.Empty {
			empty = true
		}
	}
	for _, c := range st.Captcha {
		if c.Kind == "recaptcha_v3" {
			continue
		}
		if c.Visible && !c.Invisible && c.W > 20 && c.H > 20 && empty {
			return c.Kind
		}
	}
	return ""
}

// ignoredWrite reports a write that is not the application: analytics,
// pixels and similar beacons.
func ignoredWrite(u string) bool {
	l := strings.ToLower(u)
	for _, w := range []string{"analytics", "pixel", "beacon", "telemetry", "metrics", "collect", "sentry", "hotjar", "segment.", "gtag", "/log", "doubleclick", "recaptcha", "hcaptcha", "datadog", "newrelic", "fullstory", "mixpanel", "amplitude"} {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

// classify reads the result of the press in this order: the application
// service refused the write, the page names a validation problem, a challenge
// stands in place of the confirmation; otherwise the application counts as
// pressed.
func (r *Runner) classify(ctx context.Context, e *Exec, s *session, watch ResponseWatch, po *pressOutcome) {
	tm := s.r.d.Page.Timing.withDefaults()
	deadline := time.Now().Add(tm.PressWait)
	var before string
	if po.kind != "unknown" && e.Run.ATS == ATSLinkedIn {
		r.linkedInWait(ctx, s, watch, tm.ConfirmWait)
	} else if po.kind != "unknown" {
		for time.Now().Before(deadline) {
			time.Sleep(200 * time.Millisecond)
			if watch != nil && hasAppWrite(watch.Responses()) {
				time.Sleep(300 * time.Millisecond)
				break
			}
			if sig, err := Signature(ctx, s.tab); err == nil {
				if before == "" {
					before = sig
				} else if sig != before {
					break
				}
			}
		}
	}
	if watch != nil {
		// An answer that is still loading is waited for, a little: the verdict
		// of the application service is the first evidence.
		for i := 0; i < 20 && watch.Pending() > 0; i++ {
			time.Sleep(150 * time.Millisecond)
		}
		po.responses = watch.Responses()
		if po.skipN <= len(po.responses) {
			po.responses = po.responses[po.skipN:]
		}
	}
	for _, rsp := range po.responses {
		if !isWrite(rsp.Method) || ignoredWrite(rsp.URL) {
			continue
		}
		if rsp.Status >= 400 {
			po.kind, po.status, po.body = "refused", rsp.Status, rsp.Body
			return
		}
	}
	// A bounced application is a refusal like a 4xx, but the page's own
	// validation words still get their repair round first.
	bounce, bounced := browser.Response{}, false
	if e.Run.ATS == ATSAshby {
		bounce, bounced = ashbyBounce(po.responses)
	}
	if bounced {
		po.missing = ashbyMissing(bounce.Body)
	}
	refuseBounce := func() {
		po.kind, po.status, po.body = "refused", bounce.Status, bounce.Body
		po.evidence = ashbyBounceEvidence(bounce)
	}
	st, err := Probe(ctx, s.tab)
	if err != nil {
		if bounced {
			refuseBounce()
		} else if po.kind != "unknown" {
			po.kind, po.evidence = "unknown", "the page could not be read after the press: "+err.Error()
		}
		return
	}
	s.after = st
	still := st.HasForm() && sameDoc(st.URL, s.last)
	if still && len(st.Validation) > 0 && SuccessText(e.Run.ATS, st.Lines) == "" && !acceptedBy(e.Run.ATS, po.responses) {
		po.kind, po.evidence = "validation", strings.Join(st.Validation, " / ")
		if bounced {
			po.evidence = ashbyBounceEvidence(bounce) + " / " + po.evidence
		}
		return
	}
	if unsolvedWidget(st) != "" || BotInterstitial(st.Title, st.Lines) != "" || anyOpenChallenge(st) {
		po.kind, po.evidence = "challenge", challengeSignal(st)
		return
	}
	if bounced {
		refuseBounce()
		return
	}
	if po.kind == "unknown" {
		return
	}
	po.kind = "pressed"
}

// challengeTextRE is the wording of a bot-verification wall in a refused
// write's body.
var challengeTextRE = lazyregexp.New(`(?i)captcha|recaptcha|hcaptcha|turnstile|challenge|are\s+you\s+(?:a\s+)?human|verify\s+you(?:\s+are)?|checking your browser`)

// challengeRefusal reports a refused write a person clears in the page: a 428
// (Greenhouse's captcha-failed) or a body in a verification wall's words.
func challengeRefusal(status int, body string) bool {
	return status == 428 || challengeTextRE.MatchString(body)
}

var (
	htmlBodyRE  = lazyregexp.New(`(?i)^\s*<(?:!doctype|html)\b`)
	htmlBlockRE = lazyregexp.New(`(?is)<(?:script|style|svg|head)\b.*?</(?:script|style|svg|head)>`)
	htmlTagRE   = lazyregexp.New(`<[^>]*>`)
)

// refusedBodyText is a refused write's body as text a person can read: an
// HTML page (Teamtailor's 422 re-renders the form behind kilobytes of head
// scripts) is reduced to its own text, a JSON or plain body passes through,
// both cut to 300 characters.
func refusedBodyText(body string) string {
	if htmlBodyRE.MatchString(body) {
		body = htmlTagRE.ReplaceAllString(htmlBlockRE.ReplaceAllString(body, " "), " ")
		body = strings.Join(strings.Fields(strings.ReplaceAll(body, "&nbsp;", " ")), " ")
	}
	return clip(body, 300)
}

// challengeSignal names what showed the challenge: a visible CAPTCHA widget,
// challenge wording in the page text, or an open challenge frame.
func challengeSignal(st *PageState) string {
	switch kind := unsolvedWidget(st); {
	case kind != "":
		return "a visible " + kind + " widget"
	case BotInterstitial(st.Title, st.Lines) != "":
		return "challenge wording: " + BotInterstitial(st.Title, st.Lines)
	}
	return "an open challenge frame"
}

func anyOpenChallenge(st *PageState) bool {
	for _, c := range st.Captcha {
		if c.BframeOpen && c.Visible {
			return true
		}
	}
	return false
}

func sameDoc(url string, last *PageState) bool {
	return last != nil && redactURL(url) == redactURL(last.URL)
}

func isWrite(m string) bool {
	switch strings.ToUpper(m) {
	case "POST", "PUT", "PATCH":
		return true
	}
	return false
}

func hasAppWrite(rs []browser.Response) bool {
	for _, r := range rs {
		if isWrite(r.Method) && !ignoredWrite(r.URL) && r.Status != 0 {
			return true
		}
	}
	return false
}

// acceptedBy reports whether the system's application service accepted the
// write. LinkedIn accepts only its applications endpoint answering 200.
func acceptedBy(ats string, rs []browser.Response) bool {
	switch ats {
	case ATSLinkedIn:
		return linkedInAccepted(rs)
	case ATSAshby:
		return ashbyAccepted(rs)
	}
	return hasAccepted(rs)
}

func hasAccepted(rs []browser.Response) bool {
	for _, r := range rs {
		if isWrite(r.Method) && !ignoredWrite(r.URL) && r.Status >= 200 && r.Status < 300 {
			return true
		}
	}
	return false
}

// confirm establishes whether the pressed application was accepted from four
// kinds of evidence and records which decided: the application service's
// answer to its write, the page's confirmation, the in-page observer (evidence
// but not confirmation) and a mailed receipt. A page that still shows the
// form with no refusal evidence fails the stage and is never retried, because
// some systems reset the form on an accepted submit.
func (r *Runner) confirm(ctx context.Context, e *Exec, s *session, po *pressOutcome, claimID string) *stop {
	start := r.d.Now()
	run := e.Run
	accepted := acceptedBy(run.ATS, po.responses)
	hint := ""
	if s.after != nil {
		hint = SuccessText(run.ATS, s.after.Lines)
	}
	observed := false
	if obs, err := s.page.SubmitObservations(ctx, 120*time.Second); err == nil && len(obs) > 0 {
		observed = true
	}
	var rcpt *gmail.Receipt
	if r.d.Page.Receipts != nil {
		lk := gmail.ReceiptLookup{JobURL: redactURL(run.Input.URL), ATS: run.ATS, Tenant: run.Tenant, SubmittedAt: po.at}
		if run.Report != nil {
			lk.Company = run.Report.Company
		} else {
			lk.Company = run.Input.Company
		}
		got, err := r.d.Page.Receipts.AwaitReceipt(ctx, lk, gmail.AwaitOptions{PageConfirmed: accepted || hint != ""})
		if err != nil {
			run.Warnings = append(run.Warnings, "the receipt lookup failed: "+err.Error())
		} else {
			rcpt = got
		}
	}
	src, receiptID := "", ""
	switch {
	case accepted:
		src = ConfirmATSResponse
	case hint != "":
		src = ConfirmPage
	case rcpt != nil:
		src = ConfirmReceipt
	}
	// A receipt found beside a page or service confirmation is kept too: the
	// submission then waits for nothing more.
	if rcpt != nil {
		receiptID = rcpt.MessageID
	}
	confirmed := src != ""
	v := ledger.Verdict{Landed: true, PressDispatched: true, Confirmed: confirmed, ConfirmSource: src, ReceiptID: receiptID}
	r.settle(e, claimID, v)
	r.freeze(ctx, e, s)
	run.Outcome = Outcome{Kind: OutcomeSubmitted, Confirmed: confirmed, ConfirmSource: src}
	switch {
	case confirmed:
		r.record(e, StageConfirm, StatusOK, "confirmed by "+src, start)
	case observed:
		run.Outcome.ConfirmSource = ConfirmObserved
		r.record(e, StageConfirm, StatusOK, "the in-page observer saw the submit path fire; that is evidence, not confirmation, so the submission stays unconfirmed", start)
	case run.ATS == ATSLinkedIn:
		rep := linkedInUnconfirmed(s.after, r.d.Page.Timing.withDefaults().ConfirmWait)
		if s.after != nil && len(Categorize(s.after, true).Submit) > 0 {
			r.record(e, StageConfirm, StatusFailed, "submit clicked; "+rep+"; verify manually", start)
		} else {
			r.record(e, StageConfirm, StatusOK, "pressed; "+rep, start)
		}
	case s.after != nil && (s.after.HasForm() || len(Categorize(s.after, true).Submit) > 0):
		r.record(e, StageConfirm, StatusFailed, "submit clicked; no confirmation and the form is still shown — verify manually", start)
	default:
		r.record(e, StageConfirm, StatusOK, "pressed; no confirmation evidence yet (a receipt may still arrive)", start)
	}
	return nil
}

// walkBoard is the runs-board reason of a walk end that needs the user.
func walkBoard(e WalkEnd) string {
	switch e {
	case EndBotChallenge:
		return "challenge"
	case EndLoginRequired:
		return "sign-in"
	case EndConsentGate:
		return "answer"
	}
	return ""
}

// checkBoard is the runs-board reason of a failed verdict: an answer gap is
// "answer", a verification code that never arrived "one-time-code", and a
// blocking review finding "review".
func (s *session) checkBoard(v gates.ReadinessResult) string {
	switch v.Code {
	case gates.CodeRequiredUnanswered:
		for _, ps := range s.pages {
			for _, l := range ps.leftovers {
				if l.Reason == ReasonCodeBox {
					return "one-time-code"
				}
			}
		}
		return "answer"
	case gates.CodeIdentityMismatch, gates.CodeReviewContradiction:
		return "review"
	}
	return ""
}
