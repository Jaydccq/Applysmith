package pirpc

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The turn-* transcripts other than turn-edge-events are recorded stdout of
// a pi 1.0.4 process in RPC mode driven by a scripted model server, with the
// system prompt replaced and local paths removed. turn-edge-events and
// turn-provider-error are written by hand from the 1.0.4 event reference for
// the events a scripted run cannot produce cheaply (thinking, retry,
// compaction, tool progress, extension errors).

type replayed struct {
	records []Record
	stream  []StreamEvent
	usage   []UsageRecord
	nudges  []int // record index of each nudge request
	aborts  []string
	abortAt []int // record index of each abort request
}

func load(t *testing.T, name string) []Record {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var recs []Record
	if err := ReadRecords(bytes.NewReader(data), func(r Record) { recs = append(recs, r) }); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return recs
}

func replay(t *testing.T, name string, turn *Turn, hook func(i int, r Record) *Output) replayed {
	t.Helper()
	rep := replayed{records: load(t, name)}
	for i, r := range rep.records {
		out := turn.Feed(r)
		if hook != nil {
			if extra := hook(i, r); extra != nil {
				out.Stream = append(out.Stream, extra.Stream...)
			}
		}
		rep.stream = append(rep.stream, out.Stream...)
		rep.usage = append(rep.usage, out.Usage...)
		if out.Nudge {
			rep.nudges = append(rep.nudges, i)
		}
		if out.Abort != "" {
			rep.aborts = append(rep.aborts, out.Abort)
			rep.abortAt = append(rep.abortAt, i)
		}
	}
	return rep
}

func (r replayed) types() string {
	var ts []string
	for _, e := range r.stream {
		ts = append(ts, e.Type)
	}
	return strings.Join(ts, " ")
}

func (r replayed) terminal() []StreamEvent {
	var out []StreamEvent
	for _, e := range r.stream {
		if e.Type == StreamFinal || e.Type == StreamError {
			out = append(out, e)
		}
	}
	return out
}

func TestPiRPC_VersionCheck(t *testing.T) {
	for _, tc := range []struct {
		out string
		ok  bool
	}{
		{"1.0.4\n", true},
		{"1.0.4", true},
		{"0.83.0\n", false},
		{"1.1.0\n", false},
		{"v1.0.4\n", false},
		{"", false},
	} {
		if err := CheckVersion(tc.out); (err == nil) != tc.ok {
			t.Errorf("CheckVersion(%q) = %v", tc.out, err)
		}
	}
}

func TestPiRPC_LaunchSwitchesOffEverythingPiWouldDiscover(t *testing.T) {
	args := strings.Join(LaunchArgs("/x/ext.mjs", "p", "m:high"), " ")
	for _, want := range []string{"--mode rpc", "--no-session", "--no-builtin-tools", "--no-extensions", "--no-mcp", "--no-skills",
		"--no-prompt-templates", "--no-themes", "--no-context-files", "--no-approve", "-e /x/ext.mjs", "--provider p", "--model m:high"} {
		if !strings.Contains(args, want) {
			t.Errorf("args lack %q: %s", want, args)
		}
	}
	if n := strings.Count(args, " -e "); n != 1 {
		t.Errorf("%d extensions loaded, want exactly the embedded one", n)
	}
	env := strings.Join(ChildEnv("/data/agent"), " ")
	for _, want := range []string{"PI_CODING_AGENT_DIR=/data/agent", "PI_SKIP_VERSION_CHECK=1", "PI_TELEMETRY=0", "PI_OFFLINE=1"} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %q", want)
		}
	}
}

