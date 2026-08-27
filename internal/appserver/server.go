// Package appserver exposes the harness through a minimal HTTP application.
package appserver

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"sync"

	"github.com/lobster-bujiaban/lob-codex/internal/model"
	"github.com/lobster-bujiaban/lob-codex/internal/session"
	"github.com/lobster-bujiaban/lob-codex/internal/tools"
)

//go:embed web/*
var webFiles embed.FS

// Handler owns one long-lived Codex session and exposes it through HTTP.
type Handler struct {
	mux       *http.ServeMux
	sessionIO *session.IO
	chatMu    sync.Mutex
}

type chatStreamEvent struct {
	Type           string   `json:"type"`
	Delta          string   `json:"delta,omitempty"`
	CallID         string   `json:"call_id,omitempty"`
	Name           string   `json:"name,omitempty"`
	Arguments      string   `json:"arguments,omitempty"`
	Output         string   `json:"output,omitempty"`
	Message        string   `json:"message,omitempty"`
	TurnID         string   `json:"turn_id,omitempty"`
	Command        []string `json:"command,omitempty"`
	CWD            string   `json:"cwd,omitempty"`
	Reason         string   `json:"reason,omitempty"`
	ProposedPrefix []string `json:"proposed_prefix_rule,omitempty"`
}

// NewHandler creates the GUI and chat API using one long-lived model session.
func NewHandler(client model.Client) *Handler {
	_, sessionIO := session.New(client)
	handler := &Handler{mux: http.NewServeMux(), sessionIO: sessionIO}
	handler.mux.HandleFunc("POST /api/chat", handler.chat)
	handler.mux.HandleFunc("POST /api/approvals/{callID}", handler.respondApproval)

	staticFiles, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err)
	}
	handler.mux.Handle("GET /", http.FileServer(http.FS(staticFiles)))
	return handler
}

func (h *Handler) respondApproval(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		TurnID   string                 `json:"turn_id"`
		Decision tools.ApprovalDecision `json:"decision"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 16<<10)).Decode(&input); err != nil {
		http.Error(writer, "invalid JSON request", http.StatusBadRequest)
		return
	}
	if input.Decision != tools.ApprovalApproved &&
		input.Decision != tools.ApprovalApprovedForSession &&
		input.Decision != tools.ApprovalApprovedWithAmendment &&
		input.Decision != tools.ApprovalDenied {
		http.Error(writer, "invalid approval decision", http.StatusBadRequest)
		return
	}
	response := session.ExecApprovalResponse{
		CallID: request.PathValue("callID"), TurnID: input.TurnID, Decision: input.Decision,
	}
	if err := h.sessionIO.RespondExecApproval(request.Context(), response); err != nil {
		http.Error(writer, err.Error(), http.StatusConflict)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	h.mux.ServeHTTP(writer, request)
}

// Close shuts down the owned session.
func (h *Handler) Close(ctx context.Context) error {
	return h.sessionIO.Shutdown(ctx)
}

func (h *Handler) chat(writer http.ResponseWriter, request *http.Request) {
	h.chatMu.Lock()
	defer h.chatMu.Unlock()

	var input struct {
		Prompt string `json:"prompt"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20))
	if err := decoder.Decode(&input); err != nil {
		http.Error(writer, "invalid JSON request", http.StatusBadRequest)
		return
	}
	input.Prompt = strings.TrimSpace(input.Prompt)
	if err := session.ValidateInput(input.Prompt); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}

	writer.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("X-Content-Type-Options", "nosniff")

	wrote, err := streamTurn(request, writer, h.sessionIO, input.Prompt)
	if err != nil {
		if !wrote {
			http.Error(writer, err.Error(), http.StatusBadGateway)
			return
		}
		_ = writeChatStreamEvent(writer, chatStreamEvent{Type: "error", Message: err.Error()})
	}
}

func streamTurn(request *http.Request, writer http.ResponseWriter, sessionIO *session.IO, prompt string) (bool, error) {
	turnID, err := sessionIO.SubmitTurnInput(request.Context(), prompt)
	if err != nil {
		return false, err
	}
	wrote := false
	for {
		event, err := sessionIO.NextEvent(request.Context())
		if err != nil {
			return wrote, err
		}
		if event.ID != turnID {
			continue
		}
		switch event.Msg.Type {
		case "agent_message_content_delta":
			if err := writeChatStreamEvent(writer, chatStreamEvent{
				Type:  "assistant_delta",
				Delta: event.Msg.AgentMessageContentDelta.Delta,
			}); err != nil {
				return wrote, err
			}
			wrote = true
		case "tool_call_started":
			toolCall := event.Msg.ToolCallStarted
			if err := writeChatStreamEvent(writer, chatStreamEvent{
				Type:      "tool_call_started",
				CallID:    toolCall.CallID,
				Name:      toolCall.Name,
				Arguments: toolCall.Arguments,
			}); err != nil {
				return wrote, err
			}
			wrote = true
		case "tool_call_completed":
			toolCall := event.Msg.ToolCallCompleted
			if err := writeChatStreamEvent(writer, chatStreamEvent{
				Type:   "tool_call_completed",
				CallID: toolCall.CallID,
				Name:   toolCall.Name,
				Output: toolCall.Output,
			}); err != nil {
				return wrote, err
			}
			wrote = true
		case "exec_approval_request":
			approval := event.Msg.ExecApprovalRequest
			if err := writeChatStreamEvent(writer, chatStreamEvent{
				Type: "exec_approval_request", CallID: approval.CallID, TurnID: approval.TurnID,
				Command: approval.Command, CWD: approval.CWD, Reason: approval.Reason,
				ProposedPrefix: approval.ProposedPrefix,
			}); err != nil {
				return wrote, err
			}
			wrote = true
		case "turn_complete":
			if event.Msg.TurnComplete.Error != nil {
				return wrote, errors.New(event.Msg.TurnComplete.Error.Message)
			}
			return wrote, nil
		case "turn_aborted":
			return wrote, fmt.Errorf("turn aborted: %s", event.Msg.TurnAborted.Reason)
		}
	}
}

func writeChatStreamEvent(writer http.ResponseWriter, event chatStreamEvent) error {
	if err := json.NewEncoder(writer).Encode(event); err != nil {
		return err
	}
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}
