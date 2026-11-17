package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/agent"
	"applysmith/internal/agent/origins"
	chatsessions "applysmith/internal/agent/sessions"
	"applysmith/internal/api/applyapi"
	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/gates"
	"applysmith/internal/notify"
)

// gatedWindow is an apply window whose attachment waits for the test, so a
// resumed run stays working while the test presses Done again.
type gatedWindow struct{ release chan struct{} }

func (g gatedWindow) fn(c *Config) {
	c.ApplyWindow = func(ctx context.Context) (apply.Window, error) {
		select {
		case <-g.release:
		case <-ctx.Done():
		}
		return nil, errors.New("the test browser is down")
	}
}

// waitingRun puts an application run of the Ask Agent on the board, waiting for
// the user, with the conversation and request it came from recorded.
func waitingRun(t *testing.T, h *harness, id, reason, tab string, submit bool) {
	t.Helper()
	d := h.domainOf()
	sess, err := d.sessions.Create(t.Context(), chatsessions.Dashboard, "Apply")
	if err != nil {
		t.Fatal(err)
	}
	req := origins.ApplyRequest{RunID: id, TurnID: "turn_1", Caller: string(gates.CallerAskAgent), Tool: gates.ToolAutofillApplication, Submit: submit, Explicit: true,
		UserMessage: "apply to https://jobs.example.test/p/1", URL: "https://jobs.example.test/p/1", Company: "Example Corp", Role: "Engineer"}
	if err := d.origins.RecordApply(t.Context(), sess.ID, req); err != nil {
		t.Fatal(err)
	}
	openWaiting(t, h, id, notify.OriginAgent, reason, tab)
}

func openWaiting(t *testing.T, h *harness, id string, origin notify.Origin, reason, tab string) {
	t.Helper()
	board := h.domainOf().notify.board
	if _, err := board.Open(t.Context(), notify.OpenArgs{ID: id, Kind: notify.KindApply, Origin: origin, Title: "Apply to Example Corp", Stage: "fill", FocusTab: tab}); err != nil {
		t.Fatal(err)
	}
	if _, err := board.Move(t.Context(), id, notify.NeedsYou, notify.MoveArgs{Reason: reason, Detail: "a bot check", FocusTab: tab}); err != nil {
		t.Fatal(err)
	}
}

func entryOf(t *testing.T, h *harness, id string) notify.Entry {
	t.Helper()
	e, err := h.domainOf().notify.board.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func waitEntry(t *testing.T, h *harness, id string, cond func(notify.Entry) bool) notify.Entry {
	t.Helper()
	var e notify.Entry
	waitLong(t, "the board entry to change", func() bool {
		var err error
		e, err = h.domainOf().notify.board.Get(t.Context(), id)
		return err == nil && cond(e)
	})
	return e
}

func TestDone_RefusalsLeaveTheEntryAsItWas(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	d := h.domainOf()
	waitingRun(t, h, "run_confirm", "confirmation", "tab-1", true)
	waitingRun(t, h, "run_maybe", "may-have-submitted", "tab-2", true)
	openWaiting(t, h, "run_user", notify.OriginUser, "sign-in", "tab-3")
	openWaiting(t, h, "run_schedule", notify.OriginSchedule, "sign-in", "tab-7")
	openWaiting(t, h, "run_norequest", notify.OriginAgent, "sign-in", "tab-4")
	waitingRun(t, h, "run_blocked", "sign-in", "tab-5", false)
	if _, err := d.notify.board.Move(t.Context(), "run_blocked", notify.Blocked, notify.MoveArgs{Reason: "lockout"}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		id     string
		status int
		code   string
		words  string
	}{
		{"run_unknown", 404, "not_found", "no such run"},
		{"run_confirm", 409, "conflict", "confirmation"},
		{"run_maybe", 409, "conflict", "confirmation"},
		{"run_user", 409, "conflict", "no request is recorded"},
		{"run_schedule", 409, "conflict", "you or the assistant started"},
		{"run_blocked", 409, "conflict", "not waiting"},
		{"run_norequest", 409, "conflict", "no request is recorded"},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			var before notify.Entry
			if c.id != "run_unknown" {
				before = entryOf(t, h, c.id)
			}
			r := h.post("/api/runs/"+c.id+"/done", nil)
			if r.StatusCode != c.status || r.errCode() != c.code || !strings.Contains(string(r.body), c.words) {
				t.Fatalf("%d %s", r.StatusCode, r.body)
			}
			if c.id != "run_unknown" {
				if after := entryOf(t, h, c.id); after.State != before.State || after.Revision != before.Revision {
					t.Fatalf("the entry changed: %+v", after)
				}
			}
		})
	}
	if d.apply.driving() {
		t.Fatal("a refused Done left the apply window reserved")
	}
}

