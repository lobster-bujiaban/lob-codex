package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/lobster-bujiaban/lob-codex/internal/model"
	"github.com/lobster-bujiaban/lob-codex/internal/protocol"
	"github.com/lobster-bujiaban/lob-codex/internal/tools"
)

const compactPrompt = `You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary for another LLM that will resume the task.

Include current progress and key decisions, important constraints and preferences, remaining work, and critical files or references. Be concise, structured, and focused on seamless continuation.`

const compactSummaryPrefix = `Another language model started to solve this problem and produced a summary of its work. Continue from this checkpoint:

`

func (s *Session) runRegularTask(
	ctx context.Context,
	turnContext *TurnContext,
	input []TurnInput,
) taskResult {
	s.sendEvent(turnContext, protocol.NewTurnStarted(turnContext.SubID, turnContext.StartedAt.Unix()))
	s.rollout.recordTurnContext(turnContext.SubID, s.workspaceRoot)
	return s.runTurn(ctx, turnContext, input)
}

// runTurn mirrors Codex's regular-turn loop: capture a StepContext, sample,
// and continue only when the model or pending input requires a follow-up.
func (s *Session) runTurn(ctx context.Context, turnContext *TurnContext, input []TurnInput) taskResult {
	var lastAgentMessage *string
	var timeToFirstToken *int64
	var turnUsage model.TokenUsage
	var responseID string
	for _, item := range input {
		s.recordTurnInput(item)
	}

	for {
		if s.shouldCompact() {
			if err := s.compactHistory(ctx, turnContext); err != nil {
				s.sendEvent(turnContext, protocol.NewError(err.Error()))
				return taskResult{LastAgentMessage: lastAgentMessage, TimeToFirstToken: timeToFirstToken, Err: err}
			}
		}
		stepContext := s.captureStepContext(turnContext)
		result, err := s.runSamplingRequest(ctx, stepContext, s.history.ForPrompt())
		if err != nil {
			s.sendEvent(turnContext, protocol.NewError(err.Error()))
			return taskResult{LastAgentMessage: lastAgentMessage, TimeToFirstToken: timeToFirstToken, Err: err}
		}
		lastAgentMessage = result.LastAgentMessage
		turnUsage.Add(&result.Usage)
		if result.ResponseID != "" {
			responseID = result.ResponseID
		}
		if result.TimeToFirstToken != nil {
			timeToFirstToken = result.TimeToFirstToken
		}
		s.activeMu.Lock()
		active := s.active
		s.activeMu.Unlock()
		if active != nil && active.turnID == turnContext.SubID {
			for _, item := range active.takePendingInput(result.NeedsFollowUp) {
				s.recordTurnInput(item)
				result.NeedsFollowUp = true
			}
		}
		if !result.NeedsFollowUp {
			return taskResult{LastAgentMessage: lastAgentMessage, TimeToFirstToken: timeToFirstToken, Usage: turnUsage, ResponseID: responseID}
		}
	}
}

func (s *Session) recordTurnInput(input TurnInput) {
	s.recordConversationItems(protocol.NewUserMessageWithImages(input.Text, input.ImageURLs))
	if instructions := s.extensions.Instructions(input.Text); instructions != "" && instructions != input.Text {
		s.recordConversationItems(protocol.NewDeveloperMessage(instructions))
	}
}

func (s *Session) shouldCompact() bool {
	window := s.client.ContextWindow()
	return window > 0 && len(s.history.ForPrompt()) > 2 && s.history.EstimatedTokens() >= window*9/10
}

