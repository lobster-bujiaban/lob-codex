// Package model defines model-provider boundaries used by the agent runtime.
package model

import (
	"context"

	"github.com/lobster-bujiaban/lob-codex/internal/protocol"
)

// Request contains the provider-independent input for one model response.
type Request struct {
	Input string
}

// Stream carries model events and the terminal asynchronous error, if any.
type Stream struct {
	Events <-chan protocol.Event
	Errors <-chan error
}

// Client streams provider-independent model events.
type Client interface {
	Stream(context.Context, Request) Stream
}
