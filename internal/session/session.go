// Package session implements the Codex submission, task, turn, and sampling lifecycle.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/lobster-bujiaban/lob-codex/internal/extensions"
	"github.com/lobster-bujiaban/lob-codex/internal/mcp"
	"github.com/lobster-bujiaban/lob-codex/internal/model"
	"github.com/lobster-bujiaban/lob-codex/internal/protocol"
	"github.com/lobster-bujiaban/lob-codex/internal/tools"
)

// Session owns the model client, active task, and public event stream.
type Session struct {
	client        model.Client
	events        chan protocol.Event
	ctx           context.Context
	cancel        context.CancelFunc
	history       ConversationHistory
	rollout       *rolloutRecorder
	tools         *tools.Router
	workspaceRoot string
	extensionRoot string
	extensions    extensions.Catalog
	mcpClients    []*mcp.Client
	extensionMu   sync.RWMutex
	mcpStatuses   map[string]MCPServerStatus

	approvalMu sync.Mutex
	approvals  map[string]pendingApproval

	elicitationMu sync.Mutex
	elicitations  map[string]pendingElicitation

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

	mu             sync.Mutex
	abortReason    string
	pendingInput   []TurnInput
	acceptingInput bool
}

type pendingApproval struct {
	turnID string
	result chan tools.ApprovalDecision
}

type pendingElicitation struct {
	result chan ElicitationResponse
}

type MCPServerStatus struct {
	Name       string     `json:"name"`
	State      string     `json:"state"`
	SourcePath string     `json:"source_path,omitempty"`
	Tools      []mcp.Tool `json:"tools,omitempty"`
	Error      string     `json:"error,omitempty"`
	NeedsAuth  bool       `json:"needs_auth,omitempty"`
}
type ExtensionInventory struct {
	ExtensionRoot string               `json:"extension_root"`
	WorkspaceRoot string               `json:"workspace_root"`
	Skills        []extensions.Skill   `json:"skills"`
	Plugins       []extensions.Plugin  `json:"plugins"`
	MCPServers    []MCPServerStatus    `json:"mcp_servers"`
	Hooks         []extensions.Hook    `json:"hooks,omitempty"`
	Apps          []extensions.App     `json:"apps,omitempty"`
	Commands      []extensions.Command `json:"commands,omitempty"`
	Agents        []extensions.Agent   `json:"agents,omitempty"`
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
	return NewInWorkspaceWithRollout(client, workspaceRoot, "")
}

// NewInWorkspaceWithRollout creates or resumes a Session from one canonical rollout.
func NewInWorkspaceWithRollout(client model.Client, workspaceRoot, rolloutPath string) (*Session, *IO, error) {
	return NewInWorkspaceWithRolloutAndExtensions(client, workspaceRoot, rolloutPath, workspaceRoot)
}

// NewInWorkspaceWithRolloutAndExtensions separates the thread tool workspace
// from the application-owned extension root.
func NewInWorkspaceWithRolloutAndExtensions(client model.Client, workspaceRoot, rolloutPath, extensionRoot string) (*Session, *IO, error) {
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
	extensionRoot, err = filepath.Abs(extensionRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve extension root: %w", err)
	}
	extensionRoot, err = filepath.EvalSymlinks(extensionRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("canonicalize extension root: %w", err)
	}
	info, err = os.Stat(extensionRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("open extension root: %w", err)
	}
	if !info.IsDir() {
		return nil, nil, errors.New("extension root must be a directory")
	}
	ctx, cancel := context.WithCancel(context.Background())
	submissions := make(chan Submission)
	events := make(chan protocol.Event, 128)
	done := make(chan struct{})
	environment := tools.Environment{
		WorkingDirectory: workspaceRoot, WorkspaceRoot: workspaceRoot,
		ExecServer: strings.TrimSpace(os.Getenv("LOB_CODEX_EXEC_SERVER")),
	}
	sess := &Session{
		client: client, events: events, ctx: ctx, cancel: cancel,
		tools: tools.NewDefaultRouter(environment), approvals: make(map[string]pendingApproval),
		elicitations:  make(map[string]pendingElicitation),
		workspaceRoot: workspaceRoot, extensionRoot: extensionRoot, mcpStatuses: map[string]MCPServerStatus{},
	}
	catalog, err := extensions.Load(extensionRoot)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("load extensions: %w", err)
	}
	sess.extensions = catalog
	sess.connectMCPServers(catalog.MCPServers)
	sess.runHooks("sessionStart")
	sess.runHooks("session_start")
	if rolloutPath != "" {
		recorder, initialHistory, err := openRollout(rolloutPath)
		if err != nil {
			cancel()
			return nil, nil, err
		}
		sess.rollout = recorder
		sess.rollout.recordSessionMeta(workspaceRoot)
		sess.history.Restore(initialHistory)
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
	return io.SubmitTurnInputWithImages(ctx, text, nil)
}

