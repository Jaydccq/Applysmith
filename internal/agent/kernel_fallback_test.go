package agent

import (
	"strings"
	"testing"

	"applysmith/internal/agent/pirpc"
	"applysmith/internal/api/wire"
	"applysmith/internal/llm"
)

// The gateway walks the row's candidates inside each model call; a failure pi
// reports ends the turn, and no second process is started to try another model.
func TestAgent_Kernel_AProviderFailureStartsNoSecondProcess(t *testing.T) {
	f := newFixture(t)
	f.router.route.Fallbacks = []llm.Fallback{{Provider: "acme", Model: "scripted-2"}}
	f.launcher.script = standard(t, "turn-provider-error.jsonl", replayOptions{})
	f.send("hello")
	end := f.terminal()
	f.idle()

	if end.Type != wire.ChatError || end.Reason != pirpc.ReasonProvider {
		t.Fatalf("terminal = %+v", end)
	}
	procs := f.launcher.started()
	if len(procs) != 1 || !strings.Contains(strings.Join(procs[0].spec.Args, " "), "--model scripted-1") {
		t.Fatalf("a provider failure must not start a second process or change the model: %d processes", len(procs))
	}
}
