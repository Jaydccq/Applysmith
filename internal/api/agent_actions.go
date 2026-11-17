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
	"unicode"

	"applysmith/internal/agent"
	harnessv "applysmith/internal/agent/harness"
	"applysmith/internal/agent/improve"
	"applysmith/internal/answerbank/assist"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/documentsapi"
	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/discovery"
	"applysmith/internal/documents"
	"applysmith/internal/evaluate"
	"applysmith/internal/gates"
	"applysmith/internal/identity"
	"applysmith/internal/research"
	"applysmith/internal/signals"
	"applysmith/internal/tracker"
	"applysmith/internal/workspace"
)

// agentPrincipal scopes the idempotency keys of the runs the Ask Agent starts.
const agentPrincipal = "ask-agent"

// agentActions gives the Ask Agent's tools the services of the domain. The
// domain is looked up when a tool runs, because it is built after the agent.
type agentActions struct{ s *Server }

// refusal turns an API error a model can act on into the tool's refusal and
// leaves any other error alone.
func refusal(err error) error {
	var ae *apikit.Error
	if errors.As(err, &ae) {
		switch ae.Code {
		case wire.CodeConflict, wire.CodeValidationFailed, wire.CodeNotFound, wire.CodeUnavailable:
			return fmt.Errorf("%w: %s", agent.ErrRefused, ae.Message)
		}
	}
	return err
}

func (a agentActions) StartEvaluation(ctx context.Context, r agent.EvaluationRequest) (string, error) {
	d, err := a.s.dom()
	if err != nil {
		return "", err
	}
	req := evaluate.Request{URL: r.URL, Text: r.Text, Title: r.Title, Company: r.Company, Source: agentPrincipal, Priority: evaluate.PriorityUser}
	if len(req.Text) > maxPostingText {
		return "", fmt.Errorf("%w: the posting text holds at most %d characters", agent.ErrRefused, maxPostingText)
	}
	h, err := a.s.submitByURL(ctx, d, evaluate.Claim{Principal: agentPrincipal, Key: "ag-" + randomKey()}, req)
	switch {
	case errors.Is(err, evaluate.ErrQueueFull):
		return "", fmt.Errorf("%w: the evaluation queue is full; try again shortly", agent.ErrRefused)
	case err != nil:
		return "", refusal(err)
	}
	a.noteOrigin(ctx, d, h.RunID, r.ConversationID, "evaluate_job")
	a.s.notify("runs")
	return h.RunID, nil
}

// noteOrigin ties a run to the conversation that started it, before the board
// first hears of the run, so its end is told to that conversation.
func (a agentActions) noteOrigin(ctx context.Context, d *domain, runID, sessionID, tool string) {
	if err := d.origins.Record(context.WithoutCancel(ctx), runID, sessionID, tool); err != nil {
		a.s.log.Warn("the origin of a run was not recorded", "run", runID, "error", err.Error())
	}
}

func (a agentActions) StartScan(ctx context.Context, r agent.ScanRequest) (string, error) {
	ref, err := a.s.launchScan(ctx, agentPrincipal, "ag-"+randomKey(), wire.ScanRequest{Source: r.Source})
	if err != nil {
		return "", refusal(err)
	}
	if d, derr := a.s.dom(); derr == nil {
		a.noteOrigin(ctx, d, ref.RunID, r.ConversationID, "scan_portal")
	}
	return ref.RunID, nil
}

// ScanConflict is the admission check of a scan, asked without starting one:
// the refusal a scan of the source would meet now.
func (a agentActions) ScanConflict(ctx context.Context, source string) error {
	d, err := a.s.dom()
	if err != nil {
		return err
	}
	var running *discovery.RunningError
	switch err := refuseRunningScan(ctx, d.repo, source); {
	case errors.As(err, &running):
		return fmt.Errorf("%w: %s", agent.ErrRefused, running.Error())
	case err != nil:
		return err
	case d.scans.busy(source):
		return fmt.Errorf("%w: a %s scan is already running", agent.ErrRefused, source)
	}
	return nil
}

