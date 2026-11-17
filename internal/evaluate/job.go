// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"applysmith/internal/browser"
	"applysmith/internal/discovery"
	"applysmith/internal/gates"
	"applysmith/internal/identity"
	"applysmith/internal/llm"
	"applysmith/internal/prompts"
	"applysmith/internal/tracker"
)

var (
	errJobTimeout = errors.New("evaluation job timed out")
	errCancelled  = errors.New("evaluation job cancelled")
	errShutdown   = errors.New("evaluation pool shut down")
)

// jobTimeout is JobTimeout; tests shorten it.
var jobTimeout = JobTimeout

const systemPrompt = "You are a careful job-posting analyst. Reply with the JSON the user asks for and nothing else."

// posting is the normalized input of one job.
type posting struct {
	URL, Source, Title, Company, Location, Compensation, Seniority, Text string
	Verification                                                         string
}

// promptData is the value every template executes against.
type promptData struct {
	Posting    PostingView
	Profile    ProfileView
	Sponsor    SponsorView
	Knowledge  []Snippet
	Signals    []string
	Evaluation string
	Page       string
}

type jobRun struct {
	e      *Evaluator
	runID  string
	req    Request
	key    string
	detail map[string]any
	fake   bool

	prof   Profile
	p      posting
	stance gates.Stance
	note   string
	need   Need
	tier   Tier
	policy string
	delta  float64
	// closed is the page read that found the posting closed, if one did.
	closed *ClosedError
	// aliases are the keys of the addresses the posting was reached through
	// when the read found it at another address (adopt).
	aliases []string
}

// Run executes one job to its outcome and records it. The run row exists
// already (see Pool.Submit); the job never creates another.
func (e *Evaluator) Run(ctx context.Context, runID string, req Request, key string) Result {
	res, detail := e.execute(ctx, runID, req, key)
	e.finish(runID, res, detail)
	return res
}

// execute runs one job to its outcome and tells the inbox and the scan that
// queued it; the run stays unfinished until finish records the outcome.
func (e *Evaluator) execute(ctx context.Context, runID string, req Request, key string) (Result, map[string]any) {
	ctx, cancel := context.WithTimeoutCause(ctx, jobTimeout, errJobTimeout)
	defer cancel()
	j := &jobRun{e: e, runID: runID, req: req, key: key, detail: map[string]any{}}
	res, err := j.pipeline(ctx)
	if err != nil {
		res = j.failure(ctx, err)
	}
	res.RunID = runID
	return j.settle(res), j.detail
}

func (j *jobRun) failure(ctx context.Context, err error) Result {
	reason, explanation, charge := "error", "", discovery.ChargeAttempt
	var je *JobError
	var le *llm.Error
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, errJobTimeout):
		reason = ReasonTimeout
	case errors.Is(cause, errCancelled):
		reason = ReasonCancelled
	case errors.Is(cause, errShutdown):
		reason, charge = ReasonInterrupted, discovery.ChargeNone
	case errors.As(err, &je):
		reason = je.Reason
		if environmentFault(je.Err) || throttled(je.Err) {
			charge = discovery.ChargeUndecided
		}
		var short shortText
		switch code, _ := browser.CodeOf(je.Err); {
		case errors.Is(je.Err, ErrNeedsBrowser):
			explanation = ErrNeedsBrowser.Error()
		case browser.Unresponsive(je.Err):
			explanation = browserSilent
		case code == browser.CodeProfileInUse:
			explanation = profileHeld
		case code == browser.CodeChromeMissing:
			explanation = chromeMissing
		case errors.As(je.Err, &short):
			explanation = short.Error()
		case throttled(je.Err):
			explanation = siteThrottled
		case j.closed != nil:
			explanation = j.closedWords()
		case je.Reason == ReasonUnreadable && je.Err != nil:
			explanation = unreadableWords(je.Err)
		}
	case errors.As(err, &le):
		reason = string(le.Kind)
		explanation = modelFailureFix(le)
		// A task with no model decides nothing about the posting; the scan and
		// the inbox drain queue nothing while one has none, so the row keeps
		// its attempts for when one is chosen.
		if le.Kind == llm.KindNoRoute {
			charge = discovery.ChargeNone
		}
	}
	j.e.d.Log.Warn("evaluation failed", "run", j.runID, "reason", reason, "err", err)
	j.detail["error"] = err.Error()
	if explanation != "" {
		j.detail["explanation"] = explanation
	}
	if j.closed != nil {
		j.detail["closed"] = true
	}
	return Result{Outcome: OutcomeFailed, Reason: reason, Explanation: explanation, Closed: j.closed != nil, charge: charge}
}

