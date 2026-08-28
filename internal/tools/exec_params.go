package tools

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/lobster-bujiaban/lob-codex/internal/execserver"
)

// CommandFromExecParams applies this host's sandbox to a remote-unwrapped command.
func CommandFromExecParams(ctx context.Context, params execserver.ExecParams) (*exec.Cmd, error) {
	command := params.Command
	if command == "" {
		command = shellCommandFromArgv(params.Argv)
	}
	if command == "" {
		return nil, fmt.Errorf("command is required")
	}
	policy := SandboxPolicy{WorkingDirectory: params.CWD}
	if params.Sandbox != nil {
		policy.WorkspaceWrite = params.Sandbox.WorkspaceWrite
		policy.NetworkAccess = params.Sandbox.NetworkAccess
		if params.Sandbox.CWD != "" {
			policy.WorkingDirectory = params.Sandbox.CWD
		}
		if len(params.Sandbox.WorkspaceRoots) > 0 {
			policy.WorkspaceRoot = params.Sandbox.WorkspaceRoots[0]
		}
	}
	if policy.WorkingDirectory == "" {
		policy.WorkingDirectory = policy.WorkspaceRoot
	}
	cmd, _, err := SandboxedCommand(ctx, policy, command)
	return cmd, err
}
