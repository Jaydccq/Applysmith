package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"applysmith/internal/api/wire"
	"applysmith/internal/discovery"
	"applysmith/internal/evaluate"
	"applysmith/internal/llm"
)

const portalsYAML = `version: 1
filters:
  title:
    positive: [engineer]
    negative: [intern]
companies:
  - name: Example Tracked
    ats: greenhouse
    slug: example-tracked
`

// atsBoards serves synthetic Greenhouse and Lever boards.
func atsBoards(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/v1/boards/example-tracked/jobs", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{"jobs": []map[string]any{
			{"id": 4001, "title": "Software Engineer", "location": map[string]any{"name": "Austin, TX"}, "absolute_url": "https://job-boards.greenhouse.io/example-tracked/jobs/4001", "updated_at": "2026-10-09T10:00:00Z"},
			{"id": 4002, "title": "Engineering Intern", "location": map[string]any{"name": "Austin, TX"}, "absolute_url": "https://job-boards.greenhouse.io/example-tracked/jobs/4002"},
			{"id": 4003, "title": "Office Manager", "location": map[string]any{"name": "Austin, TX"}, "absolute_url": "https://job-boards.greenhouse.io/example-tracked/jobs/4003"},
		}})
	})
	mux.HandleFunc("/v1/boards/example-tracked/jobs/4001", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{"id": 4001, "title": "Software Engineer", "company_name": "Example Tracked", "location": map[string]any{"name": "Austin, TX"},
			"content": "<p>Example Tracked builds scheduling software for clinics. You will design and build backend services in Go, own their reliability in production, and work with a small team on a shared roadmap. 2+ years of experience required.</p>"})
	})
	mux.HandleFunc("/v0/postings/example-lever", func(w http.ResponseWriter, r *http.Request) {
		write(w, []map[string]any{{"id": "5d1c0a9e-0000-4000-8000-000000000001", "text": "Platform Engineer", "hostedUrl": "https://jobs.lever.co/example-lever/5d1c0a9e-0000-4000-8000-000000000001",
			"createdAt": time.Now().Add(-24 * time.Hour).UnixMilli(), "categories": map[string]any{"location": "Austin, TX"}, "descriptionPlain": "Run the platform."}})
	})
	mux.HandleFunc("/list.csv", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ats,slug\nashby,seeded-co\nbogus,nope\n"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (te *testEnv) discovery(srv *httptest.Server, args ...string) output {
	te.t.Helper()
	var out, errw syncBuffer
	e := te.env(&out, &errw)
	e.scanEndpoints = discovery.Endpoints{}
	for _, a := range discovery.ATSNames {
		e.scanEndpoints[a] = srv.URL
	}
	fake := llm.NewFake()
	fake.SetGenerator(evaluate.FakeGenerator)
	e.llmOptions = []llm.Option{llm.WithFake(fake), llm.WithOfflineSimulation()}
	code := e.run(context.Background(), append(append([]string{}, args...), "--workspace", te.ws))
	return output{code, out.String(), errw.String()}
}

