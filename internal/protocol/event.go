// Package protocol defines the events exchanged by the harness core and its clients.
package protocol

// EventType identifies a harness lifecycle event.
type EventType string

const (
	EventResponseStarted   EventType = "response.started"
	EventTextDelta         EventType = "text.delta"
	EventResponseCompleted EventType = "response.completed"
)

// Event is the smallest observable unit emitted by a harness run.
type Event struct {
	Type EventType `json:"type"`
	Text string    `json:"text,omitempty"`
}
