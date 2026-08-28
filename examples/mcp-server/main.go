package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var input request
		if json.Unmarshal(scanner.Bytes(), &input) != nil || input.ID == nil {
			continue
		}
		response := map[string]any{"jsonrpc": "2.0", "id": input.ID}
		switch input.Method {
		case "initialize":
			response["result"] = map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "lob-codex-example", "version": "0.1.0"},
			}
		case "tools/list":
			response["result"] = map[string]any{"tools": []map[string]any{{
				"name":        "example_echo",
				"description": "返回传入的 text，用于验证 MCP 调用链",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"text": map[string]any{"type": "string"}},
					"required":   []string{"text"},
				},
				"annotations": map[string]any{"readOnlyHint": true},
			}}}
		case "tools/call":
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(input.Params, &params)
			if params.Name != "example_echo" {
				response["error"] = map[string]any{"code": -32601, "message": "unknown tool"}
			} else {
				response["result"] = map[string]any{"content": []map[string]string{{"type": "text", "text": fmt.Sprint(params.Arguments["text"])}}}
			}
		default:
			response["error"] = map[string]any{"code": -32601, "message": "method not found"}
		}
		_ = encoder.Encode(response)
	}
}
