package apply

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"runtime/debug"
	"strings"
	"sync"

	"applysmith/internal/answerbank/bind"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/browser"
	"applysmith/internal/captcha"
	"applysmith/internal/discovery"
	"applysmith/internal/documents"
	"applysmith/internal/engine"
	"applysmith/internal/gates"
	"applysmith/internal/gmail"
	"applysmith/internal/lazyregexp"
	"applysmith/internal/tracker"
)

// Documents is the document store as the chain uses it: the versions of a
// candidacy, the stored verification of one, its bytes, its text, and the
// freeze that keeps the attached versions after a submit.
type Documents interface {
	List(ctx context.Context, candidacy int64) ([]documents.Document, error)
	Verification(ctx context.Context, d documents.Document) (*documents.Verification, error)
	Freeze(ctx context.Context, candidacy int64, ids ...int64) error
	// ReadPDF returns the stored PDF of a version.
	ReadPDF(ctx context.Context, d documents.Document) ([]byte, error)
	// Source returns the stored text of a version.
	Source(ctx context.Context, id int64) (documents.Source, error)
}

// Receipts looks for the confirmation mail of a submission.
type Receipts interface {
	AwaitReceipt(ctx context.Context, lk gmail.ReceiptLookup, opt gmail.AwaitOptions) (*gmail.Receipt, error)
}

// Poster receives learning events. It returns ErrTooLarge when the receiving
// side rejects a batch as too large.
type Poster interface {
	PostEvents(ctx context.Context, events []LearnEvent) error
}

// Settings are the runtime settings a run reads.
type Settings struct {
	// AutoAcceptLegalDeclarations is the user's setting for required privacy
	// and legal checkboxes; on by default.
	AutoAcceptLegalDeclarations bool
	// AllowGoogleSignIn permits one target Google login/registration, matching
	// account selection and a verified basic identity authorization.
	AllowGoogleSignIn bool
}

// PageDeps are the collaborators of the browser stages.
type PageDeps struct {
	// Applicant reads the applicant snapshot (GET /api/autofill/applicant).
	Applicant func(ctx context.Context) (json.RawMessage, error)
	Matcher   engine.Matcher
	// Pin resolves each question against the bank entry named at its index
	// with the matcher's own guards (bind.Binding.Pinned): it verifies a
	// grounded answer citing a saved answer. Nil refuses every such answer.
	Pin func(ctx context.Context, req engine.MatchRequest, entryIDs []string) ([]engine.Resolution, error)
	// Matches collects the matcher's own results beside what the engine wrote;
	// wire its Observe into bind.Deps.
	Matches *MatchLog
	// Facts are the profile facts the check reads (sponsorship timing).
	Facts     func(ctx context.Context) (match.Facts, error)
	Documents Documents
	// Transcript is the academic transcript stored in the Answer Bank, for an
	// upload slot the page labels as a transcript; nil when none is stored.
	Transcript func(ctx context.Context) (*engine.ResumePayload, error)
	// ProfileResume is the roster résumé uploaded when the candidacy has no
	// verified document: the best match for the bound report (nil when the run
	// has none), else the default. Why names the entry and the reason.
	ProfileResume func(ctx context.Context, report *ReportBinding) (pl *engine.ResumePayload, why string, err error)
	Settings      func() Settings
	Receipts      Receipts
	Poster        Poster
	Tracker       *tracker.Tracker
	Timing        WalkTiming
	Check         CheckTiming
	Engine        engine.Timing
	MainWorld     bool
	StageDir      string
	// SnapshotDir receives page snapshots (DOM and screenshot) at a stop.
	SnapshotDir string
	Keep        *Keeper
	Credentials Credentials
	Sessions    SessionSync
	Mail        Mail
	Policy      SignupPolicy
	// Signals stores the detection signals a run observes, so the account
	// creation gate's cooldown applies; nil records none.
	Signals Signals
	Account AccountTiming
	Codes   Codes
	Code    CodeTiming
	// Model is the routed model of the ladder, the fix planner and the page
	// decisions; nil leaves every model rung out and the deterministic rules in
	// charge.
	Model Model
	// Own reads the user's own facts for the ladder and the deterministic
	// answers.
	Own func(ctx context.Context) (Own, error)
	// OwnSearch reads what the user's information says about each question on
	// the user's own experience (bind.Binding.OwnSearch): the ladder answers
	// No a row no entry claims when nothing mentions it (F-10). Nil answers no
	// row that way.
	OwnSearch func(ctx context.Context, req engine.MatchRequest) ([]match.OwnSearch, error)
	// Knowledge reads the sections of the user's own knowledge base for a
	// question, the model rungs' kb:<ref> evidence; nil or a failure shows none.
	Knowledge func(ctx context.Context, query string) ([]match.KnowledgeSection, error)
	// ReportText reads the evaluated report at a ReportBinding path.
	ReportText func(ctx context.Context, path string) (string, error)
	// Posting reads a Greenhouse, Lever or Ashby posting from its public board
	// (one request); nil leaves the page as the only source of the posting.
	Posting func(ctx context.Context, postingURL string) (PostingRead, error)
	// Revision stamps the answer bank, profile and CV: it changes when any of
	// them is saved, which ends the reuse of a rung 2 decline.
	Revision func(ctx context.Context) string
	// Declines remembers rung 2 declines; New fills it from the store.
	Declines *Declines
	// Solver builds the run's captcha solver (internal/captcha) with the audit
	// callback; nil leaves every challenge to the user.
	Solver func(e *Exec, audit func(gates.Decision)) captcha.Solver
	// Letters writes the cover letter a page's slot asks for when the
	// candidacy has none ready; nil leaves such a slot empty.
	Letters Letters
}

