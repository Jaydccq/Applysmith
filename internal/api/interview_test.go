package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/notify"
)

func (d docsHarness) interviewPath(rest string) string {
	return "/api/candidacies/" + d.cand + "/interview" + rest
}

func (d docsHarness) addStage(id, typ string, confirmed bool) result {
	d.t.Helper()
	return d.send("POST", d.interviewPath("/stages"), map[string]any{"id": id, "type": typ, "format": "video", "interviewers": []string{"Sam Sample"}, "userConfirmed": confirmed, "expectedRevision": 0})
}

func (d docsHarness) workspaceView() wire.InterviewWorkspace {
	d.t.Helper()
	r := d.get(d.interviewPath(""))
	var env wire.Success[wire.InterviewWorkspace]
	strictInto(d.t, r.body, &env)
	if r.StatusCode != 200 {
		d.t.Fatalf("workspace: %d %s", r.StatusCode, r.body)
	}
	return env.Data
}

func TestInterviewRoutes_EmptyWorkspaceThenStage(t *testing.T) {
	d := newDocsHarness(t, nil)
	w := d.workspaceView()
	if len(w.Stages) != 0 || w.Degraded || len(w.NextActions) != 1 || w.NextActions[0].Kind != "stage" || !strings.Contains(w.NextActions[0].Text, "Add an interview stage") {
		t.Fatalf("empty workspace = %+v", w)
	}
	r := d.addStage("tech-1", "technical", false)
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("add stage: %d %s", r.StatusCode, r.body)
	}
	w = d.workspaceView()
	if len(w.Stages) != 1 || w.Stages[0].ID != "tech-1" || w.Stages[0].UserConfirmed {
		t.Fatalf("workspace = %+v", w)
	}
	if r := d.get("/api/candidacies/999/interview"); r.StatusCode != 404 || r.errCode() != "not_found" {
		t.Errorf("unknown candidacy: %d %s", r.StatusCode, r.body)
	}
	// A second tab adding the same name, or cancelling from a stale read, is refused.
	if r := d.addStage("tech-1", "technical", true); r.StatusCode != 409 {
		t.Errorf("re-add over a recorded stage: %d %s", r.StatusCode, r.body)
	}
	read := w.Stages[0].Revision
	cancel := map[string]any{"id": "tech-1", "type": "technical", "cancelled": true, "expectedRevision": read}
	if r := d.send("POST", d.interviewPath("/stages"), cancel); r.StatusCode != http.StatusCreated {
		t.Fatalf("cancel: %d %s", r.StatusCode, r.body)
	}
	if r := d.send("POST", d.interviewPath("/stages"), cancel); r.StatusCode != 409 || r.errCode() != "conflict" {
		t.Errorf("stale cancel: %d %s", r.StatusCode, r.body)
	}
	if w = d.workspaceView(); len(w.Stages) != 0 || len(w.Cancelled) != 1 || len(w.NextActions) != 1 {
		t.Errorf("a cancelled stage is history only: %+v", w)
	}
	if r := d.send("POST", d.interviewPath("/stages/tech-1/feedback"), map[string]any{"text": "x"}); r.StatusCode != 422 {
		t.Errorf("feedback for a cancelled stage: %d %s", r.StatusCode, r.body)
	}
}

