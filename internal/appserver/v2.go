package appserver

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/lobster-bujiaban/lob-codex/internal/session"
)

type v2Thread struct {
	ID          string               `json:"id"`
	Name        string               `json:"name,omitempty"`
	CWD         string               `json:"cwd"`
	Path        string               `json:"path"`
	Status      map[string]string    `json:"status"`
	Turns       []session.StoredTurn `json:"turns"`
	CreatedAtMS int64                `json:"createdAtMs,omitempty"`
}

func (h *Handler) v2ThreadObject(runtime *threadRuntime, includeTurns bool) (v2Thread, error) {
	runtime.mu.Lock()
	loaded := runtime.io != nil
	activeTurnID := ""
	if runtime.session != nil {
		activeTurnID = runtime.session.ActiveTurnID()
	}
	runtime.mu.Unlock()
	thread := v2Thread{ID: runtime.metadata.ID, Name: h.threadTitle(runtime.metadata.ID), CWD: runtime.metadata.WorkspaceRoot, Path: h.store.rolloutPath(runtime.metadata.ID), Status: map[string]string{"type": "notLoaded"}, Turns: []session.StoredTurn{}, CreatedAtMS: runtime.metadata.CreatedAtMS}
	if loaded {
		thread.Status = map[string]string{"type": "idle"}
	}
	if activeTurnID != "" {
		thread.Status = map[string]string{"type": "active"}
	}
	if includeTurns {
		turns, err := session.ReadRolloutTurns(thread.Path)
		if err != nil {
			return thread, err
		}
		thread.Turns = turns
		if len(turns) > 0 && turns[len(turns)-1].Status == "inProgress" {
			thread.Status = map[string]string{"type": "active"}
		}
	}
	return thread, nil
}

func (h *Handler) v2ListThreads(writer http.ResponseWriter, _ *http.Request) {
	h.threadsMu.Lock()
	runtimes := make([]*threadRuntime, 0, len(h.threads))
	for _, runtime := range h.threads {
		runtimes = append(runtimes, runtime)
	}
	h.threadsMu.Unlock()
	data := make([]v2Thread, 0, len(runtimes))
	for _, runtime := range runtimes {
		thread, err := h.v2ThreadObject(runtime, false)
		if err == nil {
			data = append(data, thread)
		}
	}
	writeV2JSON(writer, http.StatusOK, map[string]any{"data": data, "nextCursor": nil})
}

func (h *Handler) v2StartThread(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		CWD string `json:"cwd"`
	}
	if json.NewDecoder(http.MaxBytesReader(writer, request.Body, 16<<10)).Decode(&input) != nil {
		http.Error(writer, "invalid JSON request", 400)
		return
	}
	metadata, err := h.store.create(strings.TrimSpace(input.CWD))
	if err != nil {
		http.Error(writer, err.Error(), 400)
		return
	}
	runtime := &threadRuntime{metadata: metadata}
	h.threadsMu.Lock()
	h.threads[metadata.ID] = runtime
	h.threadsMu.Unlock()
	thread, _ := h.v2ThreadObject(runtime, false)
	writeV2JSON(writer, http.StatusCreated, map[string]any{"thread": thread})
}

func (h *Handler) v2ReadThread(writer http.ResponseWriter, request *http.Request) {
	runtime, err := h.thread(request.PathValue("threadID"))
	if err != nil {
		http.Error(writer, err.Error(), 404)
		return
	}
	include := request.URL.Query().Get("includeTurns") == "true"
	thread, err := h.v2ThreadObject(runtime, include)
	if err != nil {
		http.Error(writer, err.Error(), 500)
		return
	}
	writeV2JSON(writer, 200, map[string]any{"thread": thread})
}

