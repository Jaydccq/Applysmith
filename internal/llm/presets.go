package llm

import (
	"sort"
	"strings"
)

// Preset is one entry of the shipped preset table. The setup wizard, the setup
// guide and the CLI all read this table; nothing else names a model per class.
type Preset struct {
	Name  string
	Label string
	Kind  string
	// BaseURL is the endpoint of an API preset.
	BaseURL string
	// KeyURL is where the user creates a key; empty for command-line presets.
	KeyURL string
	// Program is the command-line tool of a cli preset.
	Program string
	// InSetup marks the presets the setup wizard offers.
	InSetup bool
	Models  map[Class]string
	// Embedding is the embeddings model, empty when the endpoint has none.
	Embedding string
	// StructuredOutput says the endpoint accepts a JSON-schema response format.
	StructuredOutput bool
	// MaxTokensParam is the request field that carries the output ceiling.
	MaxTokensParam string
	// ReasoningStyle is how a thinking level is sent: "effort" for a
	// reasoning_effort field, "nested" for a reasoning object, "template" for
	// the chat template's enable_thinking switch, "none" when the endpoint
	// takes no such field.
	ReasoningStyle string
	// LowestEffort and MaxEffort are the smallest and largest effort strings
	// the endpoint accepts; the highest thinking level maps to MaxEffort.
	LowestEffort string
	MaxEffort    string
	// MediumEffort maps the app's medium level when an endpoint has no medium.
	MediumEffort string
	// PreserveReasoning keeps assistant reasoning_content across tool turns.
	PreserveReasoning bool
	// RequireUserPrice prevents calls until this provider instance has USD
	// prices. Regional services publish CNY prices; USD is never guessed.
	RequireUserPrice bool
	// Subscription uses included plan allowance rather than per-call cash billing.
	Subscription bool
	// OffEffort is the effort string that turns reasoning off, for an endpoint
	// that reasons when the field is omitted; empty when omission is off.
	OffEffort string
	// Temperature says the endpoint honors a temperature field on a call with
	// reasoning off; deterministic tasks then send temperature 0.
	Temperature bool
	// MandatoryReasoning lists the models that cannot turn reasoning off.
	MandatoryReasoning []string
	// WebSearch says the endpoint runs a web search for a request that asks
	// for the "web" plugin and returns url citations in the reply.
	WebSearch bool
}

