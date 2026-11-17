package apply

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"path/filepath"
	"strings"
	"testing"
	"time"

	"applysmith/internal/store"
)

func lsStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "applysmith.db"), store.Options{Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func lsExec(t *testing.T, st *store.Store, q string, args ...any) {
	t.Helper()
	if err := st.Write(context.Background(), func(tx *sql.Tx) error { _, err := tx.Exec(q, args...); return err }); err != nil {
		t.Fatal(err)
	}
}

// lsInsertRun writes a run row the way a finished run leaves it.
func lsInsertRun(t *testing.T, st *store.Store, r RunFact) {
	t.Helper()
	signins, _ := json.Marshal(r.Signins)
	pages, _ := json.Marshal(r.Pages)
	var lines []StageLine
	if r.HasResolve {
		lines = append(lines, StageLine{Stage: StageResolve, Status: StatusOK, Detail: r.ResolveDetail})
	}
	if r.Snapshot != "" {
		lines = append(lines, StageLine{Stage: StageLearn, Status: StatusOK, Detail: "stopped; lane work-item (snapshot " + r.Snapshot + ")"})
	}
	stages, _ := json.Marshal(lines)
	if lines == nil {
		stages = []byte("[]")
	}
	finished := r.Finished
	if finished.IsZero() {
		finished = r.Started.Add(time.Minute)
	}
	lsExec(t, st, `INSERT INTO apply_runs (run_id, started_at, finished_at, caller, posting_url, identity_key, ats, report_number, outcome_kind, outcome_stage,
		outcome_class, outcome_reason, confirmed, build, signins_json, pages_json, stages_json, company, role) VALUES (?, ?, ?, 'apply_list', ?, ?, ?, NULLIF(?, 0), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Started.UTC().Format(stampLayout), finished.UTC().Format(stampLayout), r.URL, postingKey(r.URL), r.ATS, r.Report, string(r.Kind), string(r.Stage),
		string(r.Class), r.Reason, r.Confirmed, r.Build, string(signins), string(pages), string(stages), r.Company, r.Role)
}

func TestApplyList_Evidence_ReadsTheCompleteLedgerNotAWindow(t *testing.T) {
	ctx := context.Background()
	st := lsStore(t)
	stamp := t0.UTC().Format(stampLayout)
	lsExec(t, st, `INSERT INTO report_artifacts (report_number, kind, path, status, content_hash, created_at, updated_at) VALUES (7, 'final', 'reports/7.md', 'ready', 'h', ?, ?)`, stamp, stamp)
	lsExec(t, st, `INSERT INTO report_index (report_number, kind, company, role, posting_url, identity_key, score, score_kind, recommendation, legitimacy, verification, markdown_path, html_path, job_id, created_at)
		VALUES (7, 'final', 'Example Corp', 'Data Engineer', 'https://careers.example.test/jobs/7?utm_source=x', 'k7', 4.2, 'final', 'apply', 'credible', 'cli pass', 'reports/7.md', 'reports/7.html', 'j7', ?)`, stamp)
	lsExec(t, st, `INSERT INTO tracker_entries (date, company, role, status, report_number, posting_url, identity_key, created_at, updated_at) VALUES ('2027-03-01', 'Example Corp', 'Data Engineer', 'applied', 7, '', 'k7', ?, ?)`, stamp, stamp)
	lsExec(t, st, `INSERT INTO tracker_aliases (entry_id, identity_key) VALUES (1, 'alias7')`)
	// Documents and a documents override as the document service and the
	// tracker stamp them; a draft without its PDF lifts nothing.
	doc := func(version int, state, sha string, at time.Time) {
		lsExec(t, st, `INSERT INTO documents (bundle, version, kind, mode, candidacy_id, path, source_path, content_hash, manifest_hash, generation, language, paper,
			page_limit, limited, pdf_state, pdf_path, pdf_sha256, pages, warnings_json, run_id, created_at)
			VALUES ('b7', ?, 'resume', 'tailored', 1, 'documents/b7', 'documents/b7/resume.json', 'c', 'm', 'model', 'en', 'letter', 1, 0, ?, NULL, NULLIF(?, ''), NULL, '[]', 'r7', ?)`,
			version, state, sha, at.UTC().Format(time.RFC3339))
	}
	doc(1, "ready", "s1", t0.Add(-30*time.Minute))
	doc(2, "none", "", t0.Add(-10*time.Minute))
	overridden := t0.Add(-20 * time.Minute)
	lsExec(t, st, `INSERT INTO tracker_events (entry_id, at, actor, kind, detail_json) VALUES (1, ?, 'user', 'documents_override', '{"report_number":7}')`, overridden.UTC().Format(time.RFC3339))
	lsExec(t, st, `INSERT INTO submissions (identity_key, url, host, company, role, report_number, state, confirmed, source, observed_at, created_at, updated_at)
		VALUES ('k9', 'https://x.example.net/9', 'x.example.net', 'Sample Inc', 'Analyst', NULL, 'submitted', 1, 'agent', ?, ?, ?)`, stamp, stamp, stamp)
	lsExec(t, st, `INSERT INTO apply_batches (batch_id, created_at, deadline_at, caller, tool, submit, explicit) VALUES ('b1', ?, ?, 'apply_list', 'apply_from_list', 1, 1)`, stamp, stamp)
	lsExec(t, st, `INSERT INTO apply_batch_rows (batch_id, idx, url, state, updated_at) VALUES ('b1', 0, 'https://careers.example.test/jobs/mid', 'started', ?), ('b1', 1, 'https://careers.example.test/jobs/done', 'ended', ?)`, stamp, stamp)

	old := t0.Add(-40 * 24 * time.Hour)
	closed := "https://careers.example.test/jobs/closed"
	lsInsertRun(t, st, lsRun("old-1", closed, old.Add(-48*time.Hour), func(r *RunFact) { r.Class, r.Reason = ClassDefect, "an earlier stop" }))
	lsInsertRun(t, st, lsRun("old-2", closed, old, func(r *RunFact) {
		r.Class, r.Stage, r.Reason = ClassPosting, StageAccount, "posting closed: HTTP 404"
	}))
	lsInsertRun(t, st, lsRun("recent", "https://careers.example.test/jobs/recent", t0.Add(-2*time.Hour), func(r *RunFact) {
		r.Class, r.Signins, r.ATS, r.HasResolve, r.ResolveDetail = ClassCredential, []SigninRecord{{Host: "careers.example.test", Refusals: 3, Exhausted: true}}, "generic", true, "resolved"
	}))

	saved := t0.Add(-time.Hour)
	ev := SQLEvidence{Store: st, DataSavedAt: func(context.Context) (time.Time, error) { return saved, nil }}
	f, err := ev.Facts(ctx, t0)
	if err != nil || len(f.Unreadable) != 0 {
		t.Fatalf("%v %v", err, f.Unreadable)
	}
	if len(f.Ledger) != 1 || f.Ledger[0].Company != "Sample Inc" || !f.Ledger[0].Confirmed {
		t.Fatalf("ledger %+v", f.Ledger)
	}
	if len(f.Tracker) != 1 || f.Tracker[0].ReportURL == "" || len(f.Tracker[0].Aliases) != 1 || f.Tracker[0].Key != "k7" {
		t.Fatalf("tracker %+v", f.Tracker)
	}
	if len(f.Reports) != 1 || f.Reports[0].Role != "Data Engineer" {
		t.Fatalf("reports %+v", f.Reports)
	}
	var ids []string
	for _, r := range f.Runs {
		ids = append(ids, r.ID)
	}
	if strings.Join(ids, ",") != "old-2,recent" {
		t.Fatalf("runs %v: the newest run of a posting at any age and every recent run, not a superseded old one", ids)
	}
	if got := f.Runs[1]; got.Class != ClassCredential || len(got.Signins) != 1 || !got.HasResolve || got.ResolveDetail != "resolved" {
		t.Fatalf("run decoded as %+v", got)
	}
	if len(f.Interrupted) != 1 || f.Interrupted[0].Key != postingKey("https://careers.example.test/jobs/mid") {
		t.Fatalf("journal %+v", f.Interrupted)
	}
	if !f.DataSavedAt.Equal(saved) {
		t.Fatalf("data saved at %v", f.DataSavedAt)
	}
	if !f.DocumentsAt[1].Equal(overridden) {
		t.Fatalf("documents at %v, want the override, the newest of a ready document and an override", f.DocumentsAt)
	}

	sel := NewSelector(ev)
	res, err := sel.Select(ctx, []Candidate{
		lsCand(closed, "Closed Co", "Role"),
		lsCand("https://careers.example.test/jobs/mid", "Mid Co", "Role"),
		lsCand("https://careers.example.test/jobs/7", "Example Corp", "Data Engineer II"),
		lsCand("https://x.example.net/9", "Sample Inc", "Analyst"),
		lsCand("https://careers.example.test/jobs/next", "Next Co", "Role"),
	}, lsOpts(nil))
	if err != nil {
		t.Fatal(err)
	}
	rules := map[string]string{}
	for _, s := range res.Skipped {
		rules[s.URL] = s.Rule
	}
	want := map[string]string{
		closed: RuleHold, "https://careers.example.test/jobs/mid": RuleInterrupted, "https://x.example.net/9": RuleApplied,
		// The report URL of an applied tracker row, tracking parameter aside.
		"https://careers.example.test/jobs/7": RuleApplied,
	}
	for u, rule := range want {
		if rules[u] != rule {
			t.Errorf("%s: rule %q, want %q (all: %v)", u, rules[u], rule, rules)
		}
	}
	// The credential stop 2h ago cools its host, so the next row waits too.
	if rules["https://careers.example.test/jobs/next"] != RuleHold {
		t.Errorf("the account host is cooling: %v", rules)
	}
}

func TestApplyList_Evidence_UnreadableSourceBlocksASubmittingList(t *testing.T) {
	ctx := context.Background()
	st := lsStore(t)
	lsExec(t, st, `DROP TABLE apply_batch_rows`)
	ev := SQLEvidence{Store: st}
	f, err := ev.Facts(ctx, t0)
	if err != nil || len(f.Unreadable) != 1 || f.Unreadable[0] != "journal" {
		t.Fatalf("%v %v", err, f.Unreadable)
	}
	c := lsCand("https://careers.example.test/jobs/1", "Example Corp", "Data Engineer")
	if _, err := NewSelector(ev).Select(ctx, []Candidate{c}, lsOpts(func(o *SelectOptions) { o.Submit = true })); !errors.Is(err, ErrEvidenceUnreadable) {
		t.Fatalf("err = %v", err)
	}
	res, err := NewSelector(ev).Select(ctx, []Candidate{c}, lsOpts(nil))
	if err != nil || len(res.Rows) != 1 {
		t.Fatalf("a dry list goes on: %+v %v", res, err)
	}
}
