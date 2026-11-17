package agent

import (
	"context"
	"testing"

	"applysmith/internal/agent/origins"
	"applysmith/internal/api/wire"
	"applysmith/internal/gates"
)

func TestSubmitAgain_DecidesFromTheOriginalTurnAndNeverFromTheDonePress(t *testing.T) {
	const posting = "https://jobs.example.com/p/1"
	base := origins.ApplyRequest{Caller: string(gates.CallerAskAgent), Tool: gates.ToolAutofillApplication, Submit: true, Explicit: true, URL: posting}
	list := func(urls ...string) ApplyLanes {
		return func(context.Context) ([]ApplyLane, error) {
			l := ApplyLane{Lane: wire.LaneApplyNow}
			for _, u := range urls {
				l.Items = append(l.Items, ApplyItem{URL: u})
			}
			return []ApplyLane{l}, nil
		}
	}
	cases := []struct {
		name    string
		edit    func(*origins.ApplyRequest)
		lanes   ApplyLanes
		verdict gates.Verdict
	}{
		{"the user's message names the exact posting", func(r *origins.ApplyRequest) { r.UserMessage = "apply to " + posting }, nil, gates.Allow},
		{"the exact posting is named but outside content was read", func(r *origins.ApplyRequest) { r.UserMessage = "apply to " + posting; r.Outside = true }, nil, gates.Allow},
		{"the posting was not named", func(r *origins.ApplyRequest) { r.UserMessage = "apply for me" }, nil, gates.Hold},
		{"the posting was not named and outside content was read", func(r *origins.ApplyRequest) { r.Outside = true }, list(posting), gates.Hold},
		{"the posting is on today's list", func(r *origins.ApplyRequest) {}, list(posting), gates.Allow},
		{"the list is elsewhere", func(r *origins.ApplyRequest) {}, list("https://jobs.example.com/p/2"), gates.Hold},
		{"a held submit the user confirmed on its card", func(r *origins.ApplyRequest) { r.Confirmed = true }, nil, gates.AllowWithTrace},
		{"a Telegram caller never submits", func(r *origins.ApplyRequest) { r.Caller = string(gates.CallerTelegram); r.UserMessage = posting }, nil, gates.Deny},
		{"a scheduled caller never submits", func(r *origins.ApplyRequest) { r.Caller = string(gates.CallerScheduler); r.UserMessage = posting }, nil, gates.Deny},
		{"a batch over the user's list with no outside content", func(r *origins.ApplyRequest) { r.Tool = gates.ToolApplyFromList }, nil, gates.Allow},
		{"a batch after outside content", func(r *origins.ApplyRequest) { r.Tool = gates.ToolApplyFromList; r.Outside = true }, nil, gates.Hold},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := base
			c.edit(&req)
			d := SubmitAgain(context.Background(), req, c.lanes)
			if d.Verdict != c.verdict {
				t.Fatalf("verdict %s (%s), want %s", d.Verdict, d.Reason, c.verdict)
			}
		})
	}
}
