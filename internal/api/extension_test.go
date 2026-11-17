package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"applysmith/internal/api/apikit"
	"applysmith/internal/api/wire"
	"applysmith/internal/discovery"
	"applysmith/internal/extension"
	"applysmith/internal/workspace"
)

const (
	applicantPath = "/api/autofill/applicant"
	fillRunsPath  = "/api/autofill/runs"
	buildPath     = "/api/extension/build"
	postingPath   = "/api/autofill/posting"
)

// The session cookies the extension sends are secret: their bodies are never
// logged.
func TestExtensionSessionUploadsAreSecret(t *testing.T) {
	for _, path := range []string{"/api/browser/sessions/imports", "/api/browser/sessions/pushes"} {
		if r := routeOf(t, "POST", path); !r.Secret || r.Body != BodyJSONLarge {
			t.Errorf("%s: %+v", path, r)
		}
	}
}

// The extension holds the bearer token and the dashboard holds a session; any
// other caller, or a wrong token, is turned away before a handler runs.
func TestExtensionRoutesNeedACredential(t *testing.T) {
	h := newDomainHarness(t)
	calls := []call{
		{method: "GET", path: applicantPath},
		{method: "POST", path: fillRunsPath, body: jsonBody(wire.FillRunRequest{Host: "jobs.example.com", URL: "https://jobs.example.com/a"})},
		{method: "POST", path: fillRunsPath + "/run_x/end", body: jsonBody(wire.FillRunEnd{Status: "succeeded"})},
		{method: "GET", path: buildPath},
		{method: "GET", path: postingPath + "?url=https://jobs.lever.co/example/abc/apply"},
		{method: "POST", path: "/api/browser/sessions/checkin", body: jsonBody(wire.SessionCheckin{})},
		{method: "POST", path: "/api/browser/sessions/imports", body: jsonBody(wire.SessionImport{})},
		{method: "POST", path: "/api/browser/sessions/pushes", body: jsonBody(wire.SessionPush{})},
	}
	for _, c := range calls {
		if c.body != nil {
			c.contentType = "application/json"
		}
		none, wrong := c, c
		wrong.bearer = "not-the-token-not-the-token-not-the-token"
		for name, cc := range map[string]call{"no credential": none, "wrong token": wrong} {
			if r := h.do(cc); r.StatusCode != http.StatusUnauthorized || r.errCode() != "unauthenticated" {
				t.Errorf("%s %s with %s: %d %s", c.method, c.path, name, r.StatusCode, r.body)
			}
		}
	}
	// A dashboard session reaches the same routes.
	if r := h.do(call{path: applicantPath, cookie: h.session()}); r.StatusCode != 200 {
		t.Fatalf("session: %d %s", r.StatusCode, r.body)
	}
}

const applicantTestProfile = `version: 1
candidate:
  full_name: Alex Quinn Example
  email: alex@example.com
  phone: "+1 555 0100"
  date_of_birth: "1990-01-01"
  address:
    line1: "@ask-me"
    city: Exampleville
    country: US
links:
  linkedin: linkedin.com/in/alex-example
work_authorization:
  requires_sponsorship_now: false
  visa_status: citizen
targets:
  compensation: {currency: USD, period: year, minimum: 100000, target: 150000}
history:
  employment:
    - {employer: Example Corp, title: Engineer, start: "2021-07", end: present}
  education:
    - {school: Example University, degree: BSc, start: "2012-09", end: "2015-06"}
`

