package apply

import (
	"strings"
	"testing"
)

// Conduct (Ashby): "How do you rate your coding skills out of 10 without AI
// assistance?" is a number box; the composed prose "I'd rate myself 8/10 ..."
// left it NaN. The rung is asked for a number and writes only one.
func TestApply_Ladder_NumberBoxTakesABareNumber(t *testing.T) {
	q := "How do you rate your coding skills out of 10 without AI assistance?"
	if _, user := composedPrompt([]*ladderRow{{label: q, number: true}}, Own{}, "", "Engineer", "Example Corp"); !strings.Contains(user, q+" (a number only)") {
		t.Fatalf("prompt:\n%s", user)
	}
	rows := []*ladderRow{{label: q, number: true}, {label: q, number: true}}
	got := parseComposed(`[{"id":1,"answer":"8"},{"id":2,"answer":"I'd rate myself 8/10 without AI assistance."}]`, rows, Own{})
	if len(got) != 1 || got[0].value != "8" || rows[1].why != reasonNotANumber {
		t.Fatalf("answers %+v, second row %q", got, rows[1].why)
	}
}
