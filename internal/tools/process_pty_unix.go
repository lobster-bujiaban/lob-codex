//go:build !windows

package tools

import (
	"os/exec"

	"github.com/creack/pty"
)

// AttachPTY starts the command on a 24×120 Unix PTY, matching the local unified-exec size.
func AttachPTY(command *exec.Cmd) (PTYAttach, error) {
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Rows: 24, Cols: 120})
	if err != nil {
		return PTYAttach{}, ptyWaitError(err)
	}
	return PTYAttach{Stdin: terminal, Stdout: terminal, Closer: terminal, Wait: func() int { return waitCommand(command) }}, nil
}
