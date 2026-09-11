// Package model defines model-provider boundaries used by the agent runtime.
package model

import (
	"context"

	"github.com/lobster-bujiaban/lob-codex/internal/protocol"
	"github.com/lobster-bujiaban/lob-codex/internal/tools"
)

// Request contains the provider-independent input for one sampling request.
type Request struct {
	Input []protocol.ResponseItem
	Tools []tools.Definition
}

// ResponseEventType identifies an internal model stream event.
type ResponseEventType string

const (
	ResponseOutputTextDelta ResponseEventType = "response.output_text.delta"
	ResponseOutputItemDone  ResponseEventType = "response.output_item.done"
	ResponseCompleted       ResponseEventType = "response.completed"
)

// ResponseEvent is mapped to public protocol events by a turn.
type ResponseEvent struct {
	Type       ResponseEventType
	Delta      string
	Item       *protocol.ResponseItem
	ResponseID string
	Usage      *TokenUsage
}

type TokenUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

func (usage *TokenUsage) Add(other *TokenUsage) {
	if usage == nil || other == nil {
		return
	}
	usage.InputTokens += other.InputTokens
	usage.CachedInputTokens += other.CachedInputTokens
	usage.CacheWriteInputTokens += other.CacheWriteInputTokens
	usage.OutputTokens += other.OutputTokens
	usage.ReasoningOutputTokens += other.ReasoningOutputTokens
	usage.TotalTokens += other.TotalTokens
}

// Stream carries model response events and the terminal asynchronous error, if any.
type Stream struct {
	Events <-chan ResponseEvent
	Errors <-chan error
}

// Client creates a model stream for one sampling request.
type Client interface {
	Stream(context.Context, Request) Stream
	ContextWindow() int
}
