// Package agent coordinates model calls and publishes observable harness events.
package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/lobster-bujiaban/lob-codex/internal/model"
	"github.com/lobster-bujiaban/lob-codex/internal/protocol"
)

// EventSink receives events produced during a harness run.
type EventSink interface {
	Emit(protocol.Event) error
}

// Runner executes the smallest model-to-event harness loop.
type Runner struct {
	client model.Client
	sink   EventSink
}

// NewRunner constructs a Runner from replaceable model and output boundaries.
func NewRunner(client model.Client, sink EventSink) *Runner {
	return &Runner{client: client, sink: sink}
}

// Run sends one input to the model and forwards its event stream to the sink.
func (r *Runner) Run(ctx context.Context, input string) error {
	stream := r.client.Stream(ctx, model.Request{Input: input})
	events := stream.Events
	errorsChannel := stream.Errors

	for events != nil || errorsChannel != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if err := r.sink.Emit(event); err != nil {
				return err
			}
		case err, ok := <-errorsChannel:
			if !ok {
				errorsChannel = nil
				continue
			}
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// ValidateInput rejects input that cannot form a meaningful model request.
func ValidateInput(input string) error {
	if strings.TrimSpace(input) == "" {
		return errors.New("prompt must not be empty")
	}
	return nil
}
