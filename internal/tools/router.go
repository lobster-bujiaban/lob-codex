// Package tools implements Codex's model-visible tool registry and routing boundary.
package tools

import (
	"context"
	"fmt"
	"sync"

	"github.com/lobster-bujiaban/lob-codex/internal/protocol"
)

// Definition is the function-tool shape advertised to the Responses API.
type Definition struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Strict      bool           `json:"strict"`
}

// Call is the provider-independent invocation built from a FunctionCall item.
type Call struct {
	CallID    string
	Name      string
	Arguments string
}

// Environment is the turn-scoped execution location captured for tool calls.
type Environment struct {
	WorkingDirectory string
	WorkspaceRoot    string
}

// ApprovalDecision is the subset of Codex review decisions implemented now.
type ApprovalDecision string

const (
	ApprovalApproved ApprovalDecision = "approved"
	ApprovalDenied   ApprovalDecision = "denied"
)

// ApprovalRequest describes one command waiting for user review.
type ApprovalRequest struct {
	CallID           string
	Command          string
	WorkingDirectory string
	Reason           string
}

// ApprovalReviewer emits a request and blocks until a decision or cancellation.
type ApprovalReviewer func(context.Context, ApprovalRequest) (ApprovalDecision, error)

// Invocation carries the call and turn environment into one tool executor.
type Invocation struct {
	Call        Call
	Environment Environment
	Reviewer    ApprovalReviewer
}

// Executor handles one registered function tool.
type Executor interface {
	Definition() Definition
	Execute(context.Context, Invocation) (string, error)
}

// Router owns the registry used both for model advertisement and execution.
type Router struct {
	mu       sync.RWMutex
	registry map[string]Executor
	order    []string
	env      Environment
	reviewer ApprovalReviewer
}

// SetApprovalReviewer connects tool execution to the owning Session.
func (router *Router) SetApprovalReviewer(reviewer ApprovalReviewer) {
	router.mu.Lock()
	defer router.mu.Unlock()
	router.reviewer = reviewer
}

// NewRouter creates an empty tool router.
func NewRouter(environment Environment) *Router {
	return &Router{registry: make(map[string]Executor), env: environment}
}

// NewDefaultRouter creates the currently implemented Codex learning tool set.
func NewDefaultRouter(environment Environment) *Router {
	router := NewRouter(environment)
	for _, executor := range []Executor{EchoExecutor{}, ExecCommandExecutor{}} {
		if err := router.Register(executor); err != nil {
			panic(err)
		}
	}
	return router
}

// Register adds a tool while rejecting duplicate names.
func (router *Router) Register(executor Executor) error {
	definition := executor.Definition()
	router.mu.Lock()
	defer router.mu.Unlock()
	if _, exists := router.registry[definition.Name]; exists {
		return fmt.Errorf("tool %q is already registered", definition.Name)
	}
	router.registry[definition.Name] = executor
	router.order = append(router.order, definition.Name)
	return nil
}

// ModelVisibleDefinitions returns the stable tool list captured by a Step.
func (router *Router) ModelVisibleDefinitions() []Definition {
	router.mu.RLock()
	defer router.mu.RUnlock()
	definitions := make([]Definition, 0, len(router.order))
	for _, name := range router.order {
		definitions = append(definitions, router.registry[name].Definition())
	}
	return definitions
}

// BuildToolCall maps a FunctionCall ResponseItem into the router's call type.
func (router *Router) BuildToolCall(item protocol.ResponseItem) (*Call, error) {
	if item.Type != "function_call" {
		return nil, nil
	}
	if item.CallID == "" {
		return nil, fmt.Errorf("function call is missing call_id")
	}
	if item.Name == "" {
		return nil, fmt.Errorf("function call %q is missing name", item.CallID)
	}
	return &Call{CallID: item.CallID, Name: item.Name, Arguments: item.Arguments}, nil
}

// Execute converts every routable failure into model-visible tool output.
func (router *Router) Execute(ctx context.Context, call Call) protocol.ResponseItem {
	router.mu.RLock()
	executor := router.registry[call.Name]
	reviewer := router.reviewer
	router.mu.RUnlock()
	if executor == nil {
		return protocol.NewFunctionCallOutput(call.CallID, fmt.Sprintf("tool %q is not registered", call.Name))
	}
	output, err := executor.Execute(ctx, Invocation{Call: call, Environment: router.env, Reviewer: reviewer})
	if err != nil {
		output = fmt.Sprintf("tool %q failed: %v", call.Name, err)
	}
	return protocol.NewFunctionCallOutput(call.CallID, output)
}
