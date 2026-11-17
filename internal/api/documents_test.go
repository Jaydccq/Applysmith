package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/documents"
	"applysmith/internal/documents/toolchain"
	"applysmith/internal/identity"
	"applysmith/internal/tracker"
)

const docsProfile = `version: 1
candidate:
  full_name: Alex Example
  email: alex.example@example.com
history:
  employment:
    - employer: Example Systems
      title: Senior Engineer
      start: 2021-03
      end: present
  education:
    - school: Example University
      degree: BSc
      field: Mathematics
      start: 2011-09
      end: 2015-06
skills:
  - name: Go
`

const docsCV = `# Alex Example

## Summary

Backend engineer focused on reliable data platforms.

## Experience

### Senior Engineer, Example Systems (2021-03 to present)

- Led the platform migration and cut incidents by 40% across 12 services
- Mentored four engineers in code review and design

## Skills

- Languages: Go, SQL
`

const docsPosting = "Backend Engineer at Example Corp\n\nRequirements:\n- 3+ years of experience building backend services in Go\n- Ability to mentor engineers and lead code review\n"

// docsHarness is a ready server with a profile and CV, one candidacy and a
// document toolchain that reports the tools missing, so no TeX ever runs.
type docsHarness struct {
	*harness
	cand string
}

func newDocsHarness(t *testing.T, opener func(context.Context) (*toolchain.Toolchain, error), mods ...func(*Config)) docsHarness {
	t.Helper()
	if opener == nil {
		opener = func(context.Context) (*toolchain.Toolchain, error) {
			return nil, &toolchain.UnavailableError{Report: toolchain.Report{State: toolchain.StateUnavailable, Missing: []string{"lualatex"}}}
		}
	}
	h := newDomainHarness(t, append([]func(*Config){func(c *Config) { c.Domain.DocumentToolchain = opener }}, mods...)...)
	for name, text := range map[string]string{"profile.yml": docsProfile, "cv.md": docsCV} {
		if err := os.WriteFile(filepath.Join(h.ws.Root, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cand := h.addCandidacy("Example Corp", "Backend Engineer", "https://jobs.example.com/backend-1")
	return docsHarness{harness: h, cand: idOf(cand)}
}

// eligible gives the candidacy a final-kind report that clears the PDF score
// floor.
func (d docsHarness) eligible() {
	d.t.Helper()
	ctx := context.Background()
	err := d.srv.store.Load().Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO report_artifacts (report_number, kind, path, status, content_hash, created_at, updated_at)
			VALUES (7, 'report', 'reports/0007-example-corp.md', 'ready', 'h', 'x', 'x')`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO report_index (report_number, kind, company, role, posting_url, identity_key, score, score_kind, recommendation, legitimacy, verification, markdown_path, html_path, job_id, documents_eligible, created_at)
			VALUES (7, 'final', 'Example Corp', 'Backend Engineer', 'https://jobs.example.com/backend-1', 'k', 4.6, 'final', 'strong', 'credible', 'pasted unverified', 'reports/0007-example-corp.md', 'reports/0007-example-corp.html', 'j', 1, 'x')`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE tracker_entries SET report_number = 7 WHERE id = ?`, d.cand)
		return err
	})
	if err != nil {
		d.t.Fatal(err)
	}
}

func (d docsHarness) start(path string, body any) result {
	d.t.Helper()
	return d.send("POST", path, body, "Idempotency-Key", freshKey())
}

// finish waits for a document run to end and returns its view.
func (d docsHarness) finish(runID string) wire.DocumentRunView {
	d.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	pause := newPollPause()
	for {
		r := d.get("/api/documents/runs/" + runID)
		var env wire.Success[wire.DocumentRunView]
		strictInto(d.t, r.body, &env)
		if r.StatusCode == 200 && env.Data.Status != "queued" && env.Data.Status != wire.RunRunning {
			return env.Data
		}
		if time.Now().After(deadline) {
			d.t.Fatalf("document run %s did not finish: %s", runID, r.body)
		}
		pause()
	}
}

// generate runs a pair for the candidacy and returns the stored documents.
func (d docsHarness) generate() []wire.DocumentView {
	d.t.Helper()
	r := d.start("/api/documents/resume", map[string]any{"candidacyId": atoi(d.cand), "includeCoverLetter": true, "posting": docsPosting})
	if r.StatusCode != http.StatusAccepted {
		d.t.Fatalf("start: %d %s", r.StatusCode, r.body)
	}
	if run := d.finish(r.data()["runId"].(string)); run.Status != wire.RunSucceeded || len(run.Documents) != 2 {
		d.t.Fatalf("run = %+v", run)
	}
	return d.list()
}

func (d docsHarness) list() []wire.DocumentView {
	d.t.Helper()
	r := d.get("/api/documents?candidacy=" + d.cand)
	var env wire.Success[wire.DocumentList]
	strictInto(d.t, r.body, &env)
	if r.StatusCode != 200 {
		d.t.Fatalf("list: %d %s", r.StatusCode, r.body)
	}
	return env.Data.Documents
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

func TestDocumentRoutes_ResumeRunsAndListsAPair(t *testing.T) {
	d := newDocsHarness(t, nil)
	r := d.start("/api/documents/resume", map[string]any{"candidacyId": atoi(d.cand), "includeCoverLetter": true, "posting": docsPosting})
	id, _ := r.data()["runId"].(string)
	if r.StatusCode != http.StatusAccepted || id == "" || r.Header.Get("Location") != "/api/runs/"+id {
		t.Fatalf("%d %v %s", r.StatusCode, r.Header, r.body)
	}
	run := d.finish(id)
	if run.Status != wire.RunSucceeded || run.Generation != "deterministic" || len(run.Documents) != 2 || run.Retrieval.Query == "" {
		t.Fatalf("run = %+v", run)
	}
	docs := d.list()
	if len(docs) != 2 {
		t.Fatalf("documents = %+v", docs)
	}
	kinds := map[string]bool{}
	for _, doc := range docs {
		kinds[doc.Kind] = true
		if doc.PDFState != "none" || !doc.Current || doc.Frozen || doc.CandidacyID == nil || doc.Version != 1 {
			t.Errorf("document = %+v", doc)
		}
	}
	if !kinds["resume"] || !kinds["cover-letter"] {
		t.Errorf("kinds = %v", kinds)
	}
}

func TestDocumentRoutes_ResumeRefusals(t *testing.T) {
	d := newDocsHarness(t, nil)
	cases := map[string]map[string]any{
		"both modes":        {"candidacyId": atoi(d.cand), "baseline": true, "posting": docsPosting},
		"unknown candidacy": {"candidacyId": 999, "posting": docsPosting},
	}
	for name, body := range cases {
		r := d.start("/api/documents/resume", body)
		want, code := 422, "validation_failed"
		if name == "unknown candidacy" {
			want, code = 404, "not_found"
		}
		if r.StatusCode != want || r.errCode() != code {
			t.Errorf("%s: %d %s", name, r.StatusCode, r.body)
		}
	}
	if r := d.send("POST", "/api/documents/resume", map[string]any{"baseline": true}); r.StatusCode != 400 || r.errCode() != "bad_request" {
		t.Errorf("no Idempotency-Key: %d %s", r.StatusCode, r.body)
	}
	// The row links no report, so there is no floor decision to override: the
	// refusal offers none, and the list says so before anything is asked.
	if r := d.start("/api/documents/resume", map[string]any{"candidacyId": atoi(d.cand), "pdf": true, "posting": docsPosting}); r.StatusCode != 422 || r.errReason() != "" || contains(string(r.body), "override") {
		t.Errorf("a PDF for a row with no report: %d %s", r.StatusCode, r.body)
	}
	var env wire.Success[wire.DocumentList]
	if strictInto(t, d.get("/api/documents?candidacy="+d.cand).body, &env); !env.Data.NoReport || env.Data.BelowFloor {
		t.Errorf("the list must say the row has no report, and offer no floor to override: %+v", env.Data)
	}
	if h := d.runCount(); h != 0 {
		t.Errorf("%d runs were created by refused requests", h)
	}
}

// A PDF refused below the score floor is allowed by the row's override, which
// only the dashboard session sends.
func TestDocumentRoutes_OverrideLiftsTheScoreFloor(t *testing.T) {
	d := newDocsHarness(t, nil)
	d.eligible()
	var env wire.Success[wire.DocumentList]
	if strictInto(t, d.get("/api/documents?candidacy="+d.cand).body, &env); env.Data.NoReport || env.Data.BelowFloor {
		t.Errorf("a row with a report over the floor: %+v", env.Data)
	}
	ctx := context.Background()
	if err := d.srv.store.Load().Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE report_index SET documents_eligible = 0, score = 3.9 WHERE report_number = 7`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// The list says the floor before any run is asked for.
	if strictInto(t, d.get("/api/documents?candidacy="+d.cand).body, &env); !env.Data.BelowFloor {
		t.Error("the list must say the report is under the floor")
	}
	body := map[string]any{"candidacyId": atoi(d.cand), "pdf": true, "posting": docsPosting}
	if r := d.start("/api/documents/resume", body); r.errReason() != wire.ReasonBelowScoreFloor || !contains(string(r.body), "Allow a PDF anyway") {
		t.Fatalf("below the floor: %d %s", r.StatusCode, r.body)
	}
	path := "/api/candidacies/" + d.cand + "/documents-override"
	if r := d.post(path, nil); r.StatusCode != 403 || r.errReason() != wire.ReasonSessionOnly {
		t.Fatalf("the bearer token overrode the floor: %d %s", r.StatusCode, r.body)
	}
	r := d.do(call{method: "POST", path: path, cookie: d.session(), headers: map[string]string{"Origin": d.srv.Origin()}})
	if r.StatusCode != 200 || r.data()["reportNumber"] != float64(7) {
		t.Fatalf("override: %d %s", r.StatusCode, r.body)
	}
	var after wire.Success[wire.DocumentList]
	if strictInto(t, d.get("/api/documents?candidacy="+d.cand).body, &after); after.Data.BelowFloor {
		t.Error("an overridden floor is still offered")
	}
	// Past the floor, the next gate is the missing toolchain of this harness.
	if r := d.start("/api/documents/resume", body); r.errReason() != wire.ReasonToolchainUnavailable {
		t.Errorf("after the override: %d %s", r.StatusCode, r.body)
	}
}

// finalReport is a final report as the evaluator writes it: the header, the
// title, the fit table whose evidence quotes the CV, the scorecard, the
// keywords and the answers.
func finalReport(n int64, role, requirement, source, evidence, keywords string) string {
	return fmt.Sprintf("report: %04d\ndate: 2026-01-02\nscore_kind: final\nrecommendation: apply\nurl: https://jobs.example.com/example-corp/%d\n\n# Example Corp - %s\n\n## Verdict\n\nWorth it.\n\n"+
		"## Fit\n\n| Group | Requirement | Hit | Evidence |\n|---|---|---|---|\n| must-have | %s | 1.0 | %s |\n\n```scorecard\n{\n  \"items\": {\n    \"must_have\": [\n      {\n"+
		"        \"id\": \"m1\",\n        \"label\": %q,\n        \"source\": %q,\n        \"hit_level\": 1,\n        \"evidence\": %q\n      }\n    ]\n  }\n}\n```\n\n"+
		"## Keywords\n\n%s\n\n## Application answers\n\n**Why this company?**\n\nThe work.\n", n, n, role, requirement, evidence, requirement, source, evidence, keywords)
}

// seedReport records report n of Example Corp as evaluate/persist.go does: the
// markdown file, the artifact and index rows and the tracker upsert in one
// transaction. It returns the tracker row.
func seedReport(t *testing.T, h *harness, n int64, kind string, score float64, role, md string) int64 {
	t.Helper()
	ctx := t.Context()
	path := fmt.Sprintf("reports/%04d-example-corp-2026-01-02.md", n)
	if err := os.MkdirAll(filepath.Join(h.ws.Root, "reports"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.ws.Root, path), []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	url := fmt.Sprintf("https://jobs.example.com/example-corp/%d", n)
	key, err := identity.URLKey(url)
	if err != nil {
		t.Fatal(err)
	}
	scoreKind, eligible := tracker.ScoreScreening, 0
	if kind == "final" {
		scoreKind = tracker.ScoreFinal
		if score >= 4.5 {
			eligible = 1
		}
	}
	var id int64
	err = h.srv.store.Load().Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO report_artifacts (report_number, kind, path, status, content_hash, created_at, updated_at)
			VALUES (?, 'report', ?, 'ready', 'h', 'x', 'x')`, n, path); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO report_index (report_number, kind, company, role, posting_url, identity_key, score, score_kind, recommendation, legitimacy, verification, role_family, markdown_path, html_path, job_id, documents_eligible, created_at)
			VALUES (?, ?, 'Example Corp', ?, ?, ?, ?, ?, 'apply', 'credible', 'pasted unverified', '', ?, ?, 'j', ?, 'x')`,
			n, kind, role, url, key, score, string(scoreKind), path, strings.TrimSuffix(path, ".md")+".html", eligible); err != nil {
			return err
		}
		res, err := h.domainOf().tracker.UpsertReportIn(ctx, tx, tracker.Report{Number: n, Date: "2026-01-02", Score: &score, Kind: scoreKind,
			Outcome: tracker.OutcomeRecommended, Posting: identity.Posting{Company: "Example Corp", Role: role, URL: url}})
		id = res.Entry.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (d docsHarness) override(candidacy int64) {
	d.t.Helper()
	r := d.do(call{method: "POST", path: fmt.Sprintf("/api/candidacies/%d/documents-override", candidacy), cookie: d.session(), headers: map[string]string{"Origin": d.srv.Origin()}})
	if r.StatusCode != 200 {
		d.t.Fatalf("override: %d %s", r.StatusCode, r.body)
	}
}

// coverOnlyEligible sets the document settings to write a cover letter a form
// asks for only above the score floor.
func (d docsHarness) coverOnlyEligible() {
	d.t.Helper()
	var cur wire.Success[wire.DocumentPreferences]
	strictInto(d.t, d.get("/api/documents/preferences").body, &cur)
	if r := d.send("PATCH", "/api/documents/preferences", map[string]any{"revision": cur.Data.Revision, "coverWhenAsked": "eligible"}); r.StatusCode != 200 {
		d.t.Fatalf("cover letter setting: %d %s", r.StatusCode, r.body)
	}
}

// By default the chain's letter clears the floor and no row offers an
// override. With letters only above the floor, the apply list offers the
// override on a final report below it, never on a screening report (no
// override covers it). The chain's letter reads the floor when it is written,
// so an override recorded before a run starts is the one that run's letter
// sees.
func TestDocumentFloor_TheListOffersTheOverrideAndTheChainReadsItAtWriteTime(t *testing.T) {
	d := newDocsHarness(t, nil)
	below := seedReport(t, d.harness, 21, "final", 4.1, "Systems Engineer", finalReport(21, "Systems Engineer", "Linux.", "Linux.", "CV: Linux.", "Linux"))
	seedReport(t, d.harness, 22, "screening", 3.8, "Data Engineer", "report: 0022\nscore_kind: screening\n\n# Example Corp - Data Engineer\n")
	seedReport(t, d.harness, 23, "final", 4.6, "Platform Engineer", finalReport(23, "Platform Engineer", "Go.", "Go.", "CV: Go.", "Go"))
	offers := func() []string {
		var out []string
		for _, l := range decode[wire.ApplyList](t, d.get("/api/apply/list")).Lanes {
			for _, it := range l.Items {
				if it.LetterOffer {
					out = append(out, it.Role)
				}
			}
		}
		return out
	}
	letters := applyLetters{s: d.srv, d: d.domainOf()}
	var missing *toolchain.UnavailableError
	if got := offers(); len(got) != 0 {
		t.Fatalf("by default: %v", got)
	}
	if err := letters.Write(t.Context(), below); !errors.As(err, &missing) {
		t.Fatalf("by default the letter clears the floor, and the next gate is the harness's missing toolchain: %v", err)
	}
	d.coverOnlyEligible()
	if got := offers(); !slices.Equal(got, []string{"Systems Engineer"}) {
		t.Fatalf("before the override: %v", got)
	}
	var floor *documents.BelowFloorError
	if err := letters.Write(t.Context(), below); !errors.As(err, &floor) || floor.Report != 21 {
		t.Fatalf("the letter below the floor: %v", err)
	}
	d.override(below)
	if got := offers(); len(got) != 0 {
		t.Fatalf("after the override: %v", got)
	}
	if err := letters.Write(t.Context(), below); !errors.As(err, &missing) {
		t.Fatalf("past the floor, the next gate is the harness's missing toolchain: %v", err)
	}
}

func TestDocumentRoutes_CoverLetterRunsAndRefusesATwoPageLimit(t *testing.T) {
	d := newDocsHarness(t, nil)
	r := d.start("/api/documents/cover-letter", map[string]any{"candidacyId": atoi(d.cand), "posting": docsPosting})
	if r.StatusCode != http.StatusAccepted {
		t.Fatalf("%d %s", r.StatusCode, r.body)
	}
	if run := d.finish(r.data()["runId"].(string)); run.Status != wire.RunSucceeded || len(run.Documents) != 1 {
		t.Fatalf("run = %+v", run)
	}
	if docs := d.list(); len(docs) != 1 || docs[0].Kind != "cover-letter" {
		t.Fatalf("documents = %+v", docs)
	}
	if r := d.start("/api/documents/cover-letter", map[string]any{"candidacyId": atoi(d.cand), "pages": 2}); r.StatusCode != 422 {
		t.Errorf("pages: %d %s", r.StatusCode, r.body)
	}
}

func TestDocumentRoutes_PDFTakesAJSONBodyAndNeedsTheToolchain(t *testing.T) {
	d := newDocsHarness(t, nil)
	d.eligible()
	docs := d.generate()
	path := "/api/documents/" + itoa(int(docs[0].ID)) + "/pdf"
	// The toolchain is missing: the request is refused before any run exists.
	runs := d.runCount()
	r := d.start(path, map[string]any{})
	if r.StatusCode != http.StatusServiceUnavailable || r.errReason() != wire.ReasonToolchainUnavailable || !contains(string(r.body), "lualatex") {
		t.Errorf("missing toolchain: %d %s", r.StatusCode, r.body)
	}
	if d.runCount() != runs {
		t.Error("a refused PDF request created a run")
	}
	// The body is part of the contract.
	if r := d.do(call{method: "POST", path: path, useToken: true, headers: map[string]string{"Idempotency-Key": freshKey()}}); r.StatusCode != 415 {
		t.Errorf("no body: %d %s", r.StatusCode, r.body)
	}
	if r := d.start("/api/documents/9999/pdf", map[string]any{}); r.StatusCode != 404 || r.errCode() != "not_found" {
		t.Errorf("unknown document: %d %s", r.StatusCode, r.body)
	}
}

func TestDocumentRoutes_PDFRunIsAcceptedWithAToolchain(t *testing.T) {
	// A toolchain with no tools: the run is accepted, the build fails, and the
	// version is published without a PDF.
	opener := func(context.Context) (*toolchain.Toolchain, error) {
		return &toolchain.Toolchain{Root: toolchain.Root(t.TempDir())}, nil
	}
	d := newDocsHarness(t, opener)
	d.eligible()
	docs := d.generate()
	r := d.start("/api/documents/"+itoa(int(docs[0].ID))+"/pdf", map[string]any{})
	id, _ := r.data()["runId"].(string)
	if r.StatusCode != http.StatusAccepted || id == "" || r.Header.Get("Location") != "/api/runs/"+id {
		t.Fatalf("%d %v %s", r.StatusCode, r.Header, r.body)
	}
	run := d.finish(id)
	if run.Status != wire.RunSucceeded || len(run.Documents) != 2 || !strings.Contains(strings.Join(run.Warnings, "|"), "no PDF could be verified") {
		t.Fatalf("run = %+v", run)
	}
	if after := d.list(); len(after) != 4 {
		t.Errorf("documents = %d, want the draft version and the new one", len(after))
	}
}

func TestDocumentRoutes_DeleteKeepsTheRowAndRefusesAFrozenVersionUnlessConfirmed(t *testing.T) {
	d := newDocsHarness(t, nil)
	docs := d.generate()
	resume, letter := docs[0], docs[1]
	if resume.Kind != "resume" {
		resume, letter = letter, resume
	}
	// A plain delete removes one document and returns what is left.
	r := d.send("DELETE", "/api/documents/"+itoa(int(letter.ID)), nil)
	var left wire.Success[wire.DocumentList]
	strictInto(t, r.body, &left)
	if r.StatusCode != 200 || len(left.Data.Documents) != 1 || left.Data.Documents[0].ID != resume.ID {
		t.Fatalf("delete: %d %s", r.StatusCode, r.body)
	}
	if tr := d.get("/api/candidacies/" + d.cand); tr.StatusCode != 200 {
		t.Errorf("the tracker row was deleted: %d %s", tr.StatusCode, tr.body)
	}
	// A version attached to a submitted application is frozen.
	if err := d.domainOf().docs.Freeze(context.Background(), int64(atoi(d.cand)), int64(resume.ID)); err != nil {
		t.Fatal(err)
	}
	path := "/api/documents/" + itoa(int(resume.ID))
	if r := d.send("DELETE", path, nil); r.StatusCode != 409 || r.errCode() != "conflict" {
		t.Errorf("frozen delete: %d %s", r.StatusCode, r.body)
	}
	if r := d.send("DELETE", path+"?submitted=other", nil); r.StatusCode != 409 {
		t.Errorf("only submitted=confirm lifts the freeze: %d %s", r.StatusCode, r.body)
	}
	if docs := d.list(); len(docs) != 1 || !docs[0].Frozen {
		t.Fatalf("the frozen version must survive the refusals: %+v", docs)
	}
	r = d.send("DELETE", path+"?submitted=confirm", nil)
	strictInto(t, r.body, &left)
	if r.StatusCode != 200 || len(left.Data.Documents) != 0 {
		t.Errorf("confirmed delete: %d %s", r.StatusCode, r.body)
	}
}

func TestDocumentRoutes_PreferencesAreRevisionedAndValidated(t *testing.T) {
	d := newDocsHarness(t, nil)
	var cur wire.Success[wire.DocumentPreferences]
	r := d.get("/api/documents/preferences")
	strictInto(t, r.body, &cur)
	if r.StatusCode != 200 || cur.Data.Language != "en" || cur.Data.Paper != "a4" || cur.Data.ResumePages != 2 || cur.Data.CoverWhenAsked != "always" || cur.Data.Revision < 1 {
		t.Fatalf("defaults: %d %s", r.StatusCode, r.body)
	}
	rev := cur.Data.Revision
	r = d.send("PATCH", "/api/documents/preferences", map[string]any{"revision": rev, "paper": "letter", "resumePages": 1})
	strictInto(t, r.body, &cur)
	if r.StatusCode != 200 || cur.Data.Paper != "letter" || cur.Data.ResumePages != 1 || cur.Data.Revision != rev+1 {
		t.Fatalf("patch: %d %s", r.StatusCode, r.body)
	}
	// A stale edit is a conflict and changes nothing.
	if r := d.send("PATCH", "/api/documents/preferences", map[string]any{"revision": rev, "paper": "a4"}); r.StatusCode != 409 || r.errCode() != "conflict" {
		t.Errorf("stale revision: %d %s", r.StatusCode, r.body)
	}
	if r := d.send("PATCH", "/api/documents/preferences", map[string]any{"revision": rev + 1, "paper": "legal"}); r.StatusCode != 422 || r.errCode() != "validation_failed" {
		t.Errorf("bad paper: %d %s", r.StatusCode, r.body)
	}
}

// A candidacy has one document run at a time; the list says which run is
// working, and a refusal names it, so a drawer opened later can follow it.
func TestDocumentRoutes_OneRunAtATimePerCandidacy(t *testing.T) {
	d := newDocsHarness(t, nil)
	docs := d.generate()
	resume := d.pick(docs, "resume")
	working := func(runID, kind string, req documents.Request) {
		t.Helper()
		ctx := context.Background()
		input, _ := json.Marshal(req)
		err := d.srv.store.Load().Write(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `INSERT INTO runs (id, kind, status, input_json, created_at, updated_at) VALUES (?, 'documents', 'running', ?, 'x', 'x')`, runID, string(input)); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO document_jobs (run_id, kind, candidacy_id, created_at) VALUES (?, ?, ?, 'x')`, runID, kind, d.cand)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	working("run_docs", "resume", documents.Request{Action: documents.ActionResume, CandidacyID: int64(atoi(d.cand))})
	var env wire.Success[wire.DocumentList]
	strictInto(t, d.get("/api/documents?candidacy="+d.cand).body, &env)
	if env.Data.ActiveRunID != "run_docs" {
		t.Errorf("list active run = %q", env.Data.ActiveRunID)
	}
	src, _ := d.source(resume.ID)
	src.Resume.Experience[0].Bullets[1].Text = "Mentored four engineers through code review and design"
	for name, r := range map[string]result{
		"regenerate": d.start("/api/documents/resume", map[string]any{"candidacyId": atoi(d.cand), "posting": docsPosting}),
		"edit":       d.start("/api/documents/"+itoa(int(resume.ID))+"/edit", map[string]any{"resume": src.Resume}),
	} {
		// The refusal names the working run, so a caller can follow it.
		e, _ := r.json()["error"].(map[string]any)
		if d, _ := e["details"].(map[string]any); r.StatusCode != http.StatusConflict || r.errReason() != wire.ReasonDocumentRunActive || d["runId"] != "run_docs" {
			t.Errorf("%s during a run: %d %s", name, r.StatusCode, r.body)
		}
	}
}

// storePDF gives a stored draft a PDF and the verdict a real run would have
// written for it, without compiling anything.
func (d docsHarness) storePDF(doc wire.DocumentView, state string) string {
	d.t.Helper()
	pdf := []byte("%PDF-1.7 synthetic " + doc.Kind)
	sum := sha256.Sum256(pdf)
	pdfSHA := hex.EncodeToString(sum[:])
	dir := filepath.Join(d.ws.Root, filepath.FromSlash(doc.Path))
	record := map[string]any{"schemaVersion": 1, "verifications": map[string]any{doc.Kind: map[string]any{
		"schemaVersion": 1, "kind": doc.Kind, "state": state, "pages": 1, "pdfSha256": pdfSHA, "texSha256": strings.Repeat("a", 64),
		"sourceSha256": doc.ContentHash, "manifestHash": doc.ManifestHash, "template": "t1",
		"verifiers": map[string]any{"rules": "1", "pdfScript": "2", "layoutScript": "2", "layoutProfile": "resume-a4"},
		"checks": []any{
			map[string]any{"tool": "verify_pdf", "id": "pdf-readable", "status": "pass"},
			map[string]any{"tool": "verify_layout", "id": "clipping", "status": "fail", "reason": "p1 text extends outside the page"},
			map[string]any{"tool": "final", "id": "contact", "status": "unverified", "reason": "the text layer could not be extracted"},
		},
		"previews":     []any{map[string]any{"page": 1, "name": doc.Kind + "-page-1.png", "sha256": strings.Repeat("b", 64)}},
		"visualReview": map[string]any{"status": "pending"},
	}}}
	raw, err := json.Marshal(record)
	if err != nil {
		d.t.Fatal(err)
	}
	for name, content := range map[string][]byte{doc.Kind + ".pdf": pdf, "pdf-verification.json": raw} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			d.t.Fatal(err)
		}
	}
	st, err := d.srv.db()
	if err != nil {
		d.t.Fatal(err)
	}
	// A stored version is written once, so the PDF arrives as the next version
	// of the same sources, as a real PDF run publishes it.
	err = st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO documents (bundle, version, kind, mode, candidacy_id, path, source_path, content_hash, manifest_hash, generation,
			language, paper, page_limit, limited, pdf_state, pdf_path, pdf_sha256, pages, warnings_json, run_id, created_at)
			SELECT bundle, version + 1, kind, mode, candidacy_id, path, source_path, content_hash, manifest_hash, generation,
			language, paper, page_limit, limited, ?, ?, ?, 1, warnings_json, run_id, created_at FROM documents WHERE id = ?`,
			state, doc.Path+"/"+doc.Kind+".pdf", pdfSHA, int(doc.ID))
		return err
	})
	if err != nil {
		d.t.Fatal(err)
	}
	return filepath.Join(dir, doc.Kind+".pdf")
}
