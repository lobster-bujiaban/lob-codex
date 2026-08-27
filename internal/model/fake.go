package model

import (
	"context"
	"strings"

	"github.com/lobster-bujiaban/lob-codex/internal/protocol"
)

// FakeClient is a deterministic model implementation for local learning and tests.
type FakeClient struct{}

// NewFakeClient creates a deterministic model client that requires no network access.
func NewFakeClient() *FakeClient {
	return &FakeClient{}
}

// Stream emits a small response while preserving the same lifecycle as a real model.
func (c *FakeClient) Stream(ctx context.Context, request Request) Stream {
	events := make(chan protocol.Event)
	errors := make(chan error, 1)

	go func() {
		defer close(events)
		defer close(errors)

		response := "Fake model: " + strings.TrimSpace(request.Input)
		chunks := strings.Fields(response)

		if !sendEvent(ctx, events, protocol.Event{Type: protocol.EventResponseStarted}) {
			return
		}
		for index, chunk := range chunks {
			if index < len(chunks)-1 {
				chunk += " "
			}
			if !sendEvent(ctx, events, protocol.Event{Type: protocol.EventTextDelta, Text: chunk}) {
				return
			}
		}
		sendEvent(ctx, events, protocol.Event{Type: protocol.EventResponseCompleted})
	}()

	return Stream{Events: events, Errors: errors}
}

func sendEvent(ctx context.Context, events chan<- protocol.Event, event protocol.Event) bool {
	select {
	case <-ctx.Done():
		return false
	case events <- event:
		return true
	}
}
