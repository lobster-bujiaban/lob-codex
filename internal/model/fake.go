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
	events := make(chan ResponseEvent)
	errors := make(chan error, 1)

	go func() {
		defer close(events)
		defer close(errors)

		input := ""
		for index := len(request.Input) - 1; index >= 0; index-- {
			if request.Input[index].Role == "user" {
				input = request.Input[index].Text()
				break
			}
		}
		response := "Fake model: " + input
		chunks := strings.Fields(response)

		for index, chunk := range chunks {
			if index < len(chunks)-1 {
				chunk += " "
			}
			if !sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseOutputTextDelta, Delta: chunk}) {
				return
			}
		}
		item := protocol.NewAssistantMessage(response)
		if !sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseOutputItemDone, Item: &item}) {
			return
		}
		sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseCompleted})
	}()

	return Stream{Events: events, Errors: errors}
}

func sendResponseEvent(ctx context.Context, events chan<- ResponseEvent, event ResponseEvent) bool {
	select {
	case <-ctx.Done():
		return false
	case events <- event:
		return true
	}
}
