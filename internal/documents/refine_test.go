package documents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"applysmith/internal/documents/toolchain"
)

const revisedProfile = "Backend engineer focused on dependable data platforms."

// refineFixture publishes a pair with ready PDFs and returns a model whose
// refine calls are answered by reply; every other call is the grounded echo.
type refineFix struct {
	*fixture
	model          *scriptModel
	resume, letter Document
}

func newRefineFixture(t *testing.T, reply func(task, prompt string) (any, error)) *refineFix {
	t.Helper()
	base := echoModel(nil)
	m := &scriptModel{fn: func(task, prompt string) (any, error) {
		if strings.HasPrefix(prompt, "Task: resume-refine") || strings.HasPrefix(prompt, "Task: cover-letter-refine") {
			return reply(task, prompt)
		}
		return base.fn(task, prompt)
	}}
	fb := &fakeBuilder{}
	f := newFixture(t, fixtureOpts{model: m, builder: func(*toolchain.Toolchain) pdfBuilder { return fb }})
	res := f.run(Request{Action: ActionPair, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting, PDF: true})
	if !res.Published {
		t.Fatalf("pair = %+v", res)
	}
	r := &refineFix{fixture: f, model: m}
	docs, _ := f.svc.List(t.Context(), f.cand)
	for _, d := range docs {
		if d.Kind == KindResume {
			r.resume = d
		} else {
			r.letter = d
		}
	}
	if r.resume.PDFState != PDFReady {
		t.Fatalf("the résumé PDF is %s; the fixture needs a ready one", r.resume.PDFState)
	}
	return r
}

// revisedResume answers a résumé refine with the stored draft and a new profile line.
func (r *refineFix) revisedResume() (any, error) {
	src, err := r.svc.Source(context.Background(), r.resume.ID)
	if err != nil {
		return nil, err
	}
	d := *src.Resume
	d.Profile = revisedProfile
	return d, nil
}

func (r *refineFix) refine(doc Document, instruction string, turns ...Turn) Result {
	r.t.Helper()
	return r.run(Request{Action: ActionRefine, DocumentID: doc.ID, Instruction: instruction, Turns: turns})
}

func (r *refineFix) tree(d Document) map[string]string {
	return treeHash(r.t, filepath.Join(r.ws.Root, filepath.FromSlash(d.Path)))
}

func newResumeRefine(t *testing.T) *refineFix {
	var r *refineFix
	r = newRefineFixture(t, func(string, string) (any, error) { return r.revisedResume() })
	return r
}

// Scenario: refine keeps the revision as a pending proposal with a line diff
// and does not replace the draft until the user accepts it.
func TestRefine_PendingUntilAccepted(t *testing.T) {
	r := newResumeRefine(t)
	before := r.tree(r.resume)
	docsBefore, _ := r.svc.List(t.Context(), r.cand)
	res := r.refine(r.resume, "Make the profile line say dependable instead of reliable")
	if !res.Published || res.Proposal == 0 || len(res.Documents) != 0 {
		t.Fatalf("result = %+v", res)
	}
	rf, err := r.svc.Refinement(t.Context(), r.resume.ID)
	must(t, err)
	p := rf.Pending
	if p == nil || p.Status != ProposalPending || p.ID != res.Proposal || p.Resume == nil || p.Resume.Profile != revisedProfile {
		t.Fatalf("pending = %+v", p)
	}
	var add, del int
	for _, l := range p.Diff {
		switch l.Op {
		case DiffAdd:
			add++
			if !strings.Contains(l.Text, "dependable") {
				t.Errorf("added line %q", l.Text)
			}
		case DiffDel:
			del++
			if !strings.Contains(l.Text, "reliable") {
				t.Errorf("deleted line %q", l.Text)
			}
		}
	}
	if add != 1 || del != 1 {
		t.Errorf("diff = %+v; want one line replaced", p.Diff)
	}
	if !reflect.DeepEqual(before, r.tree(r.resume)) {
		t.Error("the stored version changed while the proposal is pending")
	}
	if after, _ := r.svc.List(t.Context(), r.cand); len(after) != len(docsBefore) {
		t.Errorf("documents = %d, want %d: a proposal is not a version", len(after), len(docsBefore))
	}
	if d := r.svc.mustGet(t, r.resume.ID); !d.RefinePending || d.PDFState != PDFReady || !d.Current {
		t.Errorf("document = %+v", d)
	}
}

