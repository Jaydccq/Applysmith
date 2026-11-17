package learn

import (
	"strings"
	"testing"
)

func TestAnswerBank_Candidates_PlaceholderLabels(t *testing.T) {
	for _, l := range []string{
		"", "   ", "--", "...", "—", "––", "-- No answer --", "--Select--", "—— Choose one ——", "Select", "Select one", "select an option...",
		"Choose", "Choose an option", "Please select", "Please choose one…", "Make a selection", "make a selection here", "No selection",
		"None", "none selected", "Pick one", "N/A", "na", "n/a.",
	} {
		if !IsNothingChosen(l) {
			t.Errorf("%q must read as nothing chosen", l)
		}
	}
	for _, l := range []string{"Yes", "No", "Nonexistent", "Select committee", "Not applicable to me", "Pick one of three", "2 years", "-- 5 --x"} {
		if IsNothingChosen(l) {
			t.Errorf("%q is an answer", l)
		}
	}
}

func TestAnswerBank_Candidates_ReviewerFlags(t *testing.T) {
	if f := ReviewFlags("Python (3/4), Go (4/4)"); len(f) != 1 || !strings.HasPrefix(f[0], "REVIEW:") || !strings.Contains(f[0], "(3/4)") {
		t.Fatalf("rating shorthand: %v", f)
	}
	for _, md := range []string{"**Go** and Python", "# Heading\ntext", "- bullet one\n- bullet two", "line\n* star bullet"} {
		if f := ReviewFlags(md); len(f) != 1 || !strings.Contains(f[0], "markdown") {
			t.Errorf("markdown %q: %v", md, f)
		}
	}
	if f := ReviewFlags("100 Example Street, Springfield 00000"); len(f) != 0 {
		t.Fatalf("a plain factual value is not flagged: %v", f)
	}
	if f := ReviewFlags("a **b** (1/2)"); len(f) != 2 || !strings.Contains(f[0], "rating shorthand") || !strings.Contains(f[1], "markdown") {
		t.Fatalf("both flags can apply: %v", f)
	}
}
