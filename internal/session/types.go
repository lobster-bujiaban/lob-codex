package session

import (
	"time"

	"github.com/lobster-bujiaban/lob-codex/internal/model"
	"github.com/lobster-bujiaban/lob-codex/internal/tools"
)

// OpType identifies an operation dispatched by the session submission loop.
type OpType string

const (
	OpTurnInput                OpType = "turn_input"
	OpExecApproval             OpType = "exec_approval"
	OpElicitation              OpType = "elicitation"
	OpInterrupt                OpType = "interrupt"
	OpRefreshExtensions        OpType = "refresh_extensions"
	OpCleanBackgroundTerminals OpType = "clean_background_terminals"
	OpShutdown                 OpType = "shutdown"
)

// Op is the subset of Codex session operations implemented by the current port.
type Op struct {
	Type           OpType
	Input          []TurnInput
	ExpectedTurnID string
	AdmissionReply chan TurnInputAdmission
	Approval       *ExecApprovalResponse
	Elicitation    *ElicitationResponse
	ResultReply    chan error
}

// TurnInputAdmission reports whether input started a turn or steered the active one.
type TurnInputAdmission struct {
	TurnID string
	Mode   string
	Err    error
}

// ExecApprovalResponse carries a client decision back to a waiting tool call.
type ExecApprovalResponse struct {
	CallID   string
	TurnID   string
	Decision tools.ApprovalDecision
}

type ElicitationResponse struct {
	ElicitationID string
	Action        string
	Content       map[string]any
}

// TurnInput is model-visible input reserved for one turn.
type TurnInput struct {
	Text      string
	ImageURLs []string
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
	Turn       *TurnContext
	ToolRouter *tools.Router
}

type samplingRequestResult struct {
	NeedsFollowUp    bool
	LastAgentMessage *string
	TimeToFirstToken *int64
	Usage            model.TokenUsage
	ResponseID       string
}

type taskResult struct {
	LastAgentMessage *string
	TimeToFirstToken *int64
	Err              error
	Usage            model.TokenUsage
	ResponseID       string
}