// A letter revision reads voice.md, so the version it makes is written in the
// voice notes the revision read: an edit of them before the revision no longer
// dates the letter, an edit after it does.
func TestRefine_LetterRecordsTheVoiceItRead(t *testing.T) {
	var r *refineFix
	closings := []string{"Thank you for your time.", "Thank you for reading."}
	r = newRefineFixture(t, func(task, _ string) (any, error) {
		src, err := r.svc.Source(context.Background(), r.letter.ID)
		if err != nil {
			return nil, err
		}
		d := *src.Letter
		d.Closing, closings = closings[0], closings[1:]
		return d, nil
	})
	voice := func(text string) { must(t, os.WriteFile(filepath.Join(r.ws.Root, "voice.md"), []byte(text), 0o600)) }
	// revise revises the current letter, runs between the revision and its
	// acceptance, and returns what the accepted version names as changed.
	revise := func(between func()) []string {
		res := r.refine(r.letter, "Close with thanks")
		rf, _ := r.svc.Refinement(t.Context(), r.letter.ID)
		if !res.Published || rf.Pending == nil {
			t.Fatalf("refine = %+v", res)
		}
		between()
		if acc := r.run(Request{Action: ActionEdit, DocumentID: r.letter.ID, ProposalID: rf.Pending.ID}); !acc.Published {
			t.Fatalf("accept = %+v", acc)
		}
		docs, err := r.svc.List(t.Context(), r.cand)
		must(t, err)
		r.letter = byKind(docs, KindCover)
		return r.svc.SourcesChanged(t.Context(), r.letter)
	}
	voice("Short sentences.")
	if got := r.svc.SourcesChanged(t.Context(), r.letter); !slices.Equal(got, []string{"voice.md"}) {
		t.Fatalf("before the revision = %v", got)
	}
	if got := revise(func() {}); got != nil {
		t.Fatalf("after a revision in the new voice = %v", got)
	}
	if got := revise(func() { voice("Plain words.") }); !slices.Equal(got, []string{"voice.md"}) {
		t.Fatalf("voice.md edited between the revision and its acceptance = %v", got)
	}
}

// A revision that states a figure no evidence line holds is refused by the same
// gate that checks a draft, and the draft stays as it was.
func TestRefine_UngroundedRevisionIsRefused(t *testing.T) {
	var r *refineFix
	r = newRefineFixture(t, func(string, string) (any, error) {
		src, _ := r.svc.Source(context.Background(), r.resume.ID)
		d := *src.Resume
		d.Experience = append([]EntryDraft(nil), d.Experience...)
		d.Experience[0].Bullets = append([]Bullet{{Text: "Cut incidents by 95% across 40 services", Evidence: d.Experience[0].Bullets[0].Evidence}}, d.Experience[0].Bullets...)
		return d, nil
	})
	before := r.tree(r.resume)
	res := r.refine(r.resume, "Add a bullet about incident reduction")
	if res.Published || res.Reason != ReasonNoDraft {
		t.Fatalf("result = %+v", res)
	}
	rf, _ := r.svc.Refinement(t.Context(), r.resume.ID)
	if rf.Pending != nil {
		t.Error("an ungrounded revision became a proposal")
	}
	if !reflect.DeepEqual(before, r.tree(r.resume)) {
		t.Error("the version changed")
	}
}

// Scenario: Rejected proposal.
func TestRefine_RejectedProposal(t *testing.T) {
	r := newResumeRefine(t)
	before := r.tree(r.resume)
	first := r.refine(r.resume, "Say dependable")
	pdfBefore := r.svc.mustGet(t, r.resume.ID).PDFSHA256
	must(t, r.svc.Discard(t.Context(), r.resume.ID, first.Proposal))

	if !reflect.DeepEqual(before, r.tree(r.resume)) {
		t.Error("rejecting changed the stored version")
	}
	if d := r.svc.mustGet(t, r.resume.ID); d.RefinePending || d.PDFSHA256 != pdfBefore || d.PDFState != PDFReady || !d.Ready {
		t.Errorf("document after reject = %+v", d)
	}
	if v, err := r.svc.Verification(t.Context(), r.svc.mustGet(t, r.resume.ID)); err != nil || v == nil || v.Stale != "" {
		t.Errorf("verification = %+v, %v; want untouched", v, err)
	}
	rf, _ := r.svc.Refinement(t.Context(), r.resume.ID)
	if rf.Pending != nil || len(rf.Turns) != 2 || rf.Turns[1].Decision != DecisionRejected {
		t.Fatalf("refinement = %+v", rf)
	}

	// The next request carries the rejected decision; the rejected text reaches
	// the model neither as evidence nor in any other form.
	r.refine(r.resume, "Try a shorter profile", rf.Turns...)
	reqs := r.model.requestsTo("resume-write")
	last := reqs[len(reqs)-1].Prompt
	if !strings.Contains(last, "The user rejected the result of the previous instruction") || !strings.Contains(last, "Earlier instruction: Say dependable") {
		t.Errorf("prompt lacks the turns: %s", last)
	}
	if strings.Contains(last, revisedProfile) {
		t.Error("the rejected proposal's text was sent to the model")
	}
	if d := r.svc.mustGet(t, r.resume.ID); !d.RefinePending {
		t.Error("the second instruction should be pending")
	}
}

