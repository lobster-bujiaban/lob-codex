// Package protocol defines the public events exchanged by the harness core and its clients.
package protocol

// Event wraps one protocol message and correlates it with a submission.
type Event struct {
	ID  string   `json:"id"`
	Msg EventMsg `json:"msg"`
}

// EventMsg is the tagged union of protocol events currently implemented by the Go port.
type EventMsg struct {
	Type                     string                         `json:"type"`
	TurnStarted              *TurnStartedEvent              `json:"turn_started,omitempty"`
	AgentMessageContentDelta *AgentMessageContentDeltaEvent `json:"agent_message_content_delta,omitempty"`
	ToolCallStarted          *ToolCallStartedEvent          `json:"tool_call_started,omitempty"`
	ToolCallCompleted        *ToolCallCompletedEvent        `json:"tool_call_completed,omitempty"`
	ExecCommandOutputDelta   *ExecCommandOutputDeltaEvent   `json:"exec_command_output_delta,omitempty"`
	TerminalInteraction      *TerminalInteractionEvent      `json:"terminal_interaction,omitempty"`
	ExecApprovalRequest      *ExecApprovalRequestEvent      `json:"exec_approval_request,omitempty"`
	ContextCompaction        *ContextCompactionEvent        `json:"context_compaction,omitempty"`
	TurnComplete             *TurnCompleteEvent             `json:"turn_complete,omitempty"`
	TurnAborted              *TurnAbortedEvent              `json:"turn_aborted,omitempty"`
	Error                    *ErrorEvent                    `json:"error,omitempty"`
}

type ContextCompactionEvent struct {
	TurnID       string `json:"turn_id"`
	BeforeTokens int    `json:"before_tokens"`
	AfterTokens  int    `json:"after_tokens"`
}

// ExecCommandOutputDeltaEvent carries one stdout or stderr chunk while a command runs.
type ExecCommandOutputDeltaEvent struct {
	CallID string `json:"call_id"`
	Stream string `json:"stream"`
	Chunk  []byte `json:"chunk"`
}

// TerminalInteractionEvent records stdin transport for an existing exec session.
type TerminalInteractionEvent struct {
	CallID    string `json:"call_id"`
	ProcessID string `json:"process_id"`
	Stdin     string `json:"stdin"`
}

// ExecApprovalRequestEvent asks the client to review one command invocation.
type ExecApprovalRequestEvent struct {
	CallID             string   `json:"call_id"`
	TurnID             string   `json:"turn_id"`
	StartedAtMS        int64    `json:"started_at_ms"`
	Command            []string `json:"command"`
	CWD                string   `json:"cwd"`
	Reason             string   `json:"reason,omitempty"`
	AvailableDecisions []string `json:"available_decisions"`
	ProposedPrefix     []string `json:"proposed_prefix_rule,omitempty"`
}

// ToolCallStartedEvent exposes a routed model tool invocation to clients.
type ToolCallStartedEvent struct {
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCallCompletedEvent exposes the model-visible output recorded in history.
type ToolCallCompletedEvent struct {
	CallID string `json:"call_id"`
	Name   string `json:"name"`
	Output string `json:"output"`
}

// TurnStartedEvent mirrors Codex's public turn-start lifecycle event.
type TurnStartedEvent struct {
	TurnID    string `json:"turn_id"`
	StartedAt int64  `json:"started_at"`
}

// AgentMessageContentDeltaEvent carries streamed assistant text for one turn.
type AgentMessageContentDeltaEvent struct {
	TurnID string `json:"turn_id"`
	Delta  string `json:"delta"`
}

// TurnCompleteEvent mirrors Codex's normal terminal turn event.
type TurnCompleteEvent struct {
	TurnID             string      `json:"turn_id"`
	LastAgentMessage   *string     `json:"last_agent_message,omitempty"`
	Error              *ErrorEvent `json:"error,omitempty"`
	StartedAt          int64       `json:"started_at"`
	CompletedAt        int64       `json:"completed_at"`
	DurationMS         int64       `json:"duration_ms"`
	TimeToFirstTokenMS *int64      `json:"time_to_first_token_ms,omitempty"`
	Usage              *TokenUsage `json:"usage,omitempty"`
	ResponseID         string      `json:"response_id,omitempty"`
}

type TokenUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

// TurnAbortedEvent mirrors Codex's interrupted terminal turn event.
type TurnAbortedEvent struct {
	TurnID      string `json:"turn_id"`
	Reason      string `json:"reason"`
	StartedAt   int64  `json:"started_at"`
	CompletedAt int64  `json:"completed_at"`
	DurationMS  int64  `json:"duration_ms"`
}

// ErrorEvent is a model-visible or terminal error emitted during a turn.
type ErrorEvent struct {
	Message string `json:"message"`
}

func NewTurnStarted(turnID string, startedAt int64) EventMsg {
	return EventMsg{Type: "turn_started", TurnStarted: &TurnStartedEvent{TurnID: turnID, StartedAt: startedAt}}
}

func NewAgentMessageContentDelta(turnID, delta string) EventMsg {
	return EventMsg{
		Type:                     "agent_message_content_delta",
		AgentMessageContentDelta: &AgentMessageContentDeltaEvent{TurnID: turnID, Delta: delta},
	}
}

func NewToolCallStarted(callID, name, arguments string) EventMsg {
	return EventMsg{
		Type:            "tool_call_started",
		ToolCallStarted: &ToolCallStartedEvent{CallID: callID, Name: name, Arguments: arguments},
	}
}

func NewToolCallCompleted(callID, name, output string) EventMsg {
	return EventMsg{
		Type:              "tool_call_completed",
		ToolCallCompleted: &ToolCallCompletedEvent{CallID: callID, Name: name, Output: output},
	}
}

func NewExecCommandOutputDelta(callID, stream string, chunk []byte) EventMsg {
	return EventMsg{
		Type: "exec_command_output_delta",
		ExecCommandOutputDelta: &ExecCommandOutputDeltaEvent{
			CallID: callID, Stream: stream, Chunk: append([]byte(nil), chunk...),
		},
	}
}

func NewTerminalInteraction(callID, processID, stdin string) EventMsg {
	return EventMsg{
		Type: "terminal_interaction",
		TerminalInteraction: &TerminalInteractionEvent{
			CallID: callID, ProcessID: processID, Stdin: stdin,
		},
	}
}

func NewExecApprovalRequest(
	callID, turnID, command, cwd, reason string,
	proposedPrefix []string,
	startedAtMS int64,
) EventMsg {
	availableDecisions := []string{"approved", "denied"}
	if len(proposedPrefix) > 0 {
		availableDecisions = []string{"approved", "approved_for_session", "approved_with_amendment", "denied"}
	}
	return EventMsg{
		Type: "exec_approval_request",
		ExecApprovalRequest: &ExecApprovalRequestEvent{
			CallID: callID, TurnID: turnID, StartedAtMS: startedAtMS,
			Command: []string{command}, CWD: cwd, Reason: reason,
			AvailableDecisions: availableDecisions, ProposedPrefix: proposedPrefix,
		},
	}
}

func NewError(message string) EventMsg {
	return EventMsg{Type: "error", Error: &ErrorEvent{Message: message}}
}

func NewContextCompaction(turnID string, beforeTokens, afterTokens int) EventMsg {
	return EventMsg{Type: "context_compaction", ContextCompaction: &ContextCompactionEvent{
		TurnID: turnID, BeforeTokens: beforeTokens, AfterTokens: afterTokens,
	}}
}
