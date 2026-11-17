package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"applysmith/internal/gates"
	"applysmith/internal/workspace"
)

type recordingApply struct {
	reqs []ApplyRequest
	err  error
}

func (r *recordingApply) StartApply(_ context.Context, req ApplyRequest) (string, error) {
	r.reqs = append(r.reqs, req)
	return "run_test_1", r.err
}

func applyTool(t *testing.T, d ToolDeps, name string) Tool {
	t.Helper()
	return toolOf(t, applyTools(d), name)
}

func callApply(t *testing.T, tool Tool, turn *Turn, args string) (ToolResult, error) {
	t.Helper()
	return tool.Run(context.Background(), ToolCall{CallID: "call_1", Args: json.RawMessage(args), Turn: turn})
}

func TestAutofillApplication_HandsTheChainTheTurnsFactsNotTheModelsClaim(t *testing.T) {
	cases := []struct {
		name     string
		turn     *Turn
		wantExpl bool
	}{
		{"the model chose it from a message that never asked", &Turn{SessionID: "ses_1", Channel: ChannelDashboard, Origin: OriginModel, UserMessage: "what is on my plate"}, false},
		{"the user asked to apply", &Turn{SessionID: "ses_1", Channel: ChannelDashboard, Origin: OriginModel, UserMessage: "please apply to that role"}, true},
		{"a button made the call", &Turn{SessionID: "ses_1", Channel: ChannelDashboard, Origin: OriginButton}, true},
		{"the user asked to correct a submitted application", &Turn{SessionID: "ses_1", Channel: ChannelDashboard, Origin: OriginModel,
			UserMessage: "correct the answers for Example Corp https://jobs.example.test/p/1"}, true},
		{"the correct answer is not a request", &Turn{SessionID: "ses_1", Channel: ChannelDashboard, Origin: OriginModel,
			UserMessage: "the correct answer was Yes for Example Corp https://jobs.example.test/p/1"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := &recordingApply{}
			res, err := callApply(t, applyTool(t, ToolDeps{Apply: rec}, "autofill_application"), c.turn, `{"url":" https://jobs.example.test/p/1 ","submit":true}`)
			if err != nil || res.RunID != "run_test_1" || !strings.Contains(res.Text, "run_test_1") {
				t.Fatalf("result = %+v, %v", res, err)
			}
			if len(rec.reqs) != 1 {
				t.Fatalf("%d runs started", len(rec.reqs))
			}
			got := rec.reqs[0]
			if got.Tool != gates.ToolAutofillApplication || !got.Submit || got.ExplicitRequest != c.wantExpl || got.Caller != gates.CallerAskAgent ||
				got.ConversationID != "ses_1" || len(got.Rows) != 1 || got.Rows[0].URL != "https://jobs.example.test/p/1" || got.UserMessage != c.turn.UserMessage {
				t.Fatalf("request = %+v", got)
			}
		})
	}
}

func TestAutofillApplication_ARefusedStartIsAToolError(t *testing.T) {
	rec := &recordingApply{err: errors.New("an application run is already working")}
	_, err := callApply(t, applyTool(t, ToolDeps{Apply: rec}, "autofill_application"), &Turn{Channel: ChannelDashboard}, `{"url":"https://jobs.example.test/p/1"}`)
	if err == nil || !strings.Contains(err.Error(), "already working") {
		t.Fatalf("err = %v", err)
	}
}

