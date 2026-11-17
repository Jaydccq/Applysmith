package kb

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden files")

// syntheticBank is a small story bank that exercises every grade: a green
// ownership cell, a yellow technical-depth cell, a project story that may not
// evidence disagreement, and red cells for the rest.
func syntheticBank(f *fixture) {
	publishStoryFile(f, "alerts-ownership", map[string]string{"principles": "[ownership, delivery]", "source": "work", "dims": "[metric, tradeoff]", "facts": "[\"Cut 200 alerts to 15 alerts\"]"})
	publishStoryFile(f, "parser-rewrite", map[string]string{"principles": "[technical-depth, disagreement]", "source": "project", "dims": "[risk]", "facts": "[\"Replaced the importer\"]"})
	publishStoryFile(f, "review-mentoring", map[string]string{"principles": "[mentoring, collaboration]", "source": "leadership", "dims": "[learning, conflict]", "facts": "[\"Mentored four engineers\"]",
		"situation": "Four newer engineers struggled to get changes through code review.",
		"task":      "I was asked to mentor the engineers and lead code review for the platform team.",
		"action":    "I paired with each engineer weekly and wrote review guidelines because inconsistent feedback caused most of the delay.",
		"result":    "Review time dropped from 5 days to 2 days for the four engineers."})
}

func TestCoverage_GoldenGrid(t *testing.T) {
	f := newFixture(t)
	syntheticBank(f)
	cov, err := f.coverage()
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(cov, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "coverage.golden.json")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if string(want) != string(got) {
		t.Errorf("the coverage grid differs from %s; check the change and run with -update.\ngot:\n%s", path, got)
	}
}

func TestMatchStories_RanksBestFirstAndRecordsNoGap(t *testing.T) {
	f := newFixture(t)
	syntheticBank(f)
	got, err := f.svc.MatchStories(f.ctx, "mentor engineers and lead code review", 3)
	if err != nil || len(got) == 0 || got[0].ID != "review-mentoring" {
		t.Fatalf("matches = %+v, %v", got, err)
	}
	none, err := f.svc.MatchStories(f.ctx, "kubernetes operator reconciliation", 3)
	if err != nil || len(none) != 0 {
		t.Fatalf("an unrelated requirement matched: %+v, %v", none, err)
	}
	gaps, err := f.svc.Gaps(f.ctx)
	if err != nil || len(gaps) != 0 {
		t.Fatalf("matching recorded knowledge gaps: %+v, %v", gaps, err)
	}
	if limited, _ := f.svc.MatchStories(f.ctx, "mentor engineers code review", 0); len(limited) != 0 {
		t.Errorf("k=0 returned %+v", limited)
	}
}