// SubmitTurnInputWithImages starts a turn containing text and clipboard image data URLs.
func (io *IO) SubmitTurnInputWithImages(ctx context.Context, text string, imageURLs []string) (string, error) {
	return io.Submit(ctx, Op{Type: OpTurnInput, Input: []TurnInput{{Text: text, ImageURLs: imageURLs}}})
}

// Steer queues input only when expectedTurnID is still the active turn.
func (io *IO) Steer(ctx context.Context, expectedTurnID, text string) (string, error) {
	reply := make(chan TurnInputAdmission, 1)
	_, err := io.Submit(ctx, Op{
		Type: OpTurnInput, Input: []TurnInput{{Text: text}},
		ExpectedTurnID: expectedTurnID, AdmissionReply: reply,
	})
	if err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-io.done:
		return "", errors.New("session stopped")
	case admission := <-reply:
		return admission.TurnID, admission.Err
	}
}

// RespondExecApproval delivers a client decision through the submission loop.
func (io *IO) RespondExecApproval(ctx context.Context, response ExecApprovalResponse) error {
	_, err := io.Submit(ctx, Op{Type: OpExecApproval, Approval: &response})
	return err
}

func (io *IO) RespondElicitation(ctx context.Context, response ElicitationResponse) error {
	_, err := io.Submit(ctx, Op{Type: OpElicitation, Elicitation: &response})
	return err
}

// Interrupt cancels the active turn through the Session submission loop.
func (io *IO) Interrupt(ctx context.Context) error {
	_, err := io.Submit(ctx, Op{Type: OpInterrupt})
	return err
}

func (io *IO) InterruptTurn(ctx context.Context, expectedTurnID string) error {
	reply := make(chan error, 1)
	if _, err := io.Submit(ctx, Op{Type: OpInterrupt, ExpectedTurnID: expectedTurnID, ResultReply: reply}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-reply:
		return err
	}
}

func (io *IO) RefreshExtensions(ctx context.Context) error {
	reply := make(chan error, 1)
	if _, err := io.Submit(ctx, Op{Type: OpRefreshExtensions, ResultReply: reply}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-reply:
		return err
	}
}