func TestAutofillApplication_APostingOnTheListCarriesItsCompanyAndRole(t *testing.T) {
	rec := &recordingApply{}
	d := listDeps(rec, nil)
	tool := applyTool(t, d, "autofill_application")
	turn := &Turn{Channel: ChannelDashboard, Origin: OriginRouter}
	if _, err := callApply(t, tool, turn, `{"url":"https://jobs.example.test/s/1"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := callApply(t, tool, turn, `{"url":"https://jobs.example.test/other/9"}`); err != nil {
		t.Fatal(err)
	}
	if len(rec.reqs) != 2 {
		t.Fatalf("%d runs started", len(rec.reqs))
	}
	if r := rec.reqs[0].Rows[0]; r.Company != "Sample Labs" || r.Role != "Platform" {
		t.Fatalf("listed row = %+v", r)
	}
	if r := rec.reqs[1].Rows[0]; r.Company != "" || r.Role != "" || r.URL != "https://jobs.example.test/other/9" {
		t.Fatalf("unlisted row = %+v", r)
	}
	if _, err := callApply(t, tool, turn, `{"url":"https://jobs.example.test/other/9","company":" Example Corp ","role":"Data Engineer"}`); err != nil {
		t.Fatal(err)
	}
	if r := rec.reqs[2].Rows[0]; r.Company != "Example Corp" || r.Role != "Data Engineer" {
		t.Fatalf("named row = %+v", r)
	}
}

func listDeps(rec *recordingApply, entries []string) ToolDeps {
	return ToolDeps{Apply: rec,
		ApplyLanes: func(context.Context) ([]ApplyLane, error) {
			return []ApplyLane{{Lane: "apply-now", Items: []ApplyItem{
				{Company: "Example Corp", Role: "Backend", URL: "https://jobs.example.test/a/1"},
				{Company: "Meta", Role: "Backend", URL: "https://jobs.example.test/m/1"},
				{Company: "Sample Labs", Role: "Platform", URL: "https://jobs.example.test/s/1"},
				{Company: "Example Corp", Role: "Backend", URL: "https://jobs.example.test/a/1"},
			}}}, nil
		},
		Profile: func(context.Context) (workspace.Profile, error) {
			doc := "version: 1\ncandidate:\n  full_name: Alex Example\n  email: alex@example.com\n  phone: \"+1 555 0100\"\nbatch_skip_companies: [" + strings.Join(entries, ", ") + "]\n"
			p, rep := workspace.ParseProfile([]byte(doc))
			if !rep.OK() {
				return p, errors.New("test profile does not parse")
			}
			return p, nil
		}}
}

func TestApplyFromList_LeavesTheSkipListToTheUserAndRunsTheRestOnce(t *testing.T) {
	rec := &recordingApply{}
	d := listDeps(rec, []string{"Meta"})
	res, err := callApply(t, applyTool(t, d, "apply_from_list"), &Turn{Channel: ChannelDashboard, Origin: OriginRouter}, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.reqs) != 1 || rec.reqs[0].Tool != gates.ToolApplyFromList || rec.reqs[0].Submit {
		t.Fatalf("requests = %+v", rec.reqs)
	}
	var urls []string
	for _, r := range rec.reqs[0].Rows {
		urls = append(urls, r.Company)
	}
	if strings.Join(urls, ",") != "Example Corp,Sample Labs" {
		t.Fatalf("rows = %v", urls)
	}
	if !strings.Contains(res.Text, "Left for you (skip list): Meta") {
		t.Fatalf("text = %q", res.Text)
	}
}

func TestApplyFromList_LimitAndAnEmptyList(t *testing.T) {
	rec := &recordingApply{}
	d := listDeps(rec, []string{})
	if _, err := callApply(t, applyTool(t, d, "apply_from_list"), &Turn{Channel: ChannelDashboard}, `{"limit":1}`); err != nil {
		t.Fatal(err)
	}
	if len(rec.reqs) != 1 || len(rec.reqs[0].Rows) != 1 {
		t.Fatalf("requests = %+v", rec.reqs)
	}
	all := &recordingApply{}
	res, err := callApply(t, applyTool(t, listDeps(all, []string{"Example Corp", "Meta", "Sample Labs"}), "apply_from_list"), &Turn{Channel: ChannelDashboard}, `{}`)
	if err != nil || len(all.reqs) != 0 || !strings.Contains(res.Text, "nothing on today's apply list") {
		t.Fatalf("everything skipped: %q, %v, %d runs", res.Text, err, len(all.reqs))
	}
}

func TestApplyFromList_AnUnreadableProfileStopsBeforeAnyRun(t *testing.T) {
	rec := &recordingApply{}
	d := listDeps(rec, nil)
	d.Profile = func(context.Context) (workspace.Profile, error) { return workspace.Profile{}, errors.New("unreadable") }
	if _, err := callApply(t, applyTool(t, d, "apply_from_list"), &Turn{Channel: ChannelDashboard}, `{}`); err == nil || len(rec.reqs) != 0 {
		t.Fatalf("err = %v, runs = %d", err, len(rec.reqs))
	}
}

func TestApplyRequest_OwnWordsLeaveAQuotedCorrectionOut(t *testing.T) {
	const url = "https://jobs.example.test/acme/44"
	msg := "apply to " + url + " and submit it\n> Please correct my application for Example Corp at " + url
	req := request(gates.ToolAutofillApplication, ToolCall{Turn: &Turn{Channel: ChannelDashboard, Origin: OriginModel, UserMessage: msg}}, true, nil)
	if req.UserMessage != msg || !req.ExplicitRequest || gates.CorrectionAsked(req.OwnWords) || !strings.Contains(req.OwnWords, url) {
		t.Fatalf("request %+v", req)
	}
}
