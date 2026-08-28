package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/lobster-bujiaban/lob-codex/internal/execserver"
	"github.com/lobster-bujiaban/lob-codex/internal/protocol"
)

var ansiEscapePattern = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

const (
	minYieldTime               = 250 * time.Millisecond
	maxYieldTime               = 30 * time.Second
	emptyPollMinimum           = 5 * time.Second
	unifiedExecOutputMaxBytes  = 1 << 20
	execOutputDeltaMaxBytes    = 8192
	maxExecOutputDeltasPerCall = 10_000
)

type synchronizedBuffer struct {
	mu   sync.Mutex
	data *headTailBuffer
}

func (buffer *synchronizedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.data == nil {
		buffer.data = newHeadTailBuffer(unifiedExecOutputMaxBytes)
	}
	buffer.data.pushChunk(data)
	return len(data), nil
}

func (buffer *synchronizedBuffer) drain() *headTailBuffer {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.data == nil {
		return newHeadTailBuffer(unifiedExecOutputMaxBytes)
	}
	result := buffer.data
	buffer.data = newHeadTailBuffer(unifiedExecOutputMaxBytes)
	return result
}

type managedProcess struct {
	id         int
	command    *exec.Cmd
	remote     *remoteProcess
	stdin      io.WriteCloser
	terminal   *os.File
	tty        bool
	policyRule string
	callID     string
	emit       EventEmitter
	output     synchronizedBuffer
	done       chan struct{}
	startedAt  time.Time

	interactionMu sync.Mutex
	deltaMu       sync.Mutex
	emittedDeltas int
	exitCode      int
}

type remoteProcess struct {
	client    *execserver.Client
	processID string
}

type processOutputWriter struct {
	process *managedProcess
	stream  string
}

func (writer processOutputWriter) Write(data []byte) (int, error) {
	written, err := writer.process.output.Write(data)
	if written > 0 {
		writer.process.emitOutputDeltas(writer.stream, data[:written])
	}
	return written, err
}

func (process *managedProcess) emitOutputDeltas(stream string, data []byte) {
	if process.emit == nil {
		return
	}
	process.deltaMu.Lock()
	defer process.deltaMu.Unlock()
	for len(data) > 0 && process.emittedDeltas < maxExecOutputDeltasPerCall {
		length := min(len(data), execOutputDeltaMaxBytes)
		process.emit(protocol.NewExecCommandOutputDelta(process.callID, stream, data[:length]))
		process.emittedDeltas++
		data = data[length:]
	}
}

// ProcessManager owns running unified-exec processes for one Session.
type ProcessManager struct {
	mu        sync.Mutex
	nextID    int
	processes map[int]*managedProcess
}

// NewProcessManager creates an empty process store.
func NewProcessManager() *ProcessManager {
	return &ProcessManager{nextID: 1000, processes: make(map[int]*managedProcess)}
}

func (manager *ProcessManager) start(
	command *exec.Cmd,
	callID string,
	tty bool,
	yield time.Duration,
	outputLimit int,
	policyRule string,
	emit EventEmitter,
) (string, error) {
	process := &managedProcess{
		command: command, done: make(chan struct{}), startedAt: time.Now(),
		tty: tty, policyRule: policyRule, callID: callID, emit: emit,
	}
	outputDone := make(chan struct{})
	if tty {
		if err := startProcessPTY(command, process, outputDone); err != nil {
			return "", err
		}
		if command.Process != nil {
			if err := finishSandboxStart(command); err != nil {
				_ = command.Process.Kill()
				return "", err
			}
		}
	} else {
		stdin, err := command.StdinPipe()
		if err != nil {
			return "", fmt.Errorf("open command stdin: %w", err)
		}
		process.stdin = stdin
		command.Stdout = processOutputWriter{process: process, stream: "stdout"}
		command.Stderr = processOutputWriter{process: process, stream: "stderr"}
		if err := command.Start(); err != nil {
			return "", fmt.Errorf("start command: %w", err)
		}
		if err := finishSandboxStart(command); err != nil {
			_ = command.Process.Kill()
			return "", err
		}
		close(outputDone)
	}
	manager.mu.Lock()
	process.id = manager.nextID
	manager.nextID++
	manager.processes[process.id] = process
	manager.mu.Unlock()
	go func() {
		_ = command.Wait()
		process.exitCode = command.ProcessState.ExitCode()
		if process.terminal != nil {
			_ = process.terminal.Close()
			<-outputDone
		}
		close(process.done)
	}()

	finished := waitForProcess(process.done, yield)
	result := process.result(outputLimit, !finished)
	if finished {
		manager.remove(process.id)
	}
	return encodeExecResult(result)
}