// Scenario: Pending blocks the PDF.
func TestRefine_PendingBlocksThePDF(t *testing.T) {
	r := newResumeRefine(t)
	res := r.refine(r.resume, "Say dependable")
	if err := r.svc.admit(t.Context(), &Request{Action: ActionPDF, DocumentID: r.resume.ID}); !errors.Is(err, ErrProposalPending) {
		t.Fatalf("pdf while pending = %v, want ErrProposalPending", err)
	}
	if _, err := r.svc.Submit(t.Context(), Claim{Principal: "test", Key: "pdf-1"}, Request{Action: ActionPDF, DocumentID: r.resume.ID}); !errors.Is(err, ErrConflict) {
		t.Errorf("submit while pending = %v", err)
	}
	must(t, r.svc.Discard(t.Context(), r.resume.ID, res.Proposal))
	if err := r.svc.admit(t.Context(), &Request{Action: ActionPDF, DocumentID: r.resume.ID}); err != nil {
		t.Errorf("pdf after the decision = %v", err)
	}
}

// Accepting creates a new version; the earlier PDF is no longer the current one.
func TestRefine_AcceptCreatesANewVersion(t *testing.T) {
	r := newResumeRefine(t)
	before := r.tree(r.resume)
	res := r.refine(r.resume, "Say dependable")
	acc := r.run(Request{Action: ActionEdit, DocumentID: r.resume.ID, ProposalID: res.Proposal})
	if !acc.Published || len(acc.Documents) != 2 {
		t.Fatalf("accept = %+v", acc)
	}
	if !reflect.DeepEqual(before, r.tree(r.resume)) {
		t.Error("the earlier version changed")
	}
	var fresh Document
	for _, id := range acc.Documents {
		if d := r.svc.mustGet(t, id); d.Kind == KindResume {
			fresh = d
		}
	}
	if fresh.Version != r.resume.Version+1 || fresh.PDFState != PDFNone || !fresh.Current || fresh.Generation != string(GenModel) {
		t.Errorf("new version = %+v", fresh)
	}
	if old := r.svc.mustGet(t, r.resume.ID); old.Current || old.RefinePending {
		t.Errorf("earlier version = %+v", old)
	}
	src, _ := r.svc.Source(t.Context(), fresh.ID)
	if src.Resume.Profile != revisedProfile {
		t.Errorf("profile = %q", src.Resume.Profile)
	}
	rf, _ := r.svc.Refinement(t.Context(), r.resume.ID)
	if rf.Pending != nil || rf.Turns[len(rf.Turns)-1].Decision != DecisionAccepted {
		t.Errorf("refinement = %+v", rf)
	}
	// An accepted proposal cannot be accepted again.
	if _, err := r.svc.Submit(t.Context(), Claim{Principal: "test", Key: "again"}, Request{Action: ActionEdit, DocumentID: r.resume.ID, ProposalID: res.Proposal}); err == nil {
		t.Error("an accepted proposal was accepted twice")
	}
}

