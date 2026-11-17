package api

import (
	"slices"
	"strings"
	"testing"

	"applysmith/internal/api/wire"
	"applysmith/internal/evaluate"
)

func evaluatedReport(t *testing.T, h *harness, marker string) int {
	t.Helper()
	out := h.evaluate(marker)
	if out["reportNumber"] == nil {
		t.Fatalf("no report: %v", out)
	}
	return int(out["reportNumber"].(float64))
}

func TestReportCatalogFiltersAndPages(t *testing.T) {
	h := newDomainHarness(t)
	for _, m := range []string{"one", "two", "three"} {
		evaluatedReport(t, h, m)
	}
	var page wire.Success[wire.ReportPage]
	r := h.get("/api/reports?pageSize=2")
	strictInto(t, r.body, &page)
	if r.StatusCode != 200 || page.Data.Total != 3 || len(page.Data.Reports) != 2 || page.Data.PageSize != 2 {
		t.Fatalf("catalog: %s", r.body)
	}
	if page.Data.Reports[0].Number < page.Data.Reports[1].Number {
		t.Errorf("newest first expected: %d then %d", page.Data.Reports[0].Number, page.Data.Reports[1].Number)
	}
	rec := page.Data.Reports[0].Recommendation
	byRec := h.get("/api/reports?recommendation=" + rec)
	if byRec.StatusCode != 200 || byRec.data()["total"].(float64) == 0 {
		t.Errorf("recommendation %s: %s", rec, byRec.body)
	}
	for _, x := range byRec.data()["reports"].([]any) {
		if x.(map[string]any)["recommendation"] != rec {
			t.Errorf("filter leaked %v", x)
		}
	}
	if none := h.get("/api/reports?scoreMin=4.99&scoreMax=5"); none.data()["total"].(float64) != 0 || none.data()["reports"] == nil {
		t.Errorf("empty range: %s", none.body)
	}
	if byCompany := h.get("/api/reports?company=example"); byCompany.data()["total"].(float64) != 3 {
		t.Errorf("company: %s", byCompany.body)
	}
	// Scores that run against the report order: the oldest scores highest.
	if err := h.srv.store.Load().Write(t.Context(), func(tx *sqlTx) error {
		_, err := tx.Exec(`UPDATE report_index SET score = CASE report_number WHEN (SELECT min(report_number) FROM report_index) THEN 4.5
			WHEN (SELECT max(report_number) FROM report_index) THEN 3.5 ELSE 2 END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var byScore wire.Success[wire.ReportPage]
	strictInto(t, h.get("/api/reports?sort=score_desc").body, &byScore)
	var scores []float64
	for _, x := range byScore.Data.Reports {
		if x.Score != nil {
			scores = append(scores, *x.Score)
		}
	}
	if !slices.Equal(scores, []float64{4.5, 3.5, 2}) {
		t.Errorf("score order: %v", scores)
	}
}

// Home's and the week funnel's evaluated count is postings, each once; the
// list it opens holds one report per posting, the newest, so the two agree
// when a posting has two reports.
func TestReportCatalogSince_ListsWhatTheEvaluatedCountCounts(t *testing.T) {
	h := newDomainHarness(t)
	first := evaluatedReport(t, h, "one")
	evaluatedReport(t, h, "two")
	again := first + 100
	if err := h.srv.store.Load().Write(t.Context(), func(tx *sqlTx) error {
		if _, err := tx.Exec(`INSERT INTO report_artifacts (report_number, kind, path, status, content_hash, created_at, updated_at)
			VALUES (?, 'report', 'reports/again.md', 'ready', 'h', 'x', 'x')`, again); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO report_index (report_number, kind, company, role, posting_url, identity_key, score, score_kind, recommendation, legitimacy,
			verification, role_family, markdown_path, html_path, job_id, documents_eligible, created_at) SELECT ?, kind, company, role, posting_url, identity_key, score,
			score_kind, recommendation, legitimacy, verification, role_family, 'reports/again.md', 'reports/again.html', job_id, documents_eligible, created_at
			FROM report_index WHERE report_number = ?`, again, first)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	week := decode[wire.FunnelWeek](t, h.get("/api/discovery/funnel/week"))
	page := decode[wire.ReportPage](t, h.get("/api/reports?since="+week.Since))
	var listed []int
	for _, r := range page.Reports {
		listed = append(listed, int(r.Number))
	}
	if int(page.Total) != int(week.Current.Evaluated) || len(listed) != 2 || !slices.Contains(listed, again) || slices.Contains(listed, first) {
		t.Errorf("evaluated %d, since lists %v (total %d), want the newest of each posting", week.Current.Evaluated, listed, page.Total)
	}
}

func TestReportCatalogRejectsBadQueries(t *testing.T) {
	h := newDomainHarness(t)
	for name, q := range map[string]string{
		"page size bound": "pageSize=500", "band": "recommendation=maybe", "since": "since=today",
	} {
		if r := h.get("/api/reports?" + q); r.StatusCode != 422 || r.errCode() != "validation_failed" {
			t.Errorf("%s: %d %s", name, r.StatusCode, r.body)
		}
	}
}

func TestOneReportHasIndexHeaderAndMarkdown(t *testing.T) {
	h := newDomainHarness(t)
	n := evaluatedReport(t, h, "detail")
	r := h.get("/api/reports/" + itoa(n))
	var env wire.Success[wire.ReportDetail]
	strictInto(t, r.body, &env)
	d := env.Data
	if r.StatusCode != 200 || int(d.Report.Number) != n || d.Report.Kind != "final" || d.Header["report"] != "000"+itoa(n) ||
		d.Header["recommendation"] != d.Report.Recommendation || !strings.Contains(d.Content, "## Verdict") || !strings.Contains(d.Content, evaluate.FakeNote) {
		t.Fatalf("report: %s", r.body)
	}
	if missing := h.get("/api/reports/99999"); missing.StatusCode != 404 || missing.errCode() != "not_found" {
		t.Errorf("unknown report: %d %s", missing.StatusCode, missing.body)
	}
	for _, bad := range []string{"abc", "0"} {
		if r := h.get("/api/reports/" + bad); r.StatusCode != 422 {
			t.Errorf("number %q: %d %s", bad, r.StatusCode, r.body)
		}
	}
}

// An HTML report is built from captured posting text, so it is served only to
// a caller with a credential, by number, with the sandbox policy.
func TestHTMLReportCarriesTheSandboxPolicy(t *testing.T) {
	h := newDomainHarness(t)
	n := evaluatedReport(t, h, "html")
	r := h.get("/api/reports/" + itoa(n) + "/html")
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if got := r.Header.Get("Content-Security-Policy"); got != "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:" {
		t.Errorf("Content-Security-Policy = %q", got)
	}
	if ct := r.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cd := r.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "inline;") || !strings.Contains(cd, "report-000"+itoa(n)+".html") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if r.Header.Get("X-Content-Type-Options") != "nosniff" || r.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("security headers missing: %v", r.Header)
	}
	if !strings.Contains(string(r.body), "<html") || strings.Contains(string(r.body), "<script") {
		t.Errorf("body:\n%.300s", r.body)
	}
	if anon := h.do(call{path: "/api/reports/" + itoa(n) + "/html"}); anon.StatusCode != 401 || anon.errCode() != "unauthenticated" {
		t.Errorf("without a credential: %d %s", anon.StatusCode, anon.body)
	}
	if anon := h.do(call{path: "/api/reports/" + itoa(n)}); anon.StatusCode != 401 {
		t.Errorf("markdown without a credential: %d", anon.StatusCode)
	}
	if missing := h.get("/api/reports/99999/html"); missing.StatusCode != 404 || !strings.HasPrefix(missing.Header.Get("Content-Type"), "application/json") {
		t.Errorf("missing html report: %d %v", missing.StatusCode, missing.Header)
	}
}

func TestReportFilesAreNeverServedByPath(t *testing.T) {
	h := newDomainHarness(t)
	n := evaluatedReport(t, h, "path")
	for _, p := range []string{"/api/reports/../reports/0001.md", "/api/reports/" + itoa(n) + "/../../.applysmith/applysmith.db", "/reports/" + itoa(n) + ".md"} {
		r := h.get(p)
		if strings.Contains(string(r.body), "## Verdict") || strings.HasPrefix(string(r.body), "SQLite format") {
			t.Errorf("%s served a workspace file: %d %.60s", p, r.StatusCode, r.body)
		}
	}
}