func (h *Handler) v2Events(writer http.ResponseWriter, request *http.Request) {
	runtime, err := h.thread(request.PathValue("threadID"))
	if err != nil {
		http.Error(writer, err.Error(), 404)
		return
	}
	after, _ := strconv.ParseUint(request.URL.Query().Get("after"), 10, 64)
	events, next, err := session.ReadRolloutEvents(h.store.rolloutPath(runtime.metadata.ID), after)
	if err != nil {
		http.Error(writer, err.Error(), 500)
		return
	}
	writeV2JSON(writer, 200, map[string]any{"data": events, "nextCursor": next})
}

func (h *Handler) v2StartTurn(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Input []struct{ Type, Text, URL string } `json:"input"`
	}
	if json.NewDecoder(http.MaxBytesReader(writer, request.Body, 20<<20)).Decode(&input) != nil {
		http.Error(writer, "invalid JSON request", 400)
		return
	}
	var text string
	var images []string
	for _, item := range input.Input {
		switch item.Type {
		case "text":
			text += item.Text
		case "image":
			images = append(images, item.URL)
		}
	}
	if err := session.ValidateTurnInput(session.TurnInput{Text: text, ImageURLs: images}); err != nil {
		http.Error(writer, err.Error(), 400)
		return
	}
	runtime, err := h.thread(request.PathValue("threadID"))
	if err != nil {
		http.Error(writer, err.Error(), 404)
		return
	}
	io, err := h.sessionIO(runtime)
	if err != nil {
		http.Error(writer, err.Error(), 409)
		return
	}
	runtime.chatMu.Lock()
	defer runtime.chatMu.Unlock()
	writer.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-cache")
	turnID, err := io.SubmitTurnInputWithImages(request.Context(), text, images)
	if err != nil {
		http.Error(writer, err.Error(), 409)
		return
	}
	writeV2Notification(writer, "turn/started", map[string]any{"threadId": runtime.metadata.ID, "turn": map[string]any{"id": turnID, "status": "inProgress", "items": []any{}}})
	writeV2Notification(writer, "item/started", map[string]any{"threadId": runtime.metadata.ID, "turnId": turnID, "item": map[string]any{"type": "userMessage", "id": "user-" + turnID, "content": input.Input}})
	writeV2Notification(writer, "item/completed", map[string]any{"threadId": runtime.metadata.ID, "turnId": turnID, "item": map[string]any{"type": "userMessage", "id": "user-" + turnID, "content": input.Input}})
	agentItemID := "agent-" + turnID
	agentStarted := false
	for {
		event, err := io.NextEvent(request.Context())
		if err != nil {
			return
		}
		if event.ID != turnID {
			continue
		}
		switch event.Msg.Type {
		case "agent_message_content_delta":
			if !agentStarted {
				writeV2Notification(writer, "item/started", map[string]any{"threadId": runtime.metadata.ID, "turnId": turnID, "item": map[string]any{"type": "agentMessage", "id": agentItemID, "text": "", "status": "inProgress"}})
				agentStarted = true
			}
			writeV2Notification(writer, "item/agentMessage/delta", map[string]any{"threadId": runtime.metadata.ID, "turnId": turnID, "delta": event.Msg.AgentMessageContentDelta.Delta})
		case "tool_call_started":
			call := event.Msg.ToolCallStarted
			writeV2Notification(writer, "item/started", map[string]any{"threadId": runtime.metadata.ID, "turnId": turnID, "item": map[string]any{"type": "toolCall", "id": call.CallID, "name": call.Name, "arguments": call.Arguments, "status": "inProgress"}})
		case "tool_call_completed":
			call := event.Msg.ToolCallCompleted
			writeV2Notification(writer, "item/completed", map[string]any{"threadId": runtime.metadata.ID, "turnId": turnID, "item": map[string]any{"type": "toolCall", "id": call.CallID, "name": call.Name, "output": call.Output, "status": "completed"}})
		case "exec_command_output_delta":
			delta := event.Msg.ExecCommandOutputDelta
			writeV2Notification(writer, "item/commandExecution/outputDelta", map[string]any{"threadId": runtime.metadata.ID, "turnId": turnID, "itemId": delta.CallID, "stream": delta.Stream, "chunk": delta.Chunk})
		case "terminal_interaction":
			interaction := event.Msg.TerminalInteraction
			writeV2Notification(writer, "item/commandExecution/terminalInteraction", map[string]any{"threadId": runtime.metadata.ID, "turnId": turnID, "itemId": interaction.CallID, "processId": interaction.ProcessID, "stdin": interaction.Stdin})
		case "exec_approval_request":
			approval := event.Msg.ExecApprovalRequest
			writeV2Notification(writer, "item/commandExecution/requestApproval", map[string]any{"threadId": runtime.metadata.ID, "turnId": turnID, "itemId": approval.CallID, "command": approval.Command, "cwd": approval.CWD, "reason": approval.Reason, "availableDecisions": approval.AvailableDecisions})
		case "context_compaction":
			compaction := event.Msg.ContextCompaction
			writeV2Notification(writer, "item/contextCompaction/completed", map[string]any{"threadId": runtime.metadata.ID, "turnId": turnID, "beforeTokens": compaction.BeforeTokens, "afterTokens": compaction.AfterTokens})
		case "turn_complete":
			complete := event.Msg.TurnComplete
			if agentStarted {
				text := ""
				if complete.LastAgentMessage != nil {
					text = *complete.LastAgentMessage
				}
				writeV2Notification(writer, "item/completed", map[string]any{"threadId": runtime.metadata.ID, "turnId": turnID, "item": map[string]any{"type": "agentMessage", "id": agentItemID, "text": text, "status": "completed"}})
			}
			status := "completed"
			if complete.Error != nil {
				status = "failed"
			}
			writeV2Notification(writer, "turn/completed", map[string]any{"threadId": runtime.metadata.ID, "turn": map[string]any{"id": turnID, "status": status, "items": []any{}, "error": complete.Error, "usage": complete.Usage}})
			return
		case "turn_aborted":
			writeV2Notification(writer, "turn/completed", map[string]any{"threadId": runtime.metadata.ID, "turn": map[string]any{"id": turnID, "status": "interrupted", "items": []any{}}})
			return
		}
	}
}

