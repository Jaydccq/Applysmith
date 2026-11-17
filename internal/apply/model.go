package apply

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"

	"applysmith/internal/llm"
)

// MaxModelCalls is the run's budget of model calls shared by the completion
// ladder and the walker's page decisions. The fix planner and the advisory
// review have budgets of their own.
const MaxModelCalls = 12

// The routed tasks the chain calls (internal/llm owns their rows).
const (
	taskComplete     = "apply-complete"
	taskPageDecision = "apply-page-decision"
	taskFix          = "apply-fix"
)

// Why a model rung made no call.
const (
	whyNoModel      = "no-model"
	whyNoRoute      = "no-route"
	whyBudgetCapped = "budget-capped"
)

// Model is the routed model. *llm.Router satisfies it, so every call is in the
// call ledger.
type Model interface {
	Resolve(ctx context.Context, taskID string) (llm.Resolved, error)
	Complete(ctx context.Context, req llm.Request) (*llm.Response, error)
}

// callBudget counts the run's model calls.
type callBudget struct {
	mu   sync.Mutex
	used int
}

func (b *callBudget) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used >= MaxModelCalls {
		return false
	}
	b.used++
	return true
}

func (b *callBudget) spent() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

// ask makes one call of a task. counted calls take one unit of the run's
// budget before they are made. A model that is not configured, a task with no
// route and a spent budget make no call and say why in the second result; a
// failing call returns "failed:" and its kind. The text is empty in each case.
func (e *Exec) ask(ctx context.Context, task, system, prompt string, maxTokens int, counted bool) (string, string) {
	text, why, _ := e.askOwn(ctx, task, system, prompt, maxTokens, counted)
	return text, why
}

// askOwn is ask that also reports whether the task's own model answered, not
// one of its fallbacks.
func (e *Exec) askOwn(ctx context.Context, task, system, prompt string, maxTokens int, counted bool) (string, string, bool) {
	if e.noModel {
		return "", whyNoModel, false
	}
	m := e.r.d.Page.Model
	if m == nil {
		return "", whyNoModel, false
	}
	route, err := m.Resolve(ctx, task)
	if err != nil {
		return "", whyNoRoute, false
	}
	if counted {
		if !e.budget.take() {
			return "", whyBudgetCapped, false
		}
		e.Run.ModelCalls = e.budget.spent()
	}
	resp, err := m.Complete(ctx, llm.Request{Task: task, System: system, Prompt: prompt, MaxOutputTokens: maxTokens, RunID: e.Run.ID})
	if err != nil {
		return "", "failed:" + string(llm.KindOf(err)), false
	}
	return resp.Text, "", resp.Provider == route.Provider && resp.Model == route.Model
}

// jsonObjects reads the JSON objects of a model reply: a top-level array, an
// object holding one array under any key, or a single object. Anything else
// reads as no objects, so a reply that is not the contract is dropped whole.
func jsonObjects(text string) []map[string]json.RawMessage {
	text = strings.TrimSpace(text)
	if i, j := strings.Index(text, "["), strings.LastIndex(text, "]"); i >= 0 && j > i {
		var arr []map[string]json.RawMessage
		if json.Unmarshal([]byte(text[i:j+1]), &arr) == nil {
			return arr
		}
	}
	if i, j := strings.Index(text, "{"), strings.LastIndex(text, "}"); i >= 0 && j > i {
		var obj map[string]json.RawMessage
		if json.Unmarshal([]byte(text[i:j+1]), &obj) != nil {
			return nil
		}
		for _, v := range obj {
			var arr []map[string]json.RawMessage
			if json.Unmarshal(v, &arr) == nil && len(arr) > 0 {
				return arr
			}
		}
		return []map[string]json.RawMessage{obj}
	}
	return nil
}

func rawString(m map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := m[key]
	if !ok {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String(), true
	}
	return "", false
}

func rawInt(m map[string]json.RawMessage, key string) (int, bool) {
	raw, ok := m[key]
	if !ok {
		return 0, false
	}
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return n, true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if k, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return k, true
		}
	}
	return 0, false
}

func rawFloat(m map[string]json.RawMessage, key string) (float64, bool) {
	raw, ok := m[key]
	if !ok {
		return 0, false
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return f, true
	}
	return 0, false
}

func rawBool(m map[string]json.RawMessage, key string) bool {
	var b bool
	if raw, ok := m[key]; ok && json.Unmarshal(raw, &b) == nil {
		return b
	}
	return false
}

// rawStrings reads a string or an array of strings. A string that holds an
// array of strings reads as that array (Reddit: a model wrote the options it
// chose as "[\"A\",\"B\"]").
func rawStrings(m map[string]json.RawMessage, key string) []string {
	raw, ok := m[key]
	if !ok {
		return nil
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		var inner []string
		if json.Unmarshal([]byte(strings.TrimSpace(one)), &inner) == nil {
			return inner
		}
		return []string{one}
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		return many
	}
	return nil
}
