package tools

import (
	"context"
	"encoding/json"
	"errors"
)

// WriteStdinExecutor writes to or polls one process in the Session store.
type WriteStdinExecutor struct {
	Manager *ProcessManager
}

// Definition mirrors Codex's write_stdin tool arguments.
func (WriteStdinExecutor) Definition() Definition {
	return Definition{
		Type: "function", Name: "write_stdin",
		Description: "Writes characters to an existing exec session or polls for new output.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id":        map[string]any{"type": "number"},
				"chars":             map[string]any{"type": "string"},
				"yield_time_ms":     map[string]any{"type": "number"},
				"max_output_tokens": map[string]any{"type": "number"},
			},
			"required": []string{"session_id"}, "additionalProperties": false,
		},
		Strict: false,
	}
}

// Execute serializes interactions per process and returns only new output.
func (executor WriteStdinExecutor) Execute(ctx context.Context, invocation Invocation) (string, error) {
	var arguments struct {
		SessionID       int    `json:"session_id"`
		Chars           string `json:"chars"`
		YieldTimeMS     int64  `json:"yield_time_ms"`
		MaxOutputTokens int    `json:"max_output_tokens"`
	}
	if err := json.Unmarshal([]byte(invocation.Call.Arguments), &arguments); err != nil {
		return "", errors.New("arguments must be valid write_stdin JSON")
	}
	if arguments.SessionID <= 0 {
		return "", errors.New("session_id must be positive")
	}
	outputLimit := defaultOutputBytes
	if arguments.MaxOutputTokens > 0 {
		outputLimit = min(arguments.MaxOutputTokens*4, maxOutputBytes)
	}
	yield := clampYield(arguments.YieldTimeMS, arguments.Chars == "")
	return executor.Manager.writeStdin(
		ctx, arguments.SessionID, arguments.Chars, yield, outputLimit, invocation.Emit,
	)
}
