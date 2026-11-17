package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"applysmith/internal/agent"
	"applysmith/internal/agent/approvals"
	"applysmith/internal/agent/memory"
	chatsessions "applysmith/internal/agent/sessions"
	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/gates"
)

// heldCard holds a call the way the guard chain does and returns its card id.
func heldCard(t *testing.T, h *harness, tool, args string, tier int, channel agent.Channel) string {
	t.Helper()
	return heldCardFor(t, h, tool, args, tier, channel, "how are things")
}

func heldCardFor(t *testing.T, h *harness, tool, args string, tier int, channel agent.Channel, message string) string {
	t.Helper()
	d, err := h.srv.dom()
	if err != nil {
		t.Fatal(err)
	}
	sess, err := d.sessions.Create(context.Background(), chatsessions.Dashboard, "")
	if err != nil {
		t.Fatal(err)
	}
	turn := &agent.Turn{ID: "turn_" + sess.ID, SessionID: sess.ID, Channel: channel, Origin: agent.OriginModel, UserMessage: message}
	dec := agent.Decision{Verdict: gates.Hold, Gate: agent.GateConfirmation, Code: agent.CodeIntentNotStated, Reason: "no intent", Tier: tier}
	id := d.confirmations.Hold(context.Background(), turn, agent.Tool{Name: tool, Label: "Saving a fact"}, "call_1", json.RawMessage(args), dec)
	if id == "" {
		t.Fatal("no card was recorded")
	}
	return id
}

func decode[T any](t *testing.T, r result) T {
	t.Helper()
	var env struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(r.body, &env); err != nil {
		t.Fatalf("%v: %s", err, r.body)
	}
	return env.Data
}

// The two routes the specification marks remote-allowed are served on the
// tunnel listener (TestRemoteGoldenMatchesTheRouteTable).
func TestApprovalsAPI_RouteTableSplitsSessionOnlyFromTheRest(t *testing.T) {
	want := map[string]struct {
		session bool
		remote  Remote
		access  Access
	}{
		"GET /api/agent/confirmations":                        {false, RemoteDenied, AccessStandard},
		"POST /api/agent/confirmations/{id}/approve":          {false, RemoteAllowed, AccessStandard},
		"POST /api/agent/confirmations/{id}/approve-standing": {true, RemoteDenied, AccessStandard},
		"POST /api/agent/confirmations/{id}/decline":          {false, RemoteDenied, AccessStandard},
		"GET /api/agent/approvals":                            {false, RemoteDenied, AccessStandard},
		"DELETE /api/agent/approvals/{id}":                    {false, RemoteAllowed, AccessStandard},
	}
	seen := 0
	for _, r := range Routes() {
		w, ok := want[r.Pattern()]
		if !ok {
			continue
		}
		seen++
		if r.SessionOnly != w.session || r.Remote != w.remote || r.Access != w.access || r.Limit != LimitGeneral {
			t.Errorf("%s = %+v", r.Pattern(), r)
		}
	}
	if seen != len(want) {
		t.Errorf("found %d of %d routes", seen, len(want))
	}
}

