package tools

import (
	"bytes"
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

	"github.com/creack/pty"
	"github.com/lobster-bujiaban/lob-codex/internal/protocol"
)

var ansiEscapePattern = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

const (
	minYieldTime     = 250 * time.Millisecond
	maxYieldTime     = 30 * time.Second
	emptyPollMinimum = 5 * time.Second
)

type synchronizedBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (buffer *synchronizedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.data.Write(data)
}

func (buffer *synchronizedBuffer) sliceFrom(offset int) ([]byte, int) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	data := buffer.data.Bytes()
	if offset > len(data) {
		offset = len(data)
	}
	result := append([]byte(nil), data[offset:]...)
	return result, len(data)
}

type managedProcess struct {
	id         int
	command    *exec.Cmd
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
	readOffset    int
	exitCode      int
}

type processOutputWriter struct {
	process *managedProcess
	stream  string
}

func (writer processOutputWriter) Write(data []byte) (int, error) {
	written, err := writer.process.output.Write(data)
	if written > 0 && writer.process.emit != nil {
		writer.process.emit(protocol.NewExecCommandOutputDelta(
			writer.process.callID, writer.stream, data[:written],
		))
	}
	return written, err
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
		terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 24, Cols: 120})
		if err != nil {
			return "", fmt.Errorf("start PTY command: %w", err)
		}
		process.stdin = terminal
		process.terminal = terminal
		go func() {
			_, _ = io.Copy(processOutputWriter{process: process, stream: "stdout"}, terminal)
			close(outputDone)
		}()
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
	ChunkID         string  `json:"chunk_id"`
	SessionID       *int    `json:"session_id,omitempty"`
	ExitCode        *int    `json:"exit_code,omitempty"`
	WallTimeSeconds float64 `json:"wall_time_seconds"`
	Output          string  `json:"output"`
	OutputTruncated bool    `json:"output_truncated,omitempty"`
	TTY             bool    `json:"tty,omitempty"`
	PolicyRule      string  `json:"policy_rule,omitempty"`
}

func (process *managedProcess) result(outputLimit int, running bool) execResult {
	data, nextOffset := process.output.sliceFrom(process.readOffset)
	process.readOffset = nextOffset
	text, truncated := truncateOutput(data, outputLimit)
	if process.tty {
		text = ansiEscapePattern.ReplaceAllString(strings.ReplaceAll(text, "\r\n", "\n"), "")
	}
	result := execResult{
		ChunkID: generateChunkID(), WallTimeSeconds: time.Since(process.startedAt).Seconds(), Output: text,
		OutputTruncated: truncated, TTY: process.tty, PolicyRule: process.policyRule,
	}
	if running {
		result.SessionID = &process.id
	} else {
		result.ExitCode = &process.exitCode
	}
	return result
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
		_ = process.command.Process.Kill()
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
