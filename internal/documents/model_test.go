package documents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/llm"
	"applysmith/internal/store"
)

var evLine = regexp.MustCompile(`(?m)^(E\d+) \[(\w*)\] (.*)$`)

// echoModel answers a résumé call with the evidence of each entry, reworded
// only by a prefix, and a letter call with a grounded body. mutate lets a test
// corrupt the answer.
func echoModel(mutate func(task string, v any) any) *scriptModel {
	return &scriptModel{fn: func(task, prompt string) (any, error) {
		var v any
		switch task {
		case "resume-write":
			draft := ResumeDraft{ProfileEvidence: []string{}, Competencies: []Competency{}, Experience: []EntryDraft{}, Projects: []EntryDraft{}}
			byEntry := map[string][]Bullet{}
			var order []string
			for _, m := range evLine.FindAllStringSubmatch(prompt, -1) {
				if m[2] == "" {
					continue
				}
				if _, ok := byEntry[m[2]]; !ok {
					order = append(order, m[2])
				}
				byEntry[m[2]] = append(byEntry[m[2]], Bullet{Text: m[3], Evidence: []string{m[1]}})
			}
			for _, k := range order {
				ed := EntryDraft{Entry: k, Bullets: byEntry[k]}
				if strings.HasPrefix(k, "P") {
					draft.Projects = append(draft.Projects, ed)
				} else {
					draft.Experience = append(draft.Experience, ed)
				}
			}
			draft.Profile = "Backend engineer focused on reliable data platforms."
			v = draft
		case "cover-letter":
			var bullets []Bullet
			for _, m := range evLine.FindAllStringSubmatch(prompt, -1) {
				if m[2] != "" && len(bullets) < 2 {
					bullets = append(bullets, Bullet{Text: strings.TrimSuffix(m[3], ".") + ".", Evidence: []string{m[1]}})
				}
			}
			v = LetterDraft{
				Opening:         "I am applying for the Backend Engineer role at Example Corp because the work on backend services in Go matches what I have built.",
				OpeningEvidence: []string{}, Body: "Examples from my recent work are below.", Bullets: bullets, CompanyFacts: []string{},
				Closing: "I would be glad to discuss how this experience can help Example Corp. Thank you for reading.",
			}
		case "document-review":
			v = reviewReply{Factual: []reviewFinding{}, Narrative: []narrativeNote{}}
		default:
			return nil, errors.New("unexpected task " + task)
		}
		if mutate != nil {
			v = mutate(task, v)
		}
		return v, nil
	}}
}

func TestModelDraftAcceptedAndLedgerTasks(t *testing.T) {
	m := echoModel(nil)
	f := newFixture(t, fixtureOpts{model: m})
	res := f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
	if !res.Published || res.Generation != GenModel {
		t.Fatalf("result = %+v", res)
	}
	if got := strings.Join(m.tasks, ","); got != "resume-write,cover-letter,document-review" {
		t.Errorf("model tasks = %s; one drafting call per document, one review and no fan-out", got)
	}
	docs, _ := f.svc.List(t.Context(), f.cand)
	for _, d := range docs {
		if d.Generation != string(GenModel) {
			t.Errorf("%s generation = %s", d.Kind, d.Generation)
		}
	}
	if rec := f.read(docs[0].Path + "/document-review.json"); !strings.Contains(rec, `"status": "reviewed"`) || strings.Contains(rec, "approved") {
		t.Errorf("the review record must say reviewed and never approved: %s", rec)
	}
}

// Scenario: Per-bullet replacement. A bullet the gate rejects is swapped for the
// evidence line it cites; the grounded bullets of the model draft stay.
func TestModelFabricatedMetricReplacesOnlyThatBullet(t *testing.T) {
	m := echoModel(func(task string, v any) any {
		d, ok := v.(ResumeDraft)
		if !ok {
			return v
		}
		for _, e := range d.Experience {
			for i, b := range e.Bullets {
				switch {
				case strings.Contains(b.Text, "platform migration"):
					e.Bullets[i].Text = "Led the platform migration and cut incidents by 28% across 12 services"
				case strings.Contains(b.Text, "ingestion"):
					e.Bullets[i].Text = "Built the ingestion service used by twelve teams on Kubernetes"
				case strings.Contains(b.Text, "Mentored"):
					e.Bullets[i].Text = "Coached four engineers in code review and design"
				}
			}
		}
		return d
	})
	f := newFixture(t, fixtureOpts{model: m})
	res := f.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
	if !res.Published || res.Generation != GenModel {
		t.Fatalf("result = %+v", res)
	}
	warned := strings.Join(res.Warnings, "|")
	if n := strings.Count(warned, "was replaced by its source line"); n != 2 || !strings.Contains(warned, "unsupported-figure") || !strings.Contains(warned, "unsupported-name") {
		t.Errorf("warnings = %v; want one warning per replaced bullet, naming why", res.Warnings)
	}
	if strings.Contains(warned, "deterministic builder") {
		t.Errorf("warnings = %v; the draft was not discarded", res.Warnings)
	}
	docs, _ := f.svc.List(t.Context(), f.cand)
	stored := f.read(docs[0].Path + "/resume.json")
	if strings.Contains(stored, "28%") || strings.Contains(stored, "Kubernetes") {
		t.Error("a rejected claim reached the stored résumé")
	}
	for _, want := range []string{"Coached four engineers", "Led the platform migration and cut incidents by 40%", "Built the ingestion service used by twelve teams"} {
		if !strings.Contains(stored, want) {
			t.Errorf("the stored résumé lacks %q", want)
		}
	}
}