func (a agentActions) StartResume(ctx context.Context, r agent.ResumeRequest) (string, error) {
	svc, err := a.s.docs()
	if err != nil {
		return "", err
	}
	req := documents.Request{Action: documents.ActionResume, Mode: documents.ModeTailored, CandidacyID: r.CandidacyID, PDF: r.PDF, Actor: tracker.ActorAgent}
	if r.CoverLetter {
		req.Action = documents.ActionPair
	}
	h, err := svc.Submit(ctx, documents.Claim{Principal: agentPrincipal, Key: "ag-" + randomKey()}, req)
	if err != nil {
		return "", refusal(documentsapi.DocumentError(err))
	}
	if d, derr := a.s.dom(); derr == nil {
		a.noteOrigin(ctx, d, h.RunID, r.ConversationID, "tailor_resume")
	}
	a.s.notify("runs")
	return h.RunID, nil
}

// PlanRegistration reads the plan for a sign-up destination: the system the
// destination belongs to, the account creation gate's answer for a run the
// user starts now, and whether a sign-in is already stored. It writes nothing.
func (a agentActions) PlanRegistration(ctx context.Context, destination string) (agent.RegistrationPlan, error) {
	d, err := a.s.dom()
	if err != nil {
		return agent.RegistrationPlan{}, err
	}
	cl, stop := apply.Classify(destination)
	if stop != nil || cl.ATS == "" {
		return agent.RegistrationPlan{Supported: false}, nil
	}
	system := string(cl.ATS)
	key := "generic"
	if slices.Contains(gates.SignupSystems, system) {
		key = system
	}
	host := hostOf(destination)
	plan := agent.RegistrationPlan{Host: host, System: system, Supported: key != "generic"}
	cooldowns, err := d.accounts.Cooldowns(ctx)
	if err != nil {
		return plan, err
	}
	dec := gates.AccountCreation(gates.AccountInput{Demo: a.s.cfg.Workspace.IsDemo(), Now: a.s.now().UnixMilli(), UserStarted: true, System: key,
		Cooldowns: cooldowns, LegalSettingOn: true})
	plan.GateAllows, plan.GateReason = dec.Allowed(), dec.Reason
	stored, err := d.creds.StoredAccounts(ctx)
	if err != nil {
		return plan, err
	}
	for _, acc := range stored {
		if strings.EqualFold(acc.Host, host) || (acc.System != "" && strings.EqualFold(acc.System, system) && strings.EqualFold(acc.Tenant, cl.Tenant)) {
			plan.HasAccount = true
		}
	}
	return plan, nil
}

// agentSkipList edits the profile's batch skip list through the workspace's
// structured edit, so comments and key order in profile.yml stay as written.
type agentSkipList struct{ ws *workspace.Workspace }

const skipKey = "batch_skip_companies"

func (e agentSkipList) read() (workspace.Profile, workspace.Content, error) {
	cur, err := e.ws.Read(workspace.FileProfile)
	if err != nil {
		return workspace.Profile{}, cur, err
	}
	if cur.Revision == workspace.RevisionAbsent {
		return workspace.Profile{}, cur, fmt.Errorf("%w: there is no profile yet", agent.ErrRefused)
	}
	p, _ := workspace.ParseProfile(cur.Data)
	return p, cur, nil
}

func (e agentSkipList) SkipList(context.Context) ([]string, bool, error) {
	p, _, err := e.read()
	if err != nil {
		return nil, false, err
	}
	eff := p.EffectiveSkipCompanies()
	return eff, len(p.BatchSkipCompanies) == 0 && len(eff) > 0, nil
}

func (e agentSkipList) AddSkip(_ context.Context, company string) (bool, error) {
	p, cur, err := e.read()
	if err != nil {
		return false, err
	}
	eff := p.EffectiveSkipCompanies()
	if slices.ContainsFunc(eff, func(s string) bool { return strings.EqualFold(s, company) }) {
		return false, nil
	}
	if len(eff) >= gates.MaxSkipEntries || len([]rune(company)) > gates.MaxSkipEntryLength {
		return false, fmt.Errorf("%w: the skip list holds at most %d entries of %d characters", agent.ErrRefused, gates.MaxSkipEntries, gates.MaxSkipEntryLength)
	}
	return e.write(cur, p, append(slices.Clone(eff), company), workspace.Op{Kind: workspace.OpAppend, Path: skipKey, Value: jsonText(company)})
}

func (e agentSkipList) RemoveSkip(_ context.Context, company string) (bool, error) {
	p, cur, err := e.read()
	if err != nil {
		return false, err
	}
	eff := p.EffectiveSkipCompanies()
	i := slices.IndexFunc(eff, func(s string) bool { return strings.EqualFold(s, company) })
	if i < 0 {
		return false, nil
	}
	after := slices.Delete(slices.Clone(eff), i, i+1)
	return e.write(cur, p, after, workspace.Op{Kind: workspace.OpRemove, Path: skipKey, Match: eff[i]})
}

