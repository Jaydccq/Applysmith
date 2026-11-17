package llm

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Route sources, in resolution order.
const (
	SourceTable  = "table"
	SourceEnv    = "env"
	SourcePreset = "preset"
)

// NoModelSet is the Unrouted reason of a task that no table row, environment
// override or preset routes.
const NoModelSet = "no model set"

// Resolved is the effective routing row of a task and where it came from.
type Resolved struct {
	Task            Task
	Provider        string
	Model           string
	Thinking        string
	MaxOutputTokens int
	Timeout         time.Duration
	// FirstToken is the first-token deadline of streaming API attempts; zero is off.
	FirstToken time.Duration
	Fallbacks  []Fallback
	Source     string
}

// EnvVar is the environment variable that pins a task to provider/model.
func EnvVar(taskID string) string {
	return "APPLYSMITH_MODEL_" + strings.ToUpper(strings.ReplaceAll(taskID, "-", "_"))
}

func fromRoute(t Task, row Route, source string) Resolved {
	res := Resolved{Task: t, Provider: row.Provider, Model: row.Model, Thinking: row.Thinking,
		MaxOutputTokens: row.MaxOutputTokens, Timeout: time.Duration(row.TimeoutSeconds) * time.Second,
		FirstToken: time.Duration(row.FirstTokenSeconds) * time.Second, Fallbacks: row.Fallbacks, Source: source}
	if res.Thinking == "" {
		res.Thinking = t.Thinking
	}
	if res.MaxOutputTokens == 0 {
		res.MaxOutputTokens = t.MaxTokens
	}
	if res.Timeout == 0 {
		res.Timeout = t.Timeout
	}
	return res
}

// Resolve picks the task's row on every call: the stored table, then the
// environment pin, then the active preset. Nothing is cached, so a changed
// setting, variable or preset applies to the next call.
func (r *Router) Resolve(ctx context.Context, taskID string) (Resolved, error) {
	s, err := LoadSettings(ctx, r.store)
	if err != nil {
		return Resolved{}, err
	}
	return r.resolveWith(s, taskID)
}

func (r *Router) resolveWith(s Settings, taskID string) (Resolved, error) {
	t, ok := LookupTask(taskID)
	if !ok {
		return Resolved{}, &Error{Kind: KindUnknownTask, Task: taskID, Message: "not a task id"}
	}
	if r.offline || s.Offline {
		return Resolved{Task: t, Provider: offlineProvider, Model: "fake", Thinking: t.Thinking,
			MaxOutputTokens: t.MaxTokens, Timeout: t.Timeout, Source: SourcePreset}, nil
	}
	if row, ok := s.Routes[taskID]; ok {
		if err := s.ValidateRoute(taskID, row); err == nil {
			return fromRoute(t, row, SourceTable), nil
		} else {
			r.warnOnce("route:"+taskID, "stored route skipped: "+err.Error())
		}
	}
	if raw := strings.TrimSpace(r.getenv(EnvVar(taskID))); raw != "" {
		prov, model, ok := strings.Cut(raw, "/")
		p, known := s.Provider(prov)
		if _, isPi := PiLogin(prov); isPi && taskID == AgentTask {
			p, known = ProviderConfig{}, true
		}
		switch {
		case !ok || prov == "" || model == "":
			r.warnOnce("env:"+taskID, EnvVar(taskID)+" must be provider/model; ignored")
		case ValidModelID(model) != nil:
			r.warnOnce("env:"+taskID, EnvVar(taskID)+": "+ValidModelID(model).Error()+"; ignored")
		case !known:
			r.warnOnce("env:"+taskID, EnvVar(taskID)+" names an unconfigured provider; ignored")
		case p.ID != "" && CheckCapability(t, p) != nil:
			r.warnOnce("env:"+taskID, EnvVar(taskID)+": "+CheckCapability(t, p).Error()+"; ignored")
		default:
			return fromRoute(t, Route{Provider: prov, Model: model}, SourceEnv), nil
		}
	}
	if row, ok := presetRow(s, t); ok {
		return fromRoute(t, row, SourcePreset), nil
	}
	return Resolved{}, &Error{Kind: KindNoRoute, Task: taskID,
		Message: fmt.Sprintf("no model is routed for %s; run `applysmith llm configure --preset <name> --key-stdin`", taskID)}
}

// presetRow is the active preset's default row for a task.
func presetRow(s Settings, t Task) (Route, bool) {
	p, ok := PresetByName(s.Preset)
	if !ok {
		return Route{}, false
	}
	prov, ok := s.Provider(p.Name)
	if !ok {
		return Route{}, false
	}
	model := p.ModelFor(t)
	if model == "" || CheckCapability(t, prov) != nil {
		return Route{}, false
	}
	row := Route{Provider: prov.ID, Model: model}
	if fbp, ok := PresetByName(s.PresetFallback); ok && fbp.Name != p.Name {
		if _, configured := s.Provider(fbp.Name); configured {
			if fm := fbp.ModelFor(t); fm != "" {
				row.Fallbacks = []Fallback{{Provider: fbp.Name, Model: fm}}
			}
		}
	}
	return row, true
}

// RoutingTable resolves every task, for `llm show` and the routing API. A task
// that cannot be routed is listed with its reason in Unrouted. A task with no
// route at all gets the short reason NoModelSet, without the command-line
// remedy of the error, so every front end can word its own next step.
type RoutingTable struct {
	Rows     []Resolved
	Unrouted map[string]string
	Broken   map[string]error
}

// Show resolves all tasks.
func (r *Router) Show(ctx context.Context) (RoutingTable, error) {
	s, err := LoadSettings(ctx, r.store)
	if err != nil {
		return RoutingTable{}, err
	}
	out := RoutingTable{Unrouted: map[string]string{}, Broken: s.BrokenRoutes()}
	var why map[string]string
	if p, ok := PresetByName(s.Preset); ok {
		why = p.Unrouted()
	}
	for _, t := range tasks {
		res, err := r.resolveWith(s, t.ID)
		if optIn[t.ID] && KindOf(err) == KindNoRoute {
			continue
		}
		if err != nil {
			out.Unrouted[t.ID] = err.Error()
			if KindOf(err) == KindNoRoute {
				out.Unrouted[t.ID] = NoModelSet
			}
			if reason, ok := why[t.ID]; ok {
				out.Unrouted[t.ID] = reason
			}
			continue
		}
		out.Rows = append(out.Rows, res)
	}
	return out, nil
}
