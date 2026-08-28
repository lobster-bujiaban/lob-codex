package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lobster-bujiaban/lob-codex/internal/protocol"
)

type rolloutLine struct {
	Timestamp string          `json:"timestamp"`
	Ordinal   uint64          `json:"ordinal"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

type sessionMetaItem struct {
	ID            string `json:"id"`
	StartedAt     string `json:"started_at"`
	CWD           string `json:"cwd"`
	Source        string `json:"source"`
	ModelProvider string `json:"model_provider"`
}

type turnContextItem struct {
	TurnID            string `json:"turn_id"`
	CWD               string `json:"cwd"`
	ApprovalPolicy    string `json:"approval_policy"`
	SandboxPolicy     string `json:"sandbox_policy"`
	CollaborationMode string `json:"collaboration_mode"`
}

type compactedItem struct {
	Message            string                  `json:"message"`
	ReplacementHistory []protocol.ResponseItem `json:"replacement_history"`
	EstimatedTokens    int                     `json:"estimated_tokens"`
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
	UsageExact   bool  `json:"usage_exact"`
}

type StoredEvent struct {
	Ordinal   uint64          `json:"ordinal"`
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type StoredTurn struct {
	ID               string                  `json:"id"`
	Status           string                  `json:"status"`
	Items            []protocol.ResponseItem `json:"items"`
	StartedAt        int64                   `json:"started_at,omitempty"`
	CompletedAt      int64                   `json:"completed_at,omitempty"`
	Error            *protocol.ErrorEvent    `json:"error,omitempty"`
	Usage            *protocol.TokenUsage    `json:"usage,omitempty"`
	LastAgentMessage *string                 `json:"last_agent_message,omitempty"`
	ResponseID       string                  `json:"response_id,omitempty"`
}

func ReadRolloutEvents(path string, after uint64) ([]StoredEvent, uint64, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, after, nil
	}
	if err != nil {
		return nil, after, err
	}
	defer file.Close()
	var events []StoredEvent
	next := after
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		var line rolloutLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			return nil, next, err
		}
		if line.Ordinal < after {
			continue
		}
		events = append(events, StoredEvent{Ordinal: line.Ordinal, Timestamp: line.Timestamp, Type: line.Type, Payload: line.Payload})
		if line.Ordinal >= next {
			next = line.Ordinal + 1
		}
	}
	return events, next, scanner.Err()
}

func ReadRolloutTurns(path string) ([]StoredTurn, error) {
	events, _, err := ReadRolloutEvents(path, 0)
	if err != nil {
		return nil, err
	}
	var turns []StoredTurn
	current := -1
	for _, event := range events {
		switch event.Type {
		case "event_msg":
			var message protocol.EventMsg
			if json.Unmarshal(event.Payload, &message) != nil {
				continue
			}
			if message.TurnStarted != nil {
				turns = append(turns, StoredTurn{ID: message.TurnStarted.TurnID, Status: "inProgress", StartedAt: message.TurnStarted.StartedAt})
				current = len(turns) - 1
			}
			if current >= 0 && message.TurnComplete != nil {
				turns[current].Status = "completed"
				turns[current].CompletedAt = message.TurnComplete.CompletedAt
				turns[current].Error = message.TurnComplete.Error
				turns[current].Usage = message.TurnComplete.Usage
				turns[current].LastAgentMessage = message.TurnComplete.LastAgentMessage
				turns[current].ResponseID = message.TurnComplete.ResponseID
				if message.TurnComplete.Error != nil {
					turns[current].Status = "failed"
				}
			}
			if current >= 0 && message.TurnAborted != nil {
				turns[current].Status = "interrupted"
				turns[current].CompletedAt = message.TurnAborted.CompletedAt
			}
		case "response_item":
			if current >= 0 {
				var item protocol.ResponseItem
				if json.Unmarshal(event.Payload, &item) == nil {
					turns[current].Items = append(turns[current].Items, item)
				}
			}
		}
	}
	return turns, nil
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
func WriteRollout(path, workspaceRoot string, items []protocol.ResponseItem) error {
	recorder, existing, err := openRollout(path)
	if err != nil {
		return err
	}
	if len(existing) != 0 {
		_ = recorder.close()
		return errors.New("fork rollout already contains history")
	}
	recorder.recordSessionMeta(workspaceRoot)
	recorder.recordResponseItems(items...)
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
		if line.Type == "response_item" && len(line.Payload) != 0 {
			var item protocol.ResponseItem
			if err := json.Unmarshal(line.Payload, &item); err != nil {
				return nil, 0, fmt.Errorf("decode response item ordinal %d: %w", line.Ordinal, err)
			}
			items = append(items, item)
		} else if line.Type == "compacted" && len(line.Payload) != 0 {
			var compacted compactedItem
			if err := json.Unmarshal(line.Payload, &compacted); err != nil {
				return nil, 0, fmt.Errorf("decode compacted item ordinal %d: %w", line.Ordinal, err)
			}
			items = append([]protocol.ResponseItem(nil), compacted.ReplacementHistory...)
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
	exactUsage := false
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		var line rolloutLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			return nil, FlowSummary{}, fmt.Errorf("decode rollout flow: %w", err)
		}
		if line.Type == "event_msg" && len(line.Payload) != 0 {
			var message protocol.EventMsg
			if json.Unmarshal(line.Payload, &message) == nil && message.TurnComplete != nil {
				summary.DurationMS += message.TurnComplete.DurationMS
				if usage := message.TurnComplete.Usage; usage != nil && usage.TotalTokens > 0 {
					if !exactUsage {
						summary.InputTokens, summary.OutputTokens, exactUsage = 0, 0, true
					}
					summary.InputTokens += int(usage.InputTokens)
					summary.OutputTokens += int(usage.OutputTokens)
				}
			}
		}
		if line.Type != "response_item" || len(line.Payload) == 0 {
			continue
		}
		var item protocol.ResponseItem
		if err := json.Unmarshal(line.Payload, &item); err != nil {
			return nil, FlowSummary{}, fmt.Errorf("decode rollout response item: %w", err)
		}
		occurredAt, _ := time.Parse(time.RFC3339Nano, line.Timestamp)
		if firstTime.IsZero() && !occurredAt.IsZero() {
			firstTime = occurredAt
		}
		if !occurredAt.IsZero() {
			lastTime = occurredAt
		}
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
				if !exactUsage {
					summary.InputTokens += estimateTokens(item.Text())
				}
			} else if item.Role == "assistant" {
				step++
				event.Turn, event.Step = turn, step
				event.Kind, event.Title, event.Detail = "assistant", "模型输出 · Turn 完成", item.Text()
				summary.ModelCalls++
				if !exactUsage {
					summary.OutputTokens += estimateTokens(item.Text())
				}
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
			if !exactUsage {
				summary.OutputTokens += estimateTokens(item.Arguments)
			}
		case "function_call_output":
			event.Turn, event.Step = turn, step
			event.Kind, event.Title = "tool_output", "工具执行结果"
			event.Detail, event.CallID = item.Output, item.CallID
			if !exactUsage {
				summary.InputTokens += estimateTokens(item.Output)
			}
		default:
			continue
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, FlowSummary{}, fmt.Errorf("read rollout flow: %w", err)
	}
	if summary.DurationMS == 0 && !firstTime.IsZero() && !lastTime.IsZero() {
		summary.DurationMS = lastTime.Sub(firstTime).Milliseconds()
	}
	summary.UsageExact = exactUsage
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

func (recorder *rolloutRecorder) recordResponseItems(items ...protocol.ResponseItem) {
	if recorder == nil || len(items) == 0 {
		return
	}
	for index := range items {
		recorder.recordItem("response_item", items[index])
	}
}

func (recorder *rolloutRecorder) recordSessionMeta(workspaceRoot string) {
	if recorder == nil || recorder.ordinal != 0 {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	id := strings.TrimSuffix(filepath.Base(recorder.path), filepath.Ext(recorder.path))
	recorder.recordItem("session_meta", sessionMetaItem{
		ID: id, StartedAt: now, CWD: workspaceRoot, Source: "lob_codex", ModelProvider: "openai",
	})
}

func (recorder *rolloutRecorder) recordTurnContext(turnID, workspaceRoot string) {
	recorder.recordItem("turn_context", turnContextItem{
		TurnID: turnID, CWD: workspaceRoot, ApprovalPolicy: "on-request",
		SandboxPolicy: "workspace-write", CollaborationMode: "default",
	})
}

func (recorder *rolloutRecorder) recordEvent(message protocol.EventMsg) {
	switch message.Type {
	case "agent_message_content_delta", "exec_command_output_delta":
		return
	default:
		recorder.recordItem("event_msg", message)
	}
}

func (recorder *rolloutRecorder) recordCompacted(message string, history []protocol.ResponseItem, estimatedTokens int) {
	recorder.recordItem("compacted", compactedItem{
		Message: message, ReplacementHistory: history, EstimatedTokens: estimatedTokens,
	})
}

func (recorder *rolloutRecorder) recordItem(itemType string, payload any) {
	if recorder == nil {
		return
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.err != nil {
		return
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		recorder.err = fmt.Errorf("encode rollout %s item: %w", itemType, err)
		return
	}
	line := rolloutLine{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Ordinal: recorder.ordinal,
		Type: itemType, Payload: encoded,
	}
	if err := json.NewEncoder(recorder.file).Encode(line); err != nil {
		recorder.err = fmt.Errorf("append rollout %s: %w", recorder.path, err)
		return
	}
	recorder.ordinal++
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