func TestDone_AResumeMovesTheEntryToWorkingAndRecordsWhoPressedAndWhen(t *testing.T) {
	gate := gatedWindow{release: make(chan struct{})}
	h := newDomainHarness(t, gate.fn)
	waitingRun(t, h, "run_1", "sign-in", "tab-1", false)
	r := h.post("/api/runs/run_1/done", nil)
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	var env wire.Success[wire.BoardEntry]
	strictInto(t, r.body, &env)
	if env.Data.State != string(notify.Working) || env.Data.Reason != "" || env.Data.CanDone {
		t.Fatalf("the answer = %+v", env.Data)
	}
	req, _, err := h.domainOf().origins.Request(t.Context(), "run_1")
	if err != nil || req.PressedBy != applyapi.DoneFromDashboard || req.PressedAt.IsZero() {
		t.Fatalf("press = %+v %v", req, err)
	}

	// While the resumed run works, a second press loses.
	for _, p := range []struct {
		name string
		do   func() (int, string)
	}{
		{"the dashboard again", func() (int, string) { r := h.post("/api/runs/run_1/done", nil); return r.StatusCode, string(r.body) }},
	} {
		t.Run(p.name, func(t *testing.T) {
			status, body := p.do()
			if status != 409 || !strings.Contains(body, "already going on") && !strings.Contains(body, "lane") {
				t.Fatalf("%d %s", status, body)
			}
		})
	}
	close(gate.release)
	waitEntry(t, h, "run_1", func(e notify.Entry) bool { return e.State != notify.Working })
}

func TestDone_ATabThatIsGoneBlocksTheEntryAsLaneUnavailable(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	waitingRun(t, h, "run_1", "challenge", "", false)
	r := h.post("/api/runs/run_1/done", nil)
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	e := entryOf(t, h, "run_1")
	if e.State != notify.Blocked || e.Reason != "lane-unavailable" {
		t.Fatalf("entry = %+v", e)
	}
	if h.domainOf().apply.driving() {
		t.Fatal("the apply window stayed reserved")
	}
}

