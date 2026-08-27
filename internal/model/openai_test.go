package model

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lobster-bujiaban/lob-codex/internal/protocol"
)

func TestOpenAIClientStreamsTextDeltas(t *testing.T) {
	const imageURL = "data:image/png;base64,aW1hZ2U="
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/responses" {
			t.Fatalf("path = %q, want /responses", request.URL.Path)
		}
		var body struct {
			Input []protocol.ResponseItem `json:"input"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(body.Input) != 1 || len(body.Input[0].Content) != 2 || body.Input[0].Content[1].ImageURL != imageURL {
			t.Fatalf("request input = %#v, want text and image", body.Input)
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

	stream := client.Stream(context.Background(), Request{Input: []protocol.ResponseItem{
		protocol.NewUserMessageWithImages("hi", []string{imageURL}),
	}})
	var events []ResponseEvent
	for event := range stream.Events {
		events = append(events, event)
	}
	for streamErr := range stream.Errors {
		if streamErr != nil {
			t.Fatalf("Stream() error = %v", streamErr)
		}
	}

	want := []ResponseEvent{
		{Type: ResponseOutputTextDelta, Delta: "hello "},
		{Type: ResponseOutputTextDelta, Delta: "world"},
		{Type: ResponseCompleted},
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
