package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lobster-bujiaban/lob-codex/internal/extensions"
)

func TestHTTPStartListsAndCallsTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		var envelope struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(request.Body).Decode(&envelope)
		writer.Header().Set("Content-Type", "application/json")
		switch envelope.Method {
		case "initialize", "notifications/initialized":
			_ = json.NewEncoder(writer).Encode(map[string]any{"jsonrpc": "2.0", "id": envelope.ID, "result": map[string]any{}})
		case "tools/list":
			_ = json.NewEncoder(writer).Encode(map[string]any{"jsonrpc": "2.0", "id": envelope.ID, "result": map[string]any{
				"tools": []map[string]any{{"name": "echo", "description": "echo", "inputSchema": map[string]any{"type": "object"}}},
			}})
		case "tools/call":
			_ = json.NewEncoder(writer).Encode(map[string]any{"jsonrpc": "2.0", "id": envelope.ID, "result": map[string]any{
				"content": []map[string]string{{"type": "text", "text": "ok"}},
			}})
		default:
			http.Error(writer, envelope.Method, http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client, err := Start(context.Background(), extensions.MCPServer{Name: "demo", URL: server.URL, StartupTimeout: time.Second}, t.TempDir())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer client.Close()
	tools, err := client.ListTools(context.Background())
	if err != nil || len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("ListTools = %+v, %v", tools, err)
	}
	output, err := client.CallTool(context.Background(), "echo", map[string]any{"text": "hi"})
	if err != nil || !strings.Contains(output, "ok") {
		t.Fatalf("CallTool = %s, %v", output, err)
	}
}

func TestHTTPStartRetriesThenSucceeds(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		if attempts.Add(1) == 1 {
			http.Error(writer, "busy", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{}})
	}))
	defer server.Close()
	client, err := Start(context.Background(), extensions.MCPServer{Name: "demo", URL: server.URL, StartupTimeout: 2 * time.Second}, t.TempDir())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	client.Close()
}

func TestHTTPUnauthorizedRequiresOAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("WWW-Authenticate", `Bearer realm="mcp"`)
		http.Error(writer, "login", http.StatusUnauthorized)
	}))
	defer server.Close()
	_, err := Start(context.Background(), extensions.MCPServer{Name: "secure", URL: server.URL, StartupTimeout: time.Second}, t.TempDir())
	if !errors.Is(err, ErrOAuthRequired) {
		t.Fatalf("Start error = %v", err)
	}
}

func TestHTTPSSEDispatchesElicitationRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(writer, "data: {\"jsonrpc\":\"2.0\",\"id\":9,\"method\":\"elicitation/create\",\"params\":{\"message\":\"need name\",\"requestedSchema\":{\"type\":\"object\",\"properties\":{\"name\":{\"type\":\"string\"}}}}}\n\n")
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
			time.Sleep(50 * time.Millisecond)
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{}})
	}))
	defer server.Close()
	client, err := Start(context.Background(), extensions.MCPServer{Name: "sse", URL: server.URL, StartupTimeout: time.Second}, t.TempDir())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer client.Close()
	select {
	case request := <-client.Requests():
		if request.Method != "elicitation/create" || request.ID != 9 {
			t.Fatalf("request = %+v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for elicitation request")
	}
}

func TestOAuthTokenRoundTrip(t *testing.T) {
	workspace := t.TempDir()
	token := Token{AccessToken: "abc", RefreshToken: "r", TokenType: "Bearer"}
	if err := SaveToken(workspace, "demo", token); err != nil {
		t.Fatal(err)
	}
	loaded, ok := LoadToken(workspace, "demo")
	if !ok || loaded.AccessToken != "abc" {
		t.Fatalf("LoadToken = %+v %v", loaded, ok)
	}
}

func TestSchemaFields(t *testing.T) {
	fields := SchemaFields(map[string]any{"properties": map[string]any{"name": map[string]any{"type": "string", "title": "Name"}}})
	if len(fields) != 1 || fields[0].Name != "name" {
		t.Fatalf("fields = %+v", fields)
	}
}