// write applies the edit. A profile with no list of its own gets the whole
// effective list written, so the built-in names the user did not touch stay.
func (e agentSkipList) write(cur workspace.Content, p workspace.Profile, after []string, op workspace.Op) (bool, error) {
	if len(p.BatchSkipCompanies) == 0 {
		op = workspace.Op{Kind: workspace.OpSet, Path: skipKey, Value: jsonText(after)}
	}
	if _, err := e.ws.Edit(workspace.FileProfile, []workspace.Op{op}, cur.Revision); err != nil {
		var conflict *workspace.ConflictError
		if errors.As(err, &conflict) {
			return false, fmt.Errorf("%w: profile.yml changed while the edit was prepared; ask again", agent.ErrRefused)
		}
		return false, err
	}
	return true, nil
}

// jsonText renders a value as JSON, which is valid YAML flow text.
func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func hostOf(raw string) string {
	if _, rest, ok := strings.Cut(raw, "://"); ok {
		raw = rest
	}
	host, _, _ := strings.Cut(raw, "/")
	host, _, _ = strings.Cut(host, "?")
	host, _, _ = strings.Cut(host, ":")
	return strings.ToLower(host)
}

// Draft drafts one answer with the answer bank's composer, under the same
// refusals, grounding and budget as the autofill lane. The stored grounding
// mode applies except dangerous, which only a supervised form fill may use.
func (a agentActions) Draft(ctx context.Context, r agent.DraftRequest) (agent.DraftResult, error) {
	svc, err := a.s.composeService()
	if err != nil {
		return agent.DraftResult{}, err
	}
	src, err := a.s.composeSources(r.Host)
	if err != nil {
		return agent.DraftResult{}, err
	}
	bank, err := a.s.answerBank()
	if err != nil {
		return agent.DraftResult{}, err
	}
	mode := assist.Mode(bank.Settings(ctx).GroundingMode)
	if mode == assist.ModeDangerous {
		mode = assist.ModeFlex
	}
	var from, jobURL string
	if strings.TrimSpace(r.PostingText) == "" {
		r.PostingText, from, jobURL = a.storedPosting(ctx, r.Company, r.Role)
	}
	y, m, d := a.s.now().Date()
	out := svc.Compose(ctx, assist.ComposeRequest{Question: r.Question, Host: r.Host, MaxLength: r.MaxLength, Company: r.Company, Role: r.Role,
		JD: truncateRunes(r.PostingText, assist.MaxPostingTextChars), JobURL: jobURL, Mode: mode, Sources: src, Today: match.Date{Year: y, Month: int(m), Day: d}})
	if !out.Filled {
		return agent.DraftResult{Reason: string(out.Reason), PostingFrom: from}, nil
	}
	return agent.DraftResult{Filled: true, Answer: out.Answer, Cited: out.Cited, PostingFrom: from}, nil
}

// storedPosting is the posting text of the one evaluated tracker row a company
// (and role) names, read from its report the way a tailored document reads it,
// the report it came from and the posting's address (which may declare the
// company's own site for the composer's company context). Nothing is returned
// when no row or more than one matches: a posting is never guessed.
func (a agentActions) storedPosting(ctx context.Context, company, role string) (text, from, postingURL string) {
	company, role = strings.TrimSpace(company), strings.TrimSpace(role)
	d, err := a.s.dom()
	if company == "" || err != nil {
		return "", "", ""
	}
	page, err := d.tracker.List(ctx, tracker.ListQuery{Company: company, Sort: tracker.SortUpdatedDesc, Page: 1, PageSize: 25})
	if err != nil {
		return "", "", ""
	}
	var named, exact []tracker.Entry
	for _, e := range page.Entries {
		if e.ReportNumber == nil {
			continue
		}
		named = append(named, e)
		if strings.EqualFold(strings.TrimSpace(e.Company), company) {
			exact = append(exact, e)
		}
	}
	if len(exact) > 0 {
		named = exact
	}
	if role != "" {
		var same []tracker.Entry
		for _, e := range named {
			if sameRole(e.Role, role) {
				same = append(same, e)
			}
		}
		named = same
	}
	if len(named) != 1 {
		return "", "", ""
	}
	e := named[0]
	text, err = d.docs.PostingOf(ctx, e.ID)
	if err != nil || strings.TrimSpace(text) == "" {
		return "", "", ""
	}
	return text, fmt.Sprintf("evaluation report %d (%s, %s)", *e.ReportNumber, e.Company, e.Role), e.PostingURL
}