// environmentFault reports whether a read failed for a reason of the app's own
// machine, not of the posting: the server has no browser reader, the browser
// did not answer, or what the computer has or lacks (Chrome, its profile, the
// platform) kept the browser from starting.
func environmentFault(err error) bool {
	return errors.Is(err, ErrNeedsBrowser) || browser.Unresponsive(err) || browser.EnvironmentFault(err)
}

// throttled reports a read the posting's site refused for now: HTTP 429, as
// Glassdoor answers right after a scan's detail reads, LinkedIn's 999, or a
// page in the words a board reader knows a throttle by. Nothing was decided
// about the posting.
func throttled(err error) bool {
	var u unsureRead
	return errors.As(err, &u) && u.reason == "rate-limited"
}

// browserSilent explains a read the browser did not answer: nothing was decided
// about the posting, so it can be evaluated again as it is.
const browserSilent = "the app's browser did not respond, so the posting was not read and nothing was decided about it; evaluate it again, or paste the posting text"

// siteThrottled explains a read the posting's site held back for now: nothing
// was decided about the posting. nextRead says what comes next.
const siteThrottled = "the site limited how fast it can be read, so nothing was decided about the posting"

// profileHeld and chromeMissing explain a read the computer stopped before the
// browser started: another Chrome process holds the app's browser profile, or
// there is no Chrome. The everyday Chrome uses a profile of its own, so the
// first says it is not the one to quit.
const (
	profileHeld   = "another Chrome process holds the app's browser profile, so the posting was not read and nothing was decided about it; quit that process (your everyday Chrome is not it), then evaluate it again, or paste the posting text"
	chromeMissing = "Google Chrome was not found on this computer, so the posting was not read and nothing was decided about it; install Google Chrome, then evaluate it again, or paste the posting text"
)

// unreadableWords explain any other read that failed: what the reader met, and
// the two things the user can do, so no failure is shown as a bare reason code.
func unreadableWords(err error) string {
	return "the posting could not be read (" + err.Error() + "); check that the link opens the posting, or paste the posting text"
}

// closedWhy words the liveness reasons of a read that found the posting closed.
var closedWhy = map[string]string{
	"closure-wording":   "the page says it no longer takes applications",
	"http-404":          "its page no longer exists",
	"http-410":          "its page no longer exists",
	"redirect-lost-job": "its link now leads to a page that no longer names the job",
}

// closedWords explain a read that found the posting closed, which the board
// would otherwise word as a posting that could not be read: nothing is broken,
// the posting is gone (flow round 9, E-4). Once keeping the result has expired
// the job's inbox row, settleInbox adds that it left the inbox.
func (j *jobRun) closedWords() string {
	s := "the posting is closed"
	if why := closedWhy[j.closed.Liveness.Reason]; why != "" {
		s += " (" + why + ")"
	}
	return s
}

// modelFailureFix names the provider that refused a model call and the one
// step that fixes it, so a failed evaluation says more than its reason code.
// A task with no routed model is said the way every front end but the command
// line says it (llm.NoModel), naming the page where one is chosen; the command
// line names its own command (cli printEvaluation).
func modelFailureFix(le *llm.Error) string {
	if le.Kind == llm.KindNoRoute {
		return llm.NoModel("the " + cmp.Or(le.Task, "evaluation") + " task")
	}
	provider := ""
	if n := len(le.Attempts); n > 0 {
		provider = le.Attempts[n-1].Provider
	}
	class, fix := llm.ClassifyTest(le, cmp.Or(provider, "<provider>"))
	if class == llm.ClassOther {
		return ""
	}
	if provider == "" {
		return fix
	}
	return provider + ": " + fix
}

func explanationSuffix(res Result) string {
	if res.Explanation == "" {
		return ""
	}
	return " (" + res.Explanation + ")"
}

// settle tells the inbox and the scan run that queued the job how it ended, and
// returns the result with what the inbox did to the job's row.
func (j *jobRun) settle(res Result) Result {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res = j.settleInbox(ctx, res)
	if j.req.ScanRun != "" && j.e.d.ScanOutcome != nil {
		if err := j.e.d.ScanOutcome(ctx, j.req.ScanRun, j.runID, res); err != nil {
			j.e.d.Log.Error("evaluate: recording the outcome on the scan run failed", "scan", j.req.ScanRun, "run", j.runID, "err", err)
		}
	}
	return res
}