func TestDone_WhileAnotherRunDrivesTheWindowTheEntryStaysWaiting(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	waitingRun(t, h, "run_1", "sign-in", "tab-1", false)
	a := h.domainOf().apply
	a.busy.Store(true)
	r := h.post("/api/runs/run_1/done", nil)
	a.busy.Store(false)
	if r.StatusCode != 409 || r.errReason() != wire.ReasonLaneBusy {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if e := entryOf(t, h, "run_1"); e.State != notify.NeedsYou {
		t.Fatalf("entry = %+v", e)
	}
}

func TestDone_ADashboardPressResumesFromTheOriginalRequest(t *testing.T) {
	req := origins.ApplyRequest{Caller: string(gates.CallerAskAgent), Tool: gates.ToolAutofillApplication, Submit: true, Explicit: true, URL: "https://jobs.example.test/p/1"}
	org := origins.Origin{SessionID: "chat_1"}
	for _, c := range []struct {
		by     string
		submit bool
		hold   bool
	}{
		{applyapi.DoneFromDashboard, true, false},
		{applyapi.DoneFromDashboard, false, false},
	} {
		r := req
		r.Submit = c.submit
		in := resumeInput(r, org, "run_1", c.by)
		if (in.HoldSubmit != "") != c.hold {
			t.Errorf("by %s with submit %v: hold %q", c.by, c.submit, in.HoldSubmit)
		}
		// Done is not an input to the run's own gates: its request is the original.
		if in.Input.Caller != gates.CallerAskAgent || in.Input.Submit != c.submit || !in.Input.ExplicitRequest || in.Input.ConversationID != "chat_1" {
			t.Errorf("input = %+v", in.Input)
		}
	}
}

func TestDone_ARunTheUserStartedIsRebuiltFromItsRequest(t *testing.T) {
	q := apply.StartRequest{RunID: "run_1", Caller: gates.CallerDashboard, Tool: gates.ToolAutofillApplication, Explicit: true,
		URL: "https://jobs.example.test/p/1?gh_jid=7", Company: "Example Corp", Role: "Engineer"}
	for _, c := range []struct {
		by           string
		submit, hold bool
	}{{applyapi.DoneFromDashboard, true, false}, {applyapi.DoneFromDashboard, false, false}} {
		r := q
		r.Submit = c.submit
		if in := startInput(r, c.by); (in.HoldSubmit != "") != c.hold || in.Input.Submit != c.submit {
			t.Errorf("by %s with submit %v: %+v", c.by, c.submit, in)
		}
	}
}

// The Ask Agent's last answer continues a submitting run only as far as
// Submit, whoever started the run, because the agent read the posting's
// questions (AC-4).
func TestDone_TheAskAgentsLastAnswerHoldsTheSubmitOfTheRunItContinues(t *testing.T) {
	byAgent := origins.ApplyRequest{Caller: string(gates.CallerAskAgent), Tool: gates.ToolAutofillApplication, Explicit: true, URL: "https://jobs.example.test/p/1"}
	byUser := apply.StartRequest{RunID: "run_1", Caller: gates.CallerDashboard, Tool: gates.ToolAutofillApplication, Explicit: true, URL: "https://jobs.example.test/p/1"}
	for _, submit := range []bool{true, false} {
		byAgent.Submit, byUser.Submit = submit, submit
		if in := resumeInput(byAgent, origins.Origin{SessionID: "chat_1"}, "run_1", applyapi.DoneFromAgent); (in.HoldSubmit != "") != submit || in.Input.Submit != submit {
			t.Errorf("a run the agent started, submit %v: %+v", submit, in)
		}
		if in := startInput(byUser, applyapi.DoneFromAgent); (in.HoldSubmit != "") != submit || in.Input.Submit != submit {
			t.Errorf("a run the user started, submit %v: %+v", submit, in)
		}
	}
}

// The production writer is a start of the user: the request it records is the
// one a Done on its stop reads.
func TestDone_ARunTheUserStartedRecordsItsRequestAndGoesOnFromIt(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	d := h.domainOf()
	const posting = "https://jobs.example.test/p/1?gh_jid=7"
	r := h.post("/api/apply/runs", wire.ApplyRunRequest{URL: posting, Company: "Example Corp", Role: "Engineer"})
	if r.StatusCode != 202 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	id := decode[wire.RunAccepted](t, r).RunID
	waitEntry(t, h, id, func(e notify.Entry) bool { return e.State == notify.Done })
	waitLong(t, "the window to be free", func() bool { return !d.apply.driving() })
	if q, err := apply.LoadRequest(t.Context(), h.srv.store.Load(), id); err != nil || q.Submit || !q.Explicit || q.URL != posting {
		t.Fatalf("request = %+v %v", q, err)
	}
	// The run stops for the user with its tab kept, and Done goes on from it.
	if _, err := d.notify.board.Move(t.Context(), id, notify.Working, notify.MoveArgs{}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.notify.board.Move(t.Context(), id, notify.NeedsYou, notify.MoveArgs{Reason: "sign-in", Detail: "sign in", FocusTab: "tab-1"}); err != nil {
		t.Fatal(err)
	}
	if r := h.post("/api/runs/"+id+"/done", nil); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	e := waitEntry(t, h, id, func(e notify.Entry) bool { return e.State != notify.Working })
	if e.State != notify.Done || e.Outcome != notify.OutcomeFailed || !strings.Contains(e.Detail, "the test browser is down") {
		t.Fatalf("entry = %+v", e)
	}
	v := decode[wire.ApplyRunView](t, h.get("/api/apply/runs/"+id))
	if n := len(slices.DeleteFunc(slices.Clone(v.Stages), func(s wire.ApplyStage) bool { return s.Stage != string(apply.StagePreflight) })); n != 2 {
		t.Fatalf("%d preflight stages: the resumed leg continues the run's own record\n%+v", n, v.Stages)
	}
}

func TestDone_AResumedRunDecidesItsSubmitGateAgain(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	a := h.domainOf().apply
	plain := origins.ApplyRequest{Caller: string(gates.CallerAskAgent), Tool: gates.ToolAutofillApplication, Explicit: true, URL: "https://jobs.example.test/p/1"}
	if a.recheck(plain) != nil {
		t.Fatal("a run that does not submit has a submit gate to decide")
	}
	named := plain
	named.Submit, named.UserMessage = true, "apply to https://jobs.example.test/p/1"
	if d := a.recheck(named)(t.Context()); d.Verdict != gates.Allow {
		t.Fatalf("a named posting: %+v", d)
	}
	unnamed := plain
	unnamed.Submit, unnamed.UserMessage = true, "apply for me"
	if d := a.recheck(unnamed)(t.Context()); d.Verdict != gates.Hold {
		t.Fatalf("a posting nobody named: %+v", d)
	}
	tainted := named
	tainted.Outside, tainted.UserMessage = true, "apply for me"
	if d := a.recheck(tainted)(t.Context()); d.Verdict != gates.Hold {
		t.Fatalf("a turn that read outside content: %+v", d)
	}
}

func TestDone_TheBoardEntryOffersDoneOnlyWhereItApplies(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	waitingRun(t, h, "run_ok", "sign-in", "tab-1", false)
	waitingRun(t, h, "run_confirm", "confirmation", "tab-2", true)
	openWaiting(t, h, "run_user", notify.OriginUser, "sign-in", "tab-3")
	openWaiting(t, h, "run_user_filled", notify.OriginUser, "confirmation", "tab-4")
	for id, want := range map[string]bool{"run_ok": true, "run_confirm": false, "run_user": true, "run_user_filled": false} {
		r := h.get("/api/runs/" + id)
		var env wire.Success[wire.RunView]
		strictInto(t, r.body, &env)
		if env.Data.Entry == nil || env.Data.Entry.CanDone != want {
			t.Errorf("%s: canDone = %+v, want %v", id, env.Data.Entry, want)
		}
	}
}

// ---- "Submit this form" on a filled form that waits in its kept tab

// filledWaiting records the start request of a run that filled its form, and
// puts it on the board waiting at Submit for the user's confirmation.
func filledWaiting(t *testing.T, h *harness, id, tab string, origin notify.Origin) {
	t.Helper()
	q := apply.StartRequest{RunID: id, Caller: gates.CallerDashboard, Tool: gates.ToolAutofillApplication, Explicit: true,
		URL: "https://jobs.example.test/p/1?gh_jid=7", Company: "Example Corp", Role: "Engineer"}
	if err := apply.SaveRequest(t.Context(), h.srv.store.Load(), q, time.Now()); err != nil {
		t.Fatal(err)
	}
	openWaiting(t, h, id, origin, "confirmation", tab)
}

func TestSubmitThisForm_GoesOnInTheKeptTabWithTheConfirmedSubmit(t *testing.T) {
	gate := gatedWindow{release: make(chan struct{})}
	h := newDomainHarness(t, gate.fn)
	filledWaiting(t, h, "run_1", "tab-1", notify.OriginUser)
	if e := decode[wire.RunView](t, h.get("/api/runs/run_1")).Entry; e == nil || !e.CanSubmit {
		t.Fatalf("entry = %+v", e)
	}
	waiting := entryOf(t, h, "run_1")
	if r := h.post("/api/apply/runs/run_1/submit", wire.ApplySubmitRequest{}); r.StatusCode != 422 || r.errField() != "confirmed" {
		t.Fatalf("an unconfirmed press: %d %s", r.StatusCode, r.body)
	}
	if e := entryOf(t, h, "run_1"); e.Revision != waiting.Revision {
		t.Fatalf("an unconfirmed press moved the entry: %+v", e)
	}
	r := h.post("/api/apply/runs/run_1/submit", wire.ApplySubmitRequest{Confirmed: true})
	if r.StatusCode != 202 || decode[wire.RunAccepted](t, r).RunID != "run_1" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if e := entryOf(t, h, "run_1"); e.State != notify.Working || e.Reason != "" {
		t.Fatalf("entry = %+v", e)
	}
	// A later Done on the run decides from the confirmed submit.
	if q, err := apply.LoadRequest(t.Context(), h.srv.store.Load(), "run_1"); err != nil || !q.Submit || !q.Explicit {
		t.Fatalf("request = %+v %v", q, err)
	}
	close(gate.release)
	waitEntry(t, h, "run_1", func(e notify.Entry) bool { return e.State != notify.Working })
}

func TestSubmitThisForm_RefusalsLeaveTheEntryAsItWas(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	a := h.domainOf().apply
	openWaiting(t, h, "run_agent_norequest", notify.OriginAgent, "confirmation", "tab-1")
	waitingRun(t, h, "run_agent_token", "confirmation", "tab-3", true)
	filledWaiting(t, h, "run_busy", "tab-2", notify.OriginUser)
	openWaiting(t, h, "run_norequest", notify.OriginUser, "confirmation", "tab-4")
	cookie := h.session()
	for _, c := range []struct {
		id      string
		session bool
		status  int
		words   string
		busy    bool
	}{
		{"run_agent_norequest", true, 409, "no request is recorded", false},
		{"run_agent_token", false, 403, "from the dashboard, not with a local token", false},
		{"run_norequest", false, 409, "no request is recorded", false},
		{"run_busy", false, 409, "already working in the apply window", true},
	} {
		t.Run(c.id, func(t *testing.T) {
			before := entryOf(t, h, c.id)
			a.busy.Store(c.busy)
			path, body := "/api/apply/runs/"+c.id+"/submit", wire.ApplySubmitRequest{Confirmed: true}
			var r result
			if c.session {
				r = h.chat(cookie, "POST", path, body)
			} else {
				r = h.post(path, body)
			}
			a.busy.Store(false)
			if r.StatusCode != c.status || !strings.Contains(string(r.body), c.words) {
				t.Fatalf("%d %s", r.StatusCode, r.body)
			}
			if req, _, err := h.domainOf().origins.Request(t.Context(), c.id); err == nil && req.Confirmed {
				t.Fatal("a refused press confirmed the agent's submit")
			}
			if after := entryOf(t, h, c.id); after.State != before.State || after.Revision != before.Revision {
				t.Fatalf("the entry changed: %+v", after)
			}
			if q, err := apply.LoadRequest(t.Context(), h.srv.store.Load(), c.id); err == nil && q.Submit {
				t.Fatal("a refused press recorded a submit")
			}
		})
	}
}

// heldRun puts on the board a run of a posting whose filled form waits at
// Submit in its kept tab: its record, the request it came from and its waiting
// entry. A run of the Ask Agent comes from a conversation of its own.
func heldRun(t *testing.T, h *harness, id string, origin notify.Origin) (posting, key string) {
	t.Helper()
	posting = "https://jobs.example.test/p/1"
	key, stp := apply.IdentityKey(posting)
	if stp != nil {
		t.Fatal(stp)
	}
	err := h.srv.store.Load().Write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO apply_runs (run_id, started_at, finished_at, caller, posting_url, identity_key, outcome_kind) VALUES
			(?, '2027-10-12T15:04:05.000Z', '2027-10-12T15:09:05.000Z', 'dashboard', ?, ?, 'filled')`, id, posting, key)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if origin == notify.OriginAgent {
		waitingRun(t, h, id, "confirmation", "tab-1", true)
	} else {
		filledWaiting(t, h, id, "tab-1", origin)
	}
	return posting, key
}

func runsOf(t *testing.T, h *harness, key string) int {
	t.Helper()
	var n int
	if err := h.srv.store.Load().Read().QueryRow(`SELECT count(*) FROM apply_runs WHERE identity_key = ?`, key).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// submitFromConversation is the Ask Agent's call to submit the posting, as its
// tool makes it once the card that held it is approved.
func submitFromConversation(t *testing.T, h *harness, session, posting string) (string, error) {
	return h.domainOf().apply.StartApply(t.Context(), agent.ApplyRequest{Tool: gates.ToolAutofillApplication, Rows: []agent.ApplyRow{{URL: posting}},
		Submit: true, ExplicitRequest: true, Caller: gates.CallerAskAgent, ConversationID: session, TurnID: "turn_2",
		UserMessage: "submit it", OwnWords: "submit it", Outside: true, Confirmed: true})
}

// AC-6: the filled form of a run the Ask Agent started, waiting at Submit in
// its kept tab, is submitted from the board as a run the user started is, and
// "Submit this form" and the run's conversation settle that one hold: the first
// goes on in the kept tab and records the confirmed submit for a later Done;
// the second is refused and starts no run.
func TestSubmitThisForm_TheAgentsFilledFormIsSettledOnceFromTheBoardOrItsConversation(t *testing.T) {
	board := func(t *testing.T, h *harness, _ string) error {
		if r := h.chat(h.session(), "POST", "/api/apply/runs/run_1/submit", wire.ApplySubmitRequest{Confirmed: true}); r.StatusCode != 202 {
			return fmt.Errorf("%d %s", r.StatusCode, r.body)
		}
		return nil
	}
	conversation := func(t *testing.T, h *harness, posting string) error {
		_, err := submitFromConversation(t, h, sessionOfRun(t, h, "run_1"), posting)
		return err
	}
	for _, c := range []struct {
		name          string
		first, second func(*testing.T, *harness, string) error
	}{{"the board first", board, conversation}, {"the conversation first", conversation, board}} {
		t.Run(c.name, func(t *testing.T) {
			gate := gatedWindow{release: make(chan struct{})}
			h := newDomainHarness(t, gate.fn)
			// Before the harness stops: a stale tab is closed on the gated window.
			release := sync.OnceFunc(func() { close(gate.release) })
			t.Cleanup(release)
			posting, key := heldRun(t, h, "run_1", notify.OriginAgent)
			if err := c.first(t, h, posting); err != nil {
				t.Fatal(err)
			}
			if e := entryOf(t, h, "run_1"); e.State != notify.Working {
				t.Fatalf("the first confirmation left %+v", e)
			}
			if err := c.second(t, h, posting); err == nil {
				t.Fatal("the second confirmation of the settled form went on")
			}
			if n := runsOf(t, h, key); n != 1 {
				t.Fatalf("%d runs of the posting", n)
			}
			if req, _, err := h.domainOf().origins.Request(t.Context(), "run_1"); err != nil || !req.Submit || !req.Explicit || !req.Confirmed {
				t.Fatalf("a later Done decides from %+v, %v", req, err)
			}
			release()
			waitEntry(t, h, "run_1", func(e notify.Entry) bool { return e.State != notify.Working })
		})
	}
}

// Only the conversation that started a run confirms its form from there, and
// only while that form waits at Submit, so the one told how the run goes is the
// one that asked. Any other start fills the posting again as a new run, which
// retires a form that still waits (F-3).
func TestSubmitThisForm_OnlyTheRunsConversationConfirmsItsWaitingFormFromThere(t *testing.T) {
	retry := func(t *testing.T, h *harness, _ string) (string, error) {
		r := h.post("/api/apply/runs/run_1/retry", wire.ApplyRetryRequest{Submit: true, Confirmed: true})
		if r.StatusCode != 202 {
			return "", fmt.Errorf("%d %s", r.StatusCode, r.body)
		}
		return decode[wire.RunAccepted](t, r).RunID, nil
	}
	another := func(t *testing.T, h *harness, posting string) (string, error) {
		sess, err := h.domainOf().sessions.Create(t.Context(), chatsessions.Dashboard, "Apply")
		if err != nil {
			t.Fatal(err)
		}
		return submitFromConversation(t, h, sess.ID, posting)
	}
	own := func(t *testing.T, h *harness, posting string) (string, error) {
		return submitFromConversation(t, h, sessionOfRun(t, h, "run_1"), posting)
	}
	for _, c := range []struct {
		name   string
		origin notify.Origin
		ended  bool
		start  func(*testing.T, *harness, string) (string, error)
	}{
		{"retry and submit on the dashboard", notify.OriginAgent, false, retry},
		{"another conversation", notify.OriginAgent, false, another},
		{"a form of a run the user started", notify.OriginUser, false, another},
		{"its own run that no longer waits", notify.OriginAgent, true, own},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newDomainHarness(t, unavailableWindow)
			posting, key := heldRun(t, h, "run_1", c.origin)
			if c.ended {
				if _, err := h.domainOf().notify.board.Move(t.Context(), "run_1", notify.Done, notify.MoveArgs{Outcome: notify.OutcomeFailed}); err != nil {
					t.Fatal(err)
				}
			}
			id, err := c.start(t, h, posting)
			if err != nil || id == "run_1" || runsOf(t, h, key) != 2 {
				t.Fatalf("the start went on as %q: %v", id, err)
			}
			waitLong(t, "the window to be free", func() bool { return !h.domainOf().apply.driving() })
		})
	}
}

// AC-6: the user's confirmation of an agent run's filled form is the one the
// agent's submit gate asks for, so the run goes on to the press even for a
// posting nobody named, which that gate holds on a Done.
func TestSubmitThisForm_TheUsersConfirmationLetsTheAgentsRunReachThePress(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	unnamed := origins.ApplyRequest{Caller: string(gates.CallerAskAgent), Tool: gates.ToolAutofillApplication, Submit: true, Explicit: true,
		UserMessage: "apply for me", URL: "https://jobs.example.test/p/1"}
	in := h.domainOf().apply.confirmedInput(unnamed, origins.Origin{SessionID: "chat_1"}, "run_1")
	if in.HoldSubmit != "" || !in.Input.Submit || !in.Input.ExplicitRequest || in.Recheck == nil {
		t.Fatalf("input = %+v", in)
	}
	if d := in.Recheck(t.Context()); d.Verdict != gates.Allow && d.Verdict != gates.AllowWithTrace {
		t.Fatalf("the agent's submit gate: %+v", d)
	}
}

// ---- the conversation hears how its runs go

func messagesOf(t *testing.T, h *harness, session string) []chatsessions.Message {
	t.Helper()
	ms, err := h.domainOf().sessions.Messages(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	var out []chatsessions.Message
	for _, m := range ms {
		if m.Role == chatsessions.RoleNote {
			out = append(out, m)
		}
	}
	return out
}

func sessionOfRun(t *testing.T, h *harness, runID string) string {
	t.Helper()
	o, err := h.domainOf().origins.Get(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	return o.SessionID
}

func waitNotes(t *testing.T, h *harness, session string, n int) []chatsessions.Message {
	t.Helper()
	var got []chatsessions.Message
	waitLong(t, "the conversation to be told", func() bool {
		got = messagesOf(t, h, session)
		return len(got) >= n
	})
	return got
}

// drainCompletion returns once the completion worker stored every line queued
// before it.
func drainCompletion(h *harness) {
	done := make(chan struct{})
	h.domainOf().completion.enqueue(func() { close(done) })
	<-done
}

func TestCompletion_AConversationIsToldWhenItsRunWaitsOrEnds(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	waitingRun(t, h, "run_1", "sign-in", "tab-1", false)
	chat := sessionOfRun(t, h, "run_1")
	notes := waitNotes(t, h, chat, 1)
	n := notes[0]
	if !n.SystemMade || !n.OK || !n.OutsideContent || !strings.Contains(n.Body, "needs a sign-in") || !strings.Contains(n.Body, "press Done on the Runs page") || !strings.Contains(n.Body, "a bot check") {
		t.Fatalf("note = %+v", n)
	}
	// The note names its run, so the conversation can offer to finish it.
	if n.Envelope.RunID != "run_1" {
		t.Fatalf("the note names run %q", n.Envelope.RunID)
	}

	// A run that ends with nothing from a page to say carries no outside text.
	d := h.domainOf()
	if err := d.origins.Record(t.Context(), "run_2", chat, "scan_portal"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.notify.board.Open(t.Context(), notify.OpenArgs{ID: "run_2", Kind: notify.KindScan, Origin: notify.OriginAgent, Title: "Scan builtin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.notify.board.Move(t.Context(), "run_2", notify.Done, notify.MoveArgs{Outcome: notify.OutcomeOK}); err != nil {
		t.Fatal(err)
	}
	notes = waitNotes(t, h, chat, 2)
	if got := notes[1]; !strings.Contains(got.Body, `"Scan builtin" finished`) || got.OutsideContent || !got.SystemMade {
		t.Fatalf("a finish with no page text: %+v", got)
	}
	if _, err := d.notify.board.Move(t.Context(), "run_2", notify.Working, notify.MoveArgs{}); err != nil {
		t.Fatal(err)
	}
	drainCompletion(h)
	if n := len(messagesOf(t, h, chat)); n != 2 {
		t.Fatalf("%d notes; going back to work is not an end", n)
	}
}

// The Runs board opens the conversation a run came from (agent-drives-apply
// task 2.3).
func TestRunView_NamesTheConversationThatStartedTheRun(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	waitingRun(t, h, "run_1", "sign-in", "tab-1", false)
	chat := sessionOfRun(t, h, "run_1")
	if v := decode[wire.RunView](t, h.get("/api/runs/run_1")); v.Conversation != chat {
		t.Fatalf("conversation = %q, want %q", v.Conversation, chat)
	}
	openWaiting(t, h, "run_user", notify.OriginUser, "sign-in", "tab-9")
	if v := decode[wire.RunView](t, h.get("/api/runs/run_user")); v.Conversation != "" {
		t.Fatalf("a run no conversation started names %q", v.Conversation)
	}
}

// An end the queue never told (here the origin was recorded after its run
// ended; a stop or a full queue leave the same) keeps its conversation's task
// waiting until a pass over the stored ends tells it, once.
func TestCompletion_AnEndTheQueueMissedIsToldByAPassAndCountsUntilThen(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	d := h.domainOf()
	ctx := t.Context()
	sess, err := d.sessions.Create(ctx, chatsessions.Dashboard, "Evaluate")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.notify.board.Open(ctx, notify.OpenArgs{ID: "run_late", Kind: notify.KindScan, Origin: notify.OriginAgent, Title: "Scan builtin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.notify.board.Move(ctx, "run_late", notify.Done, notify.MoveArgs{Outcome: notify.OutcomeOK}); err != nil {
		t.Fatal(err)
	}
	drainCompletion(h) // the end's line found no origin
	if err := d.origins.Record(ctx, "run_late", sess.ID, "scan_portal"); err != nil {
		t.Fatal(err)
	}
	if n, err := d.origins.ActiveIn(ctx, sess.ID); err != nil || n != 1 || len(messagesOf(t, h, sess.ID)) != 0 {
		t.Fatalf("before the pass: ActiveIn %d (%v), %d notes; want the untold end counted and no note", n, err, len(messagesOf(t, h, sess.ID)))
	}
	d.completion.tellUntold(ctx, time.Now())
	d.completion.tellUntold(ctx, time.Now())
	notes := messagesOf(t, h, sess.ID)
	if len(notes) != 1 || !strings.Contains(notes[0].Body, `"Scan builtin" finished`) {
		t.Fatalf("notes = %+v", notes)
	}
	if n, err := d.origins.ActiveIn(ctx, sess.ID); err != nil || n != 0 {
		t.Fatalf("after the pass: ActiveIn %d (%v)", n, err)
	}
}

func TestCompletion_APageReadAfterDoneIsStoredAsOutsideContent(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	waitingRun(t, h, "run_1", "sign-in", "tab-1", false)
	chat := sessionOfRun(t, h, "run_1")
	waitNotes(t, h, chat, 1)
	h.domainOf().completion.resumed("run_1", applyapi.DoneFromDashboard, apply.PageGlance{URL: "https://jobs.example.test/p/1", Title: "Apply", Controls: 4})
	notes := waitNotes(t, h, chat, 2)
	last := notes[len(notes)-1]
	if !last.OutsideContent || !last.SystemMade || !strings.Contains(last.Body, "You reported finishing the step") || !strings.Contains(last.Body, "4 form control") ||
		!strings.Contains(last.Body, "Done in dashboard") {
		t.Fatalf("note = %+v", last)
	}
}

// ---- the cap on the runs the Ask Agent has going

func TestRunGate_CountsTheRunsAConversationStartedThatAreStillGoing(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	d := h.domainOf()
	gate := h.srv.runGate(d)
	sess, err := d.sessions.Create(t.Context(), chatsessions.Dashboard, "Runs")
	if err != nil {
		t.Fatal(err)
	}
	active := func() int {
		n, err := gate.Active(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if active() != 0 {
		t.Fatal("a fresh server has runs going")
	}
	for _, id := range []string{"run_a", "run_b", "run_c"} {
		if err := d.origins.Record(t.Context(), id, sess.ID, "scan_portal"); err != nil {
			t.Fatal(err)
		}
		if _, err := d.notify.board.Open(t.Context(), notify.OpenArgs{ID: id, Kind: notify.KindScan, Origin: notify.OriginAgent, Title: id}); err != nil {
			t.Fatal(err)
		}
	}
	if active() != 3 {
		t.Fatalf("active = %d", active())
	}
	if _, err := d.notify.board.Move(t.Context(), "run_a", notify.Done, notify.MoveArgs{Outcome: notify.OutcomeOK}); err != nil {
		t.Fatal(err)
	}
	if active() != 2 {
		t.Fatalf("a finished run still counts: %d", active())
	}
}

// A queued start is stored with its conversation: the conversation's delegated
// task counts it as work going, and a start the server left waiting is told to
// the conversation at the next start, its line and row taken away.
func TestRunGate_AQueuedStartIsStoredAndASettleTellsItsConversation(t *testing.T) {
	h := newDomainHarness(t, unavailableWindow)
	d := h.domainOf()
	sess, err := d.sessions.Create(t.Context(), chatsessions.Dashboard, "Scans")
	if err != nil {
		t.Fatal(err)
	}
	gate := h.srv.runGate(d)
	if _, err := gate.Hold(t.Context(), sess.ID, "Starting a scan"); err != nil {
		t.Fatal(err)
	}
	if n, err := d.origins.ActiveIn(t.Context(), sess.ID); err != nil || n != 1 {
		t.Fatalf("ActiveIn = %d, %v: the conversation's task must wait for the queued start", n, err)
	}
	if n, err := gate.Active(t.Context()); err != nil || n != 0 {
		t.Fatalf("Active = %d, %v: a queued start does not count against the cap it waits on", n, err)
	}

	// The server stopped here; the next start settles what it left.
	if err := gate.Settle(t.Context()); err != nil {
		t.Fatal(err)
	}
	notes := waitNotes(t, h, sess.ID, 1)
	if n := notes[0]; !n.SystemMade || n.OutsideContent || n.Body != "The queued start (a scan) did not begin: the server stopped before one of my runs finished; ask again to start it." {
		t.Fatalf("note = %+v", n)
	}
	if qs, err := d.origins.QueuedStarts(t.Context()); err != nil || len(qs) != 0 {
		t.Fatalf("queued starts left: %+v, %v", qs, err)
	}
	if es, _ := d.notify.board.List(t.Context(), notify.Filter{Kinds: []notify.Kind{notify.KindAgent}, Limit: 10}); len(es) != 0 {
		t.Fatalf("the queued line stayed: %+v", es)
	}
	if n, _ := d.origins.ActiveIn(t.Context(), sess.ID); n != 0 {
		t.Fatalf("ActiveIn = %d after the settle", n)
	}
}