func (h *Handler) v2SteerTurn(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Input []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"input"`
	}
	if json.NewDecoder(request.Body).Decode(&input) != nil {
		http.Error(writer, "invalid JSON request", 400)
		return
	}
	var text string
	for _, item := range input.Input {
		if item.Type == "text" {
			text += item.Text
		}
	}
	runtime, err := h.thread(request.PathValue("threadID"))
	if err != nil {
		http.Error(writer, err.Error(), 404)
		return
	}
	io, err := h.sessionIO(runtime)
	if err != nil {
		http.Error(writer, err.Error(), 409)
		return
	}
	turnID, err := io.Steer(request.Context(), request.PathValue("turnID"), text)
	if err != nil {
		http.Error(writer, err.Error(), 409)
		return
	}
	writeV2JSON(writer, 202, map[string]string{"turnId": turnID})
}
func (h *Handler) v2InterruptTurn(writer http.ResponseWriter, request *http.Request) {
	runtime, err := h.thread(request.PathValue("threadID"))
	if err != nil {
		http.Error(writer, err.Error(), 404)
		return
	}
	io, err := h.sessionIO(runtime)
	if err != nil {
		http.Error(writer, err.Error(), 409)
		return
	}
	if err := io.InterruptTurn(request.Context(), request.PathValue("turnID")); err != nil {
		http.Error(writer, err.Error(), 409)
		return
	}
	writeV2JSON(writer, 200, map[string]any{})
}
func writeV2JSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
func writeV2Notification(writer http.ResponseWriter, method string, params any) {
	_ = json.NewEncoder(writer).Encode(map[string]any{"method": method, "params": params})
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
}