var presets = []Preset{
	{
		Name: "openrouter", Label: "OpenRouter", Kind: KindOpenAI, InSetup: true,
		BaseURL: "https://openrouter.ai/api/v1", KeyURL: "https://openrouter.ai/keys",
		Models:    map[Class]string{ClassFast: "openai/gpt-6-luna", ClassStandard: "openai/gpt-6.1-sol", ClassDeep: "openai/gpt-6.1-sol"},
		Embedding: "openai/text-embedding-3-small", StructuredOutput: true,
		MaxTokensParam: "max_tokens", ReasoningStyle: "nested", LowestEffort: "low", MaxEffort: "high", OffEffort: "none",
		Temperature:        true,
		MandatoryReasoning: []string{"openai/gpt-6-luna", "openai/gpt-6.1-sol"},
		WebSearch:          true,
	},
	{
		Name: "deepseek", Label: "DeepSeek", Kind: KindOpenAI, InSetup: true,
		BaseURL: "https://api.deepseek.com", KeyURL: "https://platform.deepseek.com/api_keys",
		Models: map[Class]string{ClassFast: "deepseek-flash", ClassStandard: "deepseek-flash", ClassDeep: "deepseek-v4-pro"},
		// Both models think at effort high unless told otherwise; "none" turns
		// it off (api-docs.deepseek.com/guides/thinking_mode; see docs/architecture/llm.md).
		MaxTokensParam: "max_tokens", ReasoningStyle: "effort", LowestEffort: "low", MaxEffort: "max", OffEffort: "none",
		Temperature: true, PreserveReasoning: true,
	},
	{
		Name: "openai", Label: "OpenAI", Kind: KindOpenAI, InSetup: true,
		BaseURL: "https://api.openai.com/v1", KeyURL: "https://platform.openai.com/api-keys",
		Models:    map[Class]string{ClassFast: "gpt-6-luna", ClassStandard: "gpt-6.1-sol", ClassDeep: "gpt-6.1-sol"},
		Embedding: "text-embedding-3-small", StructuredOutput: true,
		MaxTokensParam: "max_completion_tokens", ReasoningStyle: "effort", LowestEffort: "low", MaxEffort: "max",
		MandatoryReasoning: []string{"gpt-6-luna", "gpt-6.1-sol"},
	},
	{
		Name: "anthropic", Label: "Anthropic", Kind: KindAnthropic, InSetup: true,
		BaseURL: "https://api.anthropic.com", KeyURL: "https://console.anthropic.com/settings/keys",
		Models: map[Class]string{ClassFast: "claude-haiku-5-5", ClassStandard: "claude-sonnet-5-5", ClassDeep: "claude-opus-5-5"},
	},
	{
		Name: "gemini", Label: "Google Gemini", Kind: KindOpenAI, InSetup: true,
		BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai/", KeyURL: "https://aistudio.google.com/apikey",
		Models:    map[Class]string{ClassFast: "gemini-3.5-flash-lite", ClassStandard: "gemini-3.8-flash", ClassDeep: "gemini-3.8-flash"},
		Embedding: "gemini-embedding-001", StructuredOutput: true,
		MaxTokensParam: "max_tokens", ReasoningStyle: "effort", LowestEffort: "low", MaxEffort: "high",
		MandatoryReasoning: []string{"gemini-3.5-flash-lite", "gemini-3.8-flash"},
	},
	{
		Name: "opencode-go", Label: "OpenCode Go (subscription API)", Kind: KindOpenAI, InSetup: true,
		BaseURL: "https://opencode.ai/inference/go/openai/v1", KeyURL: "https://opencode.ai/auth",
		Models:         map[Class]string{ClassFast: "glm-5.3-flash", ClassStandard: "glm-5.3", ClassDeep: "glm-5.3"},
		MaxTokensParam: "max_tokens", ReasoningStyle: "effort", LowestEffort: "low", MaxEffort: "max", MediumEffort: "high",
		MandatoryReasoning: []string{"glm-5.3-flash", "glm-5.3"}, Subscription: true,
	},
	{
		// A Kimi Code member's own API key on the OpenAI-compatible coding
		// endpoint. The membership terms require the client's real identity,
		// so requests say applysmith/1.0 (openAIRequestHeaders).
		Name: "kimi-code", Label: "Kimi Code (membership)", Kind: KindOpenAI, InSetup: true,
		BaseURL: "https://api.kimi.com/coding/v1", KeyURL: "https://www.kimi.com/en/help/kimi-code/membership-guide",
		Models:         map[Class]string{ClassFast: "kimi-for-coding", ClassStandard: "kimi-for-coding", ClassDeep: "kimi-for-coding"},
		MaxTokensParam: "max_tokens", ReasoningStyle: "none", Subscription: true,
	},
	{
		Name: "minimax", Label: "MiniMax (international API)", Kind: KindOpenAI, InSetup: true,
		BaseURL: "https://api.minimax.io/v1", KeyURL: "https://platform.minimax.io/console/access",
		Models:         map[Class]string{ClassFast: "MiniMax-M3.1-Flash", ClassStandard: "MiniMax-M3.1-Flash", ClassDeep: "MiniMax-M3.1-Flash"},
		MaxTokensParam: "max_completion_tokens", ReasoningStyle: "effort", LowestEffort: "low", MaxEffort: "max",
		MandatoryReasoning: []string{"MiniMax-M3.1-Flash"}, PreserveReasoning: true,
	},
	{
		Name: "minimax-cn", Label: "MiniMax (China API)", Kind: KindOpenAI, InSetup: true,
		BaseURL: "https://api.minimax.cn/v1", KeyURL: "https://platform.minimax.cn/console/access",
		Models:         map[Class]string{ClassFast: "MiniMax-M3.1-Flash", ClassStandard: "MiniMax-M3.1-Flash", ClassDeep: "MiniMax-M3.1-Flash"},
		MaxTokensParam: "max_completion_tokens", ReasoningStyle: "effort", LowestEffort: "low", MaxEffort: "max",
		MandatoryReasoning: []string{"MiniMax-M3.1-Flash"}, PreserveReasoning: true, RequireUserPrice: true,
	},
	{
		// General API billing; Coding Plan keys and its coding endpoint are separate.
		Name: "zai", Label: "Z.AI GLM (standard API)", Kind: KindOpenAI, InSetup: true,
		BaseURL: "https://api.z.ai/api/paas/v4", KeyURL: "https://z.ai/manage-apikey/apikey-list",
		Models:         map[Class]string{ClassFast: "glm-5.3-flash", ClassStandard: "glm-5.3", ClassDeep: "glm-5.3"},
		MaxTokensParam: "max_tokens", ReasoningStyle: "effort", LowestEffort: "low", MaxEffort: "max", MediumEffort: "high",
		MandatoryReasoning: []string{"glm-5.3-flash", "glm-5.3"},
	},
	{
		Name: "bigmodel", Label: "BigModel GLM (China standard API)", Kind: KindOpenAI, InSetup: true,
		BaseURL: "https://open.bigmodel.cn/api/paas/v4", KeyURL: "https://bigmodel.cn/usercenter/proj-mgmt/apikeys",
		Models:         map[Class]string{ClassFast: "glm-5.3-flash", ClassStandard: "glm-5.3", ClassDeep: "glm-5.3"},
		MaxTokensParam: "max_tokens", ReasoningStyle: "effort", LowestEffort: "low", MaxEffort: "max", MediumEffort: "high",
		MandatoryReasoning: []string{"glm-5.3-flash", "glm-5.3"}, RequireUserPrice: true,
	},
	{
		Name: "xai", Label: "Grok (xAI API)", Kind: KindOpenAI, InSetup: true,
		BaseURL: "https://api.x.ai/v1", KeyURL: "https://console.x.ai",
		Models:           map[Class]string{ClassFast: "grok-4.7", ClassStandard: "grok-4.7", ClassDeep: "grok-4.7"},
		StructuredOutput: true,
		MaxTokensParam:   "max_tokens", ReasoningStyle: "effort", LowestEffort: "low", MaxEffort: "xhigh",
		MandatoryReasoning: []string{"grok-4.7"},
	},
	{
		Name: "codex", Label: "Codex CLI (subscription)", Kind: KindCLI, InSetup: true, Program: "codex",
		Models: map[Class]string{ClassFast: cliDefaultModel, ClassStandard: cliDefaultModel, ClassDeep: cliDefaultModel},
	},
	{
		Name: "claude", Label: "Claude Code CLI (subscription)", Kind: KindCLI, InSetup: true, Program: "claude",
		Models: map[Class]string{ClassFast: "haiku", ClassStandard: "sonnet", ClassDeep: "opus"},
	},
	{
		// The model names are the defaults of the openai preset; the account's
		// own list is read live (ChatGPTModels) because it differs per plan.
		Name: "chatgpt", Label: "ChatGPT plan (sign in)", Kind: KindChatGPT, InSetup: true,
		BaseURL: "https://api.openai.com/v1",
		Models:  map[Class]string{ClassFast: "gpt-6-luna", ClassStandard: "gpt-6.1-sol", ClassDeep: "gpt-6.1-sol"},
		// A plan call rejects max_output_tokens and temperature, so neither is sent.
		StructuredOutput: true,
		ReasoningStyle:   "nested", LowestEffort: "low", MaxEffort: "max",
		MandatoryReasoning: []string{"gpt-6-luna", "gpt-6.1-sol"},
	},
	{
		Name: "nvidia", Label: "NVIDIA NIM", Kind: KindOpenAI,
		BaseURL: "https://integrate.api.nvidia.com/v1", KeyURL: "https://build.nvidia.com/settings/api-keys",
		Models: map[Class]string{ClassFast: "minimaxai/minimax-m3.1-flash", ClassStandard: "nvidia/nemotron-3.5-lightning-30b-a3b", ClassDeep: "nvidia/nemotron-3-ultra-550b-a55b"},
		// Its reasoning models think unless the chat template is told not to.
		MaxTokensParam: "max_tokens", ReasoningStyle: "template",
	},
}

