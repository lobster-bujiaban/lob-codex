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
		if item.Type == "message" {
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
	return items
}
