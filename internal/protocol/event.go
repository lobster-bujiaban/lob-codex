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
	TurnComplete             *TurnCompleteEvent             `json:"turn_complete,omitempty"`
	TurnAborted              *TurnAbortedEvent              `json:"turn_aborted,omitempty"`
	Error                    *ErrorEvent                    `json:"error,omitempty"`
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

func NewError(message string) EventMsg {
	return EventMsg{Type: "error", Error: &ErrorEvent{Message: message}}
}
