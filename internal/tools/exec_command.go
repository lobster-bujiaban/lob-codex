package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

const (
	defaultExecTimeout = 10 * time.Second
	maxExecTimeout     = 30 * time.Second
	defaultOutputBytes = 40 << 10
	maxOutputBytes     = 160 << 10
)

var readOnlyCommands = []string{"file", "find", "head", "ls", "pwd", "rg", "sed", "stat", "tail", "wc"}

// ExecCommandExecutor is the first local unified-exec slice. It only admits
// read-only commands and runs them in a read-only Seatbelt sandbox on macOS.
type ExecCommandExecutor struct{}

// Definition mirrors Codex's exec_command request shape for completed commands.
func (ExecCommandExecutor) Definition() Definition {
	return Definition{
		Type:        "function",
		Name:        "exec_command",
		Description: "Runs a read-only shell command in the current workspace and returns its output.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"cmd":               map[string]any{"type": "string", "description": "Shell command to execute."},
				"workdir":           map[string]any{"type": "string", "description": "Working directory relative to the workspace root."},
				"yield_time_ms":     map[string]any{"type": "number", "description": "Timeout for this learning-stage synchronous command."},
				"max_output_tokens": map[string]any{"type": "number", "description": "Approximate output token budget."},
			},
			"required":             []string{"cmd"},
			"additionalProperties": false,
		},
		Strict: false,
	}
}

// Execute resolves the turn environment, applies policy, and launches Seatbelt.
func (ExecCommandExecutor) Execute(ctx context.Context, invocation Invocation) (string, error) {
	var arguments struct {
		Command         string `json:"cmd"`
		WorkingDir      string `json:"workdir"`
		YieldTimeMS     int64  `json:"yield_time_ms"`
		MaxOutputTokens int    `json:"max_output_tokens"`
	}
	if err := json.Unmarshal([]byte(invocation.Call.Arguments), &arguments); err != nil {
		return "", errors.New("arguments must be valid exec_command JSON")
	}
	if err := validateReadOnlyCommand(arguments.Command); err != nil {
		return "", err
	}
	workingDirectory, err := resolveWorkingDirectory(invocation.Environment, arguments.WorkingDir)
	if err != nil {
		return "", err
	}

	timeout := defaultExecTimeout
	if arguments.YieldTimeMS > 0 {
		timeout = time.Duration(arguments.YieldTimeMS) * time.Millisecond
		if timeout > maxExecTimeout {
			timeout = maxExecTimeout
		}
	}
	outputLimit := defaultOutputBytes
	if arguments.MaxOutputTokens > 0 {
		outputLimit = min(arguments.MaxOutputTokens*4, maxOutputBytes)
	}

	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command, err := sandboxedCommand(commandContext, invocation.Environment.WorkspaceRoot, workingDirectory, arguments.Command)
	if err != nil {
		return "", err
	}
	startedAt := time.Now()
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	runErr := command.Run()
	wallTime := time.Since(startedAt).Seconds()
	exitCode := 0
	if runErr != nil {
		var exitError *exec.ExitError
		if errors.As(runErr, &exitError) {
			exitCode = exitError.ExitCode()
		} else if commandContext.Err() != nil {
			return "", fmt.Errorf("command timed out after %s", timeout)
		} else {
			return "", fmt.Errorf("run command: %w", runErr)
		}
	}
	text, truncated := truncateOutput(output.Bytes(), outputLimit)
	result := map[string]any{
		"exit_code":         exitCode,
		"wall_time_seconds": wallTime,
		"output":            text,
	}
	if truncated {
		result["output_truncated"] = true
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("encode command output: %w", err)
	}
	return string(encoded), nil
}

func validateReadOnlyCommand(command string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return errors.New("cmd must not be empty")
	}
	if strings.ContainsAny(command, ";|&><\n\r`") || strings.Contains(command, "$(") {
		return errors.New("command requires approval: shell operators are not allowed by the read-only policy")
	}
	fields := strings.Fields(command)
	if len(fields) == 0 || !slices.Contains(readOnlyCommands, filepath.Base(fields[0])) {
		return fmt.Errorf("command requires approval: %q is not in the read-only policy", fields[0])
	}
	for _, field := range fields[1:] {
		trimmed := strings.Trim(field, "'\"")
		if filepath.IsAbs(trimmed) || trimmed == ".." || strings.HasPrefix(trimmed, "../") || strings.Contains(trimmed, "/../") {
			return errors.New("command requires approval: paths must stay relative to the workspace")
		}
	}
	return nil
}

func resolveWorkingDirectory(environment Environment, requested string) (string, error) {
	workingDirectory := environment.WorkingDirectory
	if requested != "" {
		if filepath.IsAbs(requested) {
			return "", errors.New("workdir must be relative to the workspace root")
		}
		workingDirectory = filepath.Join(environment.WorkspaceRoot, requested)
	}
	workingDirectory, err := filepath.Abs(workingDirectory)
	if err != nil {
		return "", fmt.Errorf("resolve workdir: %w", err)
	}
	relative, err := filepath.Rel(environment.WorkspaceRoot, workingDirectory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("workdir escapes the workspace root")
	}
	return workingDirectory, nil
}

func sandboxedCommand(ctx context.Context, workspaceRoot, workingDirectory, command string) (*exec.Cmd, error) {
	if runtime.GOOS != "darwin" {
		return nil, errors.New("read-only exec sandbox is currently implemented for macOS only")
	}
	profile := fmt.Sprintf(`(version 1)
(deny default)
(import "system.sb")
(allow process-exec)
(allow process-fork)
(allow signal (target self))
(allow sysctl-read)
(allow file-read* (subpath "/System") (subpath "/usr") (subpath "/bin") (subpath "/sbin") (subpath "/Library") (subpath %q))`, workspaceRoot)
	cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", "-p", profile, "/bin/zsh", "-c", command)
	cmd.Dir = workingDirectory
	return cmd, nil
}

func truncateOutput(output []byte, limit int) (string, bool) {
	if len(output) <= limit {
		return string(output), false
	}
	return string(output[:limit]) + "\n…output truncated…", true
}
