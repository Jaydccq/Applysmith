package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/agent/sessions"
	"applysmith/internal/answerbank/bind"
	"applysmith/internal/answerbank/match"
	"applysmith/internal/discovery"
	"applysmith/internal/tracker"
)

type stubBank struct{ result match.Result }

func (b stubBank) Preview(_ context.Context, req bind.Request) (bind.Outcome, error) {
	r := b.result
	r.Selector = req.Questions[0].Selector
	return bind.Outcome{Results: []match.Result{r}}, nil
}

type stubInbox struct{ rows []discovery.InboxRow }

func (s stubInbox) InboxList(_ context.Context, q discovery.InboxQuery) ([]discovery.InboxRow, error) {
	var out []discovery.InboxRow
	rows := slices.Clone(s.rows)
	if q.Newest {
		slices.SortStableFunc(rows, func(a, b discovery.InboxRow) int { return b.FirstSeen.Compare(a.FirstSeen) })
	}
	for _, r := range rows {
		if r.Status == q.Status && (q.Limit == 0 || len(out) < q.Limit) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s stubInbox) InboxCount(_ context.Context, q discovery.InboxQuery) (int, error) {
	n := 0
	for _, r := range s.rows {
		if r.Status == q.Status {
			n++
		}
	}
	return n, nil
}

func stubLanes(context.Context) ([]ApplyLane, error) {
	score := 4.3
	return []ApplyLane{{Lane: "apply-now", Total: 1, Items: []ApplyItem{{Company: "Example Corp", Role: "Backend Engineer", URL: "https://jobs.example.test/acme/1", Score: &score}}},
		{Lane: "selective", Total: 0}}, nil
}

func runTool(t *testing.T, tool Tool, f *fixture, args string) (ToolResult, error) {
	t.Helper()
	tn := &Turn{ID: "turn_x", SessionID: "ses_x", Channel: ChannelDashboard}
	raw, _, err := splitStep(json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateArgs(tool.params, raw); err != nil {
		t.Fatalf("%s: arguments %s: %v", tool.Name, args, err)
	}
	return tool.Run(context.Background(), ToolCall{CallID: "call_1", Args: raw, Turn: tn})
}

func toolOf(t *testing.T, tools []Tool, name string) Tool {
	t.Helper()
	reg, err := NewRegistry(tools...)
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := reg.Lookup(name)
	if !ok {
		t.Fatalf("no tool %s", name)
	}
	return tool
}

func TestTool_TrackerQuery_CountsAndListsRows(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, r := range []tracker.AddRow{
		{Company: "Example Corp", Role: "Backend Engineer", URL: "https://jobs.example.test/acme/1", Status: tracker.Evaluated},
		{Company: "Sample Labs", Role: "Platform Engineer", URL: "https://jobs.example.test/sample/2", Status: tracker.Applied, AppliedAt: "2026-01-05"},
		{Company: "Sample Labs", Role: "Data Engineer", URL: "https://jobs.example.test/sample/3", Status: tracker.Applied, AppliedAt: "2026-01-06"},
	} {
		if _, err := f.tracker.Add(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	tool := toolOf(t, ReadTools(ToolDeps{Tracker: f.tracker}), "tracker_query")
	if tool.Class != ClassRead || tool.OutsideContent {
		t.Fatalf("metadata = %+v", tool)
	}
	res, err := runTool(t, tool, f, `{"status":"Applied","step":"Counting applied rows"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"applied=2", "evaluated=1", "Matching rows: 2", "Sample Labs", "Data Engineer"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("result lacks %q:\n%s", want, res.Text)
		}
	}
	if strings.Contains(res.Text, "Example Corp") {
		t.Errorf("the status filter did not apply:\n%s", res.Text)
	}
	res, _ = runTool(t, tool, f, `{"company":"example","limit":1}`)
	if !strings.Contains(res.Text, "Matching rows: 1") || !strings.Contains(res.Text, "Example Corp") {
		t.Errorf("company filter:\n%s", res.Text)
	}
	if _, err := runTool(t, tool, f, `{"status":"hired"}`); err == nil || !strings.Contains(err.Error(), "valid statuses") {
		t.Errorf("unknown status = %v", err)
	}
	if res, _ := runTool(t, tool, f, `{}`); !strings.Contains(res.Text, "Matching rows: 3") {
		t.Errorf("no filter:\n%s", res.Text)
	}
	// "The top one by score", and a limit over 25 is clamped, not refused.
	if err := f.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE tracker_entries SET score = CASE role WHEN 'Platform Engineer' THEN 4.6 ELSE 3.1 END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if res, _ := runTool(t, tool, f, `{"sort":"score","limit":1}`); !strings.Contains(res.Text, "Platform Engineer") || strings.Contains(res.Text, "Data Engineer") {
		t.Errorf("by score:\n%s", res.Text)
	}
	if res, err := runTool(t, tool, f, `{"limit":500}`); err != nil || !strings.Contains(res.Text, "Matching rows: 3 (showing 3)") {
		t.Errorf("large limit = %q, %v", res.Text, err)
	}
}

func TestTool_FindReport_ListsAndReadsAReportAndTaints(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "reports"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The scorecard block repeats the Fit table as JSON; the model reads the
	// table and the sections after the block instead.
	md := "# Report 7\n\nBackend Engineer at Example Corp.\n\n## Fit\n\n| must-have | Go | 1.0 |\n\n```scorecard\n{\"items\": {\"must_have\": [" +
		strings.Repeat(`{"label":"Go","hit":1},`, 600) + "{}]}}\n```\n\n## Pay\n\nThe range meets the target.\n"
	if err := os.WriteFile(filepath.Join(root, "reports", "007-example.md"), []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	err := f.st.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO report_artifacts (report_number, kind, path, status, content_hash, created_at, updated_at)
			VALUES (7, 'final', 'reports/007-example.md', 'ready', 'h', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO report_index (report_number, kind, company, role, posting_url, identity_key, score, score_kind, recommendation,
			legitimacy, verification, markdown_path, html_path, job_id, created_at)
			VALUES (7, 'final', 'Example Corp', 'Backend Engineer', 'https://jobs.example.test/acme/1', 'ex:1', 4.2, 'final', 'apply', 'credible',
			'cli pass 2026-01-01', 'reports/007-example.md', 'reports/007-example.html', 'job1', '2026-01-01T00:00:00Z')`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	tool := toolOf(t, ReadTools(ToolDeps{Store: f.st, WorkspaceRoot: root}), "find_report")
	if !tool.OutsideContent {
		t.Fatal("a report quotes the posting, so find_report returns outside content")
	}
	list, err := runTool(t, tool, f, `{"company":"example"}`)
	if err != nil || !strings.Contains(list.Text, "Report 7 | Example Corp | Backend Engineer | score 4.2") {
		t.Fatalf("list = %q, %v", list.Text, err)
	}
	one, err := runTool(t, tool, f, `{"number":7}`)
	if err != nil || !strings.Contains(one.Text, "# Report 7") || !strings.Contains(one.Text, "apply") {
		t.Fatalf("report = %q, %v", one.Text, err)
	}
	if strings.Contains(one.Text, "scorecard\n") || strings.Contains(one.Text, `"hit"`) || !strings.Contains(one.Text, "| must-have | Go | 1.0 |") ||
		!strings.Contains(one.Text, "## Pay\n\nThe range meets the target.") || len(one.Text) > ModelResultLimit {
		t.Errorf("report as the model reads it (%d characters):\n%s", len(one.Text), one.Text)
	}
	// The whole report opens on the Reports page.
	if one.NextAction != "Open report 7 in full: /reports?id=7" || !strings.Contains(tool.Description, "/reports?id=<number>") {
		t.Errorf("next action %q, description %q", one.NextAction, tool.Description)
	}
	if none, _ := runTool(t, tool, f, `{"number":99}`); !strings.Contains(none.Text, "no report number 99") {
		t.Errorf("missing report = %q", none.Text)
	}
}

func TestTool_AnswerBankLookup_ReportsTheBanksAnswerAndIsMarkedHighPII(t *testing.T) {
	f := newFixture(t)
	tool := toolOf(t, ReadTools(ToolDeps{Bank: stubBank{result: match.Result{Outcome: "answered", Answer: "Two months", EntryID: "notice-period", Source: "bank", Confidence: 0.92}}}), "answer_bank_lookup")
	if tool.PII != PIIHigh || tool.Class != ClassRead {
		t.Fatalf("metadata = %+v", tool)
	}
	res, err := runTool(t, tool, f, `{"question":"What is your notice period?"}`)
	if err != nil || !strings.Contains(res.Text, "Answer: Two months") || !strings.Contains(res.Text, "Entry: notice-period") {
		t.Fatalf("result = %q, %v", res.Text, err)
	}
	none := toolOf(t, ReadTools(ToolDeps{Bank: stubBank{result: match.Result{Outcome: "abstain", Reason: "no_match"}}}), "answer_bank_lookup")
	res, _ = runTool(t, none, f, `{"question":"Why do you want this job?"}`)
	if !strings.Contains(res.Text, "no answer") || !strings.Contains(res.Text, "no_match") {
		t.Fatalf("abstain = %q", res.Text)
	}
	if _, err := NewRegistry(tool); err != nil {
		t.Fatal(err)
	}
}

func TestTool_ApplyListStatusAndListPipeline(t *testing.T) {
	f := newFixture(t)
	status := toolOf(t, ReadTools(ToolDeps{ApplyLanes: stubLanes}), "apply_list_status")
	res, err := runTool(t, status, f, `{}`)
	if err != nil || !strings.Contains(res.Text, "Lane apply-now: 1 posting\n") || !strings.Contains(res.Text, "Example Corp | Backend Engineer | score 4.3") {
		t.Fatalf("apply list = %q, %v", res.Text, err)
	}
	urls, err := applyListURLs(stubLanes)(context.Background())
	if err != nil || len(urls) != 1 || urls[0] != "https://jobs.example.test/acme/1" {
		t.Fatalf("urls = %v, %v", urls, err)
	}

	found := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	inbox := stubInbox{rows: []discovery.InboxRow{
		{ID: 1, Company: "Example Corp", Role: "Backend Engineer", Location: "Remote", Score: 88, URL: "https://jobs.example.test/acme/1", Status: discovery.InboxPending, FirstSeen: found, PostedAt: time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)},
		{ID: 2, Company: "Old Corp", Role: "Clerk", Status: discovery.InboxDismissed},
		{ID: 3, Company: "Fresh Corp", Role: "Data Engineer", Score: 40, Status: discovery.InboxPending, FirstSeen: found.Add(24 * time.Hour)},
	}}
	pipe := toolOf(t, ReadTools(ToolDeps{Inbox: inbox}), "list_pipeline")
	res, _ = runTool(t, pipe, f, `{"limit":1}`)
	if !strings.Contains(res.Text, "2 postings pending (showing 1)\n") || !strings.Contains(res.Text, "Example Corp") || !strings.Contains(res.Text, "found 2026-10-28") || strings.Contains(res.Text, "Old Corp") {
		t.Fatalf("pending = %q", res.Text)
	}
	// The day the source posted it is listed when known: without it the agent
	// read page after page to check "posted this week".
	if !strings.Contains(res.Text, "found 2026-10-28 | posted 2026-10-27 |") {
		t.Fatalf("posted = %q", res.Text)
	}
	if res, _ = runTool(t, pipe, f, `{"order":"newest","limit":1}`); !strings.Contains(res.Text, "Fresh Corp") || !strings.Contains(res.Text, "found 2026-10-29") || strings.Contains(res.Text, "posted") {
		t.Fatalf("newest = %q", res.Text)
	}
	res, _ = runTool(t, pipe, f, `{"status":"dismissed"}`)
	if !strings.Contains(res.Text, "Old Corp") {
		t.Fatalf("dismissed = %q", res.Text)
	}

	// A row the user's filters turn away is marked, so the agent does not send
	// it to an evaluation that ends at the gate.
	senior := func(r discovery.InboxRow) string {
		if strings.Contains(r.Role, "Data") {
			return "seniority"
		}
		return ""
	}
	gated := toolOf(t, ReadTools(ToolDeps{Inbox: inbox, InboxGate: func(context.Context) (discovery.Gate, error) { return senior, nil }}), "list_pipeline")
	res, _ = runTool(t, gated, f, `{}`)
	if !strings.Contains(res.Text, "Fresh Corp | Data Engineer") || !strings.Contains(res.Text, "/acme/1\n") || !strings.Contains(res.Text, "| the user's filters skip it (seniority)\n") {
		t.Fatalf("gated = %q", res.Text)
	}

	// The count is the inbox's, not the page's, and a limit over 25 lists 25.
	var many []discovery.InboxRow
	for i := range 30 {
		many = append(many, discovery.InboxRow{ID: int64(i + 1), Company: fmt.Sprintf("Corp %d", i), Status: discovery.InboxPending})
	}
	pipe = toolOf(t, ReadTools(ToolDeps{Inbox: stubInbox{rows: many}}), "list_pipeline")
	res, err = runTool(t, pipe, f, `{"limit":3}`)
	if err != nil || !strings.HasPrefix(res.Text, "30 postings pending (showing 3)\n") || strings.Count(res.Text, "\n") != 4 {
		t.Fatalf("paged = %q, %v", res.Text, err)
	}
	if res, _ = runTool(t, pipe, f, `{"limit":100}`); !strings.HasPrefix(res.Text, "30 postings pending (showing 25)\n") {
		t.Fatalf("clamped = %q", res.Text)
	}
}

func TestTool_RecallSession_ReturnsRankedSnippetsFromOtherConversations(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	old, _ := f.sessions.Create(ctx, sessions.Telegram, "")
	f.sessions.Append(ctx, sessions.Message{SessionID: old.ID, TurnID: "t1", Role: sessions.RoleUser, Body: "I am considering relocation to Exampleville", OK: true})
	here, _ := f.sessions.Create(ctx, sessions.Dashboard, "")
	f.sessions.Append(ctx, sessions.Message{SessionID: here.ID, TurnID: "t2", Role: sessions.RoleUser, Body: "relocation again", OK: true})
	tool := toolOf(t, ReadTools(ToolDeps{Sessions: f.sessions}), "recall_session")

	tn := &Turn{ID: "turn_x", SessionID: here.ID, Channel: ChannelDashboard}
	res, err := tool.Run(ctx, ToolCall{CallID: "c", Args: json.RawMessage(`{"query":"relocation"}`), Turn: tn})
	if err != nil || !strings.Contains(res.Text, old.ID) || !strings.Contains(res.Text, "telegram") || strings.Contains(res.Text, here.ID) {
		t.Fatalf("recall = %q, %v", res.Text, err)
	}
	if err := f.sessions.Delete(ctx, old.ID); err != nil {
		t.Fatal(err)
	}
	res, _ = tool.Run(ctx, ToolCall{CallID: "c", Args: json.RawMessage(`{"query":"relocation"}`), Turn: tn})
	if strings.Contains(res.Text, old.ID) || !strings.Contains(res.Text, "No earlier conversation") {
		t.Fatalf("a deleted conversation was recalled: %q", res.Text)
	}
}
