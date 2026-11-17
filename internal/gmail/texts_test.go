package gmail

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"applysmith/internal/gmail/classify"
	"applysmith/internal/gmail/lifecycle"
)

func TestSummary_ValidityRule(t *testing.T) {
	for _, c := range []struct {
		text string
		ok   bool
	}{
		{"Example Corp invited you to an interview on Thursday.", true},
		{"Example Corp invited you to an interview on Thursday", false},
		{"Short one.", false},
		{"", false},
		{"Example Corp asked whether you are \"available on Thursday.\"", true},
		{"Example Corp invited you to an interview on Thursday。", true},
		{"Example Corp invited you to an interview on Thursday！", true},
		{"Example Corp wrote that the role is closed and then the budget", false},
	} {
		if got := validSentence(c.text); got != c.ok {
			t.Errorf("%q: %v, want %v", c.text, got, c.ok)
		}
	}
}

const goodSummary = "Example Corp asked you to complete an online assessment for the Backend Engineer role."

func modelReturning(texts ...Completion) func(task, system, prompt string, n int) (Completion, error) {
	return func(_, _, _ string, n int) (Completion, error) { return texts[min(n, len(texts))-1], nil }
}

func TestSummary_FingerprintCache(t *testing.T) {
	p := newPipeEnv(t)
	p.model.fn = modelReturning(Completion{Text: goodSummary, Finish: "stop"})
	p.seed(sig{id: "a", thread: "t1", typ: classify.TypeAssessment, role: "Backend Engineer", at: p.ago(hour)})
	first, err := p.stack.SummaryFor(p.ctx, "t1")
	if err != nil || first.Method != MethodModel || first.Cached || first.Text != goodSummary {
		t.Fatalf("first = %+v %v", first, err)
	}
	p.clk.advance(time.Minute)
	second, err := p.stack.SummaryFor(p.ctx, "t1")
	if err != nil || !second.Cached || !second.GeneratedAt.Equal(first.GeneratedAt) || p.model.count(TaskSummary) != 1 {
		t.Fatalf("second = %+v %v, calls %d", second, err, p.model.count(TaskSummary))
	}
	// A new message changes the fingerprint.
	p.seed(sig{id: "b", thread: "t1", typ: classify.TypeInterview, at: p.clk.now()})
	third, _ := p.stack.SummaryFor(p.ctx, "t1")
	if third.Cached || p.model.count(TaskSummary) != 2 {
		t.Fatalf("third = %+v, calls %d", third, p.model.count(TaskSummary))
	}
}

func TestSummary_RetryOnceThenDeterministicAndNotCached(t *testing.T) {
	p := newPipeEnv(t)
	p.model.fn = modelReturning(Completion{Text: "Example Corp asked you to complete an on", Finish: "stop"})
	p.seed(sig{id: "a", thread: "t1", typ: classify.TypeAssessment, role: "Backend Engineer", at: p.ago(hour)})
	s, err := p.stack.SummaryFor(p.ctx, "t1")
	if err != nil || s.Method != MethodDeterministic || s.Cached || p.model.count(TaskSummary) != 2 {
		t.Fatalf("summary = %+v %v, calls %d", s, err, p.model.count(TaskSummary))
	}
	if !strings.Contains(s.Text, "Example Corp") || !strings.Contains(s.Text, "assessment") {
		t.Errorf("deterministic text = %q", s.Text)
	}
	// Nothing was cached: the next request tries the model again.
	p.model.fn = modelReturning(Completion{Text: goodSummary, Finish: "stop"})
	again, _ := p.stack.SummaryFor(p.ctx, "t1")
	if again.Method != MethodModel {
		t.Fatalf("after recovery = %+v", again)
	}
}

func TestSummary_ModelUnavailableGivesTheDeterministicText(t *testing.T) {
	p := newPipeEnv(t)
	p.seed(sig{id: "a", thread: "t1", typ: classify.TypeInterview, role: "Backend Engineer", at: p.ago(hour), due: p.clk.now().Add(48 * hour)})
	s, err := p.stack.SummaryFor(p.ctx, "t1")
	if err != nil || s.Method != MethodDeterministic || !strings.Contains(s.Text, "interview scheduled") {
		t.Fatalf("%+v %v", s, err)
	}
	p.stack.Model = nil
	if s, _ = p.stack.SummaryFor(p.ctx, "t1"); s.Method != MethodDeterministic {
		t.Fatalf("without a model: %+v", s)
	}
	if _, err := p.stack.SummaryFor(p.ctx, "missing"); !errors.Is(err, ErrNoThread) {
		t.Fatalf("unknown thread = %v", err)
	}
}

