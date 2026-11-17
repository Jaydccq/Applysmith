// Package agent runs the Ask Agent: the tool registry and its guard chain, the
// supervisor of the pinned pi process and the bridge that the process's
// embedded extension calls, and the service the API and the channels use. The
// model loop itself runs in pi; everything that decides, records or acts is
// here.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"applysmith/internal/api/wire"
	"applysmith/internal/gates"
	"applysmith/internal/lazyregexp"
)

// Class is a tool's permission class.
type Class string

const (
	ClassRead           Class = "read"
	ClassDraft          Class = "draft"
	ClassWriteLocal     Class = "write-local"
	ClassExternalAction Class = "external-action"
)

func (c Class) valid() bool {
	switch c {
	case ClassRead, ClassDraft, ClassWriteLocal, ClassExternalAction:
		return true
	}
	return false
}

// PII levels of a tool's results.
const (
	PIINone = "none"
	PIILow  = "low"
	PIIHigh = "high"
)

// StepArg is the reserved display argument every tool accepts.
const StepArg = "step"

// MaxToolTimeout is the longest timeout a tool may declare. A call is answered
// inside the tool-call route's synchronous deadline, so a tool that takes
// longer starts a run and returns. It fits decode_posting, the slowest model
// tool; the turn's silence clock does not run while a call is in flight.
const MaxToolTimeout = 90 * time.Second

// Tool is one catalog entry. A tool without Run is declared: the guard chain
// decides its calls and audits them, but the model is not offered it, because
// nothing could execute it yet.
type Tool struct {
	Name        string
	Label       string
	Description string
	Class       Class
	// RequiresApproval holds every call until a recorded approval covers it.
	RequiresApproval bool
	// Idempotent tools run only after a claim of (tool, call id) is written.
	Idempotent bool
	// OutsideContent marks a tool whose result is text written by third
	// parties: it taints the turn.
	OutsideContent bool
	// Fresh marks a read whose answer changes on its own, such as a window's
	// controls: a repeat in the same turn runs again instead of being answered
	// from the first result.
	Fresh bool
	// Stepwise marks a tool that works one control or one answer per call: a
	// turn that calls one may make MaxStepwiseToolCalls calls (AC-5).
	Stepwise bool
	PII      string
	Timeout  time.Duration
	// Schema is the JSON Schema of the arguments, without the reserved step.
	Schema string
	Run    ToolFunc
	// Conflict, when set, returns the refusal (ErrRefused) a call would meet
	// now, such as a scan of the same source already running or an answer
	// outside the question's choices. It writes nothing. The run cap asks it
	// before it queues a start and the bridge before it holds a call for a
	// card, so a call that would be refused is answered at once.
	Conflict func(ctx context.Context, c ToolCall) error

	params map[string]any
}

// Offered reports whether the model may be offered the tool.
func (t Tool) Offered() bool { return t.Run != nil }

// DeclaresSubmit reports whether the tool takes a submit argument.
func (t Tool) DeclaresSubmit() bool {
	props, _ := t.params["properties"].(map[string]any)
	_, ok := props["submit"]
	return ok
}

// ToolCall is what a tool function receives.
type ToolCall struct {
	CallID string
	// Args are the validated arguments with the reserved step removed.
	Args json.RawMessage
	Turn *Turn
}

// ToolResult is what a tool function returns. RunID names a background run the
// call started, so a cancel can name it.
type ToolResult struct {
	Text  string
	RunID string
	// Citations, Limitations and NextAction go to the envelope of the turn's
	// final answer: the sources a draft rests on, what it could not establish
	// and what the user can do next.
	Citations   []Citation
	Limitations []string
	NextAction  string
	// Clean says the result holds no text written outside the product: an
	// empty set, or a status line the product wrote. A tool marked
	// OutsideContent taints the turn unless its result says so. The tool
	// decides from what it is returning (no rows), never from the wording.
	Clean bool
	// Outside says the result rests on text written outside the product
	// although the tool is not one that returns such text (a draft composed
	// from a stored posting). It taints the turn like an OutsideContent
	// result; it never clears a taint.
	Outside bool
	// Note is an event of the call the audit records beside the chain's own
	// decisions, such as a refusal the tool itself made.
	Note *Note
	// Images go to the model beside the text when the turn's model takes
	// images (AC-3). The record never keeps them.
	Images []Image
	// Skill names the skill this call loads into the turn; the bridge loads it.
	Skill string
	// Final ends the turn's tool calls: the model gives its answer next, as
	// after a held call (a task now waits for the user).
	Final bool
}

// Image is one image of a tool result, such as a window's screenshot.
type Image struct {
	MIMEType string
	Data     []byte
}

// ToolFunc executes one call. An error becomes an error result.
type ToolFunc func(ctx context.Context, c ToolCall) (ToolResult, error)

