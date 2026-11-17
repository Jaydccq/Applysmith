package api

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"applysmith/internal/agent"
	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/discovery"
	"applysmith/internal/gates"
	"applysmith/internal/notify"
	"applysmith/internal/signals"
	"applysmith/internal/workspace"
)

// unavailableWindow stands for a browser that cannot be reached: every run
// reaches the runner and stops at preflight, which needs no Chrome.
func unavailableWindow(c *Config) {
	c.ApplyWindow = func(context.Context) (apply.Window, error) { return nil, errors.New("the test browser is down") }
}

type applyRunRow struct{ caller, url, kind, stage, reason string }

func applyRunRows(t *testing.T, h *harness) []applyRunRow {
	t.Helper()
	rows, err := h.srv.store.Load().Read().Query(`SELECT caller, posting_url, outcome_kind, outcome_stage, outcome_reason FROM apply_runs ORDER BY started_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []applyRunRow
	for rows.Next() {
		var r applyRunRow
		if err := rows.Scan(&r.caller, &r.url, &r.kind, &r.stage, &r.reason); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func waitForApplyRun(t *testing.T, h *harness) applyRunRow {
	t.Helper()
	var got applyRunRow
	waitLong(t, "an application run to end", func() bool {
		for _, r := range applyRunRows(t, h) {
			if r.kind != "running" {
				got = r
				return true
			}
		}
		return false
	})
	return got
}

func TestApplyStack_AnApprovedHeldSubmitReachesTheRunnerAndTheBoard(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	cookie := h.session()
	id := heldCardFor(t, h, "autofill_application", `{"url":"https://jobs.example.test/a/1","submit":true}`, 4, agent.ChannelDashboard, "apply to that job")
	r := h.chat(cookie, "POST", "/api/agent/confirmations/"+id+"/approve", nil)
	if r.StatusCode != 200 {
		t.Fatalf("approve: %d %s", r.StatusCode, r.body)
	}
	out := decode[wire.ConfirmationOutcome](t, r)
	if out.Card.State != "approved" || !strings.Contains(out.Result, "Started to fill the application") {
		t.Fatalf("outcome = %+v", out)
	}
	run := waitForApplyRun(t, h)
	if run.caller != string(gates.CallerAskAgent) || run.url != "https://jobs.example.test/a/1" || run.kind != "stopped" || run.stage != "preflight" ||
		!strings.Contains(run.reason, "the test browser is down") {
		t.Fatalf("run = %+v", run)
	}
	var entry notify.Entry
	waitLong(t, "the board entry to end", func() bool {
		es, err := h.domainOf().notify.board.List(context.Background(), notify.Filter{Kinds: []notify.Kind{notify.KindApply}, Limit: 10})
		if err != nil || len(es) != 1 {
			return false
		}
		entry = es[0]
		return entry.State == notify.Done
	})
	if entry.Origin != notify.OriginAgent || entry.Outcome != notify.OutcomeFailed || !strings.HasPrefix(entry.ID, "run_") || entry.Link != "/runs/"+entry.ID {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestApplyStack_ARoutedApplyRequestStartsADryRunThroughTheGuardChain(t *testing.T) {
	h, l := chatHarness(t, trackerTurn, unavailableWindow)
	cookie := h.session()
	r := h.chat(cookie, "POST", "/api/chat/sessions/new/messages", map[string]any{"text": "apply to https://jobs.example.test/p/1"})
	var acc wire.Success[wire.ChatTurnAccepted]
	strictInto(t, r.body, &acc)
	if r.StatusCode != 202 {
		t.Fatalf("post: %d %s", r.StatusCode, r.body)
	}
	readStream(t, h, acc.Data.SessionID, "", terminalItem, func() {})
	if n := l.count(); n != 0 {
		t.Fatalf("%d pi processes for a routed request", n)
	}
	run := waitForApplyRun(t, h)
	if run.url != "https://jobs.example.test/p/1" || run.stage != "preflight" {
		t.Fatalf("run = %+v", run)
	}
}

// applyTurn plays a model that calls autofill_application with submit true.
func applyTurn(p *scriptedPi) {
	piStart(p)
	args := map[string]any{"url": "https://jobs.example.test/p/1", "submit": true, "step": "Filling"}
	p.emit(assistantLine([]map[string]any{{"type": "toolCall", "id": "call_1", "name": "autofill_application", "arguments": args}}, "toolUse"))
	p.emit(map[string]any{"type": "tool_execution_start", "toolCallId": "call_1", "toolName": "autofill_application", "args": args})
	_, res := p.bridge("POST", "/internal/agent/tool-call", map[string]any{"toolCallId": "call_1", "name": "autofill_application", "args": args})
	p.emit(map[string]any{"type": "tool_execution_end", "toolCallId": "call_1", "toolName": "autofill_application",
		"result": map[string]any{"content": []map[string]any{{"type": "text", "text": res["text"]}}}, "isError": res["isError"]})
	p.emit(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "Done."}})
	p.emit(assistantLine([]map[string]any{{"type": "text", "text": "Done."}}, "stop"))
	p.emit(map[string]any{"type": "agent_settled"})
}

func TestApplyStack_ASubmitTheModelChoseIsRefusedAndOneTheUserAskedForIsHeld(t *testing.T) {
	for _, c := range []struct {
		name, message string
		wantCards     int
	}{
		{"the message never asked to apply", "tell me what roles look good this week", 0},
		{"the user asked but never named the posting", "please apply for a good role", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, l := chatHarness(t, applyTurn, unavailableWindow)
			l.release()
			cookie := h.session()
			r := h.chat(cookie, "POST", "/api/chat/sessions/new/messages", map[string]any{"text": c.message})
			var acc wire.Success[wire.ChatTurnAccepted]
			strictInto(t, r.body, &acc)
			if r.StatusCode != 202 {
				t.Fatalf("post: %d %s", r.StatusCode, r.body)
			}
			readStream(t, h, acc.Data.SessionID, "", terminalItem, func() {})
			h.waitIdle(acc.Data.SessionID)
			if rows := applyRunRows(t, h); len(rows) != 0 {
				t.Fatalf("a submit the gates did not allow reached the runner: %+v", rows)
			}
			cards := decode[wire.ConfirmationList](t, h.get("/api/agent/confirmations"))
			if len(cards.Cards) != c.wantCards {
				t.Fatalf("%d cards, want %d: %+v", len(cards.Cards), c.wantCards, cards)
			}
		})
	}
}

func TestApplyStack_OneRunAtATimeBecauseTheyShareTheApplyWindow(t *testing.T) {
	hold := make(chan struct{})
	h := newDomainHarness(t, func(c *Config) {
		c.ApplyWindow = func(ctx context.Context) (apply.Window, error) {
			select {
			case <-hold:
			case <-ctx.Done():
			}
			return nil, errors.New("the test browser is down")
		}
	})
	a := h.domainOf().apply
	req := agent.ApplyRequest{Tool: gates.ToolAutofillApplication, Caller: gates.CallerAskAgent, Rows: []agent.ApplyRow{{URL: "https://jobs.example.test/p/1"}}}
	first, err := a.StartApply(context.Background(), req)
	if err != nil || first == "" {
		t.Fatalf("first start: %q, %v", first, err)
	}
	if _, err := a.StartApply(context.Background(), req); err == nil || !strings.Contains(err.Error(), "already working") {
		t.Fatalf("second start: %v", err)
	}
	if !a.runner.Live() || !h.srv.applyRunLive() {
		t.Fatal("a working run is not reported as live to the session import")
	}
	close(hold)
	waitForApplyRun(t, h)
	waitLong(t, "the window to free", func() bool { return !a.busy.Load() })
	if _, err := a.StartApply(context.Background(), req); err != nil {
		t.Fatalf("after the first ended: %v", err)
	}
}

func TestApplyStack_TheBoardEndIsDecidedByWhatTheChainSaidAboutTheStop(t *testing.T) {
	cases := []struct {
		name    string
		outcome apply.Outcome
		state   notify.State
		reason  string
		result  string
	}{
		{"submitted and confirmed", apply.Outcome{Kind: apply.OutcomeSubmitted, Confirmed: true}, notify.Done, "", notify.OutcomeOK},
		{"filled without a press", apply.Outcome{Kind: apply.OutcomeFilled}, notify.Done, "", notify.OutcomeOK},
		{"a sign-in the chain could not finish", apply.Outcome{Kind: apply.OutcomeStopped, Board: "sign-in", Reason: "wall"}, notify.NeedsYou, "sign-in", ""},
		{"a press that may have gone out", apply.Outcome{Kind: apply.OutcomeStopped, Board: "may-have-submitted"}, notify.NeedsYou, "may-have-submitted", ""},
		{"the repair round gave up", apply.Outcome{Kind: apply.OutcomeStopped, Board: "needs-user"}, notify.NeedsYou, "other", ""},
		{"a refused password", apply.Outcome{Kind: apply.OutcomeStopped, Board: "credential-refused"}, notify.Blocked, "credential-refused", ""},
		{"cancelled", apply.Outcome{Kind: apply.OutcomeStopped, Reason: "cancelled before account"}, notify.Done, "", notify.OutcomeCancelled},
		{"a stop with no label", apply.Outcome{Kind: apply.OutcomeStopped, Reason: "preflight blocked"}, notify.Done, "", notify.OutcomeFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			state, mv := boardEnd(c.outcome, false)
			if state != c.state || mv.Reason != c.reason || mv.Outcome != c.result {
				t.Fatalf("got %s %+v", state, mv)
			}
		})
	}
	// Live: the demo's GitLab run reached the submit stage, the gate refused
	// the press, and the conversation was told the run waited for a Submit
	// that a demo never takes.
	filled := apply.Outcome{Kind: apply.OutcomeFilled}
	// Live: the Ask Agent's dry run of Nuro closed its tab, and the board told
	// the user the form was waiting for their Submit.
	if _, mv := boardEnd(filled, false); strings.Contains(mv.Detail, "waiting") || !strings.Contains(mv.Detail, "the tab is closed") {
		t.Fatalf("a filled run: %+v", mv)
	}
	if state, mv := boardEnd(apply.Outcome{Kind: apply.OutcomeFilled, Board: apply.HoldConfirmation}, false); state != notify.NeedsYou || !strings.Contains(mv.Detail, "Submit waits for your confirmation") {
		t.Fatalf("a held dry run: %s %+v", state, mv)
	}
	if state, mv := boardEnd(filled, true); state != notify.Done || mv.Outcome != notify.OutcomeOK || strings.Contains(mv.Detail, "waiting") || !strings.Contains(mv.Detail, "never submits") {
		t.Fatalf("a filled demo run: %s %+v", state, mv)
	}
}

func TestApplyStack_NoBrowserWindowIsAPreflightStopNotAPanic(t *testing.T) {
	h := newDomainHarness(t)
	a := h.domainOf().apply
	if _, err := (applyEnv{a}).Attach(context.Background()); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("attach = %v", err)
	}
}

func TestMirrorProductionDependencies(t *testing.T) {
	for _, mode := range []string{"workday", "two-sites", "greenhouse", "web", "web-workday-site", "no-key"} {
		t.Run(mode, func(t *testing.T) {
			workdayReads, boardNames, searches := 0, 0, 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasPrefix(r.URL.Path, "/wday/cxs/"):
					workdayReads++
					var in map[string]any
					if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&in) != nil || in["searchText"] != "software engineer" {
						t.Errorf("bad search: %s %v", r.Method, in)
					}
					jobs := []map[string]any{{"title": "Software Engineer", "externalPath": "/job/Austin/Software-Engineer_R100", "locationsText": "Austin"}, {"title": "Senior Software Engineer", "externalPath": "/job/Austin/Senior_R200"}}
					// Cadence: one requisition on two career sites, each posting with its own -N.
					if mode == "two-sites" {
						n := "1"
						if strings.Contains(strings.ToLower(r.URL.Path), "/university/") {
							n = "2"
						}
						jobs = []map[string]any{{"title": "Software Engineer", "externalPath": "/job/Austin/Software-Engineer_R100-" + n, "locationsText": "Austin"}}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"total": len(jobs), "jobPostings": jobs})
				case r.URL.Path == "/v1/boards/example/jobs":
					if mode != "greenhouse" {
						http.NotFound(w, r)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"jobs": []map[string]any{{"id": 12345, "title": "Software Engineer", "absolute_url": "https://boards.greenhouse.io/example/jobs/12345", "location": map[string]any{"name": "Austin"}}}})
				case r.URL.Path == "/v1/boards/example":
					boardNames++
					_ = json.NewEncoder(w).Encode(map[string]any{"name": "Example"})
				case r.URL.Path == "/search":
					searches++
					hit := map[string]any{"title": "Software Engineer - Example", "url": "https://careers.example.com/jobs/12345"}
					if mode == "web-workday-site" {
						hit = map[string]any{"title": "Staff DevOps Engineer", "url": "https://example.wd5.myworkdayjobs.com/en-US/Careers/job/Austin/Staff-DevOps_R900"}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{hit}})
				default:
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			h := newDomainHarness(t, func(c *Config) {
				c.Domain.ScanEndpoints = discovery.Endpoints{}
				for _, ats := range discovery.ATSNames {
					c.Domain.ScanEndpoints[ats] = upstream.URL
				}
				c.Domain.ScanEndpoints["workday"] = upstream.URL
				c.Domain.SignalEndpoints = signals.Endpoints{Exa: upstream.URL}
			})
			if mode == "workday" || mode == "two-sites" {
				row, err := h.domainOf().repo.InboxAddURL(t.Context(), "https://example.wd5.myworkdayjobs.com/en-US/Careers/job/Austin/Old_R001")
				if err != nil || row.Row == nil {
					t.Fatalf("%+v %v", row, err)
				}
			}
			if mode == "two-sites" {
				row, err := h.domainOf().repo.InboxAddURL(t.Context(), "https://example.wd5.myworkdayjobs.com/en-US/University/job/Austin/Old-Grad_R002")
				if err != nil || row.Row == nil {
					t.Fatalf("%+v %v", row, err)
				}
			}
			if mode == "web" || mode == "web-workday-site" {
				if _, err := h.srv.cfg.Vault.Set(signals.KeyExa, "synthetic-test-key"); err != nil {
					t.Fatal(err)
				}
			}
			m := h.srv.mirrorSources(h.domainOf())
			out := m.Resolve(t.Context(), "https://mirror.example.test/jobs/12345", apply.MirrorHint{Company: "Example", Role: "Software Engineer"})
			switch mode {
			case "workday":
				if out.Via != "workday-search" || !strings.Contains(out.URL, "_R100") || workdayReads != 1 {
					t.Fatalf("%+v reads=%d", out, workdayReads)
				}
			case "two-sites":
				if out.Via != "workday-search" || !strings.Contains(out.URL, "_R100-") || workdayReads != 2 {
					t.Fatalf("%+v reads=%d", out, workdayReads)
				}
			case "greenhouse":
				if out.Via != "board-probe" || boardNames != 1 {
					t.Fatalf("%+v names=%d", out, boardNames)
				}
			case "web":
				if out.Via != "web-search" || searches != 3 {
					t.Fatalf("%+v searches=%d", out, searches)
				}
			case "web-workday-site":
				if out.Via != "workday-search" || !strings.Contains(out.URL, "example.wd5.myworkdayjobs.com/Careers/job/Austin/Software-Engineer_R100") || workdayReads != 1 {
					t.Fatalf("%+v reads=%d", out, workdayReads)
				}
			case "no-key":
				if out.URL != "" || searches != 0 {
					t.Fatalf("%+v searches=%d", out, searches)
				}
			}
		})
	}
}

// The ladder's knowledge sections are the user's own notes and résumé, each
// once; the profile, whose values reach a model only through the ladder's
// fact allowlist, is never one (F-10).
func TestApplyStack_KnowledgeSectionsAreTheUsersOwnNotes(t *testing.T) {
	h := newDomainHarness(t)
	writeKnowledge(t, h, "knowledge/experience/club.md", "## Ticketing\nBuilt ticketing for a sports league.\nLeague: Northwind Example Sports\n")
	writeKnowledge(t, h, "cv.md", "## Experience\nRan ticketing for a sports league.\n")
	h.writeProfile("version: 1\ncandidate:\n  full_name: Alex Example\nskills:\n  - name: Sports league ticketing\n")
	got, err := h.domainOf().apply.knowledge(context.Background(), "Do you have experience with sports league ticketing?")
	if err != nil {
		t.Fatal(err)
	}
	var refs []string
	for _, k := range got {
		refs = append(refs, k.Ref)
	}
	slices.Sort(refs)
	if want := []string{"cv.md#Experience", "knowledge/experience/club.md#Ticketing"}; !slices.Equal(refs, want) {
		t.Fatalf("sections %q, want %q", refs, want)
	}
}

// One document gives one section, its best: three framing sections of the
// narrative left Evertune's "Describe a backend system you built" with no
// project.
func TestApplyStack_KnowledgeGivesOneSectionPerDocument(t *testing.T) {
	h := newDomainHarness(t)
	writeKnowledge(t, h, "narrative.md", "## Exit\nBackend systems builder moving into AI systems.\n## Roles\nBackend systems and AI platform roles.\n## Advantage\nBuilds backend systems that deploy.\n")
	writeKnowledge(t, h, "knowledge/behavioral/relay.md", "## Relay\nI built the relay backend system and moved its deploys to containers.\n")
	got, err := h.domainOf().apply.knowledge(context.Background(), "Describe a backend system you built.")
	if err != nil {
		t.Fatal(err)
	}
	var docs []string
	for _, k := range got {
		doc, _, _ := strings.Cut(k.Ref, "#")
		docs = append(docs, doc)
	}
	slices.Sort(docs)
	if want := []string{"knowledge/behavioral/relay.md", "narrative.md"}; !slices.Equal(docs, want) {
		t.Fatalf("documents %q, want %q", docs, want)
	}
}

// The review and the model rungs see where the applicant lives, to the city:
// with no place they flagged a correct "Yes" to "Are you currently based in the
// U.S. or Canada?" (Runway). The street and postal code never reach them.
func TestProfileFactsGiveTheCityNotTheStreet(t *testing.T) {
	var p workspace.Profile
	p.Candidate.Address = workspace.Address{Line1: "12 Example Street", City: "Charleston", Region: "South Carolina", PostalCode: "29401", Country: "US"}
	f := profileFacts(p, factsToday)
	if got := f["current location"]; got != "Charleston, South Carolina, United States" {
		t.Fatalf("current location = %q", got)
	}
	if !gates.LadderFactAllowed("current location") {
		t.Fatal("the current location is withheld from the model rungs")
	}
	for k, v := range f {
		if strings.Contains(v, "Example Street") || strings.Contains(v, "29401") {
			t.Fatalf("%s carries the street or postal code: %q", k, v)
		}
	}
}

// A job reaches the review with its dates and ahead of the skills, which the
// review's line cap cuts: with no job it flagged a work row's correct start
// (Symbotic). Its summary too: with none, CharterUP's question on
// professional TypeScript experience saw no job's stack.
func TestProfileFactsGiveTheJobsBeforeTheSkills(t *testing.T) {
	var p workspace.Profile
	for i := range 20 {
		p.Skills = append(p.Skills, workspace.Skill{Name: "Skill " + strconv.Itoa(i+10), Level: "advanced"})
	}
	p.History.Employment = []workspace.Employment{{Employer: "Example Co", Title: "Software Engineer", Start: "2026-06", End: "present", Location: "Durham, NC", Summary: "Builds the Go/TypeScript services."}}
	f := profileFacts(p, factsToday)
	if got := f["experience 1"]; got != "Software Engineer, Example Co, Durham, NC, 2026-06 to present (6 months), Builds the Go/TypeScript services." {
		t.Fatalf("experience 1 = %q", got)
	}
	keys := slices.Sorted(maps.Keys(f))
	if !gates.LadderFactAllowed("experience 1") || !slices.Contains(keys[:gates.ReviewProfileMaxLines-2], "experience 1") {
		t.Fatalf("the job is not among the review's lines: %q", keys)
	}
}

// factsToday is the date the profile facts are read on.
var factsToday = time.Date(2026, 11, 8, 15, 0, 0, 0, time.UTC)

// BCC (Greenhouse, queue312): the review read "2026-06 to present", five
// months on, as "over two years" and flagged "less than one year" of
// experience. A job carries its length in whole months, both end months
// counted as a résumé counts them, so no model does the date arithmetic.
func TestProfileFactsGiveEachJobItsLength(t *testing.T) {
	for _, c := range []struct{ start, end, want string }{
		{"2026-06", "present", "2026-06 to present (6 months)"},
		{"2021-01", "2023-03", "2021-01 to 2023-03 (2 years 3 months)"},
		{"", "2023-03", "ended 2023-03"},
	} {
		var p workspace.Profile
		p.History.Employment = []workspace.Employment{{Employer: "Example Co", Start: c.start, End: c.end}}
		if got := profileFacts(p, factsToday)["experience 1"]; got != "Example Co, "+c.want {
			t.Errorf("%s to %s: %q", c.start, c.end, got)
		}
	}
}

// An education entry reaches the review with its grade average: with none the
// review flagged the profile's own GPA as unsupported (Adobe).
func TestProfileFactsGiveTheGradeAverage(t *testing.T) {
	var p workspace.Profile
	p.History.Education = []workspace.Education{{School: "Example University", Degree: "BS", Field: "Mathematics", Start: "2019-09", End: "2023-05", GradeAverage: "3.4"}}
	if got := profileFacts(p, factsToday)["education 1"]; got != "BS, Mathematics, Example University, 2019-09 to 2023-05, GPA 3.4" {
		t.Fatalf("education 1 = %q", got)
	}
}

// An open relocation default accepts any place not excluded, its listed places
// only preferred: one line says so, or the review reads a separate list as the
// only places.
func TestProfileFactsSayTheRelocationRule(t *testing.T) {
	var p workspace.Profile
	p.Preferences.Relocation = workspace.Relocation{Default: "open", Places: []string{"New York", "Chicago"}, Excluded: []string{"London"}}
	f := profileFacts(p, factsToday)
	if got := f["relocation"]; got != "open: willing to relocate to any place not excluded; preferred: New York, Chicago" || f["relocation places"] != "" {
		t.Fatalf("facts = %v", f)
	}
	p.Preferences.Relocation.Default = "closed"
	if got := profileFacts(p, factsToday)["relocation"]; got != "closed: willing to relocate only to New York, Chicago" {
		t.Fatalf("relocation = %q", got)
	}
}

// The check sees the first day the applicant can start and the notice behind
// it (RobCo: a start a month out read as contradicted by the current job), a
// blank or past date as thirty days from today, under keys a model may see.
func TestAvailabilityFactsGiveTheStartAndTheNotice(t *testing.T) {
	for _, c := range []struct{ earliest, notice, start string }{
		{"", "1 month", "2026-12-08"},
		{"2026-01-05", "", "2026-12-08"},
		{"2027-02-01", "2 weeks", "2027-02-01"},
	} {
		f := map[string]string{}
		availabilityFacts(f, c.earliest, c.notice, factsToday)
		if f["availability earliest start"] != c.start || f["availability notice period"] != c.notice {
			t.Errorf("%q, %q: facts = %v", c.earliest, c.notice, f)
		}
		for k := range f {
			if !gates.LadderFactAllowed(k) {
				t.Errorf("%q is withheld from a model", k)
			}
		}
	}
	availabilityFacts(nil, "", "1 month", factsToday)
}
