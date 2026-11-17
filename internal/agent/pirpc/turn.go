package pirpc

import (
	"encoding/json"
	"strings"
)

// Stream event types of the Ask Agent session stream. turn.start, tool.held
// and steer-by-request come from the system itself; the rest are produced
// here from pi events.
const (
	StreamToken      = "token"
	StreamToolCall   = "tool.call"
	StreamToolResult = "tool.result"
	StreamSteer      = "steer"
	StreamFinal      = "final"
	StreamError      = "error"
)

// Error reasons the mapping produces. The system adds cancelled and
// time-limit through Stop.
const (
	ReasonProvider    = "provider_error"
	ReasonTruncated   = "truncated"
	ReasonUnavailable = "runtime_unavailable"
	ReasonStuck       = "stuck"
	ReasonCostCap     = "cost-cap"
	ReasonCancelled   = "cancelled"
	ReasonTimeLimit   = "time-limit"
)

// StreamEvent is one event for the session stream. Only the fields of its
// Type are set.
type StreamEvent struct {
	Type    string
	Text    string // token, steer, final
	ToolID  string // tool.call, tool.result
	Tool    string
	Phrase  string // tool.call
	OK      bool   // tool.result
	Summary string // tool.result
	Reason  string // error
	Detail  string // error
}

// UsageRecord is one model response's usage for the ledger.
type UsageRecord struct {
	Source     string // assistant, tool or compaction
	Provider   string
	Model      string
	Outcome    string // ok, truncated, aborted or error
	StopReason string
	Error      string
	Usage      Usage
	Estimated  bool // pi reported no counts: the caller estimates them
}

// Output is what one record produced.
type Output struct {
	Stream []StreamEvent
	Usage  []UsageRecord
	// Nudge asks the caller to send NudgeText as a steering message.
	Nudge bool
	// Abort names the reason the caller must abort pi; the matching error
	// event is already in Stream.
	Abort string
}

// Turn follows one agent turn on one pi process.
type Turn struct {
	// Phrase turns a call's raw status phrase into the one shown; nil keeps
	// it as given.
	Phrase func(tool, raw string) string

	// queued is the steering queue as pi last reported it; delivering holds
	// the texts that left the queue and have not yet appeared as a message.
	queued     []string
	delivering []string
	last       *Message
	retrying   bool
	failure    string
	finished   bool
	guard      RepetitionGuard
}

// Feed processes one record from pi.
func (t *Turn) Feed(r Record) Output {
	var out Output
	if !r.Known {
		return out
	}
	switch r.Type {
	case TypeMessageUpdate:
		if r.Delta != nil && r.Delta.Type == "text_delta" && !t.finished {
			out.Stream = append(out.Stream, StreamEvent{Type: StreamToken, Text: r.Delta.Delta})
		}
	case TypeToolExecStart:
		if !t.finished {
			out.Stream = append(out.Stream, StreamEvent{Type: StreamToolCall, ToolID: r.ToolCallID, Tool: r.ToolName, Phrase: t.phrase(r)})
		}
	case TypeToolExecEnd:
		if !t.finished {
			res := r.ToolResultOf()
			out.Stream = append(out.Stream, StreamEvent{Type: StreamToolResult, ToolID: r.ToolCallID, Tool: r.ToolName, OK: !r.IsError, Summary: firstText(res.Content)})
		}
	case TypeQueueUpdate:
		if n := len(t.queued) - len(r.Steering); n > 0 {
			t.delivering = append(t.delivering, t.queued[:n]...)
		}
		t.queued = r.Steering
	case TypeMessageStart:
		if r.Message != nil && r.Message.Role == "user" && len(t.delivering) > 0 && r.Message.Text() == t.delivering[0] {
			t.delivering = t.delivering[1:]
			if !t.finished {
				out.Stream = append(out.Stream, StreamEvent{Type: StreamSteer, Text: r.Message.Text()})
			}
		}
	case TypeMessageEnd:
		t.messageEnd(r.Message, &out)
	case TypeCompactionEnd:
		if c := r.CompactionOf(); c != nil && c.Usage != nil {
			t.addUsage(&out, UsageRecord{Source: "compaction", Outcome: "ok"}, c.Usage)
		}
	case TypeAutoRetryStart:
		t.retrying = true
	case TypeAutoRetryEnd:
		t.retrying = false
		if !r.Success {
			t.failure = r.FinalError
		}
	case TypeAgentSettled:
		t.settle(&out)
	}
	return out
}

