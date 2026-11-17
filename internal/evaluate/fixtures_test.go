// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func body(md string) string {
	_, rest, _ := strings.Cut(md, "\n\n")
	return rest
}

func TestFixtures_FakeOutputIsLabeled(t *testing.T) {
	e := newTenv(t)
	res := e.run(req("fl"))
	md := e.readMarkdown(res.ReportNumber)
	verdict := strings.SplitN(strings.SplitN(md, "## Verdict", 2)[1], "## Snapshot", 2)[0]
	if !strings.Contains(verdict, FakeNote) {
		t.Fatalf("verdict:\n%s", verdict)
	}
}

func TestFixtures_ScriptedRepairSucceedsAfterExactlyOneRepair(t *testing.T) {
	bad := deepReply(deepOpts{})
	bad = strings.Replace(bad, `"hit_level":1`, `"hit_level":0.7`, 1)
	e := newTenv(t, deepFixture("Marker rp.", bad, deepReply(deepOpts{})))
	res := e.run(req("rp"))
	if res.Outcome != OutcomeReported || e.ledgerCalls("deep-eval") != 2 {
		t.Fatalf("result %+v, deep calls %d", res, e.ledgerCalls("deep-eval"))
	}
}

func TestFixtures_PersistentInvalidOutputFailsWithoutFallbackOrReport(t *testing.T) {
	bad := strings.Replace(deepReply(deepOpts{}), `"hit_level":1`, `"hit_level":0.7`, 1)
	e := newTenv(t, deepFixture("Marker pi.", bad, bad, deepReply(deepOpts{})))
	res := e.run(req("pi"))
	if res.Outcome != OutcomeFailed || res.Reason != "invalid-structured-output" {
		t.Fatalf("result = %+v", res)
	}
	if e.ledgerCalls("deep-eval") != 2 || e.count("report_index") != 0 || len(e.entries()) != 0 {
		t.Fatalf("calls %d, reports %d", e.ledgerCalls("deep-eval"), e.count("report_index"))
	}
}

func TestFixtures_ModelSuppliedScoreIsIgnored(t *testing.T) {
	extra := map[string]any{"score": 5.0, "recommendation": "strong", "midpoint": 4.9, "penalty": 0, "caps": []string{}}
	e := newTenv(t, deepFixture("Marker ms.", deepReply(deepOpts{must: []float64{1, 0, 1}, extra: extra})))
	res := e.run(req("ms"))
	want := Compute(ScoreInput{Must: []float64{1, 0, 1}, Comp: 3, MustMisses: 1})
	if res.Outcome != OutcomeReported || *res.Score != want.Score || res.Recommendation == RecStrong {
		t.Fatalf("result %+v, want score %v", res, want.Score)
	}
	var detail string
	_ = e.st.Read().QueryRow(`SELECT detail_json FROM evaluation_jobs WHERE run_id = ?`, res.RunID).Scan(&detail)
	if !strings.Contains(detail, "ignored_fields") {
		t.Fatalf("the ignored fields must be logged: %s", detail)
	}
}

func TestFixtures_AtLeastOneMustHaveIsRequired(t *testing.T) {
	empty := strings.Replace(deepReply(deepOpts{}), `"must_have":[`, `"must_have":[],"ignored":[`, 1)
	e := newTenv(t, deepFixture("Marker nm.", empty, empty))
	if res := e.run(req("nm")); res.Reason != "invalid-structured-output" {
		t.Fatalf("result = %+v", res)
	}
}

func TestPrompts_OverrideReplacesTheEmbeddedTextAndIsRecorded(t *testing.T) {
	dir := t.TempDir()
	e := newTenv(t, deepFixture("OVERRIDE-MARK", deepReply(deepOpts{})))
	e.psetDir = dir
	e.rebuild()
	rec := &recorder{inner: e.router}
	e.model = rec
	e.rebuild()
	tmpl := "Task: deep-eval\nOVERRIDE-MARK {{.Profile.CV}} {{.Posting.Title}} {{.Sponsor.Stance}}\n<job_description>{{.Posting.Text}}</job_description>\n"
	if err := os.WriteFile(filepath.Join(dir, "deep-eval.tmpl"), []byte(tmpl), 0o600); err != nil {
		t.Fatal(err)
	}
	res := e.run(req("ov"))
	if res.Outcome != OutcomeReported || !strings.Contains(rec.prompts[0], "OVERRIDE-MARK Alex Example") {
		t.Fatalf("result %+v prompts %q", res, rec.prompts)
	}
	var detail string
	_ = e.st.Read().QueryRow(`SELECT detail_json FROM evaluation_jobs WHERE run_id = ?`, res.RunID).Scan(&detail)
	if !strings.Contains(detail, `"override":true`) || !strings.Contains(detail, `"hash":"`) {
		t.Fatalf("detail = %s", detail)
	}
}

func TestPrompts_BrokenOverrideFailsNamingTheFileWithNoFallback(t *testing.T) {
	dir := t.TempDir()
	e := newTenv(t)
	e.psetDir = dir
	e.rebuild()
	if err := os.WriteFile(filepath.Join(dir, "deep-eval.tmpl"), []byte("{{.Posting.Text}} only"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := e.run(req("bo"))
	if res.Outcome != OutcomeFailed || res.Reason != ReasonPrompt {
		t.Fatalf("result = %+v", res)
	}
	var detail string
	_ = e.st.Read().QueryRow(`SELECT detail_json FROM evaluation_jobs WHERE run_id = ?`, res.RunID).Scan(&detail)
	if !strings.Contains(detail, "prompts/deep-eval.tmpl") || !strings.Contains(detail, "required field") {
		t.Fatalf("the error must name the file and the problem: %s", detail)
	}
	if e.ledgerCalls("deep-eval") != 0 || e.count("report_index") != 0 {
		t.Fatal("nothing may run on a broken override")
	}
}