// cliDefaultModel tells a cli provider to leave model selection to the tool.
const cliDefaultModel = "default"

// retiredModels are model ids a provider has withdrawn or the owner has ruled
// out. No preset may name one and the model picker never offers one.
var retiredModels = []string{
	"gpt-4-32k", "gpt-4-0314", "gpt-4-1106-preview", "o1-preview", "o1-mini",
	"claude-2.0", "claude-2.1", "claude-3-opus-20240229", "claude-3-sonnet-20240229",
	"claude-3-5-sonnet-20240620", "claude-3-5-sonnet-20241022",
	"claude-3-5-haiku-20241022", "claude-3-7-sonnet-20250219",
	"gemini-1.0-pro", "gemini-1.5-pro", "gemini-1.5-flash", "deepseek-coder",
	// DeepSeek lists only deepseek-flash and deepseek-v4-pro; the older names
	// are served as aliases at best.
	"deepseek-chat", "deepseek-reasoner", "deepseek-v4-flash",
	// NVIDIA NIM withdrew these or no longer lists them.
	"gpt-oss-120b", "deepseek-v4-flash-0731", "llama-3.3-70b-instruct", "deepseek-r1",
	// Ruled out by the owner on every provider (unpriced, so its calls escaped
	// the daily cap); docs/architecture/llm.md records the decision.
	"gpt-oss-20b",
}

