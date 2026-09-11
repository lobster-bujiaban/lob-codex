package tools

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lobster-bujiaban/lob-codex/internal/execserver"
)

func TestExecCommandRemoteSendsUnwrappedArgv(t *testing.T) {
	var got execserver.ExecParams
	handler := execserver.NewHandler(func(_ context.Context, params execserver.ExecParams) (*exec.Cmd, error) {
		got = params
		return exec.Command("pwd"), nil
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	dir := t.TempDir()
	executor := ExecCommandExecutor{Manager: NewProcessManager(), Policy: NewExecPolicy(dir)}
	output, err := executor.Execute(context.Background(), Invocation{
		Call:        Call{CallID: "c1", Name: "exec_command", Arguments: `{"cmd":"pwd"}`},
		Environment: Environment{WorkingDirectory: dir, WorkspaceRoot: dir, ExecServer: server.URL},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got.Command != "pwd" {
		t.Fatalf("command = %q, want pwd", got.Command)
	}
	joined := strings.Join(got.Argv, " ")
	if strings.Contains(joined, "sandbox-exec") || strings.Contains(joined, "bwrap") {
		t.Fatalf("remote argv is host-wrapped: %q", got.Argv)
	}
	wantShell := "/bin/sh"
	if runtime.GOOS == "windows" {
		wantShell = "cmd.exe"
	}
	if len(got.Argv) < 3 || filepath.Base(got.Argv[0]) != filepath.Base(wantShell) {
		t.Fatalf("argv = %q, want native shell %s", got.Argv, wantShell)
	}
	if got.Sandbox == nil || !strings.EqualFold(got.Sandbox.WindowsSandboxLevel, execserver.WindowsSandboxRestrictedToken) {
		t.Fatalf("sandbox intent = %+v", got.Sandbox)
	}
	if len(got.Sandbox.WorkspaceRoots) != 1 || got.Sandbox.WorkspaceRoots[0] != dir {
		t.Fatalf("workspace roots = %q", got.Sandbox.WorkspaceRoots)
	}
	var result execResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("result = %s", output)
	}
}

func TestExecCommandRemoteForwardsTTY(t *testing.T) {
	var got execserver.ExecParams
	handler := execserver.NewHandler(func(_ context.Context, params execserver.ExecParams) (*exec.Cmd, error) {
		got = params
		return exec.Command("pwd"), nil
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	dir := t.TempDir()
	executor := ExecCommandExecutor{Manager: NewProcessManager(), Policy: NewExecPolicy(dir)}
	_, err := executor.Execute(context.Background(), Invocation{
		Call:        Call{CallID: "c-tty", Name: "exec_command", Arguments: `{"cmd":"pwd","tty":true}`},
		Environment: Environment{WorkingDirectory: dir, WorkspaceRoot: dir, ExecServer: server.URL},
	})
	if err == nil {
		t.Fatal("Execute() succeeded without exec-server StartPTY, want error")
	}
	if !got.TTY {
		t.Fatalf("remote exec params tty = false, want true")
	}
}

func TestNativeShellArgvIsUnwrapped(t *testing.T) {
	argv := NativeShellArgv("ls")
	joined := strings.Join(argv, " ")
	if strings.Contains(joined, "sandbox-exec") || strings.Contains(joined, "bwrap") {
		t.Fatalf("native argv wrapped: %q", argv)
	}
}

func TestExtraExecSearchDirectories(t *testing.T) {
	directories := extraExecSearchDirectories()
	if len(directories) == 0 {
		t.Fatal("extra exec search directories is empty")
	}
	joined := strings.Join(directories, "\n")
	if runtime.GOOS == "windows" {
		if !strings.Contains(joined, "Git") || !strings.Contains(joined, "scoop") {
			t.Fatalf("windows extras = %q", directories)
		}
		return
	}
	if !strings.Contains(joined, "/usr/local/bin") {
		t.Fatalf("unix extras = %q", directories)
	}
}
