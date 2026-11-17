package documents

import (
	"reflect"
	"strings"
	"testing"

	"applysmith/internal/workspace"
)

func roster() []workspace.Resume {
	return []workspace.Resume{
		{ID: "general", Label: "General", Tags: []string{"software"}, Default: true},
		{ID: "systems", Label: "Systems", Tags: []string{"c++", "linux"}, Description: "Low latency services"},
		{ID: "dotnet", Label: "Dotnet", Tags: []string{"c#", "node.js"}},
	}
}

func TestRankKeepsLanguageNamesWhole(t *testing.T) {
	got := RankResumes(roster(), RankPosting{Title: "C++ Engineer", Description: "You know C++ and Linux, not C or C#."})
	if got.Chosen != "systems" || got.Ranked[0].Reason != "c++, linux" || got.Ranked[0].Score != 2 {
		t.Fatalf("%+v", got)
	}
	// "c#" is one word, so the dotnet entry matches it once.
	if got.Ranked[1].ID != "dotnet" || got.Ranked[1].Score != 1 || got.Ranked[1].Reason != "c#" {
		t.Fatalf("%+v", got.Ranked)
	}
}

func TestRankTiesGoToTheDefaultThenRosterOrder(t *testing.T) {
	r := []workspace.Resume{
		{ID: "one", Tags: []string{"go"}},
		{ID: "two", Tags: []string{"go"}},
		{ID: "three", Tags: []string{"go"}, Default: true},
	}
	got := RankResumes(r, RankPosting{Title: "Go developer"})
	ids := []string{got.Ranked[0].ID, got.Ranked[1].ID, got.Ranked[2].ID}
	if !reflect.DeepEqual(ids, []string{"three", "one", "two"}) || got.Chosen != "three" {
		t.Fatalf("%v", ids)
	}
	none := RankResumes(r, RankPosting{Title: "Pastry"})
	ids = []string{none.Ranked[0].ID, none.Ranked[1].ID, none.Ranked[2].ID}
	if !reflect.DeepEqual(ids, []string{"three", "one", "two"}) || none.Ranked[0].Reason != ReasonDefault {
		t.Fatalf("%v %+v", ids, none)
	}
}

// A final report is ranked through its posting view: the evidence column and
// the scorecard's evidence describe the seeker, so the C# and Node.js named
// there score nothing, while the keywords and the quoted requirement do.
func TestRankReportReadsThePostingNotTheEvidence(t *testing.T) {
	md := "report: 0012\nscore: 4.1\nscore_kind: final\n\n# Example Corp - Systems Engineer\n\n## Fit\n\n| Group | Requirement | Hit | Evidence |\n|---|---|---|---|\n" +
		"| must-have | Modern C++ | 1.0 | CV: node.js services and C# tools |\n\n```scorecard\n" +
		`{"items":{"must_have":[{"id":"m1","label":"Modern C++","source":"“Modern C++ on Linux”","hit_level":1,"evidence":"CV: node.js services and C# tools"}]}}` +
		"\n```\n\n## Keywords\n\nC++, Linux\n"
	got := RankReport(roster(), "Systems Engineer", "Example Corp", md)
	if got.Chosen != "systems" || !strings.Contains(got.Ranked[0].Reason, "c++") || !strings.Contains(got.Ranked[0].Reason, "linux") {
		t.Fatalf("%+v", got)
	}
	for _, r := range got.Ranked {
		if r.ID == "dotnet" && r.Score != 0 {
			t.Fatalf("the seeker's evidence scored: %+v", r)
		}
	}
	if raw := RankResumes(roster(), RankPosting{Title: "Systems Engineer", Description: md}); raw.Ranked[1].ID != "dotnet" || raw.Ranked[1].Score == 0 {
		t.Fatalf("read whole, the evidence scores (the case this guards): %+v", raw)
	}
	// A quick screen has no posting view: its reasons name the seeker's C#, so
	// only the title ranks.
	screen := "report: 0013\nscore_kind: screening\n\n# Example Corp - Linux Engineer\n\n## Screen result\n\nReasons:\n- Matches the seeker's C# and Node.js background.\n"
	if got := RankReport(roster(), "Linux Engineer", "Example Corp", screen); got.Chosen != "systems" || got.Ranked[0].Reason != "linux" || got.Ranked[1].Score != 0 {
		t.Fatalf("screening: %+v", got)
	}
}
