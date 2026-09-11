package tools

import (
	"fmt"
	"io"
	"os/exec"
)

// PTYAttach is the host PTY or ConPTY session attached to a sandboxed command.
type PTYAttach struct {
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	Closer io.Closer
	Wait   func() int
}

func AttachPTYForExecServer(command *exec.Cmd) (io.WriteCloser, io.ReadCloser, io.Closer, func() int, error) {
	attached, err := AttachPTY(command)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return attached.Stdin, attached.Stdout, attached.Closer, attached.Wait, nil
}

func startProcessPTY(command *exec.Cmd, process *managedProcess, outputDone chan struct{}) error {
	attached, err := AttachPTY(command)
	if err != nil {
		return err
	}
	process.stdin = attached.Stdin
	process.terminal = attached.Closer
	process.wait = attached.Wait
	go func() {
		_, _ = io.Copy(processOutputWriter{process: process, stream: "stdout"}, attached.Stdout)
		close(outputDone)
	}()
	return nil
}

func waitCommand(command *exec.Cmd) int {
	_ = command.Wait()
	if command.ProcessState != nil {
		return command.ProcessState.ExitCode()
	}
	return -1
}

func ptyWaitError(err error) error {
	return fmt.Errorf("start PTY command: %w", err)
}
