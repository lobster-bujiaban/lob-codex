// Package model defines model-provider boundaries used by the agent runtime.
package model

import (
	"context"

	"github.com/lobster-bujiaban/lob-codex/internal/protocol"
)

// Request contains the provider-independent input for one sampling request.
type Request struct {
	Input []protocol.ResponseItem
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
	Type  ResponseEventType
	Delta string
	Item  *protocol.ResponseItem
}

// Stream carries model response events and the terminal asynchronous error, if any.
type Stream struct {
	Events <-chan ResponseEvent
	Errors <-chan error
}

// Client creates a model stream for one sampling request.
type Client interface {
	Stream(context.Context, Request) Stream
}
