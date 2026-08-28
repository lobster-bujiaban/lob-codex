package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/lobster-bujiaban/lob-codex/internal/extensions"
	"github.com/lobster-bujiaban/lob-codex/internal/tools"
)

type Client struct {
	name          string
	workspace     string
	cmd           *exec.Cmd
	in            io.WriteCloser
	writeMu       sync.Mutex
	stateMu       sync.Mutex
	nextID        int
	pending       map[int]chan rpcResponse
	notifications chan Notification
	requests      chan Request
	done          chan struct{}
	closeOnce     sync.Once
	url           string
	headers       map[string]string
	httpClient    *http.Client
	sessionID     string
	authorization string
	streamCancel  context.CancelFunc
}

type rpcResponse struct {
	Result json.RawMessage
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
}
type Notification struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}
type Request struct {
	ID     int
	Method string
	Params json.RawMessage
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
	ReadOnly    bool           `json:"read_only"`
}

func Start(ctx context.Context, config extensions.MCPServer, cwd string) (*Client, error) {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 200 * time.Millisecond):
			}
		}
		client, err := startOnce(ctx, config, cwd)
		if err == nil {
			return client, nil
		}
		if errors.Is(err, ErrOAuthRequired) {
			return nil, err
		}
		last = err
		if !retryable(err) && attempt == 0 && !strings.Contains(strings.ToLower(err.Error()), "start") {
			return nil, err
		}
	}
	return nil, last
}

func startOnce(ctx context.Context, config extensions.MCPServer, cwd string) (*Client, error) {
	if config.URL != "" {
		streamCtx, cancel := context.WithCancel(ctx)
		client := &Client{
			name: config.Name, workspace: cwd, url: config.URL, headers: config.Headers,
			httpClient: http.DefaultClient, pending: map[int]chan rpcResponse{},
			notifications: make(chan Notification, 32), requests: make(chan Request, 16),
			done: make(chan struct{}), streamCancel: cancel,
		}
		client.loadAuthorization()
		startupContext, startupCancel := context.WithTimeout(ctx, config.StartupTimeout)
		defer startupCancel()
		var initialized any
		if err := client.call(startupContext, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"elicitation": map[string]any{}}, "clientInfo": map[string]any{"name": "lob-codex", "version": "0.1"}}, &initialized); err != nil {
			cancel()
			return nil, err
		}
		_ = client.notify("notifications/initialized", map[string]any{})
		client.startHTTPStream(streamCtx)
		return client, nil
	}
	cmd := exec.CommandContext(ctx, config.Command, config.Args...)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	for key, value := range config.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	client := &Client{name: config.Name, workspace: cwd, cmd: cmd, in: in, pending: map[int]chan rpcResponse{}, notifications: make(chan Notification, 32), requests: make(chan Request, 16), done: make(chan struct{})}
	go client.readLoop(bufio.NewReader(out))
	startupContext, cancel := context.WithTimeout(ctx, config.StartupTimeout)
	defer cancel()
	var initialized any
	if err := client.call(startupContext, "initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{"elicitation": map[string]any{}},
		"clientInfo": map[string]any{"name": "lob-codex", "version": "0.1"},
	}, &initialized); err != nil {
		client.Close()
		return nil, err
	}
	_ = client.notify("notifications/initialized", map[string]any{})
	return client, nil
}

func (client *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var result struct {
		Tools []struct {
			Name, Description string
			InputSchema       map[string]any `json:"inputSchema"`
			Annotations       struct {
				ReadOnly bool `json:"readOnlyHint"`
			} `json:"annotations"`
		} `json:"tools"`
	}
	if err := client.call(ctx, "tools/list", map[string]any{}, &result); err != nil {
		return nil, err
	}
	toolsList := make([]Tool, 0, len(result.Tools))
	for _, tool := range result.Tools {
		toolsList = append(toolsList, Tool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema, ReadOnly: tool.Annotations.ReadOnly})
	}
	return toolsList, nil
}

func (client *Client) CallTool(ctx context.Context, name string, arguments any) (string, error) {
	var result struct {
		Content []struct{ Type, Text string } `json:"content"`
		IsError bool                          `json:"isError"`
	}
	if err := client.call(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments}, &result); err != nil {
		return "", err
	}
	encoded, _ := json.Marshal(result)
	if result.IsError {
		return string(encoded), fmt.Errorf("MCP tool %s failed", name)
	}
	return string(encoded), nil
}