func TestApplicantRouteServesIdentityAndHistoryAndNothingSensitive(t *testing.T) {
	h := seededBankHarness(t)
	if err := os.WriteFile(h.ws.Path(workspace.FileProfile), []byte(applicantTestProfile), 0o600); err != nil {
		t.Fatal(err)
	}
	r := h.get(applicantPath)
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	d := r.data()
	cand := d["candidate"].(map[string]any)
	if cand["fullName"] != "Alex Quinn Example" || cand["firstName"] != "Alex" || cand["middleName"] != "Quinn" || cand["lastName"] != "Example" ||
		cand["phoneCountryCode"] != "+1" || cand["phoneNational"] != "5550100" || cand["email"] != "alex@example.com" {
		t.Fatalf("candidate %v", cand)
	}
	if links := d["links"].(map[string]any); links["linkedin"] != "https://www.linkedin.com/in/alex-example/" {
		t.Fatalf("links %v", links)
	}
	hist := d["history"].(map[string]any)
	job := hist["employment"].([]any)[0].(map[string]any)
	study := hist["education"].([]any)[0].(map[string]any)
	if job["employer"] != "Example Corp" || job["current"] != true || job["startDate"] != "2021-07" || job["endDate"] != nil ||
		study["school"] != "Example University" || study["inProgress"] != false || study["endDate"] != "2015-06" {
		t.Fatalf("history %v", hist)
	}
	// An "@ask-me" value is left out and named; nothing sensitive is served.
	addr := cand["address"].(map[string]any)
	if _, has := addr["line1"]; has || addr["city"] != "Exampleville" {
		t.Fatalf("address %v", addr)
	}
	if ask := d["askMe"].([]any); len(ask) != 1 || ask[0] != "address.line1" {
		t.Fatalf("askMe %v", ask)
	}
	for _, banned := range []string{"@ask-me", "1990-01-01", "citizen", "150000", "100000", "sponsorship", "dateOfBirth", h.token()} {
		if strings.Contains(string(r.body), banned) {
			t.Errorf("the response contains %q", banned)
		}
	}
}

// The engine plans numbered education rows from the snapshot in its order and
// the matcher answers them most recent first: a profile that lists its schools
// oldest first is served most recent first, or a form with two rows gets one
// school's name beside the other's degree (10a Labs).
func TestApplicantRouteListsEducationMostRecentFirst(t *testing.T) {
	h := seededBankHarness(t)
	profile := strings.Replace(applicantTestProfile, `    - {school: Example University, degree: BSc, start: "2012-09", end: "2015-06"}`,
		`    - {school: Example University, degree: BSc, start: "2012-09", end: "2015-06"}
    - {school: Example Graduate School, degree: MSc, start: "2015-09", end: "2017-06"}`, 1)
	if err := os.WriteFile(h.ws.Path(workspace.FileProfile), []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range h.get(applicantPath).data()["history"].(map[string]any)["education"].([]any) {
		got = append(got, s.(map[string]any)["school"].(string))
	}
	if !slices.Equal(got, []string{"Example Graduate School", "Example University"}) {
		t.Fatalf("education %v", got)
	}
}

func TestExtensionBuildRoute(t *testing.T) {
	h := newDomainHarness(t, func(c *Config) {
		c.ExtensionBuild = func() (wire.ExtensionBuild, error) {
			return wire.ExtensionBuild{Stamp: "b21c5e90a7d3f648", Version: "0.1.0"}, nil
		}
	})
	r := h.get(buildPath)
	if r.StatusCode != 200 || r.data()["stamp"] != "b21c5e90a7d3f648" || r.data()["version"] != "0.1.0" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestExtensionBuildRouteReportsAnExtensionThatWasNotBuilt(t *testing.T) {
	h := newDomainHarness(t, func(c *Config) {
		c.ExtensionBuild = func() (wire.ExtensionBuild, error) { return wire.ExtensionBuild{}, extension.ErrNotBuilt }
	})
	r := h.get(buildPath)
	if r.StatusCode != 503 || r.errCode() != "unavailable" || r.errReason() != "extension-not-built" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

// A Lever /apply or Ashby /application page holds only the form: the route
// reads the posting from the board's public API, one request, and refuses any
// other address before it calls anything.
func TestPostingRouteReadsALeverOrAshbyBoard(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/v0/postings/example/5f3c2a1b-0000-4000-8000-000000000001":
			_ = json.NewEncoder(w).Encode(map[string]any{"text": "Platform Engineer", "descriptionPlain": "Build the platform.",
				"categories": map[string]any{"location": "Durham, NC"}, "lists": []map[string]any{{"text": "Requirements", "content": "<li>Go</li>"}}})
		case "/posting-api/job-board/example":
			_ = json.NewEncoder(w).Encode(map[string]any{"jobs": []map[string]any{{"id": "9a8b7c6d-0000-4000-8000-000000000002", "title": "Data Engineer", "location": "Remote", "descriptionPlain": "Move the data."}}})
		default:
			http.Error(w, "down", http.StatusBadGateway)
		}
	}))
	defer upstream.Close()
	h := newDomainHarness(t, func(c *Config) {
		c.Domain.ScanEndpoints = discovery.Endpoints{discovery.Lever: upstream.URL, discovery.Ashby: upstream.URL}
	})
	read := func(page string) result { return h.get(postingPath + "?url=" + url.QueryEscape(page)) }
	r := read("https://jobs.lever.co/example/5f3c2a1b-0000-4000-8000-000000000001/apply")
	if d := r.data(); r.StatusCode != 200 || d["title"] != "Platform Engineer" || d["location"] != "Durham, NC" || d["text"] != "Build the platform.\nRequirements\n- Go" {
		t.Fatalf("lever: %d %s", r.StatusCode, r.body)
	}
	r = read("https://jobs.ashbyhq.com/example/9a8b7c6d-0000-4000-8000-000000000002/application")
	if d := r.data(); r.StatusCode != 200 || d["title"] != "Data Engineer" || d["location"] != "Remote" || d["text"] != "Move the data." {
		t.Fatalf("ashby: %d %s", r.StatusCode, r.body)
	}
	if r = read("https://jobs.lever.co/gone/5f3c2a1b-0000-4000-8000-000000000001/apply"); r.StatusCode != 503 || r.errReason() != "board-unreadable" {
		t.Fatalf("unreadable board: %d %s", r.StatusCode, r.body)
	}
	before := calls.Load()
	for _, page := range []string{"https://boards.greenhouse.io/example/jobs/1", "https://jobs.lever.co.evil.test/example/5f3c2a1b-0000-4000-8000-000000000001/apply"} {
		if r = read(page); r.StatusCode != 422 || r.errCode() != "validation_failed" {
			t.Errorf("%q: %d %s", page, r.StatusCode, r.body)
		}
	}
	if calls.Load() != before {
		t.Fatal("a refused address reached the network")
	}
}

