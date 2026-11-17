package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/evaluate"
	"applysmith/internal/store"
)

// htmlReportPolicy is the policy an HTML report is served under: the page is
// built from captured posting text, so nothing in it may run or load.
const htmlReportPolicy = "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:"

func toReportSummary(x evaluate.IndexRow) wire.ReportSummary {
	return wire.ReportSummary{
		Number: apikit.Safe64(x.Number), Kind: x.Kind, Company: x.Company, Role: x.Role, PostingURL: x.PostingURL, Score: x.Score,
		ScoreKind: x.ScoreKind, Recommendation: x.Recommendation, Legitimacy: x.Legitimacy, Verification: x.Verification,
		CreatedAt: wire.Time{Time: x.CreatedAt.UTC()}, DocumentsEligible: x.DocumentsEligible,
		TrackerStatus: x.TrackerStatus,
	}
}

// reportError maps a catalog failure onto the API errors.
func reportError(err error) error {
	var qe *evaluate.QueryError
	switch {
	case errors.As(err, &qe):
		return apikit.InvalidField(qe.Field, "%s %s", qe.Field, qe.Message)
	case errors.Is(err, store.ErrNotFound):
		return apikit.NewErr(wire.CodeNotFound, "no such report")
	case errors.Is(err, evaluate.ErrReportFile):
		return apikit.NewErr(wire.CodeNotFound, "the report file is not available")
	}
	return err
}

func (s *Server) listReports(c *apikit.Call) (apikit.Reply, error) {
	st, err := s.db()
	if err != nil {
		return apikit.Reply{}, err
	}
	q := apikit.QueryOf(c)
	page, size := q.Paging(25)
	rq := evaluate.ReportQuery{
		ScoreMin: q.Float("scoreMin"), ScoreMax: q.Float("scoreMax"), Recommendation: q.Str("recommendation"),
		Legitimacy: q.Str("legitimacy"), Company: q.Str("company"), From: q.Day("from"), To: q.Day("to"), Since: q.Day("since"),
		Undecided: q.Boolean("undecided"), Sort: q.Str("sort"), Page: page, PageSize: size,
	}
	if q.Err != nil {
		return apikit.Reply{}, q.Err
	}
	result, err := evaluate.ListReports(c.R.Context(), st, rq)
	if err != nil {
		return apikit.Reply{}, reportError(err)
	}
	out := wire.ReportPage{Reports: make([]wire.ReportSummary, len(result.Rows)), Page: apikit.Safe(result.Page), PageSize: apikit.Safe(result.PageSize), Total: apikit.Safe(result.Total)}
	for i, x := range result.Rows {
		out.Reports[i] = toReportSummary(x)
	}
	return apikit.Reply{Data: out}, nil
}

// findReport resolves the {number} path value to a report.
func (s *Server) findReport(c *apikit.Call) (evaluate.ReportRef, error) {
	st, err := s.db()
	if err != nil {
		return evaluate.ReportRef{}, err
	}
	n, err := strconv.ParseInt(c.R.PathValue("number"), 10, 64)
	if err != nil || n < 1 || n > wire.MaxSafeInt {
		return evaluate.ReportRef{}, apikit.InvalidField("number", "number must be a positive whole number")
	}
	ref, err := evaluate.FindReport(c.R.Context(), st, s.cfg.Workspace.Root, n)
	if err != nil {
		return evaluate.ReportRef{}, reportError(err)
	}
	return ref, nil
}

func (s *Server) getReport(c *apikit.Call) (apikit.Reply, error) {
	ref, err := s.findReport(c)
	if err != nil {
		return apikit.Reply{}, err
	}
	md, err := ref.Markdown()
	if err != nil {
		return apikit.Reply{}, reportError(err)
	}
	return apikit.Reply{Data: wire.ReportDetail{Report: toReportSummary(ref.Row), Header: evaluate.ParseHeader(string(md)), Content: string(md)}}, nil
}

// getReportHTML writes the rendered report itself: an HTML document is not a
// JSON envelope. It is served only by number, with an explicit type, a
// download name and the sandbox policy.
func (s *Server) getReportHTML(c *apikit.Call) (apikit.Reply, error) {
	ref, err := s.findReport(c)
	if err != nil {
		return apikit.Reply{}, err
	}
	page, err := ref.HTML()
	if err != nil {
		return apikit.Reply{}, reportError(err)
	}
	h := c.W.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Disposition", fmt.Sprintf(`inline; filename="report-%04d.html"`, ref.Row.Number))
	h.Set("Content-Security-Policy", htmlReportPolicy)
	h.Set("Content-Length", strconv.Itoa(len(page)))
	c.W.WriteHeader(http.StatusOK)
	_, _ = c.W.Write(page)
	c.Handled = true
	return apikit.Reply{}, nil
}
