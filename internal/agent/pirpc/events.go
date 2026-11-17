package pirpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Event type names the contract uses. Anything else decodes with Known false
// and is counted, never fatal, so a pi release that adds an event does not
// break a turn.
const (
	TypeResponse           = "response"
	TypeAgentStart         = "agent_start"
	TypeAgentEnd           = "agent_end"
	TypeAgentSettled       = "agent_settled"
	TypeTurnStart          = "turn_start"
	TypeTurnEnd            = "turn_end"
	TypeMessageStart       = "message_start"
	TypeMessageUpdate      = "message_update"
	TypeMessageEnd         = "message_end"
	TypeToolExecStart      = "tool_execution_start"
	TypeToolExecUpdate     = "tool_execution_update"
	TypeToolExecEnd        = "tool_execution_end"
	TypeQueueUpdate        = "queue_update"
	TypeCompactionStart    = "compaction_start"
	TypeCompactionEnd      = "compaction_end"
	TypeAutoRetryStart     = "auto_retry_start"
	TypeAutoRetryEnd       = "auto_retry_end"
	TypeSummarizationRetry = "summarization_retry_scheduled"
	TypeExtensionError     = "extension_error"
	TypeEntryAppended      = "entry_appended"
	TypeSessionInfo        = "session_info_changed"
	TypeThinkingLevel      = "thinking_level_changed"
)

var knownTypes = map[string]bool{
	TypeResponse: true, TypeAgentStart: true, TypeAgentEnd: true, TypeAgentSettled: true,
	TypeTurnStart: true, TypeTurnEnd: true, TypeMessageStart: true, TypeMessageUpdate: true,
	TypeMessageEnd: true, TypeToolExecStart: true, TypeToolExecUpdate: true, TypeToolExecEnd: true,
	TypeQueueUpdate: true, TypeCompactionStart: true, TypeCompactionEnd: true,
	TypeAutoRetryStart: true, TypeAutoRetryEnd: true, TypeSummarizationRetry: true,
	TypeExtensionError: true, TypeEntryAppended: true, TypeSessionInfo: true, TypeThinkingLevel: true,
	"summarization_retry_attempt_start": true, "summarization_retry_finished": true,
	"bash_execution_update": true,
}

// Cost is pi's price for one response, in USD, from pi's own price tables.
type Cost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	Total      float64 `json:"total"`
}

// Usage is what pi reports for one response.
type Usage struct {
	Input       int  `json:"input"`
	Output      int  `json:"output"`
	CacheRead   int  `json:"cacheRead"`
	CacheWrite  int  `json:"cacheWrite"`
	Reasoning   int  `json:"reasoning"`
	TotalTokens int  `json:"totalTokens"`
	Cost        Cost `json:"cost"`
}

// Block is one content block of a message: text, thinking or a tool call.
type Block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Message is a pi message. Content is a string or a block list.
type Message struct {
	Role         string          `json:"role"`
	Content      json.RawMessage `json:"content"`
	Provider     string          `json:"provider"`
	Model        string          `json:"model"`
	Usage        *Usage          `json:"usage"`
	StopReason   string          `json:"stopReason"`
	ErrorMessage string          `json:"errorMessage"`
	ToolCallID   string          `json:"toolCallId"`
	ToolName     string          `json:"toolName"`
	IsError      bool            `json:"isError"`
}

// Blocks returns the content as blocks; a plain string is one text block.
func (m Message) Blocks() []Block {
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return []Block{{Type: "text", Text: s}}
	}
	var bs []Block
	_ = json.Unmarshal(m.Content, &bs)
	return bs
}

// Text joins the text blocks.
func (m Message) Text() string {
	var b strings.Builder
	for _, bl := range m.Blocks() {
		if bl.Type == "text" {
			b.WriteString(bl.Text)
		}
	}
	return b.String()
}

// Delta is the assistantMessageEvent of a message_update.
type Delta struct {
	Type         string `json:"type"`
	ContentIndex int    `json:"contentIndex"`
	Delta        string `json:"delta"`
	ID           string `json:"id"`
	ToolName     string `json:"toolName"`
}

// ToolResult is the result of a tool execution.
type ToolResult struct {
	Content []Block `json:"content"`
}

// CompactionResult is the result of a pi compaction.
type CompactionResult struct {
	Summary string `json:"summary"`
	Usage   *Usage `json:"usage"`
}

// Record is one decoded line of pi's stdout: a command response or an event.
// Only the fields of its Type are set.
type Record struct {
	Type  string `json:"type"`
	Known bool   `json:"-"`

	// response; Success also carries auto_retry_end, Error also extension_error
	ID      string          `json:"id"`
	Command string          `json:"command"`
	Success bool            `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`

	// message_start, message_end, turn_end, message_update
	Message     *Message  `json:"message"`
	ToolResults []Message `json:"toolResults"`
	Usage       *Usage    `json:"usage"`
	Delta       *Delta    `json:"assistantMessageEvent"`

	// agent_end
	Messages  []Message `json:"messages"`
	WillRetry bool      `json:"willRetry"`

	// tool_execution_*
	ToolCallID    string          `json:"toolCallId"`
	ToolName      string          `json:"toolName"`
	Args          json.RawMessage `json:"args"`
	Result        json.RawMessage `json:"result"` // a ToolResult, or a CompactionResult on compaction_end
	PartialResult *ToolResult     `json:"partialResult"`
	IsError       bool            `json:"isError"`

	// queue_update
	Steering []string `json:"steering"`
	FollowUp []string `json:"followUp"`

	// compaction_*, auto_retry_*
	Reason       string `json:"reason"`
	Aborted      bool   `json:"aborted"`
	ErrorMessage string `json:"errorMessage"`
	Attempt      int    `json:"attempt"`
	MaxAttempts  int    `json:"maxAttempts"`
	FinalError   string `json:"finalError"`

	// extension_error
	ExtensionPath string `json:"extensionPath"`
	ExtensionEvt  string `json:"event"`
}

// ToolResultOf reads Result as a tool result.
func (r Record) ToolResultOf() ToolResult {
	var t ToolResult
	_ = json.Unmarshal(r.Result, &t)
	return t
}

// CompactionOf reads Result as a compaction result; nil when absent.
func (r Record) CompactionOf() *CompactionResult {
	var c CompactionResult
	if len(r.Result) == 0 || json.Unmarshal(r.Result, &c) != nil {
		return nil
	}
	return &c
}

// Decode parses one line.
func Decode(line []byte) (Record, error) {
	line = bytes.TrimSuffix(line, []byte("\r"))
	var r Record
	if err := json.Unmarshal(line, &r); err != nil {
		return Record{}, fmt.Errorf("pi record: %w", err)
	}
	if r.Type == "" {
		return Record{}, fmt.Errorf("pi record has no type")
	}
	r.Known = knownTypes[r.Type]
	return r, nil
}
