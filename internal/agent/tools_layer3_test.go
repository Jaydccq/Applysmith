package agent

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"applysmith/internal/agent/approvals"
	"applysmith/internal/documents"
	"applysmith/internal/evaluate"
	"applysmith/internal/llm"
	"applysmith/internal/messages"
	"applysmith/internal/research"
	"applysmith/internal/tracker"
)

type stubDrafter struct {
	res DraftResult
	req *DraftRequest
	n   *int
}

func (s stubDrafter) Draft(_ context.Context, r DraftRequest) (DraftResult, error) {
	if s.req != nil {
		*s.req = r
	}
	if s.n != nil {
		*s.n++
	}
	return s.res, nil
}

type stubPlanner struct{ plan ApplicationPlan }

func (s stubPlanner) PlanApplication(_ context.Context, u string) (ApplicationPlan, error) {
	p := s.plan
	p.URL = u
	return p, nil
}

type stubDocs struct{}

func (stubDocs) List(_ context.Context, id int64) ([]documents.Document, error) {
	return []documents.Document{{Kind: documents.KindResume, Version: 2, Mode: documents.ModeTailored, PDFState: documents.PDFReady, Current: true}}, nil
}

func layer3Env(t *testing.T, deps ToolDeps) *confEnv {
	t.Helper()
	f := newFixture(t)
	deps.Tracker, deps.Store = f.tracker, f.st
	reg, err := NewRegistry(Layer3Tools(deps)...)
	if err != nil {
		t.Fatal(err)
	}
	e := &confEnv{clock: &cfClock{t: time.Date(2027, 3, 3, 9, 0, 0, 0, time.UTC)}, f: f}
	f.registry = reg
	e.cards = approvals.New(f.st, f.sessions, e.clock.now, time.UTC)
	e.conf = NewConfirmations(ConfirmationsConfig{Cards: e.cards, Sessions: f.sessions})
	guard := NewGuard(reg, GuardOptions{Sessions: f.sessions, Salt: "salt", Grants: e.conf})
	e.bridge = NewBridge(BridgeOptions{Registry: reg, Guard: guard, Sessions: f.sessions, Packer: ContextPacker{Sessions: f.sessions}, Confirmations: e.conf})
	return e
}

func TestAgent_Draft_NoAnswerWhenNoSource(t *testing.T) {
	var req DraftRequest
	e := layer3Env(t, ToolDeps{Drafter: stubDrafter{res: DraftResult{Reason: "no-grounding"}, req: &req}})
	tn := e.turn(t, ChannelDashboard, "draft an answer for me", false)
	res := tn.call(t, "c1", "draft_application_answer", `{"question":"Why do you want to work at Example Corp?","company":"Example Corp","host":"jobs.example.test"}`)
	if res.IsError || !strings.Contains(res.Text, "NO_ANSWER: no source the system holds supports an answer") || strings.Contains(res.Text, "Draft (") {
		t.Fatalf("result = %+v", res)
	}
	if strings.Contains(res.Text, "answer bank") {
		t.Fatalf("an ungrounded question points to the bank: %q", res.Text)
	}
	if req.Company != "Example Corp" || req.Host != "jobs.example.test" {
		t.Fatalf("request = %+v", req)
	}
}

