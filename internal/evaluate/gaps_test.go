// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"applysmith/internal/store"
)

// gapSeed is one synthetic final report.
type gapSeed struct {
	company, role, key, created string
	must, nice, hidden          []Item
	unsupported                 []string
	kind                        string
}

func miss(id, label string) Item {
	return Item{ID: id, Label: label, Source: "the posting asks for " + label}
}
func partial(id, label string) Item {
	return Item{ID: id, Label: label, Hit: 0.5, Evidence: "CV: some exposure to " + label}
}
func met(id, label string) Item {
	return Item{ID: id, Label: label, Hit: 1, Evidence: "CV: shipped " + label}
}

// seedReport writes a report file and its index row the way the evaluator
// leaves them, with a scorecard block built from the seed.
func seedReport(t *testing.T, e *tenv, s gapSeed) int64 {
	t.Helper()
	if s.kind == "" {
		s.kind = KindFinal
	}
	if s.created == "" {
		s.created = "2026-10-01T10:00:00Z"
	}
	var doc scorecardDoc
	doc.Items.Must, doc.Items.Nice, doc.Items.Hidden, doc.Unsupported = s.must, s.nice, s.hidden, s.unsupported
	card, _ := json.Marshal(doc)
	md := "# Report\n\n## Verdict\n\nA synthetic verdict.\n\n## Fit\n\n```scorecard\n" + string(card) + "\n```\n\n## Pay\n\nNone.\n"
	if s.kind != KindFinal {
		md = "# Report\n\n## Verdict\n\nScreened only.\n"
	}
	sum := sha256.Sum256([]byte(md))
	var n int64
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO report_artifacts (kind, path, status, content_hash, created_at, updated_at) VALUES ('report', ?, 'ready', ?, ?, ?)`,
			fmt.Sprintf("reports/seed-%s.md", s.key), hex.EncodeToString(sum[:]), s.created, s.created)
		if err != nil {
			return err
		}
		n, _ = res.LastInsertId()
		path := fmt.Sprintf("reports/%04d-seed.md", n)
		if _, err := tx.Exec(`UPDATE report_artifacts SET path = ? WHERE report_number = ?`, path, n); err != nil {
			return err
		}
		scoreKind := "final"
		if s.kind != KindFinal {
			scoreKind = "screening"
		}
		_, err = tx.Exec(`INSERT INTO report_index (report_number, kind, company, role, posting_url, identity_key, score_kind, recommendation, legitimacy, verification, markdown_path, html_path, job_id, created_at)
			VALUES (?, ?, ?, ?, '', ?, ?, 'apply', 'credible', 'pass', ?, ?, 'job', ?)`, n, s.kind, s.company, s.role, s.key, scoreKind, path, strings.TrimSuffix(path, ".md")+".html", s.created)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(e.root, "reports"), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(e.root, path), []byte(md), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

type fakeFinder struct {
	found []Resource
	err   error
	asked []string
}

func (f *fakeFinder) Find(_ context.Context, skill string, _ int) ([]Resource, error) {
	f.asked = append(f.asked, skill)
	return f.found, f.err
}

func (e *tenv) gaps(f ResourceFinder) *Gaps {
	return NewGaps(e.st, e.root, GapOptions{Now: func() time.Time { return testNow }, Finder: f})
}

func (e *tenv) analyze(g *Gaps, nums ...int64) GapReport {
	e.t.Helper()
	id, _, err := g.Start(context.Background(), "tester", fmt.Sprintf("key-%d", len(nums))+nowKey(), nums)
	if err != nil {
		e.t.Fatal(err)
	}
	rep, err := g.Get(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return rep
}

var keyCounter int

func nowKey() string { keyCounter++; return fmt.Sprintf("-%d", keyCounter) }

func gapOf(t *testing.T, r GapReport, skill string) Gap {
	t.Helper()
	g, ok := r.gap(skill)
	if !ok {
		t.Fatalf("no gap %q in %+v", skill, r.Gaps)
	}
	return g
}

func TestGaps_SamePostingFromTwoSourcesCountsOnce(t *testing.T) {
	e := newTenv(t)
	// A discovery report and a tracker report of one opening share its key.
	a := seedReport(t, e, gapSeed{company: "Example Corp", role: "Data Engineer", key: "k:one", created: "2026-10-01T10:00:00Z", must: []Item{miss("m1", "Rust")}})
	b := seedReport(t, e, gapSeed{company: "Example Corp", role: "Data Engineer", key: "k:one", created: "2026-10-05T10:00:00Z", must: []Item{miss("m1", "Rust")}})
	rep := e.analyze(e.gaps(nil), a, b)
	if got := gapOf(t, rep, "rust"); got.Required != 1 || len(got.Sources) != 1 {
		t.Fatalf("a posting seen twice must count once: %+v", got)
	}
	if len(rep.Sample) != 1 || rep.Sample[0].Report != b {
		t.Fatalf("the newest report stands for the opening: %+v", rep.Sample)
	}
	if len(rep.Excluded) != 1 || rep.Excluded[0].Report != a || rep.Excluded[0].Reason != ExcludedDuplicate || rep.Excluded[0].DuplicateOf != b {
		t.Fatalf("exclusions = %+v", rep.Excluded)
	}
}

func TestGaps_TrackerAliasJoinsReportsWhoseKeysDiffer(t *testing.T) {
	e := newTenv(t)
	a := seedReport(t, e, gapSeed{company: "Alpha Works", role: "Platform Engineer", key: "k:alpha", must: []Item{miss("m1", "Rust")}})
	b := seedReport(t, e, gapSeed{company: "Zeta Labs", role: "Systems Programmer", key: "k:zeta", must: []Item{miss("m1", "Rust")}})
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO tracker_entries (date, company, role, status, identity_key, created_at, updated_at) VALUES ('2026-10-01', 'Alpha Works', 'Platform Engineer', 'evaluated', 'k:alpha', '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		_, err = tx.Exec(`INSERT INTO tracker_aliases (identity_key, entry_id) VALUES ('k:zeta', ?)`, id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := gapOf(t, e.analyze(e.gaps(nil), a, b), "rust"); got.Required != 1 {
		t.Fatalf("a tracker row that answers to both keys makes them one opening: %+v", got)
	}
}

func TestGaps_DifferentRequisitionsAreDifferentPostings(t *testing.T) {
	e := newTenv(t)
	a := seedReport(t, e, gapSeed{company: "Example Corp", role: "Data Engineer", key: "greenhouse:examplecorp/1001", must: []Item{miss("m1", "Rust")}})
	b := seedReport(t, e, gapSeed{company: "Example Corp", role: "Data Engineer", key: "greenhouse:examplecorp/1002", must: []Item{miss("m1", "Rust")}})
	if got := gapOf(t, e.analyze(e.gaps(nil), a, b), "rust"); got.Required != 2 {
		t.Fatalf("two requisitions are two postings: %+v", got)
	}
}

func TestGaps_MissingEvidenceIsNotAConfirmedGap(t *testing.T) {
	e := newTenv(t)
	n := seedReport(t, e, gapSeed{company: "Example Corp", role: "Engineer", key: "k:1", must: []Item{miss("m1", "Kubernetes operators"), partial("m2", "Terraform"), met("m3", "Go")},
		nice: []Item{miss("n1", "GraphQL")}})
	g := e.gaps(nil)
	rep := e.analyze(g, n)
	if got := gapOf(t, rep, "kubernetes operators"); got.State != GapNotEvidenced {
		t.Fatalf("absence from the CV is not-evidenced, got %q", got.State)
	}
	if got := gapOf(t, rep, "terraform"); got.State != GapNeedsReview || got.Sources[0].Evidence == "" {
		t.Fatalf("partial evidence needs review and keeps its evidence: %+v", got)
	}
	if _, ok := rep.gap("go"); ok {
		t.Fatal("a requirement the profile evidences is no gap")
	}
	if _, err := g.BuildPlan(context.Background(), rep.ID, []string{"Kubernetes operators"}, false); !errors.Is(err, ErrGapInvalid) {
		t.Fatalf("a not-evidenced gap must be confirmed before it is planned, got %v", err)
	}
	rep, err := g.Confirm(context.Background(), rep.ID, "Kubernetes Operators", true)
	if err != nil {
		t.Fatal(err)
	}
	if got := gapOf(t, rep, "kubernetes operators"); got.State != GapConfirmed {
		t.Fatalf("state after confirmation = %q", got.State)
	}
	if rep, err = g.BuildPlan(context.Background(), rep.ID, []string{"kubernetes operators"}, false); err != nil || rep.Plan == nil || len(rep.Plan.Items) != 1 {
		t.Fatalf("plan = %+v, %v", rep.Plan, err)
	}
	if rep, err = g.Confirm(context.Background(), rep.ID, "kubernetes operators", false); err != nil || gapOf(t, rep, "kubernetes operators").State != GapNotEvidenced {
		t.Fatalf("taking the confirmation back restores not-evidenced: %v", err)
	}
}

func TestGaps_UnsupportedHitNeedsReview(t *testing.T) {
	e := newTenv(t)
	n := seedReport(t, e, gapSeed{company: "Example Corp", role: "Engineer", key: "k:1", must: []Item{{ID: "m1", Label: "Spark", Hit: 0, Evidence: "I think so"}}, unsupported: []string{"m1"}})
	got := gapOf(t, e.analyze(e.gaps(nil), n), "spark")
	if got.State != GapNeedsReview || !got.Sources[0].Unsupported {
		t.Fatalf("a hit that named no profile source needs review: %+v", got)
	}
}

func TestGaps_InferredRequirementsAreListedApartAndNotCounted(t *testing.T) {
	e := newTenv(t)
	n := seedReport(t, e, gapSeed{company: "Example Corp", role: "Engineer", key: "k:1", must: []Item{miss("m1", "Rust")}, hidden: []Item{miss("h1", "Leadership of a team"), miss("h2", "Rust")}})
	rep := e.analyze(e.gaps(nil), n)
	if _, ok := rep.gap("leadership of a team"); ok {
		t.Fatal("an inferred signal must not become a gap")
	}
	if got := gapOf(t, rep, "rust"); got.Required != 1 || got.Preferred != 0 || len(got.Sources) != 1 {
		t.Fatalf("inferred items do not count as demand: %+v", got)
	}
	if len(rep.Inferred) != 2 || rep.Inferred[0].Label != "Leadership of a team" {
		t.Fatalf("inferred = %+v", rep.Inferred)
	}
}

func TestGaps_RejectedApplicationsAddNoWeight(t *testing.T) {
	e := newTenv(t)
	n := seedReport(t, e, gapSeed{company: "Example Corp", role: "Engineer", key: "k:1", must: []Item{miss("m1", "Rust")}})
	before := e.analyze(e.gaps(nil), n)
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO tracker_entries (date, company, role, status, identity_key, report_number, created_at, updated_at) VALUES ('2026-10-01', 'Example Corp', 'Engineer', 'rejected', 'k:1', ?, '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`, n)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	after := e.analyze(e.gaps(nil), n)
	b, _ := json.Marshal(before.Gaps)
	a, _ := json.Marshal(after.Gaps)
	if string(a) != string(b) {
		t.Fatalf("a rejection changed the gaps:\n%s\n%s", b, a)
	}
}