// Scenario: a failed revision leaves the draft editable and records an error turn.
func TestRefine_FailedRevisionRecordsAnErrorTurn(t *testing.T) {
	r := newRefineFixture(t, func(string, string) (any, error) { return nil, errors.New("provider unreachable at /secret/path") })
	before := r.tree(r.resume)
	res := r.refine(r.resume, "Say dependable")
	if res.Published || res.Proposal != 0 {
		t.Fatalf("result = %+v", res)
	}
	rf, _ := r.svc.Refinement(t.Context(), r.resume.ID)
	if rf.Pending != nil || len(rf.Turns) != 2 || rf.Turns[1].Kind != TurnError || rf.Turns[1].Text == "" {
		t.Fatalf("refinement = %+v", rf)
	}
	if strings.Contains(rf.Turns[1].Text, "/secret") {
		t.Errorf("the error turn leaks provider text: %q", rf.Turns[1].Text)
	}
	if !reflect.DeepEqual(before, r.tree(r.resume)) {
		t.Error("the version changed")
	}
	src, _ := r.svc.Source(t.Context(), r.resume.ID)
	edit := *src.Resume
	edit.Profile = "Backend engineer who ships dependable data platforms."
	if err := r.svc.admit(t.Context(), &Request{Action: ActionEdit, DocumentID: r.resume.ID, Resume: &edit}); err != nil {
		t.Errorf("the draft is not editable after a failed revision: %v", err)
	}
	if err := r.svc.admit(t.Context(), &Request{Action: ActionPDF, DocumentID: r.resume.ID}); err != nil {
		t.Errorf("a failed revision blocked the PDF: %v", err)
	}
}

// Request limits: an instruction of at most 2,000 characters and up to 8
// earlier turns, each an instruction or an accept or reject decision.
func TestRefine_RequestLimits(t *testing.T) {
	r := newResumeRefine(t)
	ok := func(n int) []Turn {
		out := make([]Turn, n)
		for i := range out {
			out[i] = Turn{Kind: TurnInstruction, Text: "x"}
		}
		return out
	}
	cases := []struct {
		name  string
		instr string
		turns []Turn
		want  bool
	}{
		{"2000 characters", strings.Repeat("é", 2000), nil, true},
		{"2001 characters", strings.Repeat("é", 2001), nil, false},
		{"eight turns", "go", ok(8), true},
		{"nine turns", "go", ok(9), false},
		{"unknown decision", "go", []Turn{{Kind: TurnDecision, Decision: "maybe"}}, false},
		{"error turn is not sent", "go", []Turn{{Kind: TurnError, Text: "x"}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := r.svc.normalize(t.Context(), Request{Action: ActionRefine, DocumentID: r.resume.ID, Instruction: c.instr, Turns: c.turns})
			if (err == nil) != c.want || (err != nil && !errors.Is(err, ErrInvalid)) {
				t.Errorf("err = %v, want ok=%v", err, c.want)
			}
		})
	}
}

func TestRefine_RefusalsBeforeAnyRun(t *testing.T) {
	r := newResumeRefine(t)
	req := Request{Action: ActionRefine, DocumentID: r.resume.ID, Instruction: "Say dependable"}
	pending := r.refine(r.resume, "Say dependable")
	if err := r.svc.admit(t.Context(), &req); !errors.Is(err, ErrProposalPending) {
		t.Errorf("second proposal while pending = %v", err)
	}
	must(t, r.svc.Discard(t.Context(), r.resume.ID, pending.Proposal))
	if err := r.svc.Discard(t.Context(), r.resume.ID, pending.Proposal); !errors.Is(err, ErrConflict) {
		t.Errorf("discarding twice = %v", err)
	}
	if err := r.svc.Discard(t.Context(), r.letter.ID, pending.Proposal); !errors.Is(err, ErrNotFound) {
		t.Errorf("discarding through another document = %v", err)
	}
	must(t, r.svc.Freeze(t.Context(), r.cand, r.resume.ID))
	if err := r.svc.admit(t.Context(), &req); !errors.Is(err, ErrFrozen) {
		t.Errorf("submitted version = %v", err)
	}
	if err := r.svc.admit(t.Context(), &Request{Action: ActionRefine, DocumentID: 9999, Instruction: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown document = %v", err)
	}
}

func TestRefine_OlderVersionIsNotRevised(t *testing.T) {
	r := newResumeRefine(t)
	src, _ := r.svc.Source(t.Context(), r.resume.ID)
	edit := *src.Resume
	edit.Profile = "Backend engineer who ships dependable data platforms."
	r.run(Request{Action: ActionEdit, DocumentID: r.resume.ID, Resume: &edit})
	if err := r.svc.admit(t.Context(), &Request{Action: ActionRefine, DocumentID: r.resume.ID, Instruction: "x"}); !errors.Is(err, ErrConflict) {
		t.Errorf("older version = %v", err)
	}
}
