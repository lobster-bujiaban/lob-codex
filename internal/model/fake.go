package model

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lobster-bujiaban/lob-codex/internal/protocol"
)

// FakeClient is a deterministic model implementation for local learning and tests.
type FakeClient struct{}

// NewFakeClient creates a deterministic model client that requires no network access.
func NewFakeClient() *FakeClient {
	return &FakeClient{}
}

// Stream emits a small response while preserving the same lifecycle as a real model.
func (c *FakeClient) Stream(ctx context.Context, request Request) Stream {
	events := make(chan ResponseEvent)
	errors := make(chan error, 1)

	go func() {
		defer close(events)
		defer close(errors)
		calls := make(map[string]struct{})
		outputs := make(map[string]struct{})
		for _, item := range request.Input {
			if item.Type == "function_call" {
				calls[item.CallID] = struct{}{}
			} else if item.Type == "function_call_output" {
				outputs[item.CallID] = struct{}{}
			}
		}
		for callID := range calls {
			if _, exists := outputs[callID]; !exists {
				errors <- fmt.Errorf("no tool output found for function call %s", callID)
				return
			}
		}

		if len(request.Input) > 0 {
			last := request.Input[len(request.Input)-1]
			if last.Type == "function_call_output" {
				toolName := "tool"
				if len(request.Input) > 1 {
					toolName = request.Input[len(request.Input)-2].Name
				}
				var processResult struct {
					SessionID int `json:"session_id"`
				}
				if json.Unmarshal([]byte(last.Output), &processResult) == nil && processResult.SessionID > 0 {
					chars := `hello\n`
					if request.Input[len(request.Input)-2].ID == "fc_fake_pty_interrupt" {
						chars = `\u0003`
					}
					item := protocol.ResponseItem{
						Type: "function_call", ID: "fc_fake_write_stdin", CallID: "call_fake_write_stdin",
						Name: "write_stdin", Arguments: `{"session_id":` + fmt.Sprint(processResult.SessionID) + `,"chars":"` + chars + `","yield_time_ms":5000}`,
					}
					if !sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseOutputItemDone, Item: &item}) {
						return
					}
					sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseCompleted})
					return
				}
				emitFakeText(ctx, events, "Fake model received "+toolName+" result: "+last.Output)
				return
			}
		}
		input := ""
		for index := len(request.Input) - 1; index >= 0; index-- {
			if request.Input[index].Role == "user" {
				input = request.Input[index].Text()
				break
			}
		}
		if strings.Contains(input, "长进程演示") {
			item := protocol.ResponseItem{
				Type: "function_call", ID: "fc_fake_long_exec", CallID: "call_fake_long_exec",
				Name: "exec_command", Arguments: `{"cmd":"read line; echo process-done:$line","yield_time_ms":250}`,
			}
			if !sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseOutputItemDone, Item: &item}) {
				return
			}
			sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseCompleted})
			return
		}
		if strings.Contains(input, "大输出演示") {
			item := protocol.ResponseItem{
				Type: "function_call", ID: "fc_fake_large_exec", CallID: "call_fake_large_exec",
				Name: "exec_command", Arguments: `{"cmd":"seq 1 50000","max_output_tokens":1000}`,
			}
			if !sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseOutputItemDone, Item: &item}) {
				return
			}
			sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseCompleted})
			return
		}
		if strings.Contains(input, "PTY 演示") {
			item := protocol.ResponseItem{
				Type: "function_call", ID: "fc_fake_pty", CallID: "call_fake_pty",
				Name: "exec_command", Arguments: `{"cmd":"read -r line; printf 'pty-received:%s\\n' \"$line\"","tty":true,"yield_time_ms":250}`,
			}
			if !sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseOutputItemDone, Item: &item}) {
				return
			}
			sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseCompleted})
			return
		}
		if strings.Contains(input, "PTY 中断演示") {
			item := protocol.ResponseItem{
				Type: "function_call", ID: "fc_fake_pty_interrupt", CallID: "call_fake_pty_interrupt",
				Name: "exec_command", Arguments: `{"cmd":"sleep 30","tty":true,"yield_time_ms":250}`,
			}
			if !sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseOutputItemDone, Item: &item}) {
				return
			}
			sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseCompleted})
			return
		}
		if strings.Contains(input, "审批演示") {
			item := protocol.ResponseItem{
				Type: "function_call", ID: "fc_fake_approval", CallID: "call_fake_approval",
				Name: "exec_command", Arguments: `{"cmd":"pwd; echo approval-demo"}`,
			}
			if !sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseOutputItemDone, Item: &item}) {
				return
			}
			sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseCompleted})
			return
		}
		if strings.Contains(input, "会话规则演示") {
			item := protocol.ResponseItem{
				Type: "function_call", ID: "fc_fake_session_rule", CallID: "call_fake_session_rule",
				Name: "exec_command", Arguments: `{"cmd":"go version"}`,
			}
			if !sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseOutputItemDone, Item: &item}) {
				return
			}
			sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseCompleted})
			return
		}
		if strings.Contains(input, "工作区") || strings.Contains(input, "列出文件") {
			item := protocol.ResponseItem{
				Type:      "function_call",
				ID:        "fc_fake_exec",
				CallID:    "call_fake_exec",
				Name:      "exec_command",
				Arguments: `{"cmd":"pwd && rg --files | sed -n '1,240p'","max_output_tokens":1000}`,
			}
			if !sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseOutputItemDone, Item: &item}) {
				return
			}
			sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseCompleted})
			return
		}
		if strings.Contains(strings.ToLower(input), "echo") || strings.Contains(input, "回显") {
			item := protocol.ResponseItem{
				Type:      "function_call",
				ID:        "fc_fake_echo",
				CallID:    "call_fake_echo",
				Name:      "echo",
				Arguments: `{"text":"LOB Codex Tool Loop"}`,
			}
			if !sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseOutputItemDone, Item: &item}) {
				return
			}
			sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseCompleted})
			return
		}
		emitFakeText(ctx, events, "Fake model: "+input)
	}()

	return Stream{Events: events, Errors: errors}
}

func emitFakeText(ctx context.Context, events chan<- ResponseEvent, response string) {
	chunks := strings.Fields(response)

	for index, chunk := range chunks {
		if index < len(chunks)-1 {
			chunk += " "
		}
		if !sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseOutputTextDelta, Delta: chunk}) {
			return
		}
	}
	item := protocol.NewAssistantMessage(response)
	if !sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseOutputItemDone, Item: &item}) {
		return
	}
	sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseCompleted})
}

func sendResponseEvent(ctx context.Context, events chan<- ResponseEvent, event ResponseEvent) bool {
	select {
	case <-ctx.Done():
		return false
	case events <- event:
		return true
	}
}
