//go:build darwin

package appserver

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

func chooseWorkspace(ctx context.Context) (string, bool, error) {
	command := exec.CommandContext(
		ctx, "/usr/bin/osascript", "-e",
		`POSIX path of (choose folder with prompt "选择新对话的工作区")`,
	)
	output, err := command.Output()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && strings.Contains(string(exitError.Stderr), "User canceled") {
			return "", true, nil
		}
		return "", false, fmt.Errorf("open workspace picker: %w", err)
	}
	return strings.TrimSpace(string(output)), false, nil
}
