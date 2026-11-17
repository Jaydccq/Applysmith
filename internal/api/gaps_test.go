package api

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"applysmith/internal/api/wire"
	"applysmith/internal/kb"
	"applysmith/internal/research"
)

// evaluateAs evaluates a synthetic posting of a distinct company and returns its report number.
func (h *harness) evaluateAs(company string) int {
	h.t.Helper()
	text := strings.ReplaceAll(postingBody(company), "Example Systems", company)
	r := h.startEvaluation(freshKey(), map[string]any{"text": text, "url": "https://jobs.example.com/p/" + strings.ToLower(company), "company": company, "title": "Software Engineer " + company})
	if r.StatusCode != 202 {
		h.t.Fatalf("start: %d %s", r.StatusCode, r.body)
	}
	out := h.waitEvaluation(r.data()["runId"].(string))
	if out["reportNumber"] == nil {
		h.t.Fatalf("no report: %v", out)
	}
	return int(out["reportNumber"].(float64))
}

func (h *harness) startGaps(key string, reports ...int) result {
	h.t.Helper()
	nums := make([]int, len(reports))
	copy(nums, reports)
	return h.send("POST", "/api/evaluations/gaps", map[string]any{"reports": nums}, "Idempotency-Key", key)
}

func (h *harness) gapReport(id string) wire.GapReport {
	h.t.Helper()
	r := h.get("/api/evaluations/gaps/" + id)
	var env wire.Success[wire.GapReport]
	strictInto(h.t, r.body, &env)
	if r.StatusCode != 200 {
		h.t.Fatalf("get gaps: %d %s", r.StatusCode, r.body)
	}
	return env.Data
}

