package kb

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
)

// fakeEmbedder maps a text to a vector by the first keyword it contains. A
// keyword's value is its cosine similarity with the query direction (1, 0).
type fakeEmbedder struct {
	mu     sync.Mutex
	model  string
	cos    map[string]float64
	calls  int
	texts  []string
	err    error
	querys string
}

func (e *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	e.texts = append(e.texts, texts...)
	if e.err != nil {
		return nil, "", e.err
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = []float32{0, 1}
		if e.querys != "" && strings.HasPrefix(strings.ToLower(t), e.querys) {
			out[i] = []float32{1, 0}
			continue
		}
		for kw, c := range e.cos {
			if strings.Contains(strings.ToLower(t), kw) {
				out[i] = []float32{float32(c), float32(math.Sqrt(1 - c*c))}
				break
			}
		}
	}
	return out, e.model, nil
}

func newEmbedder(cos map[string]float64) *fakeEmbedder {
	return &fakeEmbedder{model: "embed-a", cos: cos, querys: "how"}
}

const semQuery = "How do you cope when the pager goes off at night?"

func TestSemantic_OffByDefault(t *testing.T) {
	f := newFixture(t)
	f.write("knowledge/experience/a.md", snippet("", "Handled on-call rotations and incident response."))
	if r := f.search(semQuery); len(r.Facts) != 0 {
		t.Fatalf("lexical-only retrieval returned %v", ids(r))
	}
}

func TestSemantic_NotWhenLexicalClears(t *testing.T) {
	e := newEmbedder(nil)
	f := newFixture(t, func(o *Options) { o.Embedder = e })
	f.write("knowledge/experience/a.md", snippet("", "Handled pager rotations at night."))
	f.search("How do you handle the pager at night?")
	if e.calls != 0 {
		t.Fatalf("embedder called %d times although the lexical stage cleared", e.calls)
	}
}

func TestSemantic_BareFormLabelSendsNothing(t *testing.T) {
	e := newEmbedder(nil)
	f := newFixture(t, func(o *Options) { o.Embedder = e })
	f.write("knowledge/experience/a.md", snippet("", "Handled pager rotations at night."))
	f.search("Employment History")
	if e.calls != 0 {
		t.Fatal("a bare form label reached the embedding provider")
	}
}

func TestSemantic_RanksBelowLexicalAndSkipsRepeats(t *testing.T) {
	e := newEmbedder(map[string]float64{"on-call": 0.6, "mentoring": 0.2})
	f := newFixture(t, func(o *Options) { o.Embedder = e })
	// Carried only by a trigger, so no section clears the lexical floor.
	f.write("knowledge/experience/triggered.md", snippet("triggers: [\"pager goes off\"]", "Talked about mentoring."))
	f.write("knowledge/experience/oncall.md", snippet("", "Covered the on-call rotation overnight."))
	r := f.search(semQuery)
	got := snippetIDs(r)
	if len(got) != 2 || got[0] != "experience/triggered" || got[1] != "experience/oncall" {
		t.Fatalf("order = %v", got)
	}
	if r.Facts[0].Stage != StageLexical || r.Facts[1].Stage != StageSemantic {
		t.Fatalf("stages = %s, %s", r.Facts[0].Stage, r.Facts[1].Stage)
	}
}

func TestSemantic_StandoutGate_ClusteredPackRefused(t *testing.T) {
	e := newEmbedder(map[string]float64{"alpha": 0.31, "beta": 0.29})
	f := newFixture(t, func(o *Options) { o.Embedder = e })
	f.write("knowledge/experience/a.md", snippet("", "Alpha item."))
	f.write("knowledge/experience/b.md", snippet("", "Beta item."))
	if r := f.search(semQuery); len(r.Facts) != 0 {
		t.Fatalf("a clustered pack returned %v", ids(r))
	}
	// A clear leader passes.
	e2 := newEmbedder(map[string]float64{"alpha": 0.40, "beta": 0.29})
	f2 := newFixture(t, func(o *Options) { o.Embedder = e2 })
	f2.write("knowledge/experience/a.md", snippet("", "Alpha item."))
	f2.write("knowledge/experience/b.md", snippet("", "Beta item."))
	if got := snippetIDs(f2.search(semQuery)); len(got) != 2 {
		t.Fatalf("a standout pack returned %v", got)
	}
}

func TestSemantic_CacheKeyedByModelAndHash(t *testing.T) {
	e := newEmbedder(map[string]float64{"alpha": 0.5})
	f := newFixture(t, func(o *Options) { o.Embedder = e })
	f.write("knowledge/experience/a.md", snippet("", "Alpha item."))
	f.write("knowledge/experience/b.md", snippet("", "Beta item."))
	f.search(semQuery)
	first := len(e.texts) // query + two sections
	if first != 3 {
		t.Fatalf("first call embedded %d texts", first)
	}
	f.search(semQuery)
	if added := len(e.texts) - first; added != 1 {
		t.Fatalf("a repeat embedded %d texts (want only the query)", added)
	}
	// Editing one snippet re-embeds exactly that section.
	f.write("knowledge/experience/a.md", snippet("", "Alpha item, now rewritten."))
	mark := len(e.texts)
	f.search(semQuery)
	if added := len(e.texts) - mark; added != 2 {
		t.Fatalf("an edit re-embedded %d texts (want query + one section)", added)
	}
	// A model change embeds everything again.
	e.model = "embed-b"
	mark = len(e.texts)
	f.search(semQuery)
	if added := len(e.texts) - mark; added != 3 {
		t.Fatalf("a model change embedded %d texts", added)
	}
}

func TestSemantic_ProviderOutageDegradesToLexical(t *testing.T) {
	e := newEmbedder(nil)
	e.err = errors.New("provider unavailable")
	f := newFixture(t, func(o *Options) { o.Embedder = e })
	f.write("knowledge/experience/a.md", snippet("", "Alpha item."))
	r := f.search(semQuery)
	if len(r.Facts) != 0 || r.Status != StatusNoMatch || len(r.Diagnostics) == 0 {
		t.Fatalf("result = %+v", r)
	}
}

func TestSemantic_OnlyPublishedSnippetsAreSent(t *testing.T) {
	e := newEmbedder(map[string]float64{"alpha": 0.5})
	f := newFixture(t, func(o *Options) { o.Embedder = e })
	f.write("knowledge/experience/a.md", snippet("", "Alpha item."))
	f.write("cv.md", "## Skills\nSecret cv content that must not leave.")
	f.write("profile.yml", profileDoc)
	if _, err := f.svc.CreateDraft(f.ctx, NewDraft{Markdown: snippet("id: pending", "Draft text that must not leave.")}); err != nil {
		t.Fatal(err)
	}
	f.search(semQuery)
	for _, txt := range e.texts {
		if strings.Contains(txt, "must not leave") || strings.Contains(txt, "Alex Example") {
			t.Fatalf("sent to the provider: %q", txt)
		}
	}
}