// A draft composed from a stored posting says which report it read, and the
// posting's words came from the employer, so the turn is tainted like any read
// of outside text; a draft from the user's own sources leaves it trusted.
func TestAgent_Draft_FromAStoredPostingNamesTheReportAndTaints(t *testing.T) {
	e := layer3Env(t, ToolDeps{Drafter: stubDrafter{res: DraftResult{Filled: true, Answer: "I care about the details users feel.", Cited: []string{"profile#employment"},
		PostingFrom: "evaluation report 2 (Ramp, Software Engineer, Frontend)"}}})
	tn := e.turn(t, ChannelDashboard, "draft my answer to why I want to join Ramp", false)
	res := tn.call(t, "c1", "draft_application_answer", `{"question":"Why do you want to join Ramp?","company":"Ramp"}`)
	if res.IsError || !strings.Contains(res.Text, "Posting text: from evaluation report 2 (Ramp, Software Engineer, Frontend).") || !strings.Contains(res.Text, "Draft (cite these sources: profile#employment)") {
		t.Fatalf("result = %+v", res)
	}
	if !tn.turn.Untrusted() {
		t.Error("a draft built on a stored posting left the turn trusted")
	}
	own := layer3Env(t, ToolDeps{Drafter: stubDrafter{res: DraftResult{Filled: true, Answer: "I led a reef survey.", Cited: []string{"profile#employment"}}}})
	tn = own.turn(t, ChannelDashboard, "draft", false)
	if res := tn.call(t, "c1", "draft_application_answer", `{"question":"Describe a project you led."}`); res.IsError || tn.turn.Untrusted() || strings.Contains(res.Text, "Posting text") {
		t.Errorf("own-source draft: %+v, untrusted %v", res, tn.turn.Untrusted())
	}
}

func TestAgent_Draft_IdentityWorkAuthSalaryAndSignatureAreRefusedWithAPointerToTheBank(t *testing.T) {
	for _, reason := range []string{"sensitive-category", "section-policy", "self-id-policy", "unsupported"} {
		e := layer3Env(t, ToolDeps{Drafter: stubDrafter{res: DraftResult{Reason: reason}}})
		tn := e.turn(t, ChannelDashboard, "draft", false)
		res := tn.call(t, "c1", "draft_application_answer", `{"question":"Are you legally authorized to work in the United States?"}`)
		if !strings.HasPrefix(res.Text, "<<untrusted") && !strings.Contains(res.Text, "NO_ANSWER") {
			t.Fatalf("%s: %q", reason, res.Text)
		}
		if !strings.Contains(res.Text, "never composed") || !strings.Contains(res.Text, "answer bank") {
			t.Errorf("%s: no pointer to the answer bank: %q", reason, res.Text)
		}
	}
}

func TestAgent_Draft_AStatementAboutTheApplicationNeverReachesTheDrafter(t *testing.T) {
	n := 0
	e := layer3Env(t, ToolDeps{Drafter: stubDrafter{res: DraftResult{Filled: true, Answer: "Yes", Cited: []string{"profile#name"}}, n: &n}})
	tn := e.turn(t, ChannelDashboard, "draft", false)
	for i, q := range []string{"Did you submit a cover letter with this application?", "Have you applied to this company before?", "Did you use AI to complete this application?"} {
		res := tn.call(t, "c"+string(rune('a'+i)), "draft_application_answer", `{"question":"`+q+`"}`)
		if !strings.Contains(res.Text, "NO_ANSWER") || !strings.Contains(res.Text, "observed") {
			t.Errorf("%q: %q", q, res.Text)
		}
	}
	if n != 0 {
		t.Fatalf("the drafter ran %d times for application-state questions", n)
	}
}

func TestAgent_Plan_ReadsOnlyAndNeverStartsABrowserAction(t *testing.T) {
	score := 4.6
	plan := ApplicationPlan{Host: "jobs.lever.co", System: "lever", Supported: true, Company: "Example Corp", Role: "Backend Engineer", Status: "Evaluated", Score: &score,
		OnList: true, AccountKnown: true, HasSignIn: false, SignUpAllows: false, SignUpReason: "the weekly sign-up quota is used up"}
	e := layer3Env(t, ToolDeps{Planner: stubPlanner{plan: plan}})
	tool, _ := e.f.registry.Lookup("application_plan")
	if tool.Class != ClassDraft || tool.DeclaresSubmit() || tool.Idempotent || tool.OutsideContent {
		t.Fatalf("metadata = %+v", tool)
	}
	tn := e.turn(t, ChannelDashboard, "plan the application", false)
	res := tn.call(t, "c1", "application_plan", `{"url":"https://jobs.lever.co/example/0f8c6a4e-1111-4222-8333-444455556666"}`)
	for _, want := range []string{"Example Corp", "lever", "today's apply list", "No account is needed", "stop before the final Submit", "started nothing"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("plan lacks %q: %q", want, res.Text)
		}
	}
	if tn.turn.Untrusted() {
		t.Fatal("a plan tainted the turn")
	}
	// A draft passes the chain with a trace and holds nothing, on any channel.
	for _, ch := range []Channel{ChannelTelegram, ChannelScheduled} {
		tt := e.turn(t, ch, "plan it", false)
		if r := tt.call(t, "c2", "application_plan", `{"url":"https://jobs.lever.co/example/1"}`); strings.HasPrefix(r.Text, "Held:") || r.IsError {
			t.Errorf("%s: %+v", ch, r)
		}
	}
	if r := tn.call(t, "c3", "application_plan", `{"url":"http://127.0.0.1:47319/api"}`); !strings.Contains(r.Text, "Refused") {
		t.Fatalf("a loopback URL = %q", r.Text)
	}
}

