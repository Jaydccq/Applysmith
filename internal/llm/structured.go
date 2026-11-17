package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Structured performs a completion whose reply must validate against schema,
// and decodes the value into out. A parse or validation failure gets exactly
// one repair attempt on the same candidate; a second failure returns
// invalid-structured-output. A validation failure never advances to a fallback.
// The repair is made only when the deadline (the request's or the context's)
// leaves at least the time the first reply took, so a caller's budget is not
// spent on a repair that would be cut off.
func (r *Router) Structured(ctx context.Context, req Request, schema json.RawMessage, out any) (*Response, error) {
	parsed, err := checkSchema(schema)
	if err != nil {
		return nil, &Error{Kind: KindBadRequest, Task: req.Task, Message: err.Error()}
	}
	cs, err := r.prepare(ctx, req)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	resp, err := r.complete(ctx, cs, req, schema)
	if err != nil {
		return nil, err
	}
	val, problems := parseAndValidate(parsed, resp.Text)
	if len(problems) == 0 {
		return resp, decodeInto(val, out)
	}
	if dl, ok := callDeadline(ctx, req); ok && time.Until(dl) < time.Since(start) {
		return nil, &Error{Kind: KindInvalidStructure, Task: req.Task,
			Message: "the reply did not validate and the deadline leaves no time for a repair: " + strings.Join(problems, "; ")}
	}

	repair := req
	repair.Prompt = req.Prompt + "\n\nYour previous reply was rejected:\n- " + strings.Join(problems, "\n- ") +
		"\nThe previous reply was:\n" + resp.Text + "\nReply again with only the corrected JSON value."
	repair.pin = &Fallback{Provider: resp.Provider, Model: resp.Model}
	repair.callID, repair.attemptBase = resp.CallID, resp.Attempts
	cs2, err := r.prepare(ctx, repair)
	if err != nil {
		return nil, err
	}
	resp2, err := r.complete(ctx, cs2, repair, schema)
	if err != nil {
		return nil, err
	}
	val, problems = parseAndValidate(parsed, resp2.Text)
	if len(problems) > 0 {
		return nil, &Error{Kind: KindInvalidStructure, Task: req.Task,
			Message: "the reply did not validate after one repair: " + strings.Join(problems, "; ")}
	}
	resp2.Usage = addUsage(resp.Usage, resp2.Usage)
	resp2.CostUSD = addCost(resp.CostUSD, resp2.CostUSD)
	return resp2, decodeInto(val, out)
}

// callDeadline is the earlier of the request's deadline and the context's.
func callDeadline(ctx context.Context, req Request) (time.Time, bool) {
	dl, ok := ctx.Deadline()
	if !req.Deadline.IsZero() && (!ok || req.Deadline.Before(dl)) {
		dl, ok = req.Deadline, true
	}
	return dl, ok
}

func parseAndValidate(schema any, text string) (json.RawMessage, []string) {
	raw, err := extractJSON(text)
	if err != nil {
		return nil, []string{err.Error()}
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, []string{"the reply is not valid JSON: " + err.Error()}
	}
	var problems []string
	validate(schema, v, "$", &problems)
	return raw, problems
}

func decodeInto(raw json.RawMessage, out any) error {
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode structured output: %w", err)
	}
	return nil
}

func addUsage(a, b Usage) Usage {
	return Usage{Input: a.Input + b.Input, Output: a.Output + b.Output, CachedInput: a.CachedInput + b.CachedInput,
		CacheWrite: a.CacheWrite + b.CacheWrite, Reasoning: a.Reasoning + b.Reasoning}
}

// addCost sums two costs; one unpriced part leaves the whole unpriced.
func addCost(a, b *float64) *float64 {
	if a == nil || b == nil {
		return nil
	}
	sum := roundMicro(*a + *b)
	return &sum
}
