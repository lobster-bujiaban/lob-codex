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