// sameRole says two role titles name the same job: the words of one (three
// letters or more, case and punctuation aside) are all in the other, so
// "Frontend Engineer" matches "Software Engineer, Frontend" and "Backend
// Engineer" does not.
func sameRole(a, b string) bool {
	words := func(s string) map[string]bool {
		out := map[string]bool{}
		for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
			if len([]rune(w)) >= 3 {
				out[w] = true
			}
		}
		return out
	}
	covers := func(x, y map[string]bool) bool {
		for w := range x {
			if !y[w] {
				return false
			}
		}
		return len(x) > 0
	}
	wa, wb := words(a), words(b)
	return covers(wa, wb) || covers(wb, wa)
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// PlanApplication reads what the apply chain would face for a posting. It
// writes nothing and opens no page.
func (a agentActions) PlanApplication(ctx context.Context, postingURL string) (agent.ApplicationPlan, error) {
	d, err := a.s.dom()
	if err != nil {
		return agent.ApplicationPlan{}, err
	}
	plan := agent.ApplicationPlan{URL: postingURL, Host: hostOf(postingURL)}
	cl, stop := apply.Classify(postingURL)
	if stop != nil {
		return plan, nil
	}
	plan.Supported, plan.System, plan.Tenant = true, cl.ATS, cl.Tenant
	plan.AccountRequired, plan.AccountKnown = apply.AccountRequired(cl.ATS)
	key, err := identity.URLKey(postingURL)
	if err != nil {
		return plan, fmt.Errorf("%w: %v", agent.ErrRefused, err)
	}
	if found, err := d.tracker.Lookup(ctx, postingURL); err == nil && found.Found {
		e := found.Entry
		plan.Company, plan.Role, plan.Status, plan.Score = e.Company, e.Role, string(e.Status), e.Score
	}
	if plan.Submitted, err = a.submissionOf(ctx, key); err != nil {
		return plan, err
	}
	if plan.Company != "" {
		prof, err := evaluate.LoadProfile(a.s.cfg.Workspace)
		if err == nil {
			for _, entry := range prof.EffectiveSkipCompanies() {
				if gates.CoversCompany(entry, plan.Company) {
					plan.SkipEntry = entry
					break
				}
			}
		}
	}
	if lanes, err := a.s.apply.AgentApplyLanes()(ctx); err == nil {
		for _, l := range lanes {
			for _, it := range l.Items {
				if k, err := identity.URLKey(it.URL); err == nil && k == key {
					plan.OnList = true
				}
			}
		}
	}
	reg, err := a.PlanRegistration(ctx, postingURL)
	if err != nil {
		return plan, err
	}
	plan.HasSignIn, plan.SignUpAllows, plan.SignUpReason = reg.HasAccount, reg.GateAllows, reg.GateReason
	return plan, nil
}

// submissionOf words the ledger's record of a posting, or returns "" when
// nothing was submitted.
func (a agentActions) submissionOf(ctx context.Context, identityKey string) (string, error) {
	st, err := a.s.db()
	if err != nil {
		return "", err
	}
	var state, at string
	err = st.Read().QueryRowContext(ctx, `SELECT state, observed_at FROM submissions WHERE identity_key = ? AND state IN ('submitted', 'may-have-submitted')
		ORDER BY observed_at DESC LIMIT 1`, identityKey).Scan(&state, &at)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", err
	case state == "may-have-submitted":
		return "marked as possibly submitted on " + datePart(at) + " (not confirmed)", nil
	}
	return "submitted on " + datePart(at), nil
}

func datePart(stamp string) string {
	if i := strings.IndexByte(stamp, 'T'); i > 0 {
		return stamp[:i]
	}
	return stamp
}

// researchSource serves research_question the web-search provider of the
// hiring-signal collector: the same client and the same keychain key.
func researchSource(sg *signals.Service) func() research.Searcher {
	return func() research.Searcher {
		p := sg.SearchProvider()
		if p == nil {
			return nil
		}
		return searchHits{p}
	}
}

type searchHits struct{ p signals.SearchProvider }