func TestApprovalsAPI_StandingScopeNeedsTheDashboardSessionAndCreatesOneApproval(t *testing.T) {
	h := newDomainHarness(t)
	cookie := h.session()
	id := heldCard(t, h, "save_fact", `{"body":"likes remote work"}`, 2, agent.ChannelDashboard)

	// The token (a CLI or a remote client) cannot widen.
	r := h.send("POST", "/api/agent/confirmations/"+id+"/approve-standing", map[string]string{"scope": "today"})
	if r.StatusCode != 403 || r.errReason() != wire.ReasonSessionOnly {
		t.Fatalf("token approve-standing: %d %s", r.StatusCode, r.body)
	}
	if got := decode[wire.StandingApprovalList](t, h.get("/api/agent/approvals")); len(got.Approvals) != 0 {
		t.Fatalf("a refused request created an approval: %+v", got)
	}

	list := decode[wire.ConfirmationList](t, h.chat(cookie, "GET", "/api/agent/confirmations", nil))
	if len(list.Cards) != 1 || list.Cards[0].ID != id || len(list.Cards[0].Scopes) != 3 || list.Cards[0].Purpose != "how are things" {
		t.Fatalf("list = %+v", list)
	}
	if r := h.chat(cookie, "POST", "/api/agent/confirmations/"+id+"/approve-standing", map[string]string{"scope": "once"}); r.StatusCode != 422 {
		t.Fatalf("scope once through approve-standing: %d %s", r.StatusCode, r.body)
	}
	r = h.chat(cookie, "POST", "/api/agent/confirmations/"+id+"/approve-standing", map[string]string{"scope": "today"})
	if r.StatusCode != 200 {
		t.Fatalf("approve-standing: %d %s", r.StatusCode, r.body)
	}
	out := decode[wire.ConfirmationOutcome](t, r)
	if out.Card.State != "approved" || out.Approval == nil || out.Approval.Tool != "save_fact" || out.Approval.Scope != "today" {
		t.Fatalf("outcome = %+v", out)
	}
	if r := h.chat(cookie, "POST", "/api/agent/confirmations/"+id+"/approve-standing", map[string]string{"scope": "today"}); r.StatusCode != 409 {
		t.Fatalf("approving a handled card: %d %s", r.StatusCode, r.body)
	}

	approvalsNow := decode[wire.StandingApprovalList](t, h.get("/api/agent/approvals"))
	if len(approvalsNow.Approvals) != 1 {
		t.Fatalf("approvals = %+v", approvalsNow)
	}
	aid := approvalsNow.Approvals[0].ID
	if r := h.send("DELETE", "/api/agent/approvals/"+aid, nil); r.StatusCode != 200 {
		t.Fatalf("revoke: %d %s", r.StatusCode, r.body)
	}
	if got := decode[wire.StandingApprovalList](t, h.get("/api/agent/approvals")); len(got.Approvals) != 0 {
		t.Fatalf("after the revoke: %+v", got)
	}
	if r := h.send("DELETE", "/api/agent/approvals/"+aid, nil); r.StatusCode != 409 {
		t.Fatalf("second revoke: %d", r.StatusCode)
	}
	if r := h.send("DELETE", "/api/agent/approvals/grant_none", nil); r.StatusCode != 404 {
		t.Fatalf("unknown revoke: %d", r.StatusCode)
	}
}

func TestApprovalsAPI_AOneTimeApprovalRunsTheCallOnceAndAHandledCardAnswersConflict(t *testing.T) {
	h := newDomainHarness(t)
	id := heldCard(t, h, "save_fact", `{"body":"prefers remote roles"}`, 2, agent.ChannelDashboard)
	r := h.post("/api/agent/confirmations/"+id+"/approve", nil)
	if r.StatusCode != 200 {
		t.Fatalf("approve: %d %s", r.StatusCode, r.body)
	}
	out := decode[wire.ConfirmationOutcome](t, r)
	if out.Approval != nil || !strings.Contains(out.Result, "Saved") {
		t.Fatalf("outcome = %+v", out)
	}
	if r := h.post("/api/agent/confirmations/"+id+"/approve", nil); r.StatusCode != 409 {
		t.Fatalf("second approval: %d %s", r.StatusCode, r.body)
	}
	if r := h.post("/api/agent/confirmations/"+id+"/decline", nil); r.StatusCode != 409 {
		t.Fatalf("decline after approval: %d", r.StatusCode)
	}
	if r := h.post("/api/agent/confirmations/conf_none/approve", nil); r.StatusCode != 404 {
		t.Fatalf("unknown card: %d", r.StatusCode)
	}
	d, _ := h.srv.dom()
	facts, err := d.memory.Facts(context.Background(), memory.Trusted)
	if err != nil || len(facts) != 1 || !strings.Contains(facts[0].Body, "prefers remote roles") {
		t.Fatalf("the approved call did not run exactly once: %+v %v", facts, err)
	}
}

func TestApprovalsAPI_ADeclinedCardNeverRuns(t *testing.T) {
	h := newDomainHarness(t)
	id := heldCard(t, h, "save_fact", `{"body":"never stored"}`, 2, agent.ChannelDashboard)
	if r := h.post("/api/agent/confirmations/"+id+"/decline", nil); r.StatusCode != 200 {
		t.Fatalf("decline: %d %s", r.StatusCode, r.body)
	}
	d, _ := h.srv.dom()
	if facts, err := d.memory.Facts(context.Background(), memory.Trusted); err != nil || len(facts) != 0 {
		t.Fatalf("a declined call ran: %+v %v", facts, err)
	}
	if got := decode[wire.ConfirmationList](t, h.get("/api/agent/confirmations")); len(got.Cards) != 0 {
		t.Fatalf("the declined card is still listed: %+v", got)
	}
}

