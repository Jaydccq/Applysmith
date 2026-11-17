package apply

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"applysmith/internal/gates"
	"applysmith/internal/identity"
)

// listConcurrency is the number of rows in flight. The browser stays one lane,
// so the second row only overlaps the first row's confirm, record and learn.
const listConcurrency = 2

// ListRow is one posting of a list.
type ListRow struct {
	URL string
	// EmployerURL is a mirror resolution the list already made; the identity
	// stays URL.
	EmployerURL string
	Company     string
	Role        string
	// Named is true when the user named the row's company in the request.
	Named bool
	// Mirrors is how many other links the same job was listed under.
	Mirrors int
	// Resubmit re-sends an application the ledger holds as unconfirmed.
	Resubmit bool
}

// ListOptions are what a list is run with.
type ListOptions struct {
	BatchID                 string
	Submit, ExplicitRequest bool
	Caller                  gates.Caller
	Tool                    string
	ConversationID          string
	UserMessage             string
	OwnWords                string
	// UserStarted says a user's own act started the list (Input.UserStarted).
	UserStarted bool
	// UserMessageHash is the hash of the message a resumed batch was asked by.
	UserMessageHash string
	// Deadline ends the list: a row that would start after it is skipped.
	Deadline time.Time
	// Journal records the rows; JournalIndex maps a row to its journal
	// position when the rows are a resumed batch's pending ones.
	Journal      Journal
	JournalIndex []int
	// HasSubmission says whether the ledger holds a submission for the row; an
	// error counts as yes, because the retry of a row must never repeat a press.
	HasSubmission func(ctx context.Context, row ListRow) (bool, error)
	// Started is called with a row's position and its run id once the run
	// exists; Landed in list order as soon as the row and every row before it
	// have landed, and once more for a row the retry sweep ran again.
	Started func(idx int, runID string)
	Landed  func(ListResult)
	// FirstRunID names the first run the list starts; empty makes one up.
	FirstRunID string
}

// ListResult is what happened to one row. Run is nil for a row that never ran;
// Note then says why.
type ListResult struct {
	Index int
	Row   ListRow
	Run   *Run
	Note  string
}

// Row notes of rows that did not run.
const (
	noteCancelled = "cancelled: the list was stopped before this row"
	noteAborted   = "not attempted: the list was aborted after repeated environment faults"
)

type listRun struct {
	r    *Runner
	ctx  context.Context
	rows []ListRow
	opt  ListOptions
	lane *RowLane

	mu      sync.Mutex
	verdict *Verdict
	gen     int
	armed   bool
	aborted string
	reached []bool
	// cooled holds, per account host, why its later rows are skipped: a real
	// refusal, lockout or hold there earlier in this list.
	cooled  map[string]string
	results []ListResult
	// firstID is FirstRunID until a run takes it.
	firstID string

	repairMu sync.Mutex
	// vmu guards the one preflight.
	vmu sync.Mutex
	// sw orders the rows of the sweep in progress.
	sw sweepOrder

	cbMu   sync.Mutex
	landed []bool
	next   int
}

// RunList runs the rows through a pool of two rows in flight over one browser
// lane. It takes one preflight and one browser attach for the whole list.
// Results are returned by list index; the first sweep's rows that stopped on
// the environment are run once more and replace their first result.
func (r *Runner) RunList(ctx context.Context, rows []ListRow, opt ListOptions) []ListResult {
	l := &listRun{r: r, ctx: ctx, rows: rows, opt: opt, lane: NewRowLane(), armed: true, cooled: map[string]string{},
		reached: make([]bool, len(rows)), results: make([]ListResult, len(rows)), landed: make([]bool, len(rows)), firstID: opt.FirstRunID}
	l.sweep(allRows(len(rows)))
	if l.retryable() {
		l.sweep(l.environmentRows())
	}
	return l.results
}

