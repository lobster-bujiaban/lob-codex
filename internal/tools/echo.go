package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// EchoExecutor is a side-effect-free tool used to expose the full Agent Tool Loop.
type EchoExecutor struct{}

// Definition advertises echo's strict JSON arguments to the model.
func (EchoExecutor) Definition() Definition {
	return Definition{
		Type:        "function",
		Name:        "echo",
		Description: "Return the supplied text unchanged.",
		Parameters: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"text": map[string]any{"type": "string"}},
			"required":             []string{"text"},
			"additionalProperties": false,
		},
		Strict: true,
	}
}

// Execute validates the JSON payload and returns its text field.
func (EchoExecutor) Execute(_ context.Context, invocation Invocation) (string, error) {
	var input struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(invocation.Call.Arguments), &input); err != nil {
		return "", errors.New("arguments must be valid JSON with a text field")
	}
	if strings.TrimSpace(input.Text) == "" {
		return "", errors.New("text must not be empty")
	}
	return input.Text, nil
}
