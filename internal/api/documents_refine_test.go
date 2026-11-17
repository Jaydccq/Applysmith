package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"applysmith/internal/api/wire"
	"applysmith/internal/llm"
)

const refinedBullet = "Mentored four engineers through code review and design"

// refineHarness is a documents harness whose fake provider answers a résumé
// refinement with the reply the test stores; every other call fails, so the
// first pair is written by the deterministic builder.
type refineHarness struct {
	docsHarness
	reply *atomic.Value
}

func newRefineHarness(t *testing.T) refineHarness {
	t.Helper()
	reply := &atomic.Value{}
	fake := llm.NewFake()
	fake.SetGenerator(func(task, prompt string) (string, bool) {
		if strings.HasPrefix(prompt, "Task: resume-refine") {
			if s, ok := reply.Load().(string); ok {
				return s, true
			}
		}
		return "", false
	})
	d := newDocsHarness(t, nil, func(c *Config) { c.Domain.LLM = append(c.Domain.LLM, llm.WithFake(fake)) })
	return refineHarness{docsHarness: d, reply: reply}
}

// revise stores the model's next answer: the stored text of doc with its second
// bullet reworded.
func (h refineHarness) revise(doc wire.DocumentView) {
	h.t.Helper()
	src, _ := h.source(doc.ID)
	src.Resume.Experience[0].Bullets[1].Text = refinedBullet
	raw, err := json.Marshal(src.Resume)
	if err != nil {
		h.t.Fatal(err)
	}
	h.reply.Store(string(raw))
}

func (h refineHarness) refinement(id wire.SafeInt) wire.DocumentRefinement {
	h.t.Helper()
	r := h.get("/api/documents/refine/" + itoa(int(id)))
	var env wire.Success[wire.DocumentRefinement]
	strictInto(h.t, r.body, &env)
	if r.StatusCode != http.StatusOK {
		h.t.Fatalf("refinement: %d %s", r.StatusCode, r.body)
	}
	return env.Data
}

func (h refineHarness) refine(id wire.SafeInt, body any) (result, wire.DocumentRunView) {
	h.t.Helper()
	r := h.start("/api/documents/"+itoa(int(id))+"/refine", body)
	if r.StatusCode != http.StatusAccepted {
		return r, wire.DocumentRunView{}
	}
	return r, h.finish(r.data()["runId"].(string))
}

func (h refineHarness) find(id wire.SafeInt) wire.DocumentView {
	h.t.Helper()
	for _, d := range h.list() {
		if d.ID == id {
			return d
		}
	}
	h.t.Fatalf("document %d not listed", id)
	return wire.DocumentView{}
}

