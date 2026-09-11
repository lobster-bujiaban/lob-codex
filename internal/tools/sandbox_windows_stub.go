//go:build !windows

package tools

import (
	"context"
	"errors"
	"os/exec"
)

type windowsRestrictedTokenBackend struct{}

func (windowsRestrictedTokenBackend) Name() string { return "windows-restricted-token" }

func (windowsRestrictedTokenBackend) Command(context.Context, SandboxPolicy, string) (*exec.Cmd, error) {
	return nil, errors.New("windows sandbox is only available on Windows")
}
