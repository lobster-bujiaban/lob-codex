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

func TestThreadWorkspacePersistsAndRoutesExec(t *testing.T) {
	dataRoot := t.TempDir()
	t.Chdir(dataRoot)
	workspace := filepath.Join(dataRoot, "sample-workspace")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "workspace-marker.txt"), []byte("marker"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}

	handler := appserver.NewHandler(model.NewFakeClient())
	server := httptest.NewServer(handler)
	createResponse, err := http.Post(
		server.URL+"/api/threads",
		"application/json",
		strings.NewReader(`{"workspace_root":"`+workspace+`"}`),
	)
	if err != nil {
		t.Fatalf("create thread Post() error = %v", err)
	}
	var thread struct {
		ID            string `json:"id"`
		WorkspaceRoot string `json:"workspace_root"`
	}
	if err := json.NewDecoder(createResponse.Body).Decode(&thread); err != nil {
		t.Fatalf("decode thread: %v", err)
	}
	createResponse.Body.Close()
	if createResponse.StatusCode != http.StatusCreated || thread.WorkspaceRoot != canonicalWorkspace {
		t.Fatalf("created thread = (%d, %+v)", createResponse.StatusCode, thread)
	}

	chatResponse, err := http.Post(
		server.URL+"/api/chat",
		"application/json",
		strings.NewReader(`{"thread_id":"`+thread.ID+`","prompt":"请列出文件"}`),
	)
	if err != nil {
		t.Fatalf("chat Post() error = %v", err)
	}
	var answer strings.Builder
	decoder := json.NewDecoder(chatResponse.Body)
	for {
		var event struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		}
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("decode chat event: %v", err)
		}
		answer.WriteString(event.Delta)
	}
	chatResponse.Body.Close()
	server.Close()
	if err := handler.Close(context.Background()); err != nil {
		t.Fatalf("handler Close() error = %v", err)
	}
	if !strings.Contains(answer.String(), "workspace-marker.txt") {
		t.Fatalf("answer = %q, want workspace marker", answer.String())
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "tmp", "threads", thread.ID+".json")); err != nil {
		t.Fatalf("persisted thread metadata: %v", err)
	}
	rolloutPath := filepath.Join(dataRoot, "tmp", "threads", thread.ID+".jsonl")
	if _, err := os.Stat(rolloutPath); err != nil {
		t.Fatalf("persisted thread rollout: %v", err)
	}
	rollout, err := os.OpenFile(rolloutPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open rollout: %v", err)
	}
	_, err = rollout.WriteString(`{"timestamp":"2026-08-28T00:00:00Z","ordinal":999,"type":"response_item","payload":{"type":"function_call","id":"fc_interrupted","call_id":"call_interrupted","name":"exec_command","arguments":"{}"}}` + "\n")
	if closeErr := rollout.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("append interrupted call: %v", err)
	}

	resumedHandler := appserver.NewHandler(model.NewFakeClient())
	defer resumedHandler.Close(context.Background())
	resumedServer := httptest.NewServer(resumedHandler)
	defer resumedServer.Close()
	resumeResponse, err := http.Post(
		resumedServer.URL+"/api/chat",
		"application/json",
		strings.NewReader(`{"thread_id":"`+thread.ID+`","prompt":"hello"}`),
	)
	if err != nil {
		t.Fatalf("resume chat Post() error = %v", err)
	}
	resumeResponse.Body.Close()
	if resumeResponse.StatusCode != http.StatusOK {
		t.Fatalf("resume chat status = %d", resumeResponse.StatusCode)
	}
	historyResponse, err := http.Get(resumedServer.URL + "/api/threads/" + thread.ID + "/history")
	if err != nil {
		t.Fatalf("history Get() error = %v", err)
	}
	defer historyResponse.Body.Close()
	var history struct {
		Items []struct {
			Type string `json:"type"`
			Role string `json:"role"`
		} `json:"items"`
	}
	if err := json.NewDecoder(historyResponse.Body).Decode(&history); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(history.Items) < 4 || history.Items[0].Role != "user" || history.Items[len(history.Items)-1].Role != "assistant" {
		t.Fatalf("resumed history = %+v", history.Items)
	}
	flowResponse, err := http.Get(resumedServer.URL + "/api/threads/" + thread.ID + "/flow")
	if err != nil {
		t.Fatalf("flow Get() error = %v", err)
	}
	var flow struct {
		Events []struct {
			Kind string `json:"kind"`
			Turn int    `json:"turn"`
		} `json:"events"`
		Summary struct {
			Turns      int `json:"turns"`
			ModelCalls int `json:"model_calls"`
			ToolCalls  int `json:"tool_calls"`
		} `json:"summary"`
	}
	if err := json.NewDecoder(flowResponse.Body).Decode(&flow); err != nil {
		t.Fatalf("decode flow: %v", err)
	}
	flowResponse.Body.Close()
	if len(flow.Events) < 4 || flow.Summary.Turns != 2 || flow.Summary.ModelCalls < 2 || flow.Summary.ToolCalls < 1 {
		t.Fatalf("flow = %+v", flow)
	}
	forkResponse, err := http.Post(
		resumedServer.URL+"/api/threads/"+thread.ID+"/fork",
		"application/json",
		strings.NewReader(`{"item_count":1}`),
	)
	if err != nil {
		t.Fatalf("fork Post() error = %v", err)
	}
	var forkedThread struct {
		ID            string `json:"id"`
		WorkspaceRoot string `json:"workspace_root"`
	}
	if err := json.NewDecoder(forkResponse.Body).Decode(&forkedThread); err != nil {
		t.Fatalf("decode forked thread: %v", err)
	}
	forkResponse.Body.Close()
	if forkResponse.StatusCode != http.StatusCreated || forkedThread.ID == thread.ID || forkedThread.WorkspaceRoot != canonicalWorkspace {
		t.Fatalf("forked thread = (%d, %+v)", forkResponse.StatusCode, forkedThread)
	}
	forkHistoryResponse, err := http.Get(resumedServer.URL + "/api/threads/" + forkedThread.ID + "/history")
	if err != nil {
		t.Fatalf("fork history Get() error = %v", err)
	}
	var forkHistory struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.NewDecoder(forkHistoryResponse.Body).Decode(&forkHistory); err != nil {
		t.Fatalf("decode fork history: %v", err)
	}
	forkHistoryResponse.Body.Close()
	if len(forkHistory.Items) != 1 {
		t.Fatalf("fork history item count = %d, want 1", len(forkHistory.Items))
	}
	removeRequest, err := http.NewRequest(
		http.MethodDelete,
		resumedServer.URL+"/api/workspaces",
		strings.NewReader(`{"workspace_root":"`+canonicalWorkspace+`"}`),
	)
	if err != nil {
		t.Fatalf("create remove workspace request: %v", err)
	}
	removeRequest.Header.Set("Content-Type", "application/json")
	removeResponse, err := http.DefaultClient.Do(removeRequest)
	if err != nil {
		t.Fatalf("remove workspace request: %v", err)
	}
	removeResponse.Body.Close()
	if removeResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("remove workspace status = %d, want %d", removeResponse.StatusCode, http.StatusNoContent)
	}
	if _, err := os.Stat(canonicalWorkspace); !os.IsNotExist(err) {
		t.Fatalf("workspace still exists after removal: %v", err)
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

func TestRemoveWorkspaceDeletesTmpAndHidesDefault(t *testing.T) {
	dataRoot := t.TempDir()
	t.Chdir(dataRoot)
	extra := filepath.Join(dataRoot, "extra-project")
	if err := os.Mkdir(extra, 0o755); err != nil {
		t.Fatalf("Mkdir extra: %v", err)
	}
	if err := os.WriteFile(filepath.Join(extra, "keep-me.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Mkdir(filepath.Join(extra, "tmp"), 0o755); err != nil {
		t.Fatalf("Mkdir extra tmp: %v", err)
	}
	if err := os.WriteFile(filepath.Join(extra, "tmp", "exec-policy.rules"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile extra tmp: %v", err)
	}

	handler := appserver.NewHandler(model.NewFakeClient())
	defer handler.Close(context.Background())
	server := httptest.NewServer(handler)
	defer server.Close()

	createBody, err := json.Marshal(map[string]string{"workspace_root": extra})
	if err != nil {
		t.Fatalf("marshal create: %v", err)
	}
	createResponse, err := http.Post(server.URL+"/api/threads", "application/json", strings.NewReader(string(createBody)))
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if createResponse.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(createResponse.Body)
		createResponse.Body.Close()
		t.Fatalf("create status = %d, body = %s", createResponse.StatusCode, body)
	}
	createResponse.Body.Close()

	listResponse, err := http.Get(server.URL + "/api/threads")
	if err != nil {
		t.Fatalf("list threads: %v", err)
	}
	var listed struct {
		Threads []struct {
			ID            string `json:"id"`
			WorkspaceRoot string `json:"workspace_root"`
		} `json:"threads"`
	}
	if err := json.NewDecoder(listResponse.Body).Decode(&listed); err != nil {
		listResponse.Body.Close()
		t.Fatalf("decode list: %v", err)
	}
	listResponse.Body.Close()

	var defaultRoot, extraRoot, extraID string
	for _, thread := range listed.Threads {
		switch thread.ID {
		case "current-workspace":
			defaultRoot = thread.WorkspaceRoot
		default:
			extraID = thread.ID
			extraRoot = thread.WorkspaceRoot
		}
	}
	if defaultRoot == "" || extraRoot == "" || extraID == "" {
		t.Fatalf("listed threads = %+v", listed.Threads)
	}

	deleteWorkspace := func(root string) {
		t.Helper()
		body, err := json.Marshal(map[string]string{"workspace_root": root})
		if err != nil {
			t.Fatalf("marshal delete: %v", err)
		}
		request, err := http.NewRequest(http.MethodDelete, server.URL+"/api/workspaces", strings.NewReader(string(body)))
		if err != nil {
			t.Fatalf("new delete request: %v", err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("delete workspace: %v", err)
		}
		payload, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("delete %s status = %d, body = %s", root, response.StatusCode, payload)
		}
	}

	deleteWorkspace(extraRoot)
	if _, err := os.Stat(filepath.Join(extra, "keep-me.txt")); err != nil {
		t.Fatalf("project files were deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(extra, "tmp")); !os.IsNotExist(err) {
		t.Fatalf("workspace tmp still on disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "tmp", "threads", extraID+".json")); !os.IsNotExist(err) {
		t.Fatalf("thread metadata still present: %v", err)
	}

	deleteWorkspace(defaultRoot)
	if _, err := os.Stat(dataRoot); err != nil {
		t.Fatalf("default workspace files were deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "tmp", "threads")); err != nil {
		t.Fatalf("process tmp/threads was deleted: %v", err)
	}

	afterResponse, err := http.Get(server.URL + "/api/threads")
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	listed.Threads = nil
	if err := json.NewDecoder(afterResponse.Body).Decode(&listed); err != nil {
		afterResponse.Body.Close()
		t.Fatalf("decode after list: %v", err)
	}
	afterResponse.Body.Close()
	for _, thread := range listed.Threads {
		if thread.ID == "current-workspace" {
			t.Fatal("hidden default workspace still listed")
		}
		if thread.WorkspaceRoot == extraRoot || thread.WorkspaceRoot == defaultRoot {
			t.Fatalf("deleted workspace still listed: %+v", thread)
		}
	}
}