func allRows(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// sweep runs the chosen rows in list order. A row waits for every earlier row
// of the same host, company or posting, so those never overlap and keep their
// order; the rest are limited by the pool.
func (l *listRun) sweep(idx []int) {
	done := map[int]chan struct{}{}
	for _, i := range idx {
		done[i] = make(chan struct{})
	}
	l.sw = newSweepOrder(idx)
	sem := make(chan struct{}, listConcurrency)
	var wg sync.WaitGroup
	for k, i := range idx {
		for _, j := range idx[:k] {
			if rowsConflict(l.rows[i], l.rows[j]) {
				<-done[j]
			}
		}
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(done[i])
			defer func() { <-sem }()
			l.runRow(i)
		}()
	}
	wg.Wait()
}

// sweepOrder makes the rows of a sweep take the lane in list order and lets a
// row wait until the rows before it have taken what their results teach.
type sweepOrder struct {
	pos       map[int]int
	entered   []chan struct{}
	afterDone []chan struct{}
}

func newSweepOrder(idx []int) sweepOrder {
	o := sweepOrder{pos: map[int]int{}, entered: make([]chan struct{}, len(idx)), afterDone: make([]chan struct{}, len(idx))}
	for k, i := range idx {
		o.pos[i] = k
		o.entered[k], o.afterDone[k] = make(chan struct{}), make(chan struct{})
	}
	return o
}

// waitTurn blocks until the previous row of the sweep has taken the lane or
// given up; the lane is then handed out in list order, so a row that holds it
// never waits for one that has not reached it.
func (o sweepOrder) waitTurn(i int) {
	if k := o.pos[i]; k > 0 {
		<-o.entered[k-1]
	}
}

func (o sweepOrder) entering(i int) { close(o.entered[o.pos[i]]) }

// waitEarlier blocks until every earlier row of the sweep has applied what its
// result teaches, so an environment fault is judged in list order.
func (o sweepOrder) waitEarlier(i int) {
	for _, c := range o.afterDone[:o.pos[i]] {
		<-c
	}
}

func (o sweepOrder) applied(i int) { close(o.afterDone[o.pos[i]]) }

func (l *listRun) jidx(i int) int {
	if i < len(l.opt.JournalIndex) {
		return l.opt.JournalIndex[i]
	}
	return i
}

// runRow takes the lane, decides whether the row may still run, runs it and
// lands the result. A panic in the chain is reported as the row's result.
func (l *listRun) runRow(i int) {
	row := l.rows[i]
	hold := l.lane.Hold()
	res := ListResult{Index: i, Row: row}
	var entered, applied bool
	defer func() {
		if p := recover(); p != nil {
			res.Run, res.Note = nil, fmt.Sprintf("failed: the row stopped on an internal error (%v)", p)
		}
		if !entered {
			l.sw.entering(i)
		}
		if !applied {
			l.sw.applied(i)
		}
		hold.Release()
		l.land(res)
	}()
	l.sw.waitTurn(i)
	if err := hold.Acquire(l.ctx); err != nil {
		res.Note = noteCancelled
		return
	}
	entered = true
	l.sw.entering(i)
	if note := l.refuse(i); note != "" {
		res.Note = note
		l.journalEnded(i, note)
		return
	}
	j := l.opt.Journal
	if j != nil {
		if err := j.Started(l.ctx, l.jidx(i)); err != nil {
			res.Note = "not run: the journal could not record the row (" + err.Error() + ")"
			return
		}
	}
	l.mu.Lock()
	gen, runID := l.gen, l.firstID
	l.firstID = ""
	l.mu.Unlock()
	o := l.opt
	in := Input{URL: row.URL, EmployerURL: row.EmployerURL, Submit: o.Submit, ExplicitRequest: o.ExplicitRequest, UserStarted: o.UserStarted, NamedRetry: row.Named, Resubmit: row.Resubmit,
		Caller: o.Caller, Tool: o.Tool, Company: row.Company, Role: row.Role, ConversationID: o.ConversationID, UserMessage: o.UserMessage, OwnWords: o.OwnWords, UserMessageHash: o.UserMessageHash,
		PreflightFn: l.takeVerdict, BatchID: o.BatchID, Hold: hold, RunID: runID}
	in.Started = func(id string) {
		if j != nil {
			j.Attach(l.ctx, l.jidx(i), id)
		}
		if o.Started != nil {
			o.Started(i, id)
		}
	}
	run := l.r.RunOne(l.ctx, in)
	res.Run = run
	l.journalEnded(i, "")
	l.after(i, run, gen)
	l.sw.applied(i)
	applied = true
}

