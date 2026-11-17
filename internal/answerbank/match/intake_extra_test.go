package match

import (
	"reflect"
	"strings"
	"testing"
)

func TestGuards_LocatorQuestion(t *testing.T) {
	for _, q := range []string{"Share the best evidence: GitHub repo, demo video", "Link to your portfolio", "Open source work", "technical writeup"} {
		if !questionAsksForLocator(q) {
			t.Errorf("%q asks for a locator", q)
		}
	}
	for _, q := range []string{"Tell us about your submitted project", "Describe your role"} {
		if questionAsksForLocator(q) {
			t.Errorf("%q does not", q)
		}
	}
}

// A post-pass re-runs the per-question matcher on a rewritten question through
// Request.Rematch, with the request's entries and stages.
func TestPipeline_ReentrantMatchOne(t *testing.T) {
	st := echoStages()
	var r Result
	var err, bad error
	st.PostPasses = []PostPass{func(req *Request, _ []Question, _ []Result) {
		r, err = req.Rematch(textQ("s", "State or Region"))
		_, bad = req.Rematch(Question{})
	}}
	m := New(bankOf(ent("address-state", "fixed", withAnswer("SC"), withExamples("State or Region"))), st)
	if _, e := m.Match(Request{Host: "h", Questions: []Question{textQ("q", "Anything")}}); e != nil {
		t.Fatal(e)
	}
	if err != nil || r.Answer != "SC" {
		t.Fatalf("result %+v err %v", r, err)
	}
	if bad == nil {
		t.Fatal("validation applies")
	}
}

func TestClassify_FuzzyTraceCarriesQuestionCoverage(t *testing.T) {
	es := []Entry{ent("kube-ops", "fixed", withAnswer("Yes"), notCurated(), withDescription("kubernetes cluster operations"))}
	cl, _ := classifyWith(es, textQ("s", "kubernetes cluster upgrades planning"), nil)
	if !strings.Contains(cl.via, "qcov=0.50") {
		t.Fatalf("via = %q", cl.via)
	}
}

func TestIntake_ContractionLayoutKeepsOrdinaryWordBoundaries(t *testing.T) {
	if got := negationTokenList("education\ufeffhistory"); !reflect.DeepEqual(got, []string{"education", "history"}) {
		t.Fatalf("BOM ceased to be a word separator: %q", got)
	}
}