func TestScanCLI_AgainstSyntheticBoards(t *testing.T) {
	te := newTestEnv(t)
	srv := atsBoards(t)
	if o := te.run(context.Background(), "init", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("init: %+v", o)
	}
	if err := os.WriteFile(filepath.Join(te.ws, "portals.yml"), []byte(portalsYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	must := func(args ...string) output {
		t.Helper()
		o := te.discovery(srv, args...)
		if o.code != 0 {
			t.Fatalf("applysmith %v: code %d stderr %q stdout %q", args, o.code, o.stderr, o.stdout)
		}
		return o
	}

	if o := must("scan", "sources"); !strings.Contains(o.stdout, "registry") {
		t.Errorf("scan sources:\n%s", o.stdout)
	}
	// --json prints the GET /api/scan-sources envelope, not the table.
	var sources wire.ScanSourceList
	if o := must("scan", "sources", "--json"); json.Unmarshal([]byte(o.stdout), &sources) != nil || len(sources.Sources) != len(discovery.Sources) {
		t.Errorf("scan sources --json:\n%s", o.stdout)
	}
	if o := must("scan", "runs"); !strings.Contains(o.stdout, "no scan runs yet") {
		t.Errorf("scan runs:\n%s", o.stdout)
	}
	list := must("scan", "--list")
	for _, s := range discovery.Sources {
		if !strings.Contains(list.stdout, s.Name) {
			t.Errorf("--list lacks %s:\n%s", s.Name, list.stdout)
		}
	}
	if !regexpLine(list.stdout, "theirstack", "needs key") || !regexpLine(list.stdout, "registry", "ready") || !regexpLine(list.stdout, "indeed", "needs searches") {
		t.Errorf("--list readiness:\n%s", list.stdout)
	}

	if o := must("registry", "add", "https://jobs.lever.co/example-lever/5d1c0a9e-0000-4000-8000-000000000001"); !strings.Contains(o.stdout, "lever:example-lever: 1 new") {
		t.Errorf("registry add: %s", o.stdout)
	}
	if o := must("registry", "seed", "--source", srv.URL+"/list.csv", "--licence", "MIT"); !strings.Contains(o.stdout, "1 rows, 1 new, 1 rejected") {
		t.Errorf("registry seed: %s", o.stdout)
	}
	if o := must("registry", "stats"); !strings.Contains(o.stdout, "active 2") {
		t.Errorf("registry stats: %s", o.stdout)
	}

	o := must("scan", "tracked", "--top-n", "1")
	if !strings.Contains(o.stdout, "inserted 1") || !strings.Contains(o.stdout, "queued 1") || !strings.Contains(o.stdout, "filtered:title") {
		t.Errorf("scan tracked:\n%s", o.stdout)
	}
	// The registry scan reads the lever board and the seeded board (404, struck), and the tracked company again.
	o = must("scan", "registry")
	if !strings.Contains(o.stdout, "duplicates") {
		t.Errorf("scan registry:\n%s", o.stdout)
	}

	var page wire.InboxPage
	listed := must("inbox", "list", "--json").stdout
	if err := json.Unmarshal([]byte(listed), &page); err != nil || len(page.Rows) != 2 || page.Total != 2 {
		t.Fatalf("inbox page %+v err %v", page, err)
	}
	assertAPIShape(t, listed)
	rows := page.Rows
	var queued, tracked wire.InboxRow
	for _, r := range rows {
		// The scan evaluates what it queued before it returns.
		if r.Status == discovery.InboxEvaluated {
			queued = r
		}
		if r.Company == "Example Tracked" {
			tracked = r
		}
	}
	if queued.ID == 0 || tracked.ID == 0 || tracked.RequiredYears == nil || *tracked.RequiredYears != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if o := must("inbox", "show", "1"); !strings.Contains(o.stdout, "identity:") {
		t.Errorf("inbox show: %s", o.stdout)
	}
	if o := must("inbox", "add", "https://jobs.lever.co/example-lever/5d1c0a9e-0000-4000-8000-000000000001?utm_source=x"); !strings.Contains(o.stdout, "not added: already known to the inbox") {
		t.Errorf("inbox add duplicate: %s", o.stdout)
	}
	if o := must("inbox", "export", "--format", "csv"); !strings.Contains(o.stdout, "id,status,score,company") || !strings.Contains(o.stdout, "Platform Engineer") {
		t.Errorf("export: %s", o.stdout)
	}

	// A cut list says so; a mistyped status or id is named.
	if o := must("inbox", "list", "--limit", "1"); !strings.Contains(o.stdout, "1 of 2 rows") {
		t.Errorf("inbox list --limit 1: %s", o.stdout)
	}
	if o := te.discovery(srv, "inbox", "list", "--status", "pendng"); o.code != 2 || !strings.Contains(o.stderr, "dismissed, evaluated") {
		t.Errorf("bad inbox status: %+v", o)
	}
	if o := te.discovery(srv, "inbox", "show", "99"); !strings.Contains(o.stderr, "no inbox row 99; `applysmith inbox list`") {
		t.Errorf("missing inbox row: %+v", o)
	}
	must("blocked", "add", "Example Tracked", "--reason", "test")
	if o := must("blocked", "list"); !strings.Contains(o.stdout, "Example Tracked") {
		t.Errorf("blocked list: %s", o.stdout)
	}
	must("inbox", "dismiss", "1")
	if o := must("scan", "--runs", "--limit", "5"); !strings.Contains(o.stdout, "tracked") || !strings.Contains(o.stdout, "completed") {
		t.Errorf("scan --runs: %s", o.stdout)
	}

	// Refusals.
	if o := te.discovery(srv, "scan", "registry", "--scope", "moon"); o.code != 2 || !strings.Contains(o.stderr, "valid scopes") {
		t.Errorf("bad scope: %+v", o)
	}
	if o := te.discovery(srv, "scan", "linkedin"); o.code != 0 || !strings.Contains(o.stdout, "skipped") {
		t.Errorf("a browser source with nothing to search is skipped: %+v", o)
	}
	if o := te.discovery(srv, "scan"); o.code != 2 {
		t.Errorf("usage: %+v", o)
	}
	if o := te.discovery(srv, "scan", "linkdin"); o.code != 2 || !strings.Contains(o.stderr, `"linkedin"`) {
		t.Errorf("a mistyped source names the nearest: %+v", o)
	}
	if o := te.discovery(srv, "scan", "moonboard"); o.code != 2 || !strings.Contains(o.stderr, "registry, tracked, ") {
		t.Errorf("unknown source: %+v", o)
	}
}

// A fresh workspace has no boards in its registry and no tracked company, so
// the registry scan is skipped; the command line names the command that
// downloads the lists (job-discovery, "Empty registry").
func TestScanCLI_AnEmptyRegistryNamesTheSeedCommand(t *testing.T) {
	te := newTestEnv(t)
	if o := te.run(context.Background(), "init", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("init: %+v", o)
	}
	if o := te.run(context.Background(), "scan", "registry", "--workspace", te.ws); o.code != 0 || !strings.Contains(o.stdout, "skipped") || !strings.Contains(o.stdout, "`applysmith registry seed`") {
		t.Fatalf("scan registry: %+v", o)
	}
	// With the app open the scan runs in it, as a dashboard scan does (its
	// record carries the server's run), and the runs list reads beside it.
	te.startServe()
	o := te.run(context.Background(), "scan", "registry", "--json", "--workspace", te.ws)
	var rec wire.ScanRecord
	if o.code != 0 || json.Unmarshal([]byte(o.stdout), &rec) != nil || rec.RunID == "" || rec.Status != discovery.RunSkipped {
		t.Fatalf("scan beside the server: %+v", o)
	}
	var runs wire.ScanList
	o = te.run(context.Background(), "scan", "runs", "--json", "--workspace", te.ws)
	if o.code != 0 || json.Unmarshal([]byte(o.stdout), &runs) != nil || runs.Total != 2 {
		t.Fatalf("scan runs beside the server: %+v", o)
	}
}

// The single-row discovery writes and the lists work with the app open; a
// flag the subcommand does not take is a usage error that writes nothing.
func TestDiscoveryCLI_RowCommandsWorkBesideARunningServer(t *testing.T) {
	te := newTestEnv(t)
	ctx := context.Background()
	run := func(args ...string) output { return te.run(ctx, append(args, "--workspace", te.ws)...) }
	if o := run("init"); o.code != 0 {
		t.Fatalf("init: %+v", o)
	}
	// The "no key yet" choice gives every task a model, so promote reaches the
	// gate rather than the no-model refusal.
	choice := filepath.Join(t.TempDir(), "llm.json")
	if err := os.WriteFile(choice, []byte(`{"offline":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if o := run("setup", "apply", "llm", "--file", choice); o.code != 0 {
		t.Fatalf("setup llm: %+v", o)
	}
	te.startServe()
	if o := run("inbox", "add", "https://job-boards.greenhouse.io/example/jobs/1"); o.code != 0 {
		t.Fatalf("inbox add: %+v", o)
	}
	status := func() string {
		var row wire.InboxRow
		if o := run("inbox", "show", "1", "--json"); json.Unmarshal([]byte(o.stdout), &row) != nil {
			t.Fatalf("inbox show: %+v", o)
		}
		return row.Status
	}
	if o := run("inbox", "dismiss", "1", "--limit", "3"); o.code != 2 || status() != discovery.InboxPending {
		t.Fatalf("a flag dismiss does not take: %+v, row %s", o, status())
	}
	var row wire.InboxRow
	if o := run("inbox", "dismiss", "1", "--json"); o.code != 0 || json.Unmarshal([]byte(o.stdout), &row) != nil || row.Status != discovery.InboxDismissed {
		t.Fatalf("inbox dismiss --json: %+v", o)
	}
	for _, args := range [][]string{{"blocked", "add", "Example Corp"}, {"blocked", "list"}, {"blocked", "remove", "Example Corp"}, {"leads", "list"}, {"registry", "stats"},
		{"registry", "add", "https://jobs.lever.co/example-lever/1"}, {"registry", "block", "lever:example-lever"}, {"registry", "revive", "lever:example-lever"}} {
		if o := run(args...); o.code != 0 {
			t.Errorf("%v beside the server: %+v", args, o)
		}
	}
	// promote --top asks the server; a blocked company keeps the row out, so
	// no evaluation starts.
	var added inboxAdded
	if o := run("inbox", "add", "https://job-boards.greenhouse.io/example/jobs/2", "--json"); o.code != 0 || json.Unmarshal([]byte(o.stdout), &added) != nil || added.Row == nil {
		t.Fatalf("inbox add: %+v", o)
	}
	run("blocked", "add", added.Row.Company)
	if o := run("inbox", "promote", "--top", "3"); o.code != 0 || !strings.Contains(o.stdout, "queued 0 rows for evaluation (1 rejected by the gate)") {
		t.Errorf("promote --top beside the server: %+v", o)
	}
}

// registry refresh runs (here with every download failing, so no network is
// used); it once fell through to the usage, its code sitting under blocked.
func TestRegistryCLI_RefreshRuns(t *testing.T) {
	te := newTestEnv(t)
	var out, errw syncBuffer
	e := te.env(&out, &errw)
	e.scanHTTP = &http.Client{Transport: &http.Transport{Proxy: func(*http.Request) (*url.URL, error) { return nil, errors.New("offline") }}}
	if code := e.run(context.Background(), []string{"registry", "refresh", "--workspace", te.ws}); code != 1 || strings.Contains(errw.String(), "usage:") {
		t.Errorf("registry refresh: %d %q", code, errw.String())
	}
}

func TestInboxPromoteCLI_EvaluatesInProcessAndEndsTheRow(t *testing.T) {
	te := newTestEnv(t)
	srv := atsBoards(t)
	if o := te.run(context.Background(), "init", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("init: %+v", o)
	}
	add := te.discovery(srv, "inbox", "add", "https://job-boards.greenhouse.io/example-tracked/jobs/4001")
	if add.code != 0 {
		t.Fatalf("add: %+v", add)
	}
	o := te.discovery(srv, "inbox", "promote", "1")
	if o.code != 0 {
		t.Fatalf("promote: %+v", o)
	}
	var row wire.InboxRow
	show := te.discovery(srv, "inbox", "show", "1", "--json").stdout
	if err := json.Unmarshal([]byte(show), &row); err != nil {
		t.Fatal(err)
	}
	assertAPIShape(t, show)
	if row.Status != discovery.InboxEvaluated {
		t.Fatalf("row is %q after promote; nothing else would run its evaluation", row.Status)
	}
	if reports, _ := filepath.Glob(filepath.Join(te.ws, "reports", "0001-*.md")); len(reports) != 1 {
		t.Fatalf("reports = %v", reports)
	}
}

func TestScanCLI_RecordsTheEvaluationOutcomesOnTheRun(t *testing.T) {
	te := newTestEnv(t)
	srv := atsBoards(t)
	if o := te.run(context.Background(), "init", "--workspace", te.ws); o.code != 0 {
		t.Fatalf("init: %+v", o)
	}
	if err := os.WriteFile(filepath.Join(te.ws, "portals.yml"), []byte(portalsYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	o := te.discovery(srv, "scan", "tracked", "--top-n", "1", "--json")
	if o.code != 0 {
		t.Fatalf("scan: %+v", o)
	}
	var runs wire.ScanList
	out := te.discovery(srv, "scan", "--runs", "--json").stdout
	if err := json.Unmarshal([]byte(out), &runs); err != nil || len(runs.Scans) != 1 || runs.Total != 1 {
		t.Fatalf("runs %v %v", runs, err)
	}
	if got := runs.Scans[0].Counts.Outcomes[discovery.OutcomeReported]; got != 1 {
		t.Fatalf("outcomes = %v", runs.Scans[0].Counts.Outcomes)
	}
	assertAPIShape(t, out)
}

func regexpLine(out, first, contains string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, first) && strings.Contains(line, contains) {
			return true
		}
	}
	return false
}

// assertAPIShape checks a --json output against the rules of the HTTP API's
// wire types: lowerCamelCase keys and no zero time written out.
func assertAPIShape(t *testing.T, out string) {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				if k == "" || strings.Contains(k, "_") || unicode.IsUpper(rune(k[0])) {
					t.Errorf("%s: key %q is not lowerCamelCase", path, k)
				}
				walk(path+"."+k, val)
			}
		case []any:
			for i, val := range x {
				walk(fmt.Sprintf("%s[%d]", path, i), val)
			}
		case string:
			if strings.HasPrefix(x, "0001-01-01") {
				t.Errorf("%s: zero time %q", path, x)
			}
		}
	}
	walk("$", v)
}
