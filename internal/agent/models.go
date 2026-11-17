package agent

import (
	"cmp"
	"encoding/json"
	"strings"

	"applysmith/internal/llm"
)

const (
	// piContextWindow is what the generated model entries claim. pi uses it
	// for its own overflow handling, which is off: compaction stays here.
	piContextWindow  = 128000
	defaultMaxTokens = 16000
	// gatewayProvider is the provider name pi sees for the gateway. It is none
	// of pi's built-in names, so pi detects no endpoint quirks from it.
	gatewayProvider = "applysmith"
	// gatewayPath is the bridge's model route under its origin; pi appends
	// /chat/completions, /v1/messages or /responses.
	gatewayPath = "/internal/agent/llm"
	// ModelPathOpenAI, ModelPathAnthropic and ModelPathResponses are where pi
	// sends a model call on each wire.
	ModelPathOpenAI    = gatewayPath + "/chat/completions"
	ModelPathAnthropic = gatewayPath + "/v1/messages"
	ModelPathResponses = gatewayPath + "/responses"
	// virtualModelID is the model pi is started on when a row spans several
	// wires (pi_extension.mjs routes it); the gateway provider holds it.
	virtualModelID = "agent"
	// routeEnv carries that routing to the extension.
	routeEnv = "APPLYSMITH_AGENT_ROUTE"
	// MaxModelBody bounds a model request: the conversation with its newest
	// screenshot, which pi encodes at up to 4.5 MiB.
	MaxModelBody = 32 << 20
)

// piProviderFor is the provider name pi is launched with: its own login for a
// pi: row, the gateway otherwise.
func piProviderFor(route llm.AgentRoute) string {
	if route.PiLogin != "" {
		return route.PiLogin
	}
	return gatewayName(route)
}

// piModelFor is the model pi is launched with: the row's model, or the virtual
// model that picks among the wires of a row that has several.
func piModelFor(route llm.AgentRoute) string {
	if spansWires(route) {
		return virtualModelID
	}
	return route.Model
}

// spansWires reports whether the row has candidates on more than one wire. The
// gateway never translates between wires, so pi moves a turn across them: a
// virtual model of the extension picks the wire of each request and, when a
// request failed, the next one (docs/architecture/agent-pi-contract.md).
func spansWires(route llm.AgentRoute) bool {
	return route.PiLogin == "" && len(route.Heads) > 1
}

// wireSuffix names the physical provider of a wire in a row that spans several.
var wireSuffix = map[string]string{llm.WireOpenAI: "chat", llm.WireAnthropic: "messages", llm.WireResponses: "responses"}

func wireProviderName(route llm.AgentRoute, wire string) string {
	return gatewayName(route) + "-" + wireSuffix[wire]
}

// routeSpec is what the extension reads from routeEnv: the virtual model to
// register and the physical models it picks among, in the order to try them.
type routeSpec struct {
	Provider string     `json:"provider"`
	ID       string     `json:"id"`
	Models   []specItem `json:"models"`
}

type specItem struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
}

// routeEnvValue renders routeEnv for a row that spans wires.
func routeEnvValue(route llm.AgentRoute) (string, error) {
	spec := routeSpec{Provider: gatewayName(route), ID: virtualModelID}
	for _, h := range route.Heads {
		spec.Models = append(spec.Models, specItem{Provider: wireProviderName(route, h.Wire), ID: h.Model})
	}
	b, err := json.Marshal(spec)
	return string(b), err
}

// gatewayName is the gateway provider of a row. The light row has its own
// provider beside the heavy one, so one models.json declares both and two
// turns of different tiers starting together each find their model.
func gatewayName(route llm.AgentRoute) string {
	if route.Task.ID == llm.AgentLightTask {
		return gatewayProvider + "-light"
	}
	return gatewayProvider
}

// modelsJSON renders the gateway as pi's providers, one per row served (the
// heavy row, and the light row when intent routing is on), or one per wire of
// a row whose candidates span several: the bridge's model route as the base
// URL, the wire and the model of the row's first candidate on it, and the
// process credential as the key, which authHeader sends as a bearer token on
// every wire. The provider keys stay in this process. The prices are zero
// because the gateway, not pi, records what a call cost. A pi: row is served
// from pi's own login, so no provider of ours is declared for it.
func modelsJSON(routes []llm.AgentRoute, bridgeURL string) ([]byte, error) {
	providers := map[string]any{}
	for _, route := range routes {
		switch {
		case route.PiLogin != "":
		case spansWires(route):
			// One physical provider per wire; the virtual model of the
			// extension is registered at run time and needs no entry.
			for _, h := range route.Heads {
				providers[wireProviderName(route, h.Wire)] = gatewayEntry(route, h, bridgeURL)
			}
		default:
			providers[gatewayName(route)] = gatewayEntry(route, llm.AgentHead{Wire: llm.AgentWire(route.Config), Config: route.Config, Model: route.Model}, bridgeURL)
		}
	}
	return json.MarshalIndent(map[string]any{"providers": providers}, "", "  ")
}

func gatewayEntry(route llm.AgentRoute, head llm.AgentHead, bridgeURL string) map[string]any {
	input := []string{"text"}
	if llm.TakesImages(head.Model) {
		input = append(input, "image")
	}
	model := map[string]any{
		"id": head.Model, "name": head.Model, "reasoning": route.Thinking != llm.ThinkOff, "input": input,
		"cost":          map[string]float64{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0},
		"contextWindow": piContextWindow, "maxTokens": cmp.Or(route.MaxOutputTokens, defaultMaxTokens),
	}
	if c := piCompat(head.Config, head.Model); c != nil {
		model["compat"] = c
	}
	return map[string]any{
		"baseUrl": strings.TrimRight(bridgeURL, "/") + gatewayPath, "api": head.Wire,
		"apiKey": "$APPLYSMITH_AGENT_TOKEN", "authHeader": true, "models": []any{model},
	}
}

// piCompat is what pi 1.0.4 would detect for the primary's own endpoint
// (detectCompat in pi-ai's api/openai-completions.js) in the two settings that
// shape the history it sends; behind the gateway's address it detects neither.
// The fields each endpoint reads differently are set per candidate by the
// gateway (llm.agentBody).
func piCompat(cfg llm.ProviderConfig, model string) map[string]any {
	if cfg.Kind != llm.KindOpenAI {
		return nil
	}
	switch {
	case cfg.Preset == "deepseek":
		// DeepSeek asks for an assistant message's reasoning back.
		return map[string]any{"requiresReasoningContentOnAssistantMessages": true}
	case cfg.Preset == "openrouter" && strings.HasPrefix(model, "anthropic/"):
		// Claude through OpenRouter caches only what carries cache_control.
		return map[string]any{"cacheControlFormat": "anthropic"}
	}
	return nil
}
