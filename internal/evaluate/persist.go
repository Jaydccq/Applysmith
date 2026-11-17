// Clean-room: docs/provenance.md#CR-EVAL-RUBRIC

package evaluate

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"applysmith/internal/store"
	"applysmith/internal/tracker"
)

// reportOut is a report ready to be written.
type reportOut struct {
	meta      ReportMeta
	markdown  func(ReportMeta) string
	company   string
	role      string
	score     *float64
	scoreKind tracker.ScoreKind
	outcome   tracker.Outcome
	note      string
	docs      bool
}

const reportKind = "report"

// persist writes the report once. The number, both files, the artifact row,
// the index row and the tracker upsert commit together: if any step fails
// nothing remains, the number is not consumed, and no file is left behind.
func (j *jobRun) persist(ctx context.Context, ro reportOut) (int64, error) {
	e := j.e
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	company, role := strings.TrimSpace(ro.company), strings.TrimSpace(ro.role)
	ro.meta.Company, ro.meta.Role = company, role
	if company == "" {
		company = "Unknown company"
	}
	if role == "" {
		role = "Unknown role"
	}
	var htmlPath string
	art, err := e.d.Store.PublishArtifactTx(ctx, e.d.Root, reportKind,
		func(n int64) ([]store.PublishFile, error) {
			m := ro.meta
			m.Number = n
			md := ro.markdown(m)
			page, err := RenderHTML(company+" - "+role, md)
			if err != nil {
				return nil, err
			}
			mdPath := fmt.Sprintf("reports/%04d-%s-%s.md", n, Slug(ro.company), ro.meta.Date)
			htmlPath = strings.TrimSuffix(mdPath, ".md") + ".html"
			return []store.PublishFile{{Path: mdPath, Content: []byte(md)}, {Path: htmlPath, Content: page}}, nil
		},
		func(tx *sql.Tx, a store.Artifact) error {
			var score any
			if ro.score != nil {
				score = *ro.score
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO report_index
				(report_number, kind, company, role, posting_url, identity_key, score, score_kind, recommendation, legitimacy, verification, role_family, markdown_path, html_path, job_id, documents_eligible, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				a.Number, ro.meta.Kind, company, role, j.p.URL, j.key, score, ro.meta.ScoreKind, ro.meta.Rec, ro.meta.Legitimacy,
				ro.meta.Verification, ro.meta.RoleFamily, a.Path, htmlPath, j.runID, boolInt(ro.docs), e.stamp()); err != nil {
				return err
			}
			_, err := e.d.Tracker.UpsertReportIn(ctx, tx, tracker.Report{
				Number: a.Number, Date: ro.meta.Date, Posting: j.identityPosting(company, role), Aliases: j.aliases,
				Score: ro.score, Kind: ro.scoreKind, Outcome: ro.outcome, Note: ro.note, Simulated: ro.meta.Fake,
			})
			return err
		})
	if err != nil {
		return 0, e.fail(ReasonWrite, err)
	}
	return art.Number, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (j *jobRun) writeGated(ctx context.Context, failures []Failure) (Result, error) {
	if err := j.phase(ctx, PhaseWriting); err != nil {
		return Result{}, err
	}
	rules := make([]string, len(failures))
	for i, f := range failures {
		rules[i] = f.Rule
	}
	j.detail["gate"] = failures
	n, err := j.persist(ctx, reportOut{
		meta:     j.meta(KindGated, nil, string(tracker.ScoreScreening), RecSkip, LegitUnassessed),
		markdown: func(m ReportMeta) string { return GatedMarkdown(m, failures) },
		company:  j.p.Company, role: j.p.Title,
		scoreKind: tracker.ScoreScreening, outcome: tracker.OutcomeGated,
		note: "Gated: " + strings.Join(rules, ", "),
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Outcome: OutcomeGated, ReportNumber: n, Decision: "gated", Recommendation: RecSkip}, nil
}
