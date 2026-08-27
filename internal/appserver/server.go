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
)

//go:embed web/*
var webFiles embed.FS

// Handler owns one long-lived Codex session and exposes it through HTTP.
type Handler struct {
	mux       *http.ServeMux
	sessionIO *session.IO
	chatMu    sync.Mutex
}

// NewHandler creates the GUI and chat API using one long-lived model session.
func NewHandler(client model.Client) *Handler {
	_, sessionIO := session.New(client)
	handler := &Handler{mux: http.NewServeMux(), sessionIO: sessionIO}
	handler.mux.HandleFunc("POST /api/chat", handler.chat)

	staticFiles, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err)
	}
	handler.mux.Handle("GET /", http.FileServer(http.FS(staticFiles)))
	return handler
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

	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("X-Content-Type-Options", "nosniff")

	wrote, err := streamTurn(request, writer, h.sessionIO, input.Prompt)
	if err != nil {
		if !wrote {
			http.Error(writer, err.Error(), http.StatusBadGateway)
			return
		}
		fmt.Fprintf(writer, "\n\n[error] %v", err)
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
			if _, err := fmt.Fprint(writer, event.Msg.AgentMessageContentDelta.Delta); err != nil {
				return wrote, err
			}
			wrote = true
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
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