func TestAgent_Plan_BlockedAndLeftForTheUserCases(t *testing.T) {
	cases := []struct {
		name string
		plan ApplicationPlan
		want []string
		not  []string
	}{
		{"already submitted", ApplicationPlan{Supported: true, System: "lever", Submitted: "submitted on 2027-03-01"}, []string{"Blocked", "already submitted on 2027-03-01"}, []string{"Ask to start"}},
		{"skip list", ApplicationPlan{Supported: true, System: "lever", SkipEntry: "Example Corp"}, []string{"Left to the user", `"Example Corp"`, "still started"}, nil},
		{"no adapter", ApplicationPlan{Host: "careers.example.test"}, []string{"no adapter", "by hand"}, []string{"Steps the chain"}},
		{"stored sign-in", ApplicationPlan{Supported: true, System: "workday", AccountRequired: true, AccountKnown: true, HasSignIn: true}, []string{"Sign in with the stored sign-in"}, nil},
		{"account needed, none allowed", ApplicationPlan{Supported: true, System: "workday", AccountRequired: true, AccountKnown: true, SignUpReason: "the weekly sign-up quota is used up"},
			[]string{"quota is used up", "stop for the user at the sign-in"}, []string{"No account is needed"}},
		{"no account needed", ApplicationPlan{Supported: true, System: "greenhouse", AccountKnown: true, SignUpReason: "no risk acknowledgement"}, []string{"No account is needed: greenhouse"}, []string{"sign-in.", "risk"}},
		{"generic form", ApplicationPlan{Supported: true, System: "generic"}, []string{"shows only on its page"}, []string{"would stop for the user at the sign-in"}},
	}
	for _, c := range cases {
		e := layer3Env(t, ToolDeps{Planner: stubPlanner{plan: c.plan}})
		tn := e.turn(t, ChannelDashboard, "plan", false)
		res := tn.call(t, "c1", "application_plan", `{"url":"https://jobs.example.test/p/1"}`)
		for _, w := range c.want {
			if !strings.Contains(res.Text, w) {
				t.Errorf("%s lacks %q: %q", c.name, w, res.Text)
			}
		}
		for _, n := range c.not {
			if strings.Contains(res.Text, n) {
				t.Errorf("%s holds %q: %q", c.name, n, res.Text)
			}
		}
	}
}

