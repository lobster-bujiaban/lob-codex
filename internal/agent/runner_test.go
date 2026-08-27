package agent_test

import (
	"context"
	"testing"

	"github.com/lobster-bujiaban/lob-codex/internal/agent"
	"github.com/lobster-bujiaban/lob-codex/internal/model"
	"github.com/lobster-bujiaban/lob-codex/internal/protocol"
)

type recordingSink struct {
	events []protocol.Event
}

func (s *recordingSink) Emit(event protocol.Event) error {
	s.events = append(s.events, event)
	return nil
}

func TestRunnerForwardsCompleteModelLifecycle(t *testing.T) {
	sink := &recordingSink{}
	runner := agent.NewRunner(model.NewFakeClient(), sink)

	if err := runner.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []protocol.Event{
		{Type: protocol.EventResponseStarted},
		{Type: protocol.EventTextDelta, Text: "Fake "},
		{Type: protocol.EventTextDelta, Text: "model: "},
		{Type: protocol.EventTextDelta, Text: "hello"},
		{Type: protocol.EventResponseCompleted},
	}
	if len(sink.events) != len(want) {
		t.Fatalf("events = %#v, want %#v", sink.events, want)
	}
	for index := range want {
		if sink.events[index] != want[index] {
			t.Fatalf("events = %#v, want %#v", sink.events, want)
		}
	}
}
