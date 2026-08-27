package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/lobster-bujiaban/lob-codex/internal/extensions"
	"github.com/lobster-bujiaban/lob-codex/internal/tools"
)

type Client struct {
	name   string
	cmd    *exec.Cmd
	in     io.WriteCloser
	mu     sync.Mutex
	reader *bufio.Reader
	nextID int
}

type Tool struct {
	Name, Description string
	InputSchema       map[string]any
	ReadOnly          bool
}

func Start(ctx context.Context, config extensions.MCPServer, cwd string) (*Client, error) {
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
	client := &Client{name: config.Name, cmd: cmd, in: in, reader: bufio.NewReader(out)}
	var initialized any
	if err := client.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
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
	client.mu.Lock()
	defer client.mu.Unlock()
	client.nextID++
	id := client.nextID
	request, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if _, err := client.in.Write(append(request, '\n')); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := client.reader.ReadBytes('\n')
		if err != nil {
			return err
		}
		var response struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int
				Message string
			} `json:"error"`
		}
		if json.Unmarshal(line, &response) != nil || response.ID != id {
			continue
		}
		if response.Error != nil {
			return fmt.Errorf("MCP %s: %s", method, response.Error.Message)
		}
		return json.Unmarshal(response.Result, target)
	}
}

func (client *Client) notify(method string, params any) error {
	data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	_, err := client.in.Write(append(data, '\n'))
	return err
}
func (client *Client) Close() {
	if client.in != nil {
		_ = client.in.Close()
	}
	if client.cmd != nil && client.cmd.Process != nil {
		_ = client.cmd.Process.Kill()
		_ = client.cmd.Wait()
	}
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