// KnownModels lists the models the dashboard offers for each configured
// provider: the models of its preset, the priced ones, and the ones the
// routing table or a user price already names. It is a convenience for a
// picker, not a restriction: any valid model id may be routed.
func KnownModels(s Settings) []Fallback {
	seen := map[Fallback]bool{}
	add := func(prov, model string) {
		if model != "" && !strings.Contains(model, "embedding") && ValidModelID(model) == nil && !IsRetired(model) {
			seen[Fallback{prov, model}] = true
		}
	}
	for _, p := range s.Providers {
		ns := p.Preset
		if ns == "" {
			ns = p.ID
		}
		if pr, ok := PresetByName(ns); ok {
			for _, m := range pr.Models {
				add(p.ID, m)
			}
		}
		for k := range embeddedPrices {
			if m, ok := strings.CutPrefix(k, ns+"/"); ok {
				add(p.ID, m)
			}
		}
		for k := range s.Prices {
			if m, ok := strings.CutPrefix(k, p.ID+"/"); ok {
				add(p.ID, m)
			}
		}
	}
	for _, row := range s.Routes {
		add(row.Provider, row.Model)
		for _, f := range row.Fallbacks {
			add(f.Provider, f.Model)
		}
	}
	for _, g := range s.Groups {
		for _, m := range g.Members {
			add(m.Provider, m.Model)
		}
	}
	out := make([]Fallback, 0, len(seen))
	for f := range seen {
		if _, ok := s.Provider(f.Provider); ok {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// IsRetired reports whether a model id is on the retired list. A routing id
// with a vendor prefix ("vendor/model") is compared by its last segment too.
func IsRetired(model string) bool {
	last := model[strings.LastIndex(model, "/")+1:]
	for _, r := range retiredModels {
		if r == model || r == last {
			return true
		}
	}
	return false
}

// Presets returns the shipped table in order.
func Presets() []Preset { return append([]Preset(nil), presets...) }

// UsesPlan says a preset's calls draw on a subscription's allowance rather
// than per-call billing: a command-line program, the ChatGPT plan, or an API
// preset marked Subscription.
func (p Preset) UsesPlan() bool { return p.Kind == KindCLI || p.Kind == KindChatGPT || p.Subscription }

// PresetByName looks a preset up.
func PresetByName(name string) (Preset, bool) {
	for _, p := range presets {
		if p.Name == name {
			return p, true
		}
	}
	return Preset{}, false
}

// PresetNames lists the valid preset names, sorted.
func PresetNames() []string {
	out := make([]string, 0, len(presets))
	for _, p := range presets {
		out = append(out, p.Name)
	}
	sort.Strings(out)
	return out
}

// SetupPresets lists the presets the setup wizard offers.
func SetupPresets() []Preset {
	var out []Preset
	for _, p := range presets {
		if p.InSetup {
			out = append(out, p)
		}
	}
	return out
}

// ModelFor is the model the preset assigns to a task, or "" when the preset
// cannot serve the task: its required capability is missing, or no preset
// routes it (optIn).
func (p Preset) ModelFor(t Task) string {
	if optIn[t.ID] {
		return ""
	}
	if t.Class == ClassEmbedding {
		return p.Embedding
	}
	if t.Needs == CapTools && p.Kind == KindCLI {
		return ""
	}
	return p.Models[t.Class]
}

// ProviderConfig is the provider entry the preset creates. The id is the
// preset name.
func (p Preset) ProviderConfig() ProviderConfig {
	return ProviderConfig{
		ID: p.Name, Kind: p.Kind, BaseURL: p.BaseURL, Preset: p.Name, Program: p.Program,
		StructuredOutput: p.StructuredOutput, Embeddings: p.Embedding != "",
	}
}

// Unrouted lists the tasks the preset cannot serve, with the reason.
func (p Preset) Unrouted() map[string]string {
	out := map[string]string{}
	for _, t := range tasks {
		if p.ModelFor(t) != "" || optIn[t.ID] {
			continue
		}
		switch {
		case t.Class == ClassEmbedding && (p.Kind == KindCLI || p.Kind == KindChatGPT):
			out[t.ID] = "the " + p.Kind + " kind has no embeddings"
		case t.Class == ClassEmbedding:
			out[t.ID] = "the endpoint has no embeddings"
		default:
			out[t.ID] = "the " + p.Kind + " kind has no native tool calling"
		}
	}
	return out
}

func (p Preset) mandatoryReasoning(model string) bool {
	for _, m := range p.MandatoryReasoning {
		if m == model {
			return true
		}
	}
	return false
}
