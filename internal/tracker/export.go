package tracker

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Export formats.
const (
	FormatMarkdown = "md"
	FormatCSV      = "csv"
)

// exportHeader names every field of a row the page shows, so a shared export
// says when, where and through what each application went.
var exportHeader = []string{"id", "date", "company", "role", "location", "score", "score_kind", "status", "applied_at", "applied_by", "resume", "cover_letter", "report", "source", "archived_at", "notes", "posting_url"}

// Export writes every row the filter matches as a derived, read-only view, in
// the list's order when the filter names a Sort (a search's best matches
// first, as List puts them), else by id. Paging does not apply.
func (t *Tracker) Export(ctx context.Context, w io.Writer, format string, f ListQuery) error {
	if format != FormatMarkdown && format != FormatCSV {
		return fmt.Errorf("%w: format must be %s or %s", ErrInvalid, FormatMarkdown, FormatCSV)
	}
	sorted := f.Sort != ""
	f.Page, f.PageSize = 0, 0
	f, err := t.prepare(f)
	if err != nil {
		return err
	}
	clause, args, rank, rankArgs := f.where()
	order := "id"
	if sorted {
		order = sortOrders[f.Sort]
		if rank != "0" {
			order, args = "("+rank+") DESC, "+order, append(args, rankArgs...)
		}
	}
	entries, err := queryEntries(ctx, t.s.Read(), `SELECT `+entryColumns+` FROM tracker_entries WHERE `+clause+` ORDER BY `+order, args...)
	if err != nil {
		return err
	}
	if format == FormatCSV {
		return writeCSV(w, entries)
	}
	return writeMarkdown(w, entries)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func cells(e Entry, status string) []string {
	score, report := "", ""
	if e.Score != nil {
		score = strconv.FormatFloat(*e.Score, 'f', 1, 64)
	}
	if e.ReportNumber != nil {
		report = strconv.FormatInt(*e.ReportNumber, 10)
	}
	return []string{strconv.FormatInt(e.ID, 10), e.Date, e.Company, e.Role, e.Location, score, string(e.ScoreKind), status, e.AppliedAt, string(e.AppliedBy),
		yesNo(e.ResumePDF), yesNo(e.CoverLetter), report, e.Source, e.ArchivedAt, e.Notes, e.PostingURL}
}

// CSVSafe stops a spreadsheet from running a cell as a formula. Every CSV
// export of text that came from outside the product passes through it.
func CSVSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func writeCSV(w io.Writer, entries []Entry) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(exportHeader); err != nil {
		return err
	}
	for _, e := range entries {
		row := cells(e, string(e.Status))
		for i := range row {
			row[i] = CSVSafe(row[i])
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

var mdEscaper = strings.NewReplacer("\\", "\\\\", "|", "\\|", "\r\n", "<br>", "\n", "<br>", "\r", "<br>")

func writeMarkdown(w io.Writer, entries []Entry) error {
	var b strings.Builder
	b.WriteString("| " + strings.Join(exportHeader, " | ") + " |\n")
	b.WriteString("|" + strings.Repeat("---|", len(exportHeader)) + "\n")
	for _, e := range entries {
		row := cells(e, e.DisplayStatus())
		for i := range row {
			row[i] = mdEscaper.Replace(row[i])
		}
		b.WriteString("| " + strings.Join(row, " | ") + " |\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}