// settleInbox returns the inbox row to pending after a failure, or records the
// outcome; a failed job is never retried here. What the failure costs the row
// is the result's charge: a read that decided nothing about the posting is
// taken again by the inbox drain within the row's bound. A read that found the
// posting closed is kept as its liveness result instead, which expires the
// row, and only then do the closed words say the row left the inbox.
// A throttled read's words end with what comes next (nextRead).
func (j *jobRun) settleInbox(ctx context.Context, res Result) Result {
	if j.closed != nil && j.e.d.Closed != nil {
		err := j.e.d.Closed(ctx, j.closed.URL, j.closed.Liveness, j.req.InboxRowID)
		if err == nil {
			if j.req.InboxRowID > 0 && res.Explanation != "" {
				res.Explanation += "; it was removed from your inbox"
				j.detail["explanation"] = res.Explanation
			}
			return res
		}
		j.e.d.Log.Error("evaluate: keeping the closed posting's liveness result failed", "row", j.req.InboxRowID, "err", err)
	}
	if j.req.InboxRowID <= 0 || j.e.d.Inbox == nil {
		return j.nextRead(res, false)
	}
	var err error
	waits := false
	if res.Outcome == OutcomeFailed {
		waits, err = j.e.d.Inbox.Release(ctx, j.req.InboxRowID, "evaluation failed: "+res.Reason+explanationSuffix(res), res.charge)
	} else {
		err = j.e.d.Inbox.Done(ctx, j.req.InboxRowID, res)
	}
	if err != nil {
		j.e.d.Log.Error("evaluate: updating the inbox row failed", "row", j.req.InboxRowID, "err", err)
	}
	return j.nextRead(res, waits)
}

// nextRead ends a throttled read's words with what comes next: a later inbox
// run reads the posting again while its row waits for one, and otherwise the
// user evaluates it again.
func (j *jobRun) nextRead(res Result, waits bool) Result {
	if res.Explanation != siteThrottled {
		return res
	}
	if waits {
		res.Explanation += "; it will be read again on a later inbox run"
	} else {
		res.Explanation += "; evaluate it again later, or paste the posting text"
	}
	j.detail["explanation"] = res.Explanation
	return res
}

func (j *jobRun) phase(ctx context.Context, p Phase) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return j.e.setPhase(ctx, j.runID, p)
}

func (j *jobRun) pipeline(ctx context.Context) (Result, error) {
	e := j.e
	if err := j.phase(ctx, PhaseReading); err != nil {
		return Result{}, err
	}
	if !j.req.Force {
		if n, ok := e.finalReport(ctx, j.key); ok {
			return Result{Outcome: OutcomeDuplicate, ReportNumber: n}, nil
		}
	}
	prof, err := e.d.Profile(ctx)
	if err != nil {
		return Result{}, e.fail(ReasonProfile, err)
	}
	j.prof = prof
	if err := j.readPosting(ctx); err != nil {
		return Result{}, err
	}
	// The read found the posting at another address, whose own final report
	// makes this a duplicate too.
	if len(j.aliases) > 0 && !j.req.Force {
		if n, ok := e.finalReport(ctx, j.key); ok {
			return Result{Outcome: OutcomeDuplicate, ReportNumber: n}, nil
		}
	}

	if err := j.phase(ctx, PhaseGating); err != nil {
		return Result{}, err
	}
	j.analyse(ctx)
	if failures := Gate(j.gateInput(), ResolveGate(prof.Profile)); len(failures) > 0 && !j.req.Applying {
		return j.writeGated(ctx, failures)
	}

	mode := j.req.Mode
	if mode != ModeDeep {
		res, more, err := j.screen(ctx)
		if err != nil || !more {
			return res, err
		}
	}
	return j.deep(ctx)
}

