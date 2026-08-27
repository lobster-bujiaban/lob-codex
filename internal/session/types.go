package session

import "time"

// OpType identifies an operation dispatched by the session submission loop.
type OpType string

const (
	OpTurnInput OpType = "turn_input"
	OpShutdown  OpType = "shutdown"
)

// Op is the subset of Codex session operations implemented by the current port.
type Op struct {
	Type  OpType
	Input []TurnInput
}

// TurnInput is model-visible input reserved for one turn.
type TurnInput struct {
	Text string
}

// Submission wraps an operation with the ID used to correlate emitted events.
type Submission struct {
	ID string
	Op Op
}

// TurnContext owns state that remains stable throughout one turn.
type TurnContext struct {
	SubID     string
	StartedAt time.Time
}

// StepContext is the immutable request view captured before each model sampling request.
type StepContext struct {
	Turn *TurnContext
}

type samplingRequestResult struct {
	NeedsFollowUp    bool
	LastAgentMessage *string
	TimeToFirstToken *int64
}

type taskResult struct {
	LastAgentMessage *string
	TimeToFirstToken *int64
	Err              error
}
