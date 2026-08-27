package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lobster-bujiaban/lob-codex/internal/protocol"
)

type rolloutLine struct {
	Timestamp string                 `json:"timestamp"`
	Ordinal   uint64                 `json:"ordinal"`
	Type      string                 `json:"type"`
	Payload   *protocol.ResponseItem `json:"payload,omitempty"`
}

type rolloutRecorder struct {
	mu      sync.Mutex
	path    string
	ordinal uint64
	file    *os.File
	err     error
}

// FlowEvent is one runtime timeline node reconstructed from canonical rollout items.
type FlowEvent struct {
	Ordinal   uint64 `json:"ordinal"`
	Timestamp string `json:"timestamp"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Detail    string `json:"detail,omitempty"`
	Turn      int    `json:"turn"`
	Step      int    `json:"step,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
}

// FlowSummary contains inexpensive rollout-level runtime indicators.
type FlowSummary struct {
	Turns        int   `json:"turns"`
	ModelCalls   int   `json:"model_calls"`
	ToolCalls    int   `json:"tool_calls"`
	InputTokens  int   `json:"input_tokens"`
	OutputTokens int   `json:"output_tokens"`
	DurationMS   int64 `json:"duration_ms"`
}

func openRollout(path string) (*rolloutRecorder, []protocol.ResponseItem, error) {
	items, nextOrdinal, err := ReadRollout(path)
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, fmt.Errorf("create rollout directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open rollout: %w", err)
	}
	return &rolloutRecorder{path: path, ordinal: nextOrdinal, file: file}, items, nil
}

// WriteRollout creates a canonical rollout containing one forked history prefix.
func WriteRollout(path string, items []protocol.ResponseItem) error {
	recorder, existing, err := openRollout(path)
	if err != nil {
		return err
	}
	if len(existing) != 0 {
		_ = recorder.close()
		return errors.New("fork rollout already contains history")
	}
	recorder.record(items...)
	return recorder.close()
}

// ReadRollout replays canonical response items and returns the next ordinal.
func ReadRollout(path string) ([]protocol.ResponseItem, uint64, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("open rollout: %w", err)
	}
	defer file.Close()

	var items []protocol.ResponseItem
	var nextOrdinal uint64
	scanner := bufio.NewScanner(file)
	buffer := make([]byte, 64<<10)
	scanner.Buffer(buffer, 4<<20)
	for scanner.Scan() {
		var line rolloutLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			return nil, 0, fmt.Errorf("decode rollout ordinal %d: %w", nextOrdinal, err)
		}
		if line.Ordinal >= nextOrdinal {
			nextOrdinal = line.Ordinal + 1
		}
		if line.Type == "response_item" && line.Payload != nil {
			items = append(items, *line.Payload)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, fmt.Errorf("read rollout: %w", err)
	}
	return items, nextOrdinal, nil
}

// ReadRolloutFlow reconstructs Turn/Step/tool lifecycle nodes from the same
// canonical ResponseItems used to resume model history.
func ReadRolloutFlow(path string) ([]FlowEvent, FlowSummary, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, FlowSummary{}, nil
	}
	if err != nil {
		return nil, FlowSummary{}, fmt.Errorf("open rollout: %w", err)
	}
	defer file.Close()

	var events []FlowEvent
	var summary FlowSummary
	var firstTime, lastTime time.Time
	turn, step := 0, 0
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		var line rolloutLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			return nil, FlowSummary{}, fmt.Errorf("decode rollout flow: %w", err)
		}
		if line.Type != "response_item" || line.Payload == nil {
			continue
		}
		occurredAt, _ := time.Parse(time.RFC3339Nano, line.Timestamp)
		if firstTime.IsZero() && !occurredAt.IsZero() {
			firstTime = occurredAt
		}
		if !occurredAt.IsZero() {
			lastTime = occurredAt
		}
		item := *line.Payload
		event := FlowEvent{Ordinal: line.Ordinal, Timestamp: line.Timestamp, Turn: turn, Step: step}
		switch item.Type {
		case "message":
			if item.Role == "user" {
				turn++
				step = 0
				summary.Turns = turn
				event.Turn, event.Step = turn, 0
				event.Kind, event.Title = "turn", "Turn 开始"
				event.Detail = summarizeMessage(item)
				summary.InputTokens += estimateTokens(item.Text())
			} else if item.Role == "assistant" {
				step++
				event.Turn, event.Step = turn, step
				event.Kind, event.Title, event.Detail = "assistant", "模型输出 · Turn 完成", item.Text()
				summary.ModelCalls++
				summary.OutputTokens += estimateTokens(item.Text())
			} else {
				continue
			}
		case "function_call":
			step++
			event.Turn, event.Step = turn, step
			event.Kind, event.Title = "tool_call", "模型返回工具调用"
			event.Detail, event.CallID, event.Name = item.Arguments, item.CallID, item.Name
			summary.ModelCalls++
			summary.ToolCalls++
			summary.OutputTokens += estimateTokens(item.Arguments)
		case "function_call_output":
			event.Turn, event.Step = turn, step
			event.Kind, event.Title = "tool_output", "工具执行结果"
			event.Detail, event.CallID = item.Output, item.CallID
			summary.InputTokens += estimateTokens(item.Output)
		default:
			continue
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, FlowSummary{}, fmt.Errorf("read rollout flow: %w", err)
	}
	if !firstTime.IsZero() && !lastTime.IsZero() {
		summary.DurationMS = lastTime.Sub(firstTime).Milliseconds()
	}
	return events, summary, nil
}

func summarizeMessage(item protocol.ResponseItem) string {
	images := 0
	for _, content := range item.Content {
		if content.Type == "input_image" {
			images++
		}
	}
	text := item.Text()
	if images == 0 {
		return text
	}
	if text == "" {
		return fmt.Sprintf("%d 张图片", images)
	}
	return fmt.Sprintf("%s · %d 张图片", text, images)
}

func estimateTokens(text string) int {
	if text == "" {
		return 0
	}
	return (len([]byte(text)) + 3) / 4
}

func (recorder *rolloutRecorder) record(items ...protocol.ResponseItem) {
	if recorder == nil || len(items) == 0 {
		return
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.err != nil {
		return
	}
	encoder := json.NewEncoder(recorder.file)
	for index := range items {
		line := rolloutLine{
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Ordinal: recorder.ordinal,
			Type: "response_item", Payload: &items[index],
		}
		if err := encoder.Encode(line); err != nil {
			recorder.err = fmt.Errorf("append rollout %s: %w", recorder.path, err)
			return
		}
		recorder.ordinal++
	}
}

func (recorder *rolloutRecorder) close() error {
	if recorder == nil {
		return nil
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.file != nil {
		if err := recorder.file.Sync(); err != nil && recorder.err == nil {
			recorder.err = fmt.Errorf("flush rollout %s: %w", recorder.path, err)
		}
		if err := recorder.file.Close(); err != nil && recorder.err == nil {
			recorder.err = fmt.Errorf("close rollout %s: %w", recorder.path, err)
		}
		recorder.file = nil
	}
	return recorder.err
}
