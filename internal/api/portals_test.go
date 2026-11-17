package api

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"applysmith/internal/api/wire"
	"applysmith/internal/discovery"
	"applysmith/internal/workspace"
)

const portalsFile = `version: 1
companies:
  - name: Example Co
    ats: greenhouse
    slug: example-co
  - name: Sample Labs
    enabled: false
    careers_url: https://jobs.lever.co/sample-labs
  - name: Plain Careers Inc
    careers_url: https://careers.example.org/jobs
    filters:
      title:
        positive: [engineer]
`

func (h *harness) writePortals(src string) {
	h.t.Helper()
	if err := os.WriteFile(h.ws.Path(workspace.FilePortals), []byte(src), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func TestPortalCompanies_JoinTheRegistryScanTimeAndJobCount(t *testing.T) {
	h := newDomainHarness(t)
	h.writePortals(portalsFile)
	repo := h.domainOf().repo
	board := discovery.Board{ATS: discovery.Greenhouse, Slug: "example-co"}
	if _, err := repo.RegistryAdd(t.Context(), "tracked", board); err != nil {
		t.Fatal(err)
	}
	if err := repo.RegistryRecord(t.Context(), board, discovery.VisitOK, 7, "etag"); err != nil {
		t.Fatal(err)
	}
	var got wire.Success[wire.PortalCompanyList]
	r := h.get("/api/portals/companies")
	strictInto(t, r.body, &got)
	if r.StatusCode != 200 || len(got.Data.Companies) != 3 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	byName := map[string]wire.PortalCompany{}
	for _, c := range got.Data.Companies {
		byName[c.Name] = c
	}
	ex := byName["Example Co"]
	if ex.Board != "greenhouse:example-co" || ex.JobCount == nil || *ex.JobCount != 7 || ex.LastScannedAt == nil || ex.Status != "active" || !ex.Enabled {
		t.Errorf("a scanned board: %+v", ex)
	}
	sample := byName["Sample Labs"]
	if sample.Board != "lever:sample-labs" || sample.JobCount != nil || sample.LastScannedAt != nil || sample.Enabled {
		t.Errorf("a careers address that names a board, never scanned: %+v", sample)
	}
	plain := byName["Plain Careers Inc"]
	if plain.Board != "" || !plain.OwnTitleFilter || plain.CareersURL == "" {
		t.Errorf("a careers page: %+v", plain)
	}
}

func TestPortalCompanies_AnInvalidFileIsReportedNotListed(t *testing.T) {
	h := newDomainHarness(t)
	h.writePortals("version: 1\ncompanies:\n  - name: Broken\n")
	if r := h.get("/api/portals/companies"); r.StatusCode != 422 || r.errCode() != string(wire.CodeConfigInvalid) {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

// "Use my target roles" is planned on the server from the files as the
// dashboard's own edits leave them, and the dashboard writes the plan through
// the revision-checked patch; afterwards there is nothing left to write. A
// profile without target roles plans nothing rather than failing, so the
// dashboard simply leaves the offer out.
func TestPortalTargetRoles_PlanWritesThroughThePatchOnce(t *testing.T) {
	h := newDomainHarness(t)
	edit := func(file string, ops ...map[string]any) {
		t.Helper()
		rev := h.get("/api/config/" + file).data()["revision"]
		if r := h.send("PATCH", "/api/config/"+file, map[string]any{"expectedRevision": rev, "ops": ops}); r.StatusCode != 200 {
			t.Fatalf("edit %s: %d %s", file, r.StatusCode, r.body)
		}
	}
	plan := func() wire.PortalTargetRoles {
		t.Helper()
		var got wire.Success[wire.PortalTargetRoles]
		r := h.get("/api/portals/target-roles")
		strictInto(t, r.body, &got)
		if r.StatusCode != 200 {
			t.Fatalf("%d %s", r.StatusCode, r.body)
		}
		return got.Data
	}
	if p := plan(); len(p.Lines) != 0 || len(p.Ops) != 0 {
		t.Fatalf("a profile without target roles plans %+v", p)
	}
	edit("profile", map[string]any{"op": "set", "path": "targets.roles", "value": `[{"title": "Data Engineer", "seniority": "mid"}]`})
	edit("portals", map[string]any{"op": "set", "path": "sources.indeed.enabled", "value": "true"})
	p := plan()
	if len(p.Lines) == 0 || len(p.Ops) == 0 || p.Revision == "" {
		t.Fatalf("plan %+v", p)
	}
	if r := h.send("PATCH", "/api/config/portals", map[string]any{"expectedRevision": p.Revision, "ops": p.Ops}); r.StatusCode != 200 {
		t.Fatalf("write: %d %s", r.StatusCode, r.body)
	}
	if again := plan(); len(again.Lines) != 0 || len(again.Ops) != 0 {
		t.Fatalf("a second plan %+v", again)
	}
}

func TestRegistryStatus_CountsByStatusTheLastSeedAndTheSwitch(t *testing.T) {
	h := newDomainHarness(t)
	repo := h.domainOf().repo
	if _, err := repo.RegistryAdd(t.Context(), "test", discovery.Board{ATS: "lever", Slug: "a-co"}, discovery.Board{ATS: "lever", Slug: "b-co"}, discovery.Board{ATS: "ashby", Slug: "c-co"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.RegistrySetStatus(t.Context(), discovery.Board{ATS: "ashby", Slug: "c-co"}, discovery.StatusBlocked); err != nil {
		t.Fatal(err)
	}
	var got wire.Success[wire.PortalRegistry]
	strictInto(t, h.get("/api/registry").body, &got)
	if got.Data.Active != 2 || got.Data.Blocked != 1 || got.Data.Total != 3 || got.Data.LastSeededAt != nil || !got.Data.Enabled {
		t.Fatalf("never seeded: %+v", got.Data)
	}
	err := h.srv.store.Load().Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO data_sources (kind, url, retrieved_at, sha256, licence) VALUES ('registry', 'https://lists.example.com/a.json', '2026-10-01T08:00:00.000Z', 'x', 'MIT')`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	h.writePortals("version: 1\nregistry:\n  enabled: false\n")
	strictInto(t, h.get("/api/registry").body, &got)
	if got.Data.LastSeededAt == nil || got.Data.LastSeededAt.Format("2006-01-02") != "2026-10-01" || got.Data.Enabled {
		t.Fatalf("seeded and switched off: %+v", got.Data)
	}
}

// detectHarness points every ATS at a server that holds one Greenhouse board.
func detectHarness(t *testing.T) *harness {
	t.Helper()
	srv := atsServer(t)
	return newDomainHarness(t, func(c *Config) {
		c.Domain.ScanEndpoints = discovery.Endpoints{}
		for _, ats := range discovery.ATSNames {
			c.Domain.ScanEndpoints[ats] = srv.URL
		}
	})
}

func detect(h *harness, input string) result {
	return h.post("/api/portals/detect", wire.PortalDetectRequest{Input: input})
}

func TestPortalDetect_ABoardAddressShowsTheBoardAndSlugBeforeSaving(t *testing.T) {
	h := detectHarness(t)
	var got wire.Success[wire.PortalDetection]
	r := detect(h, "https://job-boards.greenhouse.io/example-co/jobs/41")
	strictInto(t, r.body, &got)
	d := got.Data
	if r.StatusCode != 200 || d.Kind != "board" || len(d.Candidates) != 1 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	c := d.Candidates[0]
	if c.ATS != "greenhouse" || c.Slug != "example-co" || !c.Live || c.Jobs != 1 || d.Name != "Example Co" || c.InPortals {
		t.Errorf("candidate %+v name %q", c, d.Name)
	}
	if _, err := os.Stat(h.ws.Path(workspace.FilePortals)); err == nil {
		before, _ := os.ReadFile(h.ws.Path(workspace.FilePortals))
		detect(h, "https://job-boards.greenhouse.io/example-co")
		if after, _ := os.ReadFile(h.ws.Path(workspace.FilePortals)); string(after) != string(before) {
			t.Error("a preview wrote portals.yml")
		}
	}
	h.writePortals("version: 1\ncompanies:\n  - name: Example Co\n    ats: greenhouse\n    slug: example-co\n")
	strictInto(t, detect(h, "https://job-boards.greenhouse.io/example-co").body, &got)
	if !got.Data.Candidates[0].InPortals {
		t.Error("a board already in portals.yml is not marked")
	}
}

func TestPortalDetect_ACompanyNameOffersEveryBoardThatAnswers(t *testing.T) {
	h := detectHarness(t)
	var got wire.Success[wire.PortalDetection]
	strictInto(t, detect(h, "Example Co").body, &got)
	d := got.Data
	if d.Kind != "name" || len(d.Candidates) != 1 || d.Candidates[0].ATS+":"+d.Candidates[0].Slug != "greenhouse:example-co" {
		t.Fatalf("%+v", d)
	}
	strictInto(t, detect(h, "Nobody Here Ltd").body, &got)
	if len(got.Data.Candidates) != 0 || got.Data.Note == "" {
		t.Errorf("a name no board answers to: %+v", got.Data)
	}
}

func TestPortalDetect_AnUnknownAddressIsKeptAsACareersPage(t *testing.T) {
	h := detectHarness(t)
	var got wire.Success[wire.PortalDetection]
	strictInto(t, detect(h, "careers.example.org/jobs").body, &got)
	if got.Data.Kind != "careers_url" || got.Data.CareersURL != "https://careers.example.org/jobs" || len(got.Data.Candidates) != 0 {
		t.Fatalf("%+v", got.Data)
	}
}

func TestPortalDetect_RefusesWhatPortalsCannotHold(t *testing.T) {
	h := detectHarness(t)
	for _, in := range []string{"", "http://careers.example.org/jobs"} {
		if r := detect(h, in); r.StatusCode != 422 {
			t.Errorf("%q: %d %s", in, r.StatusCode, r.body)
		}
	}
}

// seedTransport serves the default lists and counts the requests; from the
// second list on it waits for the request to be cancelled.
type seedTransport struct {
	mu      sync.Mutex
	urls    []string
	started chan struct{}
	hold    bool
}

func (s *seedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.urls = append(s.urls, r.URL.String())
	n := len(s.urls)
	s.mu.Unlock()
	if s.hold && n == 2 {
		close(s.started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	}
	return &http.Response{StatusCode: 200, Header: http.Header{}, Request: r, Body: io.NopCloser(strings.NewReader(`["seed-one","seed-two"]`))}, nil
}

func TestSeedRegistry_RunsTheDefaultListsAsARunWithProgress(t *testing.T) {
	tr := &seedTransport{}
	h := newDomainHarness(t, func(c *Config) { c.Domain.ScanHTTP = &http.Client{Transport: tr} })
	r := h.send("POST", "/api/registry/seed", nil, "Idempotency-Key", "key-seed-0001")
	id, _ := r.data()["runId"].(string)
	if r.StatusCode != 202 || id == "" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	run := h.waitRun(id)
	if run["kind"] != "registry" || run["status"] != "succeeded" {
		t.Fatalf("run %v", run)
	}
	if len(tr.urls) != len(discovery.DefaultSeedSources()) {
		t.Errorf("%d lists read", len(tr.urls))
	}
	var st wire.Success[wire.PortalRegistry]
	strictInto(t, h.get("/api/registry").body, &st)
	if st.Data.Total != 6 || st.Data.LastSeededAt == nil {
		t.Errorf("status after the seed: %+v", st.Data)
	}
	s := h.openRunStream(id)
	s.next()
	if m := s.next(); m.name != "final" || decodeEvent[wire.RunFinal](t, m).Status != wire.RunSucceeded {
		t.Errorf("final %+v", m)
	}
}

func TestSeedRegistry_OneSeedAtATimeAndCancelStopsBetweenLists(t *testing.T) {
	tr := &seedTransport{hold: true, started: make(chan struct{})}
	h := newDomainHarness(t, func(c *Config) { c.Domain.ScanHTTP = &http.Client{Transport: tr} })
	id, _ := h.send("POST", "/api/registry/seed", nil, "Idempotency-Key", "key-seed-0002").data()["runId"].(string)
	<-tr.started
	if r := h.send("POST", "/api/registry/seed", nil, "Idempotency-Key", "key-seed-0003"); r.StatusCode != 409 {
		t.Errorf("a second seed while one runs: %d %s", r.StatusCode, r.body)
	}
	if r := h.post("/api/runs/"+id+"/cancel", nil); r.StatusCode != 202 {
		t.Fatalf("cancel: %d %s", r.StatusCode, r.body)
	}
	if run := h.waitRun(id); run["status"] != "cancelled" {
		t.Fatalf("run %v", run)
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.urls) != 2 {
		t.Errorf("%d lists requested; the third must not be", len(tr.urls))
	}
}

// The page writes whole values through the portals patch and the server
// validates the result; the file stays one that the scanners accept.
func TestPortalsPatch_TheStructuredFormsWriteValidatedRevisionCheckedValues(t *testing.T) {
	h := newDomainHarness(t)
	doc := h.get("/api/config/portals").data()
	rev := doc["revision"].(string)
	ops := []map[string]any{
		{"op": "set", "path": "companies", "value": "- name: \"Example Co\"\n  ats: \"greenhouse\"\n  slug: \"example-co\"\n- name: \"Plain Careers\"\n  careers_url: \"https://careers.example.org/jobs\""},
		{"op": "set", "path": "sources", "value": "indeed:\n  queries:\n    - \"backend engineer\"\n    - search: \"platform engineer\"\n      location: \"Remote\""},
		{"op": "set", "path": "filters.title.positive", "value": "- \"engineer\""},
	}
	r := h.patch("/api/config/portals", map[string]any{"expectedRevision": rev, "ops": ops})
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	p, e := loadPortals(h.ws)
	if e != nil || len(p.Companies) != 2 || len(p.Sources["indeed"].Queries) != 2 || len(p.Filters.Title.Positive) != 1 {
		t.Fatalf("saved portals: %+v %v", p, e)
	}
	if r := h.patch("/api/config/portals", map[string]any{"expectedRevision": rev, "ops": ops[:1]}); r.StatusCode != 409 || r.errCode() != string(wire.CodeRevisionConflict) {
		t.Errorf("a stale revision: %d %s", r.StatusCode, r.body)
	}
	bad := []map[string]any{{"op": "set", "path": "companies", "value": "- name: \"No Source\""}}
	if r := h.patch("/api/config/portals", map[string]any{"expectedRevision": h.get("/api/config/portals").data()["revision"], "ops": bad}); r.StatusCode != 422 {
		t.Errorf("a company with no source form: %d %s", r.StatusCode, r.body)
	}
}
