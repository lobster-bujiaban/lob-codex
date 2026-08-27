// Package session implements the Codex submission, task, turn, and sampling lifecycle.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lobster-bujiaban/lob-codex/internal/model"
	"github.com/lobster-bujiaban/lob-codex/internal/protocol"
	"github.com/lobster-bujiaban/lob-codex/internal/tools"
)

// Session owns the model client, active task, and public event stream.
type Session struct {
	client  model.Client
	events  chan protocol.Event
	ctx     context.Context
	cancel  context.CancelFunc
	history ConversationHistory
	tools   *tools.Router

	approvalMu sync.Mutex
	approvals  map[string]pendingApproval

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
	turnID string

	mu          sync.Mutex
	abortReason string
}

type pendingApproval struct {
	turnID string
	result chan tools.ApprovalDecision
}

// New creates a session and starts the long-running submission loop.
func New(client model.Client) (*Session, *IO) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		workingDirectory = "."
	}
	sess, io, err := NewInWorkspace(client, workingDirectory)
	if err != nil {
		panic(err)
	}
	return sess, io
}

// NewInWorkspace creates a Session whose tool environment is rooted at one directory.
func NewInWorkspace(client model.Client, workspaceRoot string) (*Session, *IO, error) {
	workspaceRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	workspaceRoot, err = filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("canonicalize workspace root: %w", err)
	}
	info, err := os.Stat(workspaceRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("open workspace root: %w", err)
	}
	if !info.IsDir() {
		return nil, nil, errors.New("workspace root must be a directory")
	}
	ctx, cancel := context.WithCancel(context.Background())
	submissions := make(chan Submission)
	events := make(chan protocol.Event, 128)
	done := make(chan struct{})
	environment := tools.Environment{WorkingDirectory: workspaceRoot, WorkspaceRoot: workspaceRoot}
	sess := &Session{
		client: client, events: events, ctx: ctx, cancel: cancel,
		tools: tools.NewDefaultRouter(environment), approvals: make(map[string]pendingApproval),
	}
	sess.tools.SetApprovalReviewer(sess.requestCommandApproval)
	io := &IO{txSub: submissions, rxEvent: events, done: done}
	go sess.submissionLoop(submissions, done)
	return sess, io, nil
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

// RespondExecApproval delivers a client decision through the submission loop.
func (io *IO) RespondExecApproval(ctx context.Context, response ExecApprovalResponse) error {
	_, err := io.Submit(ctx, Op{Type: OpExecApproval, Approval: &response})
	return err
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
	defer s.tools.Close()

	for submission := range submissions {
		switch submission.Op.Type {
		case OpTurnInput:
			s.handleTurnInput(submission)
		case OpExecApproval:
			s.handleExecApproval(submission)
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

func (s *Session) requestCommandApproval(ctx context.Context, request tools.ApprovalRequest) (tools.ApprovalDecision, error) {
	turnID := s.activeTurnID()
	result := make(chan tools.ApprovalDecision, 1)
	s.approvalMu.Lock()
	s.approvals[request.CallID] = pendingApproval{turnID: turnID, result: result}
	s.approvalMu.Unlock()
	defer func() {
		s.approvalMu.Lock()
		delete(s.approvals, request.CallID)
		s.approvalMu.Unlock()
	}()
	s.sendEventRaw(protocol.Event{
		ID: turnID,
		Msg: protocol.NewExecApprovalRequest(
			request.CallID, turnID, request.Command, request.WorkingDirectory,
			request.Reason, request.ProposedPrefix, time.Now().UnixMilli(),
		),
	})
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case decision := <-result:
		return decision, nil
	}
}

func (s *Session) activeTurnID() string {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	if s.active == nil {
		return ""
	}
	return s.active.turnID
}

func (s *Session) handleExecApproval(submission Submission) {
	if submission.Op.Approval == nil {
		return
	}
	response := submission.Op.Approval
	s.approvalMu.Lock()
	pending, ok := s.approvals[response.CallID]
	s.approvalMu.Unlock()
	if !ok || pending.turnID != response.TurnID {
		return
	}
	select {
	case pending.result <- response.Decision:
	default:
	}
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