// A draft none of whose parts survive is the deterministic builder's work and
// is disclosed as such.
func TestModelDraftWithNothingGroundedFallsBackToDeterministic(t *testing.T) {
	m := echoModel(func(task string, v any) any {
		if d, ok := v.(ResumeDraft); ok {
			d.Profile = "Backend engineer who cut incidents by 28%."
			for _, e := range d.Experience {
				for i := range e.Bullets {
					e.Bullets[i].Text += " and saved 28% of costs"
				}
			}
			for _, e := range d.Projects {
				for i := range e.Bullets {
					e.Bullets[i].Text += " and saved 28% of costs"
				}
			}
			return d
		}
		return v
	})
	f := newFixture(t, fixtureOpts{model: m})
	res := f.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
	if !res.Published || res.Generation != GenDeterministic {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(strings.Join(res.Warnings, "|"), "unsupported-figure") {
		t.Errorf("warnings = %v; want the rejected figure named", res.Warnings)
	}
}

func TestModelCoverLetterRejectedTwiceUsesDeterministicLetter(t *testing.T) {
	calls := 0
	m := echoModel(func(task string, v any) any {
		if d, ok := v.(LetterDraft); ok {
			calls++
			d.Opening = "I have a strong interest in this opportunity and I am excited by your mission to change the world."
			d.Closing = "I would be glad to talk about it soon. Thank you for reading this letter today."
			return d
		}
		return v
	})
	f := newFixture(t, fixtureOpts{model: m})
	res := f.run(Request{Action: ActionCover, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
	if !res.Published || calls != 2 {
		t.Fatalf("result = %+v, model letter calls = %d (one regeneration, then deterministic)", res, calls)
	}
	docs, _ := f.svc.List(t.Context(), f.cand)
	if docs[0].Generation != string(GenDeterministic) {
		t.Errorf("generation = %s", docs[0].Generation)
	}
}

func TestModelUnavailableDisclosedAndDeterministicUsed(t *testing.T) {
	m := &scriptModel{fn: func(string, string) (any, error) {
		return nil, &llm.Error{Kind: llm.KindFakeFixture, Message: "no fixture"}
	}}
	f := newFixture(t, fixtureOpts{model: m})
	res := f.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
	if !res.Published || res.Generation != GenDeterministic {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(strings.Join(res.Warnings, "|"), "deterministic builder") {
		t.Errorf("warnings = %v", res.Warnings)
	}
}

type blockingModel struct{ started chan struct{} }

func (b *blockingModel) Structured(ctx context.Context, _ llm.Request, _ json.RawMessage, _ any) (*llm.Response, error) {
	close(b.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

// The letter call's deadline holds a primary attempt that ends at its output
// ceiling (DeepSeek at thinking low: up to 31 s) and a whole fallback attempt
// (up to 16 s), so a fallback is never started only to be cancelled. The
// letter follows the user's voice notes.
func TestModelLetterDeadlineHoldsACeilingBoundAttemptAndAFallback(t *testing.T) {
	m := echoModel(nil)
	f := newFixture(t, fixtureOpts{model: m})
	must(t, os.WriteFile(filepath.Join(f.ws.Root, "voice.md"), []byte("Never say synergy."), 0o600))
	start := time.Now()
	if res := f.run(Request{Action: ActionCover, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting}); !res.Published {
		t.Fatalf("result = %+v", res)
	}
	reqs := m.requestsTo("cover-letter")
	if len(reqs) == 0 || !strings.Contains(reqs[0].Prompt, "Never say synergy.") {
		t.Fatal("no letter call in the user's voice")
	}
	if d := reqs[0].Deadline.Sub(start); d < (31+16)*time.Second*3/2 {
		t.Fatalf("the letter deadline is %v after the start", d)
	}
}

// A letter the model wrote read voice.md, so an edit of it dates that letter
// and not its résumé; a letter written without a model never read it. An edit
// of cv.md dates both.
func TestSourcesChangedNamesVoiceOnlyForTheLetterThatReadIt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model *scriptModel
		voice []string
	}{{"model", echoModel(nil), []string{"voice.md"}}, {"no model", nil, nil}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, fixtureOpts{model: tc.model})
			write := func(name, text string) { must(t, os.WriteFile(filepath.Join(f.ws.Root, name), []byte(text), 0o600)) }
			changed := func() map[Kind][]string {
				docs, err := f.svc.List(context.Background(), f.cand)
				must(t, err)
				out := map[Kind][]string{}
				for _, d := range docs {
					out[d.Kind] = f.svc.SourcesChanged(context.Background(), d)
				}
				return out
			}
			write("voice.md", "Plain words.")
			if res := f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting}); !res.Published {
				t.Fatalf("result = %+v", res)
			}
			if got := changed(); len(got) != 2 || got[KindResume] != nil || got[KindCover] != nil {
				t.Fatalf("fresh versions = %v", got)
			}
			write("voice.md", "Short sentences.")
			if got := changed(); got[KindResume] != nil || !slices.Equal(got[KindCover], tc.voice) {
				t.Fatalf("after voice.md = %v", got)
			}
			raw, err := os.ReadFile(filepath.Join(f.ws.Root, "cv.md"))
			must(t, err)
			write("cv.md", string(raw)+"\n")
			if got := changed(); !slices.Equal(got[KindResume], []string{"cv.md"}) || !slices.Equal(got[KindCover], append([]string{"cv.md"}, tc.voice...)) {
				t.Fatalf("after cv.md = %v", got)
			}
		})
	}
}