var (
	nameRE = lazyregexp.New(`^[a-z][a-z0-9_]{1,63}$`)
	// forbiddenNames can never be registered, whatever their metadata.
	forbiddenNames = []string{"submit_application", "apply_submit", "tracker_update", "tracker_merge", "apply_queue_approve"}
)

// Registry is the validated tool set.
type Registry struct {
	tools map[string]Tool
	order []string
}

// NewRegistry validates every tool and refuses to build when one lacks
// metadata or breaks a naming rule. The error names the tool.
func NewRegistry(tools ...Tool) (*Registry, error) {
	r := &Registry{tools: map[string]Tool{}}
	seen := map[string]string{}
	for _, t := range tools {
		if err := t.validate(); err != nil {
			return nil, fmt.Errorf("tool %q: %w", t.Name, err)
		}
		low := strings.ToLower(t.Name)
		if prev, dup := seen[low]; dup {
			return nil, fmt.Errorf("tool %q: the name is already used by %q", t.Name, prev)
		}
		seen[low] = t.Name
		var params map[string]any
		_ = json.Unmarshal([]byte(t.Schema), &params)
		t.params = params
		r.tools[t.Name] = t
		r.order = append(r.order, t.Name)
	}
	slices.Sort(r.order)
	return r, nil
}

func (t Tool) validate() error {
	low := strings.ToLower(t.Name)
	switch {
	case !nameRE.MatchString(t.Name):
		return fmt.Errorf("the name must match %s", nameRE)
	case slices.Contains(forbiddenNames, low):
		return fmt.Errorf("the name is forbidden")
	case !t.Class.valid():
		return fmt.Errorf("the class %q is not one of read, draft, write-local and external-action", t.Class)
	case t.Label == "" || t.Description == "":
		return fmt.Errorf("a label and a description are required")
	case t.Timeout <= 0:
		return fmt.Errorf("a timeout is required")
	case t.Timeout > MaxToolTimeout:
		return fmt.Errorf("the timeout is over %s; start a run and return instead", MaxToolTimeout)
	case t.PII != PIINone && t.PII != PIILow && t.PII != PIIHigh:
		return fmt.Errorf("the PII level must be none, low or high")
	case t.Idempotent && t.Class != ClassExternalAction:
		return fmt.Errorf("only an external-action tool takes an idempotency key")
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(t.Schema), &params); err != nil {
		return fmt.Errorf("the argument schema is not JSON: %w", err)
	}
	if params["type"] != "object" || params["additionalProperties"] != false {
		return fmt.Errorf("the argument schema must be an object with additionalProperties false")
	}
	props, _ := params["properties"].(map[string]any)
	if _, taken := props[StepArg]; taken {
		return fmt.Errorf("a tool cannot declare its own %q argument", StepArg)
	}
	if d := gates.SubmitRegistration(t.Name, props["submit"] != nil); !d.Allowed() {
		return fmt.Errorf("%s", d.Reason)
	}
	return nil
}

// Lookup finds a tool by its exact name.
func (r *Registry) Lookup(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Tools lists every declared tool in name order.
func (r *Registry) Tools() []Tool {
	out := make([]Tool, len(r.order))
	for i, n := range r.order {
		out[i] = r.tools[n]
	}
	return out
}

// Offered lists the tools the model may be offered on a channel: those with an
// executor that the channel's envelope holds. A scheduled or digest turn is
// given read and draft tools only, so the model never sees the rest, and only
// the dashboard is given the computer-use tools.
func (r *Registry) Offered(ch Channel) []Tool {
	var out []Tool
	for _, n := range r.order {
		if t := r.tools[n]; t.Offered() && ch.offers(t) {
			out = append(out, t)
		}
	}
	return out
}

// OfferedTo is Offered for one turn: the tools of its channel's envelope less
// those the turn withholds.
func (r *Registry) OfferedTo(t *Turn) []Tool {
	return slices.DeleteFunc(r.Offered(t.Channel), func(tl Tool) bool { return !t.permits(tl) })
}

// SpecsFor renders the tools offered to one turn for the extension: each
// schema gains the optional step argument, without a length limit because the
// system cuts it and pi would reject an over-long value as a tool error.
func (r *Registry) SpecsFor(t *Turn) []wire.AgentToolSpec { return specsOf(r.OfferedTo(t)) }

func specsOf(tools []Tool) []wire.AgentToolSpec {
	var out []wire.AgentToolSpec
	for _, t := range tools {
		params := map[string]any{}
		for k, v := range t.params {
			params[k] = v
		}
		props := map[string]any{}
		for k, v := range t.params["properties"].(map[string]any) {
			props[k] = v
		}
		props[StepArg] = map[string]any{"type": "string", "description": "A short status phrase, in the user's language, shown while the call runs."}
		params["properties"] = props
		out = append(out, wire.AgentToolSpec{Name: t.Name, Label: t.Label, Description: t.Description, Parameters: params})
	}
	return out
}
