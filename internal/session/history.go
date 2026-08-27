package session

import (
	"sync"

	"github.com/lobster-bujiaban/lob-codex/internal/protocol"
)

// ConversationHistory owns model-visible ResponseItems ordered oldest to newest,
// matching Codex's context manager boundary.
type ConversationHistory struct {
	mu    sync.RWMutex
	items []protocol.ResponseItem
}

// RecordItems appends accepted API message items in conversation order.
func (history *ConversationHistory) RecordItems(items ...protocol.ResponseItem) {
	history.mu.Lock()
	defer history.mu.Unlock()
	for _, item := range items {
		switch item.Type {
		case "message", "function_call", "function_call_output":
			history.items = append(history.items, item)
		}
	}
}

// ForPrompt returns an isolated snapshot prepared for one model request.
func (history *ConversationHistory) ForPrompt() []protocol.ResponseItem {
	history.mu.RLock()
	defer history.mu.RUnlock()
	items := make([]protocol.ResponseItem, len(history.items))
	copy(items, history.items)
	return normalizeForPrompt(items)
}

// normalizeForPrompt enforces the Responses API call/output invariants without
// mutating or persisting synthetic repair items, matching Codex ContextManager.
func normalizeForPrompt(items []protocol.ResponseItem) []protocol.ResponseItem {
	callIDs := make(map[string]struct{})
	outputIDs := make(map[string]struct{})
	for _, item := range items {
		switch item.Type {
		case "function_call":
			if item.CallID != "" {
				callIDs[item.CallID] = struct{}{}
			}
		case "function_call_output":
			if item.CallID != "" {
				outputIDs[item.CallID] = struct{}{}
			}
		}
	}

	normalized := make([]protocol.ResponseItem, 0, len(items)+len(callIDs))
	for _, item := range items {
		if item.Type == "function_call_output" {
			if _, exists := callIDs[item.CallID]; !exists {
				continue
			}
		}
		normalized = append(normalized, item)
		if item.Type == "function_call" {
			if _, exists := outputIDs[item.CallID]; !exists {
				normalized = append(normalized, protocol.NewFunctionCallOutput(item.CallID, "aborted"))
			}
		}
	}
	return normalized
}

// Restore replaces the live model context with canonical response items replayed
// from a persisted rollout before the submission loop starts.
func (history *ConversationHistory) Restore(items []protocol.ResponseItem) {
	history.mu.Lock()
	defer history.mu.Unlock()
	history.items = history.items[:0]
	for _, item := range items {
		switch item.Type {
		case "message", "function_call", "function_call_output":
			history.items = append(history.items, item)
		}
	}
}
