package agent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"

	"applysmith/internal/gates"
)

// ApplyRow is one posting an application run is started for. EmployerURL is a
// mirror resolution the list already made, Named says the user named the
// row's company and Mirrors counts the other links of the same job.
type ApplyRow struct {
	URL, Company, Role string
	EmployerURL        string
	Named              bool
	Mirrors            int
	Resubmit           bool
}

// ApplyRequest is what the two apply tools hand to the chain. The chain's own
// gates still decide every side effect: Submit and ExplicitRequest are only
// the facts of this call, never a permission.
type ApplyRequest struct {
	// Tool is autofill_application or apply_from_list.
	Tool string
	Rows []ApplyRow
	// Submit is the call's submit argument. ExplicitRequest is derived from
	// the turn, not from the arguments.
	Submit, ExplicitRequest bool
	// Selected says the rows already went through the apply selection (the
	// dashboard's batch route does it); otherwise the runner selects them.
	Selected       bool
	Caller         gates.Caller
	ConversationID string
	TurnID         string
	UserMessage    string
	// OwnWords are the user's own words of the turn (OwnText), which alone
	// can ask for a correction.
	OwnWords string
	// Outside says the turn had read outside content when the call was made;
	// Confirmed says the call ran on an approved confirmation card. A resumed
	// run decides its submit again from them.
	Outside, Confirmed bool
}

// ApplyRunner starts application runs in the background and returns once the
// first one has an id. It refuses while another run is driving the apply
// window. Progress and the result reach the runs board.
type ApplyRunner interface {
	StartApply(ctx context.Context, req ApplyRequest) (runID string, err error)
}

// defaultListRows is how many rows apply_from_list runs when the call names no
// limit; the schema's upper bound is 100.
const defaultListRows = 5

// applyTools declares the two apply tools and gives them an executor when the
// build can run the chain.
func applyTools(d ToolDeps) []Tool {
	tools := ApplyTools()
	if d.Apply == nil {
		return tools
	}
	for i := range tools {
		switch tools[i].Name {
		case gates.ToolAutofillApplication:
			tools[i].Run = autofillRun(d.Apply, describeFromLanes(d.ApplyLanes))
		case gates.ToolApplyFromList:
			tools[i].Run = fromListRun(d)
		}
	}
	return tools
}

func request(tool string, c ToolCall, submit bool, rows []ApplyRow) ApplyRequest {
	req := ApplyRequest{Tool: tool, Rows: rows, Submit: submit}
	if t := c.Turn; t != nil {
		req.ExplicitRequest = t.explicitApply()
		req.Caller = t.Channel.caller()
		req.ConversationID = t.SessionID
		req.TurnID = t.ID
		req.UserMessage, req.OwnWords = t.UserMessage, t.ownText()
		req.Outside, req.Confirmed = t.Untrusted(), t.Confirmed
	}
	return req
}

// autofillRun starts one run. A posting on today's apply list carries its
// company and role into the run, so the runs board and the completion note
// name the job instead of the bare host; any other takes the company and role
// the call names, which also name the employer behind a job-board repost.
func autofillRun(r ApplyRunner, describe func(context.Context, string) (string, string)) ToolFunc {
	return func(ctx context.Context, c ToolCall) (ToolResult, error) {
		var a struct {
			URL, Company, Role string
			Submit             bool
		}
		if err := decodeArgs(c.Args, &a); err != nil {
			return ToolResult{}, err
		}
		row := ApplyRow{URL: strings.TrimSpace(a.URL), Company: strings.TrimSpace(a.Company), Role: strings.TrimSpace(a.Role)}
		if describe != nil {
			if company, role := describe(ctx, row.URL); company != "" {
				row.Company, row.Role = company, role
			}
		}
		id, err := r.StartApply(ctx, request(gates.ToolAutofillApplication, c, a.Submit, []ApplyRow{row}))
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{RunID: id, Text: startedText(id, 1, a.Submit)}, nil
	}
}

func fromListRun(d ToolDeps) ToolFunc {
	return func(ctx context.Context, c ToolCall) (ToolResult, error) {
		var a struct {
			Submit bool
			Limit  int
		}
		if err := decodeArgs(c.Args, &a); err != nil {
			return ToolResult{}, err
		}
		if d.ApplyLanes == nil {
			return ToolResult{}, errors.New("today's apply list is not available")
		}
		skip := gates.DefaultSkipList
		if d.Profile != nil {
			p, err := d.Profile(ctx)
			if err != nil {
				return ToolResult{}, fmt.Errorf("the profile cannot be read, so the skip list cannot be applied: %w", err)
			}
			skip = p.EffectiveSkipCompanies()
		}
		lanes, err := d.ApplyLanes(ctx)
		if err != nil {
			return ToolResult{}, fmt.Errorf("today's apply list cannot be read: %w", err)
		}
		limit := cmp.Or(a.Limit, defaultListRows)
		var excluded []string
		if c.Turn != nil {
			excluded = excludedCompanies(c.Turn.UserMessage)
		}
		var rows []ApplyRow
		var left, dropped []string
		seen := map[string]bool{}
		for _, l := range lanes {
			for _, it := range l.Items {
				if it.URL == "" || seen[it.URL] {
					continue
				}
				seen[it.URL] = true
				if isExcluded(it.Company, excluded) {
					dropped = append(dropped, it.Company)
					continue
				}
				if s := gates.SkipCompany(gates.SkipInput{Company: it.Company, Entries: skip}); !s.Allowed() {
					left = append(left, it.Company)
					continue
				}
				if len(rows) < limit {
					rows = append(rows, ApplyRow{URL: it.URL, Company: it.Company, Role: it.Role})
				}
			}
		}
		if len(rows) == 0 {
			return ToolResult{Text: "There is nothing on today's apply list to start." + leftText(left) + droppedText(dropped)}, nil
		}
		id, err := d.Apply.StartApply(ctx, request(gates.ToolApplyFromList, c, a.Submit, rows))
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{RunID: id, Text: startedText(id, len(rows), a.Submit) + leftText(left) + droppedText(dropped)}, nil
	}
}

func startedText(id string, n int, submit bool) string {
	what := "fill the application"
	if n > 1 {
		what = fmt.Sprintf("work through %d applications one after another", n)
	}
	end := "It stops before the final Submit."
	if submit {
		end = "It presses Submit only if every safety check passes."
	}
	return fmt.Sprintf("Started to %s (run %s). Follow it on the Runs page; you are told when it needs you or finishes. %s", what, id, end)
}

func leftText(left []string) string {
	if len(left) == 0 {
		return ""
	}
	return " Left for you (skip list): " + strings.Join(left, ", ") + "."
}

func droppedText(dropped []string) string {
	if len(dropped) == 0 {
		return ""
	}
	return " Left out as you asked: " + strings.Join(dropped, ", ") + "."
}
