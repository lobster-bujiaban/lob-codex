package mcp

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
	"time"
)

func (client *Client) callHTTP(ctx context.Context, payload []byte, target any) error {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 200 * time.Millisecond):
			}
		}
		err := client.doHTTP(ctx, payload, target)
		if err == nil {
			return nil
		}
		if errors.Is(err, ErrOAuthRequired) {
			if refreshed := client.refreshAuthorization(ctx); refreshed {
				last = err
				continue
			}
			return err
		}
		last = err
		if !retryable(err) {
			return err
		}
	}
	return last
}

func (client *Client) doHTTP(ctx context.Context, payload []byte, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	client.applyHeaders(request)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if id := response.Header.Get("Mcp-Session-Id"); id != "" {
		client.sessionID = id
	}
	if response.StatusCode == http.StatusUnauthorized && oauthWWWAuthenticate(response.Header.Get("WWW-Authenticate")) {
		return ErrOAuthRequired
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("MCP HTTP %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	if strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		parsed := firstSSEData(string(body))
		if len(parsed) == 0 {
			return nil
		}
		body = parsed
	}
	if target == nil {
		return nil
	}
	var envelope rpcResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return err
	}
	if envelope.Error != nil {
		return fmt.Errorf("MCP HTTP: %s", envelope.Error.Message)
	}
	if len(envelope.Result) == 0 {
		return nil
	}
	return json.Unmarshal(envelope.Result, target)
}

func (client *Client) applyHeaders(request *http.Request) {
	for key, value := range client.headers {
		request.Header.Set(key, os.ExpandEnv(value))
	}
	if client.sessionID != "" {
		request.Header.Set("Mcp-Session-Id", client.sessionID)
	}
	if client.authorization != "" {
		request.Header.Set("Authorization", client.authorization)
	}
}

func (client *Client) refreshAuthorization(ctx context.Context) bool {
	if client.workspace == "" {
		return false
	}
	token, err := RefreshToken(ctx, client.workspace, client.name, client.httpClient)
	if err != nil {
		return false
	}
	client.authorization = token.AuthorizationHeader()
	return true
}

func (client *Client) loadAuthorization() {
	if client.workspace == "" {
		return
	}
	if token, err := RefreshToken(context.Background(), client.workspace, client.name, client.httpClient); err == nil {
		client.authorization = token.AuthorizationHeader()
	}
}

func (client *Client) startHTTPStream(ctx context.Context) {
	go func() {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.url, nil)
		if err != nil {
			return
		}
		request.Header.Set("Accept", "text/event-stream")
		client.applyHeaders(request)
		response, err := client.httpClient.Do(request)
		if err != nil {
			return
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return
		}
		client.readSSE(response.Body)
	}()
}

func (client *Client) readSSE(body io.Reader) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8<<20)
	var data []string
	flush := func() {
		if len(data) == 0 {
			return
		}
		client.dispatchJSON([]byte(strings.Join(data, "\n")))
		data = data[:0]
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	flush()
}

func (client *Client) dispatchJSON(payload []byte) {
	var envelope struct {
		ID     *int            `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return
	}
	if envelope.ID != nil && envelope.Method != "" {
		select {
		case client.requests <- Request{ID: *envelope.ID, Method: envelope.Method, Params: envelope.Params}:
		default:
		}
		return
	}
	if envelope.ID != nil {
		client.stateMu.Lock()
		reply := client.pending[*envelope.ID]
		client.stateMu.Unlock()
		if reply != nil {
			select {
			case reply <- rpcResponse{Result: envelope.Result, Error: envelope.Error}:
			default:
			}
		}
		return
	}
	if envelope.Method != "" {
		select {
		case client.notifications <- Notification{Method: envelope.Method, Params: envelope.Params}:
		default:
		}
	}
}

func firstSSEData(body string) []byte {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data:") {
			return []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	return nil
}

func retryable(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "timeout") || strings.Contains(text, "connection") || strings.Contains(text, "503") || strings.Contains(text, "502") || strings.Contains(text, "429")
}