func (client *Client) call(ctx context.Context, method string, params any, target any) error {
	client.stateMu.Lock()
	client.nextID++
	id := client.nextID
	reply := make(chan rpcResponse, 1)
	client.pending[id] = reply
	client.stateMu.Unlock()
	defer func() { client.stateMu.Lock(); delete(client.pending, id); client.stateMu.Unlock() }()
	request, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if client.url != "" {
		return client.callHTTP(ctx, request, target)
	}
	client.writeMu.Lock()
	if _, err := client.in.Write(append(request, '\n')); err != nil {
		client.writeMu.Unlock()
		return err
	}
	client.writeMu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-client.done:
		return fmt.Errorf("MCP server %s stopped", client.name)
	case response := <-reply:
		if response.Error != nil {
			return fmt.Errorf("MCP %s: %s", method, response.Error.Message)
		}
		if len(response.Result) == 0 {
			return nil
		}
		return json.Unmarshal(response.Result, target)
	}
}

func (client *Client) readLoop(reader *bufio.Reader) {
	defer close(client.done)
	defer close(client.notifications)
	defer close(client.requests)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		client.dispatchJSON(line)
	}
}

func (client *Client) Notifications() <-chan Notification { return client.notifications }
func (client *Client) Requests() <-chan Request           { return client.requests }
func (client *Client) Respond(id int, result any, responseError error) error {
	message := map[string]any{"jsonrpc": "2.0", "id": id}
	if responseError != nil {
		message["error"] = map[string]any{"code": -32000, "message": responseError.Error()}
	} else {
		message["result"] = result
	}
	data, _ := json.Marshal(message)
	if client.url != "" {
		return client.callHTTP(context.Background(), data, nil)
	}
	client.writeMu.Lock()
	defer client.writeMu.Unlock()
	_, err := client.in.Write(append(data, '\n'))
	return err
}

func (client *Client) notify(method string, params any) error {
	data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	if client.url != "" {
		return client.callHTTP(context.Background(), data, nil)
	}
	client.writeMu.Lock()
	defer client.writeMu.Unlock()
	_, err := client.in.Write(append(data, '\n'))
	return err
}
func (client *Client) Close() {
	client.closeOnce.Do(func() {
		if client.streamCancel != nil {
			client.streamCancel()
		}
		if client.in != nil {
			_ = client.in.Close()
		}
		if client.cmd != nil && client.cmd.Process != nil {
			_ = client.cmd.Process.Kill()
			_ = client.cmd.Wait()
		}
		if client.url != "" {
			close(client.notifications)
			close(client.requests)
			close(client.done)
		}
	})
}

type Executor struct {
	Client *Client
	Server string
	Tool   Tool
}

func (executor Executor) Definition() tools.Definition {
	return tools.Definition{Type: "function", Name: "mcp__" + executor.Server + "__" + executor.Tool.Name, Description: executor.Tool.Description, Parameters: executor.Tool.InputSchema, Strict: false}
}
func (executor Executor) Execute(ctx context.Context, invocation tools.Invocation) (string, error) {
	if !executor.Tool.ReadOnly {
		if invocation.Reviewer == nil {
			return "", fmt.Errorf("MCP tool %s requires approval", executor.Tool.Name)
		}
		decision, err := invocation.Reviewer(ctx, tools.ApprovalRequest{
			CallID: invocation.Call.CallID, Command: executor.Definition().Name + " " + invocation.Call.Arguments,
			WorkingDirectory: invocation.Environment.WorkingDirectory, Reason: "MCP tool may modify external state",
		})
		if err != nil {
			return "", err
		}
		if decision != tools.ApprovalApproved && decision != tools.ApprovalApprovedForSession && decision != tools.ApprovalApprovedWithAmendment {
			return "MCP tool call denied by user", nil
		}
	}
	var arguments any = map[string]any{}
	if invocation.Call.Arguments != "" {
		if err := json.Unmarshal([]byte(invocation.Call.Arguments), &arguments); err != nil {
			return "", err
		}
	}
	return executor.Client.CallTool(ctx, executor.Tool.Name, arguments)
}

type SchemaField struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

func SchemaFields(schema map[string]any) []SchemaField {
	properties, _ := schema["properties"].(map[string]any)
	var fields []SchemaField
	for name, raw := range properties {
		property, _ := raw.(map[string]any)
		field := SchemaField{Name: name, Type: "string"}
		if typed, _ := property["type"].(string); typed != "" {
			field.Type = typed
		}
		if title, _ := property["title"].(string); title != "" {
			field.Title = title
		}
		if description, _ := property["description"].(string); description != "" {
			field.Description = description
		}
		fields = append(fields, field)
	}
	return fields
}
