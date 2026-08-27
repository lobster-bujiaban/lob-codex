package model

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lobster-bujiaban/lob-codex/internal/protocol"
)

func TestOpenAIClientStreamsTextDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/responses" {
			t.Fatalf("path = %q, want /responses", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(writer, `data: {"type":"response.output_text.delta","delta":"hello "}`)
		fmt.Fprintln(writer)
		fmt.Fprintln(writer, `data: {"type":"response.output_text.delta","delta":"world"}`)
		fmt.Fprintln(writer)
		fmt.Fprintln(writer, `data: [DONE]`)
	}))
	defer server.Close()

	client, err := NewOpenAIClient(OpenAIConfig{APIKey: "test-key", BaseURL: server.URL, Model: "test-model"})
	if err != nil {
		t.Fatalf("NewOpenAIClient() error = %v", err)
	}

	stream := client.Stream(context.Background(), Request{Input: "hi"})
	var events []protocol.Event
	for event := range stream.Events {
		events = append(events, event)
	}
	for streamErr := range stream.Errors {
		if streamErr != nil {
			t.Fatalf("Stream() error = %v", streamErr)
		}
	}

	want := []protocol.Event{
		{Type: protocol.EventResponseStarted},
		{Type: protocol.EventTextDelta, Text: "hello "},
		{Type: protocol.EventTextDelta, Text: "world"},
		{Type: protocol.EventResponseCompleted},
	}
	if len(events) != len(want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
	for index := range want {
		if events[index] != want[index] {
			t.Fatalf("events = %#v, want %#v", events, want)
		}
	}
}
