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
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lobster-bujiaban/lob-codex/internal/model"
	"github.com/lobster-bujiaban/lob-codex/internal/session"
	"github.com/lobster-bujiaban/lob-codex/internal/tools"
)

//go:embed web/*
var webFiles embed.FS

// Handler routes App Server requests to independent thread-owned Sessions.
type Handler struct {
	mux       *http.ServeMux
	client    model.Client
	store     threadStore
	defaultID string
	threadsMu sync.Mutex
	threads   map[string]*threadRuntime
}

type threadRuntime struct {
	metadata threadMetadata
	mu       sync.Mutex
	chatMu   sync.Mutex
	io       *session.IO
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
	Stream         string   `json:"stream,omitempty"`
	Chunk          []byte   `json:"chunk,omitempty"`
	ProcessID      string   `json:"process_id,omitempty"`
	Stdin          string   `json:"stdin,omitempty"`
	ThreadID       string   `json:"thread_id,omitempty"`
	BeforeTokens   int      `json:"before_tokens,omitempty"`
	AfterTokens    int      `json:"after_tokens,omitempty"`
}

// NewHandler creates the GUI and thread-aware chat API.
func NewHandler(client model.Client) *Handler {
	dataRoot, err := os.Getwd()
	if err != nil {
		dataRoot = "."
	}
	dataRoot, err = filepath.Abs(dataRoot)
	if err != nil {
		panic(err)
	}
	const defaultThreadID = "current-workspace"
	handler := &Handler{
		mux: http.NewServeMux(), client: client, store: newThreadStore(dataRoot),
		defaultID: defaultThreadID, threads: make(map[string]*threadRuntime),
	}
	handler.threads[defaultThreadID] = &threadRuntime{metadata: threadMetadata{
		ID: defaultThreadID, WorkspaceRoot: dataRoot, WorkingDirectory: dataRoot,
	}}
	storedThreads, err := handler.store.list()
	if err != nil {
		panic(err)
	}
	for _, metadata := range storedThreads {
		handler.threads[metadata.ID] = &threadRuntime{metadata: metadata}
	}
	handler.mux.HandleFunc("POST /api/chat", handler.chat)
	handler.mux.HandleFunc("POST /api/approvals/{callID}", handler.respondApproval)
	handler.mux.HandleFunc("GET /api/threads", handler.listThreads)
	handler.mux.HandleFunc("POST /api/threads", handler.startThread)
	handler.mux.HandleFunc("POST /api/threads/{threadID}/fork", handler.forkThread)
	handler.mux.HandleFunc("POST /api/threads/{threadID}/interrupt", handler.interruptTurn)
	handler.mux.HandleFunc("POST /api/threads/{threadID}/steer", handler.steerTurn)
	handler.mux.HandleFunc("POST /api/threads/{threadID}/extensions/refresh", handler.refreshExtensions)
	handler.mux.HandleFunc("POST /api/threads/{threadID}/background-terminals/clean", handler.cleanBackgroundTerminals)
	handler.mux.HandleFunc("GET /api/threads/{threadID}/history", handler.threadHistory)
	handler.mux.HandleFunc("GET /api/threads/{threadID}/flow", handler.threadFlow)
	handler.mux.HandleFunc("POST /api/workspaces/select", handler.selectWorkspace)
	handler.mux.HandleFunc("DELETE /api/workspaces", handler.removeWorkspace)

	staticFiles, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err)
	}
	handler.mux.Handle("GET /", http.FileServer(http.FS(staticFiles)))
	return handler
}

