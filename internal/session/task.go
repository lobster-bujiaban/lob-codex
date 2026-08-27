package session

import (
	"context"
	"time"

	"github.com/lobster-bujiaban/lob-codex/internal/protocol"
)

func (s *Session) spawnRegularTask(turnContext *TurnContext, input []TurnInput) {
	s.abortActive("replaced")

	taskContext, cancel := context.WithCancel(s.ctx)
	task := &runningTask{cancel: cancel, done: make(chan struct{}), turnID: turnContext.SubID}
	s.activeMu.Lock()
	s.active = task
	s.activeMu.Unlock()

	turnContext.StartedAt = time.Now()
	go func() {
		defer close(task.done)
		result := s.runRegularTask(taskContext, turnContext, input)
		s.onTaskFinished(taskContext, task, turnContext, result)

		s.activeMu.Lock()
		if s.active == task {
			s.active = nil
		}
		s.activeMu.Unlock()
	}()
}

func (s *Session) abortActive(reason string) {
	s.activeMu.Lock()
	task := s.active
	s.activeMu.Unlock()
	if task == nil {
		return
	}
	task.abort(reason)
	<-task.done
}

func (s *Session) onTaskFinished(
	ctx context.Context,
	task *runningTask,
	turnContext *TurnContext,
	result taskResult,
) {
	completedAt := time.Now()
	duration := completedAt.Sub(turnContext.StartedAt).Milliseconds()
	if ctx.Err() != nil {
		s.sendEvent(turnContext, protocol.EventMsg{
			Type: "turn_aborted",
			TurnAborted: &protocol.TurnAbortedEvent{
				TurnID:      turnContext.SubID,
				Reason:      task.reason(),
				StartedAt:   turnContext.StartedAt.Unix(),
				CompletedAt: completedAt.Unix(),
				DurationMS:  duration,
			},
		})
		return
	}

	var errorEvent *protocol.ErrorEvent
	if result.Err != nil {
		errorEvent = &protocol.ErrorEvent{Message: result.Err.Error()}
	}
	s.sendEvent(turnContext, protocol.EventMsg{
		Type: "turn_complete",
		TurnComplete: &protocol.TurnCompleteEvent{
			TurnID:             turnContext.SubID,
			LastAgentMessage:   result.LastAgentMessage,
			Error:              errorEvent,
			StartedAt:          turnContext.StartedAt.Unix(),
			CompletedAt:        completedAt.Unix(),
			DurationMS:         duration,
			TimeToFirstTokenMS: result.TimeToFirstToken,
		},
	})
}