func (j *jobRun) readPosting(ctx context.Context) error {
	r := j.req
	text := strings.TrimSpace(r.Text)
	var read PageRead
	method, result := "pasted", "unverified"
	if text == "" {
		if r.URL == "" || j.e.d.Reader == nil {
			return j.e.fail(ReasonUnreadable, errors.New("no posting text and no way to read the URL"))
		}
		var err error
		read, err = j.e.d.Reader.Read(ctx, r.URL)
		switch {
		case err == nil:
			text, method, result = read.Text, read.Method, read.Result
		case errors.Is(err, ErrPageUnsure) && strings.TrimSpace(r.Scanned) != "":
			text, method = r.Scanned, "scan"
		default:
			errors.As(err, &j.closed)
			return j.e.fail(ReasonUnreadable, err)
		}
	}
	if result == "" {
		result = "unverified"
	}
	clean := CleanText(text)
	if err := tooShort(clean, method == "pasted"); err != nil {
		return j.e.fail(ReasonUnreadable, err)
	}
	// The employer's name the ATS states comes first, then a name a source
	// read. A name made from the board's slug is a guess: the posting's own
	// text (the field extraction) comes before it.
	stated, guess := r.Company, ""
	if slugName(r.URL, r.Company) {
		stated, guess = "", r.Company
	}
	company := firstNonEmpty(read.Company, stated)
	j.p = posting{
		URL: r.URL, Source: r.Source, Text: clean, Seniority: r.Seniority,
		// A page read may hand over the page's document title as the role.
		Title:        firstNonEmpty(r.Title, tracker.RoleName(read.Title, company)),
		Company:      company,
		Location:     firstNonEmpty(r.Location, read.Location),
		Compensation: firstNonEmpty(r.Compensation, read.Compensation),
		Verification: fmt.Sprintf("%s %s %s", method, result, j.e.d.Now().Format("2006-01-02")),
	}
	j.adopt(read.URL)
	if j.p.Title == "" || j.p.Company == "" || sameName(j.p.Title, j.p.Company) {
		j.extractFields(ctx)
	}
	if j.p.Company == "" {
		j.p.Company = guess
	}
	// A source that files a page title names the employer as a tail ("... @
	// Assembled") that its board's slug ("assembledhq") did not take off.
	j.p.Title = identity.RoleWithoutCompany(j.p.Title, j.p.Company)
	// A careers site that heads every posting with the employer's name gives
	// a title that is the company. That is no role; the role stays unknown.
	if sameName(j.p.Title, j.p.Company) {
		j.p.Title = ""
	}
	return nil
}

// slugName reports a company name that is only the posting board's slug made
// readable ("Resolveai" for jobs.ashbyhq.com/resolveai/...).
func slugName(rawURL, name string) bool {
	key, ok := identity.ATSKey(rawURL)
	if !ok || squashName(name) == "" {
		return false
	}
	_, rest, _ := strings.Cut(key, ":")
	slug, _, _ := strings.Cut(rest, "/")
	return squashName(name) == squashName(slug)
}

// adopt makes the posting a page read found at another address (where a
// tracking or short link landed, the employer's posting a job board's listing
// names) the job's posting: the report, the index and the tracker row carry
// its URL and identity key, so a later application goes there directly. The
// requested URL's key stays an alias of the posting.
func (j *jobRun) adopt(addr string) {
	k, err := identity.URLKey(addr)
	if addr == "" || err != nil || k == j.key {
		return
	}
	j.aliases = append(j.aliases, j.key)
	j.key, j.p.URL = k, addr
}