func (h *Handler) refreshExtensions(writer http.ResponseWriter, request *http.Request) {
	runtime, err := h.thread(request.PathValue("threadID"))
	if err != nil {
		http.Error(writer, err.Error(), http.StatusNotFound)
		return
	}
	sessionIO, err := h.sessionIO(runtime)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusConflict)
		return
	}
	if err := sessionIO.RefreshExtensions(request.Context()); err != nil {
		http.Error(writer, err.Error(), http.StatusConflict)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (h *Handler) cleanBackgroundTerminals(writer http.ResponseWriter, request *http.Request) {
	runtime, err := h.thread(request.PathValue("threadID"))
	if err != nil {
		http.Error(writer, err.Error(), http.StatusNotFound)
		return
	}
	sessionIO, err := h.sessionIO(runtime)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusConflict)
		return
	}
	if err := sessionIO.CleanBackgroundTerminals(request.Context()); err != nil {
		http.Error(writer, err.Error(), http.StatusConflict)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (h *Handler) interruptTurn(writer http.ResponseWriter, request *http.Request) {
	runtime, err := h.thread(request.PathValue("threadID"))
	if err != nil {
		http.Error(writer, err.Error(), http.StatusNotFound)
		return
	}
	sessionIO, err := h.sessionIO(runtime)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusConflict)
		return
	}
	if err := sessionIO.Interrupt(request.Context()); err != nil {
		http.Error(writer, err.Error(), http.StatusConflict)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (h *Handler) steerTurn(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20)).Decode(&input); err != nil {
		http.Error(writer, "invalid JSON request", http.StatusBadRequest)
		return
	}
	if err := session.ValidateInput(input.Prompt); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	runtime, err := h.thread(request.PathValue("threadID"))
	if err != nil {
		http.Error(writer, err.Error(), http.StatusNotFound)
		return
	}
	sessionIO, err := h.sessionIO(runtime)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusConflict)
		return
	}
	expectedTurnID := request.Header.Get("X-Turn-ID")
	if expectedTurnID == "" {
		http.Error(writer, "X-Turn-ID is required", http.StatusBadRequest)
		return
	}
	turnID, err := sessionIO.Steer(request.Context(), expectedTurnID, strings.TrimSpace(input.Prompt))
	if err != nil {
		http.Error(writer, err.Error(), http.StatusConflict)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(writer).Encode(map[string]string{"turn_id": turnID})
}

func (h *Handler) respondApproval(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		TurnID   string                 `json:"turn_id"`
		ThreadID string                 `json:"thread_id"`
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
	runtime, err := h.thread(input.ThreadID)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusNotFound)
		return
	}
	sessionIO, err := h.sessionIO(runtime)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusConflict)
		return
	}
	if err := sessionIO.RespondExecApproval(request.Context(), response); err != nil {
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
	h.threadsMu.Lock()
	runtimes := make([]*threadRuntime, 0, len(h.threads))
	for _, runtime := range h.threads {
		runtimes = append(runtimes, runtime)
	}
	h.threadsMu.Unlock()
	for _, runtime := range runtimes {
		runtime.mu.Lock()
		sessionIO := runtime.io
		runtime.mu.Unlock()
		if sessionIO != nil {
			if err := sessionIO.Shutdown(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *Handler) chat(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Prompt    string   `json:"prompt"`
		ThreadID  string   `json:"thread_id"`
		ImageURLs []string `json:"image_urls"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 20<<20))
	if err := decoder.Decode(&input); err != nil {
		http.Error(writer, "invalid JSON request", http.StatusBadRequest)
		return
	}
	input.Prompt = strings.TrimSpace(input.Prompt)
	if err := validateImageURLs(input.ImageURLs); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	if err := session.ValidateTurnInput(session.TurnInput{Text: input.Prompt, ImageURLs: input.ImageURLs}); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	runtime, err := h.thread(input.ThreadID)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusNotFound)
		return
	}
	sessionIO, err := h.sessionIO(runtime)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusConflict)
		return
	}
	runtime.chatMu.Lock()
	defer runtime.chatMu.Unlock()

	writer.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("X-Content-Type-Options", "nosniff")

	wrote, err := streamTurn(request, writer, sessionIO, input.Prompt, input.ImageURLs)
	if err != nil {
		if !wrote {
			http.Error(writer, err.Error(), http.StatusBadGateway)
			return
		}
		_ = writeChatStreamEvent(writer, chatStreamEvent{Type: "error", Message: err.Error()})
	}
}

func (h *Handler) listThreads(writer http.ResponseWriter, _ *http.Request) {
	stored, err := h.store.list()
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	h.threadsMu.Lock()
	for _, metadata := range stored {
		if _, exists := h.threads[metadata.ID]; !exists {
			h.threads[metadata.ID] = &threadRuntime{metadata: metadata}
		}
	}
	current := h.threads[h.defaultID].metadata
	h.threadsMu.Unlock()
	current.Title = h.threadTitle(current.ID)
	for index := range stored {
		stored[index].Title = h.threadTitle(stored[index].ID)
	}
	threads := append([]threadMetadata{current}, stored...)
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{"threads": threads})
}

func (h *Handler) threadTitle(threadID string) string {
	items, _, err := session.ReadRollout(h.store.rolloutPath(threadID))
	if err != nil {
		return ""
	}
	for _, item := range items {
		if item.Type != "message" || item.Role != "user" {
			continue
		}
		title := strings.TrimSpace(item.Text())
		if title == "" {
			return "图片对话"
		}
		runes := []rune(title)
		if len(runes) > 24 {
			title = string(runes[:24]) + "…"
		}
		return title
	}
	return ""
}

func (h *Handler) startThread(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		WorkspaceRoot string `json:"workspace_root"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 16<<10)).Decode(&input); err != nil {
		http.Error(writer, "invalid JSON request", http.StatusBadRequest)
		return
	}
	metadata, err := h.store.create(strings.TrimSpace(input.WorkspaceRoot))
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	h.threadsMu.Lock()
	h.threads[metadata.ID] = &threadRuntime{metadata: metadata}
	h.threadsMu.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(writer).Encode(metadata)
}

func (h *Handler) threadHistory(writer http.ResponseWriter, request *http.Request) {
	threadID := request.PathValue("threadID")
	runtime, err := h.thread(threadID)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusNotFound)
		return
	}
	runtime.chatMu.Lock()
	defer runtime.chatMu.Unlock()
	items, _, err := session.ReadRollout(h.store.rolloutPath(threadID))
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{"items": items})
}

