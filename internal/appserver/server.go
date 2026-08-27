// Package appserver exposes the harness through a minimal HTTP application.
package appserver

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strings"

	"github.com/lobster-bujiaban/lob-codex/internal/agent"
	"github.com/lobster-bujiaban/lob-codex/internal/model"
	"github.com/lobster-bujiaban/lob-codex/internal/protocol"
)

//go:embed web/*
var webFiles embed.FS

// NewHandler creates the GUI and chat API using the provided model client.
func NewHandler(client model.Client) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/chat", chatHandler(client))

	staticFiles, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /", http.FileServer(http.FS(staticFiles)))
	return mux
}

func chatHandler(client model.Client) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		var input struct {
			Prompt string `json:"prompt"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20))
		if err := decoder.Decode(&input); err != nil {
			http.Error(writer, "invalid JSON request", http.StatusBadRequest)
			return
		}
		input.Prompt = strings.TrimSpace(input.Prompt)
		if err := agent.ValidateInput(input.Prompt); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}

		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.Header().Set("Cache-Control", "no-cache")
		writer.Header().Set("X-Content-Type-Options", "nosniff")

		sink := &httpSink{writer: writer}
		if err := agent.NewRunner(client, sink).Run(request.Context(), input.Prompt); err != nil {
			if !sink.wrote {
				http.Error(writer, err.Error(), http.StatusBadGateway)
				return
			}
			fmt.Fprintf(writer, "\n\n[error] %v", err)
		}
	}
}

type httpSink struct {
	writer http.ResponseWriter
	wrote  bool
}

func (s *httpSink) Emit(event protocol.Event) error {
	if event.Type != protocol.EventTextDelta {
		return nil
	}
	if _, err := fmt.Fprint(s.writer, event.Text); err != nil {
		return err
	}
	s.wrote = true
	if flusher, ok := s.writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}