func sameName(a, b string) bool {
	return a != "" && strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// extractWindow is how much of the posting text the field extraction reads;
// the title, the employer and the location open a posting.
const extractWindow = 6000

// extractFields fills a missing title, company and location from the posting
// text with the jd-extract task: pasted text and a page read outside the
// board APIs carry none, and the prechecks, the report and the tracker row
// need them. Only empty fields are filled, and a title that is the company;
// a failed extraction leaves them as they were and the evaluation goes on.
func (j *jobRun) extractFields(ctx context.Context) {
	prompt, err := j.render("jd-extract", promptData{Page: escapeText(string([]rune(j.p.Text)[:min(len([]rune(j.p.Text)), extractWindow)]))})
	var out struct {
		Title    string `json:"title"`
		Company  string `json:"company"`
		Location string `json:"location"`
	}
	if err == nil {
		err = j.call(ctx, "jd-extract", prompt, ExtractSchema(), &out)
	}
	if err != nil {
		if ctx.Err() == nil && j.e.d.Log != nil {
			j.e.d.Log.Warn("evaluate: the field extraction failed", "run", j.runID, "err", err)
		}
		return
	}
	if sameName(j.p.Title, firstNonEmpty(j.p.Company, line(out.Company))) {
		j.p.Title = ""
	}
	for _, f := range []struct {
		dst *string
		v   string
	}{{&j.p.Title, out.Title}, {&j.p.Company, out.Company}, {&j.p.Location, out.Location}} {
		if *f.dst == "" {
			*f.dst = line(f.v)
		}
	}
}

// analyse computes the deterministic background of the posting.
func (j *jobRun) analyse(ctx context.Context) {
	j.stance, j.note = gates.Sponsorship(j.p.Text)
	j.need = SponsorNeed(j.prof.WorkAuthorization)
	j.setTier(j.p.Company)
}

func (j *jobRun) setTier(company string) {
	j.tier = ClassifyTier(company, j.p.Text)
	j.policy, j.delta = "allow", 0
	if tp, ok := j.prof.EligibilityGate.EmployerTiers[string(j.tier)]; ok && tp.Policy != "" {
		j.policy = tp.Policy
		if tp.Policy == "penalize" && tp.Delta != nil {
			j.delta = *tp.Delta
		}
	}
}

func (j *jobRun) gateInput() GateInput {
	in := GateInput{Title: j.p.Title, Seniority: j.p.Seniority, Location: j.p.Location, Compensation: j.p.Compensation, Text: j.p.Text, URL: j.req.URL}
	switch {
	case j.p.Company != "" && j.e.d.Blocked(j.p.Company):
		in.BlockedWhy = "the employer is on the blocked-companies list or in skip_companies"
	case j.policy == "block":
		in.BlockedWhy = fmt.Sprintf("employers of tier %s are blocked by the profile", j.tier)
	}
	return in
}

func (j *jobRun) identityPosting(company, role string) identity.Posting {
	return identity.Posting{Key: j.key, URL: j.p.URL, Source: j.p.Source, Company: company, Role: role, Location: j.p.Location}
}

func (j *jobRun) sponsorView() SponsorView {
	note := j.note
	if j.need == NeedUnanswered {
		note = strings.TrimSpace(note + " profile sponsorship unanswered")
	}
	return SponsorView{Stance: string(j.stance), Note: note, Need: string(j.need)}
}

func (j *jobRun) sponsorText() string {
	s := "Sponsorship stance in the posting: " + string(j.stance)
	if j.note != "" {
		s += " (" + j.note + ")"
	}
	if j.need == NeedUnanswered {
		s += "; profile sponsorship unanswered"
	}
	return "- " + s
}

func (j *jobRun) postingView(limit int) PostingView {
	text := j.p.Text
	if limit > 0 {
		text = window(text, limit)
	}
	return PostingView{
		Title: prompts.Escape(j.p.Title), Company: prompts.Escape(j.p.Company), Location: prompts.Escape(j.p.Location),
		Compensation: prompts.Escape(j.p.Compensation), Source: prompts.Escape(j.p.Source), Text: prompts.Escape(text),
	}
}

// render executes a template and records which text it used.
func (j *jobRun) render(name string, data promptData) (string, error) {
	text, info, err := j.e.d.Prompts.Render(name, data)
	if err != nil {
		return "", j.e.fail(ReasonPrompt, err)
	}
	list, _ := j.detail["prompts"].([]map[string]any)
	j.detail["prompts"] = append(list, map[string]any{"name": info.Name, "hash": info.Hash, "override": info.Override})
	return text, nil
}

// call runs one structured model call and notes whether the fake provider
// answered.
func (j *jobRun) call(ctx context.Context, task, prompt string, schema json.RawMessage, out any) error {
	// The router clamps each attempt to the budget the request carries.
	deadline, _ := ctx.Deadline()
	resp, err := j.e.d.Model.Structured(ctx, llm.Request{Task: task, System: systemPrompt, Prompt: prompt, RunID: j.runID, Deadline: deadline}, schema, out)
	if err != nil {
		return err
	}
	if resp != nil && j.e.d.IsFake(ctx, resp.Provider) {
		j.fake = true
	}
	return nil
}

func (j *jobRun) meta(kind string, score *float64, scoreKind, rec, legit string) ReportMeta {
	return ReportMeta{
		Date: j.e.d.Now().Format("2006-01-02"), Kind: kind, Score: score, ScoreKind: scoreKind, Rec: rec,
		URL: j.p.URL, Verification: j.p.Verification, Legitimacy: legit,
		Company: j.p.Company, Role: j.p.Title, Fake: j.fake,
	}
}

func escapeText(s string) string { return prompts.Escape(s) }