// Home's list holds the stages of open applications whose time falls on the
// local today or tomorrow, soonest first; a cancelled stage, one on another
// day and one of a rejected application are left out.
func TestInterviewRoutes_SoonListsTodayAndTomorrow(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.Local)
	d := newDocsHarness(t, nil, func(c *Config) { c.Now = func() time.Time { return now } })
	at := func(days, hour int) string {
		return time.Date(2026, 10, 12+days, hour, 30, 0, 0, time.Local).UTC().Format(time.RFC3339)
	}
	stage := func(cand, id, when string, cancelled bool) {
		body := map[string]any{"id": id, "type": "technical", "at": when, "userConfirmed": true, "expectedRevision": 0}
		if r := d.send("POST", "/api/candidacies/"+cand+"/interview/stages", body); r.StatusCode != http.StatusCreated {
			t.Fatalf("stage %s: %d %s", id, r.StatusCode, r.body)
		}
		if cancelled {
			rev := d.workspaceView().Stages[0].Revision
			body["cancelled"], body["expectedRevision"] = true, rev
			if r := d.send("POST", "/api/candidacies/"+cand+"/interview/stages", body); r.StatusCode != http.StatusCreated {
				t.Fatalf("cancel %s: %d %s", id, r.StatusCode, r.body)
			}
		}
	}
	stage(d.cand, "dropped", at(1, 10), true)
	stage(d.cand, "yesterday", at(-1, 23), false)
	stage(d.cand, "tomorrow", at(1, 23), false)
	stage(d.cand, "today", at(0, 0), false)
	stage(d.cand, "later", at(2, 0), false)
	d.send("POST", d.interviewPath("/stages/today/feedback"), map[string]any{"text": "Asked about on-call."})
	other := idOf(d.addCandidacy("Other Corp", "Engineer", "https://jobs.example.com/other"))
	stage(other, "tomorrow", at(1, 8), false)
	if err := d.srv.store.Load().Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE tracker_entries SET status = 'rejected' WHERE id = ?`, other)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r := d.get("/api/interviews/soon")
	var env wire.Success[wire.InterviewsSoon]
	strictInto(t, r.body, &env)
	var got []string
	for _, i := range env.Data.Items {
		got = append(got, fmt.Sprintf("%s debriefed=%v", i.Stage.ID, i.Debriefed))
	}
	if want := []string{"today debriefed=true", "tomorrow debriefed=false"}; !slices.Equal(got, want) {
		t.Errorf("soon = %v, want %v", got, want)
	}
}

func TestInterviewRoutes_StageRefusals(t *testing.T) {
	d := newDocsHarness(t, nil)
	if r := d.addStage("Tech 1", "technical", true); r.StatusCode != 422 || r.errCode() != "validation_failed" {
		t.Errorf("bad id: %d %s", r.StatusCode, r.body)
	}
	if r := d.addStage("tech-1", "coffee", true); r.StatusCode != 422 {
		t.Errorf("bad type: %d %s", r.StatusCode, r.body)
	}
	r := d.send("POST", "/api/candidacies/999/interview/stages", map[string]any{"id": "tech-1", "type": "technical", "expectedRevision": 0})
	if r.StatusCode != 404 || r.errCode() != "not_found" {
		t.Errorf("unknown candidacy: %d %s", r.StatusCode, r.body)
	}
	if r := d.send("POST", d.interviewPath("/stages/nope-1/feedback"), map[string]any{"text": "x"}); r.StatusCode != 404 {
		t.Errorf("feedback for a missing stage: %d %s", r.StatusCode, r.body)
	}
}

func TestInterviewRoutes_PackRunFeedbackAndMockTurn(t *testing.T) {
	d := newDocsHarness(t, nil)
	d.eligible()
	d.generate()
	if r := d.addStage("onsite-1", "behavioral", true); r.StatusCode != http.StatusCreated {
		t.Fatalf("add stage: %d %s", r.StatusCode, r.body)
	}
	start := func() wire.DocumentRunView {
		r := d.start(d.interviewPath("/stages/onsite-1/pack"), map[string]any{})
		id, _ := r.data()["runId"].(string)
		if r.StatusCode != http.StatusAccepted || id == "" || r.Header.Get("Location") != "/api/runs/"+id {
			t.Fatalf("start pack: %d %v %s", r.StatusCode, r.Header, r.body)
		}
		return d.finish(id)
	}
	run := start()
	if run.Status != wire.RunSucceeded || run.Pack == nil || run.Pack.Revision != 1 || run.Pack.Stage != "onsite-1" {
		t.Fatalf("run = %+v", run)
	}
	r := d.get(d.interviewPath("/stages/onsite-1/pack"))
	var pack wire.Success[wire.InterviewPack]
	strictInto(t, r.body, &pack)
	if r.StatusCode != 200 || pack.Data.Revision != 1 || len(pack.Data.Questions) < 4 || pack.Data.Generation != "deterministic" {
		t.Fatalf("pack: %d %s", r.StatusCode, r.body)
	}
	if r := d.send("POST", d.interviewPath("/stages/onsite-1/feedback"), map[string]any{"from": "interviewer", "text": "Strong on migrations."}); r.StatusCode != http.StatusCreated {
		t.Fatalf("feedback: %d %s", r.StatusCode, r.body)
	}
	second := start()
	if second.Pack == nil || second.Pack.Revision != 2 {
		t.Fatalf("second run = %+v", second)
	}
	// Its notice says which revision it stored.
	if dom, err := d.srv.dom(); err != nil {
		t.Fatal(err)
	} else if got := dom.narrator.outcome(context.Background(), notify.KindDocuments, second.RunID); got != "Prep pack revision 2 is ready." {
		t.Errorf("outcome = %q", got)
	}
	r = d.get(d.interviewPath("/stages/onsite-1/pack"))
	strictInto(t, r.body, &pack)
	if len(pack.Data.PriorFeedback) != 1 || !strings.Contains(pack.Data.PriorFeedback[0], "Strong on migrations") {
		t.Fatalf("the next pack lacks the feedback: %s", r.body)
	}
	r = d.get(d.interviewPath("/stages/onsite-1/pack?revision=1"))
	strictInto(t, r.body, &pack)
	if pack.Data.Revision != 1 || len(pack.Data.PriorFeedback) != 0 {
		t.Fatalf("revision 1 changed: %s", r.body)
	}
	if r := d.get(d.interviewPath("/stages/onsite-1/pack?revision=9")); r.StatusCode != 404 {
		t.Errorf("missing revision: %d %s", r.StatusCode, r.body)
	}
	// Coaching is a background run; a refused answer starts none.
	runs := d.runCount()
	if r := d.start(d.interviewPath("/stages/onsite-1/mock-turns"), map[string]any{"question": "Q", "answer": " "}); r.StatusCode != 422 || d.runCount() != runs {
		t.Fatalf("empty answer: %d %s", r.StatusCode, r.body)
	}
	r = d.start(d.interviewPath("/stages/onsite-1/mock-turns"), map[string]any{"question": "Tell me about a migration.", "answer": "I led a team of 40 engineers through the platform migration."})
	id, _ := r.data()["runId"].(string)
	if r.StatusCode != http.StatusAccepted || id == "" {
		t.Fatalf("coach: %d %s", r.StatusCode, r.body)
	}
	if end := d.waitRun(id); end["status"] != "succeeded" {
		t.Fatalf("coach run = %v", end)
	}
	w := d.workspaceView()
	if len(w.Packs) != 2 || len(w.Feedback) != 1 || len(w.MockTurns) != 1 || !strings.Contains(w.MockTurns[0].Coaching, "40") || w.CoachRun != nil {
		t.Errorf("workspace = %+v", w)
	}
	// A coaching still working comes back with its answer, for this application only.
	err := d.srv.store.Load().Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO runs (id, kind, status, input_json, created_at, updated_at) VALUES ('c1', 'interview.coach', 'running', ?, 'x', 'x'), ('c2', 'interview.coach', 'running', '{"candidacy":999}', 'y', 'y')`,
			`{"candidacy":`+d.cand+`,"stage":"onsite-1","question":"Q","answer":"A"}`)
		return err
	})
	if w := d.workspaceView(); err != nil || w.CoachRun == nil || *w.CoachRun != (wire.InterviewCoachRun{RunID: "c1", Stage: "onsite-1", Question: "Q", Answer: "A"}) {
		t.Errorf("active coaching = %+v, %v", w.CoachRun, err)
	}
}

func TestInterviewRoutes_PackRunRefusals(t *testing.T) {
	d := newDocsHarness(t, nil)
	if r := d.start(d.interviewPath("/stages/missing-1/pack"), map[string]any{}); r.StatusCode != 404 || r.errCode() != "not_found" {
		t.Errorf("missing stage: %d %s", r.StatusCode, r.body)
	}
	if r := d.start("/api/candidacies/999/interview/stages/x-1/pack", map[string]any{}); r.StatusCode != 404 {
		t.Errorf("missing candidacy: %d %s", r.StatusCode, r.body)
	}
	if r := d.send("POST", d.interviewPath("/stages/missing-1/pack"), map[string]any{}); r.StatusCode != 400 || r.errCode() != "bad_request" {
		t.Errorf("no Idempotency-Key: %d %s", r.StatusCode, r.body)
	}
	if n := d.runCount(); n != 0 {
		t.Errorf("%d runs were created by refused requests", n)
	}
}
