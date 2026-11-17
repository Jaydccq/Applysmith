package agent

import (
	"strings"
	"testing"

	"applysmith/internal/gates"
)

func TestAgent_Step_StrippedBeforeHashAndGates(t *testing.T) {
	b := newBridgeEnv(t, ChannelDashboard, "check my mail and evaluate https://jobs.example.test/p/1")
	b.call(t, "c0", "gmail_signals", `{}`)
	// Two calls that differ only in step reach the same decision with the same
	// argument hash, so the audit holds one row for both.
	one := b.call(t, "c1", "evaluate_job", `{"url":"https://jobs.example.test/p/1","step":"Evaluating it"}`)
	two := b.call(t, "c2", "evaluate_job", `{"step":"A different and much longer phrase than before","url":"https://jobs.example.test/p/1"}`)
	if one.Text != two.Text || !strings.HasPrefix(one.Text, "Held:") {
		t.Fatalf("decisions differ: %q / %q", one.Text, two.Text)
	}
	holds := 0
	var hash string
	for _, r := range mustDecisions(t, b.f, "turn_b") {
		if r.Tool == "evaluate_job" && r.Gate == GateConfirmation && r.Verdict == gates.Hold {
			holds++
			hash = r.ArgHash
		}
	}
	if holds != 1 || hash == "" {
		t.Fatalf("hold rows = %d (hash %q); calls differing only in step must hash alike", holds, hash)
	}
	a, _, _ := splitStep([]byte(`{"url":"u","step":"x"}`))
	c, _, _ := splitStep([]byte(`{"url":"u"}`))
	if argHash(a) != argHash(c) {
		t.Fatal("step changed the argument hash")
	}
}