// Concurrent submits reserve their queue place before the run is recorded, so
// QueueMax holds under a burst while one run builds.
func TestSubmit_ConcurrentBurstNeverPassesQueueMax(t *testing.T) {
	started := make(chan struct{})
	f := newFixture(t, fixtureOpts{})
	f.svc.d.Model = &blockingModel{started: started}
	_, err := f.svc.Submit(t.Context(), Claim{Principal: "test", Key: "burst-0"}, Request{Action: ActionResume, Mode: ModeBaseline})
	must(t, err)
	<-started
	const extra = 5
	var wg sync.WaitGroup
	var mu sync.Mutex
	full := 0
	for i := range QueueMax + extra {
		wg.Go(func() {
			_, err := f.svc.Submit(t.Context(), Claim{Principal: "test", Key: fmt.Sprintf("burst-%d", i+1)}, Request{Action: ActionResume, Mode: ModeBaseline})
			if errors.Is(err, ErrQueueFull) {
				mu.Lock()
				full++
				mu.Unlock()
			} else if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	f.svc.mu.Lock()
	queued := len(f.svc.queue)
	f.svc.mu.Unlock()
	if queued != QueueMax || full != extra {
		t.Errorf("queued = %d, refused = %d; want %d and %d", queued, full, QueueMax, extra)
	}
}

// A run recorded while Close runs is settled as interrupted, never left queued
// on a service that no longer drains its queue.
func TestSubmit_RunRecordedAfterCloseIsSettled(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	ref, err := f.st.CreateRun(t.Context(), store.RunRequest{Principal: "test", Operation: operationOf(ActionResume), Key: "late", Kind: runKind, Input: []byte(`{}`)})
	must(t, err)
	f.svc.mu.Lock()
	f.svc.reserved++
	f.svc.mu.Unlock()
	f.svc.Close()
	if err := f.svc.enqueue(ref.RunID, Request{Action: ActionResume, Mode: ModeBaseline}); err == nil {
		t.Error("a closed service accepted the run")
	}
	if len(f.svc.queue) != 0 || f.svc.reserved != 0 {
		t.Errorf("queue = %d, reserved = %d after close", len(f.svc.queue), f.svc.reserved)
	}
	st, err := f.st.GetRun(t.Context(), ref.RunID)
	must(t, err)
	if st.Status != "failed" || st.FailureReason != ReasonInterrupted {
		t.Errorf("run = %s/%s, want failed/interrupted", st.Status, st.FailureReason)
	}
}

// An unpublished run's partial output is kept for the newest keptRuns runs
// only: the oldest directory goes when a new one is written.
func TestUnpublishedRunsKeepOnlyTheNewestPartials(t *testing.T) {
	started := make(chan struct{})
	f := newFixture(t, fixtureOpts{})
	f.svc.d.Model = &blockingModel{started: started}
	runs := filepath.Join(f.ws.StateDir(), "documents-runs")
	old := time.Now().Add(-time.Hour)
	for i := range keptRuns {
		dir := filepath.Join(runs, fmt.Sprintf("run_old%02d", i))
		must(t, os.MkdirAll(dir, 0o700))
		must(t, os.Chtimes(dir, old.Add(time.Duration(i)*time.Second), old.Add(time.Duration(i)*time.Second)))
	}
	h, err := f.svc.Submit(t.Context(), Claim{Principal: "test", Key: "partial-1"}, Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
	must(t, err)
	<-started
	f.svc.Cancel(h.RunID)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	_, err = f.svc.Wait(ctx, h.RunID)
	must(t, err)
	entries, err := os.ReadDir(runs)
	must(t, err)
	if _, err := os.Stat(filepath.Join(runs, h.RunID)); err != nil || len(entries) != keptRuns {
		t.Fatalf("runs kept = %d, newest present: %v", len(entries), err)
	}
	if _, err := os.Stat(filepath.Join(runs, "run_old00")); !os.IsNotExist(err) {
		t.Error("the oldest partial output was not pruned")
	}
}
