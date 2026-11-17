package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/api/wire"
	"applysmith/internal/apply"
	"applysmith/internal/workspace"
)

const (
	resumesPath = "/api/resumes"
	rankPath    = "/api/resumes/rank"
)

const testPDF = "%PDF-1.4\n1 0 obj\n<<>>\nendobj\ntrailer\n<<>>\n%%EOF\n"

// rosterHarness writes a two-entry roster into the seeded profile.
func rosterHarness(t *testing.T) *harness {
	t.Helper()
	h := newDomainHarness(t)
	dir := filepath.Join(h.ws.Root, "resumes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"backend.pdf", "frontend.pdf"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(testPDF), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	setRoster(t, h, `resumes:
  - {id: frontend, label: Frontend, file: resumes/frontend.pdf, tags: [react, typescript], default: true, description: Interfaces and design systems}
  - {id: backend, label: Backend, file: resumes/backend.pdf, tags: [go, postgres, backend]}
`)
	return h
}

func setRoster(t *testing.T, h *harness, block string) {
	t.Helper()
	cur, err := h.ws.Read(workspace.FileProfile)
	if err != nil {
		t.Fatal(err)
	}
	src := string(cur.Data)
	from, to := strings.Index(src, "\nresumes:"), strings.Index(src, "\nhistory:")
	if from < 0 || to < from {
		t.Fatalf("the seeded profile has no roster:\n%s", src)
	}
	next := src[:from+1] + block + src[to:]
	if err := os.WriteFile(h.ws.Path(workspace.FileProfile), []byte(next), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResumeRoutesAreStandardAndRemoteDenied(t *testing.T) {
	for _, c := range []struct {
		method, path, request, response string
		body                            Body
		secret                          bool
	}{
		{"GET", resumesPath, PayloadNone, "ResumeList", BodyNone, false},
		{"GET", resumesPath + "/{id}/file", PayloadNone, PayloadAsset, BodyNone, false},
		{"POST", rankPath, "ResumeRankRequest", "ResumeRank", BodyJSON, false},
		{"POST", "/api/submissions", "SubmissionRecord", "SubmissionRecorded", BodyJSON, false},
		{"POST", "/api/submissions/claims", "PressClaimRequest", "PressClaimGranted", BodyJSON, false},
		{"POST", "/api/signin/resolve", "SigninResolveRequest", "SigninCredential", BodyJSON, true},
		{"GET", "/api/gmail/otp/latest", PayloadNone, "OneTimeCode", BodyNone, true},
	} {
		r := routeOf(t, c.method, c.path)
		want := AccessStandard
		if c.path == "/api/signin/resolve" {
			want = AccessAdmin // the credentials-vault spec lists it among the admin, secret routes
		}
		if r.Access != want || r.Remote != RemoteDenied || r.Limit != LimitGeneral || r.Body != c.body || r.Request != c.request || r.Response != c.response || r.Secret != c.secret || r.SessionOnly {
			t.Errorf("%s %s: %+v", c.method, c.path, r)
		}
	}
}

func TestResumeRoutesNeedACredential(t *testing.T) {
	h := newDomainHarness(t)
	for _, c := range []call{
		{method: "GET", path: resumesPath},
		{method: "GET", path: resumesPath + "/backend/file"},
		{method: "POST", path: rankPath, body: jsonBody(wire.ResumeRankRequest{}), contentType: "application/json"},
		{method: "POST", path: "/api/submissions", body: []byte("{}"), contentType: "application/json"},
		{method: "POST", path: "/api/submissions/claims", body: []byte("{}"), contentType: "application/json"},
		{method: "POST", path: "/api/signin/resolve", body: []byte("{}"), contentType: "application/json"},
		{method: "GET", path: "/api/gmail/otp/latest?since=2026-01-01T00:00:00Z"},
	} {
		if r := h.do(c); r.StatusCode != http.StatusUnauthorized || r.errCode() != "unauthenticated" {
			t.Errorf("%s %s: %d %s", c.method, c.path, r.StatusCode, r.body)
		}
	}
}

func TestResumeListCarriesNoBytes(t *testing.T) {
	h := rosterHarness(t)
	r := h.get(resumesPath)
	if r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	var env wire.Success[wire.ResumeList]
	strictInto(t, r.body, &env)
	got := env.Data.Resumes
	if len(got) != 2 || got[0].ID != "frontend" || !got[0].Default || got[1].ID != "backend" || got[1].Default {
		t.Fatalf("%+v", got)
	}
	if got[1].FileName != "backend.pdf" || got[1].Size != wire.SafeInt(len(testPDF)) || got[1].Tags[0] != "go" || got[0].Description == "" {
		t.Fatalf("%+v", got[1])
	}
	if strings.Contains(string(r.body), "%PDF") {
		t.Fatalf("file content in the listing: %s", r.body)
	}
}

func TestResumeFileServesThePDFWithItsType(t *testing.T) {
	h := rosterHarness(t)
	r := h.get(resumesPath + "/backend/file")
	if r.StatusCode != 200 || string(r.body) != testPDF {
		t.Fatalf("%d %q", r.StatusCode, r.body)
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("content type %q", ct)
	}
	if cd := r.Header.Get("Content-Disposition"); cd != `inline; filename=backend.pdf` {
		t.Errorf("content disposition %q", cd)
	}
	if r.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("nosniff missing")
	}
}

func TestResumeFileRefusesWhatTheRosterDoesNotName(t *testing.T) {
	h := rosterHarness(t)
	secret := filepath.Join(h.ws.Root, "cv.md")
	if err := os.WriteFile(secret, []byte("%PDF- not a résumé"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"missing", "..", "%2e%2e%2fcv.md", "backend%2f..%2f..%2fcv.md", "resumes%2fbackend.pdf", ""} {
		if r := h.get(resumesPath + "/" + id + "/file"); r.StatusCode != 404 && r.StatusCode != 307 {
			t.Errorf("id %q: %d %s", id, r.StatusCode, r.body)
		}
	}
	// A roster entry that points outside resumes/ is refused by validation, so
	// the roster cannot be read at all rather than served.
	setRoster(t, h, "resumes:\n  - {id: leak, label: Leak, file: cv.md, default: true}\n")
	if r := h.get(resumesPath + "/leak/file"); r.StatusCode == 200 || strings.Contains(string(r.body), "not a résumé") {
		t.Errorf("outside resumes/: %d %s", r.StatusCode, r.body)
	}
}

func TestResumeFileRefusesAFileThatIsNotAPDFOrAnEscapingLink(t *testing.T) {
	h := rosterHarness(t)
	if err := os.WriteFile(filepath.Join(h.ws.Root, "resumes", "backend.pdf"), []byte("plain text, not a pdf"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := h.get(resumesPath + "/backend/file"); r.StatusCode != 404 {
		t.Errorf("not a pdf: %d %s", r.StatusCode, r.body)
	}
	outside := filepath.Join(t.TempDir(), "outside.pdf")
	if err := os.WriteFile(outside, []byte(testPDF), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(h.ws.Root, "resumes", "frontend.pdf")
	_ = os.Remove(link)
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("symbolic links are unavailable")
	}
	if r := h.get(resumesPath + "/frontend/file"); r.StatusCode != 404 {
		t.Errorf("escaping link: %d %s", r.StatusCode, r.body)
	}
}

func TestResumeFileRefusesAFileOverTenMiB(t *testing.T) {
	h := rosterHarness(t)
	big := append([]byte("%PDF-"), make([]byte, 10<<20)...)
	if err := os.WriteFile(filepath.Join(h.ws.Root, "resumes", "backend.pdf"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	if r := h.get(resumesPath + "/backend/file"); r.StatusCode != 404 {
		t.Errorf("%d", r.StatusCode)
	}
}

func rank(h *harness, title, description string) wire.ResumeRank {
	h.t.Helper()
	r := h.post(rankPath, wire.ResumeRankRequest{Title: title, Company: "Example Systems", Description: description})
	if r.StatusCode != 200 {
		h.t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	var env wire.Success[wire.ResumeRank]
	strictInto(h.t, r.body, &env)
	return env.Data
}

func TestResumeRankChoosesTheTaggedEntryWithItsMatchedTerms(t *testing.T) {
	h := rosterHarness(t)
	got := rank(h, "Backend Engineer", "Build Go services on Postgres.")
	if got.Chosen != "backend" || len(got.Ranking) != 2 || got.Ranking[0].ID != "backend" || got.Ranking[0].Score != 3 {
		t.Fatalf("%+v", got)
	}
	if got.Ranking[0].Reason != "backend, go, postgres" || got.Ranking[1].ID != "frontend" || got.Ranking[1].Score != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestResumeRankRefusals(t *testing.T) {
	h := rosterHarness(t)
	for name, body := range map[string]any{
		"long description": wire.ResumeRankRequest{Title: "x", Description: strings.Repeat("a", 20_001)},
		"unknown field":    map[string]any{"title": "x", "bytes": "y"},
	} {
		if r := h.post(rankPath, body); r.StatusCode != 422 {
			t.Errorf("%s: %d %s", name, r.StatusCode, r.body)
		}
	}
	ok := wire.ResumeRankRequest{Title: "x", Description: strings.Repeat("a", 20_000)}
	if r := h.post(rankPath, ok); r.StatusCode != 200 {
		t.Errorf("20,000 characters: %d %s", r.StatusCode, r.body)
	}
	if resp, b := h.postExpecting(rankPath, "application/json", []byte(strings.Repeat(" ", 300_000))); resp.StatusCode != 413 {
		t.Errorf("body over the json limit: %d %s", resp.StatusCode, b)
	}
}

// An apply run uploads the roster entry that best matches its bound report,
// read from production's shape: a profile with quoted keys and block tag
// lists, the default second, and the report bound through the report index.
// Which words of a report count is TestRankReportReadsThePostingNotTheEvidence's.
func TestApplyRun_ProfileResumeIsTheBestMatchForTheBoundReport(t *testing.T) {
	h := newDomainHarness(t)
	dir := filepath.Join(h.ws.Root, "resumes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"frontend.pdf", "platform.pdf"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(testPDF), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	setRoster(t, h, `resumes:
  - "id": "frontend"
    "label": "Frontend / web"
    "file": "resumes/frontend.pdf"
    "tags":
      - "react"
      - "typescript"
    "default": false
  - "id": "platform"
    "label": "Platform engineering"
    "file": "resumes/platform.pdf"
    "tags":
      - "go"
      - "kubernetes"
      - "platform"
    "default": true
`)
	evidence := "CV: Built platform services in Go on Kubernetes."
	seedReport(t, h, 12, "final", 4.1, "Frontend Engineer", finalReport(12, "Frontend Engineer", "React and TypeScript in production.", "You ship React and TypeScript to production.", evidence, "React, TypeScript, design systems"))
	seedReport(t, h, 13, "final", 4.2, "Payroll Specialist", finalReport(13, "Payroll Specialist", "Payroll operations.", "You run payroll.", evidence, "Payroll"))
	reports := apply.SQLReports{DB: h.srv.store.Load().Read()}
	for _, c := range []struct {
		report              int64
		file, label, reason string
	}{
		{12, "frontend.pdf", "Frontend / web", "react"},
		{13, "platform.pdf", "Platform engineering", "default"},
		{0, "platform.pdf", "Platform engineering", "default"},
	} {
		var rep *apply.ReportBinding
		if c.report != 0 {
			var err error
			if rep, err = reports.ByNumber(t.Context(), c.report); err != nil || rep == nil {
				t.Fatalf("bind %d: %v", c.report, err)
			}
		}
		pl, why, err := h.domainOf().apply.profileResume(t.Context(), rep)
		if err != nil || pl.FileName != c.file || !strings.Contains(why, c.label) || !strings.Contains(why, c.reason) {
			t.Errorf("report %d: %v %q %v", c.report, pl, why, err)
		}
	}
}