func (h *Handler) threadFlow(writer http.ResponseWriter, request *http.Request) {
	threadID := request.PathValue("threadID")
	runtime, err := h.thread(threadID)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusNotFound)
		return
	}
	runtime.chatMu.Lock()
	defer runtime.chatMu.Unlock()
	events, summary, err := session.ReadRolloutFlow(h.store.rolloutPath(threadID))
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"events": events, "summary": summary,
		"thread_id": threadID, "workspace_root": runtime.metadata.WorkspaceRoot,
		"storage_path": h.store.rolloutPath(threadID),
	})
}

func (h *Handler) forkThread(writer http.ResponseWriter, request *http.Request) {
	source, err := h.thread(request.PathValue("threadID"))
	if err != nil {
		http.Error(writer, err.Error(), http.StatusNotFound)
		return
	}
	var input struct {
		ItemCount *int `json:"item_count"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 16<<10)).Decode(&input); err != nil {
		http.Error(writer, "invalid JSON request", http.StatusBadRequest)
		return
	}
	source.chatMu.Lock()
	defer source.chatMu.Unlock()
	items, _, err := session.ReadRollout(h.store.rolloutPath(source.metadata.ID))
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	count := len(items)
	if input.ItemCount != nil {
		count = *input.ItemCount
	}
	if count < 0 || count > len(items) {
		http.Error(writer, "item_count is outside thread history", http.StatusBadRequest)
		return
	}
	metadata, err := h.store.create(source.metadata.WorkspaceRoot)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := session.WriteRollout(h.store.rolloutPath(metadata.ID), metadata.WorkspaceRoot, items[:count]); err != nil {
		h.store.remove(metadata.ID)
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	h.threadsMu.Lock()
	h.threads[metadata.ID] = &threadRuntime{metadata: metadata}
	h.threadsMu.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(writer).Encode(metadata)
}

func (h *Handler) selectWorkspace(writer http.ResponseWriter, request *http.Request) {
	workspaceRoot, cancelled, err := chooseWorkspace(request.Context())
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	if cancelled {
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	workspaceRoot, err = validateWorkspace(workspaceRoot)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]string{"workspace_root": workspaceRoot})
}

func (h *Handler) removeWorkspace(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		WorkspaceRoot string `json:"workspace_root"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 16<<10)).Decode(&input); err != nil {
		http.Error(writer, "invalid JSON request", http.StatusBadRequest)
		return
	}
	workspaceRoot := strings.TrimSpace(input.WorkspaceRoot)
	if workspaceRoot == "" {
		http.Error(writer, "workspace_root is required", http.StatusBadRequest)
		return
	}
	h.threadsMu.Lock()
	var removed []*threadRuntime
	defaultRoot := h.threads[h.defaultID].metadata.WorkspaceRoot
	for threadID, runtime := range h.threads {
		if threadID != h.defaultID && runtime.metadata.WorkspaceRoot == workspaceRoot {
			removed = append(removed, runtime)
			delete(h.threads, threadID)
		}
	}
	h.threadsMu.Unlock()
	for _, runtime := range removed {
		runtime.mu.Lock()
		sessionIO := runtime.io
		runtime.io = nil
		runtime.mu.Unlock()
		if sessionIO != nil {
			if err := sessionIO.Shutdown(request.Context()); err != nil {
				http.Error(writer, err.Error(), http.StatusConflict)
				return
			}
		}
	}
	if len(removed) == 0 {
		http.Error(writer, "workspace is not removable", http.StatusNotFound)
		return
	}
	cleanRoot := filepath.Clean(workspaceRoot)
	home, _ := os.UserHomeDir()
	if cleanRoot == string(filepath.Separator) || cleanRoot == filepath.Clean(home) || cleanRoot == filepath.Clean(defaultRoot) {
		http.Error(writer, "protected workspace cannot be deleted", http.StatusBadRequest)
		return
	}
	if err := os.RemoveAll(cleanRoot); err != nil {
		h.threadsMu.Lock()
		for _, runtime := range removed {
			h.threads[runtime.metadata.ID] = runtime
		}
		h.threadsMu.Unlock()
		http.Error(writer, fmt.Sprintf("delete workspace files: %v", err), http.StatusInternalServerError)
		return
	}
	for _, runtime := range removed {
		h.store.remove(runtime.metadata.ID)
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (h *Handler) thread(threadID string) (*threadRuntime, error) {
	if threadID == "" {
		threadID = h.defaultID
	}
	h.threadsMu.Lock()
	runtime := h.threads[threadID]
	h.threadsMu.Unlock()
	if runtime == nil {
		return nil, fmt.Errorf("unknown thread_id %q", threadID)
	}
	return runtime, nil
}

func (h *Handler) sessionIO(runtime *threadRuntime) (*session.IO, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.io != nil {
		return runtime.io, nil
	}
	_, sessionIO, err := session.NewInWorkspaceWithRollout(
		h.client, runtime.metadata.WorkspaceRoot, h.store.rolloutPath(runtime.metadata.ID),
	)
	if err != nil {
		return nil, fmt.Errorf("start thread %s: %w", runtime.metadata.ID, err)
	}
	runtime.io = sessionIO
	return sessionIO, nil
}

func streamTurn(request *http.Request, writer http.ResponseWriter, sessionIO *session.IO, prompt string, imageURLs []string) (bool, error) {
	turnID, err := sessionIO.SubmitTurnInputWithImages(request.Context(), prompt, imageURLs)
	if err != nil {
		return false, err
	}
	writer.Header().Set("X-Turn-ID", turnID)
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
		case "exec_command_output_delta":
			delta := event.Msg.ExecCommandOutputDelta
			if err := writeChatStreamEvent(writer, chatStreamEvent{
				Type: "exec_command_output_delta", CallID: delta.CallID,
				Stream: delta.Stream, Chunk: delta.Chunk,
			}); err != nil {
				return wrote, err
			}
			wrote = true
		case "terminal_interaction":
			interaction := event.Msg.TerminalInteraction
			if err := writeChatStreamEvent(writer, chatStreamEvent{
				Type: "terminal_interaction", CallID: interaction.CallID,
				ProcessID: interaction.ProcessID, Stdin: interaction.Stdin,
			}); err != nil {
				return wrote, err
			}
			wrote = true
		case "context_compaction":
			compaction := event.Msg.ContextCompaction
			if err := writeChatStreamEvent(writer, chatStreamEvent{
				Type: "context_compaction", BeforeTokens: compaction.BeforeTokens, AfterTokens: compaction.AfterTokens,
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

func validateImageURLs(imageURLs []string) error {
	if len(imageURLs) > 4 {
		return errors.New("at most 4 images are allowed")
	}
	totalSize := 0
	for _, imageURL := range imageURLs {
		if !strings.HasPrefix(imageURL, "data:image/") || !strings.Contains(imageURL[:min(len(imageURL), 128)], ";base64,") {
			return errors.New("images must be base64 data URLs")
		}
		totalSize += len(imageURL)
	}
	if totalSize > 16<<20 {
		return errors.New("images exceed the 16 MiB request limit")
	}
	return nil
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