// The whole round trip over HTTP: a pending proposal with a diff, the PDF
// refused meanwhile, a discard that changes nothing, then an accept.
func TestDocumentRefine_RoundTrip(t *testing.T) {
	h := newRefineHarness(t)
	resume := h.pick(h.generate(), "resume")
	if r := h.refinement(resume.ID); r.Pending != nil || len(r.Turns) != 0 {
		t.Fatalf("fresh refinement = %+v", r)
	}
	h.revise(resume)
	r, run := h.refine(resume.ID, map[string]any{"instruction": "Reword the mentoring bullet"})
	if run.Status != wire.RunSucceeded || r.Header.Get("Location") == "" {
		t.Fatalf("refine: %d %s run=%+v", r.StatusCode, r.body, run)
	}
	got := h.refinement(resume.ID)
	p := got.Pending
	if p == nil || p.Resume == nil || p.Kind != "resume" || p.Instruction != "Reword the mentoring bullet" || !strings.Contains(p.Text, refinedBullet) {
		t.Fatalf("pending = %+v", p)
	}
	var added, removed bool
	for _, l := range p.Diff {
		added = added || (l.Op == "add" && strings.Contains(l.Text, refinedBullet))
		removed = removed || (l.Op == "del" && strings.Contains(l.Text, "Mentored four engineers in code review"))
	}
	if !added || !removed {
		t.Errorf("diff = %+v", p.Diff)
	}
	if v := h.find(resume.ID); !v.RefinePending || v.ContentHash != resume.ContentHash || !v.Current {
		t.Errorf("document while pending = %+v", v)
	}
	if n := len(h.list()); n != 2 {
		t.Errorf("documents = %d; a proposal is not a version", n)
	}

	// Scenario: Pending blocks the PDF.
	if r := h.start("/api/documents/"+itoa(int(resume.ID))+"/pdf", map[string]any{}); r.StatusCode != 409 || r.errCode() != "conflict" || r.errReason() != wire.ReasonProposalPending {
		t.Errorf("pdf while pending: %d %s", r.StatusCode, r.body)
	}
	if r := h.start("/api/documents/"+itoa(int(resume.ID))+"/refine", map[string]any{"instruction": "Again"}); r.StatusCode != 409 || r.errReason() != wire.ReasonProposalPending {
		t.Errorf("second refine while pending: %d %s", r.StatusCode, r.body)
	}

	// Scenario: Rejected proposal. The version is untouched and the turns carry the decision.
	d := h.start("/api/documents/"+itoa(int(resume.ID))+"/refine/discard", map[string]any{"proposalId": p.ID})
	var env wire.Success[wire.DocumentRefinement]
	strictInto(t, d.body, &env)
	if d.StatusCode != 200 || env.Data.Pending != nil || len(env.Data.Turns) != 2 || env.Data.Turns[1].Decision != "rejected" {
		t.Fatalf("discard: %d %s", d.StatusCode, d.body)
	}
	if v := h.find(resume.ID); v.RefinePending || v.ContentHash != resume.ContentHash {
		t.Errorf("document after discard = %+v", v)
	}
	if r := h.start("/api/documents/"+itoa(int(resume.ID))+"/refine/discard", map[string]any{"proposalId": p.ID}); r.StatusCode != 409 {
		t.Errorf("discarding twice: %d %s", r.StatusCode, r.body)
	}
	if r := h.start("/api/documents/"+itoa(int(resume.ID))+"/pdf", map[string]any{}); r.errReason() == wire.ReasonProposalPending {
		t.Errorf("pdf after the decision is still blocked: %s", r.body)
	}

	// A second instruction, then accept: a new version, no PDF, the earlier one not current.
	_, run = h.refine(resume.ID, map[string]any{"instruction": "Reword it again", "turns": env.Data.Turns})
	if run.Status != wire.RunSucceeded {
		t.Fatalf("second refine run = %+v", run)
	}
	p = h.refinement(resume.ID).Pending
	if p == nil {
		t.Fatal("no second proposal")
	}
	a := h.start("/api/documents/"+itoa(int(resume.ID))+"/refine/accept", map[string]any{"proposalId": p.ID})
	if a.StatusCode != http.StatusAccepted {
		t.Fatalf("accept: %d %s", a.StatusCode, a.body)
	}
	if run := h.finish(a.data()["runId"].(string)); run.Status != wire.RunSucceeded || len(run.Documents) != 2 {
		t.Fatalf("accept run = %+v", run)
	}
	var fresh wire.DocumentView
	for _, v := range h.list() {
		if v.Kind == "resume" && v.Version == 2 {
			fresh = v
		}
	}
	if fresh.ID == 0 || fresh.PDFState != "none" || !fresh.Current || fresh.Generation != "model" {
		t.Fatalf("new version = %+v", fresh)
	}
	if got, _ := h.source(fresh.ID); got.Resume.Experience[0].Bullets[1].Text != refinedBullet {
		t.Errorf("accepted text = %+v", got.Resume)
	}
	if old := h.find(resume.ID); old.Current || old.RefinePending || old.ContentHash != resume.ContentHash {
		t.Errorf("earlier version = %+v", old)
	}
	if r := h.refinement(resume.ID); r.Pending != nil || r.Turns[len(r.Turns)-1].Decision != "accepted" {
		t.Errorf("refinement after accept = %+v", r)
	}
}

func TestDocumentRefine_RequestValidation(t *testing.T) {
	h := newRefineHarness(t)
	resume := h.pick(h.generate(), "resume")
	path := "/api/documents/" + itoa(int(resume.ID)) + "/refine"
	for name, body := range map[string]any{
		"empty instruction": map[string]any{"instruction": ""},
		"bad decision":      map[string]any{"instruction": "go", "turns": []any{map[string]any{"kind": "decision", "decision": "maybe"}}},
	} {
		if r := h.start(path, body); r.StatusCode != 422 {
			t.Errorf("%s: %d %s", name, r.StatusCode, r.body)
		}
	}
	if r := h.start("/api/documents/9999/refine", map[string]any{"instruction": "go"}); r.StatusCode != 404 {
		t.Errorf("unknown document: %d %s", r.StatusCode, r.body)
	}
	if r := h.start(path+"/accept", map[string]any{"proposalId": 9999}); r.StatusCode != 404 {
		t.Errorf("unknown proposal: %d %s", r.StatusCode, r.body)
	}
	if r := h.start(path+"/accept", map[string]any{}); r.StatusCode != 422 {
		t.Errorf("accept without an id: %d %s", r.StatusCode, r.body)
	}
}

// A model whose answer is not a résumé leaves the draft untouched
// and the error shows as a turn.
func TestDocumentRefine_FailedRevisionShowsAnErrorTurn(t *testing.T) {
	h := newRefineHarness(t)
	resume := h.pick(h.generate(), "resume")
	h.reply.Store("not json")
	_, run := h.refine(resume.ID, map[string]any{"instruction": "Reword"})
	if run.Status != wire.RunFailed {
		t.Fatalf("run = %+v", run)
	}
	r := h.refinement(resume.ID)
	if r.Pending != nil || len(r.Turns) != 2 || r.Turns[1].Kind != "error" || r.Turns[1].Text == "" {
		t.Errorf("refinement = %+v", r)
	}
	if v := h.find(resume.ID); v.RefinePending || v.ContentHash != resume.ContentHash {
		t.Errorf("document = %+v", v)
	}
}