func (l *listRun) journalEnded(i int, note string) {
	if l.opt.Journal != nil {
		l.opt.Journal.Ended(l.ctx, l.jidx(i), note)
	}
}

// takeVerdict is the list's one preflight and browser attach, taken by the
// first row that needs it, inside that row's run. A blocked verdict stops every
// later row.
func (l *listRun) takeVerdict(ctx context.Context) *Verdict {
	l.vmu.Lock()
	defer l.vmu.Unlock()
	if l.verdict == nil {
		v := l.r.Preflight(ctx)
		l.mu.Lock()
		l.verdict = v
		if v.Blocked() {
			l.aborted = "preflight blocked: " + v.Summary()
		}
		l.mu.Unlock()
	}
	return l.verdict
}

// refuse says why a row must not start now, or "".
func (l *listRun) refuse(i int) string {
	row := l.rows[i]
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case l.ctx.Err() != nil:
		return noteCancelled
	case !l.opt.Deadline.IsZero() && !l.r.d.Now().Before(l.opt.Deadline):
		return NoteDeadline
	case l.aborted != "":
		if strings.HasPrefix(l.aborted, "preflight blocked") {
			return "not attempted: " + l.aborted
		}
		return noteAborted + " (" + l.aborted + ")"
	case l.cooled[hostOf(row.URL)] != "":
		return l.cooled[hostOf(row.URL)]
	case row.EmployerURL != "" && l.cooled[hostOf(row.EmployerURL)] != "":
		return l.cooled[hostOf(row.EmployerURL)]
	}
	for j := range l.rows {
		if j != i && l.reached[j] && sameJob(l.rows[i], l.rows[j]) {
			return "skipped: the same job was already applied to through " + hostOf(l.rows[j].URL)
		}
	}
	return ""
}

// sameJob is the same-candidacy-strict predicate over two list rows.
func sameJob(a, b ListRow) bool {
	return identity.SameCandidacyStrict(identity.Posting{URL: a.URL, Company: a.Company, Role: a.Role},
		identity.Posting{URL: b.URL, Company: b.Company, Role: b.Role})
}

// rowsConflict reports two rows that must never be in flight together: one
// sign-in session per host, a receipt that names the company, and a second link
// to a posting that waits so the twin check sees the first.
func rowsConflict(a, b ListRow) bool {
	if ha := hostOf(a.URL); ha != "" && ha == hostOf(b.URL) {
		return true
	}
	if a.Company != "" && b.Company != "" && (gates.CoversCompany(a.Company, b.Company) || gates.CoversCompany(b.Company, a.Company)) {
		return true
	}
	ka, ea := IdentityKey(a.URL)
	kb, eb := IdentityKey(b.URL)
	return ea == nil && eb == nil && ka == kb
}

// reachedForm reports a run that got as far as the form: a stop at check or
// later, a filled dry run or a submit.
func reachedForm(o Outcome) bool {
	switch o.Kind {
	case OutcomeSubmitted, OutcomeFilled:
		return true
	case OutcomeStopped:
		switch o.Stage {
		case StageCheck, StageFix, StageSubmit, StageConfirm, StageRecord, StageLearn:
			return true
		}
	}
	return false
}