func (h *harness) startFill(key, host, page string) result {
	h.t.Helper()
	return h.send("POST", fillRunsPath, wire.FillRunRequest{Host: host, URL: page}, "Idempotency-Key", key)
}

func (h *harness) issueFill() string {
	h.t.Helper()
	r := h.startFill(freshKey(), "jobs.example.com", "https://jobs.example.com/apply/1")
	if r.StatusCode != 202 {
		h.t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	return r.data()["runId"].(string)
}

func (h *harness) runState(id string) (kind, status, reason, input string) {
	h.t.Helper()
	err := h.srv.store.Load().Read().QueryRow(`SELECT kind, status, failure_reason, input_json FROM runs WHERE id = ?`, id).Scan(&kind, &status, &reason, &input)
	if err != nil {
		h.t.Fatal(err)
	}
	return
}

func TestFillRunIsIssuedAsARunOfItsOwnKind(t *testing.T) {
	h := newDomainHarness(t)
	r := h.startFill(freshKey(), "Jobs.Example.com", "https://jobs.example.com/apply/1?token=zz#step2")
	id, _ := r.data()["runId"].(string)
	if r.StatusCode != 202 || id == "" || r.Header.Get("Location") != "/api/runs/"+id {
		t.Fatalf("%d %s %v", r.StatusCode, r.body, r.Header)
	}
	kind, status, _, input := h.runState(id)
	if kind != "extension-fill" || status != "running" {
		t.Fatalf("%s %s", kind, status)
	}
	if strings.Contains(input, "token") || strings.Contains(input, "step2") || !strings.Contains(input, "jobs.example.com/apply/1") {
		t.Fatalf("the stored page keeps its query or fragment: %s", input)
	}
	if got := h.get("/api/runs/" + id); got.StatusCode != 200 || got.data()["kind"] != "extension-fill" || got.data()["status"] != "running" {
		t.Fatalf("%d %s", got.StatusCode, got.body)
	}
}

func TestFillRunIsNotIssuedToARemoteDevice(t *testing.T) {
	h := newDomainHarness(t)
	for _, p := range []apikit.Principal{{Kind: apikit.PrincipalRemoteDevice, ID: "device"}, {}} {
		c := &apikit.Call{P: p, R: httptestRequest(t), Body: jsonBody(wire.FillRunRequest{Host: "jobs.example.com", URL: "https://jobs.example.com/a"})}
		c.R.Header.Set("Idempotency-Key", freshKey())
		_, err := h.srv.createFillRun(c)
		var ae *apikit.Error
		if !errors.As(err, &ae) || ae.Code != wire.CodeForbidden {
			t.Fatalf("%+v: %v", p, err)
		}
	}
	if h.runCount() != 0 {
		t.Fatal("a run was created")
	}
}

func httptestRequest(t *testing.T) *http.Request {
	t.Helper()
	r, err := http.NewRequest("POST", fillRunsPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFillRunRefusals(t *testing.T) {
	h := newDomainHarness(t)
	for _, c := range []struct {
		name, host, page, field string
	}{
		{"no host", "", "https://jobs.example.com/a", "host"},
		{"page on another host", "jobs.example.com", "https://other.example.com/a", "url"},
		{"page with credentials", "jobs.example.com", "https://user:pw@jobs.example.com/a", "url"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := h.startFill(freshKey(), c.host, c.page)
			if r.StatusCode != 422 || r.errCode() != "validation_failed" || !strings.Contains(string(r.body), `"field":"`+c.field+`"`) {
				t.Fatalf("%d %s", r.StatusCode, r.body)
			}
		})
	}
	if h.runCount() != 0 {
		t.Fatal("a refused request created a run")
	}
	if r := h.post(fillRunsPath, wire.FillRunRequest{Host: "jobs.example.com", URL: "https://jobs.example.com/a"}); r.StatusCode != 400 {
		t.Fatalf("no Idempotency-Key: %d %s", r.StatusCode, r.body)
	}
}

func TestFillRunKeyReplaysAndConflicts(t *testing.T) {
	h := newDomainHarness(t)
	key := freshKey()
	first := h.startFill(key, "jobs.example.com", "https://jobs.example.com/a")
	again := h.startFill(key, "jobs.example.com", "https://jobs.example.com/a")
	if again.StatusCode != 202 || again.data()["runId"] != first.data()["runId"] || h.runCount() != 1 {
		t.Fatalf("%d %s, %d runs", again.StatusCode, again.body, h.runCount())
	}
	if other := h.startFill(key, "jobs.example.com", "https://jobs.example.com/b"); other.StatusCode != 409 {
		t.Fatalf("%d %s", other.StatusCode, other.body)
	}
}

func TestFillRunsAreBounded(t *testing.T) {
	h := newDomainHarness(t)
	var first string
	for i := 0; i < maxOpenFills; i++ {
		if id := h.issueFill(); first == "" {
			first = id
		}
	}
	r := h.startFill(freshKey(), "jobs.example.com", "https://jobs.example.com/a")
	if r.StatusCode != 409 || r.errReason() != "too-many-fills" {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if end := h.post(fillRunsPath+"/"+first+"/end", wire.FillRunEnd{Status: "succeeded"}); end.StatusCode != 200 {
		t.Fatalf("%d %s", end.StatusCode, end.body)
	}
	if r := h.startFill(freshKey(), "jobs.example.com", "https://jobs.example.com/a"); r.StatusCode != 202 {
		t.Fatalf("an ended fill frees its place: %d %s", r.StatusCode, r.body)
	}
}

// The issued run is a run the server issued: the match route, the system-write
// route and the learning route take it.
func TestIssuedFillRunSatisfiesMatchSystemWritesAndLearning(t *testing.T) {
	h := matchHarness(t)
	run := h.issueFill()
	m := h.post("/api/answer-bank/match", wire.AnswerMatchRequest{Host: "jobs.example.com", RunID: run, Questions: []wire.AnswerMatchQuestion{
		{Selector: `[id="county"]`, Text: "County", Kind: "text"}}})
	if m.StatusCode != 200 || m.data()["results"].([]any)[0].(map[string]any)["answer"] != "Durham County" {
		t.Fatalf("match: %d %s", m.StatusCode, m.body)
	}
	if w := h.post(systemWritesPath, wire.SystemWriteRequest{RunID: run, Selector: "#city", Value: "Durham"}); w.StatusCode != 200 {
		t.Fatalf("system write: %d %s", w.StatusCode, w.body)
	}
	if h.fillGroundTruth("fill-own", run, "#city", "Which city is it", "Shelbyville") != true {
		t.Fatal("a manual fill in the issued run is the user's")
	}
}

func TestFillRunEnds(t *testing.T) {
	for _, status := range []string{"succeeded"} {
		t.Run(status, func(t *testing.T) {
			h := newDomainHarness(t)
			run := h.issueFill()
			r := h.post(fillRunsPath+"/"+run+"/end", wire.FillRunEnd{Status: status})
			if r.StatusCode != 200 || r.data()["status"] != status || r.data()["finishedAt"] == nil {
				t.Fatalf("%d %s", r.StatusCode, r.body)
			}
			if _, st, _, _ := h.runState(run); st != status {
				t.Fatal(st)
			}
			again := h.post(fillRunsPath+"/"+run+"/end", wire.FillRunEnd{Status: "cancelled"})
			if again.StatusCode != 200 || again.data()["status"] != status {
				t.Fatalf("a second end changes nothing: %d %s", again.StatusCode, again.body)
			}
		})
	}
}

func TestFillRunEndRefusals(t *testing.T) {
	h := newDomainHarness(t)
	run := h.issueFill()
	if r := h.post(fillRunsPath+"/"+run+"/end", wire.FillRunEnd{Status: "failed"}); r.StatusCode != 422 || !strings.Contains(string(r.body), `"field":"status"`) {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if r := h.post(fillRunsPath+"/run_0123456789abcdef01234567/end", wire.FillRunEnd{Status: "succeeded"}); r.StatusCode != 404 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	other := h.issueRun() // a run of another kind
	if r := h.post(fillRunsPath+"/"+other+"/end", wire.FillRunEnd{Status: "succeeded"}); r.StatusCode != 409 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if _, st, _, _ := h.runState(other); st != "running" {
		t.Fatal("a run of another kind was ended")
	}
}

func TestFillRunEndsAfterAnIdleTimeout(t *testing.T) {
	h := newDomainHarness(t)
	idle, busy := h.issueFill(), h.issueFill()
	h.clock.advance(fillRunIdle - time.Minute)
	if w := h.post(systemWritesPath, wire.SystemWriteRequest{RunID: busy, Selector: "#a", Value: "b"}); w.StatusCode != 200 {
		t.Fatalf("%d %s", w.StatusCode, w.body)
	}
	h.clock.advance(2 * time.Minute)
	h.srv.sweepFills(context.Background())
	if _, st, reason, _ := h.runState(idle); st != "cancelled" || reason != "idle-timeout" {
		t.Fatalf("the idle fill: %s %s", st, reason)
	}
	if _, st, _, _ := h.runState(busy); st != "running" {
		t.Fatalf("a call kept the second fill alive: %s", st)
	}
	// The ended run still names a valid issue for late learning events.
	if got := h.get("/api/runs/" + idle); got.data()["status"] != "cancelled" {
		t.Fatalf("%s", got.body)
	}
}

// A fill holds no authority to submit: nothing in the issue or the end of the
// run touches the submit ledger, and the run cannot be cancelled as work.
func TestFillRunNeverSubmits(t *testing.T) {
	h := newDomainHarness(t)
	run := h.issueFill()
	if end := h.post(fillRunsPath+"/"+run+"/end", wire.FillRunEnd{Status: "succeeded"}); end.StatusCode != 200 {
		t.Fatalf("end: %d %s", end.StatusCode, end.body)
	}
	if n := h.rowCount("submissions"); n != 0 {
		t.Fatalf("%d submissions", n)
	}
	if r := h.post("/api/runs/"+run+"/cancel", nil); r.StatusCode != 409 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
}

func TestFillRunLeftOpenByAStoppedServerFails(t *testing.T) {
	h := newDomainHarness(t)
	run := h.issueFill()
	h.restart()
	if _, st, reason, _ := h.runState(run); st != "failed" || reason != "interrupted" {
		t.Fatalf("%s %s", st, reason)
	}
}
