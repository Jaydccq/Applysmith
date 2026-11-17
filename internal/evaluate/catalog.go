// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"applysmith/internal/store"
)

// QueryError is a report query the caller got wrong; Field names the
// parameter.
type QueryError struct{ Field, Message string }

func (e *QueryError) Error() string { return e.Field + ": " + e.Message }

// ErrReportFile means the index names a report file that is not there or not
// a plain file inside the workspace.
var ErrReportFile = errors.New("evaluate: the report file is not available")

// Report catalog sorts.
const (
	SortDateDesc  = "date_desc"
	SortScoreDesc = "score_desc"
)

const (
	defaultReportPage = 25
	maxReportPage     = 100
	maxCompanyChars   = 200
	maxReportBytes    = 4 << 20
)

// ReportQuery filters the catalog. Zero values mean no filter; Page and
// PageSize default to 1 and 25.
type ReportQuery struct {
	ScoreMin, ScoreMax *float64
	Recommendation     string
	Legitimacy         string
	Company            string
	// From and To are local midnights, a zero one no bound: reports created
	// on or after From's day and on or before To's, whole local days.
	From, To time.Time
	// Since, a local midnight, keeps the newest report of each posting
	// evaluated (a report or a screening, gated apart) on or after it: the
	// postings Home's and the week funnel's evaluated counts count, each once.
	Since time.Time
	// Undecided keeps the reports a tracker row still holds as evaluated:
	// each posting's current report, not yet applied to, skipped or discarded.
	Undecided      bool
	Sort           string
	Page, PageSize int
}

// IndexRow is one row of the report index.
type IndexRow struct {
	Number            int64
	Kind              string
	IdentityKey       string
	Company, Role     string
	PostingURL        string
	Score             *float64
	ScoreKind         string
	Recommendation    string
	Legitimacy        string
	Verification      string
	RoleFamily        string
	CreatedAt         time.Time
	DocumentsEligible bool
	// TrackerStatus is the status of the posting's tracker row, the one
	// holding this report or, for an earlier report, the posting's key; empty
	// when the tracker has no row for it.
	TrackerStatus string
	markdownPath  string
	htmlPath      string
}

// ReportPage is one page of the catalog.
type ReportPage struct {
	Rows           []IndexRow
	Page, PageSize int
	Total          int
}

func (q ReportQuery) validate() (ReportQuery, error) {
	for field, v := range map[string]*float64{"scoreMin": q.ScoreMin, "scoreMax": q.ScoreMax} {
		if v != nil && (*v < 0 || *v > 5) {
			return q, &QueryError{field, "must be between 0.0 and 5.0"}
		}
	}
	if q.Recommendation != "" && !slices.Contains([]string{"skip", "consider", "apply", "strong"}, q.Recommendation) {
		return q, &QueryError{"recommendation", "must be skip, consider, apply or strong"}
	}
	if q.Legitimacy != "" && !slices.Contains([]string{"credible", "unclear", "doubtful", "unassessed"}, q.Legitimacy) {
		return q, &QueryError{"legitimacy", "must be credible, unclear, doubtful or unassessed"}
	}
	if len([]rune(q.Company)) > maxCompanyChars {
		return q, &QueryError{"company", fmt.Sprintf("holds at most %d characters", maxCompanyChars)}
	}
	switch q.Sort {
	case "":
		q.Sort = SortDateDesc
	case SortDateDesc, SortScoreDesc:
	default:
		return q, &QueryError{"sort", "must be date_desc or score_desc"}
	}
	if q.Page == 0 {
		q.Page = 1
	}
	if q.PageSize == 0 {
		q.PageSize = defaultReportPage
	}
	if q.Page < 1 {
		return q, &QueryError{"page", "must be 1 or more"}
	}
	if q.PageSize < 1 || q.PageSize > maxReportPage {
		return q, &QueryError{"pageSize", fmt.Sprintf("must be between 1 and %d", maxReportPage)}
	}
	return q, nil
}

func (q ReportQuery) where() (string, []any) {
	conds, args := []string{"1 = 1"}, []any{}
	if q.ScoreMin != nil {
		conds, args = append(conds, "score >= ?"), append(args, *q.ScoreMin)
	}
	if q.ScoreMax != nil {
		conds, args = append(conds, "score <= ?"), append(args, *q.ScoreMax)
	}
	if q.Recommendation != "" {
		conds, args = append(conds, "recommendation = ?"), append(args, q.Recommendation)
	}
	if q.Legitimacy != "" {
		conds, args = append(conds, "legitimacy = ?"), append(args, q.Legitimacy)
	}
	if c := strings.TrimSpace(q.Company); c != "" {
		conds, args = append(conds, "instr(lower(company), lower(?)) > 0"), append(args, c)
	}
	if !q.From.IsZero() {
		conds, args = append(conds, "created_at >= ?"), append(args, q.From.UTC().Format(time.RFC3339))
	}
	if !q.To.IsZero() {
		// The next local midnight, so a day across a daylight-saving change
		// keeps its 23 or 25 hours.
		conds, args = append(conds, "created_at < ?"), append(args, q.To.AddDate(0, 0, 1).UTC().Format(time.RFC3339))
	}
	if !q.Since.IsZero() {
		conds = append(conds, `report_number IN (SELECT max(r.report_number) FROM report_index r JOIN evaluation_jobs j ON j.run_id = r.job_id
			WHERE j.outcome IN ('reported', 'screened') AND j.finished_at >= ? AND r.kind <> 'gated' GROUP BY j.identity_key)`)
		args = append(args, q.Since.UTC().Format(time.RFC3339))
	}
	if q.Undecided {
		conds = append(conds, `report_number IN (SELECT report_number FROM tracker_entries
			WHERE status = 'evaluated' AND archived_at IS NULL AND report_number IS NOT NULL)`)
	}
	return strings.Join(conds, " AND "), args
}