func (t *Turn) phrase(r Record) string {
	var a struct {
		Step string `json:"step"`
	}
	_ = json.Unmarshal(r.Args, &a)
	if t.Phrase != nil {
		return t.Phrase(r.ToolName, a.Step)
	}
	return a.Step
}

func (t *Turn) messageEnd(m *Message, out *Output) {
	if m == nil {
		return
	}
	switch m.Role {
	case "assistant":
		rec := UsageRecord{Source: "assistant", Provider: m.Provider, Model: m.Model, StopReason: m.StopReason, Outcome: outcome(m.StopReason), Error: m.ErrorMessage}
		t.addUsage(out, rec, m.Usage)
		if m.StopReason == "stop" || m.StopReason == "toolUse" {
			t.last = m
			t.observe(m, out)
		} else if !t.retrying {
			t.last = m
		}
	case "toolResult":
		if m.Usage != nil {
			t.addUsage(out, UsageRecord{Source: "tool", Provider: m.Provider, Model: m.Model, Outcome: "ok"}, m.Usage)
		}
	}
}

func (t *Turn) addUsage(out *Output, rec UsageRecord, u *Usage) {
	if u != nil {
		rec.Usage = *u
	}
	rec.Estimated = rec.Usage.TotalTokens == 0 && rec.Usage.Input == 0 && rec.Usage.Output == 0 && rec.Outcome == "ok"
	out.Usage = append(out.Usage, rec)
}

// observe feeds the repetition guard one finished step.
func (t *Turn) observe(m *Message, out *Output) {
	if t.finished {
		return
	}
	step := Step{Text: m.Text()}
	for _, b := range m.Blocks() {
		if b.Type == "toolCall" {
			step.Calls = append(step.Calls, Call{Name: b.Name, Args: b.Arguments})
		}
	}
	switch t.guard.Observe(step) {
	case Nudge:
		out.Nudge = true
	case Stuck:
		t.stop(out, ReasonStuck, "")
	}
}

func (t *Turn) settle(out *Output) {
	if t.finished {
		return
	}
	switch {
	case t.failure != "":
		t.stop(out, ReasonProvider, t.failure)
	case t.last == nil:
		t.stop(out, ReasonUnavailable, "pi settled without a reply")
	case t.last.StopReason == "stop":
		t.finished = true
		out.Stream = append(out.Stream, StreamEvent{Type: StreamFinal, Text: t.last.Text()})
	case t.last.StopReason == "length":
		t.stop(out, ReasonTruncated, "")
	default:
		t.stop(out, ReasonProvider, t.last.ErrorMessage)
	}
}

// StopWith ends the turn with an error event of the given reason and detail,
// for the reasons only the system knows: cancelled, time-limit, and pi's
// output ending while the turn is open (runtime_unavailable). Records that
// arrive afterwards still produce usage.
func (t *Turn) StopWith(reason, detail string) Output {
	var out Output
	if !t.finished {
		t.stop(&out, reason, detail)
	}
	return out
}

// Finished reports whether the turn has emitted its terminal event.
func (t *Turn) Finished() bool { return t.finished }

func (t *Turn) stop(out *Output, reason, detail string) {
	t.finished = true
	out.Stream = append(out.Stream, StreamEvent{Type: StreamError, Reason: reason, Detail: detail})
	if reason == ReasonStuck {
		out.Abort = reason
	}
}

func outcome(stop string) string {
	switch stop {
	case "stop", "toolUse":
		return "ok"
	case "length":
		return "truncated"
	case "aborted":
		return "aborted"
	}
	return "error"
}

func firstText(bs []Block) string {
	for _, b := range bs {
		if b.Type == "text" {
			return strings.TrimSpace(b.Text)
		}
	}
	return ""
}
