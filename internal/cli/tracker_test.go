package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api"
	"applysmith/internal/api/wire"
	"applysmith/internal/store"
	"applysmith/internal/tracker"
	"applysmith/internal/workspace"
)

func (te *testEnv) tracker(args ...string) output {
	te.t.Helper()
	return te.run(context.Background(), append([]string{"tracker"}, append(args, "--workspace", te.ws)...)...)
}

func (te *testEnv) mustTracker(args ...string) output {
	te.t.Helper()
	o := te.tracker(args...)
	if o.code != 0 {
		te.t.Fatalf("applysmith tracker %v: code %d, stderr %q", args, o.code, o.stderr)
	}
	return o
}

func (te *testEnv) addRow(company, role, url string) {
	te.t.Helper()
	te.mustTracker("add", "--company", company, "--role", role, "--url", url)
}

func TestTrackerCLI_AddListShowSetStatus(t *testing.T) {
	te := newTestEnv(t)
	te.addRow("Example Corp", "Platform Engineer", "https://example.com/jobs/1")
	te.addRow("Sample Corp", "Data Analyst", "https://example.org/jobs/2")

	o := te.mustTracker("list")
	if !strings.Contains(o.stdout, "Example Corp") || !strings.Contains(o.stdout, "Sample Corp") || !strings.Contains(o.stdout, "2 total") {
		t.Errorf("%s", o.stdout)
	}

	// A status is read in any letter case, as the original tracker wrote it.
	te.mustTracker("set-status", "1", "Applied", "--note", "sent through the site", "--on", "2027-03-01")
	o = te.mustTracker("show", "1")
	for _, want := range []string{"status: applied", "sent through the site", "created", "status_changed", "evaluated -> applied"} {
		if !strings.Contains(o.stdout, want) {
			t.Errorf("show lacks %q:\n%s", want, o.stdout)
		}
	}
	var d wire.CandidacyDetail
	shown := te.mustTracker("show", "1", "--json").stdout
	if err := json.Unmarshal([]byte(shown), &d); err != nil {
		t.Fatal(err)
	}
	if d.Entry.AppliedAt != "2027-03-01" || d.Entry.AppliedBy != string(tracker.ByUser) || len(d.Events) < 4 {
		t.Errorf("%+v", d)
	}
	if !strings.Contains(shown, `"previousStatus"`) {
		t.Errorf("show --json is not the API's candidacy detail:\n%s", shown)
	}
	// Only the row moved to applied today is listed; the filter is the tracker's.
	if o = te.mustTracker("list", "--reached", "Applied", "--since", time.Now().Format(time.DateOnly)); !strings.Contains(o.stdout, "Example Corp") || strings.Contains(o.stdout, "Sample Corp") {
		t.Errorf("--reached:\n%s", o.stdout)
	}
}

