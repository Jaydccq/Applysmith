package wire

import "encoding/json"

// The agent bridge is the protocol between the supervisor and the extension
// it embeds in the pi process. It travels on the loopback listener under a
// per-process credential and is not a dashboard API.

// AgentToolSpec is one tool of the catalog as pi registers it.
type AgentToolSpec struct {
	Name        string         `json:"name"`
	Label       string         `json:"label"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// AgentCatalog answers the extension's catalog request.
type AgentCatalog struct {
	Tools []AgentToolSpec `json:"tools"`
}

// AgentHello is the extension's report of the tools pi has active.
type AgentHello struct {
	Tools []string `json:"tools"`
}

// AgentHelloResult acknowledges a hello that matched the catalog.
type AgentHelloResult struct {
	Tools SafeInt `json:"tools"`
}

// AgentToolCall forwards one tool call; Args are exactly as pi validated them.
type AgentToolCall struct {
	ToolCallID string          `json:"toolCallId"`
	Name       string          `json:"name"`
	Args       json.RawMessage `json:"args"`
}

// AgentToolResult is the answer to a forwarded call. Text is already cut to
// what the model may see. FinalOnly asks the extension to offer the model no
// tools from the next request on. Images go to the model beside the text and
// are set only when the turn's model takes images.
type AgentToolResult struct {
	IsError   bool             `json:"isError"`
	Text      string           `json:"text"`
	FinalOnly bool             `json:"finalOnly,omitempty"`
	Images    []AgentToolImage `json:"images,omitempty"`
	// ActiveTools, set by a call that loads a skill, are the only tools the
	// extension leaves active from the next request on; Skill is the skill's
	// instruction block, which the extension adds to every later request of
	// the turn.
	ActiveTools []string `json:"activeTools,omitempty"`
	Skill       string   `json:"skill,omitempty"`
}

// AgentToolImage is one image of a tool result, its bytes base64 encoded.
type AgentToolImage struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// AgentPrefixMessage is one earlier message of the conversation, packed as
// plain text for the model.
type AgentPrefixMessage struct {
	Role      string  `json:"role"`
	Text      string  `json:"text"`
	Timestamp SafeInt `json:"timestamp"`
}

// AgentTurnContext carries the system prompt and the packed earlier turns.
type AgentTurnContext struct {
	SystemPrompt string               `json:"systemPrompt"`
	Prefix       []AgentPrefixMessage `json:"prefix"`
	// ActiveTools, set when the turn starts under a skill, are the only tools
	// the extension leaves active for it.
	ActiveTools []string `json:"activeTools,omitempty"`
}
