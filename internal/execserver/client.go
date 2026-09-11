package execserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client talks JSON-RPC 2.0 over HTTP to a remote exec-server.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

func (client *Client) http() *http.Client {
	if client.HTTPClient != nil {
		return client.HTTPClient
	}
	return http.DefaultClient
}

func (client *Client) call(ctx context.Context, method string, params, result any) error {
	payload, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return fmt.Errorf("encode exec-server request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.BaseURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.http().Do(request)
	if err != nil {
		return fmt.Errorf("exec-server %s: %w", method, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("exec-server %s: HTTP %d: %s", method, response.StatusCode, body)
	}
	var decoded rpcResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return fmt.Errorf("decode exec-server response: %w", err)
	}
	if decoded.Error != nil {
		return fmt.Errorf("exec-server %s: %s", method, decoded.Error.Message)
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(decoded.Result, result); err != nil {
		return fmt.Errorf("decode exec-server result: %w", err)
	}
	return nil
}

func (client *Client) Start(ctx context.Context, params ExecParams) error {
	return client.call(ctx, MethodProcessStart, params, nil)
}

func (client *Client) Write(ctx context.Context, processID, chars string) error {
	return client.call(ctx, MethodProcessWrite, WriteParams{ProcessID: processID, Chars: chars}, nil)
}

func (client *Client) Poll(ctx context.Context, processID string, wait time.Duration) (PollResult, error) {
	var result PollResult
	err := client.call(ctx, MethodProcessPoll, PollParams{ProcessID: processID, WaitMS: wait.Milliseconds()}, &result)
	return result, err
}

func (client *Client) Kill(ctx context.Context, processID string) error {
	return client.call(ctx, MethodProcessKill, KillParams{ProcessID: processID}, nil)
}