func TestTrackerCLI_JSONListCarriesAppliedByAndFlags(t *testing.T) {
	te := newTestEnv(t)
	te.addRow("Example Corp", "Platform Engineer", "https://example.com/jobs/1")
	te.mustTracker("set-docs", "1", "--resume")
	te.mustTracker("set-status", "1", "applied")
	var page wire.CandidacyPage
	raw := te.mustTracker("list", "--json").stdout
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"appliedBy": "user"`) || !strings.Contains(raw, `"resumePdf": true`) || !strings.Contains(raw, `"coverLetter": false`) {
		t.Errorf("%s", raw)
	}
	assertAPIShape(t, raw)
	if len(page.Entries) != 1 || !page.Entries[0].ResumePDF {
		t.Errorf("%+v", page)
	}
	o := te.mustTracker("set-docs", "1", "--resume", "--clear")
	if !strings.Contains(o.stdout, "resume false") {
		t.Errorf("%s", o.stdout)
	}
}

func TestTrackerCLI_InvalidInputsFailWithAllowedValues(t *testing.T) {
	te := newTestEnv(t)
	te.addRow("Example Corp", "Platform Engineer", "https://example.com/jobs/1")
	tests := []struct {
		args     []string
		code     int
		contains string
	}{
		{[]string{"set-status", "1", "maybe"}, 2, "discarded"},
		{[]string{"set-status", "abc", "applied"}, 2, "not a row id"},
		{[]string{"set-status", "0", "applied"}, 2, "not a row id"},
		{[]string{"set-status", "99", "applied"}, 1, "no tracker row 99; `applysmith tracker list`"},
		{[]string{"set-status", "1"}, 2, "usage"},
		{[]string{"show", "99"}, 1, "no tracker row 99"},
		{[]string{"add", "--company", "X"}, 2, "usage"},
		{[]string{"add", "--company", "X Corp", "--role", "Y", "--status", "maybe"}, 2, "evaluated"},
		{[]string{"list", "--status", "maybe"}, 2, "evaluated"},
		{[]string{"list", "--status", "applied,maybe"}, 2, "evaluated"},
		{[]string{"list", "--applied-within", "-1"}, 2, "appliedWithin"},
		{[]string{"list", "--sort", "random"}, 2, "updated_desc"},
		{[]string{"list", "--page-size", "500"}, 2, "100"},
		{[]string{"archive", "--older-than", "soon"}, 2, "45d"},
		{[]string{"export", "--format", "xml"}, 2, "format must be md"},
		{[]string{"set-docs", "1"}, 2, "usage"},
		{[]string{"note", "1"}, 2, "usage"},
		{[]string{"note", "1", "text", "--undo"}, 2, "usage"},
		{[]string{"frobnicate"}, 2, "usage"},
	}
	for _, tt := range tests {
		o := te.tracker(tt.args...)
		if o.code != tt.code || !strings.Contains(o.stderr, tt.contains) {
			t.Errorf("%v: code %d stderr %q, want %d containing %q", tt.args, o.code, o.stderr, tt.code, tt.contains)
		}
	}
	if o := te.run(context.Background(), "tracker"); o.code != 2 || !strings.Contains(o.stderr, "usage") {
		t.Errorf("%+v", o)
	}
}

func TestTrackerCLI_ExportMarkdownCSVAndFile(t *testing.T) {
	te := newTestEnv(t)
	te.addRow("Example Corp", "Platform Engineer", "https://example.com/jobs/1")
	te.mustTracker("note", "1", "=SUM(1+1)")
	md := te.mustTracker("export", "--format", "md").stdout
	if !strings.HasPrefix(md, "| id | date | company") || !strings.Contains(md, "Example Corp") {
		t.Errorf("%s", md)
	}
	out := filepath.Join(t.TempDir(), "rows.csv")
	te.mustTracker("export", "--format", "csv", "--out", out)
	raw, err := os.ReadFile(out)
	if err != nil || !strings.Contains(string(raw), "Example Corp") || !strings.Contains(string(raw), "user: =SUM(1+1)") {
		t.Errorf("%s %v", raw, err)
	}
	if o := te.mustTracker("export", "--status", "applied"); strings.Contains(o.stdout, "Example Corp") {
		t.Error("filter ignored")
	}
	if o := te.mustTracker("export", "--status", "Applied,Evaluated"); !strings.Contains(o.stdout, "Example Corp") {
		t.Errorf("a status list in any letter case should keep the evaluated row: %s", o.stdout)
	}
}

func TestTrackerCLI_ArchiveAndUnarchive(t *testing.T) {
	te := newTestEnv(t)
	te.addRow("Example Corp", "Platform Engineer", "https://example.com/jobs/1")
	te.mustTracker("set-status", "1", "rejected")
	if o := te.mustTracker("archive", "--dry-run"); !strings.Contains(o.stdout, "would archive 0 rows") {
		t.Errorf("a fresh row is not old enough: %s", o.stdout)
	}
	// Age the events in the database directly, as 45 days of waiting would.
	te.withStore(func(st *store.Store) {
		if err := st.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`DROP TRIGGER tracker_events_no_rewrite`)
			if err != nil {
				return err
			}
			_, err = tx.Exec(`UPDATE tracker_events SET at = '2020-01-01T00:00:00Z'`)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	})
	if o := te.mustTracker("archive", "--dry-run", "--older-than", "45d"); !strings.Contains(o.stdout, "would archive row 1") {
		t.Errorf("%s", o.stdout)
	}
	if o := te.mustTracker("list"); !strings.Contains(o.stdout, "Example Corp") {
		t.Error("a dry run archived")
	}
	te.mustTracker("archive")
	if o := te.mustTracker("list"); strings.Contains(o.stdout, "Example Corp") {
		t.Errorf("archived rows are hidden by default: %s", o.stdout)
	}
	if o := te.mustTracker("list", "--archived"); !strings.Contains(o.stdout, "Example Corp") {
		t.Error("--archived shows them")
	}
	te.mustTracker("unarchive", "1")
	if o := te.mustTracker("list"); !strings.Contains(o.stdout, "Example Corp") {
		t.Error("unarchive failed")
	}
}

func TestTrackerCLI_DedupDryRunThenApply(t *testing.T) {
	te := newTestEnv(t)
	te.addRow("Example Corp", "Platform Engineer", "https://example.com/jobs/1")
	if o := te.mustTracker("dedup"); !strings.Contains(o.stdout, "no duplicate rows") {
		t.Errorf("%s", o.stdout)
	}
	te.withStore(func(st *store.Store) {
		if err := st.Write(context.Background(), func(tx *sql.Tx) error {
			res, err := tx.Exec(`INSERT INTO tracker_entries (date, company, role, status, identity_key, created_at, updated_at) VALUES ('2027-01-01', 'Example Corp', 'Platform Engineer', 'evaluated', 'k:twin', 'x', 'x')`)
			if err != nil {
				return err
			}
			id, _ := res.LastInsertId()
			_, err = tx.Exec(`INSERT INTO tracker_events (entry_id, at, actor, kind, new_status) VALUES (?, '2027-01-01T00:00:00Z', 'user', 'created', 'evaluated')`, id)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	})
	o := te.mustTracker("dedup")
	if !strings.Contains(o.stdout, "would merge") || !strings.Contains(o.stdout, "dry run") || !strings.Contains(te.mustTracker("list").stdout, "2 total") {
		t.Errorf("%s", o.stdout)
	}
	// --json prints the document of GET /api/tracker/duplicates.
	var plan wire.TrackerDuplicates
	if o := te.mustTracker("dedup", "--json"); json.Unmarshal([]byte(o.stdout), &plan) != nil || len(plan.Groups) != 1 || len(plan.Groups[0].Rows) != 2 ||
		!strings.Contains(o.stdout, `"needsUser"`) {
		t.Errorf("dedup --json: %s", o.stdout)
	}
	o = te.mustTracker("dedup", "--apply")
	if !strings.Contains(o.stdout, "merged") || !strings.Contains(te.mustTracker("list").stdout, "1 total") {
		t.Errorf("%s", o.stdout)
	}
}

func TestTrackerCLI_WorksBesideARunningServer(t *testing.T) {
	te := newTestEnv(t)
	te.addRow("Example Corp", "Platform Engineer", "https://example.com/jobs/1")
	rs := te.startServe()
	te.mustTracker("add", "--company", "Sample Corp", "--role", "Data Analyst", "--url", "https://example.org/jobs/2")
	te.mustTracker("set-status", "1", "applied")
	te.mustTracker("note", "2", "met", "the", "team")
	for _, args := range [][]string{{"list"}, {"show", "2"}, {"export"}, {"dedup"}, {"archive", "--dry-run"}, {"followups"}} {
		if o := te.tracker(args...); o.code != 0 {
			t.Errorf("%v beside a server: code %d, stderr %q", args, o.code, o.stderr)
		}
	}
	if !strings.Contains(te.mustTracker("list").stdout, "2 total") {
		t.Error("rows written beside the server are missing")
	}
	if _, ok := api.ProbeServer(strings.TrimPrefix(rs.origin, "http://")); !ok {
		t.Error("the CLI disturbed the server")
	}
}

// A workspace folder that does not exist (a mistyped --workspace) is
// refused by reads and writes alike, naming init, and nothing is created.
func TestCLI_AMissingWorkspaceIsRefusedAndNothingCreated(t *testing.T) {
	te := newBareTestEnv(t)
	for _, args := range [][]string{{"list"}, {"show", "1"}, {"export"}, {"dedup"}, {"followups"}, {"add", "--company", "Acme", "--role", "Engineer"}, {"set-status", "1", "applied"}} {
		o := te.tracker(args...)
		if o.code != 2 || !strings.Contains(o.stderr, "`applysmith init`") {
			t.Errorf("%v: code %d stderr %q", args, o.code, o.stderr)
		}
	}
	for _, args := range [][]string{{"inbox", "add", "https://example.com/jobs/1"}, {"blocked", "add", "Acme"}, {"kb", "list"}, {"report", "override", "1"},
		{"secrets", "delete", "anthropic"}, {"schedule", "list"}} {
		if o := te.run(context.Background(), append(args, "--workspace", te.ws)...); o.code != 2 || !strings.Contains(o.stderr, "`applysmith init`") {
			t.Errorf("%v: code %d stderr %q", args, o.code, o.stderr)
		}
	}
	if _, err := os.Stat(te.ws); !os.IsNotExist(err) {
		t.Errorf("a command created the workspace: %v", err)
	}
	if err := os.MkdirAll(te.ws, 0o700); err != nil {
		t.Fatal(err)
	}
	if o := te.tracker("list"); o.code != 1 || !strings.Contains(o.stderr, "`applysmith init`") {
		t.Errorf("a workspace with no database: %+v", o)
	}
	if o := te.run(context.Background(), "init", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("init: %+v", o)
	}
	te.mustTracker("list")
}

func TestTrackerCLI_ExportThatFailsKeepsTheExistingFile(t *testing.T) {
	te := newTestEnv(t)
	te.addRow("Example Corp", "Platform Engineer", "https://example.com/jobs/1")
	out := filepath.Join(t.TempDir(), "rows.md")
	if err := os.WriteFile(out, []byte("previous export\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if o := te.tracker("export", "--format", "xml", "--out", out); o.code == 0 {
		t.Fatal("a bad format was accepted")
	}
	if raw, _ := os.ReadFile(out); string(raw) != "previous export\n" {
		t.Errorf("a failed export damaged the file: %q", raw)
	}
	te.mustTracker("export", "--out", out)
	if raw, _ := os.ReadFile(out); !strings.Contains(string(raw), "Example Corp") {
		t.Errorf("%q", raw)
	}
	left, _ := filepath.Glob(out + ".*")
	if len(left) != 0 {
		t.Errorf("temporary files left behind: %v", left)
	}
}

func (te *testEnv) withStore(fn func(*store.Store)) {
	te.t.Helper()
	ws := workspace.Open(te.ws)
	id, err := ws.ID()
	if err != nil {
		te.t.Fatal(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(ws.StateDir(), store.DBFile), store.Options{WorkspaceID: id})
	if err != nil {
		te.t.Fatal(err)
	}
	defer st.Close()
	fn(st)
}

func TestTrackerCLI_OutcomesCountsInterviewThenRejected(t *testing.T) {
	te := newTestEnv(t)
	te.addRow("Example Corp", "Platform Engineer", "https://example.com/jobs/1")
	te.mustTracker("set-status", "1", "applied")
	te.mustTracker("set-status", "1", "interview")
	te.mustTracker("set-status", "1", "rejected")

	var o wire.TrackerOutcomes
	if err := json.Unmarshal([]byte(te.mustTracker("outcomes", "--json").stdout), &o); err != nil {
		t.Fatal(err)
	}
	var interview wire.OutcomeStageReach
	for _, s := range o.Overall.Stages {
		if s.Stage == string(tracker.Interview) {
			interview = s
		}
	}
	if o.Overall.Applications != 1 || interview.Numerator != 1 || interview.Denominator != 1 || o.AsOf == "" {
		t.Errorf("%+v", o)
	}
	text := te.mustTracker("outcomes", "--from", time.Now().Format("2006-01-02")).stdout
	for _, want := range []string{"read as of", "1/1", "fewer than 10", "No cause is inferred"} {
		if !strings.Contains(text, want) {
			t.Errorf("outcomes output lacks %q:\n%s", want, text)
		}
	}
	if bad := te.tracker("outcomes", "--as-of", "yesterday"); bad.code == 0 {
		t.Errorf("a bad cutoff must fail: %+v", bad)
	}
}