func (s *Session) compactHistory(ctx context.Context, turnContext *TurnContext) error {
	history := s.history.ForPrompt()
	if len(history) == 0 {
		return nil
	}
	beforeTokens := s.history.EstimatedTokens()
	requestInput := append([]protocol.ResponseItem(nil), history...)
	requestInput = append(requestInput, protocol.NewUserMessage(compactPrompt))
	stream := s.client.Stream(ctx, model.Request{Input: requestInput})
	var summary strings.Builder
	var completed bool
	for stream.Events != nil || stream.Errors != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-stream.Events:
			if !ok {
				stream.Events = nil
				continue
			}
			switch event.Type {
			case model.ResponseOutputTextDelta:
				summary.WriteString(event.Delta)
			case model.ResponseOutputItemDone:
				if event.Item != nil && event.Item.Role == "assistant" {
					summary.Reset()
					summary.WriteString(event.Item.Text())
				}
			case model.ResponseCompleted:
				completed = true
			}
		case err, ok := <-stream.Errors:
			if !ok {
				stream.Errors = nil
				continue
			}
			if err != nil {
				return err
			}
		}
	}
	if !completed || strings.TrimSpace(summary.String()) == "" {
		return errors.New("context compaction completed without a summary")
	}
	replacement := []protocol.ResponseItem{protocol.NewUserMessage(compactSummaryPrefix + summary.String())}
	for index := len(history) - 1; index >= 0; index-- {
		if history[index].Type == "message" && history[index].Role == "user" {
			replacement = append(replacement, history[index])
			break
		}
	}
	s.history.Replace(replacement)
	afterTokens := s.history.EstimatedTokens()
	s.rollout.recordCompacted(summary.String(), replacement, afterTokens)
	s.sendEvent(turnContext, protocol.NewContextCompaction(turnContext.SubID, beforeTokens, afterTokens))
	return nil
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
	var lastAssistantItem *protocol.ResponseItem
	var pendingCalls []tools.Call
	needsFollowUp := false
	completed := false
	var usage model.TokenUsage
	var responseID string

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
					s.recordConversationItems(item)
					if item.Role == "assistant" {
						copy := item
						lastAssistantItem = &copy
					}
					call, err := stepContext.ToolRouter.BuildToolCall(item)
					if err != nil {
						callID := item.CallID
						if callID == "" {
							callID = "invalid_function_call"
						}
						toolOutput := protocol.NewFunctionCallOutput(callID, err.Error())
						s.recordConversationItems(toolOutput)
						needsFollowUp = true
					} else if call != nil {
						s.sendEvent(stepContext.Turn, protocol.NewToolCallStarted(call.CallID, call.Name, call.Arguments))
						pendingCalls = append(pendingCalls, *call)
					}
				}
			case model.ResponseCompleted:
				completed = true
				usage.Add(event.Usage)
				responseID = event.ResponseID
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
	if len(pendingCalls) > 0 {
		outputs := make([]protocol.ResponseItem, len(pendingCalls))
		var wait sync.WaitGroup
		for index := range pendingCalls {
			wait.Add(1)
			go func(index int) {
				defer wait.Done()
				outputs[index] = stepContext.ToolRouter.Execute(ctx, pendingCalls[index], func(message protocol.EventMsg) {
					s.sendEvent(stepContext.Turn, message)
				})
			}(index)
		}
		wait.Wait()
		for index, toolOutput := range outputs {
			s.recordConversationItems(toolOutput)
			call := pendingCalls[index]
			s.sendEvent(stepContext.Turn, protocol.NewToolCallCompleted(call.CallID, call.Name, toolOutput.Output))
		}
		needsFollowUp = true
	}
	message := output.String()
	if lastAssistantItem == nil && message != "" {
		item := protocol.NewAssistantMessage(message)
		lastAssistantItem = &item
		s.recordConversationItems(item)
	}
	if lastAssistantItem != nil && lastAssistantItem.Text() != "" {
		message = lastAssistantItem.Text()
	}
	var lastMessage *string
	if message != "" {
		lastMessage = &message
	}
	return samplingRequestResult{
		NeedsFollowUp:    needsFollowUp,
		LastAgentMessage: lastMessage,
		TimeToFirstToken: timeToFirstToken,
		Usage:            usage,
		ResponseID:       responseID,
	}, nil
}