func (io *IO) CleanBackgroundTerminals(ctx context.Context) error {
	reply := make(chan error, 1)
	if _, err := io.Submit(ctx, Op{Type: OpCleanBackgroundTerminals, ResultReply: reply}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-reply:
		return err
	}
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
	defer func() {
		for _, client := range s.mcpClients {
			client.Close()
		}
	}()
	defer s.rollout.close()

	for submission := range submissions {
		switch submission.Op.Type {
		case OpTurnInput:
			s.handleTurnInput(submission)
		case OpExecApproval:
			s.handleExecApproval(submission)
		case OpElicitation:
			s.handleElicitation(submission)
		case OpInterrupt:
			var err error
			if submission.Op.ExpectedTurnID != "" && s.activeTurnID() != submission.Op.ExpectedTurnID {
				err = fmt.Errorf("active turn is %q, expected %q", s.activeTurnID(), submission.Op.ExpectedTurnID)
			} else {
				s.abortActive("interrupted")
			}
			if submission.Op.ResultReply != nil {
				submission.Op.ResultReply <- err
			}
		case OpRefreshExtensions:
			err := s.refreshExtensions()
			if submission.Op.ResultReply != nil {
				submission.Op.ResultReply <- err
			}
		case OpCleanBackgroundTerminals:
			s.tools.CleanBackgroundProcesses()
			if submission.Op.ResultReply != nil {
				submission.Op.ResultReply <- nil
			}
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

func (s *Session) refreshExtensions() error {
	s.activeMu.Lock()
	active := s.active
	s.activeMu.Unlock()
	if active != nil {
		return errors.New("cannot refresh extensions while a turn is active")
	}
	catalog, err := extensions.Load(s.extensionRoot)
	if err != nil {
		return err
	}
	for _, client := range s.mcpClients {
		client.Close()
	}
	s.mcpClients = nil
	s.tools.UnregisterPrefix("mcp__")
	s.extensionMu.Lock()
	s.mcpStatuses = map[string]MCPServerStatus{}
	s.extensions = catalog
	s.extensionMu.Unlock()
	s.connectMCPServers(catalog.MCPServers)
	s.runHooks("sessionStart")
	s.runHooks("session_start")
	return nil
}

func (s *Session) connectMCPServers(configs []extensions.MCPServer) {
	for _, config := range configs {
		status := MCPServerStatus{Name: config.Name, State: "starting", SourcePath: config.SourcePath}
		s.setMCPStatus(status)
		client, err := mcp.Start(s.ctx, config, s.extensionRoot)
		if err != nil {
			status.State = "failed"
			status.Error = err.Error()
			if errors.Is(err, mcp.ErrOAuthRequired) {
				status.State = "oauth_required"
				status.NeedsAuth = true
			}
			s.setMCPStatus(status)
			continue
		}
		listContext, cancel := context.WithTimeout(s.ctx, config.StartupTimeout)
		listed, err := client.ListTools(listContext)
		cancel()
		if err != nil {
			client.Close()
			status.State = "failed"
			status.Error = err.Error()
			s.setMCPStatus(status)
			continue
		}
		registered := true
		for _, remoteTool := range listed {
			if err := s.tools.Register(mcp.Executor{Client: client, Server: config.Name, Tool: remoteTool}); err != nil {
				status.Error = err.Error()
				registered = false
				break
			}
		}
		if !registered {
			client.Close()
			status.State = "failed"
			s.setMCPStatus(status)
			continue
		}
		status.State = "ready"
		status.Tools = listed
		s.setMCPStatus(status)
		s.mcpClients = append(s.mcpClients, client)
		go s.watchMCPNotifications(config, client)
	}
}

func (s *Session) watchMCPNotifications(config extensions.MCPServer, client *mcp.Client) {
	for {
		select {
		case <-s.ctx.Done():
			return
		case request, ok := <-client.Requests():
			if !ok {
				return
			}
			s.handleMCPRequest(config, client, request)
		case notification, ok := <-client.Notifications():
			if !ok {
				return
			}
			if notification.Method != "notifications/tools/list_changed" {
				continue
			}
			s.activeMu.Lock()
			active := s.active
			s.activeMu.Unlock()
			if active != nil {
				s.extensionMu.Lock()
				status := s.mcpStatuses[config.Name]
				status.State = "stale"
				s.mcpStatuses[config.Name] = status
				s.extensionMu.Unlock()
				continue
			}
			ctx, cancel := context.WithTimeout(s.ctx, config.StartupTimeout)
			listed, err := client.ListTools(ctx)
			cancel()
			if err != nil {
				status := s.mcpStatuses[config.Name]
				status.State = "failed"
				status.Error = err.Error()
				if errors.Is(err, mcp.ErrOAuthRequired) {
					status.State = "oauth_required"
					status.NeedsAuth = true
				}
				s.setMCPStatus(status)
				continue
			}
			s.tools.UnregisterPrefix("mcp__" + config.Name + "__")
			for _, remoteTool := range listed {
				_ = s.tools.Register(mcp.Executor{Client: client, Server: config.Name, Tool: remoteTool})
			}
			status := s.mcpStatuses[config.Name]
			status.State = "ready"
			status.Tools = listed
			status.Error = ""
			s.setMCPStatus(status)
		}
	}
}

func (s *Session) handleMCPRequest(config extensions.MCPServer, client *mcp.Client, request mcp.Request) {
	if request.Method != "elicitation/create" {
		_ = client.Respond(request.ID, nil, fmt.Errorf("unsupported MCP request %s", request.Method))
		return
	}
	var params struct {
		Message         string         `json:"message"`
		RequestedSchema map[string]any `json:"requestedSchema"`
	}
	_ = json.Unmarshal(request.Params, &params)
	response, err := s.requestElicitation(config.Name, params.Message, params.RequestedSchema)
	if err != nil {
		_ = client.Respond(request.ID, nil, err)
		return
	}
	if response.Action == "accept" {
		content := response.Content
		if content == nil {
			content = map[string]any{}
		}
		_ = client.Respond(request.ID, map[string]any{"action": "accept", "content": content}, nil)
		return
	}
	_ = client.Respond(request.ID, map[string]any{"action": "decline"}, nil)
}

func (s *Session) requestElicitation(server, message string, schema map[string]any) (ElicitationResponse, error) {
	id := fmt.Sprintf("mcp_elicitation:%s:%d", server, time.Now().UnixNano())
	result := make(chan ElicitationResponse, 1)
	s.elicitationMu.Lock()
	s.elicitations[id] = pendingElicitation{result: result}
	s.elicitationMu.Unlock()
	defer func() {
		s.elicitationMu.Lock()
		delete(s.elicitations, id)
		s.elicitationMu.Unlock()
	}()
	fields := mcp.SchemaFields(schema)
	protocolFields := make([]protocol.McpElicitationField, 0, len(fields))
	for _, field := range fields {
		protocolFields = append(protocolFields, protocol.McpElicitationField(field))
	}
	s.sendEventRaw(protocol.Event{
		ID:  s.activeTurnID(),
		Msg: protocol.NewMcpElicitationRequest(id, server, message, protocolFields, schema),
	})
	select {
	case <-s.ctx.Done():
		return ElicitationResponse{}, s.ctx.Err()
	case response := <-result:
		return response, nil
	}
}

func (s *Session) handleElicitation(submission Submission) {
	if submission.Op.Elicitation == nil {
		return
	}
	response := submission.Op.Elicitation
	s.elicitationMu.Lock()
	pending, ok := s.elicitations[response.ElicitationID]
	s.elicitationMu.Unlock()
	if !ok {
		return
	}
	select {
	case pending.result <- *response:
	default:
	}
}

func (s *Session) runHooks(event string) {
	for _, hook := range s.extensions.Hooks() {
		if !strings.EqualFold(hook.Event, event) || hook.Type != "command" || strings.TrimSpace(hook.Command) == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		shell, args := []string{"sh", "-c"}, []string{hook.Command}
		if runtime.GOOS == "windows" {
			shell, args = []string{"cmd", "/C"}, []string{hook.Command}
		}
		cmd := exec.CommandContext(ctx, shell[0], append(shell[1:], args...)...)
		cmd.Dir = s.workspaceRoot
		_ = cmd.Run()
		cancel()
	}
}
func (s *Session) setMCPStatus(status MCPServerStatus) {
	s.extensionMu.Lock()
	s.mcpStatuses[status.Name] = status
	s.extensionMu.Unlock()
}
func (s *Session) ExtensionInventory() ExtensionInventory {
	s.extensionMu.RLock()
	defer s.extensionMu.RUnlock()
	inventory := ExtensionInventory{
		ExtensionRoot: s.extensionRoot, WorkspaceRoot: s.workspaceRoot,
		Plugins: append([]extensions.Plugin(nil), s.extensions.Plugins...),
	}
	for _, plugin := range s.extensions.Plugins {
		if !plugin.Enabled {
			continue
		}
		inventory.Hooks = append(inventory.Hooks, plugin.Hooks...)
		inventory.Apps = append(inventory.Apps, plugin.Apps...)
		inventory.Commands = append(inventory.Commands, plugin.Commands...)
		inventory.Agents = append(inventory.Agents, plugin.Agents...)
	}
	for _, skill := range s.extensions.Skills {
		skill.Instructions = ""
		inventory.Skills = append(inventory.Skills, skill)
	}
	for _, status := range s.mcpStatuses {
		inventory.MCPServers = append(inventory.MCPServers, status)
	}
	return inventory
}

func (s *Session) recordConversationItems(items ...protocol.ResponseItem) {
	s.history.RecordItems(items...)
	s.rollout.recordResponseItems(items...)
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

func (s *Session) ActiveTurnID() string { return s.activeTurnID() }

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
	reply := func(admission TurnInputAdmission) {
		if submission.Op.AdmissionReply != nil {
			submission.Op.AdmissionReply <- admission
		}
	}
	if len(submission.Op.Input) != 1 || ValidateTurnInput(submission.Op.Input[0]) != nil {
		s.sendEventRaw(protocol.Event{ID: submission.ID, Msg: protocol.NewError("prompt or image is required")})
		reply(TurnInputAdmission{Err: errors.New("prompt or image is required")})
		return
	}
	s.activeMu.Lock()
	active := s.active
	s.activeMu.Unlock()
	if active != nil {
		if submission.Op.ExpectedTurnID != "" && submission.Op.ExpectedTurnID != active.turnID {
			reply(TurnInputAdmission{Err: fmt.Errorf("active turn is %q, expected %q", active.turnID, submission.Op.ExpectedTurnID)})
			return
		}
		if !active.enqueueInput(submission.Op.Input) {
			reply(TurnInputAdmission{Err: errors.New("active turn is completing and no longer accepts steer input")})
			return
		}
		reply(TurnInputAdmission{TurnID: active.turnID, Mode: "steered"})
		return
	}
	if submission.Op.ExpectedTurnID != "" {
		reply(TurnInputAdmission{Err: errors.New("no active turn to steer")})
		return
	}
	turnContext := &TurnContext{SubID: submission.ID}
	s.spawnRegularTask(turnContext, submission.Op.Input)
	reply(TurnInputAdmission{TurnID: submission.ID, Mode: "started"})
}

// ValidateInput rejects input that cannot start or steer a Codex turn.
func ValidateInput(input string) error {
	if strings.TrimSpace(input) == "" {
		return errors.New("prompt must not be empty")
	}
	return nil
}

// ValidateTurnInput accepts an image-only turn while retaining text validation.
func ValidateTurnInput(input TurnInput) error {
	if strings.TrimSpace(input.Text) == "" && len(input.ImageURLs) == 0 {
		return errors.New("prompt or image is required")
	}
	return nil
}

func (s *Session) sendEvent(turnContext *TurnContext, message protocol.EventMsg) {
	s.rollout.recordEvent(message)
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

func (task *runningTask) enqueueInput(input []TurnInput) bool {
	task.mu.Lock()
	defer task.mu.Unlock()
	if !task.acceptingInput {
		return false
	}
	task.pendingInput = append(task.pendingInput, input...)
	return true
}

func (task *runningTask) takePendingInput(keepOpen bool) []TurnInput {
	task.mu.Lock()
	defer task.mu.Unlock()
	input := append([]TurnInput(nil), task.pendingInput...)
	task.pendingInput = nil
	if len(input) == 0 && !keepOpen {
		task.acceptingInput = false
	}
	return input
}
