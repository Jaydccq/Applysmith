package documents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"applysmith/internal/interview"
)

func (f *fixture) interviewStage(id, typ string) {
	f.t.Helper()
	_, err := f.svc.Interview().RecordStage(context.Background(), f.cand, interview.StageInput{ID: id, Type: typ, Format: "video", UserConfirmed: true})
	must(f.t, err)
}

func (f *fixture) packRun(stage string) (Result, interview.Pack) {
	f.t.Helper()
	res := f.run(Request{Action: ActionInterview, CandidacyID: f.cand, Stage: stage})
	if !res.Published || res.Pack == nil {
		f.t.Fatalf("interview run = %+v", res)
	}
	p, err := f.svc.Interview().Pack(context.Background(), f.cand, stage, res.Pack.Revision)
	must(f.t, err)
	return res, p
}

func TestInterview_FrozenSources(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	first := f.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
	if !first.Published {
		t.Fatalf("resume run = %+v", first)
	}
	must(t, f.svc.Freeze(context.Background(), f.cand, first.Documents...))
	must(t, os.WriteFile(filepath.Join(f.ws.Root, "cv.md"), []byte(strings.Replace(testCV, "Built the ingestion service used by twelve teams", "Built a billing pipeline used by nine teams", 1)), 0o600))
	second := f.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
	if !second.Published {
		t.Fatalf("second resume run = %+v", second)
	}
	f.interviewStage("onsite-1", "behavioral")
	_, p := f.packRun("onsite-1")
	claims := strings.Join(p.Claims, "\n")
	if strings.Contains(claims, "billing pipeline") || !strings.Contains(claims, "ingestion service") {
		t.Errorf("claims = %v; they must come from the version attached to the submitted application", p.Claims)
	}
	for _, s := range p.Sources {
		if s.Kind == interview.KindDossier && s.State != interview.StateFrozen {
			t.Errorf("source %+v is not frozen", s)
		}
	}
	if !strings.Contains(p.RoleBrief, "Backend Engineer") {
		t.Errorf("role brief = %q", p.RoleBrief)
	}
	if len(p.Questions) < 4 {
		t.Errorf("questions = %+v; one per requirement is expected", p.Questions)
	}
}

func TestInterview_NoDocumentsUsesTheReportAndSaysSo(t *testing.T) {
	f := newFixture(t, fixtureOpts{retriever: briefRetriever{"Example Corp builds scheduling software for survey teams."}})
	f.addReport2()
	f.interviewStage("phone-1", "phone_screen")
	_, p := f.packRun("phone-1")
	kinds := map[string]string{}
	for _, s := range p.Sources {
		kinds[s.Kind] = s.State
	}
	if kinds[interview.KindReport] != interview.StateCurrent || kinds[interview.KindCompanyBrief] != interview.StateUnverified {
		t.Errorf("sources = %+v", p.Sources)
	}
	if p.ReportNumber != 7 {
		t.Errorf("report read = %d, want the row's report 7", p.ReportNumber)
	}
	if !strings.Contains(strings.Join(interview.Texts(p.Gaps), "|"), "No submitted résumé") || len(p.Hooks) != 1 {
		t.Errorf("gaps %v hooks %+v", p.Gaps, p.Hooks)
	}
	if len(p.Questions) != 2+3 {
		t.Errorf("questions = %d, want the 2 base questions and 3 requirement questions", len(p.Questions))
	}
}

func TestInterview_RunStagesAndLedgerTask(t *testing.T) {
	var mu sync.Mutex
	var stages []string
	m := &scriptModel{fn: func(task, prompt string) (any, error) {
		if task != "interview-prep" {
			return nil, errors.New("unexpected task " + task)
		}
		return map[string]any{"requirementPrompts": []any{}, "hooks": []any{}}, nil
	}}
	f := newFixture(t, fixtureOpts{model: m, emit: func(ev Event) {
		mu.Lock()
		defer mu.Unlock()
		if !ev.Final {
			stages = append(stages, ev.Stage)
		}
	}})
	f.addReport2()
	f.interviewStage("tech-1", "technical")
	res := f.run(Request{Action: ActionInterview, CandidacyID: f.cand, Stage: "tech-1"})
	if !res.Published || res.Pack == nil || res.Pack.Revision != 1 {
		t.Fatalf("result = %+v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(stages, ",") != "retrieve,draft,publish" {
		t.Errorf("stages = %v", stages)
	}
	if m.callsTo("interview-prep") != 1 {
		t.Errorf("model tasks = %v", m.tasks)
	}
	got, err := f.svc.Outcome(context.Background(), res.RunID)
	if err != nil || got.Pack == nil || got.Pack.Stage != "tech-1" {
		t.Errorf("stored outcome = %+v, %v", got, err)
	}
}

func (f *fixture) addReport2() {
	f.t.Helper()
	must(f.t, os.MkdirAll(filepath.Join(f.ws.Root, "reports"), 0o700))
	must(f.t, os.WriteFile(filepath.Join(f.ws.Root, "reports", "0007-example-corp.md"), []byte(testPosting), 0o600))
}

func TestInterview_RunRefusesUnknownStageAndCandidacy(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	ctx := context.Background()
	_, err := f.svc.Submit(ctx, Claim{Principal: "test", Key: "key-unknown-stage"}, Request{Action: ActionInterview, CandidacyID: f.cand, Stage: "nope-1"})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown stage: %v", err)
	}
	_, err = f.svc.Submit(ctx, Claim{Principal: "test", Key: "key-unknown-cand"}, Request{Action: ActionInterview, CandidacyID: 9999, Stage: "nope-1"})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown candidacy: %v", err)
	}
	_, err = f.svc.Submit(ctx, Claim{Principal: "test", Key: "key-no-stage"}, Request{Action: ActionInterview, CandidacyID: f.cand})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("no stage: %v", err)
	}
}