func TestPiRPC_Framing(t *testing.T) {
	// A U+2028 inside a JSON string is not a record break; CRLF is accepted.
	in := "{\"type\":\"agent_start\"}\r\n{\"type\":\"message_update\",\"assistantMessageEvent\":{\"type\":\"text_delta\",\"delta\":\"a b\"}}\n\n"
	var got []Record
	if err := ReadRecords(strings.NewReader(in), func(r Record) { got = append(got, r) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Delta == nil || got[1].Delta.Delta != "a b" {
		t.Fatalf("records = %+v", got)
	}
	for _, bad := range []string{"not json\n", "{\"nope\":1}\n", strings.Repeat("x", maxLine+10) + "\n"} {
		if err := ReadRecords(strings.NewReader(bad), func(Record) {}); err == nil {
			t.Errorf("%.20q accepted", bad)
		}
	}
}

func TestPiRPC_RecordedTurn_ToolThenAnswer(t *testing.T) {
	turn := &Turn{Phrase: func(_, raw string) string { return raw }}
	rep := replay(t, "turn-tool-then-answer.jsonl", turn, nil)

	var tokens strings.Builder
	var call, result, final *StreamEvent
	for i, e := range rep.stream {
		switch e.Type {
		case StreamToken:
			tokens.WriteString(e.Text)
		case StreamToolCall:
			call = &rep.stream[i]
		case StreamToolResult:
			result = &rep.stream[i]
		case StreamFinal:
			final = &rep.stream[i]
		}
	}
	if call == nil || call.Tool != "tracker_query" || call.Phrase != "Counting applied rows" {
		t.Fatalf("tool.call = %+v", call)
	}
	if result == nil || !result.OK || result.ToolID != call.ToolID || result.Summary != "3 rows with status Applied" {
		t.Fatalf("tool.result = %+v", result)
	}
	if final == nil || final.Text != "You have 3 applications." || tokens.String() != final.Text {
		t.Fatalf("final = %+v, tokens = %q", final, tokens.String())
	}
	if n := len(rep.terminal()); n != 1 {
		t.Fatalf("%d terminal events", n)
	}
	if rep.types() != "tool.call tool.result token token final" {
		t.Fatalf("stream order = %s", rep.types())
	}

	// The responses decode too: the prompt was accepted as started.
	var disp string
	for _, r := range rep.records {
		if r.Type == TypeResponse && r.Command == "prompt" {
			var d struct{ Disposition string }
			_ = json.Unmarshal(r.Data, &d)
			disp = d.Disposition
			if !r.Success {
				t.Fatal("prompt response not successful")
			}
		}
	}
	if disp != "started" {
		t.Fatalf("disposition = %q", disp)
	}
}

func TestPiRPC_Usage_OneRowPerResponseFromMessageEnd(t *testing.T) {
	turn := &Turn{}
	rep := replay(t, "turn-tool-then-answer.jsonl", turn, nil)
	if len(rep.usage) != 2 {
		t.Fatalf("%d usage rows, want one per model response (turn_end and agent_end repeat messages)", len(rep.usage))
	}
	var in, out int
	for _, u := range rep.usage {
		if u.Source != "assistant" || u.Outcome != "ok" || u.Provider != "fake" || u.Model != "scripted" || u.Estimated {
			t.Errorf("row = %+v", u)
		}
		in += u.Usage.Input
		out += u.Usage.Output
	}
	if in != 280 || out != 25 {
		t.Fatalf("totals in=%d out=%d", in, out)
	}
	if rep.usage[0].StopReason != "toolUse" || rep.usage[1].StopReason != "stop" {
		t.Fatalf("stop reasons = %s, %s", rep.usage[0].StopReason, rep.usage[1].StopReason)
	}
}

func TestPiRPC_Usage_MissingCountsAreFlaggedForEstimation(t *testing.T) {
	line := `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"provider":"p","model":"m","stopReason":"stop"}}`
	r, err := Decode([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	out := (&Turn{}).Feed(r)
	if len(out.Usage) != 1 || !out.Usage[0].Estimated {
		t.Fatalf("usage = %+v", out.Usage)
	}
}

func TestPiRPC_EveryEventTypeInTheContractDecodes(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range []string{"turn-edge-events.jsonl", "turn-tool-then-answer.jsonl", "turn-steer.jsonl", "turn-provider-error.jsonl"} {
		for _, r := range load(t, f) {
			if r.Type != "future_event_type" && !r.Known {
				t.Errorf("%s: %s decodes as unknown", f, r.Type)
			}
			seen[r.Type] = true
		}
	}
	for _, want := range []string{TypeResponse, TypeAgentStart, TypeAgentEnd, TypeAgentSettled, TypeTurnStart, TypeTurnEnd,
		TypeMessageStart, TypeMessageUpdate, TypeMessageEnd, TypeToolExecStart, TypeToolExecUpdate, TypeToolExecEnd,
		TypeQueueUpdate, TypeCompactionStart, TypeCompactionEnd, TypeAutoRetryStart, TypeAutoRetryEnd, TypeExtensionError, TypeEntryAppended} {
		if !seen[want] {
			t.Errorf("no fixture holds a %s record", want)
		}
	}
}

func TestPiRPC_EdgeEvents_MapToTheStreamWithoutNoise(t *testing.T) {
	turn := &Turn{}
	rep := replay(t, "turn-edge-events.jsonl", turn, nil)
	// Thinking, tool progress, retry, compaction, extension errors and an
	// unknown event type add nothing to the stream; the steering text and the
	// answer do.
	if rep.types() != "token token tool.call tool.result steer final" {
		t.Fatalf("stream = %s", rep.types())
	}
	last := rep.stream[len(rep.stream)-1]
	if last.Type != StreamFinal || last.Text != "One recruiter replied." {
		t.Fatalf("final = %+v (the retried 529 must not end the turn)", last)
	}
	// Usage: assistant, assistant (retry error, zero), tool, compaction, assistant.
	var sources, outcomes []string
	for _, u := range rep.usage {
		sources = append(sources, u.Source)
		outcomes = append(outcomes, u.Outcome)
	}
	if got := strings.Join(sources, " "); got != "assistant tool assistant compaction assistant" {
		t.Fatalf("usage sources = %s", got)
	}
	if got := strings.Join(outcomes, " "); got != "ok ok error ok ok" {
		t.Fatalf("usage outcomes = %s", got)
	}
}

func TestPiRPC_Steer_DeliveredMessageBecomesAStreamEvent(t *testing.T) {
	turn := &Turn{}
	rep := replay(t, "turn-steer.jsonl", turn, nil)
	var steers []string
	var callAt, resultAt, steerAt int
	for i, e := range rep.stream {
		switch e.Type {
		case StreamSteer:
			steers = append(steers, e.Text)
			steerAt = i
		case StreamToolCall:
			callAt = i
		case StreamToolResult:
			resultAt = i
		}
	}
	if len(steers) != 1 || steers[0] != "Please be brief." {
		t.Fatalf("steer events = %v", steers)
	}
	if !(callAt < resultAt && resultAt < steerAt) {
		t.Fatalf("steering must land after the running step's tool call finished: %s", rep.types())
	}
	if rep.terminal()[0].Type != StreamFinal {
		t.Fatalf("terminal = %+v", rep.terminal())
	}
}

func TestPiRPC_Stuck_ThreeIdenticalStepsNudgeThenStuck(t *testing.T) {
	turn := &Turn{}
	rep := replay(t, "turn-repeat.jsonl", turn, nil)
	if len(rep.nudges) != 1 {
		t.Fatalf("nudges at %v, want exactly one", rep.nudges)
	}
	if len(rep.aborts) != 1 || rep.aborts[0] != ReasonStuck {
		t.Fatalf("aborts = %v", rep.aborts)
	}
	// The nudge comes at the third identical step and the stop at the fourth.
	stepsUpTo := func(idx int) int {
		n := 0
		for _, r := range rep.records[:idx+1] {
			if r.Type == TypeMessageEnd && r.Message != nil && r.Message.Role == "assistant" && r.Message.StopReason == "toolUse" {
				n++
			}
		}
		return n
	}
	if n, s := stepsUpTo(rep.nudges[0]), stepsUpTo(rep.abortAt[0]); n != 3 || s != 4 {
		t.Fatalf("nudge at step %d, stuck at step %d", n, s)
	}
	term := rep.terminal()
	if len(term) != 1 || term[0].Reason != ReasonStuck {
		t.Fatalf("terminal = %+v", term)
	}
	if total := stepsUpTo(len(rep.records) - 1); total < 5 {
		t.Fatalf("fixture has %d steps; it must run past the stop to show nothing more is emitted", total)
	}
	for _, e := range rep.stream[indexOfTerminal(rep.stream)+1:] {
		t.Errorf("event after the stop: %+v", e)
	}
}

func indexOfTerminal(es []StreamEvent) int {
	for i, e := range es {
		if e.Type == StreamFinal || e.Type == StreamError {
			return i
		}
	}
	return -1
}

func step(text string, calls ...Call) Step { return Step{Text: text, Calls: calls} }

func call(name, args string) Call { return Call{Name: name, Args: json.RawMessage(args)} }

func TestPiRPC_RepetitionGuard(t *testing.T) {
	a := step("looking", call("tracker_query", `{"status":"Applied"}`))
	aReordered := step("looking", call("tracker_query", `{"step":"Counting rows","status":"Applied"}`))
	b := step("looking", call("tracker_query", `{"status":"Offer"}`))
	for _, tc := range []struct {
		name  string
		steps []Step
		want  []Verdict
	}{
		{"three identical then stuck", []Step{a, a, a, a}, []Verdict{Continue, Continue, Nudge, Stuck}},
		{"phrase and key order do not matter", []Step{a, aReordered, a, a}, []Verdict{Continue, Continue, Nudge, Stuck}},
		{"different arguments are not a repeat", []Step{a, b, a, b, a, b}, []Verdict{Continue, Continue, Continue, Continue, Continue, Continue}},
		{"changing course after the nudge resets", []Step{a, a, a, b, b, b, b}, []Verdict{Continue, Continue, Nudge, Continue, Continue, Nudge, Stuck}},
		{"different text is not a repeat", []Step{step("one", call("t", `{}`)), step("two", call("t", `{}`)), step("one", call("t", `{}`))}, []Verdict{Continue, Continue, Continue}},
		{"call order matters", []Step{step("", call("x", `{}`), call("y", `{}`)), step("", call("y", `{}`), call("x", `{}`)), step("", call("x", `{}`), call("y", `{}`))}, []Verdict{Continue, Continue, Continue}},
		{"numbers are compared as written", []Step{step("", call("t", `{"n":10000000000000000001}`)), step("", call("t", `{"n":10000000000000000002}`)), step("", call("t", `{"n":10000000000000000001}`))}, []Verdict{Continue, Continue, Continue}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var g RepetitionGuard
			for i, s := range tc.steps {
				if got := g.Observe(s); got != tc.want[i] {
					t.Fatalf("step %d: verdict %d, want %d", i+1, got, tc.want[i])
				}
			}
		})
	}
}

func TestPiRPC_Cancel_OneErrorEventAndLateUsageStillRecorded(t *testing.T) {
	turn := &Turn{}
	cancelled := false
	rep := replay(t, "turn-abort.jsonl", turn, func(_ int, r Record) *Output {
		if !cancelled && r.Type == TypeMessageStart && r.Message != nil && r.Message.Role == "assistant" {
			cancelled = true
			o := turn.StopWith(ReasonCancelled, "")
			return &o
		}
		return nil
	})
	term := rep.terminal()
	if len(term) != 1 || term[0].Type != StreamError || term[0].Reason != ReasonCancelled {
		t.Fatalf("terminal = %+v", term)
	}
	if rep.usage[len(rep.usage)-1].Outcome != "aborted" {
		t.Fatalf("usage = %+v", rep.usage)
	}
	if again := turn.StopWith(ReasonCancelled, ""); len(again.Stream) != 0 {
		t.Fatal("a second stop produced a second terminal event")
	}
}

func TestPiRPC_PiDiesMidTurn_RuntimeUnavailableAndNoFinal(t *testing.T) {
	turn := &Turn{}
	rep := replay(t, "turn-died.jsonl", turn, nil)
	if len(rep.terminal()) != 0 {
		t.Fatalf("the transcript ends mid-turn but terminal = %+v", rep.terminal())
	}
	out := turn.StopWith(ReasonUnavailable, "")
	if len(out.Stream) != 1 || out.Stream[0].Type != StreamError || out.Stream[0].Reason != ReasonUnavailable {
		t.Fatalf("closed = %+v", out.Stream)
	}
	if again := turn.StopWith(ReasonUnavailable, ""); len(again.Stream) != 0 {
		t.Fatal("a second close produced a second terminal event")
	}
}

func TestPiRPC_ProviderFailureAfterPiRetriesIsAnError(t *testing.T) {
	rep := replay(t, "turn-provider-error.jsonl", &Turn{}, nil)
	term := rep.terminal()
	if len(term) != 1 || term[0].Reason != ReasonProvider || !strings.Contains(term[0].Detail, "overloaded") {
		t.Fatalf("terminal = %+v", term)
	}
}

func TestPiRPC_TruncatedReplyIsAnError(t *testing.T) {
	turn := &Turn{}
	for _, line := range []string{
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"cut"}],"stopReason":"length","usage":{"input":1,"output":1,"totalTokens":2,"cost":{"total":0}}}}`,
		`{"type":"agent_settled"}`,
	} {
		r, err := Decode([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		if out := turn.Feed(r); r.Type == TypeAgentSettled && (len(out.Stream) != 1 || out.Stream[0].Reason != ReasonTruncated) {
			t.Fatalf("settled = %+v", out.Stream)
		}
	}
}

func TestPiRPC_AtMostOneTerminalEventPerRecordedTurn(t *testing.T) {
	for _, f := range []string{"turn-tool-then-answer.jsonl", "turn-steer.jsonl", "turn-edge-events.jsonl", "turn-repeat.jsonl", "turn-abort.jsonl", "turn-provider-error.jsonl"} {
		rep := replay(t, f, &Turn{}, nil)
		if n := len(rep.terminal()); n > 1 {
			t.Errorf("%s: %d terminal events", f, n)
		}
	}
}
