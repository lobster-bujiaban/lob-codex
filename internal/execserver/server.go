package execserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"sync"
	"time"
)

// Spawner builds a host-sandboxed command from portable exec params.
type Spawner func(context.Context, ExecParams) (*exec.Cmd, error)

type serverProcess struct {
	command  *exec.Cmd
	stdin    io.WriteCloser
	closer   io.Closer
	wait     func() int
	output   []byte
	mu       sync.Mutex
	done     chan struct{}
	exitCode int
}

type PTYStarter func(*exec.Cmd) (stdin io.WriteCloser, stdout io.ReadCloser, closer io.Closer, wait func() int, err error)

// Handler serves the minimal exec-server JSON-RPC surface.
type Handler struct {
	spawner    Spawner
	AfterStart func(*exec.Cmd) error
	StartPTY   PTYStarter
	mu         sync.Mutex
	processes  map[string]*serverProcess
}

func NewHandler(spawner Spawner) *Handler {
	return &Handler{spawner: spawner, processes: make(map[string]*serverProcess)}
}

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var rpc rpcRequest
	if err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20)).Decode(&rpc); err != nil {
		writeRPCError(writer, nil, -32700, "invalid JSON request")
		return
	}
	params, _ := json.Marshal(rpc.Params)
	result, err := handler.dispatch(request.Context(), rpc.Method, params)
	if err != nil {
		writeRPCError(writer, rpc.ID, -32000, err.Error())
		return
	}
	writeRPCResult(writer, rpc.ID, result)
}

func (handler *Handler) dispatch(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case MethodProcessStart:
		var input ExecParams
		if err := json.Unmarshal(params, &input); err != nil {
			return nil, fmt.Errorf("invalid process/start params")
		}
		return nil, handler.start(ctx, input)
	case MethodProcessWrite:
		var input WriteParams
		if err := json.Unmarshal(params, &input); err != nil {
			return nil, fmt.Errorf("invalid process/write params")
		}
		return nil, handler.write(input)
	case MethodProcessPoll:
		var input PollParams
		if err := json.Unmarshal(params, &input); err != nil {
			return nil, fmt.Errorf("invalid process/poll params")
		}
		return handler.poll(input)
	case MethodProcessKill:
		var input KillParams
		if err := json.Unmarshal(params, &input); err != nil {
			return nil, fmt.Errorf("invalid process/kill params")
		}
		return nil, handler.kill(input.ProcessID)
	default:
		return nil, fmt.Errorf("unknown method %q", method)
	}
}

func (handler *Handler) start(ctx context.Context, params ExecParams) error {
	if params.ProcessID == "" {
		return fmt.Errorf("process_id is required")
	}
	command, err := handler.spawner(ctx, params)
	if err != nil {
		return err
	}
	if params.TTY {
		if handler.StartPTY == nil {
			return fmt.Errorf("PTY unified exec is unavailable on this exec-server; ConPTY is not implemented")
		}
		stdin, stdout, closer, wait, err := handler.StartPTY(command)
		if err != nil {
			return err
		}
		process := &serverProcess{command: command, stdin: stdin, closer: closer, wait: wait, done: make(chan struct{})}
		if handler.AfterStart != nil {
			if err := handler.AfterStart(command); err != nil {
				_ = closer.Close()
				if command.Process != nil {
					_ = command.Process.Kill()
				}
				return err
			}
		}
		if err := handler.register(params.ProcessID, process); err != nil {
			_ = closer.Close()
			if command.Process != nil {
				_ = command.Process.Kill()
			}
			return err
		}
		go func() {
			_, _ = io.Copy(serverOutput{process: process}, stdout)
		}()
		go func() {
			if process.wait != nil {
				process.exitCode = process.wait()
			}
			_ = closer.Close()
			close(process.done)
		}()
		return nil
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return fmt.Errorf("open remote stdin: %w", err)
	}
	process := &serverProcess{command: command, stdin: stdin, done: make(chan struct{})}
	command.Stdout = serverOutput{process: process}
	command.Stderr = serverOutput{process: process}
	if err := command.Start(); err != nil {
		return fmt.Errorf("start remote command: %w", err)
	}
	if handler.AfterStart != nil {
		if err := handler.AfterStart(command); err != nil {
			_ = command.Process.Kill()
			return err
		}
	}
	if err := handler.register(params.ProcessID, process); err != nil {
		_ = command.Process.Kill()
		return err
	}
	go func() {
		_ = command.Wait()
		if command.ProcessState != nil {
			process.exitCode = command.ProcessState.ExitCode()
		}
		close(process.done)
	}()
	return nil
}

func (handler *Handler) register(processID string, process *serverProcess) error {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if _, exists := handler.processes[processID]; exists {
		return fmt.Errorf("process_id %q is already running", processID)
	}
	handler.processes[processID] = process
	return nil
}

func (handler *Handler) write(input WriteParams) error {
	process, err := handler.process(input.ProcessID)
	if err != nil {
		return err
	}
	if input.Chars == "" {
		return nil
	}
	_, err = io.WriteString(process.stdin, input.Chars)
	return err
}

func (handler *Handler) poll(input PollParams) (PollResult, error) {
	process, err := handler.process(input.ProcessID)
	if err != nil {
		return PollResult{}, err
	}
	wait := time.Duration(input.WaitMS) * time.Millisecond
	if wait > 0 {
		select {
		case <-process.done:
		case <-time.After(wait):
		}
	}
	process.mu.Lock()
	output := string(process.output)
	process.output = nil
	process.mu.Unlock()
	result := PollResult{Output: output, Running: true}
	select {
	case <-process.done:
		code := process.exitCode
		result.Running = false
		result.ExitCode = &code
		handler.mu.Lock()
		delete(handler.processes, input.ProcessID)
		handler.mu.Unlock()
	default:
	}
	return result, nil
}

func (handler *Handler) kill(processID string) error {
	process, err := handler.process(processID)
	if err != nil {
		return err
	}
	if process.command != nil && process.command.Process != nil {
		_ = process.command.Process.Kill()
	}
	if process.closer != nil {
		_ = process.closer.Close()
	}
	return nil
}

func (handler *Handler) process(id string) (*serverProcess, error) {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	process := handler.processes[id]
	if process == nil {
		return nil, fmt.Errorf("unknown process_id %q", id)
	}
	return process, nil
}

type serverOutput struct{ process *serverProcess }

func (writer serverOutput) Write(data []byte) (int, error) {
	writer.process.mu.Lock()
	writer.process.output = append(writer.process.output, data...)
	writer.process.mu.Unlock()
	return len(data), nil
}

func writeRPCResult(writer http.ResponseWriter, id any, result any) {
	encoded, _ := json.Marshal(result)
	if result == nil {
		encoded = []byte("null")
	}
	writeJSON(writer, rpcResponse{JSONRPC: "2.0", ID: id, Result: encoded})
}

func writeRPCError(writer http.ResponseWriter, id any, code int, message string) {
	writeJSON(writer, rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}})
}

func writeJSON(writer http.ResponseWriter, payload rpcResponse) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(payload)
}