type RemoteExecRequest struct {
	Command          string
	WorkingDirectory string
	WorkspaceRoot    string
	CallID           string
	PolicyRule       string
	Policy           SandboxPolicy
	Yield            time.Duration
	OutputLimit      int
	Emit             EventEmitter
}

type remoteStdin struct {
	client    *execserver.Client
	processID string
}

func (stdin remoteStdin) Write(data []byte) (int, error) {
	if err := stdin.client.Write(context.Background(), stdin.processID, string(data)); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (remoteStdin) Close() error { return nil }

func (manager *ProcessManager) startRemote(ctx context.Context, baseURL string, request RemoteExecRequest) (string, error) {
	processID := fmt.Sprintf("%d-%s", time.Now().UnixNano(), generateChunkID())
	client := &execserver.Client{BaseURL: baseURL}
	params := execserver.ExecParams{
		ProcessID: processID,
		Command:   request.Command,
		Argv:      NativeShellArgv(request.Command),
		CWD:       request.WorkingDirectory,
		Sandbox: &execserver.SandboxIntent{
			WorkspaceWrite:        request.Policy.WorkspaceWrite,
			NetworkAccess:         request.Policy.NetworkAccess,
			CWD:                   request.WorkingDirectory,
			WorkspaceRoots:        []string{request.WorkspaceRoot},
			WindowsSandboxLevel:   execserver.WindowsSandboxRestrictedToken,
			EnforceManagedNetwork: !request.Policy.NetworkAccess,
		},
	}
	if err := client.Start(ctx, params); err != nil {
		return "", err
	}
	process := &managedProcess{
		remote:     &remoteProcess{client: client, processID: processID},
		stdin:      remoteStdin{client: client, processID: processID},
		done:       make(chan struct{}),
		startedAt:  time.Now(),
		policyRule: request.PolicyRule + " · remote-exec-server",
		callID:     request.CallID,
		emit:       request.Emit,
	}
	manager.mu.Lock()
	process.id = manager.nextID
	manager.nextID++
	manager.processes[process.id] = process
	manager.mu.Unlock()
	go manager.pollRemote(process)
	finished := waitForProcess(process.done, request.Yield)
	result := process.result(request.OutputLimit, !finished)
	if finished {
		manager.remove(process.id)
	}
	return encodeExecResult(result)
}

func (manager *ProcessManager) pollRemote(process *managedProcess) {
	defer close(process.done)
	for {
		polled, err := process.remote.client.Poll(context.Background(), process.remote.processID, 250*time.Millisecond)
		if err != nil {
			process.exitCode = -1
			_, _ = process.output.Write([]byte(err.Error()))
			return
		}
		if polled.Output != "" {
			_, _ = processOutputWriter{process: process, stream: "stdout"}.Write([]byte(polled.Output))
		}
		if !polled.Running {
			if polled.ExitCode != nil {
				process.exitCode = *polled.ExitCode
			}
			return
		}
	}
}

func (manager *ProcessManager) writeStdin(
	ctx context.Context,
	sessionID int,
	chars string,
	yield time.Duration,
	outputLimit int,
	emit EventEmitter,
) (string, error) {
	manager.mu.Lock()
	process := manager.processes[sessionID]
	manager.mu.Unlock()
	if process == nil {
		return "", fmt.Errorf("unknown session_id %d", sessionID)
	}
	process.interactionMu.Lock()
	defer process.interactionMu.Unlock()

	if chars != "" {
		if chars == "\x03" {
			if process.tty {
				if _, err := process.stdin.Write([]byte{3}); err != nil {
					return "", fmt.Errorf("interrupt PTY process %d: %w", sessionID, err)
				}
			} else if process.remote != nil {
				if err := process.remote.client.Kill(ctx, process.remote.processID); err != nil {
					return "", fmt.Errorf("interrupt remote process %d: %w", sessionID, err)
				}
			} else if err := process.command.Process.Signal(os.Interrupt); err != nil {
				return "", fmt.Errorf("interrupt process %d: %w", sessionID, err)
			}
		} else if _, err := io.WriteString(process.stdin, chars); err != nil {
			return "", fmt.Errorf("write process %d stdin: %w", sessionID, err)
		}
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-process.done:
	case <-time.After(yield):
	}
	finished := isClosed(process.done)
	result := process.result(outputLimit, !finished)
	if emit != nil && (chars != "" || !finished) {
		emit(protocol.NewTerminalInteraction(
			process.callID, fmt.Sprint(sessionID), chars,
		))
	}
	if finished {
		manager.remove(sessionID)
	}
	return encodeExecResult(result)
}

type execResult struct {
	ChunkID            string  `json:"chunk_id"`
	SessionID          *int    `json:"session_id,omitempty"`
	ExitCode           *int    `json:"exit_code,omitempty"`
	WallTimeSeconds    float64 `json:"wall_time_seconds"`
	Output             string  `json:"output"`
	OriginalTokenCount int     `json:"original_token_count"`
	OutputOmittedBytes int     `json:"output_omitted_bytes,omitempty"`
	TTY                bool    `json:"tty,omitempty"`
	PolicyRule         string  `json:"policy_rule,omitempty"`
}

func (process *managedProcess) result(outputLimit int, running bool) execResult {
	collected := process.output.drain().withLimit(outputLimit)
	text := string(collected.bytesWithOmissionMarker())
	if process.tty {
		text = ansiEscapePattern.ReplaceAllString(strings.ReplaceAll(text, "\r\n", "\n"), "")
	}
	result := execResult{
		ChunkID: generateChunkID(), WallTimeSeconds: time.Since(process.startedAt).Seconds(), Output: text,
		OriginalTokenCount: approxTokensFromByteCount(collected.totalBytes()),
		OutputOmittedBytes: collected.omittedBytes, TTY: process.tty, PolicyRule: process.policyRule,
	}
	if running {
		result.SessionID = &process.id
	} else {
		result.ExitCode = &process.exitCode
	}
	return result
}

func approxTokensFromByteCount(bytes int) int {
	return (bytes + 3) / 4
}

func generateChunkID() string {
	var value [4]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("chunk_%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}

func encodeExecResult(result execResult) (string, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("encode command output: %w", err)
	}
	return string(encoded), nil
}

func (manager *ProcessManager) remove(id int) {
	manager.mu.Lock()
	delete(manager.processes, id)
	manager.mu.Unlock()
}

// Close interrupts all processes still owned by the Session.
func (manager *ProcessManager) Close() {
	manager.mu.Lock()
	processes := make([]*managedProcess, 0, len(manager.processes))
	for _, process := range manager.processes {
		processes = append(processes, process)
	}
	manager.processes = make(map[int]*managedProcess)
	manager.mu.Unlock()
	for _, process := range processes {
		if process.remote != nil {
			_ = process.remote.client.Kill(context.Background(), process.remote.processID)
			continue
		}
		if process.command != nil && process.command.Process != nil {
			_ = process.command.Process.Kill()
		}
		if process.terminal != nil {
			_ = process.terminal.Close()
		}
	}
}

func waitForProcess(done <-chan struct{}, yield time.Duration) bool {
	select {
	case <-done:
		return true
	case <-time.After(yield):
		return false
	}
}

func isClosed(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func clampYield(milliseconds int64, emptyPoll bool) time.Duration {
	yield := time.Duration(milliseconds) * time.Millisecond
	if yield <= 0 {
		if emptyPoll {
			return emptyPollMinimum
		}
		return 10 * time.Second
	}
	minimum := minYieldTime
	if emptyPoll {
		minimum = emptyPollMinimum
	}
	if yield < minimum {
		return minimum
	}
	if yield > maxYieldTime {
		return maxYieldTime
	}
	return yield
}
