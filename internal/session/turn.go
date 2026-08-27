package session

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lobster-bujiaban/lob-codex/internal/model"
	"github.com/lobster-bujiaban/lob-codex/internal/protocol"
)

const maxTurnSteps = 8

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
	for _, item := range input {
		s.history.RecordItems(protocol.NewUserMessage(item.Text))
	}

	for step := 0; step < maxTurnSteps; step++ {
		stepContext := s.captureStepContext(turnContext)
		result, err := s.runSamplingRequest(ctx, stepContext, s.history.ForPrompt())
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
	}
	err := errors.New("turn exceeded maximum of 8 sampling steps")
	s.sendEvent(turnContext, protocol.NewError(err.Error()))
	return taskResult{LastAgentMessage: lastAgentMessage, TimeToFirstToken: timeToFirstToken, Err: err}
}

func (s *Session) captureStepContext(turnContext *TurnContext) *StepContext {
	return &StepContext{Turn: turnContext, ToolRouter: s.tools}
}

func (s *Session) runSamplingRequest(
	ctx context.Context,
	stepContext *StepContext,
	input []protocol.ResponseItem,
) (samplingRequestResult, error) {
	if len(input) == 0 {
		return samplingRequestResult{}, errors.New("conversation history is empty")
	}

	stream := s.client.Stream(ctx, model.Request{
		Input: input,
		Tools: stepContext.ToolRouter.ModelVisibleDefinitions(),
	})
	events := stream.Events
	errorsChannel := stream.Errors
	var output strings.Builder
	var timeToFirstToken *int64
	var completedItem *protocol.ResponseItem
	recordedOutputItem := false
	needsFollowUp := false
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
			case model.ResponseOutputItemDone:
				if event.Item != nil {
					item := *event.Item
					completedItem = &item
					s.history.RecordItems(item)
					recordedOutputItem = true
					call, err := stepContext.ToolRouter.BuildToolCall(item)
					if err != nil {
						callID := item.CallID
						if callID == "" {
							callID = "invalid_function_call"
						}
						toolOutput := protocol.NewFunctionCallOutput(callID, err.Error())
						s.history.RecordItems(toolOutput)
						needsFollowUp = true
					} else if call != nil {
						s.sendEvent(stepContext.Turn, protocol.NewToolCallStarted(call.CallID, call.Name, call.Arguments))
						toolOutput := stepContext.ToolRouter.Execute(ctx, *call, func(message protocol.EventMsg) {
							s.sendEvent(stepContext.Turn, message)
						})
						s.history.RecordItems(toolOutput)
						s.sendEvent(stepContext.Turn, protocol.NewToolCallCompleted(call.CallID, call.Name, toolOutput.Output))
						needsFollowUp = true
					}
				}
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
	if completedItem == nil {
		item := protocol.NewAssistantMessage(message)
		completedItem = &item
	}
	if !recordedOutputItem {
		s.history.RecordItems(*completedItem)
	}
	if completedItem.Role == "assistant" && completedItem.Text() != "" {
		message = completedItem.Text()
	}
	return samplingRequestResult{
		NeedsFollowUp:    needsFollowUp,
		LastAgentMessage: &message,
		TimeToFirstToken: timeToFirstToken,
	}, nil
}
