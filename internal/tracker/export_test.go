package tracker

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
)

func (f *fixture) export(format string, q ListQuery) string {
	f.t.Helper()
	var buf bytes.Buffer
	if err := f.tr.Export(f.ctx, &buf, format, q); err != nil {
		f.t.Fatal(err)
	}
	return buf.String()
}

func TestExport_CSV_FormulaGuard(t *testing.T) {
	f := newFixture(t)
	e := f.add(AddRow{Company: "Example Corp", Role: "Engineer", URL: "https://example.com/1"})
	f.exec(`UPDATE tracker_entries SET notes = ?, company = ? WHERE id = ?`, `=HYPERLINK("https://example.net","click")`, "@Evil Corp", e.ID)
	recs, err := csv.NewReader(strings.NewReader(f.export(FormatCSV, ListQuery{}))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || len(recs[0]) != len(exportHeader) {
		t.Fatalf("%v", recs)
	}
	row := map[string]string{}
	for i, h := range recs[0] {
		row[h] = recs[1][i]
	}
	if row["notes"] != `'=HYPERLINK("https://example.net","click")` || row["company"] != "'@Evil Corp" {
		t.Errorf("%q %q", row["notes"], row["company"])
	}
	if row["status"] != "evaluated" || row["resume"] != "no" || row["id"] != "1" {
		t.Errorf("%v", row)
	}
}

func TestExport_CarriesWhenWhereAndThroughWhat(t *testing.T) {
	f := newFixture(t)
	e := f.add(AddRow{Company: "Example Corp", Role: "Engineer", URL: "https://example.com/1", Location: "Lisbon, Portugal", Source: "referral"})
	f.setStatus(e.ID, Rejected, ActorUser)
	f.exec(`UPDATE tracker_entries SET applied_at = '2027-01-02', archived_at = '2027-02-01T00:00:00Z' WHERE id = ?`, e.ID)
	recs, _ := csv.NewReader(strings.NewReader(f.export(FormatCSV, ListQuery{Archived: true}))).ReadAll()
	row := map[string]string{}
	for i, h := range recs[0] {
		row[h] = recs[1][i]
	}
	if row["applied_at"] != "2027-01-02" || row["location"] != "Lisbon, Portugal" || row["source"] != "referral" || row["archived_at"] != "2027-02-01T00:00:00Z" {
		t.Errorf("%v", row)
	}
}
