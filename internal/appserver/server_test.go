package appserver_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lobster-bujiaban/lob-codex/internal/appserver"
	"github.com/lobster-bujiaban/lob-codex/internal/model"
)

func TestChatStreamsModelResponse(t *testing.T) {
	handler := appserver.NewHandler(model.NewFakeClient())
	defer handler.Close(context.Background())
	server := httptest.NewServer(handler)
	defer server.Close()

	response, err := http.Post(
		server.URL+"/api/chat",
		"application/json",
		strings.NewReader(`{"prompt":"hello"}`),
	)
	if err != nil {
		t.Fatalf("Post() error = %v", err)
	}
	defer response.Body.Close()

	decoder := json.NewDecoder(response.Body)
	var body strings.Builder
	for {
		var event struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		}
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		if event.Type == "assistant_delta" {
			body.WriteString(event.Delta)
		}
	}
	if got, want := body.String(), "Fake model: hello"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestChatStreamsToolLifecycleAndFollowUp(t *testing.T) {
	handler := appserver.NewHandler(model.NewFakeClient())
	defer handler.Close(context.Background())
	server := httptest.NewServer(handler)
	defer server.Close()

	response, err := http.Post(
		server.URL+"/api/chat",
		"application/json",
		strings.NewReader(`{"prompt":"请调用 echo 工具"}`),
	)
	if err != nil {
		t.Fatalf("Post() error = %v", err)
	}
	defer response.Body.Close()

	decoder := json.NewDecoder(response.Body)
	var eventTypes []string
	var answer strings.Builder
	for {
		var event struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		}
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		eventTypes = append(eventTypes, event.Type)
		answer.WriteString(event.Delta)
	}

	if len(eventTypes) < 3 || eventTypes[0] != "tool_call_started" || eventTypes[1] != "tool_call_completed" {
		t.Fatalf("event types = %v, want tool lifecycle before assistant deltas", eventTypes)
	}
	if got, want := answer.String(), "Fake model received echo result: LOB Codex Tool Loop"; got != want {
		t.Fatalf("answer = %q, want %q", got, want)
	}
}
