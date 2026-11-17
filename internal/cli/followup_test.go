package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/evaluate"
	"applysmith/internal/llm"
	"applysmith/internal/messages"
	"applysmith/internal/tracker"
)

func daysAgo(n int) string { return time.Now().AddDate(0, 0, -n).Format("2006-01-02") }

func (te *testEnv) addApplied(company, appliedAt string) {
	te.t.Helper()
	te.mustTracker("add", "--company", company, "--role", "Engineer", "--url", "https://example.com/"+strings.ToLower(company),
		"--status", "applied", "--applied-at", appliedAt)
}

// followups reads `tracker followups --json`, the document of GET
// /api/tracker/followups.
func (te *testEnv) followups(args ...string) []wire.FollowupSuggestion {
	te.t.Helper()
	var out wire.FollowupList
	if err := json.Unmarshal([]byte(te.mustTracker(append([]string{"followups", "--json"}, args...)...).stdout), &out); err != nil {
		te.t.Fatal(err)
	}
	return out.Suggestions
}

func TestFollowupCLI_DueListRecordAndSnooze(t *testing.T) {
	te := newTestEnv(t)
	te.addApplied("Alpha", daysAgo(8))
	te.addApplied("Beta", daysAgo(2))

	due := te.followups("--due")
	if len(due) != 1 || due[0].ID != 1 || due[0].Kind != string(tracker.KindFollowUp) || due[0].Number != 1 {
		t.Fatalf("%+v", due)
	}
	if all := te.followups(); len(all) != 2 {
		t.Errorf("without --due the upcoming follow-up is listed too: %+v", all)
	}
	if o := te.mustTracker("followups", "--due"); !strings.Contains(o.stdout, "row 1 follow_up") || strings.Contains(o.stdout, "row 2") {
		t.Errorf("%s", o.stdout)
	}

	te.mustTracker("followup", "1")
	if due := te.followups("--due"); len(due) != 0 {
		t.Errorf("a recorded follow-up is not due again: %+v", due)
	}
	te.mustTracker("followup", "1", "--undo")
	if due := te.followups("--due"); len(due) != 1 {
		t.Errorf("an undone follow-up is due again: %+v", due)
	}
	te.mustTracker("followup", "2", "--snooze", "3d")
	if !strings.Contains(te.mustTracker("show", "2").stdout, "followup_snoozed") {
		t.Error("the snooze left no event")
	}
}

func TestFollowupCLI_InvalidInputs(t *testing.T) {
	te := newTestEnv(t)
	te.addApplied("Alpha", daysAgo(8))
	te.addRow("Beta", "Engineer", "https://example.com/beta")
	tests := []struct {
		args     []string
		code     int
		contains string
	}{
		{[]string{"followup"}, 2, "usage"},
		{[]string{"followup", "99"}, 1, "no tracker row 99"},
		{[]string{"followup", "1", "--snooze", "soon"}, 2, "days"},
	}
	for _, tt := range tests {
		if o := te.tracker(tt.args...); o.code != tt.code || !strings.Contains(o.stderr, tt.contains) {
			t.Errorf("%v: code %d stderr %q, want %d containing %q", tt.args, o.code, o.stderr, tt.code, tt.contains)
		}
	}
}

// draft runs a drafting command with the fake provider answering from the
// fixtures and the evaluation generator.
func (te *testEnv) draft(fixtures []llm.Fixture, args ...string) output {
	te.t.Helper()
	var out, errw syncBuffer
	e := te.env(&out, &errw)
	fake := llm.NewFake(fixtures...)
	fake.SetGenerator(evaluate.FakeGenerator)
	e.llmOptions = []llm.Option{llm.WithFake(fake), llm.WithOfflineSimulation()}
	code := e.run(context.Background(), append(append([]string{}, args...), "--workspace", te.ws))
	return output{code, out.String(), errw.String()}
}

const cliDraftReply = `{"primary":{"angle":"fit","message":"Hi Sam, I applied for the Engineer role at Alpha and would like a short call this week."},
"alternatives":[],"claims":[{"text":"applied for the Engineer role","facts":["T1"]}],"assumptions":["Sam handles this role."]}`

func TestDraftsCLI_OutreachFollowupAndDecode(t *testing.T) {
	te := newTestEnv(t)
	if o := te.run(context.Background(), "init", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("init: %+v", o)
	}
	te.addApplied("Alpha", daysAgo(9))
	fx := []llm.Fixture{{Task: messages.Task, Contains: "Task: followup", Steps: []llm.Step{{Text: cliDraftReply}}}}

	o := te.draft(fx, "followup-draft", "--candidacy", "1", "--name", "Sam")
	if o.code != 0 || !strings.Contains(o.stdout, "Follow-up draft (recruiter_ping) for Alpha, Engineer.") || !strings.Contains(o.stdout, "Nothing was sent") {
		t.Fatalf("followup-draft: %+v", o)
	}
	o = te.draft(fx, "followup-draft", "--candidacy", "1", "--json")
	var d messages.Draft
	if err := json.Unmarshal([]byte(o.stdout), &d); err != nil || d.Kind != messages.FollowupPing || len(d.Confirmed) != 1 || len(d.ToConfirm) != 1 {
		t.Fatalf("json %+v err %v (%s)", d, err, o.stderr)
	}
	if tr := te.followups(); len(tr) != 1 || tr[0].Number != 1 {
		t.Fatalf("drafting records no follow-up: %+v", tr)
	}

	// An outreach note needs a contact kind; a refused kind is a usage error.
	if o := te.draft(nil, "outreach", "--company", "Alpha"); o.code != 2 {
		t.Fatalf("no contact: %+v", o)
	}
	if o := te.draft(nil, "outreach", "--company", "Alpha", "--contact", "friend"); o.code != 2 || !strings.Contains(o.stderr, "contact kind") {
		t.Fatalf("bad kind: %+v", o)
	}
	// With no model set up a person must act (exit 3, docs/setup/reference.md).
	if o := te.run(context.Background(), "outreach", "--company", "Alpha", "--contact", "recruiter", "--workspace", te.ws); o.code != 3 || !strings.Contains(o.stderr, "applysmith llm configure") {
		t.Fatalf("no model: %+v", o)
	}
	if o := te.draft(nil, "followup-draft", "--candidacy", "9"); o.code != 2 {
		t.Fatalf("unknown row: %+v", o)
	}

	o = te.draft(nil, "evaluate", "decode", "--text", cliPosting, "--company", "Example Systems", "--title", "Software Engineer")
	if o.code != 0 || !strings.Contains(o.stdout, "# Decode: Example Systems - Software Engineer") || !strings.Contains(o.stdout, "## Match matrix") {
		t.Fatalf("decode: %+v", o)
	}
	if o := te.draft(nil, "evaluate", "decode", "--text", "Engineer wanted."); o.code != 2 {
		t.Fatalf("short posting: %+v", o)
	}
}