// treeHash covers the user's files and nothing under the state directory.
func treeHash(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		rel, _ := filepath.Rel(root, path)
		if err != nil || d.IsDir() || strings.HasPrefix(rel, ".applysmith") || strings.HasPrefix(rel, "reports") {
			return err
		}
		b, err := os.ReadFile(path)
		fmt.Fprintf(h, "%s\n%x\n", rel, sha256.Sum256(b))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func TestSkillGapRoutesRunUnderTheRunContract(t *testing.T) {
	h := newDomainHarness(t)
	a, b := h.evaluateAs("Northwind"), h.evaluateAs("Contoso")
	key := freshKey()
	r := h.startGaps(key, a, b)
	var accepted wire.Success[wire.RunAccepted]
	strictInto(t, r.body, &accepted)
	if r.StatusCode != 202 || accepted.Data.RunID == "" || r.Header.Get("Location") != "/api/runs/"+accepted.Data.RunID {
		t.Fatalf("start: %d %s %v", r.StatusCode, r.body, r.Header)
	}
	id := accepted.Data.RunID
	if run := h.get("/api/runs/" + id); run.data()["kind"] != "gap-report" || run.data()["status"] != "succeeded" {
		t.Fatalf("run: %s", run.body)
	}
	if again := h.startGaps(key, a, b); again.StatusCode != 202 || again.data()["runId"] != id {
		t.Fatalf("a repeated key must return the first run: %s", again.body)
	}
	if other := h.startGaps(key, a); other.StatusCode != 409 || other.errCode() != "conflict" {
		t.Fatalf("the same key with another selection: %d %s", other.StatusCode, other.body)
	}
	rep := h.gapReport(id)
	if rep.ID != id || len(rep.Sample) != 2 || len(rep.Excluded) != 0 || len(rep.Gaps) == 0 || rep.Plan != nil {
		t.Fatalf("report: %+v", rep)
	}
	for _, g := range rep.Gaps {
		if g.State == "confirmed-gap" || g.Required+g.Preferred == 0 || len(g.Sources) == 0 {
			t.Fatalf("a fresh analysis confirms nothing and counts demand: %+v", g)
		}
	}
	list := h.get("/api/evaluations/gaps")
	if items := list.data()["analyses"].([]any); list.StatusCode != 200 || len(items) != 1 || items[0].(map[string]any)["id"] != id {
		t.Fatalf("list: %s", list.body)
	}
	// The posting report catalog is unchanged by an analysis.
	if cat := h.get("/api/reports"); cat.data()["total"].(float64) != 2 {
		t.Fatalf("catalog: %s", cat.body)
	}
}

func TestSkillGapPlanNeedsConfirmationAndProgressTouchesNoProfile(t *testing.T) {
	h := newDomainHarness(t)
	a, b := h.evaluateAs("Northwind"), h.evaluateAs("Contoso")
	id := h.startGaps(freshKey(), a, b).data()["runId"].(string)
	skill := h.gapReport(id).Gaps[0].Skill
	base := "/api/evaluations/gaps/" + id

	if r := h.post(base+"/plan", map[string]any{"skills": []string{skill}}); r.StatusCode != 422 || r.errCode() != "validation_failed" {
		t.Fatalf("planning an unconfirmed gap: %d %s", r.StatusCode, r.body)
	}
	if r := h.post(base+"/confirm", map[string]any{"skill": "not a listed skill", "confirmed": true}); r.StatusCode != 422 {
		t.Fatalf("confirming a skill outside the analysis: %d %s", r.StatusCode, r.body)
	}
	var confirmed wire.Success[wire.GapReport]
	r := h.post(base+"/confirm", map[string]any{"skill": skill, "confirmed": true})
	strictInto(t, r.body, &confirmed)
	if r.StatusCode != 200 || confirmed.Data.Gaps[0].State != "confirmed-gap" {
		t.Fatalf("confirm: %d %s", r.StatusCode, r.body)
	}

	r = h.post(base+"/plan", map[string]any{"skills": []string{skill}, "lookupResources": true})
	var planned wire.Success[wire.GapReport]
	strictInto(t, r.body, &planned)
	plan := planned.Data.Plan
	if r.StatusCode != 200 || plan == nil || len(plan.Items) != 1 || plan.Lookup.Status != "unavailable" || len(plan.Items[0].Resources) != 0 ||
		plan.Items[0].Exercise == "" || plan.Items[0].Criterion == "" || !strings.HasPrefix(plan.Items[0].Effort, "Estimate:") {
		t.Fatalf("plan: %d %s", r.StatusCode, r.body)
	}

	before := treeHash(t, h.ws.Root)
	r = h.post(base+"/progress", map[string]any{"skill": skill, "done": true})
	var done wire.Success[wire.GapReport]
	strictInto(t, r.body, &done)
	if r.StatusCode != 200 || !done.Data.Plan.Items[0].Done || done.Data.Gaps[0].State != "confirmed-gap" {
		t.Fatalf("progress: %d %s", r.StatusCode, r.body)
	}
	if treeHash(t, h.ws.Root) != before {
		t.Fatal("completing an exercise changed a user file")
	}
}

func TestSkillGapRequestsAreValidated(t *testing.T) {
	h := newDomainHarness(t)
	n := h.evaluateAs("Northwind")
	if r := h.send("POST", "/api/evaluations/gaps", map[string]any{"reports": []int{n}}); r.StatusCode != 400 {
		t.Errorf("no Idempotency-Key: %d %s", r.StatusCode, r.body)
	}
	many := make([]int, 101)
	for i := range many {
		many[i] = i + 1
	}
	for name, reports := range map[string][]int{"none": {}, "more than 100": many} {
		if r := h.startGaps(freshKey(), reports...); r.StatusCode != 422 || r.errCode() != "validation_failed" {
			t.Errorf("%s: %d %s", name, r.StatusCode, r.body)
		}
	}
	if r := h.get("/api/evaluations/gaps/run_nothere"); r.StatusCode != 404 {
		t.Errorf("unknown analysis: %d %s", r.StatusCode, r.body)
	}
	if r := h.startGaps(freshKey(), n, 99999); r.StatusCode != 202 {
		t.Fatalf("an unknown report is an exclusion, not a failure: %d %s", r.StatusCode, r.body)
	}
}

type resourceSearch struct{}

func (resourceSearch) Search(context.Context, string, int) ([]research.Hit, error) {
	return []research.Hit{{URL: "https://docs.example/go", Title: "Go guide", Text: "Official Go practice exercises"}, {URL: "https://docs.example/missing", Title: "Missing", Text: "Missing resource"}}, nil
}

type resourceFetch struct{ calls []string }

func (f *resourceFetch) Fetch(_ context.Context, raw string) (*kb.Page, error) {
	f.calls = append(f.calls, raw)
	status := 200
	if strings.HasSuffix(raw, "missing") {
		status = 404
	}
	return &kb.Page{URL: raw, Status: status, Body: []byte("Go exercises")}, nil
}

func TestSkillGapResourcesResearchCitationsMustBeFetchedBeforeTheyAreListed(t *testing.T) {
	model := &liveModel{reply: "Use the official Go guide for practice [1]. This other resource offers more exercises [2]."}
	fetch := &resourceFetch{}
	finder := gapResources{service: &research.Service{Model: model, Search: func() research.Searcher { return resourceSearch{} }}, fetch: fetch}
	got, err := finder.Find(t.Context(), "Go", 2)
	if err != nil || len(got) != 1 || got[0].URL != "https://docs.example/go" || !got[0].Verified || len(fetch.calls) != 2 {
		t.Fatalf("resources: %+v, fetched=%v, err=%v", got, fetch.calls, err)
	}
	model.reply = "A resource with an invented citation [9]."
	if _, err := finder.Find(t.Context(), "Go", 2); err == nil {
		t.Fatal("invented research citations were accepted")
	}
	if len(fetch.calls) != 2 {
		t.Fatal("a refused research answer started fetching pages")
	}
}