func (s searchHits) Search(ctx context.Context, query string, n int) ([]research.Hit, error) {
	found, err := s.p.Search(ctx, query, n, signals.Scope{})
	if err != nil {
		var ce *signals.CollectError
		if errors.As(err, &ce) && ce.RequiredAction != "" {
			err = fmt.Errorf("%s; %s", ce.Reason, ce.RequiredAction)
		}
		return nil, err
	}
	out := make([]research.Hit, len(found))
	for i, h := range found {
		out[i] = research.Hit{URL: h.URL, Title: h.Title, Text: cmp.Or(h.Text, h.Snippet)}
	}
	return out, err
}

// The harness routes stay in api while the Memory routes are in agentapi:
// they draw on the self-improvement service, which imports the agent runtime
// and the answer-bank matcher.

// agentHarness answers the self-improvement view: the versions, the recent
// rounds and the kept lessons.
func (s *Server) agentHarness(ctx context.Context, d *domain) (wire.AgentHarness, error) {
	vs, err := d.harness.Versions(ctx, 50)
	if err != nil {
		return wire.AgentHarness{}, err
	}
	rounds, err := d.improve.Rounds(ctx, 30)
	if err != nil {
		return wire.AgentHarness{}, err
	}
	lessons, err := d.improve.Lessons(ctx)
	if err != nil {
		return wire.AgentHarness{}, err
	}
	out := wire.AgentHarness{Running: d.improve.Running(), Versions: []wire.AgentHarnessVersion{}, Rounds: []wire.AgentImproveRound{}, Lessons: []wire.AgentLesson{}}
	for _, v := range vs {
		if v.Active {
			out.Active = apikit.Safe64(v.Version)
		}
		notes := v.ToolNotes
		if notes == nil {
			notes = map[string]string{}
		}
		guidance := v.Guidance
		if guidance == nil {
			guidance = []string{}
		}
		out.Versions = append(out.Versions, wire.AgentHarnessVersion{ID: apikit.Safe64(v.Version), ParentID: apikit.Safe64(v.ParentID), Source: v.Source, Summary: v.Summary,
			Guidance: guidance, ToolNotes: notes, Active: v.Active, CreatedAt: wire.Time{Time: v.CreatedAt}})
	}
	for _, r := range rounds {
		w := wire.AgentImproveRound{ID: r.ID, StartedAt: wire.Time{Time: parseStamp(r.StartedAt)}, Outcome: r.Outcome,
			Detail: r.Detail, Adopted: apikit.Safe64(r.Adopted), Probes: apikit.Safe(r.Probes)}
		if r.FinishedAt != "" {
			w.FinishedAt = &wire.Time{Time: parseStamp(r.FinishedAt)}
		}
		out.Rounds = append(out.Rounds, w)
	}
	for _, l := range lessons {
		out.Lessons = append(out.Lessons, wire.AgentLesson{ID: l.ID, Cue: l.Cue, Lesson: l.Lesson, Evidence: l.Evidence, Confidence: l.Confidence,
			Version: apikit.Safe64(l.Version), Undone: l.Undone, CreatedAt: wire.Time{Time: parseStamp(l.CreatedAt)}})
	}
	return out, nil
}

func parseStamp(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func (s *Server) getAgentHarness(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	out, err := s.agentHarness(c.R.Context(), d)
	return apikit.Reply{Data: out}, err
}

// restoreAgentHarness makes an earlier version active again: the one way a
// person changes the harness by hand.
func (s *Server) restoreAgentHarness(c *apikit.Call) (apikit.Reply, error) {
	var req wire.AgentHarnessRestore
	if e := apikit.Decode(c.Body, &req); e != nil {
		return apikit.Reply{}, e
	}
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	ctx := c.R.Context()
	if _, err := d.improve.Restore(ctx, int64(req.ID)); err != nil {
		if errors.Is(err, harnessv.ErrNotFound) {
			return apikit.Reply{}, apikit.NewErr(wire.CodeNotFound, "no such harness version")
		}
		return apikit.Reply{}, err
	}
	out, err := s.agentHarness(ctx, d)
	return apikit.Reply{Data: out}, err
}

// startAgentRound runs a self-improvement round now, in the background.
func (s *Server) startAgentRound(c *apikit.Call) (apikit.Reply, error) {
	d, err := s.dom()
	if err != nil {
		return apikit.Reply{}, err
	}
	if err := d.improve.Start(); err != nil {
		if errors.Is(err, improve.ErrBusy) {
			return apikit.Reply{}, apikit.NewErr(wire.CodeConflict, "a round is already running")
		}
		return apikit.Reply{}, err
	}
	out, err := s.agentHarness(c.R.Context(), d)
	out.Running = true
	return apikit.Reply{Data: out}, err
}