func TestGaps_SelectionIsBounded(t *testing.T) {
	e := newTenv(t)
	g := e.gaps(nil)
	var many []int64
	for i := int64(1); i <= MaxGapReports+1; i++ {
		many = append(many, i)
	}
	for name, nums := range map[string][]int64{"none": nil, "too many": many, "not positive": {0}} {
		if _, _, err := g.Start(context.Background(), "tester", "k-"+name, nums); !errors.Is(err, ErrGapInvalid) {
			t.Errorf("%s: err = %v, want ErrGapInvalid", name, err)
		}
	}
	var runs int
	if err := e.st.Read().QueryRow(`SELECT count(*) FROM runs WHERE kind = 'gap-report'`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("a refused request left %d runs", runs)
	}
}

func TestGaps_StartIsIdempotentUnderTheRunContract(t *testing.T) {
	e := newTenv(t)
	n := seedReport(t, e, gapSeed{company: "Example Corp", role: "Engineer", key: "k:1", must: []Item{miss("m1", "Rust")}})
	g := e.gaps(nil)
	id, replayed, err := g.Start(context.Background(), "tester", "same", []int64{n})
	if err != nil || replayed {
		t.Fatalf("first start: %v replayed=%v", err, replayed)
	}
	again, replayed, err := g.Start(context.Background(), "tester", "same", []int64{n})
	if err != nil || !replayed || again != id {
		t.Fatalf("second start: %q replayed=%v err=%v, want %q replayed", again, replayed, err, id)
	}
	if _, _, err := g.Start(context.Background(), "tester", "same", []int64{n, n + 1}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("the same key with another selection = %v, want a conflict", err)
	}
	run, err := e.st.GetRun(context.Background(), id)
	if err != nil || run.Status != "succeeded" || run.Kind != "gap-report" {
		t.Fatalf("run = %+v, %v", run, err)
	}
	if list, err := g.List(context.Background()); err != nil || len(list) != 1 || list[0].ID != id || list[0].Gaps != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}
}

