package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

const (
	defaultOutputBytes = 40 << 10
	maxOutputBytes     = 160 << 10
)

var readOnlyCommands = []string{"file", "find", "head", "ls", "pwd", "rg", "sed", "stat", "tail", "wc"}

// ExecCommandExecutor launches commands through the Session process manager.
type ExecCommandExecutor struct {
	Manager *ProcessManager
}

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
				"tty":               map[string]any{"type": "boolean", "description": "Allocate a pseudo-terminal for interactive programs."},
			},
			"required":             []string{"cmd"},
			"additionalProperties": false,
		},
		Strict: false,
	}
}

// Execute resolves the turn environment, applies policy, and launches Seatbelt.
func (executor ExecCommandExecutor) Execute(ctx context.Context, invocation Invocation) (string, error) {
	var arguments struct {
		Command         string `json:"cmd"`
		WorkingDir      string `json:"workdir"`
		YieldTimeMS     int64  `json:"yield_time_ms"`
		MaxOutputTokens int    `json:"max_output_tokens"`
		TTY             bool   `json:"tty"`
	}
	if err := json.Unmarshal([]byte(invocation.Call.Arguments), &arguments); err != nil {
		return "", errors.New("arguments must be valid exec_command JSON")
	}
	workingDirectory, err := resolveWorkingDirectory(invocation.Environment, arguments.WorkingDir)
	if err != nil {
		return "", err
	}
	approved := false
	if reason := approvalReason(arguments.Command); reason != "" {
		if invocation.Reviewer == nil {
			return "", errors.New("command requires approval, but no approval reviewer is connected")
		}
		decision, err := invocation.Reviewer(ctx, ApprovalRequest{
			CallID:           invocation.Call.CallID,
			Command:          arguments.Command,
			WorkingDirectory: workingDirectory,
			Reason:           reason,
		})
		if err != nil {
			return "", err
		}
		if decision != ApprovalApproved {
			return "command execution denied by user", nil
		}
		approved = true
	}

	yield := clampYield(arguments.YieldTimeMS, false)
	outputLimit := defaultOutputBytes
	if arguments.MaxOutputTokens > 0 {
		outputLimit = min(arguments.MaxOutputTokens*4, maxOutputBytes)
	}

	command, err := sandboxedCommand(ctx, invocation.Environment.WorkspaceRoot, workingDirectory, arguments.Command, approved)
	if err != nil {
		return "", err
	}
	return executor.Manager.start(command, arguments.TTY, yield, outputLimit)
}

func approvalReason(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return "empty commands cannot be executed"
	}
	if strings.ContainsAny(command, ";|&><\n\r`") || strings.Contains(command, "$(") {
		return "shell operators are outside the automatic read-only policy"
	}
	fields := strings.Fields(command)
	if len(fields) == 0 || !slices.Contains(readOnlyCommands, filepath.Base(fields[0])) {
		return fmt.Sprintf("%q is not in the automatic read-only policy", fields[0])
	}
	for _, field := range fields[1:] {
		trimmed := strings.Trim(field, "'\"")
		if filepath.IsAbs(trimmed) || trimmed == ".." || strings.HasPrefix(trimmed, "../") || strings.Contains(trimmed, "/../") {
			return "the command references a path outside the workspace"
		}
	}
	return ""
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

func sandboxedCommand(ctx context.Context, workspaceRoot, workingDirectory, command string, approved bool) (*exec.Cmd, error) {
	if runtime.GOOS != "darwin" {
		return nil, errors.New("read-only exec sandbox is currently implemented for macOS only")
	}
	writeRule := ""
	if approved {
		writeRule = fmt.Sprintf("\n(allow file-write* (subpath %q))", workspaceRoot)
	}
	profile := fmt.Sprintf(`(version 1)
(deny default)
(import "system.sb")
(allow process-exec)
(allow process-fork)
(allow signal (target self))
(allow sysctl-read)
(allow file-read* (subpath "/System") (subpath "/usr") (subpath "/bin") (subpath "/sbin") (subpath "/Library") (subpath %q))%s`, workspaceRoot, writeRule)
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
