package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"applysmith/internal/api/wire"
	"applysmith/internal/discovery"
	"applysmith/internal/evaluate"
	"applysmith/internal/llm"
	"applysmith/internal/mcpserver"
	"applysmith/internal/signals"
)

const exitProfile = `version: 1
candidate:
  full_name: Alex Example
  email: alex@example.org
work_authorization:
  authorized_countries: [US]
targets:
  roles:
    - title: Software Engineer
  locations: [United States]
preferences:
  work_modes: [remote]
`

const exitPortals = `version: 1
companies:
  - name: Example Corp
    ats: greenhouse
    slug: examplecorp
sources:
  signals-alerts:
    enabled: true
    queries:
      - url: https://alerts.example.test/alerts/feeds/1/2
`

const exitFeed = `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><entry><title>Example Corp raises $14M</title>
<link href="https://news.example.test/a"/><published>2026-01-01T08:00:00Z</published>
<content>Example Corp raised $14M. The company plans to hire 20 engineers this year.</content></entry></feed>`

var exitDocID = regexp.MustCompile(`document id=(\d+)`)

// exitBrain answers the two signal tasks the way a model reading the feed
// entry would, and leaves every other task to the evaluation fixtures.
func exitBrain(task, prompt string) (string, bool) {
	switch task {
	case "feed-classify":
		return `{"company":"Example Corp"}`, true
	case "signal-assess":
		m := exitDocID.FindStringSubmatch(prompt)
		if m == nil {
			return "", false
		}
		return fmt.Sprintf(`{"event_type":"funding","hiring_intent":"strong","affected_teams":["engineering"],"use_of_funds_growth":true,"scale":"explicit_headcount","strength":8,"confidence":0.9,"reasoning":"r","evidence_quotes":[{"document_id":%s,"quote":"plans to hire 20 engineers this year"}]}`, m[1]), true
	}
	return evaluate.FakeGenerator(task, prompt)
}