// matcherFor is the page matcher of one application run: the engine's own
// questions are recorded under the run, as the ladder's are (RecordRun).
func (r *Runner) matcherFor(runID string) engine.Matcher {
	if r.d.Page.Matcher == nil {
		return nil
	}
	return runMatcher{r.d.Page.Matcher, runID}
}

type runMatcher struct {
	m   engine.Matcher
	run string
}

func (m runMatcher) Match(ctx context.Context, req engine.MatchRequest) (engine.MatchResponse, error) {
	req.RecordRun = m.run
	return m.m.Match(ctx, req)
}

// MatchLog remembers, per engine run, the matcher's question and result for
// each selector, so the check can ask match.TimingForReview about an answer
// and name the answers given for a presumed posting country.
type MatchLog struct {
	mu   sync.Mutex
	byID map[string]map[string]matched
}

type matched struct {
	Q match.Question
	R match.Result
	// listed marks options the computer-use rung read once it opened the list
	// (CO-1).
	listed bool
}

// Observe is bind.Deps.Observe.
func (m *MatchLog) Observe(req bind.Request, out bind.Outcome) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.byID == nil {
		m.byID = map[string]map[string]matched{}
	}
	bySel := m.byID[req.RunID]
	if bySel == nil {
		bySel = map[string]matched{}
		m.byID[req.RunID] = bySel
	}
	for i, r := range out.Results {
		if i < len(out.Prepared) {
			bySel[r.Selector] = matched{Q: out.Prepared[i], R: r}
		}
	}
}

