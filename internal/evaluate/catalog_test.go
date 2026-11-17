// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"applysmith/internal/store"
)

// catalogEnv writes three reports with different companies.
func catalogEnv(t *testing.T) *tenv {
	t.Helper()
	e := newTenv(t)
	for _, c := range []string{"Acme Labs", "Example Systems", "Zeta Works"} {
		res := e.run(req("cat-"+strings.ToLower(c[:3]), func(r *Request) { r.Company = c }))
		if res.Outcome != OutcomeReported {
			t.Fatalf("%s: %+v", c, res)
		}
	}
	return e
}

func TestReportCatalog_ListFiltersSortsAndPages(t *testing.T) {
	e := catalogEnv(t)
	ctx := context.Background()
	all, err := ListReports(ctx, e.st, ReportQuery{})
	if err != nil || all.Total != 3 || len(all.Rows) != 3 || all.Page != 1 || all.PageSize != 25 {
		t.Fatalf("all = %+v, %v", all, err)
	}
	if all.Rows[0].Number < all.Rows[2].Number {
		t.Fatalf("default order is not newest first: %d..%d", all.Rows[0].Number, all.Rows[2].Number)
	}
	byCompany, _ := ListReports(ctx, e.st, ReportQuery{Company: "acme"})
	if byCompany.Total != 1 || byCompany.Rows[0].Company != "Acme Labs" {
		t.Fatalf("company filter = %+v", byCompany)
	}
	first, _ := ListReports(ctx, e.st, ReportQuery{Page: 1, PageSize: 2})
	second, _ := ListReports(ctx, e.st, ReportQuery{Page: 2, PageSize: 2})
	if len(first.Rows) != 2 || len(second.Rows) != 1 || first.Total != 3 {
		t.Fatalf("paging = %d + %d of %d", len(first.Rows), len(second.Rows), first.Total)
	}
	byScore, _ := ListReports(ctx, e.st, ReportQuery{Sort: SortScoreDesc})
	for i := 1; i < len(byScore.Rows); i++ {
		a, b := byScore.Rows[i-1].Score, byScore.Rows[i].Score
		if a != nil && b != nil && *a < *b {
			t.Fatalf("score order broken at %d", i)
		}
	}
	rec := all.Rows[0].Recommendation
	byRec, _ := ListReports(ctx, e.st, ReportQuery{Recommendation: rec})
	if byRec.Total == 0 {
		t.Fatalf("no report with recommendation %q", rec)
	}
	low, high := 4.99, 5.0
	none, _ := ListReports(ctx, e.st, ReportQuery{ScoreMin: &low, ScoreMax: &high})
	if none.Total != 0 || none.Rows == nil {
		t.Fatalf("empty range = %+v; rows must be an empty list", none)
	}
	// From and To are whole local days: a report late in the evening is that
	// evening's, and the 25-hour day of the fall daylight-saving change keeps
	// its last hour.
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip(err)
	}
	at := func(d, h int) time.Time { return time.Date(2026, 10, d, h, 30, 0, 0, ny) }
	for i, created := range []time.Time{at(31, 23), at(32, 23), at(33, 0)} {
		if err := e.st.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE report_index SET created_at = ? WHERE report_number = ?`, created.UTC().Format(time.RFC3339), all.Rows[2-i].Number)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	day := func(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, ny) }
	for name, c := range map[string]struct {
		q    ReportQuery
		want int64
	}{
		"the fall-back day":  {ReportQuery{From: day(32), To: day(32)}, all.Rows[1].Number},
		"to the day before":  {ReportQuery{To: day(31)}, all.Rows[2].Number},
		"from the day after": {ReportQuery{From: day(33)}, all.Rows[0].Number},
	} {
		if got, _ := ListReports(ctx, e.st, c.q); got.Total != 1 || got.Rows[0].Number != c.want {
			t.Errorf("%s: %+v, want report %d alone", name, got.Rows, c.want)
		}
	}
}

// Undecided keeps the reports whose tracker row still awaits the user: an
// applied, skipped or archived row has been decided.
func TestReportCatalog_UndecidedKeepsTheRowsAwaitingADecision(t *testing.T) {
	e := catalogEnv(t)
	ctx := context.Background()
	all, _ := ListReports(ctx, e.st, ReportQuery{})
	set := func(n int64, status string, archived bool) {
		if err := e.st.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE tracker_entries SET status = ?, archived_at = CASE WHEN ? THEN created_at END WHERE report_number = ?`, status, archived, n)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	set(all.Rows[0].Number, "applied", false)
	set(all.Rows[1].Number, "evaluated", false)
	set(all.Rows[2].Number, "evaluated", true)
	got, err := ListReports(ctx, e.st, ReportQuery{Undecided: true})
	if err != nil || got.Total != 1 || got.Rows[0].Number != all.Rows[1].Number {
		t.Fatalf("undecided = %+v, %v; want report %d alone", got.Rows, err, all.Rows[1].Number)
	}
	// Each report carries its posting's tracker status, found by the posting's
	// key once the row holds another report.
	if err := e.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE tracker_entries SET report_number = NULL WHERE report_number = ?`, all.Rows[2].Number)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	now, _ := ListReports(ctx, e.st, ReportQuery{})
	for i, want := range []string{"applied", "evaluated", "evaluated"} {
		if now.Rows[i].TrackerStatus != want {
			t.Errorf("report %d: tracker status %q, want %q", now.Rows[i].Number, now.Rows[i].TrackerStatus, want)
		}
	}
}

func TestReportCatalog_RejectsBadQueries(t *testing.T) {
	e := newTenv(t)
	over := 5.5
	for field, q := range map[string]ReportQuery{
		"pageSize":       {PageSize: 500},
		"page":           {Page: -1},
		"scoreMax":       {ScoreMax: &over},
		"recommendation": {Recommendation: "maybe"},
		"legitimacy":     {Legitimacy: "shady"},
		"company":        {Company: strings.Repeat("x", 201)},
		"sort":           {Sort: "random"},
	} {
		_, err := ListReports(context.Background(), e.st, q)
		var qe *QueryError
		if !errors.As(err, &qe) || qe.Field != field {
			t.Errorf("%s: %v", field, err)
		}
	}
}

func TestReportCatalog_ReadsFilesByNumberOnly(t *testing.T) {
	e := catalogEnv(t)
	ctx := context.Background()
	ref, err := FindReport(ctx, e.st, e.root, 1)
	if err != nil {
		t.Fatal(err)
	}
	md, err := ref.Markdown()
	if err != nil || !strings.Contains(string(md), "report: 0001") {
		t.Fatalf("markdown = %q, %v", md, err)
	}
	if page, err := ref.HTML(); err != nil || !strings.Contains(string(page), "<html") {
		t.Fatalf("html = %q, %v", page, err)
	}
	if _, err := FindReport(ctx, e.st, e.root, 99999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown number: %v", err)
	}
}

func TestReportCatalog_RefusesPathsOutsideReports(t *testing.T) {
	e := catalogEnv(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(e.root, "secret.md"), []byte("not a report"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../secret.md", "secret.md", "reports/../secret.md", "reports/sub/x.md", "/etc/passwd", "reports/", "reports/missing.md"} {
		err := e.st.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE report_index SET markdown_path = ? WHERE report_number = 1`, bad)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		ref, err := FindReport(ctx, e.st, e.root, 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ref.Markdown(); !errors.Is(err, ErrReportFile) {
			t.Errorf("%q was read: %v", bad, err)
		}
	}
	// A symbolic link inside reports/ is not a plain file.
	link := filepath.Join(e.root, "reports", "linked.md")
	if err := os.Symlink(filepath.Join(e.root, "secret.md"), link); err != nil {
		t.Skip("symbolic links are not available")
	}
	if err := e.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE report_index SET markdown_path = 'reports/linked.md' WHERE report_number = 1`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ref, err := FindReport(ctx, e.st, e.root, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ref.Markdown(); !errors.Is(err, ErrReportFile) {
		t.Errorf("a symbolic link was followed: %v", err)
	}
}