// exitWeb is every outside service of the check in one local server: a
// Greenhouse board for the registry scan, a Greenhouse board for the signal
// company, and the alert feed.
func exitWeb(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/boards/example-co/jobs", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"jobs":[{"id":41,"title":"Software Engineer","location":{"name":"Remote - United States"},"company_name":"Example Co","absolute_url":"https://job-boards.greenhouse.io/example-co/jobs/41","updated_at":"2026-01-01T00:00:00Z"}],"meta":{"total":1}}`)
	})
	mux.HandleFunc("/v1/boards/example-co/jobs/41", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":41,"content":"<p>Build backend services in Go for Example Co.</p>"}`)
	})
	mux.HandleFunc("/v1/boards/examplecorp/jobs", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"jobs":[{"id":7,"title":"Software Engineer","location":{"name":"Remote - US"},"absolute_url":"https://boards.greenhouse.io/examplecorp/jobs/7","content":"Build things."}]}`)
	})
	mux.HandleFunc("/alerts/feeds/1/2", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		fmt.Fprint(w, exitFeed)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// toServer sends every request to one local server, keeping the host the
// caller asked for.
type toServer struct{ target *url.URL }

func (s toServer) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.URL.Scheme, r.URL.Host = s.target.Scheme, s.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

// TestPhase6Exit_SourceToReportSignalToInboxAndReadOnlyMCP is
// the evidence for the Phase 6 exit check (task 6.9). One server over a
// temporary workspace with the fake model and local fakes for every outside
// service. It walks a registry scan into the inbox, through promotion and
// evaluation into a report and a tracker row; a hiring signal from an alert
// feed through assessment and the posting check into the inbox and the leads
// store; and the MCP server over the real API (read-only tools, a write
// refused). The remote boundaries are TestRemote_PrimaryListenerRefusesTunnelTraffic,
// TestRemote_PrimaryListenerNeverBindsAllInterfaces and the remote route tests.
func TestPhase6Exit_SourceToReportSignalToInboxAndReadOnlyMCP(t *testing.T) {
	web := exitWeb(t)
	target, _ := url.Parse(web.URL)
	fake := llm.NewFake()
	fake.SetGenerator(exitBrain)
	r := newRemoteRig(t, func(c *Config) {
		c.Domain.LLM = append(c.Domain.LLM, llm.WithFake(fake))
		c.Domain.Reader = immediateReader{text: postingBody("exit")}
		c.Domain.ScanEndpoints = discovery.Endpoints{discovery.Greenhouse: web.URL}
		c.Domain.SignalEndpoints = signals.Endpoints{ATS: discovery.Endpoints{discovery.Greenhouse: web.URL}}
		c.Domain.ScanHTTP = &http.Client{Transport: toServer{target}}
	})
	h := r.harness
	d := h.domainOf()
	ctx := h.srv.bg

	// 1. A registry scan fills the inbox; promoting the row evaluates it into a
	// report and a tracker row.
	if _, err := d.repo.RegistryAdd(ctx, "test", discovery.Board{ATS: discovery.Greenhouse, Slug: "example-co", Company: "Example Co"}); err != nil {
		t.Fatal(err)
	}
	scan := h.startScan("key-exit-scan-1", map[string]any{"source": "registry", "topN": 0})
	if scan.StatusCode != http.StatusAccepted {
		t.Fatalf("scan: %d %s", scan.StatusCode, scan.body)
	}
	if run := h.waitRun(scan.data()["runId"].(string)); run["status"] != "succeeded" {
		t.Fatalf("scan run: %v", run)
	}
	var page wire.Success[wire.InboxPage]
	strictInto(t, h.get("/api/inbox").body, &page)
	if page.Data.Total != 1 || page.Data.Rows[0].Company != "Example Co" || page.Data.Rows[0].Status != wire.InboxPending {
		t.Fatalf("inbox after the scan: %+v", page.Data)
	}
	promote := h.post("/api/inbox/promote", map[string]any{"ids": []int{int(page.Data.Rows[0].ID)}})
	if promote.StatusCode != 200 {
		t.Fatalf("promote: %d %s", promote.StatusCode, promote.body)
	}
	waitFor(t, "the promoted row to be evaluated", func() bool {
		n, _ := h.get("/api/inbox?status=evaluated").data()["total"].(float64)
		return n == 1
	})
	entries := h.get("/api/candidacies").data()["entries"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["reportNumber"] == nil {
		t.Fatalf("tracker after the evaluation: %v", entries)
	}
	report := int(entries[0].(map[string]any)["reportNumber"].(float64))
	if rep := h.get("/api/reports/" + itoa(report)); rep.StatusCode != 200 || !strings.Contains(rep.data()["content"].(string), "Verdict") {
		t.Fatalf("report %d: %d %s", report, rep.StatusCode, rep.body)
	}

	// 2. A hiring signal: the alert feed entry becomes an event, the fake model
	// assesses it with a verbatim quote, the company's board is checked against
	// the profile, and the posting is handed to the inbox with a lead.
	if err := writeFile(h.ws.Root+"/profile.yml", exitProfile); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(h.ws.Root+"/portals.yml", exitPortals); err != nil {
		t.Fatal(err)
	}
	tick := h.send("POST", "/api/signals/ticks", map[string]any{}, "Idempotency-Key", freshKey())
	if tick.StatusCode != http.StatusAccepted {
		t.Fatalf("tick: %d %s", tick.StatusCode, tick.body)
	}
	if run := h.waitRun(tick.data()["runId"].(string)); run["status"] != "succeeded" {
		t.Fatalf("tick run: %v", run)
	}
	st := h.get("/api/signals/status")
	last, _ := st.data()["lastTick"].(map[string]any)
	if last == nil || last["status"] != "completed" || last["ingested"] != float64(1) || last["events"] != float64(1) {
		t.Fatalf("signal status: %s", st.body)
	}
	if tiers, _ := st.data()["tiers"].(map[string]any); tiers["hot"] != float64(1) {
		t.Fatalf("the company is not hot: %s", st.body)
	}
	list := h.get("/api/signals?tier=hot").data()["rows"].([]any)
	if len(list) != 1 || !strings.HasPrefix(list[0].(map[string]any)["company"].(string), "Example") {
		t.Fatalf("hot signals: %v", list)
	}
	db := h.srv.store.Load().Read()
	var company, detail string
	if err := db.QueryRow(`SELECT company, detail FROM inbox WHERE source = 'signals'`).Scan(&company, &detail); err != nil || !strings.HasPrefix(company, "Example") || !strings.Contains(detail, "news.example.test") {
		t.Fatalf("the signal handoff in the inbox: %q %q %v", company, detail, err)
	}
	var leads wire.Success[wire.LeadPage]
	strictInto(t, h.get("/api/leads").body, &leads)
	if leads.Data.Total != 1 || !strings.HasPrefix(leads.Data.Rows[0].Company, "Example") || leads.Data.Rows[0].Category != discovery.LeadPosting {
		t.Fatalf("leads after the handoff: %+v", leads.Data)
	}
	// Nothing was applied: the signal pipeline only fills the inbox and leads.
	var claims int
	if err := db.QueryRow(`SELECT count(*) FROM submissions`).Scan(&claims); err != nil || claims != 0 {
		t.Errorf("submission claims = %d, %v", claims, err)
	}

	// 3. The MCP server over the real API lists read-only tools, reads the
	// report, and a write attempt is refused with no change.
	api := mcpserver.NewClient(mcpserver.Options{
		Origin: func() (string, error) { return h.srv.Origin(), nil },
		Token:  func() (string, error) { return h.token(), nil },
	})
	srv := mcpserver.New(api, "exit", nil)
	sT, cT := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, sT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "exit-host", Version: "0"}, nil).Connect(ctx, cT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		a := tool.Annotations
		if a == nil || !a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint {
			t.Errorf("tool %s is not annotated read-only: %+v", tool.Name, a)
		}
	}
	slices.Sort(names)
	if want := []string{"apply_list_status", "get_application_by_url", "get_report", "health", "list_candidacies"}; !slices.Equal(names, want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	got, err := cs.CallTool(callCtx, &mcp.CallToolParams{Name: "get_report", Arguments: map[string]any{"number": report}})
	if err != nil || got.IsError {
		t.Fatalf("get_report: %v %+v", err, got)
	}
	for _, write := range []string{"submit_application", "update_candidacy", "start_scan", "delete_report"} {
		if _, err := cs.CallTool(callCtx, &mcp.CallToolParams{Name: write, Arguments: map[string]any{}}); err == nil {
			t.Errorf("the write tool %s was accepted", write)
		}
	}
	if n := len(h.get("/api/candidacies").data()["entries"].([]any)); n != 1 {
		t.Errorf("tracker rows after the write attempts = %d", n)
	}
}
