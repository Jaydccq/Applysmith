package wire

// Chat event types of the Ask Agent session stream. The stream carries them
// under the one SSE event name "chat"; state is the first event of a
// connection.
const (
	ChatTurnStart  = "turn.start"
	ChatToken      = "token"
	ChatToolCall   = "tool.call"
	ChatToolResult = "tool.result"
	ChatToolHeld   = "tool.held"
	ChatSteerEvent = "steer"
	ChatCompaction = "compaction"
	ChatFinal      = "final"
	ChatError      = "error"
	ChatState      = "state"
)

// MaxChatMessage is the longest user message, in characters.
const MaxChatMessage = 8000

// ChatSession is one conversation.
type ChatSession struct {
	ID              string  `json:"id"`
	Title           string  `json:"title"`
	Channel         string  `json:"channel"`
	ParentID        string  `json:"parentId,omitempty"`
	ParentMessageID SafeInt `json:"parentMessageId,omitempty"`
	CreatedAt       Time    `json:"createdAt"`
	UpdatedAt       Time    `json:"updatedAt"`
}

// ChatSessionPage lists conversations, most recently updated first.
type ChatSessionPage struct {
	Sessions []ChatSession `json:"sessions"`
	Total    SafeInt       `json:"total"`
}

// ChatMessage is one stored row of a conversation. A tool_call row carries the
// call's arguments as text; a tool_result row says whether the tool succeeded
// and whether the system made the result after a restart.
type ChatMessage struct {
	ID       SafeInt `json:"id"`
	TurnID   string  `json:"turnId,omitempty"`
	Role     string  `json:"role"`
	Text     string  `json:"text"`
	ToolName string  `json:"toolName,omitempty"`
	// Class is the permission class of ToolName on tool_call and tool_result rows.
	Class      string `json:"class,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
	OK         bool   `json:"ok"`
	SystemMade bool   `json:"systemMade,omitempty"`
	// Citations, Limitations and NextAction are the answer envelope kept with an
	// assistant row.
	Citations   []ChatCitation `json:"citations,omitempty"`
	Limitations []string       `json:"limitations,omitempty"`
	NextAction  string         `json:"nextAction,omitempty"`
	// RunID names the run a tool_result row's call started, or the run a note
	// row tells of.
	RunID     string `json:"runId,omitempty"`
	CreatedAt Time   `json:"createdAt"`
}

// ChatTurn is the record of one turn.
type ChatTurn struct {
	ID      string `json:"id"`
	Channel string `json:"channel"`
	Origin  string `json:"origin"`
	RuleID  string `json:"ruleId,omitempty"`
	// Tier is the model tier the intent classifier chose: light or heavy.
	Tier       string `json:"tier,omitempty"`
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
	Detail     string `json:"detail,omitempty"`
	StartedAt  Time   `json:"startedAt"`
	FinishedAt *Time  `json:"finishedAt,omitempty"`
}

// ChatSessionDetail is a conversation with its rows and turns.
type ChatSessionDetail struct {
	Session      ChatSession   `json:"session"`
	Messages     []ChatMessage `json:"messages"`
	Turns        []ChatTurn    `json:"turns"`
	ActiveTurnID string        `json:"activeTurnId,omitempty"`
}

// ChatRename renames a conversation.
type ChatRename struct {
	Title string `json:"title"`
}

// ChatSend posts a user message. The session id "new" in the path starts a
// conversation with it.
type ChatSend struct {
	Text string `json:"text"`
}

// ChatTurnAccepted answers a posted message: the turn's output follows on the
// session's event stream.
type ChatTurnAccepted struct {
	SessionID string `json:"sessionId"`
	TurnID    string `json:"turnId"`
}

// ChatSteer queues a steering message for the running turn.
type ChatSteer struct {
	Text string `json:"text"`
}

// ChatSteerAccepted says how many steering messages wait for the next step
// boundary.
type ChatSteerAccepted struct {
	TurnID string  `json:"turnId"`
	Queued SafeInt `json:"queued"`
}

// ChatCancelResult answers a cancel. Runs names the background runs the turn
// had started, which continue on the runs board.
type ChatCancelResult struct {
	TurnID string   `json:"turnId"`
	Runs   []string `json:"runs"`
	Note   string   `json:"note"`
}

// ChatFork forks a conversation at a message.
type ChatFork struct {
	MessageID SafeInt `json:"messageId"`
}

// ChatDeleted answers a delete.
type ChatDeleted struct {
	ID string `json:"id"`
}

// ChatEvent is one event of a session's stream. Only the fields of its Type
// are set.
type ChatEvent struct {
	Type       string `json:"type"`
	TurnID     string `json:"turnId,omitempty"`
	Text       string `json:"text,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
	Tool       string `json:"tool,omitempty"`
	// Class is the permission class of Tool (read, draft, write-local or
	// external-action) on tool events.
	Class        string `json:"class,omitempty"`
	Phrase       string `json:"phrase,omitempty"`
	OK           *bool  `json:"ok,omitempty"`
	Summary      string `json:"summary,omitempty"`
	Code         string `json:"code,omitempty"`
	Reason       string `json:"reason,omitempty"`
	Detail       string `json:"detail,omitempty"`
	ActiveTurnID string `json:"activeTurnId,omitempty"`
	// ConfirmationID names the card of a tool.held event.
	ConfirmationID string `json:"confirmationId,omitempty"`
	// RunID names the run the call of a tool.result event started.
	RunID string `json:"runId,omitempty"`
	// Citations, Limitations and NextAction complete the answer envelope of a
	// final event: the sources behind the text, what it could not establish
	// and what the user can do next.
	Citations   []ChatCitation `json:"citations,omitempty"`
	Limitations []string       `json:"limitations,omitempty"`
	NextAction  string         `json:"nextAction,omitempty"`
}

// ChatCitation names a source of a claim in a final answer. URL is set when
// the source is a web page; it is always an http or https address.
type ChatCitation struct {
	Label   string `json:"label"`
	Snippet string `json:"snippet,omitempty"`
	URL     string `json:"url,omitempty"`
}
