package llm

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"applysmith/internal/lazyregexp"
)

// Provider kinds.
const (
	KindOpenAI    = "openai-compatible"
	KindAnthropic = "anthropic"
	KindCLI       = "cli"
	// KindChatGPT is the user's ChatGPT plan, called through Sign in with
	// ChatGPT on the Responses API (chatgpt_auth.go, chatgpt_client.go).
	KindChatGPT = "chatgpt"
	KindFake    = "fake"
)

// Capability is something a provider kind can do beyond plain completion.
type Capability string

const (
	CapTools      Capability = "native tool calling"
	CapStructured Capability = "schema-constrained structured output"
	CapEmbeddings Capability = "embeddings"
	// CapWebSearch is a search the provider runs itself and answers with url
	// citations, so a reply can be checked against the pages it rests on.
	CapWebSearch Capability = "native web search"
)

// ProviderConfig is one configured provider. It holds no secret.
type ProviderConfig struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// BaseURL is required for openai-compatible and optional for anthropic.
	BaseURL string `json:"base_url,omitempty"`
	// Preset names the base-URL preset the entry came from; it also selects the
	// preset's request quirks and price namespace.
	Preset string `json:"preset,omitempty"`
	// Program is the command-line tool of a cli provider: codex or claude.
	Program string `json:"program,omitempty"`
	// StructuredOutput and Embeddings say what an openai-compatible endpoint
	// offers; presets fill them in and a custom endpoint states them.
	StructuredOutput bool `json:"structured_output,omitempty"`
	Embeddings       bool `json:"embeddings,omitempty"`
	// MaxConcurrency overrides the per-kind in-flight cap, 1 to 16.
	MaxConcurrency int `json:"max_concurrency,omitempty"`
}

// Has reports whether the provider offers a capability.
func (p ProviderConfig) Has(c Capability) bool {
	switch p.Kind {
	case KindFake:
		return c != CapWebSearch
	case KindChatGPT:
		// Tools are native on the Responses API, which the agent gateway
		// passes through from pi.
		return c == CapTools || (c == CapStructured && p.StructuredOutput)
	case KindAnthropic:
		return c == CapTools || c == CapStructured
	case KindOpenAI:
		switch c {
		case CapTools:
			return true
		case CapStructured:
			return p.StructuredOutput
		case CapEmbeddings:
			return p.Embeddings
		case CapWebSearch:
			pr, _ := PresetByName(p.Preset)
			return pr.WebSearch
		}
	}
	return false
}

// Concurrency is the in-flight cap of the provider.
func (p ProviderConfig) Concurrency() int {
	if p.MaxConcurrency > 0 {
		return p.MaxConcurrency
	}
	switch p.Kind {
	case KindCLI:
		return 2
	case KindFake:
		return 8
	}
	return 4
}

var providerID = lazyregexp.New(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// ValidateBaseURL accepts https URLs, and http only for a loopback host.
func ValidateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("base_url: %q is not a URL", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("base_url: plain http is accepted only for a loopback host, not %q", host)
	}
	return fmt.Errorf("base_url: scheme must be https, got %q", u.Scheme)
}

// Validate checks one provider entry and names the offending field.
func (p ProviderConfig) Validate() error {
	if !providerID.MatchString(p.ID) {
		return fmt.Errorf("id: %q must be 1 to 40 lowercase letters, digits, hyphen or underscore, starting with a letter or digit", p.ID)
	}
	if p.ID == GroupProvider {
		return fmt.Errorf("id: %q is reserved for routing groups", p.ID)
	}
	switch p.Kind {
	case KindOpenAI, KindChatGPT:
		if p.BaseURL == "" {
			return fmt.Errorf("base_url: required for kind %s", p.Kind)
		}
	case KindAnthropic, KindFake:
	case KindCLI:
		if p.Program != "codex" && p.Program != "claude" {
			return fmt.Errorf("program: must be codex or claude, got %q", p.Program)
		}
	default:
		return fmt.Errorf("kind: %q is not one of %s, %s, %s, %s, %s", p.Kind, KindOpenAI, KindAnthropic, KindCLI, KindChatGPT, KindFake)
	}
	if p.BaseURL != "" {
		if err := ValidateBaseURL(p.BaseURL); err != nil {
			return err
		}
	}
	if p.MaxConcurrency < 0 || p.MaxConcurrency > 16 {
		return fmt.Errorf("max_concurrency: must be between 1 and 16")
	}
	return nil
}

// CheckCapability returns the rejection message when provider cannot serve
// task, or nil. The message names the missing capability.
func CheckCapability(task Task, p ProviderConfig) error {
	if task.Needs == "" || p.Has(task.Needs) {
		return nil
	}
	switch task.Needs {
	case CapEmbeddings:
		if p.Kind == KindOpenAI {
			return fmt.Errorf("%s requires embeddings, which provider %s does not offer", task.ID, p.ID)
		}
		return fmt.Errorf("%s requires embeddings, but the %s kind has no embeddings", task.ID, p.Kind)
	}
	return fmt.Errorf("%s requires %s, which the %s kind does not offer", task.ID, task.Needs, p.Kind)
}

func isFreeModel(model string) bool { return strings.HasSuffix(model, ":free") }
