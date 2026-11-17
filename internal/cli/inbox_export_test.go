package cli

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/api/wire"
	"applysmith/internal/discovery"
)

func TestExportInbox_CSVGuardsScrapedCellsAgainstFormulas(t *testing.T) {
	var rows []discovery.InboxRow
	for _, lead := range []string{"=", "+", "-", "@"} {
		rows = append(rows, discovery.InboxRow{
			ID: 1, Status: "inbox", Company: lead + "Example Corp", Role: lead + "Engineer", Location: lead + "Exampleton",
			Source: "greenhouse", URL: lead + "https://example.com/jobs/1",
		})
	}
	var buf bytes.Buffer
	if err := exportInbox(&buf, "csv", rows); err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(strings.NewReader(buf.String())).ReadAll()
	if err != nil || len(recs) != len(rows)+1 {
		t.Fatalf("%v %d", err, len(recs))
	}
	header := recs[0]
	for _, rec := range recs[1:] {
		for i, name := range header {
			switch name {
			case "company", "role", "location", "url":
				if !strings.HasPrefix(rec[i], "'") {
					t.Errorf("%s cell %q can run as a formula", name, rec[i])
				}
			}
		}
	}
}

// An unknown format is refused before the file is touched, and the JSON export
// carries the API's inbox rows.
func TestInboxExport_RefusesAnUnknownFormatBeforeWriting(t *testing.T) {
	te := newTestEnv(t)
	te.workspaceWithInboxRow()
	out := filepath.Join(t.TempDir(), "inbox.json")
	if err := os.WriteFile(out, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := te.run(context.Background(), "inbox", "export", "--out", out, "--format", "xml", "--workspace", te.ws)
	if b, _ := os.ReadFile(out); o.code != 2 || string(b) != "keep" || !strings.Contains(o.stderr, `unknown export format "xml"`) {
		t.Fatalf("xml export = %+v; file %q", o, b)
	}
	if o := te.run(context.Background(), "inbox", "export", "--out", out, "--format", "json", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("json export = %+v", o)
	}
	b, _ := os.ReadFile(out)
	var rows []wire.InboxRow
	if err := json.Unmarshal(b, &rows); err != nil || len(rows) != 1 || rows[0].URL == "" {
		t.Fatalf("export %s: %v", b, err)
	}
	assertAPIShape(t, string(b))
}
