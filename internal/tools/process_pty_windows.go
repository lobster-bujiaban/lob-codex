//go:build windows

package tools

import (
	"errors"
	"os/exec"
)

func startProcessPTY(*exec.Cmd, *managedProcess, chan struct{}) error {
	return errors.New("PTY unified exec is unavailable on Windows; ConPTY is not implemented")
}