func TestSummary_NoCodeLinkOrInventedNumberInASummary(t *testing.T) {
	p := newPipeEnv(t)
	var prompts []string
	var mu sync.Mutex
	p.model.fn = func(_, _, prompt string, n int) (Completion, error) {
		mu.Lock()
		prompts = append(prompts, prompt)
		mu.Unlock()
		return Completion{Text: "Example Corp sent the verification code 482913 for your account, so enter it today.", Finish: "stop"}, nil
	}
	p.seed(sig{id: "a", thread: "t1", typ: classify.TypeActionNeeded, at: p.ago(hour),
		body: "Your verification code is 482913. Open https://example.org/verify?token=FAKE_abcdef0123456789abcdef0123 to continue."})
	s, _ := p.stack.SummaryFor(p.ctx, "t1")
	if s.Method != MethodDeterministic || strings.Contains(s.Text, "482913") || strings.Contains(s.Text, "http") {
		t.Fatalf("summary = %+v", s)
	}
	for _, pr := range prompts {
		if strings.Contains(pr, "482913") || strings.Contains(pr, "token=") {
			t.Fatalf("a code or link reached the model: %s", pr)
		}
		if !strings.Contains(pr, "<<<MAIL") {
			t.Fatal("the mail text is not delimited as untrusted data")
		}
	}
	// A number the thread never mentioned forces the deterministic text too.
	p.model.fn = modelReturning(Completion{Text: "Example Corp wants your reply within 9 days to move forward.", Finish: "stop"})
	if s, _ = p.stack.SummaryFor(p.ctx, "t1"); s.Method != MethodDeterministic {
		t.Fatalf("an invented number was accepted: %+v", s)
	}
}

func TestSummary_BackfillBoundedAndNonFatal(t *testing.T) {
	p := newPipeEnv(t)
	n := 0
	p.model.fn = func(_, _, _ string, _ int) (Completion, error) {
		n++
		if n%2 == 0 {
			return Completion{}, fmt.Errorf("boom")
		}
		return Completion{Text: goodSummary, Finish: "stop"}, nil
	}
	var batch []sig
	for i := range 20 {
		batch = append(batch, sig{id: fmt.Sprintf("m%02d", i), typ: classify.TypeAssessment, role: "Backend Engineer", at: p.ago(hour)})
	}
	p.seed(batch...)
	p.stack.BackfillSummaries(p.ctx)
	if got := p.model.count(TaskSummary); got > 30 || got < 15 {
		t.Errorf("model calls = %d for at most 15 threads (two attempts each)", got)
	}
	var cached int
	_ = p.st.Read().QueryRow(`SELECT count(*) FROM gmail_thread_texts WHERE kind = 'summary'`).Scan(&cached)
	if cached > 15 {
		t.Errorf("%d summaries cached, the bound is 15 threads a run", cached)
	}
	// Old threads are left alone.
	q := newPipeEnv(t)
	q.model.fn = modelReturning(Completion{Text: goodSummary, Finish: "stop"})
	q.seed(sig{id: "old", typ: classify.TypeAssessment, at: q.ago(3 * day)})
	q.stack.BackfillSummaries(q.ctx)
	if q.model.count(TaskSummary) != 0 {
		t.Error("a thread not touched in 24 hours was summarized")
	}
}

// A draft on request is written from the facts of its own thread only, under
// the no-invention rule, and fails with ErrNoModel when there is no model.
func TestDraftReply_UsesOnlyTheThreadsMessages(t *testing.T) {
	p := newPipeEnv(t)
	p.seed(sig{id: "a", thread: "t-a", typ: classify.TypeReplyNeeded, employer: "Alpha Example", body: "alpha-body please send your availability", at: p.ago(day)},
		sig{id: "b", thread: "t-b", typ: classify.TypeReplyNeeded, employer: "Beta Example", body: "beta-body unrelated request", at: p.ago(day)})
	var system, prompt string
	p.model.fn = func(_, sys, pr string, _ int) (Completion, error) {
		system, prompt = sys, pr
		return Completion{Text: "Thank you for the message; I will send my availability today.", Finish: "stop"}, nil
	}
	d, err := p.stack.DraftReply(p.ctx, "t-a")
	if err != nil || d.Kind != lifecycle.DraftReply || !strings.Contains(d.Text, "availability") {
		t.Fatalf("draft = %+v, %v", d, err)
	}
	if !strings.Contains(prompt, "alpha-body") || strings.Contains(prompt, "beta-body") || strings.Contains(prompt, "Beta Example") {
		t.Errorf("the prompt must hold this thread's facts only: %q", prompt)
	}
	if !strings.Contains(system, "Do not invent facts") || !strings.Contains(system, "untrusted") {
		t.Errorf("the grounding rules are missing from the system prompt")
	}
	if got, err := p.stack.StoredDraftOf(p.ctx, "t-a"); err != nil || got.Text != d.Text {
		t.Errorf("the draft was not kept: %+v %v", got, err)
	}
	// A draft that states a number the thread never gave is refused.
	p.model.fn = func(string, string, string, int) (Completion, error) {
		return Completion{Text: "Thank you for the message; I can start on March 1993 as agreed.", Finish: "stop"}, nil
	}
	if _, err := p.stack.DraftReply(p.ctx, "t-b"); !errors.Is(err, ErrUnusable) {
		t.Errorf("an invented number was accepted: %v", err)
	}
	if _, err := p.stack.DraftReply(p.ctx, "t-none"); !errors.Is(err, ErrNoThread) {
		t.Errorf("an unknown thread: %v", err)
	}
	p.stack.Model = nil
	if _, err := p.stack.DraftReply(p.ctx, "t-a"); !errors.Is(err, ErrNoModel) {
		t.Errorf("no model: %v", err)
	}
}