func (m *MatchLog) get(runID, selector string) (matched, bool) {
	if m == nil {
		return matched{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.byID[runID][selector]
	return v, ok
}

// soleOption reports a value that is the only real option the matcher saw on
// the selector's control (Lyft's "Thank you" menu under an instruction).
func (m *MatchLog) soleOption(runID, selector, value string) bool {
	o := m.optionLabels(runID, selector)
	return len(o) == 1 && strings.EqualFold(o[0], strings.TrimSpace(value))
}

// optionLabels are the real options, placeholders left out, the matcher saw
// on the selector's control.
func (m *MatchLog) optionLabels(runID, selector string) []string {
	v, ok := m.get(runID, selector)
	if !ok {
		return nil
	}
	var out []string
	for _, o := range v.Q.Options {
		if l := strings.TrimSpace(o.Label); l != "" && !match.IsPlaceholderOption(l) {
			out = append(out, l)
		}
	}
	return out
}

// list files on a selector's record the options the computer-use rung read
// once it opened a list the matcher was asked about with none, so the row's
// draft and learning event carry them (CO-1).
func (m *MatchLog) list(runID, selector string, options []string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.byID[runID][selector]
	if !ok {
		return
	}
	v.Q.Options, v.listed = nil, true
	for _, o := range options {
		v.Q.Options = append(v.Q.Options, match.Option{Label: o, Value: o})
	}
	m.byID[runID][selector] = v
}

// adopt files the record of one selector under another that has none.
func (m *MatchLog) adopt(runID, from, to string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.byID[runID][from]
	if _, has := m.byID[runID][to]; ok && !has {
		m.byID[runID][to] = v
	}
}

// session is the browser state of one run.
type session struct {
	r      *Runner
	e      *Exec
	win    Window
	tab    Tab
	page   Filler
	docs   *docResumes
	last   *PageState
	events []LearnEvent
	entry  *PageState
	after  *PageState
	jd     string
	// lead is the first page the walk read without a form: the posting's
	// overview when the form has a page of its own.
	lead  *PageState
	pages []*pageState
	// firstAnswer is the page that holds the run's first written answer.
	firstAnswer *pageState
	// frame is the id of the frame the current page's application lives in,
	// empty when it is the top document.
	frame string
	// covered are the frames the current page's fill wrote or read rows in;
	// the hand-over snapshot reads them beside the top document.
	covered []string
	// ownVal is the user's own facts, read once on first use.
	ownVal  Own
	ownRead bool
	// navStatus is the HTTP status of the posting's main resource.
	navStatus int
	// fl is the filler's debug log, written beside the stop snapshot.
	fl *fillLog
	// keep are the controls a resumed run leaves as the user left them.
	keep keptFields
	// lettered is set once the run has tried to make a cover letter.
	lettered bool
	// board is the posting as its public board states it, read once.
	board     PostingRead
	boardRead bool
	// trackerLoc and trackerCo are the location and company the posting's
	// tracker row holds, read once per page (trackerRow).
	trackerLoc, trackerCo string
	trackerRead           bool
	// walkNotes are the walk's own decisions, added to the fill stage's detail.
	walkNotes []string
	// pageCompany is the employer a page of the walk stated (PageCompany).
	pageCompany string
}

// writesKey fingerprints what the run wrote on the current page, for the
// stall rule.
func (s *session) writesKey() string {
	if len(s.pages) == 0 {
		return ""
	}
	return s.pages[len(s.pages)-1].writeKey
}

func parseHost(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	return strings.ToLower(u.Hostname()), nil
}

// tabNotOpened says why the apply tab did not open. A browser that did not
// answer is said in words, with what comes next: no page was opened, so
// nothing was filled or sent. The run's account stage keeps the error chain.
func tabNotOpened(ctx context.Context, err error) string {
	if ctx.Err() == nil && browser.Unresponsive(err) {
		return "the app's browser did not respond when the run asked it for a tab, so the application was not opened and nothing was filled or sent; it can be started again as it is"
	}
	return "the apply tab could not be opened: " + err.Error()
}

// drivePages runs the browser stages of a run whose resolve and duplicate
// guard passed: open the apply tab, account, the page walk with its check and
// fix, then submit and confirm when the run submits.
func (r *Runner) drivePages(ctx context.Context, e *Exec) (res *stop) {
	if e.Ver == nil || e.Ver.Window == nil {
		return &stop{class: ClassEnvironment, board: "lane-unavailable", reason: "no apply window is attached", at: StageAccount}
	}
	s := &session{r: r, e: e, win: e.Ver.Window}
	e.sess = s
	s.docs = newDocResumes(r, e)
	tab, err := s.win.OpenTab(ctx, e.Run.ID, engine.Host{Matcher: r.matcherFor(e.Run.ID), Resumes: s.docs, StageDir: r.d.Page.StageDir})
	if err != nil {
		r.record(e, StageAccount, StatusFailed, "the apply tab could not be opened: "+err.Error(), r.d.Now())
		return &stop{class: ClassEnvironment, board: "lane-unavailable", reason: tabNotOpened(ctx, err), at: StageAccount}
	}
	s.fl = &fillLog{}
	s.tab, s.page = tab, &loggedFiller{Filler: tab.Page(), log: s.fl}
	defer func() {
		if p := recover(); p != nil {
			res = internalError(p, debug.Stack())
		}
		s.end(ctx, res)
	}()

	if st := r.openPosting(ctx, e, s); st != nil {
		return st
	}
	// The page the open left the tab on is the first posting page of the run.
	e.Run.Stated = PageStated(ctx, s.tab)
	if !r.stage(e, StageAccount, func() (string, *stop) { return r.account(ctx, e, s) }) {
		return nil
	}
	return r.walkOn(ctx, e, s)
}

// internalError is the stop for a panic in the browser stages: a defect whose
// reason names the panic and the function and line it came from, so the run
// still ends with a record, a snapshot and its tab instead of reading as
// running forever.
func internalError(p any, stack []byte) *stop {
	where := ""
	lines := strings.Split(string(stack), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "panic(") && i+3 < len(lines) {
			fn := lines[i+2]
			if j := strings.LastIndex(fn, "("); j > 0 {
				fn = fn[:j]
			}
			file := strings.TrimSpace(lines[i+3])
			if j := strings.LastIndex(file, " +0x"); j > 0 {
				file = file[:j]
			}
			where = " in " + fn[strings.LastIndex(fn, "/")+1:] + " (" + file[strings.LastIndex(file, "/")+1:] + ")"
			break
		}
	}
	return &stop{class: ClassDefect, reason: fmt.Sprintf("the run stopped on an internal error: %v%s", p, where), at: StageFill}
}

// end decides the run's outcome from the stop that ended the browser stages,
// before the tab is kept or closed.
func (s *session) end(ctx context.Context, res *stop) {
	if res != nil {
		s.r.fail(s.e, res)
		s.snapshot(ctx, "stop")
	}
	s.release(ctx)
}

// walkOn walks the application from the page the tab shows, through the account
// stage's re-entries at login gates, to the check, submit and confirm stages.
func (r *Runner) walkOn(ctx context.Context, e *Exec, s *session) *stop {
	walkStart := r.d.Now()
	for {
		wr, st := r.walk(ctx, e, s)
		if st == nil && wr.End == EndLoginRequired {
			// The walk stopped at a gate: the account stage works on the same tab.
			rewalk, gstop := r.reenterGate(ctx, e, s, wr)
			if gstop != nil {
				return gstop
			}
			if rewalk {
				continue
			}
		}
		return r.afterWalk(ctx, e, s, walkStart, wr, st)
	}
}

// openPosting opens the application: LinkedIn through its Easy Apply entry,
// every other system at its application URL.
func (r *Runner) openPosting(ctx context.Context, e *Exec, s *session) *stop {
	if e.Cls.ATS == ATSLinkedIn && LinkedInFlowURL(e.Cls.ApplyURL) != "" {
		return r.openLinkedIn(ctx, e, s)
	}
	return r.openApplication(ctx, e, s)
}

// openApplication navigates to the application URL. A missing page is a
// posting stop and a site that is down is unavailable. An address the chain
// built that the site does not serve (the posting's address with an apply path
// added) says nothing about the posting: the run goes back to the posting and
// its own Apply control. The page is used once its document is parsed: a site
// that keeps a request open (ADP) never fires its load event, and the walk
// waits for the controls it needs itself.
func (r *Runner) openApplication(ctx context.Context, e *Exec, s *session) *stop {
	cls := e.Cls
	res, err := s.tab.NavigateDOM(ctx, cls.ApplyURL)
	if built := cls.Posting != "" && cls.ApplyURL != cls.Posting; built && err == nil &&
		(res.Status == 404 || res.Status == 410 || readsNotFound(ctx, s)) {
		res, err = s.tab.NavigateDOM(ctx, cls.Posting)
	}
	// Dropbox (live): a board address that redirects off greenhouse.io lands on
	// the company's own site, whose steps come before the application;
	// Greenhouse's own form for the same job holds all of it (the owner's
	// decision).
	if embed := greenhouseBoardEmbed(cls.ApplyURL); embed != "" && err == nil && res.Status > 0 && res.Status < 400 && !onGreenhouse(res.URL) {
		if er, eerr := s.tab.NavigateDOM(ctx, embed); eerr == nil && er.Status > 0 && er.Status < 400 {
			res = er
			e.Cls.ApplyURL, cls.ApplyURL = embed, embed
		}
	}
	if embed := discovery.AshbyEmbedURL(cls.ApplyURL); embed != "" && err == nil && (res.Status == 404 || res.Status == 410) {
		if er, eerr := s.tab.NavigateDOM(ctx, embed); eerr == nil && er.Status > 0 && er.Status < 400 {
			res = er
		}
	}
	s.navStatus = res.Status
	switch {
	case err != nil:
		r.record(e, StageAccount, StatusFailed, "the posting could not be opened: "+err.Error(), r.d.Now())
		return &stop{class: ClassEnvironment, reason: "the posting could not be opened: " + err.Error(), at: StageAccount}
	case res.Status == 404 || res.Status == 410:
		e.Run.PostingFacts = append(e.Run.PostingFacts, fmt.Sprintf("closed: HTTP %d", res.Status))
		r.record(e, StageAccount, StatusFailed, fmt.Sprintf("the posting answers HTTP %d", res.Status), r.d.Now())
		r.postingClosed(ctx, e, fmt.Sprintf("http-%d", res.Status))
		return &stop{class: ClassPosting, reason: fmt.Sprintf("posting closed: HTTP %d", res.Status), at: StageAccount}
	case res.Status >= 500 || res.Error != "":
		r.record(e, StageAccount, StatusFailed, fmt.Sprintf("the site is not answering (HTTP %d %s)", res.Status, res.Error), r.d.Now())
		return &stop{class: ClassUnavailable, reason: fmt.Sprintf("the site is not answering (HTTP %d %s)", res.Status, res.Error), at: StageAccount}
	}
	if why := r.redirectClosed(ctx, s, cls, res.URL); why != "" {
		e.Run.PostingFacts = append(e.Run.PostingFacts, "closed: "+why)
		r.record(e, StageAccount, StatusFailed, "the posting "+why, r.d.Now())
		r.postingClosed(ctx, e, "redirect-lost-job")
		return &stop{class: ClassPosting, reason: "posting closed: " + why, at: StageAccount}
	}
	_ = s.page.ObserveSubmissions(ctx)
	return nil
}

// postingClosed tells PostingClosed the posting's liveness result, with the
// liveness reason code of how it was found closed.
func (r *Runner) postingClosed(ctx context.Context, e *Exec, reason string) {
	if r.d.PostingClosed != nil {
		r.d.PostingClosed(ctx, firstNonEmpty(e.Cls.Posting, e.Run.Input.URL), discovery.Liveness{Verdict: discovery.LiveExpired, Reason: reason})
	}
}

// onGreenhouse reports an address on greenhouse.io; "" (a navigation that
// reported no final address) counts as one.
func onGreenhouse(raw string) bool {
	u, err := url.Parse(raw)
	return raw == "" || err == nil && hostIs(u.Hostname(), "greenhouse.io")
}

// release closes the tab unless the run stopped on something a person can
// finish in the page, in which case it is kept.
func (s *session) release(ctx context.Context) {
	ctx = context.WithoutCancel(ctx)
	run := s.e.Run
	defer run.browserDone()
	s.closeUnusedGate(ctx)
	if (run.Outcome.Kind == OutcomeStopped && humanCanFinish(run.Outcome)) || s.e.held != "" {
		// What the page holds is recorded first: a kept tab takes no command
		// from the run, so it can no longer be read once it is the user's.
		s.saveHandoff(ctx)
		if err := s.tab.Keep(); err == nil {
			s.handoff(ctx)
			return
		}
	}
	if s.e.resume != nil {
		// The tab is the user's and stays open; the run no longer offers it.
		s.r.d.Page.Keep.Forget(s.tab.ID())
	}
	_ = s.tab.Close(ctx)
}

// deadPostingRE reads a stop reason that names a posting nobody can apply to.
var deadPostingRE = lazyregexp.New(`(?i)\b404\b|page not found|posting (is )?(now )?closed|no longer (accepting|available)`)

// humanCanFinish reports whether a person can finish the stop in the page. A
// closed, missing or no-longer-available posting is not, and neither is a
// 404 page whatever class the stop carries.
func humanCanFinish(o Outcome) bool {
	if o.Class == ClassPosting || o.Class == ClassEnvironment || o.Class == ClassUnavailable {
		return false
	}
	return !deadPostingRE.MatchString(o.Reason)
}
