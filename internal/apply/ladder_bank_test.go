package apply

import (
	"fmt"
	"slices"
	"testing"

	"applysmith/internal/answerbank/match"
)

// A grounded call is shown the saved answers that share terms with its
// questions, and the ones the rows name, wherever they sit in the bank; an
// entry that shares nothing is not shown. In bank order the old caps cut the
// owner's late entries (about 100 of them filled the 16,000-character budget).
func TestApply_Ladder_GroundedBankKeepsTheRelevantEntries(t *testing.T) {
	var entries []match.Entry
	for i := range 120 {
		entries = append(entries, match.Entry{ID: fmt.Sprintf("filler-%d", i), Description: fmt.Sprintf("Gardening topic number %d", i), Examples: []string{"Which flowers do you grow?"}})
	}
	entries = append(entries,
		match.Entry{ID: "build-system", Description: "The build system the applicant prefers", Examples: []string{"Preferred build system"}},
		match.Entry{ID: "team-size", Description: "Largest team the applicant has led", Examples: []string{"How large a team have you led?"}},
		match.Entry{ID: "weekend-shifts", Description: "Whether the applicant can work weekend shifts", Examples: []string{"Can you work weekends?"}},
	)
	got := relevantEntries(entries, []string{"Are you able to work weekend shifts?"}, []string{"team-size"})
	var ids []string
	for _, e := range got {
		ids = append(ids, e.ID)
	}
	if want := []string{"team-size", "weekend-shifts"}; !slices.Equal(ids, want) {
		t.Fatalf("kept %v, want %v", ids, want)
	}
	if len(relevantEntries(entries, []string{"Which flowers do you grow?"}, nil)) != maxRelevantEntries {
		t.Fatalf("a question every filler shares is cut to %d entries", maxRelevantEntries)
	}
}
