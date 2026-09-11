//go:build !windows

package tools

import "os/exec"

func finishSandboxStart(*exec.Cmd) error { return nil }