func TestAgent_Workspace_ReadsTheApplicationAndTaintsTheTurn(t *testing.T) {
	e := layer3Env(t, ToolDeps{Documents: stubDocs{}})
	row, err := e.f.tracker.Add(context.Background(), tracker.AddRow{Company: "Example Corp", Role: "Backend Engineer", URL: "https://jobs.example.test/acme/1", Status: tracker.Evaluated, Location: "Exampleville"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC).Format("2006-01-02T15:04:05.000Z")
	if err := e.f.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO submissions (identity_key, url, host, state, confirmed, source, observed_at, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			row.IdentityKey, row.PostingURL, "jobs.example.test", "submitted", 1, "test", now, now, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	tn := e.turn(t, ChannelDashboard, "open the application", false)
	res := tn.call(t, "c1", "application_workspace", `{"application_id":`+itoa64(row.ID)+`}`)
	for _, want := range []string{"Example Corp", "Report: none", "resume v2", "PDF ready", "current", "Submission: submitted", "confirmation observed"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("workspace lacks %q:\n%s", want, res.Text)
		}
	}
	if !tn.turn.Untrusted() {
		t.Fatal("the workspace read did not taint the turn")
	}
	if r := e.turn(t, ChannelDashboard, "open it", false).call(t, "c2", "application_workspace", `{"application_id":9999}`); !strings.Contains(r.Text, "There is no application 9999") {
		t.Fatalf("unknown id = %q", r.Text)
	}
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

// stubMessages records the requests and answers with a fixed draft or error.
type stubMessages struct {
	err      error
	outreach *messages.OutreachRequest
	followup *messages.FollowupRequest
}

func (s stubMessages) Outreach(_ context.Context, r messages.OutreachRequest) (messages.Draft, error) {
	if s.outreach != nil {
		*s.outreach = r
	}
	return messages.Draft{Mode: "outreach", Kind: r.Contact.Kind, Company: "Example Corp", Primary: messages.Variant{Angle: "fit", Message: "Hi Sam."}}, s.err
}

func (s stubMessages) Followup(_ context.Context, r messages.FollowupRequest) (messages.Draft, error) {
	if s.followup != nil {
		*s.followup = r
	}
	return messages.Draft{Mode: "followup", Kind: messages.FollowupPing, Company: "Example Corp", Primary: messages.Variant{Message: "Hi again."}}, s.err
}

type stubResearcher struct{ err error }

func (s stubResearcher) Research(context.Context, research.Request) (research.Answer, error) {
	return research.Answer{Text: "Example.", Sources: []research.Source{{N: 1, Title: "T", URL: "https://example.test/"}}, Found: 1, Via: "web search"}, s.err
}

type stubDecoder struct{ err error }

func (s stubDecoder) Decode(context.Context, evaluate.DecodeRequest) (evaluate.Decoding, error) {
	return evaluate.Decoding{Title: "Engineer", Company: "Example Corp", Level: evaluate.DecodeLevel{Seniority: "mid"}}, s.err
}

func TestTool_DraftOutreachPassesTheContactAndSendsNothing(t *testing.T) {
	f := newFixture(t)
	var got messages.OutreachRequest
	tool := toolOf(t, MessageTools(ToolDeps{Messages: stubMessages{outreach: &got}}), "draft_outreach")
	if tool.Class != ClassDraft || !tool.OutsideContent {
		t.Fatalf("class %s, outside %v", tool.Class, tool.OutsideContent)
	}
	res, err := runTool(t, tool, f, `{"application_id":3,"contact_kind":"peer","contact_name":"Sam","about_contact":"gave a talk on queues"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Candidacy != 3 || got.Contact.Kind != "peer" || got.Contact.Note != "gave a talk on queues" {
		t.Errorf("request %+v", got)
	}
	if !strings.Contains(res.Text, "Outreach draft (peer) for Example Corp") || !strings.Contains(res.NextAction, "Nothing was sent") {
		t.Errorf("result %+v", res)
	}
	if err := validateArgs(tool.params, []byte(`{"contact_kind":"friend"}`)); err == nil {
		t.Error("an unknown contact kind fails the schema")
	}
}

func TestTool_DraftFollowupRelaysRefusals(t *testing.T) {
	f := newFixture(t)
	for _, err := range []error{
		fmt.Errorf("%w: the follow-ups the cadence allows are already recorded", messages.ErrCadence),
		fmt.Errorf("%w: application 9", messages.ErrNotFound),
		fmt.Errorf("%w: the primary message: it states the number 300", messages.ErrNoDraft),
	} {
		tool := toolOf(t, MessageTools(ToolDeps{Messages: stubMessages{err: err}}), "draft_followup")
		res, rerr := runTool(t, tool, f, `{"application_id":9}`)
		if rerr != nil || !strings.HasPrefix(res.Text, "Not drafted: ") || strings.Contains(res.Text, "messages:") {
			t.Errorf("%v: %q, %v", err, res.Text, rerr)
		}
	}
	var got messages.FollowupRequest
	tool := toolOf(t, MessageTools(ToolDeps{Messages: stubMessages{followup: &got}}), "draft_followup")
	if _, err := runTool(t, tool, f, `{"application_id":4,"kind":"thank_you","context":"we talked about the queue rewrite"}`); err != nil {
		t.Fatal(err)
	}
	if got.Candidacy != 4 || got.Kind != "thank_you" || got.Context != "we talked about the queue rewrite" {
		t.Errorf("request %+v", got)
	}
}

func TestTool_DecodePostingReturnsTheDecode(t *testing.T) {
	f := newFixture(t)
	text := strings.Repeat("Build and run backend services. ", 10)
	res, err := runTool(t, toolOf(t, MessageTools(ToolDeps{Decoder: stubDecoder{}}), "decode_posting"), f, `{"posting_text":"`+text+`"}`)
	if err != nil || !strings.Contains(res.Text, "# Decode: Example Corp - Engineer") || !strings.Contains(res.Text, "not a score") {
		t.Fatalf("%q, %v", res.Text, err)
	}
	refused, err := runTool(t, toolOf(t, MessageTools(ToolDeps{Decoder: stubDecoder{err: fmt.Errorf("%w: too short", evaluate.ErrDecodeInput)}}), "decode_posting"), f, `{"posting_text":"`+text+`"}`)
	if err != nil || !strings.HasPrefix(refused.Text, "Not drafted: ") {
		t.Errorf("%q, %v", refused.Text, err)
	}
}

// A tool whose task has no model points where the Ask Agent's own stopped turn
// does, to Settings, Models, and not to the command-line remedy the router's
// error names. The errors are the router's own for a task with no route; a
// fresh workspace has neither a model nor a search key.
func TestTool_ANoModelResultPointsToSettingsNotACommand(t *testing.T) {
	unrouted := func(task string) error {
		return &llm.Error{Kind: llm.KindNoRoute, Task: task, Message: "no model is routed for " + task + "; run `applysmith llm configure --preset <name> --key-stdin`"}
	}
	posting := `{"posting_text":"` + strings.Repeat("Build and run backend services. ", 10) + `"}`
	question := `{"question":"What changed at Example Corp this year?"}`
	for _, c := range []struct {
		name string
		tool Tool
		args string
	}{
		{"an answer draft", toolOf(t, Layer3Tools(ToolDeps{Drafter: stubDrafter{res: DraftResult{Reason: "no-route"}}}), "draft_application_answer"), `{"question":"Why do you want to work at Example Corp?"}`},
		{"a follow-up draft", toolOf(t, MessageTools(ToolDeps{Messages: stubMessages{err: unrouted(messages.Task)}}), "draft_followup"), `{"application_id":9}`},
		{"a posting decode", toolOf(t, MessageTools(ToolDeps{Decoder: stubDecoder{err: unrouted("jd-decode")}}), "decode_posting"), posting},
		{"research with a search key", toolOf(t, Layer3Tools(ToolDeps{Researcher: stubResearcher{err: unrouted(research.Task)}}), "research_question"), question},
		{"research in a fresh workspace", researchTool(t, &researchModel{}, nil), question},
	} {
		res, err := runTool(t, c.tool, nil, c.args)
		if err != nil || strings.Contains(res.Text, "llm configure") || !strings.Contains(res.Text, "Settings, Models") {
			t.Errorf("%s: %q, %v", c.name, res.Text, err)
		}
	}
}

// A search for job postings goes to the pipeline and the scans, not the web:
// live, "find me new jobs" went to research_question, whose aggregator pages
// tainted the turn and held the scan the task tried next.
func TestResearchQuestion_DescriptionPointsJobSearchesAtThePipelineAndTheScans(t *testing.T) {
	tool := toolOf(t, Layer3Tools(ToolDeps{Researcher: stubResearcher{}}), "research_question")
	if !strings.Contains(tool.Description, "to find job postings (list_pipeline") || !strings.Contains(tool.Description, "scan_portal starts a scan") {
		t.Fatalf("description = %q", tool.Description)
	}
}
