package session

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lobster-bujiaban/lob-codex/internal/model"
	"github.com/lobster-bujiaban/lob-codex/internal/protocol"
)

func (s *Session) runRegularTask(
	ctx context.Context,
	turnContext *TurnContext,
	input []TurnInput,
) taskResult {
	s.sendEvent(turnContext, protocol.NewTurnStarted(turnContext.SubID, turnContext.StartedAt.Unix()))
	return s.runTurn(ctx, turnContext, input)
}

// runTurn mirrors Codex's regular-turn loop: capture a StepContext, sample,
// and continue only when the model or pending input requires a follow-up.
func (s *Session) runTurn(ctx context.Context, turnContext *TurnContext, input []TurnInput) taskResult {
	var lastAgentMessage *string
	var timeToFirstToken *int64
	nextInput := input

	for {
		stepContext := s.captureStepContext(turnContext)
		result, err := s.runSamplingRequest(ctx, stepContext, nextInput)
		if err != nil {
			s.sendEvent(turnContext, protocol.NewError(err.Error()))
			return taskResult{LastAgentMessage: lastAgentMessage, TimeToFirstToken: timeToFirstToken, Err: err}
		}
		lastAgentMessage = result.LastAgentMessage
		if result.TimeToFirstToken != nil {
			timeToFirstToken = result.TimeToFirstToken
		}
		if !result.NeedsFollowUp {
			return taskResult{LastAgentMessage: lastAgentMessage, TimeToFirstToken: timeToFirstToken}
		}
		nextInput = nil
	}
}

func (s *Session) captureStepContext(turnContext *TurnContext) *StepContext {
	return &StepContext{Turn: turnContext}
}

func (s *Session) runSamplingRequest(
	ctx context.Context,
	stepContext *StepContext,
	input []TurnInput,
) (samplingRequestResult, error) {
	if len(input) == 0 {
		return samplingRequestResult{}, errors.New("follow-up sampling input is not implemented")
	}

	stream := s.client.Stream(ctx, model.Request{Input: input[0].Text})
	events := stream.Events
	errorsChannel := stream.Errors
	var output strings.Builder
	var timeToFirstToken *int64
	completed := false

	for events != nil || errorsChannel != nil {
		select {
		case <-ctx.Done():
			return samplingRequestResult{}, ctx.Err()
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			switch event.Type {
			case model.ResponseOutputTextDelta:
				if timeToFirstToken == nil {
					value := time.Since(stepContext.Turn.StartedAt).Milliseconds()
					timeToFirstToken = &value
				}
				output.WriteString(event.Delta)
				s.sendEvent(
					stepContext.Turn,
					protocol.NewAgentMessageContentDelta(stepContext.Turn.SubID, event.Delta),
				)
			case model.ResponseCompleted:
				completed = true
			}
		case err, ok := <-errorsChannel:
			if !ok {
				errorsChannel = nil
				continue
			}
			if err != nil {
				return samplingRequestResult{}, err
			}
		}
	}

	if !completed {
		return samplingRequestResult{}, errors.New("model stream closed before response.completed")
	}
	message := output.String()
	return samplingRequestResult{
		NeedsFollowUp:    false,
		LastAgentMessage: &message,
		TimeToFirstToken: timeToFirstToken,
	}, nil
}
