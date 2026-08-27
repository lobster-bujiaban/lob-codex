package appserver_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	var outputDeltas strings.Builder
	sawTerminalInteraction := false
	sawChunkID := false
	for {
		var event struct {
			Type   string `json:"type"`
			Delta  string `json:"delta"`
			CallID string `json:"call_id"`
			TurnID string `json:"turn_id"`
			Name   string `json:"name"`
			Output string `json:"output"`
			Chunk  []byte `json:"chunk"`
			Stdin  string `json:"stdin"`
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
		if event.Type == "exec_command_output_delta" {
			outputDeltas.Write(event.Chunk)
		}
		if event.Type == "terminal_interaction" && event.Stdin == "hello\n" {
			sawTerminalInteraction = true
		}
		if event.Type == "tool_call_completed" {
			var output struct {
				ChunkID string `json:"chunk_id"`
			}
			if json.Unmarshal([]byte(event.Output), &output) == nil && output.ChunkID != "" {
				sawChunkID = true
			}
		}
		answer.WriteString(event.Delta)
	}
	if got, want := strings.Join(toolNames, ","), "exec_command,write_stdin"; got != want {
		t.Fatalf("tool names = %q, want %q", got, want)
	}
	if !strings.Contains(answer.String(), "process-done") {
		t.Fatalf("answer = %q, want process-done", answer.String())
	}
	sawOutputDelta := strings.Contains(outputDeltas.String(), "process-done")
	if !sawOutputDelta || !sawTerminalInteraction || !sawChunkID {
		t.Fatalf(
			"lifecycle events: output delta=%t terminal interaction=%t chunk id=%t",
			sawOutputDelta, sawTerminalInteraction, sawChunkID,
		)
	}
}

func TestChatPreservesHeadAndTailForLargeExecOutput(t *testing.T) {
	handler := appserver.NewHandler(model.NewFakeClient())
	defer handler.Close(context.Background())
	server := httptest.NewServer(handler)
	defer server.Close()

	response, err := http.Post(
		server.URL+"/api/chat",
		"application/json",
		strings.NewReader(`{"prompt":"请运行大输出演示"}`),
	)
	if err != nil {
		t.Fatalf("Post() error = %v", err)
	}
	defer response.Body.Close()

	decoder := json.NewDecoder(response.Body)
	var completedOutput string
	for {
		var event struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			TurnID string `json:"turn_id"`
			Name   string `json:"name"`
			Output string `json:"output"`
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
		if event.Type == "tool_call_completed" && event.Name == "exec_command" {
			completedOutput = event.Output
		}
	}

	var result struct {
		Output             string `json:"output"`
		OriginalTokenCount int    `json:"original_token_count"`
		OutputOmittedBytes int    `json:"output_omitted_bytes"`
	}
	if err := json.Unmarshal([]byte(completedOutput), &result); err != nil {
		t.Fatalf("exec output JSON: %v", err)
	}
	if !strings.HasPrefix(result.Output, "1\n2\n") || !strings.HasSuffix(result.Output, "50000\n") {
		t.Fatalf("output did not preserve head and tail: %q", result.Output)
	}
	if !strings.Contains(result.Output, "bytes omitted") || result.OutputOmittedBytes == 0 {
		t.Fatalf("output omission metadata = (%q, %d)", result.Output, result.OutputOmittedBytes)
	}
	if result.OriginalTokenCount < 50_000 {
		t.Fatalf("original token count = %d, want large original output", result.OriginalTokenCount)
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

func TestSessionApprovalRuleSkipsSecondPrompt(t *testing.T) {
	handler := appserver.NewHandler(model.NewFakeClient())
	defer handler.Close(context.Background())
	server := httptest.NewServer(handler)
	defer server.Close()

	runTurn := func(decision string) (int, string) {
		response, err := http.Post(
			server.URL+"/api/chat",
			"application/json",
			strings.NewReader(`{"prompt":"请运行会话规则演示"}`),
		)
		if err != nil {
			t.Fatalf("Post() error = %v", err)
		}
		defer response.Body.Close()

		approvalCount := 0
		var answer strings.Builder
		decoder := json.NewDecoder(response.Body)
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
				approvalCount++
				responseDecision := decision
				if responseDecision == "" {
					responseDecision = "approved"
				}
				approvalResponse, err := http.Post(
					server.URL+"/api/approvals/"+event.CallID,
					"application/json",
					strings.NewReader(`{"turn_id":"`+event.TurnID+`","decision":"`+responseDecision+`"}`),
				)
				if err != nil {
					t.Fatalf("approval Post() error = %v", err)
				}
				approvalResponse.Body.Close()
			}
			answer.WriteString(event.Delta)
		}
		return approvalCount, answer.String()
	}

	firstApprovals, _ := runTurn("approved_for_session")
	secondApprovals, secondAnswer := runTurn("")
	if firstApprovals != 1 || secondApprovals != 0 {
		t.Fatalf("approval counts = (%d, %d), want (1, 0)", firstApprovals, secondApprovals)
	}
	if !strings.Contains(secondAnswer, "session prefix: go version") {
		t.Fatalf("second answer = %q, want matched session rule", secondAnswer)
	}
}

func TestPersistentApprovalRuleSurvivesSessionRestart(t *testing.T) {
	t.Chdir(t.TempDir())

	runTurn := func(serverURL, decision string) (int, string) {
		response, err := http.Post(
			serverURL+"/api/chat",
			"application/json",
			strings.NewReader(`{"prompt":"请运行会话规则演示"}`),
		)
		if err != nil {
			t.Fatalf("Post() error = %v", err)
		}
		defer response.Body.Close()

		approvalCount := 0
		var answer strings.Builder
		decoder := json.NewDecoder(response.Body)
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
				approvalCount++
				approvalResponse, err := http.Post(
					serverURL+"/api/approvals/"+event.CallID,
					"application/json",
					strings.NewReader(`{"turn_id":"`+event.TurnID+`","decision":"`+decision+`"}`),
				)
				if err != nil {
					t.Fatalf("approval Post() error = %v", err)
				}
				approvalResponse.Body.Close()
			}
			answer.WriteString(event.Delta)
		}
		return approvalCount, answer.String()
	}

	firstHandler := appserver.NewHandler(model.NewFakeClient())
	firstServer := httptest.NewServer(firstHandler)
	firstApprovals, _ := runTurn(firstServer.URL, "approved_with_amendment")
	firstServer.Close()
	if err := firstHandler.Close(context.Background()); err != nil {
		t.Fatalf("first handler Close() error = %v", err)
	}
	if firstApprovals != 1 {
		t.Fatalf("first approval count = %d, want 1", firstApprovals)
	}
	if _, err := os.Stat(filepath.Join("tmp", "exec-policy.rules")); err != nil {
		t.Fatalf("persistent rule file: %v", err)
	}

	secondHandler := appserver.NewHandler(model.NewFakeClient())
	defer secondHandler.Close(context.Background())
	secondServer := httptest.NewServer(secondHandler)
	defer secondServer.Close()
	secondApprovals, secondAnswer := runTurn(secondServer.URL, "denied")
	if secondApprovals != 0 {
		t.Fatalf("second approval count = %d, want 0", secondApprovals)
	}
	if !strings.Contains(secondAnswer, "persistent prefix: go version") {
		t.Fatalf("second answer = %q, want matched persistent rule", secondAnswer)
	}
}
