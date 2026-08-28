//go:build !windows

package tools

import (
	"fmt"
	"io"
	"os/exec"

	"github.com/creack/pty"
)

func startProcessPTY(command *exec.Cmd, process *managedProcess, outputDone chan struct{}) error {
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 24, Cols: 120})
	if err != nil {
		return fmt.Errorf("start PTY command: %w", err)
	}
	process.stdin = terminal
	process.terminal = terminal
	go func() {
		_, _ = io.Copy(processOutputWriter{process: process, stream: "stdout"}, terminal)
		close(outputDone)
	}()
	return nil
}
