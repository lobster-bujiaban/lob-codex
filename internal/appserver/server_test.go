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

func TestChatWaitsForExecApproval(t *testing.T) {
	handler := appserver.NewHandler(model.NewFakeClient())
	defer handler.Close(context.Background())
	server := httptest.NewServer(handler)
	defer server.Close()

	response, err := http.Post(
		server.URL+"/api/chat",
		"application/json",
		strings.NewReader(`{"prompt":"请执行审批演示"}`),
	)
	if err != nil {
		t.Fatalf("Post() error = %v", err)
	}
	defer response.Body.Close()

	decoder := json.NewDecoder(response.Body)
	approved := false
	var answer strings.Builder
	for {
		var event struct {
			Type   string `json:"type"`
			Delta  string `json:"delta"`
			CallID string `json:"call_id"`
			TurnID string `json:"turn_id"`
		}
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		if event.Type == "exec_approval_request" {
			approvalResponse, err := http.Post(
				server.URL+"/api/approvals/"+event.CallID,
				"application/json",
				strings.NewReader(`{"turn_id":"`+event.TurnID+`","decision":"approved"}`),
			)
			if err != nil {
				t.Fatalf("approval Post() error = %v", err)
			}
			approvalResponse.Body.Close()
			if approvalResponse.StatusCode != http.StatusNoContent {
				t.Fatalf("approval status = %d, want %d", approvalResponse.StatusCode, http.StatusNoContent)
			}
			approved = true
		}
		answer.WriteString(event.Delta)
	}
	if !approved {
		t.Fatal("exec approval request was not emitted")
	}
	if !strings.Contains(answer.String(), "exec_command result") {
		t.Fatalf("answer = %q, want exec_command follow-up", answer.String())
	}
}

func TestChatContinuesLongRunningExecWithWriteStdin(t *testing.T) {
	handler := appserver.NewHandler(model.NewFakeClient())
	defer handler.Close(context.Background())
	server := httptest.NewServer(handler)
	defer server.Close()

	response, err := http.Post(
		server.URL+"/api/chat",
		"application/json",
		strings.NewReader(`{"prompt":"请运行长进程演示"}`),
	)
	if err != nil {
		t.Fatalf("Post() error = %v", err)
	}
	defer response.Body.Close()

	decoder := json.NewDecoder(response.Body)
	var toolNames []string
	var answer strings.Builder
	for {
		var event struct {
			Type   string `json:"type"`
			Delta  string `json:"delta"`
			CallID string `json:"call_id"`
			TurnID string `json:"turn_id"`
			Name   string `json:"name"`
		}
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		if event.Type == "exec_approval_request" {
			approvalResponse, err := http.Post(
				server.URL+"/api/approvals/"+event.CallID,
				"application/json",
				strings.NewReader(`{"turn_id":"`+event.TurnID+`","decision":"approved"}`),
			)
			if err != nil {
				t.Fatalf("approval Post() error = %v", err)
			}
			approvalResponse.Body.Close()
		}
		if event.Type == "tool_call_started" {
			toolNames = append(toolNames, event.Name)
		}
		answer.WriteString(event.Delta)
	}
	if got, want := strings.Join(toolNames, ","), "exec_command,write_stdin"; got != want {
		t.Fatalf("tool names = %q, want %q", got, want)
	}
	if !strings.Contains(answer.String(), "process-done") {
		t.Fatalf("answer = %q, want process-done", answer.String())
	}
}

func TestChatRunsInteractivePTY(t *testing.T) {
	handler := appserver.NewHandler(model.NewFakeClient())
	defer handler.Close(context.Background())
	server := httptest.NewServer(handler)
	defer server.Close()

	response, err := http.Post(
		server.URL+"/api/chat",
		"application/json",
		strings.NewReader(`{"prompt":"请运行 PTY 演示"}`),
	)
	if err != nil {
		t.Fatalf("Post() error = %v", err)
	}
	defer response.Body.Close()

	decoder := json.NewDecoder(response.Body)
	var answer strings.Builder
	for {
		var event struct {
			Type   string `json:"type"`
			Delta  string `json:"delta"`
			CallID string `json:"call_id"`
			TurnID string `json:"turn_id"`
		}
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		if event.Type == "exec_approval_request" {
			approvalResponse, err := http.Post(
				server.URL+"/api/approvals/"+event.CallID,
				"application/json",
				strings.NewReader(`{"turn_id":"`+event.TurnID+`","decision":"approved"}`),
			)
			if err != nil {
				t.Fatalf("approval Post() error = %v", err)
			}
			approvalResponse.Body.Close()
		}
		answer.WriteString(event.Delta)
	}
	if !strings.Contains(answer.String(), "pty-received:hello") {
		t.Fatalf("answer = %q, want PTY response", answer.String())
	}
}