func confirmedPlan(t *testing.T, e *tenv, g *Gaps, lookup bool, skills ...string) GapReport {
	t.Helper()
	var items []Item
	for i, s := range skills {
		items = append(items, miss(fmt.Sprintf("m%d", i), s))
	}
	n := seedReport(t, e, gapSeed{company: "Example Corp", role: "Engineer", key: "k:" + strings.Join(skills, ""), must: items})
	rep := e.analyze(g, n)
	for _, s := range skills {
		var err error
		if rep, err = g.Confirm(context.Background(), rep.ID, s, true); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := g.BuildPlan(context.Background(), rep.ID, skills, lookup)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestGaps_UnavailableResourceLookupListsNoLinks(t *testing.T) {
	e := newTenv(t)
	rep := confirmedPlan(t, e, e.gaps(nil), true, "Rust")
	if rep.Plan.Lookup.Status != LookupUnavailable || rep.Plan.Lookup.Reason == "" {
		t.Fatalf("lookup = %+v", rep.Plan.Lookup)
	}
	it := rep.Plan.Items[0]
	if len(it.Resources) != 0 || it.Exercise == "" || it.Criterion == "" || !strings.HasPrefix(it.Effort, "Estimate:") {
		t.Fatalf("item = %+v", it)
	}
	if strings.Contains(it.Exercise+it.Criterion+it.Effort, "http") {
		t.Fatalf("a plan without a research source must not carry a link: %+v", it)
	}
}

func TestGaps_ResourcesAreVerifiedAndAtMostTwo(t *testing.T) {
	e := newTenv(t)
	f := &fakeFinder{found: []Resource{
		{Title: "Unchecked", URL: "https://example.com/a"}, {Title: "One", URL: "https://example.com/b", Verified: true},
		{Title: "Two", URL: "https://example.com/c", Verified: true}, {Title: "Three", URL: "https://example.com/d", Verified: true}}}
	rep := confirmedPlan(t, e, e.gaps(f), true, "Rust")
	got := rep.Plan.Items[0].Resources
	if len(got) != 2 || got[0].Title != "One" || got[1].Title != "Two" || rep.Plan.Lookup.Status != LookupDone {
		t.Fatalf("resources = %+v, lookup %+v", got, rep.Plan.Lookup)
	}
	none := confirmedPlan(t, e, e.gaps(f), false, "Zig")
	if len(f.asked) != 1 || none.Plan.Lookup.Status != LookupNotRequested || len(none.Plan.Items[0].Resources) != 0 {
		t.Fatalf("a plan that did not ask must not look anything up: asked %v, %+v", f.asked, none.Plan)
	}
}

func TestGaps_FailedLookupIsReportedNotInvented(t *testing.T) {
	e := newTenv(t)
	rep := confirmedPlan(t, e, e.gaps(&fakeFinder{err: errors.New("budget exhausted")}), true, "Rust")
	if rep.Plan.Lookup.Status != LookupFailed || len(rep.Plan.Items[0].Resources) != 0 {
		t.Fatalf("plan = %+v", rep.Plan)
	}
}

func TestGaps_PlanCoversAtMostFiveGaps(t *testing.T) {
	e := newTenv(t)
	g := e.gaps(nil)
	skills := []string{"Rust", "Zig", "Ada", "Lua", "Elixir", "Haskell"}
	var items []Item
	for i, s := range skills {
		items = append(items, miss(fmt.Sprintf("m%d", i), s))
	}
	n := seedReport(t, e, gapSeed{company: "Example Corp", role: "Engineer", key: "k:many", must: items})
	rep := e.analyze(g, n)
	for _, s := range skills {
		rep, _ = g.Confirm(context.Background(), rep.ID, s, true)
	}
	if _, err := g.BuildPlan(context.Background(), rep.ID, skills, false); !errors.Is(err, ErrGapInvalid) {
		t.Fatalf("six gaps = %v, want ErrGapInvalid", err)
	}
	if _, err := g.BuildPlan(context.Background(), rep.ID, skills[:5], false); err != nil {
		t.Fatal(err)
	}
}

// dump summarizes every table outside the gap tables, so a test can show that
// recording progress touched nothing else.
func (e *tenv) dump() string {
	e.t.Helper()
	var b strings.Builder
	rows, err := e.st.Read().Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'gap_%' ORDER BY name`)
	if err != nil {
		e.t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		names = append(names, n)
	}
	rows.Close()
	for _, n := range names {
		if n == "runs" || n == "idempotency_keys" {
			continue
		}
		var c int
		_ = e.st.Read().QueryRow(`SELECT count(*) FROM ` + n).Scan(&c)
		fmt.Fprintf(&b, "%s=%d\n", n, c)
	}
	return b.String()
}

func TestGaps_CompletingAnExerciseChangesNoProfileFact(t *testing.T) {
	e := newTenv(t)
	if err := os.MkdirAll(filepath.Join(e.root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(e.root, "config", "profile.yml")
	if err := os.WriteFile(profile, []byte("version: 1\nskills: [go]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := e.gaps(nil)
	rep := confirmedPlan(t, e, g, false, "Rust")
	before, fileBefore := e.dump(), mustRead(t, profile)
	rep, err := g.SetProgress(context.Background(), rep.ID, "rust", true)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Plan.Items[0].Done || len(rep.Done) != 1 {
		t.Fatalf("progress not recorded: %+v", rep)
	}
	if got := gapOf(t, rep, "rust"); got.State != GapConfirmed {
		t.Fatalf("finishing the exercise must not close the gap: %q", got.State)
	}
	if after := e.dump(); after != before {
		t.Fatalf("progress changed other tables:\n%s\n%s", before, after)
	}
	if mustRead(t, profile) != fileBefore {
		t.Fatal("progress rewrote the profile")
	}
	if _, err := g.SetProgress(context.Background(), rep.ID, "zig", true); !errors.Is(err, ErrGapInvalid) {
		t.Fatalf("progress on a skill outside the plan = %v", err)
	}
	if rep, err = g.SetProgress(context.Background(), rep.ID, "rust", false); err != nil || rep.Plan.Items[0].Done {
		t.Fatalf("reopening: %+v %v", rep, err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGaps_AnalysisWritesOnlyGapTablesAndCallsNoModel(t *testing.T) {
	e := newTenv(t)
	n := seedReport(t, e, gapSeed{company: "Example Corp", role: "Engineer", key: "k:1", must: []Item{miss("m1", "Rust")}})
	before := e.dump()
	e.analyze(e.gaps(nil), n)
	if after := e.dump(); after != before {
		t.Fatalf("the analysis changed other tables:\n%s\n%s", before, after)
	}
}