const indexColumns = `report_number, kind, company, role, posting_url, identity_key, score, score_kind, recommendation, legitimacy, verification,
	role_family, markdown_path, html_path, documents_eligible, created_at,
	coalesce((SELECT status FROM tracker_entries WHERE id = (SELECT id FROM tracker_entries WHERE report_number = report_index.report_number
		UNION ALL SELECT id FROM tracker_entries WHERE identity_key = report_index.identity_key
		UNION ALL SELECT entry_id FROM tracker_aliases WHERE identity_key = report_index.identity_key LIMIT 1)), '')`

type rowScanner interface{ Scan(...any) error }

func scanIndexRow(r rowScanner) (IndexRow, error) {
	var x IndexRow
	var score sql.NullFloat64
	var eligible int
	var created string
	if err := r.Scan(&x.Number, &x.Kind, &x.Company, &x.Role, &x.PostingURL, &x.IdentityKey, &score, &x.ScoreKind, &x.Recommendation, &x.Legitimacy,
		&x.Verification, &x.RoleFamily, &x.markdownPath, &x.htmlPath, &eligible, &created, &x.TrackerStatus); err != nil {
		return IndexRow{}, err
	}
	if score.Valid {
		x.Score = &score.Float64
	}
	x.DocumentsEligible = eligible == 1
	x.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return x, nil
}

// ListReports pages the report index.
func ListReports(ctx context.Context, st *store.Store, q ReportQuery) (ReportPage, error) {
	q, err := q.validate()
	if err != nil {
		return ReportPage{}, err
	}
	where, args := q.where()
	page := ReportPage{Page: q.Page, PageSize: q.PageSize}
	db := st.Read()
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM report_index WHERE `+where, args...).Scan(&page.Total); err != nil {
		return ReportPage{}, err
	}
	order := "created_at DESC, report_number DESC"
	if q.Sort == SortScoreDesc {
		order = "score IS NULL, score DESC, report_number DESC"
	}
	rows, err := db.QueryContext(ctx, `SELECT `+indexColumns+` FROM report_index WHERE `+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`,
		append(args, q.PageSize, (q.Page-1)*q.PageSize)...)
	if err != nil {
		return ReportPage{}, err
	}
	defer rows.Close()
	page.Rows = []IndexRow{}
	for rows.Next() {
		x, err := scanIndexRow(rows)
		if err != nil {
			return ReportPage{}, err
		}
		page.Rows = append(page.Rows, x)
	}
	return page, rows.Err()
}

// ReportRef is a report found by number.
type ReportRef struct {
	Row  IndexRow
	root string
}

// FindReport returns the index row of a report number, or store.ErrNotFound.
func FindReport(ctx context.Context, st *store.Store, root string, number int64) (ReportRef, error) {
	x, err := scanIndexRow(st.Read().QueryRowContext(ctx, `SELECT `+indexColumns+` FROM report_index WHERE report_number = ?`, number))
	if errors.Is(err, sql.ErrNoRows) {
		return ReportRef{}, store.ErrNotFound
	}
	return ReportRef{Row: x, root: root}, err
}

// Markdown reads the report's Markdown file.
func (r ReportRef) Markdown() ([]byte, error) { return r.read(r.Row.markdownPath) }

// HTML reads the report's rendered page.
func (r ReportRef) HTML() ([]byte, error) { return r.read(r.Row.htmlPath) }

// read opens a file the index names. The path comes from the database, so it
// must be a plain relative path under reports/ and the file a regular file.
func (r ReportRef) read(rel string) ([]byte, error) {
	slash := filepath.ToSlash(rel)
	if name, ok := strings.CutPrefix(slash, "reports/"); !ok || name == "" || strings.Contains(name, "/") || !filepath.IsLocal(rel) {
		return nil, ErrReportFile
	}
	path := filepath.Join(r.root, filepath.FromSlash(rel))
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrReportFile
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrReportFile
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxReportBytes))
	if err != nil {
		return nil, ErrReportFile
	}
	return b, nil
}
