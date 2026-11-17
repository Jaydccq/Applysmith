package documents

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"applysmith/internal/kb"
)

func TestMaterials_PackBoundsAndMarker(t *testing.T) {
	var facts []kb.Fact
	for i := 1; i <= 20; i++ {
		text := fmt.Sprintf("- Built ferry scheduler number %d in Go for coastal routes. ", i) + strings.Repeat(fmt.Sprintf("It plans crossing %d around tides and crew rest rules. ", i), 60)
		facts = append(facts, snippetFact(fmt.Sprintf("s%d", i), fmt.Sprintf("knowledge/projects/ferry-%d.md#Section %d", i, i), text))
	}
	r := &recordingRetriever{facts: facts}
	f := newFixture(t, fixtureOpts{retriever: r})
	res := f.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
	if !res.Published {
		t.Fatalf("result = %+v", res)
	}
	r.mu.Lock()
	k := r.queries[0].K
	r.mu.Unlock()
	if k != 8 {
		t.Errorf("the pack asks for %d sections, not 8", k)
	}
	d, err := f.svc.Get(t.Context(), res.Documents[0])
	must(t, err)
	materials := f.read(d.Path + "/resume-materials.md")
	for _, want := range []string{"Query:", "Retrieved snippet sections: 8", "knowledge/projects/ferry-1.md", "[trimmed]"} {
		if !strings.Contains(materials, want) {
			t.Errorf("resume-materials.md lacks %q:\n%.600s", want, materials)
		}
	}
	for _, line := range strings.Split(materials, "\n") {
		if strings.HasPrefix(line, "- E") && strings.Contains(line, "(knowledge/") && utf8.RuneCountInString(line) > 1800+200 {
			t.Errorf("a retrieved section is not trimmed: %d characters", utf8.RuneCountInString(line))
		}
	}
	// Only metadata reaches the result; the retrieved text stays in the file.
	for _, it := range res.Retrieval.Items {
		if it.Source == "" {
			t.Errorf("item without a source: %+v", it)
		}
	}
	if res.Retrieval.Retrieved != 8 || len(res.Retrieval.Items) != 8 {
		t.Errorf("the pack holds %d items (%d listed), want 8", res.Retrieval.Retrieved, len(res.Retrieval.Items))
	}
}

func TestMaterials_UnreadableOrEmptyKnowledgeBaseIsAWarningNotAFailure(t *testing.T) {
	for name, r := range map[string]*recordingRetriever{
		"unreadable": {err: errors.New("the knowledge index is unreadable")},
		"empty":      {},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, fixtureOpts{retriever: r})
			res := f.run(Request{Action: ActionResume, Mode: ModeTailored, CandidacyID: f.cand, Posting: testPosting})
			if !res.Published || res.Retrieval.Retrieved != 0 {
				t.Fatalf("result = %+v", res)
			}
			d, err := f.svc.Get(t.Context(), res.Documents[0])
			must(t, err)
			materials := f.read(d.Path + "/resume-materials.md")
			if !strings.Contains(materials, "Retrieved snippet sections: 0") || !strings.Contains(materials, "cv.md alone") {
				t.Errorf("the materials file records zero items and a warning:\n%s", materials)
			}
		})
	}
}
