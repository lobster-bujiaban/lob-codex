package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/lobster-bujiaban/lob-codex/internal/protocol"
)

const defaultOpenAIBaseURL = "https://api.openai.com/v1"

// OpenAIConfig configures an OpenAI Responses API compatible endpoint.
type OpenAIConfig struct {
	APIKey  string
	BaseURL string
	Model   string
}

// OpenAIClient streams responses from an OpenAI Responses API compatible endpoint.
type OpenAIClient struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
}

// NewOpenAIClient validates config and creates a Responses API client.
func NewOpenAIClient(config OpenAIConfig) (*OpenAIClient, error) {
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, errors.New("missing API key")
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, errors.New("missing model")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultOpenAIBaseURL
	}
	return &OpenAIClient{
		apiKey:     config.APIKey,
		baseURL:    baseURL,
		model:      config.Model,
		httpClient: http.DefaultClient,
	}, nil
}

// NewOpenAIClientFromEnv loads configuration from LOB_CODEX_* variables and
// falls back to the conventional OPENAI_* names.
func NewOpenAIClientFromEnv() (*OpenAIClient, error) {
	return NewOpenAIClient(OpenAIConfig{
		APIKey:  firstEnvironmentValue("LOB_CODEX_API_KEY", "OPENAI_API_KEY"),
		BaseURL: firstEnvironmentValue("LOB_CODEX_BASE_URL", "OPENAI_BASE_URL"),
		Model:   firstEnvironmentValue("LOB_CODEX_MODEL", "OPENAI_MODEL"),
	})
}

// Stream starts one Responses API request and converts its SSE stream into
// provider-independent harness events.
func (c *OpenAIClient) Stream(ctx context.Context, request Request) Stream {
	events := make(chan ResponseEvent)
	errorsChannel := make(chan error, 1)

	go func() {
		defer close(events)
		defer close(errorsChannel)

		if err := c.stream(ctx, request, events); err != nil {
			errorsChannel <- err
		}
	}()

	return Stream{Events: events, Errors: errorsChannel}
}

func (c *OpenAIClient) stream(ctx context.Context, request Request, events chan<- ResponseEvent) error {
	body, err := json.Marshal(map[string]any{
		"model":               c.model,
		"input":               request.Input,
		"tools":               request.Tools,
		"parallel_tool_calls": false,
		"stream":              true,
	})
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	httpRequest, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+"/responses",
		bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")

	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return fmt.Errorf("request model: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		if readErr != nil {
			return fmt.Errorf("model returned %s", response.Status)
		}
		return fmt.Errorf("model returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}

	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}

		var event struct {
			Type  string                 `json:"type"`
			Delta string                 `json:"delta"`
			Item  *protocol.ResponseItem `json:"item"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return fmt.Errorf("decode stream event: %w", err)
		}

		switch event.Type {
		case "response.output_text.delta":
			if !sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseOutputTextDelta, Delta: event.Delta}) {
				return ctx.Err()
			}
		case "response.output_item.done":
			if event.Item != nil && !sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseOutputItemDone, Item: event.Item}) {
				return ctx.Err()
			}
		case "error":
			if event.Error != nil {
				return errors.New(event.Error.Message)
			}
			return errors.New("model stream returned an error")
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read model stream: %w", err)
	}
	if !sendResponseEvent(ctx, events, ResponseEvent{Type: ResponseCompleted}) {
		return ctx.Err()
	}
	return nil
}

func firstEnvironmentValue(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}