// refusedNote says why a host's later rows in a list are skipped, from what
// its sign-in record shows: lockout wording, a hold from earlier today, or
// refused passwords. A host where no password was tried (none saved, a new
// account needed, Google sign-in only) skips nothing.
func refusedNote(s SigninRecord) string {
	host := strings.ToLower(s.Host)
	switch {
	case host == "" || s.Succeeded != "":
		return ""
	case s.Lockout:
		return "skipped: " + host + " answered with lockout wording earlier in this list; no password is tried there again"
	case s.Held:
		return "skipped: the saved passwords for " + host + " ran out earlier today (a hold); none is tried there again today"
	case s.Refusals == 1:
		return "skipped: " + host + " refused the saved password earlier in this list"
	case s.Refusals > 1:
		return fmt.Sprintf("skipped: %s refused %d saved passwords earlier in this list", host, s.Refusals)
	case s.Exhausted:
		return "skipped: the saved passwords for " + host + " ran out earlier in this list"
	}
	return ""
}

// after applies what a landed run teaches the rest of the list: the form was
// reached, a host refused the saved sign-ins, or the browser failed.
func (l *listRun) after(i int, run *Run, gen int) {
	o := run.Outcome
	if reachedForm(o) {
		l.mu.Lock()
		l.reached[i], l.armed = true, true
		l.mu.Unlock()
		return
	}
	if o.Kind != OutcomeStopped {
		return
	}
	switch {
	case o.Class == ClassCredential:
		l.mu.Lock()
		for _, s := range run.Signins {
			if note := refusedNote(s); note != "" {
				l.cooled[strings.ToLower(s.Host)] = note
			}
		}
		l.mu.Unlock()
	case o.Class == ClassEnvironment && l.ctx.Err() == nil:
		l.sw.waitEarlier(i)
		l.repair(gen)
	}
}

// repair re-attaches the browser once after an environment stop. A row that
// then reaches the form re-arms one more repair; a second stop with no row
// reaching the form in between aborts the list. A stop that belongs to an
// earlier browser than the current one is the repaired fault, not a new one.
func (l *listRun) repair(gen int) {
	l.repairMu.Lock()
	defer l.repairMu.Unlock()
	l.mu.Lock()
	if gen != l.gen || l.aborted != "" {
		l.mu.Unlock()
		return
	}
	if !l.armed {
		l.aborted = "a second environment fault with no row reaching a form since the browser was re-attached"
		l.mu.Unlock()
		return
	}
	l.armed = false
	l.mu.Unlock()
	v := l.r.Preflight(l.ctx)
	l.vmu.Lock()
	defer l.vmu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if v.Blocked() {
		l.aborted = "the one browser repair failed: " + v.Summary()
		return
	}
	l.verdict, l.gen = v, l.gen+1
}

// land stores a row's result and fires the callback in list order for every row
// that, with those before it, has landed. A row the retry sweep ran again
// lands a second time, at once: every row landed in the first sweep.
func (l *listRun) land(res ListResult) {
	l.mu.Lock()
	l.results[res.Index] = res
	l.mu.Unlock()
	l.cbMu.Lock()
	defer l.cbMu.Unlock()
	if l.landed[res.Index] {
		if l.opt.Landed != nil {
			l.opt.Landed(res)
		}
		return
	}
	l.landed[res.Index] = true
	for l.next < len(l.rows) && l.landed[l.next] {
		if l.opt.Landed != nil {
			l.mu.Lock()
			r := l.results[l.next]
			l.mu.Unlock()
			l.opt.Landed(r)
		}
		l.next++
	}
}

// environmentRows are the first sweep's rows that stopped on the environment.
func (l *listRun) environmentRows() []int {
	var out []int
	for i, r := range l.results {
		if r.Run == nil || r.Run.Outcome.Kind != OutcomeStopped || r.Run.Outcome.Class != ClassEnvironment {
			continue
		}
		if l.hasSubmission(r.Row) {
			continue
		}
		out = append(out, i)
	}
	return out
}

func (l *listRun) hasSubmission(row ListRow) bool {
	if l.opt.HasSubmission == nil {
		return true
	}
	has, err := l.opt.HasSubmission(l.ctx, row)
	return has || err != nil
}

// retryable reports whether the retry sweep may run: not after the list
// aborted, was cancelled or passed its deadline.
func (l *listRun) retryable() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.aborted != "" || l.ctx.Err() != nil {
		return false
	}
	return l.opt.Deadline.IsZero() || l.r.d.Now().Before(l.opt.Deadline)
}
