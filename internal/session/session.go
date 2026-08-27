// Package session implements the Codex submission, task, turn, and sampling lifecycle.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/lobster-bujiaban/lob-codex/internal/model"
	"github.com/lobster-bujiaban/lob-codex/internal/protocol"
)

// Session owns the model client, active task, and public event stream.
type Session struct {
	client  model.Client
	events  chan protocol.Event
	ctx     context.Context
	cancel  context.CancelFunc
	history ConversationHistory

	activeMu sync.Mutex
	active   *runningTask
}

// IO is the client-facing submission and event boundary for a Session.
type IO struct {
	txSub        chan<- Submission
	rxEvent      <-chan protocol.Event
	done         <-chan struct{}
	shutdownOnce sync.Once
}

type runningTask struct {
	cancel context.CancelFunc
	done   chan struct{}

	mu          sync.Mutex
	abortReason string
}

// New creates a session and starts the long-running submission loop.
func New(client model.Client) (*Session, *IO) {
	ctx, cancel := context.WithCancel(context.Background())
	submissions := make(chan Submission)
	events := make(chan protocol.Event, 128)
	done := make(chan struct{})
	sess := &Session{client: client, events: events, ctx: ctx, cancel: cancel}
	io := &IO{txSub: submissions, rxEvent: events, done: done}
	go sess.submissionLoop(submissions, done)
	return sess, io
}

// Submit wraps an operation in a uniquely identified Submission.
func (io *IO) Submit(ctx context.Context, op Op) (string, error) {
	id, err := newSubmissionID()
	if err != nil {
		return "", err
	}
	submission := Submission{ID: id, Op: op}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-io.done:
		return "", errors.New("session stopped")
	case io.txSub <- submission:
		return id, nil
	}
}

// SubmitTurnInput starts the StartOrSteer path with one text input.
func (io *IO) SubmitTurnInput(ctx context.Context, text string) (string, error) {
	return io.Submit(ctx, Op{Type: OpTurnInput, Input: []TurnInput{{Text: text}}})
}

// NextEvent waits for the next public session event.
func (io *IO) NextEvent(ctx context.Context) (protocol.Event, error) {
	select {
	case <-ctx.Done():
		return protocol.Event{}, ctx.Err()
	case event, ok := <-io.rxEvent:
		if !ok {
			return protocol.Event{}, errors.New("session stopped")
		}
		return event, nil
	}
}

// Shutdown asks the submission loop to stop and waits for task teardown.
func (io *IO) Shutdown(ctx context.Context) error {
	var submitErr error
	io.shutdownOnce.Do(func() {
		_, submitErr = io.Submit(ctx, Op{Type: OpShutdown})
	})
	if submitErr != nil && submitErr.Error() != "session stopped" {
		return submitErr
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-io.done:
		return nil
	}
}

func (s *Session) submissionLoop(submissions <-chan Submission, done chan<- struct{}) {
	defer close(done)
	defer close(s.events)
	defer s.cancel()

	for submission := range submissions {
		switch submission.Op.Type {
		case OpTurnInput:
			s.handleTurnInput(submission)
		case OpShutdown:
			s.abortActive("shutdown")
			return
		default:
			s.sendEventRaw(protocol.Event{
				ID:  submission.ID,
				Msg: protocol.NewError(fmt.Sprintf("unsupported operation %q", submission.Op.Type)),
			})
		}
	}
	s.abortActive("session channel closed")
}

func (s *Session) handleTurnInput(submission Submission) {
	if len(submission.Op.Input) != 1 || ValidateInput(submission.Op.Input[0].Text) != nil {
		s.sendEventRaw(protocol.Event{ID: submission.ID, Msg: protocol.NewError("prompt must not be empty")})
		return
	}
	turnContext := &TurnContext{SubID: submission.ID}
	s.spawnRegularTask(turnContext, submission.Op.Input)
}

// ValidateInput rejects input that cannot start or steer a Codex turn.
func ValidateInput(input string) error {
	if strings.TrimSpace(input) == "" {
		return errors.New("prompt must not be empty")
	}
	return nil
}

func (s *Session) sendEvent(turnContext *TurnContext, message protocol.EventMsg) {
	s.sendEventRaw(protocol.Event{ID: turnContext.SubID, Msg: message})
}

func (s *Session) sendEventRaw(event protocol.Event) {
	select {
	case <-s.ctx.Done():
	case s.events <- event:
	}
}

func newSubmissionID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate submission ID: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func (task *runningTask) abort(reason string) {
	task.mu.Lock()
	task.abortReason = reason
	task.mu.Unlock()
	task.cancel()
}

func (task *runningTask) reason() string {
	task.mu.Lock()
	defer task.mu.Unlock()
	return task.abortReason
}