func TestApprovalsAPI_AHeldSubmitIsConfirmedByTheDashboardSessionAlone(t *testing.T) {
	h := newDomainHarness(t)
	cookie := h.session()
	id := heldCardFor(t, h, "autofill_application", `{"url":"https://jobs.example.test/a/1","submit":true}`, 4, agent.ChannelDashboard, "apply to that job")

	// A remote device and a local token are refused, whatever the route says.
	for _, kind := range []apikit.PrincipalKind{apikit.PrincipalRemoteDevice, apikit.PrincipalLocalToken} {
		req := httptestRequest(t)
		req.SetPathValue("id", id)
		_, err := h.srv.approveConfirmation(&apikit.Call{P: apikit.Principal{Kind: kind, ID: string(kind)}, R: req})
		var e *apikit.Error
		if !errors.As(err, &e) || e.Code != wire.CodeForbidden {
			t.Fatalf("%s: %v, want forbidden", kind, err)
		}
	}
	if r := h.post("/api/agent/confirmations/"+id+"/approve", nil); r.StatusCode != 403 {
		t.Fatalf("token approve of a submit: %d %s", r.StatusCode, r.body)
	}
	card, _ := approvals.New(h.srv.store.Load(), nil, nil, nil).Get(context.Background(), id)
	if card.State != approvals.Pending {
		t.Fatalf("a refused approval consumed the card: %s", card.State)
	}
	// The dashboard session confirms and the run starts; this harness has no
	// browser, so the run stops at preflight and nothing is submitted.
	r := h.chat(cookie, "POST", "/api/agent/confirmations/"+id+"/approve", nil)
	if r.StatusCode != 200 {
		t.Fatalf("session approve: %d %s", r.StatusCode, r.body)
	}
	if out := decode[wire.ConfirmationOutcome](t, r); out.Card.State != "approved" || !strings.Contains(out.Result, "Started to fill the application") {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestApprovalsAPI_ARemoteDeviceMayApproveOnceButNotWiden(t *testing.T) {
	h := newDomainHarness(t)
	id := heldCard(t, h, "save_fact", `{"body":"a remote fact"}`, 2, agent.ChannelDashboard)
	req := httptestRequest(t)
	req.SetPathValue("id", id)
	remote := apikit.Principal{Kind: apikit.PrincipalRemoteDevice, ID: "device"}
	d, _ := h.srv.dom()
	if _, err := d.confirmations.Approve(context.Background(), id, approvals.ActorRemoteDevice, approvals.Today); err == nil {
		t.Fatal("a remote device widened an approval")
	}
	if _, err := h.srv.approveConfirmation(&apikit.Call{P: remote, R: req}); err != nil {
		t.Fatalf("one-time approval from a remote device: %v", err)
	}
}

func TestApprovalsAPI_TelegramApprovesOnceAndOnlyItsOwnCards(t *testing.T) {
	h := newDomainHarness(t)
	b := telegramBackend{s: h.srv}
	ctx := context.Background()
	d, _ := h.srv.dom()

	own := heldCard(t, h, "save_fact", `{"body":"from telegram"}`, 2, agent.ChannelTelegram)
	msg, err := b.Approve(ctx, own, true)
	if err != nil || !strings.Contains(msg, "Approved once") {
		t.Fatalf("approve own card: %q %v", msg, err)
	}
	if facts, _ := d.memory.Facts(ctx, memory.Trusted); len(facts) != 1 {
		t.Fatalf("the approved call did not run once: %+v", facts)
	}
	if act, _ := d.confirmations.Active(ctx); len(act) != 0 {
		t.Fatalf("a Telegram tap created a standing approval: %+v", act)
	}
	if _, err := b.Approve(ctx, own, true); err == nil || !strings.Contains(err.Error(), "already handled") {
		t.Fatalf("second tap: %v", err)
	}

	dash := heldCard(t, h, "save_fact", `{"body":"from the dashboard"}`, 2, agent.ChannelDashboard)
	if _, err := b.Approve(ctx, dash, true); err == nil || !strings.Contains(err.Error(), "dashboard") {
		t.Fatalf("telegram approved a dashboard card: %v", err)
	}
	submit := heldCardFor(t, h, "autofill_application", `{"url":"https://jobs.example.test/a","submit":true}`, 4, agent.ChannelTelegram, "apply")
	if _, err := b.Approve(ctx, submit, true); err == nil || !strings.Contains(err.Error(), "dashboard") {
		t.Fatalf("telegram confirmed a submit: %v", err)
	}
	msg, err = b.Approve(ctx, submit, false)
	if err != nil || !strings.Contains(msg, "Declined") {
		t.Fatalf("decline: %q %v", msg, err)
	}
	if facts, _ := d.memory.Facts(ctx, memory.Trusted); len(facts) != 1 {
		t.Fatalf("a refused tap ran a call: %+v", facts)
	}
}
