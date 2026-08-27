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

		if len(request.Input) > 0 {
			last := request.Input[len(request.Input)-1]
			if last.Type == "function_call_output" {
				emitFakeText(ctx, events, "Fake model received echo result: "+last.Output)
				return
			}
		}

		input := ""
		for index := len(request.Input) - 1; index >= 0; index-- {
			if request.Input[index].Role == "user" {
				input = request.Input[index].Text()
				break
			}
		}
		if strings.Contains(strings.ToLower(input), "echo") || strings.Contains(input, "回显") {
			item := protocol.ResponseItem{
				Type:      "function_call",
				ID:        "fc_fake_echo",
				CallID:    "call_fake_echo",
				Name:      "echo",
				Arguments: `{"text":"LOB Codex Tool Loop"}`,
			}
			if !sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseOutputItemDone, Item: &item}) {
				return
			}
			sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseCompleted})
			return
		}
		emitFakeText(ctx, events, "Fake model: "+input)
	}()

	return Stream{Events: events, Errors: errors}
}

func emitFakeText(ctx context.Context, events chan<- ResponseEvent, response string) {
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
}

func sendResponseEvent(ctx context.Context, events chan<- ResponseEvent, event ResponseEvent) bool {
	select {
	case <-ctx.Done():
		return false
	case events <- event:
		return true
	}
}
